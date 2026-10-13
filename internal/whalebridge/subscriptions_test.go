package whalebridge

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
)

func TestEmptyAdapterDiscoveryDoesNotStartRuntime(t *testing.T) {
	h := newProviderHTTP(t)
	if len(plugin.Load().Plugins) != 0 || plugin.Running() {
		t.Fatal("expected an empty, stopped adapter environment")
	}
	hadBun := plugin.HasBun()
	var result struct {
		Installed []json.RawMessage `json:"installed"`
		Available []json.RawMessage `json:"available"`
		Error     string            `json:"error"`
	}
	if err := json.Unmarshal(h.call("GET", "subscription/adapters", nil, 200), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 0 || len(result.Available) == 0 || result.Error != "" {
		t.Fatalf("empty discovery lost its catalog or attempted runtime loading: %+v", result)
	}
	if plugin.Running() || plugin.HasBun() != hadBun {
		t.Fatal("browsing adapters started or downloaded a runtime")
	}
}

func TestAdapterInstallReportsFailureInRequestedFormat(t *testing.T) {
	h := newProviderHTTP(t)
	for _, accept := range []string{"application/json", "application/x-ndjson"} {
		req, err := http.NewRequest("POST", h.url+"/api/subscription/adapter", strings.NewReader(`{"action":"install","package":"invalid package"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if accept == "application/json" {
			if res.StatusCode != 400 || !strings.Contains(string(body), "isn't an npm package name") {
				t.Fatalf("JSON failure: %d %s", res.StatusCode, body)
			}
			continue
		}
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/x-ndjson" {
			t.Fatalf("stream headers: %d %v", res.StatusCode, res.Header)
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		var phase, failure map[string]any
		if err := dec.Decode(&phase); err != nil || phase["phase"] != "downloading" {
			t.Fatalf("download phase: %v %v", phase, err)
		}
		if err := dec.Decode(&failure); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(failure["error"].(string), "isn't an npm package name") {
			t.Fatalf("failure: %v", failure)
		}
		var extra map[string]any
		if err := dec.Decode(&extra); err != io.EOF {
			t.Fatalf("unexpected completion after failure: %v %v", extra, err)
		}
	}
}
