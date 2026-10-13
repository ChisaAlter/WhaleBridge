package provider

import (
	"bytes"
	"cmp"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/testenv"
)

// vertexHome gives a test a home of its own with no Google credentials in
// it, neither GOOGLE_APPLICATION_CREDENTIALS nor CLOUDSDK_CONFIG set, and
// no Vertex AI token kept from another test.
func vertexHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	h := t.TempDir()
	testenv.SetHome(t, h)
	t.Setenv("LAUNCHER_COMPONENT_DATA_DIR", filepath.Join(h, "component"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(h, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(h, ".local", "state"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	forgetVertexTokens()
	t.Cleanup(forgetVertexTokens)
	return h
}

// gcloudCredentials is where `gcloud auth application-default login`
// writes a person's credentials for home: ~/.config/gcloud, or
// %APPDATA%\gcloud on Windows.
func gcloudCredentials(home string) string {
	dir := filepath.Join(home, ".config", "gcloud")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(os.Getenv("APPDATA"), "gcloud")
	}
	return filepath.Join(dir, "application_default_credentials.json")
}

// vertexGoogle is Google as a Vertex AI provider meets it: the token
// endpoint, IAM Credentials' generateAccessToken and a project's
// generateContent. It keeps every request each was sent.
type vertexGoogle struct {
	url  string
	quit chan struct{} // closed as the test ends, for a request still held
	got  chan struct{} // told of a token request held
	mu   sync.Mutex
	// set before the request it is for
	answers vertexAnswers
	// what each was sent
	tokens, iam, vertex []vertexCall
}

// vertexAnswers are how vertexGoogle answers. The token endpoint mints
// ya29.minted-<n> lasting expiresIn seconds (3599 when 0) after slow, or
// answers status and reply when status is set; a request is held
// unanswered while held is, and answered once gate is closed when that is
// set. IAM Credentials mints ya29.impersonated-<n> lasting iamLife (an hour
// when 0), or answers iamStatus and iamReply.
type vertexAnswers struct {
	expiresIn int
	slow      time.Duration
	status    int
	reply     string
	held      bool
	gate      chan struct{}
	iamLife   time.Duration
	iamStatus int
	iamReply  string
}

// vertexCall is a request vertexGoogle was sent.
type vertexCall struct {
	path   string // as it was sent, escaped
	header http.Header
	body   string
}

// newVertexGoogle serves a vertexGoogle on loopback, Vertex AI's root,
// Google's token endpoint and IAM Credentials pointed at it until the test
// ends.
func newVertexGoogle(t *testing.T) *vertexGoogle {
	t.Helper()
	f := &vertexGoogle{quit: make(chan struct{}), got: make(chan struct{}, 1)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(f.quit) }) // before Close, which waits for a held request
	f.url = srv.URL
	t.Cleanup(VertexForTest(srv.URL+"/vertex", srv.URL+"/token", srv.URL+"/iam/v1"))
	return f
}

func (f *vertexGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c := vertexCall{r.URL.EscapedPath(), r.Header.Clone(), string(b)}
	token, iam, vertex := r.URL.Path == "/token", strings.HasPrefix(r.URL.Path, "/iam/v1/"), strings.HasPrefix(r.URL.Path, "/vertex/")
	f.mu.Lock()
	n := 0
	switch {
	case token:
		f.tokens = append(f.tokens, c)
		n = len(f.tokens)
	case iam:
		f.iam = append(f.iam, c)
		n = len(f.iam)
	case vertex:
		f.vertex = append(f.vertex, c)
	}
	a := f.answers
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if token && a.gate != nil {
		select {
		case f.got <- struct{}{}:
		default:
		}
		select {
		case <-a.gate:
		case <-r.Context().Done():
			return
		case <-f.quit:
			return
		}
	}
	switch {
	case token && a.held:
		select {
		case f.got <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-f.quit:
		}
	case token && a.status != 0:
		time.Sleep(a.slow)
		w.WriteHeader(a.status)
		io.WriteString(w, a.reply)
	case token:
		time.Sleep(a.slow)
		fmt.Fprintf(w, `{"access_token":"ya29.minted-%d","expires_in":%d,"scope":"https://www.googleapis.com/auth/cloud-platform","token_type":"Bearer"}`, n, cmp.Or(a.expiresIn, 3599))
	case iam && a.iamStatus != 0:
		w.WriteHeader(a.iamStatus)
		io.WriteString(w, a.iamReply)
	case iam:
		fmt.Fprintf(w, `{"accessToken":"ya29.impersonated-%d","expireTime":%q}`, n, time.Now().Add(cmp.Or(a.iamLife, time.Hour)).UTC().Format(time.RFC3339))
	case vertex && strings.Contains(r.URL.Path, "/models/gemini-9-pro:"):
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, "{\"error\":{\"code\":404,\"message\":\"Publisher Model `projects/acme-vertex/locations/global/publishers/google/models/gemini-9-pro` was not found or your project does not have access to it. Please ensure you are using a valid model version. For more information, see: https://cloud.google.com/vertex-ai/generative-ai/docs/learn/model-versions\",\"status\":\"NOT_FOUND\"}}")
	case vertex:
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2},"modelVersion":"gemini-3.8-flash"}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *vertexGoogle) tokenCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tokens)
}

func (f *vertexGoogle) iamCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.iam)
}

func (f *vertexGoogle) vertexCalls() []vertexCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.vertex)
}

// vertexUser is a person's credentials as `gcloud auth application-default
// login` writes them.
func vertexUser(refresh string) map[string]any {
	return map[string]any{
		"account":          "",
		"client_id":        "1234567890-vertextest.apps.googleusercontent.com",
		"client_secret":    "vertex-test-secret",
		"quota_project_id": "acme-vertex",
		"refresh_token":    refresh,
		"type":             "authorized_user",
		"universe_domain":  "googleapis.com",
	}
}

