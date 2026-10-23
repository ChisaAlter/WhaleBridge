package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

var mediaPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0h8AAAAASUVORK5CYII=")

func mediaCall(t *testing.T, h http.Handler, method, path, body, remote, xff, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = remote
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func mediaAllow(t *testing.T, hosts ...string) {
	t.Helper()
	st := settings.Load()
	st.FetchPrivate = hosts
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

func mediaNoAuth(t *testing.T, r *http.Request) {
	t.Helper()
	for _, field := range []string{"Authorization", "X-API-Key", "X-Goog-API-Key", "Cookie", "X-Forwarded-For"} {
		if r.Header.Get(field) != "" {
			t.Errorf("media GET received %s: %q", field, r.Header.Get(field))
		}
	}
}

func TestMediaInputRequiresExplicitPrivatePermission(t *testing.T) {
	fresh(t)
	var inputs, edits, otherReads atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { otherReads.Add(1); mediaNoAuth(t, r); w.Write(mediaPNG) }))
	t.Cleanup(other.Close)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/input.png":
			inputs.Add(1)
			mediaNoAuth(t, r)
			w.Header().Set("Content-Type", "image/png")
			w.Write(mediaPNG)
		case "/redirect.png":
			inputs.Add(1)
			mediaNoAuth(t, r)
			http.Redirect(w, r, other.URL+"/private.png", http.StatusFound)
		case "/v1/images/edits":
			edits.Add(1)
			if r.Header.Get("Authorization") != "Bearer provider-media-key" {
				t.Error("images API lost provider authentication")
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			} else {
				defer r.MultipartForm.RemoveAll()
				file, _, err := r.FormFile("image")
				if err != nil {
					t.Error(err)
				} else {
					b, _ := io.ReadAll(file)
					file.Close()
					if !bytes.Equal(b, mediaPNG) {
						t.Error("input image bytes changed")
					}
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(mediaPNG)}}})
		default:
			t.Errorf("unexpected input fixture request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "input-media", Name: "Input media", Chat: up.URL + "/v1", Key: "provider-media-key", Models: []string{"gpt-image-1"}}); err != nil {
		t.Fatal(err)
	}
	h := New().Handler()
	body := func(src string) string {
		b, _ := json.Marshal(map[string]any{"model": "input-media/gpt-image-1", "prompt": "edit image", "image": src})
		return string(b)
	}
	for _, caller := range []struct{ remote, xff string }{{"127.0.0.1:12345", ""}, {"192.0.2.21:12345", ""}, {"127.0.0.1:12345", "198.51.100.5"}, {"192.0.2.21:12345", "127.0.0.1"}} {
		w := mediaCall(t, h, "POST", "/v1/images/edits", body(up.URL+"/input.png"), caller.remote, caller.xff, "")
		if w.Code != 401 {
			t.Fatalf("missing private auth %d %s", w.Code, w.Body)
		}
		w = mediaCall(t, h, "POST", "/v1/images/edits", body(up.URL+"/input.png"), caller.remote, caller.xff, "test-private-gateway-key")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "private address") {
			t.Fatalf("caller %+v bypassed input guard: %d %s", caller, w.Code, w.Body)
		}
	}
	if inputs.Load() != 0 || edits.Load() != 0 {
		t.Fatalf("unapproved input or API reached: input=%d edits=%d", inputs.Load(), edits.Load())
	}
	mediaAllow(t, up.Listener.Addr().String())
	w := mediaCall(t, h, "POST", "/v1/images/edits", body(up.URL+"/input.png"), "192.0.2.21:12345", "127.0.0.1", "test-private-gateway-key")
	if w.Code != 200 || inputs.Load() != 1 || edits.Load() != 1 {
		t.Fatalf("explicit input permission: %d %s input=%d edits=%d", w.Code, w.Body, inputs.Load(), edits.Load())
	}
	for _, src := range []string{other.URL + "/private.png", up.URL + "/redirect.png"} {
		w = mediaCall(t, h, "POST", "/v1/images/edits", body(src), "127.0.0.1:12345", "", "test-private-gateway-key")
		if w.Code != 400 || !strings.Contains(w.Body.String(), "private address") {
			t.Fatalf("input port/redirect allowed %s: %d %s", src, w.Code, w.Body)
		}
	}
	if otherReads.Load() != 0 || edits.Load() != 1 {
		t.Fatalf("input permission reached different port or API: %d/%d", otherReads.Load(), edits.Load())
	}
}

