package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/tidwall/gjson"
)

func TestWhaleBridgeReasonlessAllProtocolFields(t *testing.T) {
	for _, tc := range []struct {
		proto provider.Protocol
		body  string
		keep  []string
	}{
		{provider.Chat, `{"reasoning_effort":"high","reasoning":{"effort":"high"},"thinking":{"type":"disabled"},"enable_thinking":false,"extra_body":{"google":{"thinking_config":{"thinking_level":"high"},"other":7}},"messages":[{"role":"user","content":"reasoning_effort stays as text"}],"temperature":0.4}`, []string{"temperature", "messages", "extra_body.google.other"}},
		{provider.Responses, `{"reasoning":{"effort":"high"},"include":["reasoning.encrypted_content","message.output_text.logprobs"],"input":"hi"}`, []string{"input", "include"}},
		{provider.Anthropic, `{"thinking":{"type":"between_tools"},"output_config":{"effort":"max","format":{"type":"json_schema"}},"max_tokens":10000}`, []string{"max_tokens", "output_config.format"}},
		{provider.Gemini, `{"generationConfig":{"thinkingConfig":{"thinkingBudget":0},"temperature":0.4},"contents":[{"parts":[{"text":"hi"}]}]}`, []string{"contents", "generationConfig.temperature"}},
		{provider.CodeAssist, `{"request":{"generationConfig":{"thinkingConfig":{"includeThoughts":true},"maxOutputTokens":10000},"contents":[{"parts":[{"text":"hi"}]}]}}`, []string{"request.contents", "request.generationConfig.maxOutputTokens"}},
	} {
		got := withoutAnyReasoning(tc.proto, []byte(tc.body))
		for _, key := range []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking", "output_config.effort", "generationConfig.thinkingConfig", "request.generationConfig.thinkingConfig", "extra_body.google.thinking_config"} {
			if gjson.GetBytes(got, key).Exists() {
				t.Errorf("%s kept %s: %s", tc.proto, key, got)
			}
		}
		for _, key := range tc.keep {
			if !gjson.GetBytes(got, key).Exists() {
				t.Errorf("%s lost %s: %s", tc.proto, key, got)
			}
		}
		if strings.Contains(gjson.GetBytes(got, "include").Raw, "reasoning") {
			t.Fatalf("sealed reasoning requested: %s", got)
		}
	}
	// Ordinary quiet-member behavior still preserves explicit thinking off.
	body := []byte(`{"thinking":{"type":"disabled"},"max_tokens":1000}`)
	if string(withoutReasoningAsk(provider.Anthropic, body)) != string(body) {
		t.Fatal("quiet member lost explicit thinking off")
	}
}

func TestWhaleBridgeReasonlessRelayAndTranslated(t *testing.T) {
	fresh(t)
	f := &fake{reply: sse(`data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"}}]}`, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "fixture", Chat: up.URL + "/v1", Models: []string{"m"}, ExtraBody: provider.ExtraBody{"reasoning_effort": json.RawMessage(`"max"`), "enable_thinking": json.RawMessage(`true`)}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelReasonless("relay/m", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "mix", Name: "Mix", Members: []string{"relay/m:high"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"relay/m", "group/mix"} {
		for _, tc := range []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"` + model + `","stream":true,"reasoning_effort":"high","enable_thinking":true,"messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/responses", `{"model":"` + model + `","stream":true,"input":"hi","reasoning":{"effort":"high"},"include":["reasoning.encrypted_content"]}`},
			{"/v1/messages", `{"model":"` + model + `","stream":true,"max_tokens":2000,"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`},
		} {
			if code, out := post(t, tc.path, tc.body); code != 200 || !strings.Contains(out, "ok") {
				t.Fatalf("%s: %d %s", tc.path, code, out)
			}
			for _, key := range []string{"reasoning_effort", "reasoning", "thinking", "enable_thinking"} {
				if gjson.GetBytes(f.got, key).Exists() {
					t.Fatalf("%s %s sent %s: %s", model, tc.path, key, f.got)
				}
			}
		}
	}
	// Internal callers such as search and continuation build a fresh request.
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	res, _, err := New().forwardTranslated(context.Background(), *p, provider.Chat, &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}, Effort: "high", Thinking: true, ThinkOff: true, Stream: true}, "m", nil)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if gjson.GetBytes(f.got, "reasoning_effort").Exists() || gjson.GetBytes(f.got, "enable_thinking").Exists() {
		t.Fatalf("translated internal request sent reasoning: %s", f.got)
	}
}