// vertexServiceKey is a service account's key file as the Cloud console
// gives it.
func vertexServiceKey(email, keyID, key string) map[string]any {
	return map[string]any{
		"type":                        "service_account",
		"project_id":                  "acme-vertex",
		"private_key_id":              keyID,
		"private_key":                 key,
		"client_email":                email,
		"client_id":                   "104857600123456789012",
		"auth_uri":                    "https://accounts.google.com/o/oauth2/auth",
		"token_uri":                   "https://oauth2.googleapis.com/token",
		"auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
		"client_x509_cert_url":        "https://www.googleapis.com/robot/v1/metadata/x509/" + url.PathEscape(email),
		"universe_domain":             "googleapis.com",
	}
}

// vertexImpersonation is the file `gcloud auth application-default login
// --impersonate-service-account` writes: source's token is traded for one
// of the service account the URL names.
func vertexImpersonation(at string, delegates []string, source any) map[string]any {
	return map[string]any{
		"delegates":                         delegates,
		"service_account_impersonation_url": at,
		"source_credentials":                source,
		"type":                              "impersonated_service_account",
	}
}

// vertexRSA is a service account's RSA key, made once for the tests.
var vertexRSA = sync.OnceValues(func() (*rsa.PrivateKey, error) { return rsa.GenerateKey(rand.Reader, 2048) })

// vertexAt is a Vertex AI provider at the project acme-vertex in loc,
// signed with the credentials in file ("" finds them as Google's libraries
// do).
func vertexAt(loc, file string) Provider {
	return Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset,
		Vertex: &Vertex{Project: "acme-vertex", Location: loc, Credentials: file}}
}

// signVertex signs a request for model at p's address, as the gateway
// sends one, and gives the Authorization it carries.
func signVertex(ctx context.Context, p Provider, model string) (string, error) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Base(Gemini)+VertexPath(model), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	err = p.Sign(ctx, req, Gemini, body)
	return req.Header.Get("Authorization"), err
}

// jwtOf is a JWT's header and claims, once its signature verifies with pub.
func jwtOf(t *testing.T, jwt string, pub *rsa.PublicKey) (head, claims map[string]any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("no JWT: %q", jwt)
	}
	var seg [3][]byte
	for i, s := range parts {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatalf("the JWT's part %d isn't unpadded base64url: %v", i, err)
		}
		seg[i] = b
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], seg[2]); err != nil {
		t.Fatalf("the JWT's signature doesn't verify with the key's public half: %v", err)
	}
	if json.Unmarshal(seg[0], &head) != nil || json.Unmarshal(seg[1], &claims) != nil {
		t.Fatalf("the JWT's header or claims aren't JSON: %s %s", seg[0], seg[1])
	}
	return head, claims
}

// Each location is asked at a host of its own: global at
// aiplatform.googleapis.com, the us and eu multi-regions at their rep
// hosts, a region at its own. A project or location no address can be made
// of gives none, so nothing is sent anywhere, and a model typed in by hand
// stays one segment of the path.

func TestVertexAddress(t *testing.T) {
	vertexHome(t)
	for loc, want := range map[string]string{
		"":             "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global",
		"global":       "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global",
		"us":           "https://aiplatform.us.rep.googleapis.com/v1/projects/acme-vertex/locations/us",
		"eu":           "https://aiplatform.eu.rep.googleapis.com/v1/projects/acme-vertex/locations/eu",
		"us-central1":  "https://us-central1-aiplatform.googleapis.com/v1/projects/acme-vertex/locations/us-central1",
		"europe-west4": "https://europe-west4-aiplatform.googleapis.com/v1/projects/acme-vertex/locations/europe-west4",
	} {
		p := vertexAt(loc, "")
		if got := p.vertexBase(); got != want {
			t.Errorf("at %q: %s, want %s", loc, got, want)
		}
		if got := p.Base(Gemini); got != want {
			t.Errorf("at %q generateContent's base is %s, want %s", loc, got, want)
		}
	}
	// a domain-scoped project's id has a dot and a colon in it
	p := vertexAt("global", "")
	p.Vertex.Project = "example.com:acme-vertex"
	if got := p.vertexBase(); got != "https://aiplatform.googleapis.com/v1/projects/example.com:acme-vertex/locations/global" {
		t.Errorf("a domain-scoped project: %s", got)
	}
	for _, v := range []*Vertex{
		nil, {}, {Location: "us-central1"},
		{Project: "Acme-Vertex"}, {Project: "acme-vertex/../other-project"}, {Project: "acme-vertex?alt=sse"},
		{Project: "acme-vertex", Location: "us central1"}, {Project: "acme-vertex", Location: "../global"}, {Project: "acme-vertex", Location: "us-central1/x"},
	} {
		p := Provider{Name: "Vertex prod", Preset: VertexPreset, Vertex: v}
		if got := p.vertexBase(); got != "" || p.Base(Gemini) != "" || len(p.Speaks()) > 0 || p.Ready() {
			t.Errorf("%+v has an address: %q, speaks %v, ready %v", v, got, p.Speaks(), p.Ready())
		}
	}
	if got := VertexPath("gemini-3.1-pro-preview-customtools"); got != "/publishers/google/models/gemini-3.1-pro-preview-customtools:streamGenerateContent?alt=sse" {
		t.Errorf("a model's path: %s", got)
	}
	if got := VertexPath("tuned/gemini 1?x#y"); got != "/publishers/google/models/tuned%2Fgemini%201%3Fx%23y:streamGenerateContent?alt=sse" {
		t.Errorf("a model with a slash, a space, ? and # in it: %s", got)
	}
}

