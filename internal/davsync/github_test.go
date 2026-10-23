package davsync

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/backup"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

type githubFixture struct {
	mu            sync.Mutex
	objects       map[string][]byte
	writes        int
	status        int
	message       string
	redirect      string
	empty         bool
	refError      string
	initialBranch string
	defaultBranch string
	public        bool
	heads         map[string]string
	blobs         map[string][]byte
	trees         map[string]fixtureTree
	commits       map[string]fixtureCommit
	made          int
	contentsLimit int
	blobLimit     int
	biggestBlob   int
	beforeRef     func()
	requests      []string
}

type fixtureTree struct {
	base    string
	entries []struct {
		Path string
		SHA  *string
	}
}

type fixtureCommit struct{ tree, parent string }

func (f *githubFixture) head(branch string) string {
	if f.heads[branch] == "" {
		f.advance(branch)
	}
	return f.heads[branch]
}

func (f *githubFixture) advance(branch string) {
	f.made++
	sha := fmt.Sprintf("%040x", f.made)
	f.commits[sha] = fixtureCommit{tree: "tree-" + sha, parent: f.heads[branch]}
	f.heads[branch] = sha
}

func (f *githubFixture) branchAt(ref string) (string, bool) {
	for b, h := range f.heads {
		if h == ref {
			return b, true
		}
	}
	if _, ok := f.commits[ref]; ok {
		return "", false
	}
	return ref, true
}

type githubTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t githubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.github.com" {
		return nil, fmt.Errorf("GitHub token sent outside the API: %s", r.URL.Host)
	}
	c := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = t.target.Scheme, t.target.Host
	c.URL = &u
	return t.base.RoundTrip(c)
}

func githubSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func newGithubFixture(t *testing.T) *githubFixture {
	t.Helper()
	f := &githubFixture{objects: map[string][]byte{}, defaultBranch: "main", heads: map[string]string{}, blobs: map[string][]byte{}, trees: map[string]fixtureTree{}, commits: map[string]fixtureCommit{}, blobLimit: 24 << 20}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	target, _ := url.Parse(srv.URL)
	old := syncClient
	syncClient = &http.Client{Transport: githubTransport{old.Transport, target}}
	t.Cleanup(func() { syncClient = old })
	return f
}

