package imagemcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/gateway"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

func TestMCPMediaURLFallbackUsesGuard(t *testing.T) {
	testenv.SetHome(t, t.TempDir())
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(name, "")
	}
	if err := settings.Save(settings.Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a0h8AAAAASUVORK5CYII=")
	var reads, privateReads atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { privateReads.Add(1); w.Write(png) }))
	t.Cleanup(other.Close)
	media := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("MCP media fallback leaked gateway credentials")
		}
		if r.URL.Path == "/redirect.png" {
			http.Redirect(w, r, other.URL+"/private.png", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	t.Cleanup(media.Close)
	var source atomic.Value
	source.Store(media.URL + "/paid.png")
	var gatewayCalls atomic.Int32
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls.Add(1)
		if r.URL.Path != "/v1/images/generations" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+gateway.Token {
			t.Errorf("MCP gateway route/auth %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			ResponseFormat string `json:"response_format"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		if request.ResponseFormat != "b64_json" {
			t.Error("MCP did not request inline image")
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture/image", "data": []any{map[string]any{"url": source.Load().(string)}}})
	}))
	t.Cleanup(bridge.Close)
	call := func(path string) (bool, string) {
		t.Helper()
		in, out := io.Pipe()
		serverOut, serverIn := io.Pipe()
		s := &server{gateway: bridge.URL, client: bridge.Client(), enc: json.NewEncoder(serverIn), agent: gateway.DrawAgent, pending: map[string]chan rpc{}}
		done := make(chan error, 1)
		go func() { done <- s.serve(in) }()
		enc, dec := json.NewEncoder(out), json.NewDecoder(serverOut)
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "media-fixture"}}}); err != nil {
			t.Fatal(err)
		}
		var initialized rpc
		if err := dec.Decode(&initialized); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": map[string]string{"prompt": "a media fixture", "path": path}}}); err != nil {
			t.Fatal(err)
		}
		var reply rpc
		if err := dec.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		out.Close()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		s.running.Wait()
		serverIn.Close()
		serverOut.Close()
		in.Close()
		var result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			}
		}
		if len(reply.Error) != 0 || json.Unmarshal(reply.Result, &result) != nil || len(result.Content) != 1 {
			t.Fatalf("MCP reply %+v", reply)
		}
		return result.IsError, result.Content[0].Text
	}
	blocked := filepath.Join(project, "blocked.png")
	failed, text := call(blocked)
	if !failed || !strings.Contains(text, "private address") || !strings.Contains(text, source.Load().(string)) {
		t.Fatalf("MCP lost blocked paid URL: error=%v %s", failed, text)
	}
	if reads.Load() != 0 {
		t.Fatal("unauthorized local fallback reached private server")
	}
	if _, err := os.Stat(blocked); !os.IsNotExist(err) {
		t.Fatalf("blocked media wrote a file: %v", err)
	}
	st := settings.Load()
	st.FetchPrivate = []string{media.Listener.Addr().String()}
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(project, "approved.png")
	failed, text = call(approved)
	if failed || !strings.Contains(text, "saved to:") {
		t.Fatalf("explicit MCP fallback failed: %v %s", failed, text)
	}
	b, err := os.ReadFile(approved)
	if err != nil || !bytes.Equal(b, png) {
		t.Fatalf("saved media changed: %v", err)
	}
	if reads.Load() != 1 {
		t.Fatalf("approved fallback reads %d", reads.Load())
	}
	source.Store(media.URL + "/redirect.png")
	failed, text = call(filepath.Join(project, "redirect.png"))
	if !failed || !strings.Contains(text, source.Load().(string)) || !strings.Contains(text, "private address") {
		t.Fatalf("MCP redirect lost guard/URL %v %s", failed, text)
	}
	if privateReads.Load() != 0 {
		t.Fatal("MCP explicit port approval leaked to redirect's other port")
	}
	if gatewayCalls.Load() != 3 {
		t.Fatalf("protocol did not call real HTTP gateway three times: %d", gatewayCalls.Load())
	}
}