func TestVertexSaveRefuses(t *testing.T) {
	home := vertexHome(t)
	for _, c := range []struct {
		v    *Vertex
		key  string
		keys []KeyAccount
		want string
	}{
		{v: &Vertex{Project: "acme-vertex"}, key: "AIzaSyD-not-a-real-key", want: "Google Vertex AI is asked with your Google credentials, not an API key"},
		{v: &Vertex{Project: "acme-vertex"}, keys: []KeyAccount{{Name: "express", Key: "AIzaSyD-not-a-real-key"}}, want: "Google Vertex AI is asked with your Google credentials, not an API key"},
		{want: "Google Vertex AI needs the id of your Google Cloud project"},
		{v: &Vertex{Location: "us-central1"}, want: "Google Vertex AI needs the id of your Google Cloud project"},
		{v: &Vertex{Project: "acme_vertex"}, want: `"acme_vertex" is no Google Cloud project id`},
		{v: &Vertex{Project: "-acme-vertex"}, want: `"-acme-vertex" is no Google Cloud project id`},
		{v: &Vertex{Project: "acme-vertex/locations"}, want: `"acme-vertex/locations" is no Google Cloud project id`},
		{v: &Vertex{Project: "acme-vertex", Location: "us central1"}, want: `"us central1" is no Vertex AI location: global, us, eu or a region such as us-central1`},
		{v: &Vertex{Project: "acme-vertex", Location: "us-central1/"}, want: `"us-central1/" is no Vertex AI location: global, us, eu or a region such as us-central1`},
		{v: &Vertex{Project: "acme-vertex", Credentials: "keys/acme-vertex.json"}, want: `give the credentials file's full path, not "keys/acme-vertex.json"`},
		// another user's home, which nothing expands: it would be read
		// under whatever folder magpie was started in
		{v: &Vertex{Project: "acme-vertex", Credentials: "~other/acme-vertex.json"}, want: `give the credentials file's full path, not "~other/acme-vertex.json"`},
		{v: &Vertex{Project: "acme-vertex", Impersonate: "vertex-runner"}, want: `"vertex-runner" is no service account's email`},
		{v: &Vertex{Project: "acme-vertex", Impersonate: "vertex-runner@acme vertex.iam.gserviceaccount.com"}, want: `"vertex-runner@acme vertex.iam.gserviceaccount.com" is no service account's email`},
	} {
		err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: c.v, Key: c.key, Keys: c.keys})
		if err == nil || err.Error() != c.want {
			t.Errorf("%+v, key %q %v: %v, want %q", c.v, c.key, c.keys, err, c.want)
		}
	}
	if _, err := os.Stat(Path()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a provider refused was kept: %v", err)
	}
	// a file's full path, or one under the home, is taken
	for _, cred := range []string{filepath.Join(home, "keys", "acme-vertex.json"), "~/keys/acme-vertex.json"} {
		if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Credentials: cred}}); err != nil {
			t.Errorf("credentials %s: %v", cred, err)
		}
	}
}

