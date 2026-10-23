package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/catalog"
	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
)

// The test executable acts as a fake Claude CLI, never a real installed
// client. Its identity probe pauses at a loopback fixture after login exits.
func init() {
	if os.Getenv("WHALEBRIDGE_SIGNIN_FAKE_CLI") == "" || len(os.Args) < 3 || os.Args[1] != "auth" {
		return
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	switch os.Args[2] {
	case "login":
		_ = os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"fake-access","refreshToken":"fake-refresh","expiresAt":9999999999999,"subscriptionType":"max"}}`), 0o600)
		_ = os.WriteFile(os.Getenv(openedURLEnv), []byte("https://fixture.invalid/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A12345%2Fcallback"), 0o600)
	case "status":
		res, err := http.Get(os.Getenv("WHALEBRIDGE_SIGNIN_FAKE_STATUS"))
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(os.Stdout, res.Body)
		_ = res.Body.Close()
	}
	os.Exit(0)
}

func whaleSignInHome(t *testing.T) {
	t.Helper()
	isolate(t)
	writeFile(t, catalog.CachePath(), map[string]any{"fixture": map[string]any{"models": map[string]any{}}})
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func whaleSignInWait(t *testing.T, ch <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(why)
	}
}

func whaleSignInFlow(t *testing.T, agent string) *signInFlow {
	t.Helper()
	st, err := StartSignInWith(agent, SignInOptions{Proxy: new("direct")})
	if err != nil {
		t.Fatal(err)
	}
	signIns.Lock()
	s := signIns.m[st.ID]
	signIns.Unlock()
	t.Cleanup(func() {
		CancelSignIn(st.ID)
		if s.srv != nil {
			_ = s.srv.Close()
		}
		signIns.Lock()
		delete(signIns.m, st.ID)
		signIns.Unlock()
	})
	return s
}

func whaleSignInInterrupt(t *testing.T, s *signInFlow, how string) {
	t.Helper()
	switch how {
	case "cancel":
		CancelSignIn(s.st.ID)
	case "replace":
		newer := whaleSignInFlow(t, s.st.Agent)
		if newer == s || newer.status().State != "waiting" {
			t.Fatal("the replacement did not start independently")
		}
		CancelSignIn(newer.st.ID)
	}
	if how != "success" && s.status().State != "canceled" {
		t.Fatalf("interrupted state %+v", s.status())
	}
}

// Hold a fully received reply rather than only a pending request: even if
// cancellation loses the HTTP race, the later account write must be gated.
type whaleSignInHeldTransport struct {
	base    http.RoundTripper
	path    string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *whaleSignInHeldTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, err := h.base.RoundTrip(r)
	if err != nil || r.URL.Path != h.path {
		return res, err
	}
	body, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return res, nil
}

func TestWhaleBridgeSignInCanceledCallbacksDoNotKeepTokens(t *testing.T) {
	for _, agent := range []string{"codex", ChatGPTAPIID, "gemini"} {
		for _, how := range []string{"cancel", "replace", "success"} {
			t.Run(agent+"/"+how, func(t *testing.T) {
				whaleSignInHome(t)
				oldCodex, oldSIWC, oldGoogle := codexTokenURL, siwcTokenURL, googleTokenURL
				oldAddr, oldSIWCAddr := codexCallbackAddr, siwcCallbackAddr
				oldWho, oldProd := googleUserInfoURL, codeAssistProd
				oldClient, oldSIWCClient := http.DefaultClient, siwcClient
				oldAPI := siwcAPI
				defer func() {
					codexTokenURL, siwcTokenURL, googleTokenURL = oldCodex, oldSIWC, oldGoogle
					codexCallbackAddr, siwcCallbackAddr = oldAddr, oldSIWCAddr
					googleUserInfoURL, codeAssistProd, siwcAPI = oldWho, oldProd, oldAPI
					http.DefaultClient, siwcClient = oldClient, oldSIWCClient
				}()
				codexCallbackAddr, siwcCallbackAddr = "127.0.0.1:0", "127.0.0.1:0"
				var flow *signInFlow
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						claims := map[string]any{"email": "late@example.test", "sub": "subject", "iss": siwcIssuer, "aud": "fake-client", "nonce": flow.nonce,
							"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "private-account"}}
						_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "late-refresh", "id_token": fakeJWT(claims), "expires_in": 3600, "scope": siwcDirectScope})
					case "/who":
						_, _ = io.WriteString(w, `{"email":"late@example.test"}`)
					case "/v1internal:loadCodeAssist":
						_, _ = io.WriteString(w, `{"currentTier":{"id":"standard","name":"Standard"},"cloudaicompanionProject":"fixture-project"}`)
					case "/v1/models":
						_, _ = io.WriteString(w, `{"data":[{"id":"fixture-model"}]}`)
					default:
						http.NotFound(w, r)
					}
				}))
				t.Cleanup(fake.Close)
				codexTokenURL, siwcTokenURL, googleTokenURL = fake.URL+"/token", fake.URL+"/token", fake.URL+"/token"
				googleUserInfoURL, codeAssistProd, siwcAPI = fake.URL+"/who", fake.URL, fake.URL+"/v1"
				hold := &whaleSignInHeldTransport{base: http.DefaultTransport, path: "/token", entered: make(chan struct{}), release: make(chan struct{})}
				let := sync.OnceFunc(func() { close(hold.release) })
				t.Cleanup(let)
				http.DefaultClient, siwcClient = &http.Client{Transport: hold}, &http.Client{Transport: hold}
				flow = whaleSignInFlow(t, agent)
				// A native credential and an existing component-owned token are
				// sentinels: cancellation must neither add nor replace either.
				nativePath := codexAuthPath()
				native := parityAuth(t, "native@example.test", "native")
				writeFile(t, nativePath, json.RawMessage(native))
				existing := savedLogin{Agent: agent, User: "late@example.test", Owned: true, On: true, Auth: json.RawMessage(`{"sentinel":"unchanged"}`)}
				saveLogins(t, existing)
				before, err := os.ReadFile(loginsPath())
				if err != nil {
					t.Fatal(err)
				}
				beforeNative, _ := os.ReadFile(nativePath)
				q := url.Values{"state": {flow.state}, "code": {"late"}, "client_id": {"fake-client"}}
				callback := flow.redirect + "?" + q.Encode()
				ended := make(chan struct{})
				var pasteErr error
				go func() {
					defer close(ended)
					// Exercise pasted-address and browser-handler entry points.
					if how == "cancel" {
						pasteErr = SubmitSignInCallback(flow.st.ID, callback)
					} else {
						flow.callback(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, callback, nil))
					}
				}()
				whaleSignInWait(t, hold.entered, "the fake token was never fetched")
				whaleSignInInterrupt(t, flow, how)
				let()
				whaleSignInWait(t, ended, "the callback did not finish")
				after, _ := os.ReadFile(loginsPath())
				afterNative, _ := os.ReadFile(nativePath)
				if !bytes.Equal(beforeNative, afterNative) {
					t.Fatal("the component sign-in modified native credentials")
				}
				if how != "success" {
					if flow.status().State != "canceled" || !bytes.Equal(before, after) {
						t.Fatalf("late token was kept: state=%s account file changed=%v", flow.status().State, !bytes.Equal(before, after))
					}
					if how == "cancel" && pasteErr == nil {
						t.Fatal("a pasted canceled callback reported success")
					}
					return
				}
				if flow.status().State != "done" || bytes.Equal(before, after) {
					t.Fatalf("live sign-in did not save: %+v", flow.status())
				}
				found := false
				for _, l := range readLogins() {
					if l.Agent == agent && l.User == "late@example.test" {
						found = l.Owned && bytes.Contains(l.Auth, []byte("late-refresh"))
					}
				}
				if !found || ProxyOfLogin(agent, "late@example.test") != "direct" {
					t.Fatal("saved sign-in lost ownership, credentials or chosen proxy")
				}
			})
		}
	}
}

func TestWhaleBridgeSignInKeepLinearizesWithCancel(t *testing.T) {
	whaleSignInHome(t)
	s := &signInFlow{st: SignInState{ID: "keep", Agent: "codex", State: "waiting"}, done: make(chan struct{}), proxy: "direct"}
	entered, release, stored := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		_ = keepLogin(s.via(context.Background()), func() error { close(entered); <-release; close(stored); return nil })
	}()
	whaleSignInWait(t, entered, "store never entered")
	canceled := make(chan bool, 1)
	go func() { canceled <- s.finish(SignInState{State: "canceled"}) }()
	close(release)
	whaleSignInWait(t, stored, "store never completed")
	if <-canceled || !s.finish(SignInState{State: "done", User: "saved@example.test"}) || s.status().State != "done" {
		t.Fatal("a late cancel overrode a saved account")
	}
	if err := keepLogin(s.via(context.Background()), func() error { t.Error("finished flow wrote again"); return nil }); !errors.Is(err, errSignInOver) {
		t.Fatalf("finished flow: %v", err)
	}
	if netproxy.Choice(s.via(context.Background())) != "direct" {
		t.Fatal("flow context lost its chosen proxy")
	}
	// Failed writes do not make cancellation too late, and use outside a
	// browser flow remains compatible with the existing import paths.
	failed := &signInFlow{done: make(chan struct{})}
	want := errors.New("fake write failed")
	if err := failed.keep(func() error { return want }); !errors.Is(err, want) || !failed.finish(SignInState{State: "canceled"}) {
		t.Fatal("failed write blocked cancellation")
	}
	called := false
	if err := keepLogin(context.Background(), func() error { called = true; return nil }); err != nil || !called {
		t.Fatal("a non-flow caller no longer saved")
	}
}

// A channel log hook observes the device-flow goroutine's terminal error
// without sleeps or polling an already-canceled SignInState.
type whaleSignInLog struct{ failed chan struct{} }

func (l whaleSignInLog) Write(p []byte) (int, error) {
	if strings.Contains(string(p), errSignInOver.Error()) {
		select {
		case l.failed <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

func TestWhaleBridgeSignInCopilotLateIdentity(t *testing.T) {
	for _, how := range []string{"cancel", "replace", "success"} {
		t.Run(how, func(t *testing.T) {
			whaleSignInHome(t)
			oldDevice, oldToken, oldUser, oldPlan := gitHubDeviceURL, gitHubTokenURL, GitHubUserURL, CopilotUserURL
			oldClient, oldLog := http.DefaultClient, log.Writer()
			defer func() {
				gitHubDeviceURL, gitHubTokenURL, GitHubUserURL, CopilotUserURL = oldDevice, oldToken, oldUser, oldPlan
				http.DefaultClient = oldClient
				log.SetOutput(oldLog)
			}()
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/device":
					_, _ = io.WriteString(w, `{"device_code":"fake-device","user_code":"FAKE","verification_uri":"https://fixture.invalid/device","interval":1}`)
				case "/token":
					_, _ = io.WriteString(w, `{"access_token":"late-copilot-token"}`)
				case "/user":
					_, _ = io.WriteString(w, `{"login":"late-copilot"}`)
				case "/plan":
					_, _ = io.WriteString(w, `{"copilot_plan":"individual"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(fake.Close)
			gitHubDeviceURL, gitHubTokenURL, GitHubUserURL, CopilotUserURL = fake.URL+"/device", fake.URL+"/token", fake.URL+"/user", fake.URL+"/plan"
			hold := &whaleSignInHeldTransport{base: http.DefaultTransport, path: "/plan", entered: make(chan struct{}), release: make(chan struct{})}
			let := sync.OnceFunc(func() { close(hold.release) })
			t.Cleanup(let)
			http.DefaultClient = &http.Client{Transport: hold}
			failed := make(chan struct{}, 1)
			log.SetOutput(whaleSignInLog{failed: failed})
			flow := whaleSignInFlow(t, "copilot")
			whaleSignInWait(t, hold.entered, "Copilot's fake identity was never fetched")
			whaleSignInInterrupt(t, flow, how)
			let()
			if how == "success" {
				whaleSignInWait(t, flow.done, "Copilot did not finish successfully")
				if flow.status().State != "done" || len(readLogins()) != 1 || !bytes.Contains(readLogins()[0].Auth, []byte("late-copilot-token")) {
					t.Fatalf("live device sign-in was not saved: %+v", flow.status())
				}
			} else {
				whaleSignInWait(t, failed, "late Copilot identity never reached the save gate")
				if len(readLogins()) != 0 || flow.status().State != "canceled" {
					t.Fatal("an interrupted device sign-in added an account")
				}
			}
		})
	}
}