func TestWhaleBridgeReasonlessAnthropicAndGeminiRelay(t *testing.T) {
	fresh(t)
	for _, tc := range []struct {
		id                string
		proto             provider.Protocol
		path, body, reply string
	}{
		{"anth", provider.Anthropic, "/v1/messages", `{"model":"anth/m","max_tokens":2000,"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`},
		{"gem", provider.Gemini, "/v1beta/models/gem/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}`},
	} {
		f := &fake{reply: tc.reply, ctype: "application/json"}
		if tc.proto == provider.Gemini {
			f.reply, f.ctype = "data: "+tc.reply+"\n\n", "text/event-stream"
		}
		up := httptest.NewServer(f)
		t.Cleanup(up.Close)
		p := provider.Provider{ID: tc.id, Name: tc.id, Key: "fixture", Models: []string{"m"}}
		if tc.proto == provider.Anthropic {
			p.Anthropic = up.URL
		} else {
			p.Gemini = up.URL
		}
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
		if err := provider.SetModelReasonless(tc.id+"/m", true); err != nil {
			t.Fatal(err)
		}
		if code, out := post(t, tc.path, tc.body); code != 200 {
			t.Fatalf("%s: %d %s", tc.id, code, out)
		}
		if gjson.GetBytes(f.got, "thinking").Exists() || gjson.GetBytes(f.got, "output_config.effort").Exists() || gjson.GetBytes(f.got, "generationConfig.thinkingConfig").Exists() {
			t.Fatalf("%s relay reasoning: %s", tc.id, f.got)
		}
	}
}

func TestWhaleBridgeDeepSeekOutputFieldsAndRoomRetry(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var asks []int64
	prompt := 706784
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		n := gjson.GetBytes(body, "max_tokens").Int()
		mu.Lock()
		asks = append(asks, n)
		mu.Unlock()
		if prompt+int(n) > 1000000 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"error":{"message":"This model's maximum context length is 1000000 tokens. However, you requested %d tokens (%d in the messages, %d in the completion)."}}`, prompt+int(n), prompt, n)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"}}]}`, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "fixture"
	p.Chat = up.URL + "/v1"
	p.Responses = ""
	p.Anthropic = ""
	p.Models = []string{"deepseek-flash"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path, body string
		want       []int64
	}{
		{"/v1/chat/completions", `{"model":"deepseek/deepseek-flash","stream":true,"max_completion_tokens" : 393216,"messages":[{"role":"user","content":"hi"}]}`, []int64{393216, 293216}},
		{"/v1/responses", `{"model":"deepseek/deepseek-flash","stream":true,"max_output_tokens":393216,"input":"hi"}`, []int64{393216, 293216}},
		{"/v1/chat/completions", `{"model":"deepseek/deepseek-flash","stream":true,"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`, []int64{32000}},
	} {
		mu.Lock()
		asks = nil
		mu.Unlock()
		if code, out := post(t, tc.path, tc.body); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		mu.Lock()
		got := slices.Clone(asks)
		mu.Unlock()
		if !slices.Equal(got, tc.want) {
			t.Fatalf("asked %v want %v", got, tc.want)
		}
	}
	mu.Lock()
	asks = nil
	mu.Unlock()
	prompt = 996000
	if code, _ := post(t, "/v1/chat/completions", `{"model":"deepseek/deepseek-flash","stream":true,"max_tokens":393216,"messages":[{"role":"user","content":"hi"}]}`); code != 400 {
		t.Fatalf("tiny reply room status %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asks) != 1 {
		t.Fatalf("tiny room retried %v", asks)
	}
}

func TestWhaleBridgeDeepSeekTokenFieldScope(t *testing.T) {
	body := []byte(`{ "max_completion_tokens" : 393216, "messages":[{"content":"max_completion_tokens"}] }`)
	p := provider.Provider{Preset: "deepseek"}
	want := strings.Replace(string(body), `"max_completion_tokens"`, `"max_tokens"`, 1)
	if got := string(deepseekTokenField(p, provider.Chat, body)); got != want {
		t.Fatalf("renamed bytes: %s", got)
	}
	for _, tc := range []struct {
		p     provider.Provider
		proto provider.Protocol
		body  []byte
	}{{provider.Provider{}, provider.Chat, body}, {p, provider.Responses, body}, {p, provider.Chat, []byte(`{"max_tokens":7,"max_completion_tokens":5}`)}} {
		if got := deepseekTokenField(tc.p, tc.proto, tc.body); string(got) != string(tc.body) {
			t.Fatalf("unrelated field changed: %s", got)
		}
	}
}

func TestWhaleBridgeUserOutputCapsPlainProvider(t *testing.T) {
	fresh(t)
	f := &fake{ctype: "application/json", reply: `{"id":"m","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "fixture", Anthropic: up.URL, Models: []string{"m", "n"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelOutput("relay/m", 32000); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model       string
		asked, want int
	}{{"m", 128000, 32000}, {"m", 8000, 8000}, {"n", 128000, 128000}} {
		body := fmt.Sprintf(`{"model":"relay/%s","max_tokens":%d,"messages":[{"role":"user","content":"hi"}]}`, tc.model, tc.asked)
		if code, out := post(t, "/v1/messages", body); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		if got := gjson.GetBytes(f.got, "max_tokens").Int(); got != int64(tc.want) {
			t.Fatalf("%s asked %d got %d want %d", tc.model, tc.asked, got, tc.want)
		}
	}
}

func TestWhaleBridgeWindowOfUsesProviderLimit(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "window-fixture", Name: "Window fixture", Chat: "http://127.0.0.1:1/v1", Key: "fixture", Models: []string{"small", "large"}, Contexts: map[string]int{"*": 512000}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "small", Context: 262144}, {ID: "large", Context: 1000000}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model string
		want  int
	}{{"small", 262144}, {"large", 512000}} {
		if got := windowOf(p, tc.model); got != tc.want {
			t.Errorf("%s window %d want %d", tc.model, got, tc.want)
		}
	}
	p.Contexts["small"] = 600000
	if got := windowOf(p, "small"); got != 600000 {
		t.Fatalf("explicit model window %d", got)
	}
	custom := provider.Provider{ID: "custom-window", Name: "Custom", Chat: "http://127.0.0.1:1/v1", Key: "fixture", Models: []string{"claude-sonnet-4-5"}, Contexts: map[string]int{"*": 1000000}}
	if err := provider.Save(custom); err != nil {
		t.Fatal(err)
	}
	if got := windowOf(custom, "claude-sonnet-4-5"); got != 1000000 {
		t.Fatalf("heuristic capped custom window: %d", got)
	}
}

func TestWhaleBridgePluginTierTranslation(t *testing.T) {
	bun := os.Getenv("MAGPIE_BUN")
	if bun == "" {
		t.Skip("local Bun fixture runtime not configured")
	}
	fresh(t)
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
	var mu sync.Mutex
	var bodies [][]byte
	refuse := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, b)
		denied := refuse && gjson.GetBytes(b, "service_tier").Exists()
		mu.Unlock()
		if denied {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unsupported parameter: service_tier","param":"service_tier"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.Contains(r.URL.Path, "responses") {
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r","model":"responses"}}`, `data: {"type":"response.output_text.delta","delta":"ok"}`, `data: {"type":"response.completed","response":{"id":"r","model":"responses","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`))
			return
		}
		io.WriteString(w, sse(`data: {"id":"x","choices":[{"index":0,"delta":{"content":"ok"}}]}`, `data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
	}))
	t.Cleanup(up.Close)
	t.Setenv("WHALEBRIDGE_TIER_FIXTURE_BASE", up.URL+"/v1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fixture, err := filepath.Abs("testdata/tier-forwarding/index.js")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Add(ctx, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "forwarding-fixture", 0, nil, "fixture-account-one", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.APIKey(ctx, "forwarding-fixture", 0, nil, "fixture-account-two", plugin.NewAccount); err != nil {
		t.Fatal(err)
	}
	pid := provider.PluginID("forwarding-fixture")
	p, err := provider.Find(pid)
	if err != nil || !p.IsPlugin() {
		t.Fatalf("plugin provider %+v %v", p, err)
	}
	if !provider.CanFast(*p, "chat") || !provider.CanFast(*p, "responses") || provider.CanFast(*p, "ordinary") || provider.CanFast(*p, "messages") {
		t.Fatal("priority Fast capability subset is wrong")
	}
	for _, tc := range []struct {
		model, tier string
		fast        bool
	}{{"chat", "priority", true}, {"responses", "priority", true}, {"chat", "ultrafast", false}, {"responses", "ultrafast", false}, {"ordinary", "", true}, {"chat", "flex", false}} {
		mu.Lock()
		bodies = nil
		mu.Unlock()
		to := provider.Chat
		if tc.model == "responses" {
			to = provider.Responses
		}
		req := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}, Stream: true, Fast: tc.fast, Tier: tc.tier}
		res, _, err := New().forwardTranslated(ctx, *p, to, req, tc.model, nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		mu.Lock()
		sent := append([]byte(nil), bodies[len(bodies)-1]...)
		mu.Unlock()
		want := tc.tier
		if want == "flex" {
			want = ""
		}
		if got := gjson.GetBytes(sent, "service_tier").String(); got != want {
			t.Fatalf("%s tier %q fast %v went %q: %s", tc.model, tc.tier, tc.fast, got, sent)
		}
	}
	// Both cached accounts see the declared tiers; neither gets unlisted tiers.
	accounts := append([]provider.Provider{*p}, p.AlsoOn()...)
	if len(accounts) < 2 {
		t.Fatalf("fixture has %d accounts, want two", len(accounts))
	}
	for _, q := range accounts {
		if !slices.Equal(provider.PluginTiers(q, "chat"), []string{"priority", "ultrafast"}) {
			t.Fatalf("account tiers %v", provider.PluginTiers(q, "chat"))
		}
	}
	a, b := candidate{p: accounts[0], model: "chat"}, candidate{p: accounts[1], model: "chat"}
	if !takesTier(a, "priority") || !takesTier(a, "ultrafast") || takesTier(a, "flex") || tierMate([]candidate{b}, a, "ultrafast") != 0 {
		t.Fatal("account tier routing did not use declared tiers")
	}
	a.model = "ordinary"
	if !takesTier(a, "priority") {
		t.Fatal("undeclared account tiers must remain unknown")
	}
	mu.Lock()
	refuse = true
	bodies = nil
	mu.Unlock()
	s := New()
	for i := 0; i < 2; i++ {
		res, _, err := s.forwardTranslated(ctx, *p, provider.Chat, &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}}, Stream: true, Fast: true}, "chat", nil)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("tier refusal status %d", res.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || gjson.GetBytes(bodies[0], "service_tier").String() != "priority" || gjson.GetBytes(bodies[1], "service_tier").Exists() || gjson.GetBytes(bodies[2], "service_tier").Exists() {
		t.Fatalf("tier refusal retries %q", bodies)
	}
}

func TestWhaleBridgeReplyCapParsersAndBudgets(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want int
	}{
		{"This model's maximum context length is 1000000 tokens. However, you requested 1100000 tokens (706784 in the messages, 393216 in the completion).", 293216},
		{"This model's maximum context length is 32768 tokens. However, you requested 32000 output tokens and your prompt contains at least 769 input tokens", 31999},
		{"input length and `max_tokens` exceed context limit: 970000 + 64000 > 1000000", 30000},
		{"This model's maximum context length is 10000 tokens (11000 in the messages, 3000 in the completion)", 0},
		{"Invalid max_tokens", 0},
	} {
		if got := replyLeft([]byte(tc.msg)); got != tc.want {
			t.Errorf("%q got %d want %d", tc.msg, got, tc.want)
		}
	}
	got, ok := withReplyCap(provider.Anthropic, []byte(`{"max_tokens":64000,"thinking":{"type":"enabled","budget_tokens":40000}}`), 30000)
	if !ok || gjson.GetBytes(got, "max_tokens").Int() != 30000 || gjson.GetBytes(got, "thinking.budget_tokens").Int() != 29999 {
		t.Fatalf("thinking budget not fitted: %s", got)
	}
	for _, proto := range []provider.Protocol{provider.Chat, provider.Responses} {
		got, ok := withReplyCap(proto, []byte(`{"model":"m"}`), 30000)
		if !ok || !json.Valid(got) {
			t.Fatalf("%s missing cap: %s", proto, got)
		}
	}
}