func TestVertexKeyKeptDropped(t *testing.T) {
	home := vertexHome(t)
	newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	stray := []KeyAccount{{Name: "express", Key: "AQ.Ab8RN6-other"}}
	writeFile(t, Path(), map[string]any{"providers": []Provider{
		{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Key: "AQ.Ab8RN6-stray", KeyName: "express", Keys: stray, Vertex: &Vertex{Project: "acme-vertex"}},
		{ID: "vertex-bare", Name: "Vertex bare", Preset: VertexPreset, Key: "AQ.Ab8RN6-stray"},
	}})
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "" || p.KeyName != "" || p.Keys != nil || !p.On() {
		t.Fatalf("found with key %q (%q), keys %v, on %v", p.Key, p.KeyName, p.Keys, p.On())
	}
	if bare, err := Find("vertex-bare"); err != nil || bare.Ready() {
		t.Fatalf("with a key and no project: ready %v (%v)", bare.Ready(), err)
	}
	if auth, err := signVertex(context.Background(), *p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if err := SetOff("vertex-prod", true); err != nil {
		t.Fatalf("switched off: %v", err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if b, err := os.ReadFile(Path()); err != nil || json.Unmarshal(b, &f) != nil || f.Providers[0]["id"] != "vertex-prod" || f.Providers[0]["off"] != true {
		t.Fatalf("providers.json: %v (%v)", f.Providers, err)
	}
	for _, k := range []string{"key", "keyName", "keys"} {
		if v, ok := f.Providers[0][k]; ok && v != "" {
			t.Errorf("saved with its %s %v", k, v)
		}
	}
	typed := vertexAt("global", "")
	typed.Key = "AQ.Ab8RN6-typed"
	if auth, err := signVertex(context.Background(), typed, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("made with a key, signed with %q: %v", auth, err)
	}
	if typed.Vertex = nil; typed.Ready() {
		t.Fatal("a key made it ready with no project")
	}
}

func TestVertexSaveNormalizes(t *testing.T) {
	vertexHome(t)
	key := filepath.Join(t.TempDir(), "Keys", "acme-vertex.json")
	err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset,
		Chat: "https://relay.example.com/v1", Responses: "https://relay.example.com/v1",
		Anthropic: "https://relay.example.com", Decide: "https://relay.example.com/v1",
		Vertex: &Vertex{Project: " Acme-Vertex ", Credentials: " " + key + "\n", Impersonate: " Vertex-Runner@Acme-Vertex.iam.gserviceaccount.com "}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-relay", Vertex: &Vertex{Project: "acme-vertex"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Providers) != 2 {
		t.Fatalf("providers.json: %s (%v)", b, err)
	}
	want := map[string]any{"project": "acme-vertex", "location": "global", "credentials": key, "impersonate": "vertex-runner@acme-vertex.iam.gserviceaccount.com"}
	if got := f.Providers[0]["vertex"]; !reflect.DeepEqual(got, want) {
		t.Errorf("kept as %v, want %v", got, want)
	}
	for _, k := range []string{"chat", "responses", "anthropic", "decide"} {
		if u, ok := f.Providers[0][k]; ok {
			t.Errorf("a Vertex AI provider kept its %s URL %v", k, u)
		}
	}
	if v, ok := f.Providers[1]["vertex"]; ok {
		t.Errorf("an OpenAI-compatible provider kept a Vertex: %v", v)
	}
	if v := normalize(Provider{Preset: VertexPreset, Vertex: &Vertex{Project: " ", Location: " "}}).Vertex; v != nil {
		t.Errorf("nothing given is kept as %+v", v)
	}
	if v := normalize(Provider{Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: " Europe-West4 "}}).Vertex; v == nil || v.Location != "europe-west4" {
		t.Errorf("a region is kept as %+v", v)
	}
}

func TestVertexProviderListed(t *testing.T) {
	vertexHome(t)
	if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "vertex-prod"); !ok {
		t.Fatal("not listed")
	}
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	base := "https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global"
	if !p.Ready() || !p.On() || !slices.Equal(p.Speaks(), []Protocol{Gemini}) || p.Base(Gemini) != base {
		t.Fatalf("ready %v, on %v, speaks %v at %q", p.Ready(), p.On(), p.Speaks(), p.Base(Gemini))
	}
	if !customIDs()["vertex-prod"] || PluginID("vertex-prod") != "vertex-prod-plugin" {
		t.Fatalf("not the user's own: their ids %v, a plugin's %s", customIDs(), PluginID("vertex-prod"))
	}
	global := Preset(VertexPreset).Models
	for _, loc := range []string{"global", "us", "eu", "us-central1", "europe-west4"} {
		if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: loc}}); err != nil {
			t.Fatal(err)
		}
		p, err := Find("vertex-prod")
		if err != nil {
			t.Fatal(err)
		}
		fetched, err := p.Fetch(context.Background())
		if err != nil {
			t.Fatalf("at %s: %v", loc, err)
		}
		got := modelIDs(p.Available())
		if !slices.Equal(modelIDs(fetched), got) || !slices.Equal(modelIDs(p.Exposed()), got) {
			t.Errorf("at %s: fetched %v, available %v, exposed %v", loc, modelIDs(fetched), got, modelIDs(p.Exposed()))
		}
		var wrong func(string) bool
		switch loc {
		case "global":
			wrong = func(m string) bool { return !slices.Contains(global, m) }
		case "us", "eu":
			// Gemini 3's Flash models, of those global serves
			wrong = func(m string) bool {
				return !strings.HasPrefix(m, "gemini-3") || !strings.Contains(m, "-flash") || strings.Contains(m, "preview") || !slices.Contains(global, m)
			}
		default:
			wrong = func(m string) bool { return !strings.HasPrefix(m, "gemini-2.5-") }
		}
		if len(got) == 0 || slices.ContainsFunc(got, wrong) || loc == "global" && len(got) != len(global) {
			t.Errorf("at %s: %v", loc, got)
		}
	}
}

