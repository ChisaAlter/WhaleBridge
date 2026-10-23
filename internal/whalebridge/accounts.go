package whalebridge

import (
	"context"
	"fmt"
	"github.com/ChisaAlter/WhaleBridge/internal/plugin"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"net/http"
	"strings"
	"time"
)

type accountInfo struct {
	provider.Login
	Alias      string `json:"alias,omitempty"`
	CheckinKey string `json:"checkinKey,omitempty"`
	CheckinOff bool   `json:"checkinOff,omitempty"`
}

func checkinKeyForQuota(q provider.SubscriptionQuota, accounts []provider.CheckinAccount) string {
	if !q.Checkins {
		return ""
	}
	for _, a := range accounts {
		if a.By == q.CheckinBy && strings.EqualFold(a.User, q.User) {
			return a.Key
		}
	}
	return ""
}

func accountCheckinKey(id, user string, accounts []provider.CheckinAccount) string {
	qs := provider.WithCheckins([]provider.SubscriptionQuota{{Provider: id, User: user}})
	return checkinKeyForQuota(qs[0], accounts)
}

func accountInfos(id string) []accountInfo {
	logins := provider.WhaleBridgeLogins(id)
	accounts := provider.CheckinAccounts()
	cards := make([]provider.SubscriptionQuota, len(logins))
	for i, l := range logins {
		cards[i] = provider.SubscriptionQuota{Provider: id, User: l.User}
	}
	cards = provider.WithCheckins(cards)
	out := make([]accountInfo, 0, len(logins))
	for i, l := range logins {
		out = append(out, accountInfo{Login: l, Alias: provider.AccountNameOf(id, l.User), CheckinKey: checkinKeyForQuota(cards[i], accounts), CheckinOff: cards[i].CheckinOff})
	}
	return out
}

func accountRoutes(mux *http.ServeMux) {
	subscriptionRoutes(mux)
	mux.HandleFunc("POST /api/subscription/prompt", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ID     string            `json:"id"`
			Method int               `json:"method"`
			Inputs map[string]string `json:"inputs"`
			Key    string            `json:"key"`
			Value  string            `json:"value"`
		}
		if err := decode(w, r, &b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		if b.Inputs == nil {
			b.Inputs = map[string]string{}
		}
		if b.Key != "" {
			message, err := plugin.Validate(ctx, b.ID, b.Method, b.Key, b.Value)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			if message != "" {
				writeJSON(w, map[string]any{"error": message})
				return
			}
			b.Inputs[b.Key] = b.Value
		}
		p, err := plugin.NextPrompt(ctx, b.ID, b.Method, b.Inputs)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"prompt": p, "inputs": b.Inputs})
	})
	mux.HandleFunc("GET /api/subscriptions", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, provider.WhaleBridgeSubscriptions()) })
	mux.HandleFunc("POST /api/signin", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Agent  string            `json:"agent"`
			Site   string            `json:"site"`
			User   string            `json:"user"`
			Proxy  *string           `json:"proxy"`
			Plugin bool              `json:"plugin"`
			Method int               `json:"method"`
			Inputs map[string]string `json:"inputs"`
			Key    string            `json:"key"`
		}
		if err := decode(w, r, &b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var st provider.SignInState
		var err error
		if b.Plugin {
			var pp plugin.Provider
			var listed []plugin.Provider
			listed, err = plugin.Providers(r.Context())
			if err == nil {
				found := false
				for _, p := range listed {
					if p.ID == b.Agent || provider.PluginID(p.ID) == b.Agent {
						pp, found = p, true
						break
					}
				}
				if !found {
					err = fmt.Errorf("该供应商适配器未就绪，请刷新后重试")
				}
			}
			if err == nil && (b.Method < 0 || b.Method >= len(pp.Methods)) {
				err = fmt.Errorf("供应商登录方式已变化，请刷新后重试")
			}
			if err == nil {
				switch pp.Methods[b.Method].Type {
				case "api":
					_, err = provider.PluginAPIKey(r.Context(), pp.ID, b.Method, b.Inputs, b.Key)
					st.State = "done"
				case "oauth":
					st, err = provider.StartPluginSignInWith(pp.ID, b.Method, b.Inputs, provider.SignInOptions{Site: b.Site, User: b.User, Proxy: b.Proxy})
				default:
					err = fmt.Errorf("该适配器登录方式暂不支持")
				}
			}
		} else {
			st, err = provider.StartSignInWith(b.Agent, provider.SignInOptions{Site: b.Site, User: b.User, Proxy: b.Proxy})
		}
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if st.State == "done" {
			if err := syncConfiguration(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("GET /api/signin/{id}", func(w http.ResponseWriter, r *http.Request) {
		st, ok := provider.SignInStatus(r.PathValue("id"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		if st.State == "done" {
			if err := syncConfiguration(); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("POST /api/signin/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		provider.CancelSignIn(r.PathValue("id"))
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/signin/{id}/callback", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			URL string `json:"url"`
		}
		if err := decode(w, r, &b); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := provider.SubmitSignInCallback(r.PathValue("id"), b.URL); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, accountInfos(r.PathValue("id")))
	})
	mux.HandleFunc("GET /api/accounts/{id}/project", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id != "gemini" && id != "antigravity" {
			http.Error(w, "只有 Google 账号支持 Cloud project", 400)
			return
		}
		writeJSON(w, map[string]any{"project": provider.GoogleProject(id, r.URL.Query().Get("user"))})
	})
	mux.HandleFunc("POST /api/accounts/project", mutate(func(w http.ResponseWriter, r *http.Request) error {
		var b struct {
			ID      string `json:"id"`
			User    string `json:"user"`
			Project string `json:"project"`
		}
		if err := decode(w, r, &b); err != nil {
			return err
		}
		return provider.SetGoogleProject(b.ID, b.User, b.Project)
	}))
	mux.HandleFunc("POST /api/accounts/on", mutate(func(w http.ResponseWriter, r *http.Request) error {
		var b struct {
			ID   string `json:"id"`
			User string `json:"user"`
			On   bool   `json:"on"`
		}
		if err := decode(w, r, &b); err != nil {
			return err
		}
		return provider.WhaleBridgeSetLoginOn(b.ID, b.User, b.On)
	}))
	mux.HandleFunc("POST /api/accounts/remove", mutate(func(w http.ResponseWriter, r *http.Request) error {
		var b struct {
			ID   string `json:"id"`
			User string `json:"user"`
		}
		if err := decode(w, r, &b); err != nil {
			return err
		}
		return provider.WhaleBridgeForgetLogin(b.ID, b.User)
	}))
	mux.HandleFunc("GET /api/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := provider.Find(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, keyInfos(*p))
	})
	mux.HandleFunc("POST /api/keys", keyAction)
	mux.HandleFunc("POST /api/keys/{action}", keyAction)
}
