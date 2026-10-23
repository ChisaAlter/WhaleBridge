package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// SearchKeyQuota describes a read of one Tavily key. Unknown is explicit:
// missing or malformed counters never become a successful zero-use read.
type SearchKeyQuota struct {
	Key       string    `json:"key"`
	Known     bool      `json:"known"`
	Unlimited bool      `json:"unlimited,omitempty"`
	Used      int64     `json:"used"`
	Limit     int64     `json:"limit"`
	Of        string    `json:"of,omitempty"`
	Plan      string    `json:"plan,omitempty"`
	PaygoLeft int64     `json:"paygoLeft,omitempty"`
	UsedUp    bool      `json:"usedUp,omitempty"`
	Err       string    `json:"err,omitempty"`
	Resting   string    `json:"resting,omitempty"`
	RestUntil time.Time `json:"restUntil,omitzero"`
}

func readTavilyUsage(b []byte) (SearchKeyQuota, error) {
	var u struct{ Key, Account map[string]json.RawMessage }
	if err := json.Unmarshal(b, &u); err != nil {
		return SearchKeyQuota{}, err
	}
	if u.Key == nil {
		return SearchKeyQuota{}, errors.New("no key in the reply")
	}
	// Counts must be nonnegative integer JSON numbers, or an explicit null.
	num := func(o map[string]json.RawMessage, field string) (int64, bool, bool) {
		v, ok := o[field]
		if !ok {
			return 0, false, false
		}
		if strings.TrimSpace(string(v)) == "null" {
			return 0, true, true
		}
		var n int64
		if json.Unmarshal(v, &n) != nil || n < 0 {
			return 0, false, false
		}
		return n, false, true
	}
	var q SearchKeyQuota
	_ = json.Unmarshal(u.Account["current_plan"], &q.Plan)
	if limit, null, ok := num(u.Account, "paygo_limit"); ok && !null {
		if used, null, ok := num(u.Account, "paygo_usage"); ok && !null {
			q.PaygoLeft = max(0, limit-used)
		}
	}
	used, usedNull, usedOK := num(u.Key, "usage")
	limit, limitNull, ok := num(u.Key, "limit")
	if !ok {
		return SearchKeyQuota{}, errors.New("no valid key limit in the reply")
	}
	switch plan, planNull, planOK := num(u.Account, "plan_limit"); {
	case !limitNull:
		if !usedOK || usedNull {
			return SearchKeyQuota{}, errors.New("no valid key usage in the reply")
		}
		q.Used, q.Limit, q.Of = used, limit, "key"
	case planOK && !planNull:
		planUsed, null, ok := num(u.Account, "plan_usage")
		if !ok || null {
			return SearchKeyQuota{}, errors.New("no valid plan usage in the reply")
		}
		q.Used, q.Limit, q.Of = planUsed, plan, "plan"
	case planOK && planNull:
		if !usedOK || usedNull {
			return SearchKeyQuota{}, errors.New("no valid key usage in the reply")
		}
		q.Unlimited, q.Used = true, used
	default:
		return SearchKeyQuota{}, errors.New("no valid plan limit in the reply")
	}
	q.Known = true
	q.UsedUp = !q.Unlimited && q.Used >= q.Limit && (q.Of == "key" || q.PaygoLeft == 0)
	return q, nil
}

const searchQuotaKeep = 2 * time.Minute

type keptQuota struct {
	at time.Time
	q  SearchKeyQuota
}

var searchQuotas sync.Map
var quotaClient = &http.Client{Transport: upstreamTransport()}

func CanReadSearchQuota(a provider.SearchAPI) bool { return a.Vendor == "tavily" && len(a.Keys()) > 0 }

// SearchQuotas reads only when explicitly called. A recent read is reused
// unless fresh is set; no background request is made by the settings state.
func SearchQuotas(ctx context.Context, a provider.SearchAPI, fresh bool) []SearchKeyQuota {
	if !CanReadSearchQuota(a) {
		return nil
	}
	ctx = netproxy.With(ctx, "")
	keys := a.Keys()
	out := make([]SearchKeyQuota, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		wg.Go(func() { out[i] = searchQuota(ctx, a, key, fresh) })
	}
	wg.Wait()
	for i, key := range keys {
		out[i].Plan = maskSearchKeys(out[i].Plan, keys)
		out[i].Err = maskSearchKeys(out[i].Err, keys)
		if r, ok := searchKeyRest(a.Vendor, key, time.Now()); ok {
			out[i].Resting, out[i].RestUntil = r.Why, r.Until
		}
	}
	return out
}

func searchQuota(ctx context.Context, a provider.SearchAPI, key string, fresh bool) SearchKeyQuota {
	id := searchKeyID(a.Vendor+" "+a.Base(), key)
	if v, ok := searchQuotas.Load(id); ok && !fresh && time.Since(v.(keptQuota).at) < searchQuotaKeep {
		return v.(keptQuota).q
	}
	q, err := askTavilyUsage(ctx, a, key)
	if err != nil {
		q = SearchKeyQuota{Err: maskSearchKeys(err.Error(), a.Keys())}
	}
	q.Key = provider.MaskSearchKey(key)
	q.Plan = maskSearchKeys(q.Plan, a.Keys())
	searchQuotas.Store(id, keptQuota{time.Now(), q})
	switch {
	case q.UsedUp:
		restSearchKey(a.Vendor, key, outOfCreditRest, "used up", time.Now())
	case q.Known:
		if r, ok := searchKeyRest(a.Vendor, key, time.Now()); ok && (r.Why == "used up" || r.Why == "out of credit") {
			wakeSearchKey(a.Vendor, key)
		}
	}
	return q
}

func askTavilyUsage(ctx context.Context, a provider.SearchAPI, key string) (SearchKeyQuota, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Base()+"/usage", nil)
	if err != nil {
		return SearchKeyQuota{}, err
	}
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("User-Agent", SearchAgent)
	c := *quotaClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return SearchKeyQuota{}, errors.New("no answer in time")
		}
		return SearchKeyQuota{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return SearchKeyQuota{}, err
	}
	if res.StatusCode != http.StatusOK {
		// APIError may truncate unknown bodies, so remove every pool key
		// while each echo is still complete, before formatting the error.
		b = []byte(maskSearchKeys(string(b), a.Keys()))
		return SearchKeyQuota{}, errors.New(provider.APIError(b, res.Status))
	}
	q, err := readTavilyUsage(b)
	if err != nil {
		return SearchKeyQuota{}, fmt.Errorf("unexpected reply: %w", err)
	}
	return q, nil
}