func TestVertexCopies(t *testing.T) {
	vertexHome(t)
	if err := Save(Provider{ID: "vertex-prod", Name: "Vertex prod", Preset: VertexPreset, Vertex: &Vertex{Project: "acme-vertex", Location: "us"}}); err != nil {
		t.Fatal(err)
	}
	release := Hold()
	a, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	a.Vertex.Project = "changed"
	b, err := Find("vertex-prod")
	release()
	if err != nil || b.Vertex.Project != "acme-vertex" {
		t.Fatalf("a caller's change reached the held list: %+v (%v)", b, err)
	}
	id, err := AddCopy(Provider{Name: "Vertex prod copy", Preset: VertexPreset}, "vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Find(id)
	if err != nil || c.Vertex == nil || *c.Vertex != (Vertex{Project: "acme-vertex", Location: "us"}) {
		t.Fatalf("the copy %s: %+v (%v)", id, c, err)
	}
	if err := Rename("vertex-prod", "vertex-main"); err != nil {
		t.Fatal(err)
	}
	r, err := Find("vertex-main")
	if err != nil || r.Base(Gemini) != "https://aiplatform.us.rep.googleapis.com/v1/projects/acme-vertex/locations/us" {
		t.Fatalf("renamed: %+v (%v)", r, err)
	}
}

func TestVertexUserCredentials(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	writeFile(t, gcloudCredentials(home), vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", "")
	for range 2 {
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
			t.Fatalf("signed with %q: %v", auth, err)
		}
	}
	calls := f.tokenCalls()
	if len(calls) != 1 {
		t.Fatalf("the token endpoint was asked %d times, want once", len(calls))
	}
	form, err := url.ParseQuery(calls[0].body)
	want := url.Values{"grant_type": {"refresh_token"}, "client_id": {"1234567890-vertextest.apps.googleusercontent.com"},
		"client_secret": {"vertex-test-secret"}, "refresh_token": {"1//0g-test-refresh"}}
	if err != nil || !reflect.DeepEqual(form, want) {
		t.Fatalf("form %v (%v), want %v", form, err, want)
	}
	if h := calls[0].header; h.Get("Content-Type") != "application/x-www-form-urlencoded" || h.Get("Authorization") != "" {
		t.Fatalf("a refresh sent as %q with Authorization %q", h.Get("Content-Type"), h.Get("Authorization"))
	}
}

func TestVertexServiceAccountKey(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	key, err := vertexRSA()
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	pkcs1 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	const email, keyID = "vertex-caller@acme-vertex.iam.gserviceaccount.com", "6f1c2d3e4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d"
	dir := t.TempDir()
	file := filepath.Join(dir, "acme-vertex-6f1c2d3e4a5b.json")
	writeFile(t, file, vertexServiceKey(email, keyID, pkcs8))
	before := time.Now()
	if auth, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	after := time.Now()
	form, err := url.ParseQuery(f.tokenCalls()[0].body)
	if err != nil || len(form) != 2 || form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		t.Fatalf("form %v (%v)", form, err)
	}
	head, claims := jwtOf(t, form.Get("assertion"), &key.PublicKey)
	if head["alg"] != "RS256" || head["kid"] != keyID {
		t.Errorf("header %v", head)
	}
	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if claims["iss"] != email || claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" || claims["aud"] != f.url+"/token" {
		t.Errorf("claims %v", claims)
	}
	if exp-iat != 3600 || iat > float64(after.Unix()) || iat < float64(before.Add(-time.Minute).Unix()) {
		t.Errorf("issued at %v, lapses at %v, signed at %v", iat, exp, before.Unix())
	}
	// PKCS #1, with no key id
	old := filepath.Join(dir, "pkcs1.json")
	k := vertexServiceKey(email, "", pkcs1)
	delete(k, "private_key_id")
	writeFile(t, old, k)
	if auth, err := signVertex(context.Background(), vertexAt("global", old), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-2" {
		t.Fatalf("PKCS #1: signed with %q: %v", auth, err)
	}
	form, _ = url.ParseQuery(f.tokenCalls()[1].body)
	if head, _ := jwtOf(t, form.Get("assertion"), &key.PublicKey); head["alg"] != "RS256" || head["kid"] != nil {
		t.Errorf("PKCS #1: header %v", head)
	}
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalPKCS8PrivateKey(ec)
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"ec.json":       {vertexServiceKey(email, keyID, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: ecDER}))), "their private_key is no RSA key"},
		"no-pem.json":   {vertexServiceKey(email, keyID, base64.StdEncoding.EncodeToString(der)), "their private_key is no PEM key"},
		"no-email.json": {vertexServiceKey("", keyID, pkcs8), "their client_email is missing"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	if n := len(f.tokenCalls()); n != 2 {
		t.Fatalf("keys refused were sent: %d token requests", n)
	}
}

func TestVertexImpersonate(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	const runner = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	p := vertexAt("global", file)
	p.Vertex.Impersonate = runner
	sign := func(p Provider, want string) {
		t.Helper()
		if auth, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil || auth != "Bearer "+want {
			t.Fatalf("signed with %q (%v), want %s", auth, err, want)
		}
	}
	// IAM's token lapses in four minutes, the credentials' in an hour:
	// each request mints one
	f.answers.iamLife = 4 * time.Minute
	sign(p, "ya29.impersonated-1")
	sign(p, "ya29.impersonated-2")
	f.answers.iamLife = time.Hour
	sign(p, "ya29.impersonated-3")
	sign(p, "ya29.impersonated-3")
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 3 || i != 3 {
		t.Fatalf("%d token and %d IAM requests, want 3 of each", n, i)
	}
	c := f.iamCalls()[0]
	if c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" {
		t.Fatalf("IAM Credentials asked at %s", c.path)
	}
	if c.header.Get("Authorization") != "Bearer ya29.minted-1" || c.header.Get("Content-Type") != "application/json" {
		t.Fatalf("IAM Credentials asked with %q as %q", c.header.Get("Authorization"), c.header.Get("Content-Type"))
	}
	var body map[string]any
	want := map[string]any{"scope": []any{"https://www.googleapis.com/auth/cloud-platform"}, "lifetime": "3600s"}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil || !reflect.DeepEqual(body, want) {
		t.Fatalf("IAM Credentials asked for %s, want %v", c.body, want)
	}
	f.answers.iamStatus = http.StatusForbidden
	f.answers.iamReply = `{"error":{"code":403,"message":"Permission 'iam.serviceAccounts.getAccessToken' denied on resource (or it may not exist).","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"IAM_PERMISSION_DENIED","domain":"iam.googleapis.com","metadata":{"permission":"iam.serviceAccounts.getAccessToken"}}]}}`
	p.Vertex.Impersonate = "vertex-admin@acme-vertex.iam.gserviceaccount.com"
	_, err := signVertex(context.Background(), p, "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), "impersonating vertex-admin@acme-vertex.iam.gserviceaccount.com: the sign-in was refused (403): Permission 'iam.serviceAccounts.getAccessToken' denied") ||
		!strings.Contains(err.Error(), "roles/iam.serviceAccountTokenCreator") {
		t.Fatalf("403: %v", err)
	}
	f.answers.iamStatus = http.StatusNotFound
	f.answers.iamReply = `{"error":{"code":404,"message":"Not found; Gaia id not found for email vertex-gone@acme-vertex.iam.gserviceaccount.com","status":"NOT_FOUND"}}`
	p.Vertex.Impersonate = "vertex-gone@acme-vertex.iam.gserviceaccount.com"
	_, err = signVertex(context.Background(), p, "gemini-3.5-flash")
	if err == nil || !strings.Contains(err.Error(), "impersonating vertex-gone@acme-vertex.iam.gserviceaccount.com: the sign-in was refused (404): Not found") ||
		strings.Contains(err.Error(), "serviceAccountTokenCreator") {
		t.Fatalf("404: %v", err)
	}
	// an account named with what a path would read as its end, escaped
	f.answers.iamStatus = 0
	p.Vertex.Impersonate = "ops#1?x@acme-vertex.iam.gserviceaccount.com"
	if _, err := signVertex(context.Background(), p, "gemini-3.5-flash"); err != nil {
		t.Fatal(err)
	}
	if c := f.iamCalls(); c[len(c)-1].path != "/iam/v1/projects/-/serviceAccounts/ops%231%3Fx@acme-vertex.iam.gserviceaccount.com:generateAccessToken" {
		t.Fatalf("IAM Credentials asked at %s", c[len(c)-1].path)
	}
}

