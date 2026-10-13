package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestNoListPlanIgnoresCachedDrawers(t *testing.T) {
	fresh(t)
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key = "ark-cache", "fixture-key"
	pr := provider.Preset("volcengine")
	p.Chat, p.Responses, p.Anthropic = pr.Regions[2].Chat, pr.Regions[2].Responses, pr.Regions[2].Anthropic
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{{ID: "old-paid-drawer", Draws: true}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(catalog.LivePath(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if ds := Drawers(p); len(ds) != 1 || ds[0].ID != "old-paid-drawer" {
		t.Fatalf("pay-as-you-go drawer list: %+v", ds)
	}
	p.Chat, p.Responses, p.Anthropic = pr.Regions[0].Chat, pr.Regions[0].Responses, pr.Regions[0].Anthropic
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if ds := Drawers(p); len(ds) != 0 {
		t.Fatalf("Coding Plan read a former endpoint's drawers: %+v", ds)
	}
	if got := AutoDrawer(); got != "" {
		t.Fatalf("Coding Plan automatically selected a cached drawer: %q", got)
	}
	after, err := os.ReadFile(catalog.LivePath(p.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the former cache was changed: %v", err)
	}
	p.Models = []string{"gpt-image-1"}
	if ds := Drawers(p); len(ds) != 1 || ds[0].ID != "gpt-image-1" {
		t.Fatalf("explicit drawer was lost: %+v", ds)
	}
	// A custom provider whose cached list has only image models still reads it.
	p.Preset, p.Models = "", nil
	if ds := Drawers(p); len(ds) != 1 || ds[0].ID != "old-paid-drawer" {
		t.Fatalf("ordinary image-only provider's cache was lost: %+v", ds)
	}
}

// These focused plan-media cases and video response fixtures come from
// yetone/magpie a75de4c21ac28be98af8334643077d8b0bf47df7.
// The request fixture carries WhaleBridge's private admission key.

func TestVolcengineAgentPlanDrawers(t *testing.T) {
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("volcengine")
	p.Chat, p.Responses, p.Anthropic = pr.Regions[1].Chat, pr.Regions[1].Responses, pr.Regions[1].Anthropic
	ds, want := Drawers(p), p.PlanDrawers()
	if len(ds) == 0 || !slices.EqualFunc(ds, want, func(a, b catalog.Model) bool { return a.ID == b.ID }) {
		t.Fatalf("Agent Plan drawers: %+v, want preset plan's %+v", ds, want)
	}
	p.Chat, p.Responses, p.Anthropic = pr.Regions[0].Chat, pr.Regions[0].Responses, pr.Regions[0].Anthropic
	if ds := Drawers(p); len(ds) != 0 {
		t.Fatalf("Coding Plan drawers: %+v", ds)
	}
}

func TestPresetPlanEmbeddingResolvesOutsideChatModels(t *testing.T) {
	fresh(t)
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("volcengine")
	p.ID = "ark-agent"
	p.Chat, p.Responses, p.Anthropic = pr.Regions[1].Chat, pr.Regions[1].Responses, pr.Regions[1].Anthropic
	p.Key = "key"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	embed := p.PlanEmbeddings()
	if len(embed) == 0 {
		t.Fatal("Agent Plan has no preset embedding model")
	}
	for _, m := range p.Exposed() {
		if m.ID == embed[0].ID {
			t.Fatalf("embedding model exposed for chat: %+v", m)
		}
	}
	got, model, ok := resolveRetrievalModel(embed[0].ID)
	if !ok || got.ID != p.ID || model != embed[0].ID {
		t.Fatalf("bare embedding resolved to %q/%q, %v", got.ID, model, ok)
	}
}

func planMediaRequest(t *testing.T, s *Server, method, path, ct, body string) (int, map[string]any, []byte, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-private-gateway-key")
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	s.Handler().ServeHTTP(rec, req)
	var obj map[string]any
	json.Unmarshal(rec.Body.Bytes(), &obj)
	return rec.Code, obj, rec.Body.Bytes(), rec.Result().Header
}

func TestVolcengineAgentPlanVideos(t *testing.T) {
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	pr := provider.Preset("volcengine")
	p.Chat, p.Responses, p.Anthropic = pr.Regions[1].Chat, pr.Regions[1].Responses, pr.Regions[1].Anthropic
	ms, want := Videomakers(p), p.PlanVideos()
	if len(ms) == 0 || !slices.EqualFunc(ms, want, func(a, b catalog.Model) bool { return a.ID == b.ID }) {
		t.Fatalf("Agent Plan videos: %+v, want preset plan's %+v", ms, want)
	}
	p.Chat, p.Responses, p.Anthropic = pr.Regions[0].Chat, pr.Regions[0].Responses, pr.Regions[0].Anthropic
	if ms := Videomakers(p); len(ms) != 0 {
		t.Fatalf("Coding Plan videos: %+v", ms)
	}
}

func TestVolcengineVideoBody(t *testing.T) {
	f := filming{Prompt: "a magpie", Seconds: "6", Size: "1280x720", Start: &picture{Mime: "image/png", Data: []byte("png")}, References: []picture{{URL: "https://example.test/ref.png"}}}
	var got map[string]any
	if err := json.Unmarshal(volcengineVideoBody("doubao-seedance-2.5", f), &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "doubao-seedance-2.5" || got["duration"] != float64(6) || got["resolution"] != "720p" || got["ratio"] != "16:9" {
		t.Fatalf("body: %+v", got)
	}
	content, _ := got["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content: %+v", got["content"])
	}
	first, _ := content[1].(map[string]any)
	reference, _ := content[2].(map[string]any)
	if first["role"] != "first_frame" || reference["role"] != "reference_image" {
		t.Fatalf("image roles: %+v", content)
	}
}

func TestVolcengineVideoIsStartedPolledAndFetched(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var paths, bodies []string
	asked := map[string]int{}
	var up *httptest.Server
	up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(body))
		if r.URL.Path != "/ark/video.mp4" && r.Header.Get("Authorization") != "Bearer key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/plan/v3/contents/generations/tasks" && strings.Contains(string(body), "FAILING"):
			io.WriteString(w, volcengineVideoFailedCreateResponse)
		case r.Method == http.MethodPost && r.URL.Path == "/api/plan/v3/contents/generations/tasks":
			io.WriteString(w, volcengineVideoCreateResponse)
		case r.Method == http.MethodGet && r.URL.Path == "/api/plan/v3/contents/generations/tasks/cgt-20261008165954-4sc9n":
			asked["success"]++
			if asked["success"] == 1 {
				io.WriteString(w, volcengineVideoRunningResponse)
			} else {
				io.WriteString(w, strings.Replace(volcengineVideoSucceededResponse, "VIDEO_URL", up.URL+"/ark/video.mp4", 1))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/api/plan/v3/contents/generations/tasks/cgt-20261008165955-bgckq":
			io.WriteString(w, volcengineVideoFailedResponse)
		case r.Method == http.MethodGet && r.URL.Path == "/ark/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			io.WriteString(w, "ark-video-bytes")
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"code":"not_found","message":"unexpected path"}}`)
		}
	}))
	t.Cleanup(up.Close)

	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key = "ark-agent", "key"
	p.Chat, p.Responses = up.URL+"/api/plan/v3", up.URL+"/api/plan/v3"
	p.Anthropic = up.URL + "/api/plan"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}

	s := New()
	code, obj, raw, _ := planMediaRequest(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"ark-agent/doubao-seedance-2.0-mini","prompt":"a magpie","seconds":5,"size":"854x480"}`)
	if code != 200 || obj["status"] != "queued" || obj["model"] != "ark-agent/doubao-seedance-2.0-mini" {
		t.Fatalf("create: %d %s", code, raw)
	}
	id, _ := obj["id"].(string)
	code, obj, raw, _ = planMediaRequest(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "in_progress" {
		t.Fatalf("running: %d %s", code, raw)
	}
	code, obj, raw, _ = planMediaRequest(t, s, http.MethodGet, "/v1/videos/"+id, "", "")
	if code != 200 || obj["status"] != "completed" || obj["seconds"] != "5" {
		t.Fatalf("succeeded: %d %s", code, raw)
	}
	code, _, raw, head := planMediaRequest(t, s, http.MethodGet, "/v1/videos/"+id+"/content", "", "")
	if code != 200 || string(raw) != "ark-video-bytes" || head.Get("Content-Type") != "video/mp4" {
		t.Fatalf("content: %d %q %v", code, raw, head)
	}

	code, failed, raw, _ := planMediaRequest(t, s, http.MethodPost, "/v1/videos", "application/json", `{"model":"ark-agent/doubao-seedance-2.0-mini","prompt":"FAILING"}`)
	if code != 200 {
		t.Fatalf("create failing: %d %s", code, raw)
	}
	code, failed, raw, _ = planMediaRequest(t, s, http.MethodGet, "/v1/videos/"+failed["id"].(string), "", "")
	errObj, _ := failed["error"].(map[string]any)
	if code != 200 || failed["status"] != "failed" || errObj["code"] != "OutputVideoSensitiveContentDetected.PolicyViolation" || !strings.Contains(errObj["message"].(string), "copyright restrictions") {
		t.Fatalf("failed: %d %s", code, raw)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) < 7 || paths[0] != "POST /api/plan/v3/contents/generations/tasks" || !strings.Contains(bodies[0], `"content":[`) || !strings.Contains(bodies[0], `"duration":5`) || !slices.Contains(paths, "GET /ark/video.mp4") {
		t.Fatalf("upstream paths %v, create body %s", paths, bodies[0])
	}
}

func TestVolcengineVideoState(t *testing.T) {
	var st videoState
	if err := json.Unmarshal([]byte(`{"status":"succeeded","model":"doubao-seedance-2.5","content":{"video_url":"https://example.test/video.mp4"},"duration":5}`), &st); err != nil {
		t.Fatal(err)
	}
	obj := videoObject("video_volcengine.task.1.0", "volcengine/"+st.Model, time.Unix(1, 0), st, filming{})
	if obj["status"] != "completed" || obj["progress"] != 100 || obj["seconds"] != "5" || st.Content.VideoURL != "https://example.test/video.mp4" {
		t.Fatalf("completed state: %+v, object: %+v", st, obj)
	}
	for status, want := range map[string]string{"queued": "queued", "running": "in_progress", "cancelled": "failed"} {
		st = videoState{Status: status}
		if got := videoObject("id", "model", time.Unix(1, 0), st, filming{})["status"]; got != want {
			t.Errorf("%s became %v, want %s", status, got, want)
		}
	}
}

const volcengineVideoCreateResponse = `{
  "id": "cgt-20261008165954-4sc9n"
}`

const volcengineVideoRunningResponse = `{
  "id": "cgt-20261008165954-4sc9n",
  "model": "doubao-seedance-2.0-mini",
  "status": "running",
  "created_at": 1791449995,
  "updated_at": 1791449995,
  "service_tier": "default",
  "execution_expires_after": 172800,
  "generate_audio": true,
  "draft": false,
  "priority": 0
}`

const volcengineVideoSucceededResponse = `{
  "id": "cgt-20261008165954-4sc9n",
  "model": "doubao-seedance-2.0-mini",
  "status": "succeeded",
  "content": {
    "video_url": "VIDEO_URL"
  },
  "usage": {
    "completion_tokens": 48400,
    "total_tokens": 48400
  },
  "created_at": 1791449995,
  "updated_at": 1791450109,
  "seed": 14410,
  "resolution": "480p",
  "ratio": "1:1",
  "duration": 5,
  "framespersecond": 24,
  "service_tier": "default",
  "execution_expires_after": 172800,
  "generate_audio": true,
  "draft": false,
  "priority": 0
}`

const volcengineVideoFailedCreateResponse = `{
  "id": "cgt-20261008165955-bgckq"
}`

const volcengineVideoFailedResponse = `{
  "id": "cgt-20261008165955-bgckq",
  "model": "doubao-seedance-2.0-mini",
  "status": "failed",
  "error": {
    "code": "OutputVideoSensitiveContentDetected.PolicyViolation",
    "message": "The request failed because the output video may be related to copyright restrictions. Request id: 02179144999552100000000000000000000ffffac155925e93938"
  },
  "created_at": 1791449995,
  "updated_at": 1791450125,
  "service_tier": "default",
  "execution_expires_after": 172800,
  "generate_audio": true,
  "draft": false,
  "priority": 0
}`
