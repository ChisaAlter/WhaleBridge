package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
)

// A mixed provider lists both its Chat and native Gemini models. When one
// key's Gemini list cannot be fetched, Chat still updates and that key's
// own cached Gemini models survive without acquiring another key's models.
func TestMixedChatGeminiKeepsNativeModelsPerKey(t *testing.T) {
	vertexHome(t)
	const mainKey, spareKey = "mixed-main-test-key", "mixed-spare-test-key"
	var failMain atomic.Bool
	var chatCalls, nativeCalls, nativeFailures atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/v1/models":
			chatCalls.Add(1)
			if auth := r.Header.Get("Authorization"); auth != "Bearer "+mainKey && auth != "Bearer "+spareKey {
				t.Errorf("Chat list has unexpected auth %q", auth)
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "chat-only"}, {"id": "shared"}}})
		case "/native/v1beta/models":
			nativeCalls.Add(1)
			key := r.Header.Get("x-goog-api-key")
			if r.Header.Get("Authorization") != "" || key != mainKey && key != spareKey {
				t.Errorf("Gemini list has unexpected auth %q / %q", key, r.Header.Get("Authorization"))
			}
			if key == mainKey && failMain.Load() {
				nativeFailures.Add(1)
				w.WriteHeader(http.StatusServiceUnavailable)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "temporary fixture failure"}})
				return
			}
			id := "gem-a"
			if key == spareKey {
				id = "gem-b"
			}
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
				{"name": "models/" + id, "supportedGenerationMethods": []string{"generateContent"}},
				{"name": "models/shared", "supportedGenerationMethods": []string{"generateContent"}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	p := Provider{ID: "mixed-chat-native", Name: "Mixed chat and native", Key: mainKey,
		Keys: []KeyAccount{{Name: "spare", Key: spareKey}}, Chat: up.URL + "/chat/v1", Gemini: up.URL + "/native/v1beta",
		Models: []string{"chat-only", "shared", "gem-a", "gem-b"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p = *got
	check := func(ms []catalog.Model) {
		t.Helper()
		byID := map[string]catalog.Model{}
		for _, m := range ms {
			byID[m.ID] = m
		}
		if len(byID) != 4 {
			t.Fatalf("mixed list: %+v", ms)
		}
		for id, want := range map[string]struct{ apis, keys []string }{
			"chat-only": {[]string{"chat"}, []string{keyID(mainKey), keyID(spareKey)}},
			"shared":    {[]string{"chat", "gemini"}, []string{keyID(mainKey), keyID(spareKey)}},
			"gem-a":     {[]string{"gemini"}, []string{keyID(mainKey)}},
			"gem-b":     {[]string{"gemini"}, []string{keyID(spareKey)}},
		} {
			m, ok := byID[id]
			if !ok || !slices.Equal(m.APIs, want.apis) || !slices.Equal(m.Keys, want.keys) {
				t.Errorf("%s: APIs %v, keys %v; want APIs %v, keys %v", id, m.APIs, m.Keys, want.apis, want.keys)
			}
		}
	}
	for _, failed := range []bool{false, true} {
		failMain.Store(failed)
		ms, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		check(ms)
		exposed := p.Exposed()
		if !slices.Equal(modelIDs(exposed), p.Models) {
			t.Fatalf("explicit picks: %v, want %v", modelIDs(exposed), p.Models)
		}
		check(exposed)
	}
	if chatCalls.Load() != 4 || nativeCalls.Load() != 4 || nativeFailures.Load() != 1 {
		t.Fatalf("list requests: Chat %d, Gemini %d, native failures %d", chatCalls.Load(), nativeCalls.Load(), nativeFailures.Load())
	}
}

// Adding a decision endpoint to a Gemini conversation provider does not
// classify its conversation models as decision models or replace its list.
func TestMixedGeminiDecideKeepsConversationPicks(t *testing.T) {
	vertexHome(t)
	const key = "mixed-decision-test-key"
	var nativeCalls, decisionCalls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native/v1beta/models" {
			decisionCalls.Add(1)
			http.NotFound(w, r)
			return
		}
		nativeCalls.Add(1)
		if r.Header.Get("x-goog-api-key") != key || r.Header.Get("Authorization") != "" {
			t.Errorf("Gemini list has unexpected auth %q / %q", r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]any{"models": []map[string]any{
			{"name": "models/gemini-2.5-flash", "displayName": "Gemini conversation", "supportedGenerationMethods": []string{"generateContent"}, "inputTokenLimit": 1048576},
		}})
	}))
	defer up.Close()
	p := Provider{ID: "mixed-native-decide", Name: "Mixed native and decide", Key: key,
		Gemini: up.URL + "/native/v1beta", Decide: up.URL + "/decisions/v1", Models: []string{"gemini-2.5-flash"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	p = *got
	if p.DecideOnly() || p.DecidesModel(p.Models[0]) || !p.DecidesModel("jev-latest") {
		t.Fatalf("mixed decision classification: only %v, Gemini decision %v, Jev decision %v", p.DecideOnly(), p.DecidesModel(p.Models[0]), p.DecidesModel("jev-latest"))
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]catalog.Model{ms, p.Exposed()} {
		if !slices.Equal(modelIDs(list), p.Models) || list[0].Name != "Gemini conversation" || list[0].Context != 1048576 || !slices.Equal(list[0].APIs, []string{"gemini"}) || len(list[0].Keys) != 0 {
			t.Fatalf("mixed Gemini conversation list: %+v", list)
		}
	}
	if nativeCalls.Load() != 1 || decisionCalls.Load() != 0 {
		t.Fatalf("conversation list requests: native %d, decision %d", nativeCalls.Load(), decisionCalls.Load())
	}
}
