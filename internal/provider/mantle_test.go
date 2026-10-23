package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
)

func TestMantlePresetAndSharedBedrockRules(t *testing.T) {
	isolate(t)
	p, err := FromPreset("bedrock-mantle")
	if err != nil {
		t.Fatal(err)
	}
	pr := Preset("bedrock-mantle")
	if !pr.Hosts || pr.NoList || len(pr.Models) != 0 || len(pr.Regions) != 17 || !p.lists() || !p.IsBedrock() || !p.IsBedrockMantle() {
		t.Fatalf("Mantle preset/listing %+v, %+v", p, pr)
	}
	for _, r := range pr.Regions {
		root := "https://bedrock-mantle." + r.ID + ".api.aws"
		if r.ID == "eu-west-3" || r.Chat != root+"/v1" || r.Responses != r.Chat || r.Anthropic != root+"/anthropic" {
			t.Fatalf("Mantle region %+v", r)
		}
	}
	if !Preset("bedrock").NoList {
		t.Fatal("runtime lost its no-list rule")
	}
	if runtime, _ := FromPreset("bedrock"); runtime.IsBedrockMantle() {
		t.Fatal("runtime became Mantle")
	}
	for model, want := range map[string][]Protocol{
		"anthropic.claude-opus-4-7": {Anthropic},
		"openai.gpt-5.5":            {Responses, Chat},
		"openai.gpt-oss-120b":       {Chat},
		"google.gemma-3-27b-it":     {Chat},
	} {
		if got := p.APIs(model); !slices.Equal(got, want) {
			t.Errorf("%s APIs %v, want %v", model, got, want)
		}
		if CanFast(p, model) {
			t.Errorf("Mantle offered priority for %s", model)
		}
	}
	p.Key = "synthetic-bedrock"
	if got := AuthHeaders(p, Anthropic); len(got) != 1 || got["x-api-key"] != p.Key {
		t.Fatalf("Messages auth %v", got)
	}
	if got := AuthHeaders(p, Chat); got["Authorization"] != "Bearer "+p.Key {
		t.Fatalf("Chat auth %v", got)
	}
	for _, tc := range []struct {
		proto       Protocol
		model, path string
	}{
		{Chat, "openai.gpt-oss-120b", "/v1/chat/completions"},
		{Chat, "openai.gpt-5.5", "/openai/v1/chat/completions"},
		{Responses, "xai.grok-4.3", "/openai/v1/responses"},
		{Anthropic, "anthropic.claude-opus-4-7", "/anthropic/v1/messages"},
	} {
		u, body := tinyBody(p, tc.proto, tc.model)
		if u != "https://bedrock-mantle.us-east-1.api.aws"+tc.path {
			t.Errorf("%s probe %s", tc.model, u)
		}
		if tc.proto == Chat && (!strings.Contains(body, "max_completion_tokens") || strings.Contains(body, `"max_tokens"`)) {
			t.Errorf("Chat probe %s", body)
		}
	}
}

func TestMantleByAddressAndBase(t *testing.T) {
	isolate(t)
	root := "https://bedrock-mantle.eu-west-1.api.aws"
	for _, p := range []Provider{{Chat: root + "/v1"}, {Chat: root + "/openai/v1/"}, {Responses: root + "/openai/v1"}, {Anthropic: root + "/anthropic/v1/messages"}} {
		p = normalize(p)
		if !p.IsBedrockMantle() || !p.IsBedrock() || p.Chat != root+"/v1" || p.Responses != p.Chat || p.Anthropic != root+"/anthropic" {
			t.Fatalf("normalized endpoints %+v", p)
		}
	}
	for _, base := range []string{"https://bedrock-mantle.example.com/v1", "https://api.aws/v1", "https://relay.invalid/bedrock-mantle.us-east-1.api.aws/v1"} {
		p := normalize(Provider{Chat: base})
		if p.IsBedrockMantle() || p.IsBedrock() || p.Anthropic != "" {
			t.Fatalf("false Mantle: %+v", p)
		}
	}
	if p := normalize(Provider{Preset: "openrouter", Chat: root + "/v1"}); p.Anthropic != "" {
		t.Fatal("another preset's endpoints changed")
	}
	for _, tc := range []struct {
		base, model string
		other       bool
		want        string
	}{
		{root + "/v1", "openai.gpt-oss-120b", false, root + "/v1"},
		{root + "/v1", "global.openai.gpt-6-sol", false, root + "/openai/v1"},
		{root + "/openai/v1/", "xai.grok-4.3", false, root + "/openai/v1"},
		{root + "/v1", "newmaker.model", true, root + "/openai/v1"},
		{root + "/v1", "openai.gpt-5.5", true, root + "/v1"},
		{root + "/custom", "openai.gpt-5.5", false, root + "/custom"},
	} {
		if got := MantleBase(tc.base, tc.model, tc.other); got != tc.want {
			t.Errorf("MantleBase %s/%s: %s want %s", tc.base, tc.model, got, tc.want)
		}
	}
}

func TestMantleFetchUsesLiveListAtV1(t *testing.T) {
	isolate(t)
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Errorf("list headers %v", r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "openai.gpt-5.5"}, {"id": "google.gemma-3-27b-it"}}})
	}))
	t.Cleanup(srv.Close)
	p, _ := FromPreset("bedrock-mantle")
	p.Chat, p.Responses, p.Anthropic, p.Key = srv.URL+"/v1", srv.URL+"/v1", srv.URL+"/anthropic", "synthetic"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil || !slices.Equal(calls, []string{"/v1/models"}) || !slices.Equal(modelIDs(ms), []string{"openai.gpt-5.5", "google.gemma-3-27b-it"}) {
		t.Fatalf("list %v paths %v err %v", ms, calls, err)
	}
	if cached, _, ok := catalog.Live(p.ID); !ok || len(cached) != 2 {
		t.Fatal("Mantle live models were not saved")
	}
}