func TestVertexGcloudImpersonation(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		http.Error(w, "the token came here", http.StatusTeapot)
	}))
	t.Cleanup(other.Close)
	at := func(account string) string {
		return other.URL + "/v1/projects/-/serviceAccounts/" + account + ":generateAccessToken"
	}
	const runner = "vertex-runner@acme-vertex.iam.gserviceaccount.com"
	dir := t.TempDir()
	file := filepath.Join(dir, "application_default_credentials.json")
	writeFile(t, file, vertexImpersonation(at(runner),
		[]string{"first-hop@acme-vertex.iam.gserviceaccount.com", "projects/-/serviceAccounts/second-hop@acme-vertex.iam.gserviceaccount.com"},
		vertexUser("1//0g-test-refresh")))
	if auth, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.impersonated-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if form, _ := url.ParseQuery(f.tokenCalls()[0].body); form.Get("refresh_token") != "1//0g-test-refresh" {
		t.Fatalf("the source minted with %v", form)
	}
	c := f.iamCalls()[0]
	if c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" || c.header.Get("Authorization") != "Bearer ya29.minted-1" {
		t.Fatalf("IAM Credentials asked at %s with %q", c.path, c.header.Get("Authorization"))
	}
	var body map[string]any
	want := map[string]any{"scope": []any{"https://www.googleapis.com/auth/cloud-platform"}, "lifetime": "3600s",
		"delegates": []any{"projects/-/serviceAccounts/first-hop@acme-vertex.iam.gserviceaccount.com", "projects/-/serviceAccounts/second-hop@acme-vertex.iam.gserviceaccount.com"}}
	if err := json.Unmarshal([]byte(c.body), &body); err != nil || !reflect.DeepEqual(body, want) {
		t.Fatalf("IAM Credentials asked for %s, want %v", c.body, want)
	}
	// an account written escaped is asked for as itself, with no chain
	escaped := filepath.Join(dir, "escaped.json")
	writeFile(t, escaped, vertexImpersonation(at("vertex-runner%40acme-vertex.iam.gserviceaccount.com"), []string{}, vertexUser("1//0g-test-refresh")))
	if auth, err := signVertex(context.Background(), vertexAt("global", escaped), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.impersonated-2" {
		t.Fatalf("escaped: signed with %q: %v", auth, err)
	}
	if c := f.iamCalls()[1]; c.path != "/iam/v1/projects/-/serviceAccounts/"+runner+":generateAccessToken" || strings.Contains(c.body, "delegates") {
		t.Fatalf("escaped: IAM Credentials asked at %s for %s", c.path, c.body)
	}
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"nested.json":     {vertexImpersonation(at(runner), nil, vertexImpersonation(at(runner), nil, vertexUser("1//0g-test-refresh"))), "an impersonation's source is another impersonation"},
		"no-account.json": {vertexImpersonation(at(""), nil, vertexUser("1//0g-test-refresh")), "their service_account_impersonation_url names no service account"},
		"no-source.json":  {vertexImpersonation(at(runner), nil, nil), "their source_credentials can't be read"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 2 || i != 2 {
		t.Fatalf("credentials refused were sent: %d token and %d IAM requests", n, i)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("the host the file's URL names was sent %d requests", n)
	}
}

