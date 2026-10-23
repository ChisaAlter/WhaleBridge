package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

// Adapted from the reviewed upstream silent-peer regression. Existing TCP
// connections still accept writes after hush, but stop carrying bytes. A new
// connection continues to work, as after a proxy loses its outbound flow.
type whaleSilentPath struct {
	ln   net.Listener
	to   string
	mu   sync.Mutex
	open []*whaleSilentConn
	wg   sync.WaitGroup
}

type whaleSilentConn struct {
	a, b   net.Conn
	silent atomic.Bool
}

func whaleNewSilentPath(t *testing.T, to string) *whaleSilentPath {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &whaleSilentPath{ln: ln, to: to}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			a, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", to)
			if err != nil {
				a.Close()
				continue
			}
			c := &whaleSilentConn{a: a, b: b}
			p.mu.Lock()
			p.open = append(p.open, c)
			p.mu.Unlock()
			pipe := func(dst, src net.Conn) {
				defer p.wg.Done()
				buf := make([]byte, 32<<10)
				for {
					n, err := src.Read(buf)
					if n > 0 && !c.silent.Load() {
						if _, werr := dst.Write(buf[:n]); werr != nil {
							return
						}
					}
					if err != nil {
						return
					}
				}
			}
			p.wg.Add(2)
			go pipe(b, a)
			go pipe(a, b)
		}
	}()
	t.Cleanup(func() {
		p.ln.Close()
		p.mu.Lock()
		for _, c := range p.open {
			c.a.Close()
			c.b.Close()
		}
		p.mu.Unlock()
		p.wg.Wait()
	})
	return p
}

func (p *whaleSilentPath) hush() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.open {
		c.silent.Store(true)
	}
	return len(p.open)
}

func (p *whaleSilentPath) conns() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.open)
}

func whaleQuickHTTP2(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	vendor := httptest.NewUnstartedServer(handler)
	vendor.EnableHTTP2 = true
	vendor.StartTLS()
	t.Cleanup(vendor.Close)
	pool := x509.NewCertPool()
	pool.AddCert(vendor.Certificate())
	oldTLS, oldAfter, oldWait := upstreamTLS, netproxy.PingAfter, netproxy.PingWait
	upstreamTLS = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1"}
	netproxy.PingAfter, netproxy.PingWait = 200*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { upstreamTLS, netproxy.PingAfter, netproxy.PingWait = oldTLS, oldAfter, oldWait })
	return vendor
}

func TestWhaleBridgeSilentHTTP2ConnectionIsDropped(t *testing.T) {
	fresh(t)
	vendor := whaleQuickHTTP2(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, r.Proto)
	})
	path := whaleNewSilentPath(t, vendor.Listener.Addr().String())
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)
	u := "https://" + path.ln.Addr().String() + "/v1/chat/completions"
	ask := func() (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		res, err := s.client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	if got, err := ask(); err != nil || got != "HTTP/2.0" {
		t.Fatalf("first request: %q %v", got, err)
	}
	if n := path.hush(); n != 1 {
		t.Fatalf("connections before silence: %d", n)
	}
	began := time.Now()
	if _, err := ask(); err == nil {
		t.Fatal("silent connection was answered")
	} else if elapsed := time.Since(began); elapsed >= 5*time.Second {
		t.Fatalf("silent peer relied on client timeout: %v %v", elapsed, err)
	}
	if got, err := ask(); err != nil || got != "HTTP/2.0" {
		t.Fatalf("request after silence: %q %v", got, err)
	}
	if n := path.conns(); n != 2 {
		t.Fatalf("connections after recovery: %d", n)
	}
}

func TestWhaleBridgeGroupMovesOnFromSilentHTTP2Connection(t *testing.T) {
	fresh(t)
	vendor := whaleQuickHTTP2(t, func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"a","choices":[{"message":{"role":"assistant","content":"from a"}}]}`)
	})
	path := whaleNewSilentPath(t, vendor.Listener.Addr().String())
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "fixture", Models: []string{"m"}, Chat: "https://" + path.ln.Addr().String() + "/v1"}); err != nil {
		t.Fatal(err)
	}
	serveOn(t, "b", "fixture", []string{"m"}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"b","choices":[{"message":{"role":"assistant","content":"from b"}}]}`)
	}))
	if err := provider.SaveGroup(provider.Group{ID: "auto", Name: "Auto", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	t.Cleanup(s.client.CloseIdleConnections)
	ask := func() (int, string, time.Duration) {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"group/auto","messages":[{"role":"user","content":"hi"}]}`)).WithContext(ctx)
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer test-private-gateway-key")
		began := time.Now()
		s.Handler().ServeHTTP(w, r)
		return w.Code, w.Body.String(), time.Since(began)
	}
	if code, body, _ := ask(); code != http.StatusOK || !strings.Contains(body, "from a") {
		t.Fatalf("initial group route: %d %s", code, body)
	}
	path.hush()
	code, body, took := ask()
	if code != http.StatusOK || !strings.Contains(body, "from b") {
		t.Fatalf("route after silent peer: %d %s", code, body)
	}
	if took >= 5*time.Second {
		t.Fatalf("group waited for client timeout: %v", took)
	}
}