// The same fixture exposes an images API and an Ark status API. Media URL
// downloads carry no provider credential, even when they stay on its own host.
func mediaVendor(t *testing.T, source *atomic.Value, mediaReads, apiReads *atomic.Int32) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/images/generations":
			apiReads.Add(1)
			if r.Header.Get("Authorization") != "Bearer provider-media-key" {
				t.Error("image API authentication missing")
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"url": source.Load().(string)}}})
		case "/api/plan/v3/contents/generations/tasks/media-task":
			apiReads.Add(1)
			if r.Header.Get("Authorization") != "Bearer provider-media-key" {
				t.Error("video status authentication missing")
			}
			json.NewEncoder(w).Encode(map[string]any{"model": "doubao-seedance-2.0-mini", "status": "succeeded", "content": map[string]string{"video_url": source.Load().(string)}})
		case "/own.png":
			mediaReads.Add(1)
			mediaNoAuth(t, r)
			w.Write(mediaPNG)
		case "/own.mp4":
			mediaReads.Add(1)
			mediaNoAuth(t, r)
			io.WriteString(w, "safe-video-bytes")
		case "/redirect":
			mediaReads.Add(1)
			mediaNoAuth(t, r)
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
		default:
			t.Errorf("unexpected media API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	return up
}

func saveMediaVendors(t *testing.T, base, proxy string) provider.Provider {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "draw-media", Name: "Draw media", Chat: base + "/v1", Key: "provider-media-key", Models: []string{"gpt-image-1"}, Proxy: proxy}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.FromPreset("volcengine")
	if err != nil {
		t.Fatal(err)
	}
	p.ID, p.Key, p.Proxy = "video-media", "provider-media-key", proxy
	p.Chat, p.Responses, p.Anthropic = base+"/api/plan/v3", base+"/api/plan/v3", base+"/api/plan"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	if len(p.PlanVideos()) == 0 {
		t.Fatal("video fixture is not recognized as Ark Agent Plan")
	}
	return p
}

func TestMediaVendorURLScopeAndFallback(t *testing.T) {
	fresh(t)
	var source atomic.Value
	source.Store("")
	var mediaReads, apiReads, otherReads atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherReads.Add(1)
		mediaNoAuth(t, r)
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Write(mediaPNG)
		} else {
			io.WriteString(w, "safe-video-bytes")
		}
	}))
	t.Cleanup(other.Close)
	up := mediaVendor(t, &source, &mediaReads, &apiReads)
	p := saveMediaVendors(t, up.URL, "")
	h := New().Handler()
	videoPath := "/v1/videos/" + videoID(p, "media-task", time.Now()) + "/content"
	drawBody := `{"model":"draw-media/gpt-image-1","prompt":"make image","response_format":"b64_json"}`
	for _, kind := range []string{"image", "video"} {
		ext := ".png"
		if kind == "video" {
			ext = ".mp4"
		}
		own := up.URL + "/own" + ext
		foreign := other.URL + "/private" + ext
		for _, tc := range []struct {
			name, url string
			allowed   bool
		}{{"own", own, true}, {"same-host-other-port", foreign, false}, {"redirect-other-port", up.URL + "/redirect?to=" + foreign, false}} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				mediaAllow(t)
				source.Store(tc.url)
				method, path, body := "POST", "/v1/images/generations", drawBody
				if kind == "video" {
					method, path, body = "GET", videoPath, ""
				}
				before := apiReads.Load()
				w := mediaCall(t, h, method, path, body, "127.0.0.1:12345", "", "wrong-private-key")
				if w.Code != 401 || apiReads.Load() != before {
					t.Fatalf("media content bypassed component auth %d %s", w.Code, w.Body)
				}
				w = mediaCall(t, h, method, path, body, "192.0.2.22:12345", "127.0.0.1", "test-private-gateway-key")
				if kind == "image" {
					var out struct {
						Data []struct {
							URL string `json:"url"`
							B64 string `json:"b64_json"`
						}
					}
					if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Data) != 1 {
						t.Fatalf("image response %d %s", w.Code, w.Body)
					}
					if tc.allowed {
						b, _ := base64.StdEncoding.DecodeString(out.Data[0].B64)
						if out.Data[0].URL != "" || !bytes.Equal(b, mediaPNG) {
							t.Fatalf("own image not inlined: %s", w.Body)
						}
					} else if out.Data[0].URL != tc.url || out.Data[0].B64 != "" {
						t.Fatalf("paid URL lost on guard refusal: %s", w.Body)
					}
				} else if tc.allowed {
					if w.Code != 200 || w.Body.String() != "safe-video-bytes" {
						t.Fatalf("own video download %d %s", w.Code, w.Body)
					}
				} else if w.Code != 502 || !strings.Contains(w.Body.String(), "the video couldn't be fetched") || !strings.Contains(w.Body.String(), "private address") {
					t.Fatalf("guard refusal not video502: %d %s", w.Code, w.Body)
				}
				if otherReads.Load() != 0 {
					t.Fatal("own endpoint permission leaked to other port")
				}
			})
		}
		// Explicitly approving only the other media service makes its URL
		// and an own-host redirect to it usable, without auth propagation.
		mediaAllow(t, other.Listener.Addr().String())
		source.Store(up.URL + "/redirect?to=" + foreign)
		method, path, body := "POST", "/v1/images/generations", drawBody
		if kind == "video" {
			method, path, body = "GET", videoPath, ""
		}
		w := mediaCall(t, h, method, path, body, "127.0.0.1:12345", "198.51.100.7", "test-private-gateway-key")
		if w.Code != 200 {
			t.Fatalf("explicit media permission %d %s", w.Code, w.Body)
		}
		if kind == "image" && !strings.Contains(w.Body.String(), "b64_json") {
			t.Fatalf("approved image not inline: %s", w.Body)
		}
		otherReads.Store(0)
	}
	if mediaReads.Load() == 0 {
		t.Fatal("own-host fixture never downloaded media")
	}
}

