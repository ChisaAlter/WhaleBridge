package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func freeFirstCall(t *testing.T, s *Server, model, session string, authorized bool) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"model": model, "messages": []any{map[string]string{"role": "user", "content": "free routing fixture"}}})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(b)))
	r.RemoteAddr = "192.0.2.9:12345"
	r.Header.Set("Content-Type", "application/json")
	if authorized {
		r.Header.Set("Authorization", "Bearer test-private-gateway-key")
	}
	if session != "" {
		r.Header.Set("x-session-id", session)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func freeFirstCatalog(t *testing.T, p provider.Provider, models ...catalog.Model) {
	t.Helper()
	if err := catalog.SaveLive(p.ID, p.Chat, models); err != nil {
		t.Fatal(err)
	}
}

func freeFirstGroup(t *testing.T, routing, affinity string, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "Free Fixture", Routing: routing, Affinity: affinity, Members: members}); err != nil {
		t.Fatal(err)
	}
}

func freeFirstOrder(ws []Weighed) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Provider + "/" + w.Model
	}
	return out
}

func TestFreeFirstHTTPOrderTraceAndFallback(t *testing.T) {
	fresh(t)
	forgetRouting()
	var mu sync.Mutex
	var asked []string
	fail := map[string]int{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Model string }
		json.NewDecoder(r.Body).Decode(&in)
		mu.Lock()
		asked = append(asked, in.Model)
		status := fail[in.Model]
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer sk-freefirst-secret-fixture" {
			t.Error("provider authentication lost")
		}
		w.Header().Set("Content-Type", "application/json")
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, `{"error":{"message":"upstream unavailable"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "free-first", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "answer " + in.Model}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "ff-vendor", Name: "Free first vendor", Key: "sk-freefirst-secret-fixture", Chat: up.URL + "/v1", Models: []string{"ff-paid-a", "ff-free-a", "ff-unknown", "ff-free-b", "ff-paid-b"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	paid := &catalog.Price{Input: 2, Output: 5}
	freeFirstCatalog(t, p, catalog.Model{ID: "ff-paid-a", Price: paid}, catalog.Model{ID: "ff-free-a", Free: true, Price: paid}, catalog.Model{ID: "ff-unknown"}, catalog.Model{ID: "ff-free-b", Free: true}, catalog.Model{ID: "ff-paid-b", Price: paid})
	members := []string{"ff-vendor/ff-paid-a", "ff-vendor/ff-free-a", "ff-vendor/ff-unknown", "ff-vendor/ff-free-b", "ff-vendor/ff-paid-b"}
	freeFirstGroup(t, provider.FreeFirst, provider.AffinityOff, members...)
	s := New()
	if w := freeFirstCall(t, s, "group/free-fixture", "", false); w.Code != 401 {
		t.Fatalf("component auth %d %s", w.Code, w.Body)
	}
	mu.Lock()
	n := len(asked)
	mu.Unlock()
	if n != 0 {
		t.Fatal("missing auth reached vendor")
	}
	w := freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "answer ff-free-a") {
		t.Fatalf("free did not answer first: %d %s", w.Code, w.Body)
	}
	r := lastRoute(s)
	want := []string{"ff-vendor/ff-free-a", "ff-vendor/ff-free-b", "ff-vendor/ff-paid-a", "ff-vendor/ff-unknown", "ff-vendor/ff-paid-b"}
	if !slices.Equal(freeFirstOrder(r.Order), want) {
		t.Fatalf("unstable free partition %v", freeFirstOrder(r.Order))
	}
	for i, row := range r.Order {
		if row.Free != (i < 2) || row.Routing != provider.FreeFirst {
			t.Fatalf("same-rest different-model trace mixed: %+v", r.Order)
		}
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), p.Key) {
		t.Fatal("routing trace exposed provider key")
	}
	// Both free models fail in stable order, then paid candidates keep
	// their listed order even though all models share one provider rest.
	mu.Lock()
	asked = nil
	fail["ff-free-a"] = 503
	fail["ff-free-b"] = 503
	mu.Unlock()
	w = freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "answer ff-paid-a") {
		t.Fatalf("paid fallback %d %s", w.Code, w.Body)
	}
	mu.Lock()
	got := slices.Clone(asked)
	asked = nil
	clear(fail)
	mu.Unlock()
	if !slices.Equal(got, []string{"ff-free-a", "ff-free-b", "ff-paid-a"}) {
		t.Fatalf("failure order %v", got)
	}
	// Ordered remains listed order and does not populate free trace facts.
	freshRests := []string{"ff-vendor", "ff-vendor/ff-free-a", "ff-vendor/ff-free-b"}
	for _, id := range freshRests {
		clearRest(id)
	}
	freeFirstGroup(t, provider.Ordered, provider.AffinityOff, members...)
	w = freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "answer ff-paid-a") {
		t.Fatalf("Ordered changed %d %s", w.Code, w.Body)
	}
	for _, row := range lastRoute(s).Order {
		if row.Free {
			t.Fatalf("Ordered classified free row %+v", row)
		}
	}
}

func TestFreeFirstRestAndProviderFallbackBoundary(t *testing.T) {
	fresh(t)
	forgetRouting()
	var freeCalls, paidCalls atomic.Int32
	var freeFails, paidFails atomic.Bool
	freeFails.Store(true)
	serveOn(t, "ff-rest-free", "fixture-free-secret", []string{"ff-model"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		freeCalls.Add(1)
		if freeFails.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"down"}}`)
			return
		}
		io.WriteString(w, chatOK)
	}))
	serveOn(t, "ff-rest-paid", "fixture-paid-secret", []string{"ff-model"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paidCalls.Add(1)
		if paidFails.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"down"}}`)
			return
		}
		io.WriteString(w, chatOK)
	}))
	free, err := provider.Find("ff-rest-free")
	if err != nil {
		t.Fatal(err)
	}
	freeFirstCatalog(t, *free, catalog.Model{ID: "ff-model", Free: true})
	freeFirstGroup(t, provider.FreeFirst, provider.AffinityOff, "ff-rest-paid/ff-model", "ff-rest-free/ff-model")
	s := New()
	for range 2 {
		w := freeFirstCall(t, s, "group/free-fixture", "", true)
		if w.Code != 200 {
			t.Fatalf("free rest fallback %d %s", w.Code, w.Body)
		}
	}
	if freeCalls.Load() != 1 || paidCalls.Load() != 2 {
		t.Fatalf("free rest ignored: free=%d paid=%d", freeCalls.Load(), paidCalls.Load())
	}
	rows := lastRoute(s).Order
	if len(rows) != 2 || rows[0].Provider != "ff-rest-paid" || rows[0].Free || !rows[1].Free || rows[1].Rest == nil {
		t.Fatalf("free rest trace %+v", rows)
	}
	// A resting paid member still stays behind a ready free member.
	clearRest("ff-rest-free")
	freeFails.Store(false)
	restingUntil.Lock()
	restingUntil.m["ff-rest-paid"] = time.Now().Add(time.Hour)
	restingUntil.Unlock()
	w := freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 || lastRoute(s).Tries[0].ID != "ff-rest-free" {
		t.Fatalf("paid rest displaced free: %d %s", w.Code, w.Body)
	}
	// Provider fallback models remain a distinct stage; a paid primary
	// set to Free first is tried before a free model named as fallback.
	clearRest("ff-rest-paid")
	paid, err := provider.Find("ff-rest-paid")
	if err != nil {
		t.Fatal(err)
	}
	paid.Routing, paid.Fallback = provider.FreeFirst, []string{"ff-rest-free/ff-model"}
	if err := provider.Save(*paid); err != nil {
		t.Fatal(err)
	}
	before := freeCalls.Load()
	w = freeFirstCall(t, s, "ff-rest-paid/ff-model", "", true)
	if w.Code != 200 || freeCalls.Load() != before || lastRoute(s).Tries[0].ID != "ff-rest-paid" {
		t.Fatalf("free fallback crossed primary boundary: %d %s", w.Code, w.Body)
	}
	paidFails.Store(true)
	w = freeFirstCall(t, s, "ff-rest-paid/ff-model", "", true)
	r := lastRoute(s)
	if w.Code != 200 || len(r.Tries) != 2 || r.Tries[0].ID != "ff-rest-paid" || r.Tries[1].ID != "ff-rest-free" {
		t.Fatalf("provider fallback lost: %d %s %+v", w.Code, w.Body, r.Tries)
	}
}

func TestFreeFirstUnknownPoolKeepsOrderAndLoneTrace(t *testing.T) {
	fresh(t)
	forgetRouting()
	serveOn(t, "ff-unknown", "sk-unknown-secret-fixture", []string{"ff-unknown-a", "ff-unknown-b"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, chatOK) }))
	freeFirstGroup(t, provider.FreeFirst, provider.AffinityOff, "ff-unknown/ff-unknown-b", "ff-unknown/ff-unknown-a")
	s := New()
	w := freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 {
		t.Fatalf("unknown pool %d %s", w.Code, w.Body)
	}
	rows := lastRoute(s).Order
	if !slices.Equal(freeFirstOrder(rows), []string{"ff-unknown/ff-unknown-b", "ff-unknown/ff-unknown-a"}) || rows[0].Free || rows[1].Free {
		t.Fatalf("unknown became free or reordered %+v", rows)
	}
	p, err := provider.Find("ff-unknown")
	if err != nil {
		t.Fatal(err)
	}
	freeFirstCatalog(t, *p, catalog.Model{ID: "ff-unknown-a", Free: true}, catalog.Model{ID: "ff-unknown-b"})
	p.Routing = provider.FreeFirst
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	w = freeFirstCall(t, s, "ff-unknown/ff-unknown-a", "", true)
	if w.Code != 200 {
		t.Fatalf("lone free %d %s", w.Code, w.Body)
	}
	rows = lastRoute(s).Order
	if len(rows) != 1 || !rows[0].Free || rows[0].Routing != provider.FreeFirst {
		t.Fatalf("lone free trace %+v", rows)
	}
}

func TestFreeFirstQuotaWeightAndProtocolFit(t *testing.T) {
	fresh(t)
	forgetRouting()
	p := provider.Provider{ID: "ff-weight", Name: "Weight", Chat: "http://127.0.0.1:1/v1", Key: "fixture"}
	freeFirstCatalog(t, p, catalog.Model{ID: "ff-free", Free: true}, catalog.Model{ID: "ff-paid", Price: &catalog.Price{Input: 1}})
	cs := []candidate{{p: p, model: "ff-paid", rest: p.ID}, {p: p, model: "ff-free", rest: p.ID}}
	old := keyAllowance
	t.Cleanup(func() { keyAllowance = old })
	keyAllowance = func(provider.Provider) (provider.Allowance, bool) {
		return provider.Allowance{{Used: 100, Resets: time.Now().Add(time.Hour), Span: time.Hour}}, true
	}
	out, wg := weighRouted(provider.Provider{Routing: provider.FreeFirst}, cs, "", provider.Chat)
	if out[0].model != "ff-free" || wg.lefts[out[0].allowanceKey()].used != 100 {
		t.Fatalf("used quota demoted free %v %+v", restsOf(out), wg.lefts)
	}
	// A key fitting the protocol still wins over a free key that needs
	// translation: Free first is a cost policy, not a protocol bypass.
	fit := p
	fit.KeyProtocol = provider.Chat
	other := p
	other.KeyProtocol = provider.Anthropic
	other.Chat = ""
	other.Anthropic = "http://127.0.0.1:1"
	out, _ = weighRouted(provider.Provider{Routing: provider.FreeFirst}, []candidate{{p: other, model: "ff-free", rest: "other"}, {p: fit, model: "ff-paid", rest: "fit"}}, "", provider.Chat)
	if out[0].rest != "fit" {
		t.Fatalf("free bypassed keyFit %s", restsOf(out))
	}
}

func TestFreeFirstClassificationIsLazy(t *testing.T) {
	fresh(t)
	forgetRouting()
	var reads atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); http.NotFound(w, r) }))
	t.Cleanup(up.Close)
	// Other strategies do not populate free classifications. Unknown
	// metadata must remain unclassified until this policy asks for it.
	p := provider.Provider{ID: "ff-lazy", Preset: "cline", Chat: up.URL + "/v1", Key: "fixture", Models: []string{"ff-lazy-model"}}
	freeFirstCatalog(t, p, catalog.Model{ID: "ff-lazy-model"})
	c := candidate{p: p, model: "ff-lazy-model", rest: p.ID}
	for _, routing := range []string{"", provider.Ordered, provider.Rotate, provider.Weighted, provider.LeastUsed, provider.Pace} {
		_, wg := weighRouted(provider.Provider{ID: "ff-lazy-pool", Routing: routing}, []candidate{c}, "", provider.Chat)
		if wg.free != nil {
			t.Fatalf("%q eagerly populated free classification", routing)
		}
	}
	if reads.Load() != 0 {
		t.Fatal("routing unexpectedly made a vendor request")
	}
	_, wg := weighRouted(provider.Provider{Routing: provider.FreeFirst}, []candidate{c}, "", provider.Chat)
	if wg.free == nil {
		t.Fatal("Free first did not classify lone candidate")
	}
}

func TestFreeFirstNestedOwnRoutingAndSink(t *testing.T) {
	fresh(t)
	forgetSinks(t)
	forgetRouting()
	mk := func(id string, free bool) provider.Provider {
		p := provider.Provider{ID: id, Name: id, Chat: "http://127.0.0.1:1/v1", Key: "fixture-" + id}
		m := catalog.Model{ID: "ff-model", Free: free}
		if !free {
			m.Price = &catalog.Price{Input: 1}
		}
		freeFirstCatalog(t, p, m)
		return p
	}
	paid, free, inner := mk("ff-paid", false), mk("ff-free", true), mk("ff-inner", false)
	inner.Routing = provider.Ordered
	inner.Keys = []provider.KeyAccount{{Key: "fixture-inner-second"}}
	sub := provider.Group{ID: "ff-child", Routing: provider.Ordered}
	g := provider.Group{ID: "ff-parent", Routing: provider.FreeFirst}
	ms := []provider.Member{
		{Provider: paid, Model: "ff-model", Path: []string{"ff-paid/ff-model"}},
		{Provider: inner, Model: "ff-model", Path: []string{"ff-inner/ff-model"}},
		{Provider: paid, Model: "ff-model", Path: []string{"group/ff-child", "ff-paid/ff-model"}, Via: []provider.Group{sub}},
		{Provider: free, Model: "ff-model", Path: []string{"group/ff-child", "ff-free/ff-model"}, Via: []provider.Group{sub}},
		{Provider: free, Model: "ff-model", Path: []string{"ff-free/ff-model"}},
	}
	cs, pl := (&Server{}).planGroup(g, ms, provider.Chat)
	ids := func(cs []candidate) []string {
		out := make([]string, len(cs))
		for i, c := range cs {
			out[i] = c.p.ID
		}
		return out
	}
	want := []string{"ff-free", "ff-paid", "ff-inner", "ff-inner", "ff-paid", "ff-free"}
	if !slices.Equal(ids(cs), want) {
		t.Fatalf("nested/provider own order lost: %v", ids(cs))
	}
	if cs[2].p.Key != "fixture-ff-inner" || cs[3].p.Key != "fixture-inner-second" || pl.order[2].Routing != provider.Ordered || pl.order[3].Routing != provider.Ordered || pl.order[4].Free || pl.order[5].Free {
		t.Fatalf("child routing/trace overridden %+v", pl.order)
	}
	// Sink and rests remain stronger admission/order constraints than
	// cost. The trace follows exactly the moved candidates.
	sink(cs[0].restKey(), time.Now())
	g.Sink = true
	cs, pl = (&Server{}).planGroup(g, ms, provider.Chat)
	if cs[0].p.ID != "ff-paid" || pl.order[0].Free {
		t.Fatalf("Free first bypassed Sink: %v", ids(cs))
	}
	for i, c := range cs {
		if c.p.ID != pl.order[i].Provider || c.model != pl.order[i].Model {
			t.Fatal("sink trace separated from candidates")
		}
	}
}

func TestFreeFirstHTTPProtocolFallback(t *testing.T) {
	fresh(t)
	forgetRouting()
	var mu sync.Mutex
	var asked []string
	freeUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, "free "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/messages" || r.Header.Get("X-Api-Key") != "free-anthropic-fixture" {
			t.Errorf("free protocol/auth %s %s", r.URL.Path, r.Header.Get("X-Api-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"free protocol unavailable"}}`)
	}))
	t.Cleanup(freeUp.Close)
	paidUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, "paid "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer paid-chat-fixture" {
			t.Errorf("paid protocol/auth %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	t.Cleanup(paidUp.Close)
	free := provider.Provider{ID: "ff-proto-free", Name: "Free Anthropic", Anthropic: freeUp.URL, Key: "free-anthropic-fixture", Models: []string{"ff-protocol"}}
	paid := provider.Provider{ID: "ff-proto-paid", Name: "Paid Chat", Chat: paidUp.URL + "/v1", Key: "paid-chat-fixture", Models: []string{"ff-protocol"}}
	for _, p := range []provider.Provider{free, paid} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	freeFirstCatalog(t, free, catalog.Model{ID: "ff-protocol", Free: true})
	freeFirstGroup(t, provider.FreeFirst, provider.AffinityOff, "ff-proto-paid/ff-protocol", "ff-proto-free/ff-protocol")
	s := New()
	w := freeFirstCall(t, s, "group/free-fixture", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hello") {
		t.Fatalf("protocol fallback %d %s", w.Code, w.Body)
	}
	mu.Lock()
	got := slices.Clone(asked)
	mu.Unlock()
	if !slices.Equal(got, []string{"free /v1/messages", "paid /v1/chat/completions"}) {
		t.Fatalf("free-first protocol fallback order %v", got)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].ID != "ff-proto-free" || r.Tries[1].ID != "ff-proto-paid" || !r.Order[0].Free || r.Order[1].Free {
		t.Fatalf("protocol trace %+v %+v", r.Tries, r.Order)
	}
}

