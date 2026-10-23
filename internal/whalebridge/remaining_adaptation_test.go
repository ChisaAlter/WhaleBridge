package whalebridge

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestWhaleBridgeRemainingBalanceHTTP(t *testing.T) {
	h := newProviderHTTP(t)
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if r.Header.Get("Authorization") != "Bearer fixture-balance-key" {
			t.Error("balance auth lost")
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Error("custom query body should be empty")
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"remaining":12.5}`)
	}))
	t.Cleanup(srv.Close)
	id := h.save(map[string]any{"new": true, "id": "relay", "name": "Relay", "chat": h.vendor + "/v1", "key": "fixture-balance-key", "models": []string{"test-model"}, "balanceURL": srv.URL + "/balance", "balancePath": "$remaining", "balanceMethod": " post "})
	if p := h.provider(id); p.BalanceMethod != "POST" {
		t.Fatal("method was not normalized")
	}
	if supplierInfo(*h.provider(id))["balanceMethod"] != "POST" {
		t.Fatal("editor cannot reopen custom method")
	}
	h.call("POST", "provider/balance", map[string]any{"id": id}, 200)
	if len(methods) != 1 || methods[0] != "POST" {
		t.Fatalf("saved query sent %v", methods)
	}
	h.save(map[string]any{"id": id, "name": "Unrelated edit"})
	if h.provider(id).BalanceMethod != "POST" {
		t.Fatal("unrelated save erased method")
	}
	before, _ := os.ReadFile(provider.Path())
	h.call("POST", "provider/save", map[string]any{"id": id, "name": "Invalid partial save", "balanceMethod": "DELETE"}, 400)
	after, _ := os.ReadFile(provider.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("invalid method partially saved provider")
	}
	b, err := backup.Collect(false, "test")
	if err != nil {
		t.Fatal(err)
	}
	var carried provider.Provider
	for _, p := range b.Providers {
		if p.ID == id {
			carried = p
		}
	}
	if carried.BalanceMethod != "POST" || carried.Key != "" {
		t.Fatal("backup lost method or exposed key")
	}
	h.save(map[string]any{"id": id, "balanceMethod": "GET"})
	if h.provider(id).BalanceMethod != "" {
		t.Fatal("GET did not restore default")
	}
	h.call("POST", "provider/balance", map[string]any{"id": id}, 200)
	if methods[len(methods)-1] != "GET" {
		t.Fatal("default query is not GET")
	}
}

func TestWhaleBridgeRemainingPresetRoundTripHTTP(t *testing.T) {
	for _, preset := range []string{"nous", "siliconflow", "bedrock-mantle"} {
		t.Run(preset, func(t *testing.T) {
			h := newProviderHTTP(t)
			const key = "fixture-service-key-do-not-return"
			id := h.save(map[string]any{"new": true, "preset": preset, "id": preset, "name": "Fixture service", "chat": h.vendor + "/v1", "responses": "", "anthropic": "", "key": key, "models": []string{"test-model"}})
			h.call("POST", "provider/models", map[string]any{"id": id}, 200)
			p := h.provider(id)
			if p.Preset != preset || p.Key != key || p.Chat != h.vendor+"/v1" || len(p.Available()) == 0 {
				t.Fatal("preset save/list lost endpoint or key")
			}
			h.save(map[string]any{"id": id, "name": "Reopened service", "key": ""})
			if h.provider(id).Key != key {
				t.Fatal("blank edit replaced service key")
			}
			assertSecretsAbsent(t, h.call("GET", "state", nil, 200), key)
			assertSecretsAbsent(t, h.call("POST", "provider/export", map[string]any{"id": id}, 200), key)
		})
	}
}

func TestWhaleBridgeRemainingPresetDiscovery(t *testing.T) {
	h := newProviderHTTP(t)
	var state struct {
		Presets []struct {
			ID, Chat, Anthropic string
			Regions             []provider.Region
		}
	}
	if err := json.Unmarshal(h.call("GET", "state", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	foundNous, foundIntl := false, false
	for _, p := range state.Presets {
		if p.ID == "nous" {
			foundNous = p.Chat == "https://inference-api.nousresearch.com/v1" && p.Anthropic == ""
		}
		if p.ID == "siliconflow" {
			for _, r := range p.Regions {
				if r.ID == "intl" && r.Chat == "https://api.siliconflow.com/v1" {
					foundIntl = true
				}
			}
		}
	}
	if !foundNous || !foundIntl {
		t.Fatal("source picker missing Nous or international site")
	}
}
