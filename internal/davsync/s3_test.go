package davsync

import (
	"context"
	"crypto/md5"
	"encoding/hex"
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
)

// fakeS3 is an S3 server with one bucket and one access key, keeping
// objects in memory. It checks every request's signature as S3 does —
// signed again from what arrived, so a header or path that changed on the
// way fails — and does conditional PUTs, or, noCond, refuses them as a
// server without them does.
type fakeS3 struct {
	mu                 sync.Mutex
	id, secret, region string
	bucket             string
	objects            map[string][]byte
	etags              map[string]string
	noCond             bool
	log                []string // each request: method, and the condition sent
	onWrite            func()   // runs once, at the next PUT or HEAD (a write's look first), before it is answered
	sent               int      // the bytes of objects read
}

func newFakeS3(t *testing.T) (*fakeS3, *httptest.Server) {
	f := &fakeS3{id: "AKIDTEST", secret: "s3cr3t/+key", region: "us-east-1", bucket: "bkt",
		objects: map[string][]byte{}, etags: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeS3) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	if !f.signed(r, body) {
		f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != f.bucket {
		f.fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	cond := r.Header.Get("If-Match")
	if v := r.Header.Get("If-None-Match"); v != "" {
		cond = "none:" + v
	}
	f.log = append(f.log, strings.TrimSpace(r.Method+" "+cond))
	if f.onWrite != nil && (r.Method == http.MethodPut || r.Method == http.MethodHead) {
		f.onWrite()
		f.onWrite = nil
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		b, ok := f.objects[key]
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			f.fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		w.Header().Set("ETag", f.etags[key])
		// a read of the version already had: 304, and no body
		if r.Method == http.MethodGet && r.Header.Get("If-None-Match") == f.etags[key] {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodGet {
			f.sent += len(b)
			w.Write(b)
		}
	case http.MethodPut:
		m, none := r.Header.Get("If-Match"), r.Header.Get("If-None-Match")
		if f.noCond && (m != "" || none != "") {
			f.fail(w, http.StatusNotImplemented, "NotImplemented")
			return
		}
		_, there := f.objects[key]
		if none == "*" && there || m != "" && m != f.etags[key] {
			f.fail(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		f.store(key, body)
		w.Header().Set("ETag", f.etags[key])
	default:
		f.fail(w, http.StatusNotImplemented, "NotImplemented")
	}
}

func (f *fakeS3) store(key string, b []byte) {
	h := md5.Sum(b)
	f.objects[key], f.etags[key] = b, `"`+hex.EncodeToString(h[:])+`"`
}

// signed is whether r carries this key's signature over what arrived.
func (f *fakeS3) signed(r *http.Request, body []byte) bool {
	auth := r.Header.Get("Authorization")
	var cred, names string
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "Credential":
			cred = v
		case "SignedHeaders":
			names = v
		}
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil || !strings.HasPrefix(cred, f.id+"/"+at.Format("20060102")+"/"+f.region+"/s3/") || r.Header.Get("X-Amz-Content-Sha256") != sum(body) {
		return false
	}
	u, err := url.Parse("http://" + r.Host + r.RequestURI)
	if err != nil {
		return false
	}
	// the path as S3 has it: decoded, then encoded again as SigV4 says —
	// a + sent bare is signed as %2B
	u.RawPath = awsEscape(u.Path, true)
	again := &http.Request{Method: r.Method, URL: u, Host: r.Host, Header: http.Header{}}
	for _, n := range strings.Split(names, ";") {
		if n != "host" {
			again.Header[http.CanonicalHeaderKey(n)] = r.Header.Values(n)
		}
	}
	signer{id: f.id, secret: f.secret, region: f.region, service: "s3"}.sign(again, sum(body), at)
	return again.Header.Get("Authorization") == auth
}

func (f *fakeS3) config(srv *httptest.Server) Config {
	return Config{URL: "s3://bkt/team x+y", Endpoint: srv.URL, User: f.id, Password: f.secret, Passphrase: "correct horse", Keys: true, Agents: true}
}

// The object read with its ETag, written with If-None-Match: * the first
// time and If-Match after: a write over a version since changed is
// refused, as another computer's first one is over one already there.
func TestS3Put(t *testing.T) {
	f, srv := newFakeS3(t)
	ctx := context.Background()
	s, err := newS3(f.config(srv))
	if err != nil {
		t.Fatal(err)
	}
	if data, v, err := s.get(ctx, version{}); data != nil || v.ETag != "" || err != nil {
		t.Fatalf("nothing there: %q %q %v", data, v.ETag, err)
	}
	if _, err := s.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "one" {
		t.Fatalf("stored %q", got)
	}
	if _, err := s.put(ctx, []byte("another first"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("a first write over one there: %v", err)
	}
	data, v, err := s.get(ctx, version{})
	etag := v.ETag
	if string(data) != "one" || etag == "" || err != nil {
		t.Fatalf("read: %q %q %v", data, etag, err)
	}
	if _, err := s.put(ctx, []byte("two"), etag); err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(ctx, []byte("three"), etag); !errors.Is(err, errChanged) {
		t.Fatalf("a write over a version since changed: %v", err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "two" {
		t.Fatalf("stored %q", got)
	}
	want := []string{"GET", "PUT none:*", "PUT none:*", "GET", "PUT " + etag, "PUT " + etag}
	if !slices.Equal(f.log, want) {
		t.Fatalf("requests %q, want %q", f.log, want)
	}

	// what went wrong, said
	for _, c := range []struct {
		change func(*Config)
		want   string
	}{
		{func(c *Config) { c.Password = "wrong" }, "refused the access key ID or secret"},
		{func(c *Config) { c.User = "AKIDOTHER" }, "refused the access key ID or secret"},
		{func(c *Config) { c.URL = "s3://nope" }, "there is no bucket nope"},
	} {
		cfg := f.config(srv)
		c.change(&cfg)
		s, _ := newS3(cfg)
		if _, _, err := s.get(ctx, version{}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", cfg, err, c.want)
		}
	}
}

// A server that answers a conditional PUT 501 NotImplemented: the ETag is
// looked at with a HEAD before a plain PUT, which still refuses a write
// over another computer's, and the server is not asked for one again.
func TestS3NoConditions(t *testing.T) {
	f, srv := newFakeS3(t)
	f.noCond = true
	ctx := context.Background()
	s, err := newS3(f.config(srv))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(ctx, []byte("one"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.put(ctx, []byte("another first"), ""); !errors.Is(err, errChanged) {
		t.Fatalf("a first write over one there: %v", err)
	}
	_, v, _ := s.get(ctx, version{})
	etag := v.ETag
	f.mu.Lock()
	f.store("team x+y/magpie/magpie.magpie-backup", []byte("theirs"))
	f.mu.Unlock()
	if _, err := s.put(ctx, []byte("two"), etag); !errors.Is(err, errChanged) {
		t.Fatalf("a write over another computer's: %v", err)
	}
	_, v, _ = s.get(ctx, version{})
	etag = v.ETag
	if _, err := s.put(ctx, []byte("two"), etag); err != nil {
		t.Fatal(err)
	}
	if got := string(f.objects["team x+y/magpie/magpie.magpie-backup"]); got != "two" {
		t.Fatalf("stored %q", got)
	}
	want := []string{"PUT none:*", "HEAD", "PUT", "HEAD", "GET", "HEAD", "GET", "HEAD", "PUT"}
	if !slices.Equal(f.log, want) {
		t.Fatalf("requests %q, want %q", f.log, want)
	}
	for _, e := range []s3Error{{Status: 400, Code: "InvalidArgument", Message: "If-None-Match is not supported"}, {Status: 501}, {Status: 400, Code: "NotImplemented"}} {
		if !noConditions(e) {
			t.Errorf("%+v: not taken for no conditional writes", e)
		}
	}
	if noConditions(s3Error{Status: 400, Code: "InvalidArgument", Message: "Invalid storage class"}) {
		t.Error("any 400 taken for no conditional writes")
	}
}