func TestVertexCredentialsRefused(t *testing.T) {
	home := vertexHome(t)
	f := newVertexGoogle(t)
	dir := t.TempDir()
	sovereign := vertexUser("1//0g-test-refresh")
	sovereign["universe_domain"] = "s3nsapis.fr"
	for name, c := range map[string]struct {
		creds map[string]any
		want  string
	}{
		"workload-identity.json": {map[string]any{
			"type":                              "external_account",
			"audience":                          "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/github/providers/acme",
			"subject_token_type":                "urn:ietf:params:oauth:token-type:jwt",
			"token_url":                         "https://sts.googleapis.com/v1/token",
			"credential_source":                 map[string]any{"file": "/var/run/secrets/tokens/gcp-ksa/token"},
			"service_account_impersonation_url": "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/vertex-runner@acme-vertex.iam.gserviceaccount.com:generateAccessToken",
			"universe_domain":                   "googleapis.com",
		}, "workload identity federation (external_account) isn't read: use a person's credentials, a service account key, or impersonation"},
		"workforce.json": {map[string]any{
			"type":            "external_account_authorized_user",
			"audience":        "//iam.googleapis.com/locations/global/workforcePools/acme/providers/okta",
			"refresh_token":   "workforce-refresh",
			"token_url":       "https://sts.googleapis.com/v1/oauthtoken",
			"token_info_url":  "https://sts.googleapis.com/v1/introspect",
			"client_id":       "workforce-client",
			"client_secret":   "workforce-secret",
			"universe_domain": "googleapis.com",
		}, "workload identity federation (external_account_authorized_user) isn't read: use a person's credentials, a service account key, or impersonation"},
		"sovereign.json": {sovereign, "they are for s3nsapis.fr; Vertex AI is asked at googleapis.com"},
		"gdch.json": {map[string]any{
			"type": "gdch_service_account", "format_version": "1", "project": "acme", "name": "vertex",
			"private_key_id": "abcdef", "private_key": "-----BEGIN EC PRIVATE KEY-----\n-----END EC PRIVATE KEY-----\n",
			"ca_cert_path": "/etc/ssl/acme-ca.pem", "token_uri": "https://service-identity.acme.example/authenticate",
		}, `they are of type "gdch_service_account"; authorized_user, service_account and impersonated_service_account are read`},
		"no-refresh.json": {vertexUser(""), "they have no refresh token"},
	} {
		file := filepath.Join(dir, name)
		writeFile(t, file, c.creds)
		_, err := signVertex(context.Background(), vertexAt("global", file), "gemini-3.5-flash")
		if err == nil || err.Error() != "the Google credentials in "+file+": "+c.want {
			t.Errorf("%s: %v, want %s", name, err, c.want)
		}
	}
	// the page the key was to be downloaded from, saved in its place
	page := filepath.Join(dir, "key.json")
	if err := os.WriteFile(page, []byte("<!DOCTYPE html><html lang=en><title>Sign in - Google Accounts</title></html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := signVertex(context.Background(), vertexAt("global", page), "gemini-3.5-flash"); err == nil || !strings.HasPrefix(err.Error(), page+" holds no Google credentials: ") {
		t.Errorf("not JSON: %v", err)
	}
	gone := filepath.Join(dir, "gone.json")
	if _, err := signVertex(context.Background(), vertexAt("global", gone), "gemini-3.5-flash"); err == nil ||
		err.Error() != "no Google credentials at "+gone+": correct the provider's credentials file, or clear it to use Application Default Credentials" {
		t.Errorf("a missing file given: %v", err)
	}
	want := "no Google credentials at " + gcloudCredentials(home) + ": sign in with `gcloud auth application-default login`, or give the provider a credentials file"
	if _, err := signVertex(context.Background(), vertexAt("global", ""), "gemini-3.5-flash"); err == nil || err.Error() != want {
		t.Errorf("none where gcloud writes them: %v, want %s", err, want)
	}
	if n, i := len(f.tokenCalls()), len(f.iamCalls()); n != 0 || i != 0 {
		t.Fatalf("credentials refused were sent: %d token and %d IAM requests", n, i)
	}
}

func TestVertexCredentialsFile(t *testing.T) {
	home := vertexHome(t)
	dir := t.TempDir()
	given, env, cloudsdk := filepath.Join(dir, "acme-vertex.json"), filepath.Join(dir, "env.json"), filepath.Join(dir, "gcloud-work")
	is := func(in, want string) {
		t.Helper()
		if got, err := vertexCredentialsFile(in); err != nil || got != want {
			t.Errorf("%q: %s (%v), want %s", in, got, err, want)
		}
	}
	is(given, given)
	is("~/keys/acme-vertex.json", filepath.Join(home, "keys", "acme-vertex.json"))
	if runtime.GOOS == "windows" {
		is(`~\keys\acme-vertex.json`, filepath.Join(home, "keys", "acme-vertex.json"))
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "elsewhere"))
	is("", gcloudCredentials(home))
	t.Setenv("CLOUDSDK_CONFIG", cloudsdk)
	is("", filepath.Join(cloudsdk, "application_default_credentials.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", env)
	is("", env)
	is(given, given)
	// a ~ that starts a variable's path is the home, as in the given one
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "~/keys/env.json")
	is("", filepath.Join(home, "keys", "env.json"))
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "~/gcloud-work")
	is("", filepath.Join(home, "gcloud-work", "application_default_credentials.json"))
	// and one that isn't a full path names no file wherever magpie was
	// started: it is refused by name, the next place not looked in
	for _, c := range []struct{ name, v string }{{"GOOGLE_APPLICATION_CREDENTIALS", "env.json"}, {"CLOUDSDK_CONFIG", "gcloud-work"}} {
		t.Setenv(c.name, c.v)
		want := c.name + ` is "` + c.v + `", not a full path: give the whole path, unset it, or give the provider a credentials file`
		if got, err := vertexCredentialsFile(""); err == nil || err.Error() != want || got != "" {
			t.Errorf("%s=%s: %s (%v), want %s", c.name, c.v, got, err, want)
		}
		is(given, given)
		t.Setenv(c.name, "")
	}
	// and a file under the home is the one a request is signed with
	f := newVertexGoogle(t)
	writeFile(t, filepath.Join(home, "keys", "acme-vertex.json"), vertexUser("1//0g-from-home"))
	if auth, err := signVertex(context.Background(), vertexAt("global", "~/keys/acme-vertex.json"), "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("signed with %q: %v", auth, err)
	}
	if form, _ := url.ParseQuery(f.tokenCalls()[0].body); form.Get("refresh_token") != "1//0g-from-home" {
		t.Fatalf("minted from %v", form)
	}
}

