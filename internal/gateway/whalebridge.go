package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/ChisaAlter/WhaleBridge/internal/usage"
)

var whaleBridgeRequests struct {
	sync.Mutex
	active   int
	draining bool
}

// Freeze admission atomically before a stop/update. An existing stream keeps
// its own request, including tool calls; maintenance never cuts it off.
func (s *Server) WhaleBridgeMaintenance(on bool) (int, bool) {
	whaleBridgeRequests.Lock()
	defer whaleBridgeRequests.Unlock()
	active := whaleBridgeRequests.active + s.subscription.parked()
	if on && active > 0 {
		return active, false
	}
	whaleBridgeRequests.draining = on
	return active, true
}

func (s *Server) Handler() http.Handler {
	next := s.upstreamHandler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == "/whalebridge/identity" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"pid": os.Getpid(), "version": Version})
			return
		}
		// Claude's stdio helper authenticates with the run's private token.
		// This internal callback is never available to a remote or browser caller.
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_magpie/claude-mcp/") {
			if !local(r) || !loopbackHost(r.Host) || r.Header.Get("Origin") != "" {
				http.Error(w, "invalid Claude helper callback", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Codex's built-in provider keeps its own sign-in in Authorization.
		// Its private endpoint authenticates this hop without replacing that
		// sign-in, which the backend still needs for native OpenAI models.
		key := os.Getenv("WHALEBRIDGE_GATEWAY_TOKEN")
		privatePath := false
		if strings.HasPrefix(r.URL.Path, "/_whalebridge/") {
			credential, path, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/_whalebridge/"), "/")
			if !ok || key == "" || subtle.ConstantTimeCompare([]byte(credential), []byte(key)) != 1 {
				http.Error(w, "invalid WhaleBridge gateway key", http.StatusUnauthorized)
				return
			}
			r = r.Clone(r.Context())
			u := *r.URL
			u.Path, u.RawPath = "/"+path, ""
			r.URL = &u
			privatePath = strings.HasPrefix(u.Path, CodexPath+"/")
			// Claude's claude.ai sign-in also belongs to the client, not to
			// this hop. Its private address is limited to Messages and token
			// counting; it cannot authenticate another provider API.
			if path, ok := strings.CutPrefix(u.Path, "/anthropic"); ok && r.Method == http.MethodPost {
				switch path {
				case "/v1/messages", "/messages", "/v1/messages/count_tokens":
					u.Path, privatePath = path, true
				}
			}
			if !privatePath {
				http.NotFound(w, r)
				return
			}
		}
		// Provider APIs share the component's private gateway key. Client
		// installers, public caller keys and MCP management remain outside it.
		if !whaleBridgeAPI(r.Method, r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		if !privatePath && !whaleBridgeAuthorized(r, key) {
			http.Error(w, "invalid WhaleBridge gateway key", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/v1/videos/") && !strings.HasPrefix(r.URL.Path, "/videos/") {
			next.ServeHTTP(w, r)
			return
		}
		whaleBridgeRequests.Lock()
		if whaleBridgeRequests.draining {
			whaleBridgeRequests.Unlock()
			http.Error(w, "鲸桥正在更新或停止，请稍后再试", http.StatusServiceUnavailable)
			return
		}
		whaleBridgeRequests.active++
		whaleBridgeRequests.Unlock()
		defer func() { whaleBridgeRequests.Lock(); whaleBridgeRequests.active--; whaleBridgeRequests.Unlock() }()
		next.ServeHTTP(w, r)
	})
}

func WhaleBridgeCodexURL() string {
	if key := os.Getenv("WHALEBRIDGE_GATEWAY_TOKEN"); key != "" {
		return URL() + "/_whalebridge/" + key + CodexPath
	}
	return URL() + CodexPath
}

func WhaleBridgeAnthropicURL() string {
	if key := os.Getenv("WHALEBRIDGE_GATEWAY_TOKEN"); key != "" {
		return URL() + "/_whalebridge/" + key + "/anthropic"
	}
	return URL()
}

func whaleBridgeAuthorized(r *http.Request, key string) bool {
	if key == "" {
		return false
	}
	for _, value := range callerKeys(r) {
		if subtle.ConstantTimeCompare([]byte(value), []byte(key)) == 1 {
			return true
		}
		if len(value) <= len(key)+1 || value[len(key)] != '-' || subtle.ConstantTimeCompare([]byte(value[:len(key)]), []byte(key)) != 1 {
			continue
		}
		id := value[len(key)+1:]
		if usage.Agents != nil && slices.ContainsFunc(usage.Agents(), func(a usage.Known) bool { return a.ID == id }) {
			return true
		}
	}
	return false
}

func whaleBridgeAPI(method, path string) bool {
	switch method {
	case http.MethodPost:
		switch path {
		case CodexPath + "/responses", CodexPath + "/responses/compact", CodexPath + AlphaSearchPath:
			return true
		case "/v1/chat/completions", "/chat/completions", "/v1/responses", "/responses", "/v1/messages", "/messages",
			"/v1/messages/count_tokens", "/v1/systemone", "/v1" + AlphaSearchPath, AlphaSearchPath, "/v1/images/generations", "/images/generations",
			"/v1/images/edits", "/images/edits", "/v1/embeddings", "/embeddings", "/v1/rerank", "/rerank", "/v1/videos", "/videos":
			return true
		}
		return strings.HasPrefix(path, "/v1beta/models/")
	case http.MethodGet:
		switch path {
		case CodexPath + "/models", CodexPath + "/responses":
			return true
		case "/v1/models", "/models", CodexCatalogPath, "/v1beta/models", "/v1/responses", "/responses",
			"/v1/magpie/quotas", "/v1/magpie/quotas/history", "/v1/magpie/usage", "/v1/magpie/usage/requests", "/v1/magpie/route", "/v1/magpie/concurrency":
			return true
		}
		return strings.HasPrefix(path, "/v1/models/") || strings.HasPrefix(path, "/v1/videos/") || strings.HasPrefix(path, "/videos/")
	}
	return false
}
