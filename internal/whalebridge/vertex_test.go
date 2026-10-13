package whalebridge

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestProviderVertexHTTPPersistence(t *testing.T) {
	a := newProviderHTTP(t)
	credentials := filepath.Join(t.TempDir(), "missing-adc.json")
	id := a.save(map[string]any{"new": true, "preset": provider.VertexPreset, "name": "Vertex fixture", "vertex": map[string]any{
		"project": "fixture-project", "location": "us", "credentials": credentials, "impersonate": "service@fixture-project.iam.gserviceaccount.com",
	}})
	a.save(map[string]any{"id": id, "name": "Vertex edited"})
	p, err := provider.Find(id)
	if err != nil || p.Vertex == nil || p.Vertex.Project != "fixture-project" || p.Vertex.Location != "us" || p.Vertex.Credentials != credentials || p.Key != "" {
		t.Fatalf("Vertex edit did not preserve its config: %+v %v", p, err)
	}
	var state struct {
		Providers []struct {
			ID          string
			KeyOptional bool
			Vertex      *provider.Vertex
			PresetInfo  *provider.PresetDef
		}
	}
	if err := json.Unmarshal(a.call("GET", "state", nil, 200), &state); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range state.Providers {
		if row.ID != id {
			continue
		}
		found = true
		if !row.KeyOptional || row.Vertex == nil || *row.Vertex != *p.Vertex || row.PresetInfo == nil || row.PresetInfo.ID != provider.VertexPreset {
			t.Fatalf("Vertex DTO lost editable configuration or preset: %+v", row)
		}
	}
	if !found {
		t.Fatal("saved Vertex provider is missing from the component")
	}
	// Probe this explicit missing fixture path; never discover a real ADC file.
	result := a.call("POST", "provider/test", map[string]any{"id": id, "models": []string{"gemini-3.8-flash"}}, 200)
	if !strings.Contains(string(result), "missing-adc.json") || strings.Contains(string(result), `"ok":true`) {
		t.Fatalf("missing Vertex authorization was not reported: %s", result)
	}
}