func (f *githubFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	send := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(status int, message string) { send(status, map[string]string{"message": message}) }
	if r.Header.Get("Authorization") != "Bearer fixture-github-token" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		fail(401, "Bad credentials")
		return
	}
	if f.status != 0 {
		fail(f.status, f.message)
		return
	}
	if f.redirect != "" {
		http.Redirect(w, r, f.redirect, http.StatusFound)
		return
	}
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if f.git(w, r, send, fail) {
		return
	}
	base := "/repos/alice/private"
	if r.URL.Path == base {
		send(200, map[string]any{"default_branch": f.defaultBranch, "private": !f.public})
		return
	}
	if branch, ok := strings.CutPrefix(r.URL.Path, base+"/git/ref/heads/"); ok {
		if f.empty {
			fail(409, "Git Repository is empty.")
			return
		}
		if f.refError != "" {
			fail(409, f.refError)
			return
		}
		if branch != f.defaultBranch && branch != "sync/settings" {
			fail(404, "Not Found")
			return
		}
		send(200, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": f.head(branch)}})
		return
	}
	if strings.HasPrefix(r.URL.Path, base+"/git/blobs/") {
		sha := strings.TrimPrefix(r.URL.Path, base+"/git/blobs/")
		if b, ok := f.blobs[sha]; ok && r.Header.Get("Accept") == "application/vnd.github.raw+json" {
			w.Write(b)
			return
		}
		for _, b := range f.objects {
			if githubSHA(b) == sha && r.Header.Get("Accept") == "application/vnd.github.raw+json" {
				w.Write(b)
				return
			}
		}
		fail(404, "Not Found")
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, base+"/contents/")
	if !ok {
		fail(404, "Not Found")
		return
	}
	if r.Method == http.MethodGet {
		if f.empty {
			fail(404, "This repository is empty.")
			return
		}
		branch, current := f.branchAt(r.URL.Query().Get("ref"))
		if !current {
			fail(404, "the fixture keeps no old trees")
			return
		}
		b, ok := f.objects[branch+"|"+path]
		if ok {
			c := githubContent{Type: "file", SHA: githubSHA(b), Size: int64(len(b)), Encoding: "base64", Content: base64.StdEncoding.EncodeToString(b)}
			if len(b) > 1<<20 {
				c.Encoding, c.Content = "none", ""
			}
			send(200, c)
			return
		}
		var entries []githubContent
		for key, b := range f.objects {
			if name, ok := strings.CutPrefix(key, branch+"|"+path+"/"); ok && !strings.Contains(name, "/") {
				entries = append(entries, githubContent{Type: "file", Name: name, SHA: githubSHA(b), Size: int64(len(b))})
			}
		}
		if len(entries) > 0 {
			send(200, entries)
			return
		}
		fail(404, "Not Found")
		return
	}
	var in struct{ Message, Branch, SHA, Content string }
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Message == "" {
		fail(422, "Invalid request")
		return
	}
	initialBranch := in.Branch
	if in.Branch == "" {
		in.Branch = f.defaultBranch
	}
	key := in.Branch + "|" + path
	old, exists := f.objects[key]
	if exists && in.SHA == "" {
		fail(422, "Invalid request.\n\n\"sha\" wasn't supplied.")
		return
	}
	if exists && in.SHA != githubSHA(old) || !exists && in.SHA != "" {
		fail(409, "the SHA does not match")
		return
	}
	if r.Method == http.MethodDelete {
		delete(f.objects, key)
		f.advance(in.Branch)
		f.writes++
		send(200, map[string]any{"content": nil})
		return
	}
	b, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil || r.Method != http.MethodPut {
		fail(422, "Invalid content")
		return
	}
	if f.contentsLimit > 0 && len(b) > f.contentsLimit {
		fail(422, "Sorry, the file is too large to be processed")
		return
	}
	f.objects[key] = b
	f.advance(in.Branch)
	f.writes++
	if f.empty {
		f.initialBranch = initialBranch
		f.empty = false
	}
	status := http.StatusOK
	if !exists {
		status = http.StatusCreated
	}
	send(status, map[string]any{"content": githubContent{Type: "file", SHA: githubSHA(b), Size: int64(len(b))}})
}