func TestMediaDownloadsPreserveProviderProxy(t *testing.T) {
	fresh(t)
	var source atomic.Value
	source.Store("http://cdn.media.test/file.png")
	var mediaReads, apiReads, viaProxy, globalProxy atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxy.Add(1)
		mediaNoAuth(t, r)
		if r.URL.Host != "93.184.216.34:80" || r.Host != "93.184.216.34:80" {
			t.Errorf("proxy URL not pinned: %s Host %s", r.URL, r.Host)
		}
		if strings.HasSuffix(r.URL.Path, ".png") {
			w.Write(mediaPNG)
		} else {
			io.WriteString(w, "proxy-video-bytes")
		}
	}))
	t.Cleanup(proxy.Close)
	wrongProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { globalProxy.Add(1); http.Error(w, "wrong proxy", 502) }))
	t.Cleanup(wrongProxy.Close)
	oldLookup := lookupIP
	t.Cleanup(func() { lookupIP = oldLookup })
	lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host != "cdn.media.test" {
			return nil, fmt.Errorf("unexpected DNS lookup %s", host)
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	st := settings.Load()
	st.Proxy = wrongProxy.URL
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	up := mediaVendor(t, &source, &mediaReads, &apiReads)
	p := saveMediaVendors(t, up.URL, proxy.URL)
	h := New().Handler()
	w := mediaCall(t, h, "POST", "/v1/images/generations", `{"model":"draw-media/gpt-image-1","prompt":"make image","response_format":"b64_json"}`, "127.0.0.1:12345", "", "test-private-gateway-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "b64_json") {
		t.Fatalf("provider image proxy %d %s", w.Code, w.Body)
	}
	source.Store("http://cdn.media.test/file.mp4")
	w = mediaCall(t, h, "GET", "/v1/videos/"+videoID(p, "media-task", time.Now())+"/content", "", "127.0.0.1:12345", "", "test-private-gateway-key")
	if w.Code != 200 || w.Body.String() != "proxy-video-bytes" {
		t.Fatalf("provider video proxy %d %s", w.Code, w.Body)
	}
	if viaProxy.Load() != 2 || globalProxy.Load() != 0 {
		t.Fatalf("media lost provider Via route: own=%d global=%d", viaProxy.Load(), globalProxy.Load())
	}
}