func TestVertexSignOnlyAtItsAddress(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	p := vertexAt("global", file)
	p.Headers = map[string]string{"X-Vertex-AI-LLM-Request-Type": "shared"}
	root := f.url + "/vertex/v1/projects/"
	model := "/publishers/google/models/gemini-3.5-flash:streamGenerateContent?alt=sse"
	refuse := func(p Provider, u string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, u, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		err = p.Sign(context.Background(), req, Gemini, []byte("{}"))
		if want := p.Name + " is asked only at Vertex AI, at its project's address"; err == nil || err.Error() != want || req.Header.Get("Authorization") != "" {
			t.Errorf("%s: %v, signed with %q", u, err, req.Header.Get("Authorization"))
		}
	}
	for _, u := range []string{
		root + "acme-vertex-2/locations/global" + model,
		root + "acme-vertex/locations/global-x" + model,
		root + "acme-vertex/locations/us" + model,
		root + "acme-vertex/locations/global",
		"https://generativelanguage.googleapis.com/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse",
		"https://aiplatform.googleapis.com/v1/projects/acme-vertex/locations/global" + model,
	} {
		refuse(p, u)
	}
	for _, v := range []*Vertex{nil, {Credentials: file}, {Project: "Acme Vertex", Credentials: file}} {
		refuse(Provider{Name: "Vertex dev", Preset: VertexPreset, Vertex: v}, root+"acme-vertex/locations/global"+model)
	}
	if n := len(f.tokenCalls()); n != 0 {
		t.Fatalf("%d tokens minted for requests not signed", n)
	}
	req, _ := http.NewRequest(http.MethodPost, p.Base(Gemini)+VertexPath("gemini-3.5-flash"), strings.NewReader("{}"))
	if err := p.Sign(context.Background(), req, Gemini, []byte("{}")); err != nil || req.Header.Get("Authorization") != "Bearer ya29.minted-1" {
		t.Fatalf("at its address: %v, signed with %q", err, req.Header.Get("Authorization"))
	}
	if h := req.Header["X-Vertex-AI-LLM-Request-Type"]; !slices.Equal(h, []string{"shared"}) || req.Header.Get("x-api-key") != "" || req.Header.Get("x-goog-api-key") != "" {
		t.Fatalf("headers %v", req.Header)
	}
	// a domain-scoped project's address has a dot and a colon in it
	q := vertexAt("global", file)
	q.Vertex.Project = "example.com:acme-vertex"
	if auth, err := signVertex(context.Background(), q, "gemini-3.5-flash"); err != nil || auth != "Bearer ya29.minted-1" {
		t.Fatalf("a domain-scoped project: signed with %q: %v", auth, err)
	}
}

func TestVertexTestRequest(t *testing.T) {
	vertexHome(t)
	f := newVertexGoogle(t)
	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	writeFile(t, file, vertexUser("1//0g-test-refresh"))
	q := vertexAt("global", file)
	base := f.url + "/vertex/v1/projects/acme-vertex/locations/global"
	low, budget := map[string]any{"thinkingLevel": "low"}, func(n float64) map[string]any { return map[string]any{"thinkingBudget": n} }
	for model, think := range map[string]map[string]any{
		"gemini-3.5-flash":       low,
		"gemini-3.1-pro-preview": low,
		"gemini-3-flash-preview": low,
		"gemini-2.5-pro":         budget(128),
		"Gemini-2.5-Pro":         budget(128),
		"gemini-2.5-flash":       budget(0),
		"gemini-2.5-flash-lite":  budget(0),
		"gemini-2.0-flash-001":   nil,
	} {
		u, body := vertexTest(q, model)
		if u != base+"/publishers/google/models/"+model+":generateContent" {
			t.Errorf("%s asked at %s", model, u)
		}
		var b struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			Config map[string]any `json:"generationConfig"`
		}
		if err := json.Unmarshal([]byte(body), &b); err != nil || len(b.Contents) != 1 || b.Contents[0].Role != "user" || len(b.Contents[0].Parts) != 1 || b.Contents[0].Parts[0].Text != "hi" {
			t.Errorf("%s: %s (%v)", model, body, err)
		}
		got, _ := b.Config["thinkingConfig"].(map[string]any)
		if b.Config["maxOutputTokens"] != 16.0 || !reflect.DeepEqual(got, think) {
			t.Errorf("%s: %s", model, body)
		}
		if tu, tb := tinyBody(q, Gemini, model); tu != u || tb != body {
			t.Errorf("%s: the probe asks %s %s", model, tu, tb)
		}
	}
	if why := q.ModelTest(); why != "" {
		t.Fatalf("its models can't each be tested: %s", why)
	}
	if err := Save(q); err != nil {
		t.Fatal(err)
	}
	p, err := Find("vertex-prod")
	if err != nil {
		t.Fatal(err)
	}
	rs := p.Test(context.Background())
	if len(rs) != 1 || !rs[0].OK || rs[0].Protocol != Gemini || rs[0].Status != http.StatusOK || rs[0].Model != p.Exposed()[0].ID {
		t.Fatalf("test: %+v", rs)
	}
	rs = p.TestModels(context.Background(), []string{"gemini-2.5-pro", "gemini-9-pro"})
	if !rs[0].OK || rs[1].OK || rs[1].Status != http.StatusNotFound || !strings.Contains(rs[1].Error, "gemini-9-pro") {
		t.Fatalf("models' tests: %+v", rs)
	}
	calls := f.vertexCalls()
	if len(calls) != 3 {
		t.Fatalf("Vertex AI was asked %d times, want 3", len(calls))
	}
	for _, c := range calls {
		if c.header.Get("Authorization") != "Bearer ya29.minted-1" || !strings.HasPrefix(c.path, "/vertex/v1/projects/acme-vertex/locations/global/publishers/google/models/") ||
			!strings.HasSuffix(c.path, ":generateContent") {
			t.Errorf("asked at %s with %q", c.path, c.header.Get("Authorization"))
		}
		if strings.Contains(c.path, "/gemini-2.5-pro:") && !strings.Contains(c.body, `"thinkingBudget":128`) {
			t.Errorf("2.5 Pro tested with %s", c.body)
		}
	}
}