// The real local plugin host must carry each account's free flags across
// providers RPC, cache, provider expansion and the private gateway handler.
// Both accounts list the same model, so the union's first-wins flag cannot
// answer this test correctly in either direction.
func TestFreeFirstPluginAccountsHTTP(t *testing.T) {
	bun := os.Getenv("MAGPIE_BUN")
	if bun == "" {
		t.Skip("local Bun fixture runtime not configured")
	}
	fresh(t)
	forgetRouting()
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	plugin.Settle()
	t.Cleanup(plugin.Settle)
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	statePath := filepath.Join(t.TempDir(), "free-models.json")
	var mu sync.Mutex
	var asked []string
	var failing string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		var in struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		if (key != "a" && key != "b") || in.Model != "shared" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("plugin request identity/model/path: %q %q %q", key, in.Model, r.URL.Path)
		}
		mu.Lock()
		asked = append(asked, key)
		fails := failing == key
		mu.Unlock()
		if fails {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"fixture unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"plugin-free","choices":[{"index":0,"delta":{"content":"plugin answer `+key+`"}}]}`, `data: {"id":"plugin-free","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	writeState := func(freeKey string) {
		t.Helper()
		state := map[string]any{}
		for _, key := range []string{"a", "b"} {
			free := []string{}
			if key == freeKey {
				free = []string{"shared"}
			}
			state[key] = map[string]any{"free": free}
		}
		b, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(statePath, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeState("b")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixture, err := filepath.Abs("../plugin/testdata/freefirst/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	if err := plugin.SetOptions(fixture, map[string]any{"statePath": statePath, "base": up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"a", "b"} {
		if _, err := plugin.APIKey(ctx, "freefixture", 0, nil, key, plugin.NewAccount); err != nil {
			t.Fatal(err)
		}
	}
	pid := provider.PluginID("freefixture")
	relist := func(freeKey string) {
		t.Helper()
		writeState(freeKey)
		ps, err := plugin.Providers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(ps, func(p plugin.Provider) bool { return p.ID == "freefixture" })
		if i < 0 || len(ps[i].Accounts) != 2 {
			t.Fatalf("plugin account listing %+v", ps)
		}
		for _, account := range ps[i].Accounts {
			key, _ := plugin.Auths("freefixture")[account.Key]["key"].(string)
			want := []string{}
			if key == freeKey {
				want = []string{"shared"}
			}
			if account.FreeModels == nil || !slices.Equal(account.FreeModels, want) || !slices.Equal(account.Models, []string{"shared"}) {
				t.Fatalf("host free fact %s: models=%#v free=%#v want=%#v", key, account.Models, account.FreeModels, want)
			}
		}
		provider.Changed()
	}
	relist("b")
	p, err := provider.Find(pid)
	if err != nil || !p.IsPlugin() {
		t.Fatalf("plugin provider %+v %v", p, err)
	}
	accounts := append([]provider.Provider{*p}, p.AlsoOn()...)
	if len(accounts) != 2 {
		t.Fatalf("provider accounts %d", len(accounts))
	}
	cs := perKey(*p, "shared", provider.Chat)
	if len(cs) != 2 {
		t.Fatalf("expanded accounts %d", len(cs))
	}
	ids := map[string]string{"a": cs[0].rest, "b": cs[1].rest}
	if p.FreeModel("shared") || !accounts[1].FreeModel("shared") {
		t.Fatal("actual account free flags ignored before routing")
	}
	if err := provider.SetRouting(pid, provider.FreeFirst); err != nil {
		t.Fatal(err)
	}
	s := New()
	check := func(freeKey string, fail bool) {
		t.Helper()
		mu.Lock()
		asked = nil
		failing = ""
		if fail {
			failing = freeKey
		}
		mu.Unlock()
		w := freeFirstCall(t, s, pid+"/shared", "", true)
		other := "a"
		if freeKey == "a" {
			other = "b"
		}
		answer := freeKey
		want := []string{freeKey}
		if fail {
			answer = other
			want = append(want, other)
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), "plugin answer "+answer) {
			t.Fatalf("plugin free routing %s fail=%v: %d %s", freeKey, fail, w.Code, w.Body)
		}
		mu.Lock()
		got := slices.Clone(asked)
		mu.Unlock()
		if !slices.Equal(got, want) {
			t.Fatalf("actual plugin account order %v want %v", got, want)
		}
		r := lastRoute(s)
		if len(r.Order) != 2 || r.Order[0].ID != ids[freeKey] || !r.Order[0].Free || r.Order[1].ID != ids[other] || r.Order[1].Free || r.Order[0].Who == r.Order[1].Who || r.Order[0].Routing != provider.FreeFirst {
			t.Fatalf("per-account free trace %+v", r.Order)
		}
		if len(r.Tries) != len(want) {
			t.Fatalf("plugin tries %+v", r.Tries)
		}
		for i, key := range want {
			if r.Tries[i].ID != ids[key] {
				t.Fatalf("try %d actual account %s trace %+v", i, key, r.Tries)
			}
		}
		if fail && (r.Tries[0].Status != 503 || r.Tries[1].Status != 200) {
			t.Fatalf("plugin fallback statuses %+v", r.Tries)
		}
	}
	check("b", false)
	relist("a")
	check("a", false)
	check("a", true)
}

func TestFreeFirstStrictAffinityKeepsPaidConversation(t *testing.T) {
	fresh(t)
	forgetRouting()
	var freeFails atomic.Bool
	freeFails.Store(true)
	serveOn(t, "ff-aff-free", "fixture-free", []string{"ff-model"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if freeFails.Load() {
			w.WriteHeader(503)
			io.WriteString(w, `{"error":{"message":"down"}}`)
			return
		}
		io.WriteString(w, chatOK)
	}))
	serveOn(t, "ff-aff-paid", "fixture-paid", []string{"ff-model"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, chatOK) }))
	p, err := provider.Find("ff-aff-free")
	if err != nil {
		t.Fatal(err)
	}
	freeFirstCatalog(t, *p, catalog.Model{ID: "ff-model", Free: true})
	freeFirstGroup(t, provider.FreeFirst, provider.AffinityStrict, "ff-aff-paid/ff-model", "ff-aff-free/ff-model")
	s := New()
	w := freeFirstCall(t, s, "group/free-fixture", "strict-free-fixture", true)
	if w.Code != 200 {
		t.Fatalf("first strict turn %d %s", w.Code, w.Body)
	}
	r := lastRoute(s)
	if len(r.Tries) != 2 || r.Tries[0].ID != "ff-aff-free" || r.Tries[1].ID != "ff-aff-paid" {
		t.Fatalf("strict fixture did not fail over %+v", r.Tries)
	}
	clearRest("ff-aff-free")
	freeFails.Store(false)
	w = freeFirstCall(t, s, "group/free-fixture", "strict-free-fixture", true)
	if w.Code != 200 {
		t.Fatalf("kept strict turn %d %s", w.Code, w.Body)
	}
	r = lastRoute(s)
	if r.Affinity == nil || r.Affinity.Why != "strict" || !r.Affinity.Kept || len(r.Tries) != 1 || r.Tries[0].ID != "ff-aff-paid" {
		t.Fatalf("free displaced strict paid affinity %+v", r)
	}
}
