package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/gwmw"
	"github.com/ChisaAlter/WhaleBridge/internal/masking"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

type failedRegistrationMasker struct {
	masking.Masker
	at string
}

func (m failedRegistrationMasker) JSON(body []byte) ([]byte, int) {
	panic("raw-request-must-not-appear")
}
func (m failedRegistrationMasker) Text(text string) (string, int) { panic("raw-query-must-not-appear") }

func TestRegistrationPrivacyBlocksEveryExit(t *testing.T) {
	for _, mode := range []string{"missing", "panic", "corrupt-settings"} {
		t.Run(mode, func(t *testing.T) {
			fresh(t)
			t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
			if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				withoutBuiltinMasker(t)
			}
			if mode == "panic" {
				remove := gwmw.Mask(failedRegistrationMasker{})
				t.Cleanup(remove)
			}
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`)
			}))
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "privacy", Name: "Privacy", Chat: up.URL + "/v1", Anthropic: up.URL, Responses: up.URL + "/v1", Key: "fixture", Models: []string{"m", "vid"}}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "m"}, {ID: "vid", Films: true}}); err != nil {
				t.Fatal(err)
			}
			if err := provider.SetSearchAPI(provider.SearchAPI{Vendor: "searxng", URL: up.URL}); err != nil {
				t.Fatal(err)
			}
			s := New()
			h := s.upstreamHandler()
			if mode == "corrupt-settings" {
				if err := os.WriteFile(settings.Path(), []byte(`{"redactPersonal":`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cases := []struct{ name, path, body string }{
				{"chat", "/v1/chat/completions", `{"model":"privacy/m","messages":[{"role":"user","content":"contact private.fixture@proton.me"}]}`},
				{"messages", "/v1/messages", `{"model":"privacy/m","max_tokens":8,"messages":[{"role":"user","content":"private.fixture@proton.me"}]}`},
				{"responses", "/v1/responses", `{"model":"privacy/m","input":"private.fixture@proton.me"}`},
				{"count", "/v1/messages/count_tokens", `{"model":"privacy/m","messages":[{"role":"user","content":"private.fixture@proton.me"}]}`},
				{"embed", "/v1/embeddings", `{"model":"privacy/m","input":"private.fixture@proton.me"}`},
				{"rerank", "/v1/rerank", `{"model":"privacy/m","query":"private.fixture@proton.me","documents":["private.fixture@proton.me"]}`},
				{"images", "/v1/images/generations", `{"model":"privacy/m","prompt":"private.fixture@proton.me"}`},
				{"videos", "/v1/videos", `{"model":"privacy/vid","prompt":"private.fixture@proton.me"}`},
			}
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					w := httptest.NewRecorder()
					r := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
					r.Header.Set("Content-Type", "application/json")
					h.ServeHTTP(w, r)
					if w.Code != 503 {
						t.Fatalf("%s: %d %s", tt.name, w.Code, w.Body)
					}
					if strings.Contains(w.Body.String(), "private.fixture") || strings.Contains(w.Body.String(), "raw-request") {
						t.Fatal("masker failure leaked request in error")
					}
				})
			}
			run := func(name string, f func() error) {
				t.Helper()
				if err := f(); err == nil {
					t.Fatalf("%s did not stop for failed masking", name)
				}
			}
			run("api-search", func() error { _, _, err := s.apiSearch(context.Background(), "private.fixture@proton.me"); return err })
			kimi := p
			kimi.Preset = "kimi-code-cn"
			kimi.Chat = up.URL + "/coding/v1"
			run("kimi-search", func() error {
				_, _, err := s.kimiSearch(context.Background(), kimi, "private.fixture@proton.me")
				return err
			})
			run("workbuddy-search", func() error {
				_, _, err := s.wbSearch(context.Background(), p, "private.fixture@proton.me")
				return err
			})
			run("decision", func() error {
				_, err := s.askJev(p, "m", []string{"work"}, before{}, true, "private.fixture@proton.me")
				return err
			})
			// Codex passthrough and native Alpha search must stop before signing too.
			for _, alpha := range []bool{false, true} {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("POST", "/backend-api/codex/responses", strings.NewReader(`{}`))
				if alpha {
					served, _ := s.alphaSearchOn(w, r, []byte(`{"query":"private.fixture@proton.me"}`), "m", nil)
					if !served {
						t.Fatal("failed masking permitted Alpha fallback")
					}
				} else {
					s.codexUpstreamOn(w, r, "/responses", []byte(`{"input":"private.fixture@proton.me"}`), nil)
				}
				if w.Code != 503 {
					t.Fatalf("Codex privacy failure %d %s", w.Code, w.Body)
				}
			}
			if calls.Load() != 0 {
				t.Fatalf("%s allowed %d vendor calls", mode, calls.Load())
			}
		})
	}
}

func TestRegistrationPrivacyLocalException(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	withoutBuiltinMasker(t)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), "local.fixture@proton.me") {
			t.Error("local unmasked exception lost original")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "local", Name: "Local", Chat: up.URL + "/v1", Key: "fixture", Models: []string{"m"}, Unredacted: true}); err != nil {
		t.Fatal(err)
	}
	code, out := post(t, "/v1/chat/completions", `{"model":"local/m","messages":[{"role":"user","content":"local.fixture@proton.me"}]}`)
	if code != 200 || calls.Load() != 1 {
		t.Fatalf("local exception %d %s (%d calls)", code, out, calls.Load())
	}
}

func TestRegistrationPrivacyVideoPollAfterDisable(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	var sent string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			var q struct{ Prompt string }
			json.NewDecoder(r.Body).Decode(&q)
			sent = q.Prompt
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "vid_fixture", "model": "vid", "status": "completed", "prompt": sent})
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "studio", Name: "Studio", Chat: up.URL + "/v1", Key: "fixture", Models: []string{"vid"}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "vid", Films: true}}); err != nil {
		t.Fatal(err)
	}
	h := New().upstreamHandler()
	code, obj, raw := asKey(t, h, "", "POST", "/v1/videos", `{"model":"studio/vid","prompt":"late.fixture@proton.me"}`)
	if code != 200 || strings.Contains(sent, "late.fixture@proton.me") || !strings.Contains(sent, "{{EMAIL_") {
		t.Fatalf("video create %d %s; sent %q", code, raw, sent)
	}
	id, _ := obj["id"].(string)
	if err := settings.Save(settings.Settings{}); err != nil {
		t.Fatal(err)
	}
	withoutBuiltinMasker(t)
	code, _, raw = asKey(t, h, "", "GET", "/v1/videos/"+id, "")
	if code != 200 || !strings.Contains(string(raw), "late.fixture@proton.me") || strings.Contains(string(raw), "{{EMAIL_") {
		t.Fatalf("late video poll %d %s", code, raw)
	}
}

type capturedRegistrationMasker struct {
	masking.Masker
	name string
}

func (m capturedRegistrationMasker) Text(s string) (string, int)           { return "masked-by-" + m.name, 1 }
func (m capturedRegistrationMasker) Restore(s string, escaped bool) string { return m.name + ":" + s }
func TestRegistrationPrivacyCapturesRestore(t *testing.T) {
	fresh(t)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", "")
	if err := settings.Save(settings.Settings{RedactPersonal: true}); err != nil {
		t.Fatal(err)
	}
	remove := gwmw.Mask(capturedRegistrationMasker{name: "first"})
	ctx, err := captureMasking(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	remove()
	next := gwmw.Mask(capturedRegistrationMasker{name: "next"})
	defer next()
	masked, err := maskedQueryIn(ctx, "secret", nil)
	if err != nil || masked != "masked-by-first" {
		t.Fatal("query did not retain captured masker")
	}
	restored, err := restoreMasked(ctx, masked)
	if err != nil || restored != "first:masked-by-first" {
		t.Fatal("restoration changed generation")
	}
}