// git serves immutable blobs and commits, trees based on a parent, and a
// non-force ref update. beforeRef simulates a concurrent writer atomically.
func (f *githubFixture) git(w http.ResponseWriter, r *http.Request, send func(int, any), fail func(int, string)) bool {
	base := "/repos/alice/private/"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == base+"git/blobs":
		var in struct{ Content, Encoding string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Encoding != "base64" {
			fail(422, "invalid blob")
			return true
		}
		b, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			fail(422, "invalid blob content")
			return true
		}
		if len(b) > f.biggestBlob {
			f.biggestBlob = len(b)
		}
		if f.blobLimit > 0 && len(b) > f.blobLimit {
			fail(422, "Sorry, your input was too large to process")
			return true
		}
		sha := githubSHA(b)
		f.blobs[sha] = b
		send(201, map[string]string{"sha": sha})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, base+"git/commits/"):
		sha := strings.TrimPrefix(r.URL.Path, base+"git/commits/")
		c, ok := f.commits[sha]
		if !ok {
			fail(404, "unknown commit")
			return true
		}
		send(200, map[string]any{"tree": map[string]string{"sha": c.tree}})
	case r.Method == http.MethodPost && r.URL.Path == base+"git/trees":
		var in struct {
			Base string `json:"base_tree"`
			Tree []struct {
				Path, Mode, Type string
				SHA              *string
			} `json:"tree"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Base == "" {
			fail(422, "invalid tree")
			return true
		}
		t := fixtureTree{base: in.Base}
		for _, e := range in.Tree {
			if e.Mode != "100644" || e.Type != "blob" {
				fail(422, "invalid tree entry")
				return true
			}
			if e.SHA != nil {
				if _, ok := f.blobs[*e.SHA]; !ok {
					fail(422, "unknown blob")
					return true
				}
			} else {
				found := false
				for branch, head := range f.heads {
					if f.commits[head].tree == in.Base {
						_, found = f.objects[branch+"|"+e.Path]
					}
				}
				if !found {
					fail(422, "cannot delete missing path")
					return true
				}
			}
			t.entries = append(t.entries, struct {
				Path string
				SHA  *string
			}{e.Path, e.SHA})
		}
		f.made++
		sha := fmt.Sprintf("tree-%040x", f.made)
		f.trees[sha] = t
		send(201, map[string]string{"sha": sha})
	case r.Method == http.MethodPost && r.URL.Path == base+"git/commits":
		var in struct {
			Message, Tree string
			Parents       []string
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Parents) != 1 {
			fail(422, "invalid commit")
			return true
		}
		f.made++
		sha := fmt.Sprintf("%040x", f.made)
		f.commits[sha] = fixtureCommit{tree: in.Tree, parent: in.Parents[0]}
		send(201, map[string]string{"sha": sha})
	case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, base+"git/refs/heads/"):
		branch := strings.TrimPrefix(r.URL.Path, base+"git/refs/heads/")
		var in struct {
			SHA   string
			Force *bool
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			fail(422, "invalid ref")
			return true
		}
		if f.beforeRef != nil {
			f.beforeRef()
		}
		c, ok := f.commits[in.SHA]
		if f.heads[branch] == "" || !ok {
			fail(422, "Reference does not exist")
			return true
		}
		if in.Force == nil || *in.Force || c.parent != f.heads[branch] {
			fail(422, "Update is not a fast forward")
			return true
		}
		t := f.trees[c.tree]
		if t.base != f.commits[c.parent].tree {
			fail(422, "invalid base tree")
			return true
		}
		for _, e := range t.entries {
			if e.SHA == nil {
				delete(f.objects, branch+"|"+e.Path)
			} else {
				f.objects[branch+"|"+e.Path] = f.blobs[*e.SHA]
			}
		}
		f.heads[branch] = in.SHA
		f.writes++
		send(200, map[string]any{"ref": "refs/heads/" + branch, "object": map[string]string{"sha": in.SHA}})
	default:
		return false
	}
	return true
}

func githubConfig() Config {
	return Config{URL: "github://alice/private/team", Password: "fixture-github-token",
		Passphrase: "fixture-independent-passphrase", Keys: true, Agents: true}
}

// GitHub uses the same damaged-file diagnostics as WebDAV and S3, but
// the recovery command and location must name the configured repository.
func TestGitHubNotBackup(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		replace    bool
	}{
		{"empty", "", true},
		{"another app", `{"format":"another-app"}`, true},
		{"web page", "<html><title>Sign in</title></html>", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGithubFixture(t)
			c := githubConfig()
			f.objects["main|team/magpie/"+file] = []byte(tc.body)
			newComputer(t).use(t)
			if err := Configure(c); err != nil {
				t.Fatal(err)
			}
			err := Now(context.Background())
			if err == nil || !strings.Contains(err.Error(), "GitHub") || !strings.Contains(err.Error(), "alice/private") || strings.Contains(err.Error(), "WebDAV") {
				t.Fatalf("the recovery advice should name the GitHub repository: %v", err)
			}
			if tc.replace && !strings.Contains(err.Error(), "team/magpie/"+file) || strings.Contains(err.Error(), "magpie/magpie/") {
				t.Fatalf("the advice should name the file where it is, team/magpie/%s: %v", file, err)
			}
			v := Status()
			if v.File == nil || v.File.Replace != tc.replace {
				t.Fatalf("the file's replacement status: %+v", v.File)
			}
			if v.File.Replace && !strings.Contains(err.Error(), "magpie github upload") {
				t.Fatalf("the recovery advice should name the GitHub upload command: %v", err)
			}
			if f.writes != 0 {
				t.Fatal("a failed sync replaced the repository's file")
			}
			if tc.replace {
				if err := Upload(context.Background()); err != nil {
					t.Fatal(err)
				}
				if _, err := backup.Open(f.objects["main|team/magpie/"+file], c.Passphrase); err != nil {
					t.Fatalf("the uploaded GitHub backup doesn't open: %v", err)
				}
			} else if err := Upload(context.Background()); err == nil || f.writes != 0 {
				t.Fatal("a server page must be refused by explicit upload too")
			}
		})
	}
}

func TestGitHubRemote(t *testing.T) {
	f := newGithubFixture(t)
	c := githubConfig()
	c.Branch = "sync/settings"
	g, err := newGitHub(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if b, _, err := g.get(ctx, version{}); err != nil || b != nil {
		t.Fatalf("empty repository folder: %q %v", b, err)
	}
	first := []byte("sealed fixture")
	v, err := g.put(ctx, first, "")
	if err != nil || v.ETag != githubSHA(first) {
		t.Fatalf("create: %+v %v", v, err)
	}
	if b, got, err := g.get(ctx, version{}); err != nil || string(b) != string(first) || got != v {
		t.Fatalf("download: %q %+v %v", b, got, err)
	}
	if _, _, err := g.get(ctx, v); !errors.Is(err, errNotModified) {
		t.Fatalf("unchanged: %v", err)
	}
	if _, err := g.put(ctx, []byte("racing creation"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("another computer created it: %v", err)
	}
	if _, err := g.put(ctx, []byte("stale"), "old-sha"); !errors.Is(err, errChanged) {
		t.Fatalf("another computer updated it: %v", err)
	}
	large := []byte(strings.Repeat("encrypted", 140000))
	v, err = g.put(ctx, large, v.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if b, _, err := g.get(ctx, version{}); err != nil || string(b) != string(large) {
		t.Fatalf("a backup larger than 1 MB: size %d, %v", len(b), err)
	}
	if _, ok := f.objects["main|team/magpie/"+file]; ok {
		t.Fatal("the selected branch was ignored")
	}
	if len(f.objects["sync/settings|team/magpie/"+file]) != len(large) {
		t.Fatal("the prefix or branch was ignored")
	}
	for _, name := range []string{"computer-2026-10-06.magpie-usage", "computer.magpie-quotas"} {
		if err := g.write(ctx, name, first); err != nil {
			t.Fatal(err)
		}
		if err := g.write(ctx, name, []byte("updated")); err != nil {
			t.Fatal(err)
		}
		if b, err := g.read(ctx, name); err != nil || string(b) != "updated" {
			t.Fatalf("usage read: %q %v", b, err)
		}
	}
	list, err := g.list(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("usage listing: %v %v", list, err)
	}
	if err := g.remove(ctx, "computer-2026-10-06.magpie-usage"); err != nil {
		t.Fatal(err)
	}
	if err := g.remove(ctx, "computer-2026-10-06.magpie-usage"); err != nil {
		t.Fatalf("already removed: %v", err)
	}
}

func TestGitHubSync(t *testing.T) {
	f := newGithubFixture(t)
	ctx := context.Background()
	c := githubConfig()
	a, b := newComputer(t), newComputer(t)
	a.use(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "fixture-provider-key"}); err != nil {
		t.Fatal(err)
	}
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	sealed := f.objects["main|team/magpie/"+file]
	bundle, err := backup.Open(sealed, c.Passphrase)
	if err != nil || len(bundle.Providers) != 1 || strings.Contains(string(sealed), "fixture-provider-key") {
		t.Fatalf("encrypted backup on GitHub: %v", err)
	}
	before := f.writes
	if err := Now(ctx); err != nil || f.writes != before {
		t.Fatalf("unchanged setup committed again: %v, writes %d", err, f.writes-before)
	}
	b.use(t)
	if err := Configure(c); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil || !slices.Equal(ids(), []string{"deepseek=fixture-provider-key"}) {
		t.Fatalf("the second computer joins: %v, providers %v", err, ids())
	}
	if err := provider.Save(provider.Provider{ID: "kimi", Name: "Kimi", Chat: "https://api.moonshot.cn/v1", Key: "fixture-second-key"}); err != nil {
		t.Fatal(err)
	}
	if err := Now(ctx); err != nil {
		t.Fatal(err)
	}
	a.use(t)
	if err := Now(ctx); err != nil || len(ids()) != 2 {
		t.Fatalf("the first computer receives the second's change: %v, %v", err, ids())
	}
	if err := provider.Delete("kimi"); err != nil {
		t.Fatal(err)
	}
	parts, err := Restore(ctx)
	if err != nil || !slices.Contains(parts, "providers") || len(ids()) != 2 {
		t.Fatalf("restore from GitHub: %v %v %v", parts, err, ids())
	}
	if _, err := Undo(); err != nil || len(ids()) != 1 {
		t.Fatalf("undo GitHub restore: %v %v", err, ids())
	}
}

func TestGitHubEmptyRepository(t *testing.T) {
	for _, tc := range []struct{ defaultBranch, branch string }{
		{defaultBranch: "main"}, {defaultBranch: "main", branch: "main"},
		{defaultBranch: "trunk"}, {defaultBranch: "trunk", branch: "trunk"},
	} {
		t.Run("default="+tc.defaultBranch+"/branch="+tc.branch, func(t *testing.T) {
			f := newGithubFixture(t)
			f.empty, f.defaultBranch = true, tc.defaultBranch
			newComputer(t).use(t)
			c := githubConfig()
			c.Branch = tc.branch
			if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "fixture-provider-key"}); err != nil {
				t.Fatal(err)
			}
			if err := Configure(c); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := Now(ctx); err != nil {
				t.Fatalf("the first encrypted backup must initialize the empty repository: %v", err)
			}
			sealed := f.objects[tc.defaultBranch+"|team/magpie/"+file]
			bundle, err := backup.Open(sealed, c.Passphrase)
			if err != nil || len(bundle.Providers) != 1 || strings.Contains(string(sealed), "fixture-provider-key") {
				t.Fatalf("the initial commit must contain an encrypted backup: %v", err)
			}
			if f.empty || f.writes != 1 || len(f.objects) != 1 || f.initialBranch != "" {
				t.Fatalf("the backup itself must initialize the default branch: empty %v, writes %d, files %d, branch %q", f.empty, f.writes, len(f.objects), f.initialBranch)
			}
			if err := Now(ctx); err != nil || f.writes != 1 {
				t.Fatalf("the unchanged second sync must not create another commit: %v, writes %d", err, f.writes)
			}
		})
	}
}

func TestGitHubEmptyRepositoryFailures(t *testing.T) {
	for _, tc := range []struct {
		name, branch, refError, want string
		empty                        bool
	}{
		{name: "empty repository with another branch", branch: "new-backup", empty: true, want: "default branch"},
		{name: "existing repository with a missing branch", branch: "new-backup", want: "HTTP 404"},
		{name: "another ref conflict", refError: "Git Database is unavailable.", want: "Git Database is unavailable."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newGithubFixture(t)
			f.empty, f.refError = tc.empty, tc.refError
			c := githubConfig()
			c.Branch = tc.branch
			g, err := newGitHub(c)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = g.get(context.Background(), version{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.writes != 0 {
				t.Fatalf("an inaccessible branch must not become an empty backup: %v, writes %d", err, f.writes)
			}
		})
	}
	t.Run("initial creation races another computer", func(t *testing.T) {
		f := newGithubFixture(t)
		f.empty = true
		g, err := newGitHub(githubConfig())
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if _, _, err := g.get(ctx, version{}); err != nil {
			t.Fatal(err)
		}
		other := []byte("another computer's sealed backup")
		f.mu.Lock()
		f.empty = false
		f.objects["main|team/magpie/"+file] = other
		f.mu.Unlock()
		if _, err := g.put(ctx, []byte("this computer's sealed backup"), ""); !errors.Is(err, errChanged) {
			t.Fatalf("a racing initial commit must require the normal merge: %v", err)
		}
		if f.writes != 0 || string(f.objects["main|team/magpie/"+file]) != string(other) {
			t.Fatal("the racing computer's backup was overwritten")
		}
	})
}

func TestGitHubConfig(t *testing.T) {
	newComputer(t).use(t)
	c := githubConfig()
	for _, address := range []string{"github://alice", "github://alice/private/../other", "github://alice/private/a//b",
		"github://alice/private?token=x", "github://user:secret@alice/private", "github://alice:443/private", "github://./private"} {
		bad := c
		bad.URL = address
		if err := Check(bad); err == nil {
			t.Errorf("accepted invalid address %s", address)
		}
	}
	dav := Config{URL: "https://dav.example/dav", User: "user", Password: "dav-secret", Passphrase: c.Passphrase}
	s3 := Config{URL: "s3://bucket/team", User: "AKID", Password: "s3-secret", Passphrase: c.Passphrase}
	for _, cfg := range []Config{dav, s3, c} {
		if err := Configure(cfg); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := Load()
	for _, kind := range []string{"webdav", "s3"} {
		if s, ok := current.Kept(kind); !ok || s.Password == "" {
			t.Fatalf("lost the %s setup while adding a third kind", kind)
		}
	}
	view, _ := json.Marshal(Status())
	if strings.Contains(string(view), c.Password) || strings.Contains(string(view), "dav-secret") || strings.Contains(string(view), c.Passphrase) {
		t.Fatal("sync status exposed a credential")
	}
	next := c
	next.URL, next.Branch, next.Password, next.Passphrase = "github://alice/private/other", "sync/settings", "", ""
	if err := Configure(next); err != nil {
		t.Fatalf("same repository keeps its token: %v", err)
	}
	current, _ = Load()
	if current.Password != c.Password || current.Branch != next.Branch || current.Passphrase != c.Passphrase {
		t.Fatal("did not keep the token, branch or passphrase")
	}
	next.URL = "github://alice/another"
	if err := Configure(next); err == nil {
		t.Fatal("reused the saved token for another repository")
	}
	same := c
	same.Passphrase = c.Password
	if err := Configure(same); err == nil {
		t.Fatal("accepted the GitHub token as the encryption passphrase")
	}
	dav.Password, dav.Passphrase = "", ""
	if err := Configure(dav); err != nil {
		t.Fatalf("return to the first of three kinds: %v", err)
	}
	current, _ = Load()
	if current.Password != "dav-secret" {
		t.Fatal("did not restore the original WebDAV credential")
	}
}

func TestGitHubFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{{401, "token"}, {403, "Contents"}, {404, "repository or branch"}, {422, "HTTP 422"}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newGithubFixture(t)
			f.status, f.message = tc.status, "fixture refusal"
			g, _ := newGitHub(githubConfig())
			_, _, err := g.get(context.Background(), version{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || f.writes != 0 {
				t.Fatalf("failure treated as an empty remote: %v, writes %d", err, f.writes)
			}
		})
	}
	res := &http.Response{StatusCode: 403, Header: http.Header{"X-Ratelimit-Remaining": {"0"}, "Retry-After": {"120"}},
		Body: io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`))}
	var limited *rateLimited
	if err := githubError(res); !errors.As(err, &limited) || limited.after != 2*time.Minute {
		t.Fatalf("rate limit without backoff: %v", err)
	}
	t.Run("protected branch", func(t *testing.T) {
		f := newGithubFixture(t)
		g, _ := newGitHub(githubConfig())
		if err := g.prepare(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.mu.Lock()
		f.status, f.message = 422, "Protected branch update failed: changes must be made through a pull request"
		f.mu.Unlock()
		_, err := g.put(context.Background(), []byte("encrypted fixture"), "")
		if err == nil || errors.Is(err, errChanged) || !strings.Contains(err.Error(), "Protected branch") {
			t.Fatalf("repository rules treated as a racing file creation: %v", err)
		}
	})
	t.Run("token cannot follow an external redirect", func(t *testing.T) {
		f := newGithubFixture(t)
		f.redirect = "https://outside.api.github.com/token"
		g, _ := newGitHub(githubConfig())
		_, _, err := g.get(context.Background(), version{})
		if err == nil || !strings.Contains(err.Error(), "refused a redirect outside") {
			t.Fatalf("external redirect was followed: %v", err)
		}
	})
}

// A public repository would give anyone every sealed version of the setup,
// its whole history, to try passphrases on: sync refuses it before writing.
func TestGitHubPublicRepositoryRefused(t *testing.T) {
	f := newGithubFixture(t)
	f.public = true
	newComputer(t).use(t)
	if err := Configure(githubConfig()); err != nil {
		t.Fatal(err)
	}
	err := Now(context.Background())
	if err == nil || !strings.Contains(err.Error(), "public") || !strings.Contains(err.Error(), "alice/private") {
		t.Fatalf("a public repository should be refused by name: %v", err)
	}
	if f.writes != 0 || len(f.objects) != 0 {
		t.Fatalf("a public repository got the backup: writes %d, files %d", f.writes, len(f.objects))
	}
}