func TestWhaleBridgeSignInClaudeLateCLIIdentity(t *testing.T) {
	for _, how := range []string{"cancel", "replace", "success"} {
		t.Run(how, func(t *testing.T) {
			whaleSignInHome(t)
			entered, release := make(chan struct{}), make(chan struct{})
			let := sync.OnceFunc(func() { close(release) })
			markEntered := sync.OnceFunc(func() { close(entered) })
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				markEntered()
				<-release
				_, _ = io.WriteString(w, `{"email":"late-claude@example.test","subscriptionType":"max"}`)
			}))
			t.Cleanup(fake.Close)
			t.Cleanup(let)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			oldCLI := claudeExecutable
			claudeExecutable = func() string { return exe }
			t.Cleanup(func() { claudeExecutable = oldCLI })
			t.Setenv("WHALEBRIDGE_SIGNIN_FAKE_CLI", "1")
			t.Setenv("WHALEBRIDGE_SIGNIN_FAKE_STATUS", fake.URL+"/status")
			flow := whaleSignInFlow(t, "claude")
			whaleSignInWait(t, entered, "the fake Claude CLI did not reach auth status")
			// The replacement starts its own fake CLI while the older
			// identity response is still held; neither may publish later.
			whaleSignInInterrupt(t, flow, how)
			let()
			claudeLogins.Wait()
			if how == "success" {
				if flow.status().State != "done" {
					t.Fatalf("Claude state %+v", flow.status())
				}
				found := false
				for _, l := range readLogins() {
					if l.Agent == "claude" && l.Owned && bytes.Contains(l.Auth, []byte("fake-refresh")) {
						found = true
					}
				}
				if !found || ProxyOfLogin("claude", flow.status().User) != "direct" {
					t.Fatal("live CLI sign-in lost its account or proxy")
				}
			} else if flow.status().State != "canceled" || len(readLogins()) != 0 {
				t.Fatal("the interrupted CLI sign-in added an account")
			}
		})
	}
}
