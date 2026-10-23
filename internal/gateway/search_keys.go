package gateway

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// Search keys take turns or follow the user's saved order. A key refused
// recently is tried after healthy keys, soonest back first, never discarded.
type keyRest struct {
	Until time.Time
	Why   string
}

var searchRests sync.Map
var searchTurns sync.Map

// Runtime state identifies keys by a hash, never by their secret bytes.
func searchKeyID(vendor, key string) string {
	sum := sha256.Sum256([]byte(key))
	return vendor + " " + hex.EncodeToString(sum[:8])
}

const (
	rateLimitRest    = time.Minute
	rateLimitRestMin = 30 * time.Second
	rateLimitRestMax = 10 * time.Minute
	turnedAwayRest   = 10 * time.Minute
	outOfCreditRest  = time.Hour
)

func searchRestFor(vendor string, status int, retryAfter string, now time.Time) (time.Duration, string) {
	switch {
	case status == 401 || status == 403:
		return turnedAwayRest, "turned away"
	case status == 402 || vendor == "tavily" && (status == 432 || status == 433):
		return outOfCreditRest, "out of credit"
	}
	d := rateLimitRest
	if seconds, err := strconv.ParseInt(retryAfter, 10, 64); err == nil && seconds >= 0 {
		// Bound before duration conversion, including arbitrarily large headers.
		d = time.Duration(min(seconds, int64(rateLimitRestMax/time.Second))) * time.Second
	} else if at, err := http.ParseTime(retryAfter); err == nil {
		d = at.Sub(now)
	}
	return min(max(d, rateLimitRestMin), rateLimitRestMax), "rate limited"
}

func restSearchKey(vendor, key string, d time.Duration, why string, now time.Time) {
	r := keyRest{Until: now.Add(d), Why: why}
	if old, ok := searchKeyRest(vendor, key, now); ok && old.Until.After(r.Until) {
		return
	}
	searchRests.Store(searchKeyID(vendor, key), r)
}
func wakeSearchKey(vendor, key string) { searchRests.Delete(searchKeyID(vendor, key)) }
func searchKeyRest(vendor, key string, now time.Time) (keyRest, bool) {
	v, ok := searchRests.Load(searchKeyID(vendor, key))
	if !ok {
		return keyRest{}, false
	}
	r := v.(keyRest)
	return r, now.Before(r.Until)
}

func searchKeyOrder(a provider.SearchAPI, keys []string, now time.Time) []int {
	n := len(keys)
	start := 0
	if a.Pick != provider.SearchPickOrder && n > 1 {
		v, _ := searchTurns.LoadOrStore(a.Vendor, new(atomic.Uint64))
		start = int((v.(*atomic.Uint64).Add(1) - 1) % uint64(n))
	}
	order := make([]int, 0, n)
	var resting []int
	until := map[int]time.Time{}
	for k := range n {
		i := (start + k) % n
		if r, ok := searchKeyRest(a.Vendor, keys[i], now); ok {
			resting = append(resting, i)
			until[i] = r.Until
		} else {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(resting, func(x, y int) int { return until[x].Compare(until[y]) })
	return append(order, resting...)
}
func forgetSearchKeys() { searchRests.Clear(); searchTurns.Clear() }
func keyLabel(i int, key string) string {
	return "key " + strconv.Itoa(i+1) + " (" + cmp.Or(provider.MaskSearchKey(key), "none") + ")"
}

// A vendor may echo a different key of the same saved pool in errors or plan
// names. Mask every key, longest first so overlapping keys cannot reveal a tail.
func maskSearchKeys(text string, keys []string) string {
	keys = slices.Clone(keys)
	slices.SortFunc(keys, func(a, b string) int { return len(b) - len(a) })
	for _, key := range keys {
		if key != "" {
			text = strings.ReplaceAll(text, key, provider.MaskSearchKey(key))
		}
	}
	return text
}
