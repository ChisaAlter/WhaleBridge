package gateway

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
)

func TestMediaGuardFakeIPAnswerMatrix(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	mediaGuardAnswers(t, map[string][]netip.Addr{
		"fake.test":          {netip.MustParseAddr("198.18.0.7"), netip.MustParseAddr("198.19.0.8")},
		"loon.test":          {netip.MustParseAddr("fd27:712::c612:7"), netip.MustParseAddr("198.18.0.7")},
		"mixed-public.test":  {netip.MustParseAddr("198.18.0.7"), netip.MustParseAddr("93.184.216.34")},
		"mixed-private.test": {netip.MustParseAddr("198.18.0.7"), netip.MustParseAddr("192.168.1.2")},
		"unpaired.test":      {netip.MustParseAddr("fd27:712::c612:8"), netip.MustParseAddr("198.18.0.7")},
		"private-loon.test":  {netip.MustParseAddr("fd27:712::a00:1"), netip.MustParseAddr("10.0.0.1")},
	})
	var hosts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host)
		io.WriteString(w, "TUN media")
	}))
	defer srv.Close()
	old := dialPinned
	var dialled []string
	dialPinned = func(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
		if err := d.Control(network, addr, nil); err != nil {
			return nil, err
		}
		for _, other := range []string{"198.18.0.8:80", "198.19.1.1:443", "127.0.0.1:80", "192.168.1.2:80", "not-an-address"} {
			if other != addr && d.Control(network, other, nil) == nil {
				t.Errorf("TUN guard for %s allowed %s", addr, other)
			}
		}
		dialled = append(dialled, addr)
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(func() { dialPinned = old })
	for _, host := range []string{"fake.test", "loon.test"} {
		if b, err := mediaGuardGet(PublicClient(), context.Background(), "http://"+host); err != nil || b != "TUN media" {
			t.Fatalf("named all-fake %s: %q %v", host, b, err)
		}
	}
	if !slices.Equal(dialled, []string{"198.18.0.7:80", "198.18.0.7:80"}) || !slices.Equal(hosts, []string{"fake.test", "loon.test"}) {
		t.Fatalf("TUN lost pinned address or original Host: %v, %v", dialled, hosts)
	}
	for _, raw := range []string{
		"http://198.18.0.7/", "https://198.19.1.2/", "http://[::ffff:198.18.0.7]/",
		"http://mixed-public.test/", "http://mixed-private.test/", "http://unpaired.test/", "http://private-loon.test/",
	} {
		if _, err := mediaGuardGet(PublicClient(), context.Background(), raw); !errors.Is(err, errPrivateAddress) {
			t.Errorf("unsafe fake-IP shape %s: %v", raw, err)
		}
	}
	if len(dialled) != 2 {
		t.Fatalf("refused fake shape was dialled: %v", dialled)
	}
}

func TestMediaGuardFakeIPLocalHTTPProxyAndRedirect(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	mediaGuardAnswers(t, map[string][]netip.Addr{
		"fake.test":    {netip.MustParseAddr("198.18.0.7")},
		"private.test": {netip.MustParseAddr("127.0.0.1")},
	})
	var mu sync.Mutex
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.Method+" "+r.URL.String()+" Host="+r.Host)
		mu.Unlock()
		if r.Method == "CONNECT" {
			w.WriteHeader(200)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://private.test/", http.StatusFound)
			return
		}
		io.WriteString(w, "local proxy")
	}))
	defer proxy.Close()
	port := strings.TrimPrefix(proxy.Listener.Addr().String(), "127.0.0.1:")
	for _, choice := range []string{proxy.URL, "http://localhost:" + port} {
		if b, err := mediaGuardGet(PublicClient(), netproxy.With(context.Background(), choice), "http://fake.test/image"); err != nil || b != "local proxy" {
			t.Fatalf("local proxy %s: %q %v", choice, b, err)
		}
	}
	if _, err := mediaGuardGet(PublicClient(), netproxy.With(context.Background(), proxy.URL), "http://fake.test/redirect"); !errors.Is(err, errPrivateAddress) {
		t.Fatalf("local proxy redirect bypassed private check: %v", err)
	}
	// A caller-supplied Host must not steer an absolute HTTP proxy URL
	// away from the URL whose DNS answer was checked.
	foreign, err := http.NewRequestWithContext(netproxy.With(context.Background(), proxy.URL), "GET", "http://fake.test/image", nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign.Host = "private.test:99"
	res, err := PublicClient().Do(foreign)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// A CONNECT has to name the host for a local fake-IP proxy. The fake
	// proxy forwards nothing, so TLS fails after its captured authority.
	if _, err := mediaGuardGet(PublicClient(), netproxy.With(context.Background(), proxy.URL), "https://fake.test/image"); err == nil {
		t.Fatal("non-tunnelling CONNECT fixture unexpectedly completed TLS")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 5 || !strings.Contains(asked[0], "http://fake.test:80/image Host=fake.test:80") || !strings.Contains(asked[3], "http://fake.test:80/image Host=fake.test:80") || !strings.HasPrefix(asked[4], "CONNECT //fake.test:443 Host=fake.test:443") {
		t.Fatalf("local fake proxy was not given the original name: %v", asked)
	}
}

func TestMediaGuardRemoteProxyFakeOnlyAndExplicitAuthorization(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	mediaGuardAnswers(t, map[string][]netip.Addr{"fake.test": {netip.MustParseAddr("198.18.0.7")}})
	var asked []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.String())
		io.WriteString(w, "explicit proxy")
	}))
	defer proxy.Close()
	// The configured proxy is remote; its socket alone is substituted by
	// the local fixture, so no external address is contacted.
	old := dialMediaProxy
	dialMediaProxy = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, proxy.Listener.Addr().String())
	}
	t.Cleanup(func() { dialMediaProxy = old })
	ctx := netproxy.With(context.Background(), "http://203.0.113.9:3128")
	if _, err := mediaGuardGet(PublicClient(), ctx, "http://fake.test/image"); !errors.Is(err, errPrivateAddress) || len(asked) != 0 {
		t.Fatalf("remote proxy accepted automatic fake-only permission: %v, %v", err, asked)
	}
	for _, allow := range []string{"fake.test", "198.18.0.0/15"} {
		mediaGuardSettings(t, allow)
		if b, err := mediaGuardGet(PublicClient(), ctx, "http://fake.test/image"); err != nil || b != "explicit proxy" {
			t.Fatalf("remote proxy rejected independent explicit %s: %q %v", allow, b, err)
		}
	}
	mediaGuardSettings(t)
	if b, err := mediaGuardGet(vendorClient(providerForFakeMedia()), ctx, "http://fake.test/image"); err != nil || b != "explicit proxy" {
		t.Fatalf("remote proxy rejected explicit vendor endpoint: %q %v", b, err)
	}
	if !slices.Equal(asked, []string{"http://198.18.0.7:80/image", "http://198.18.0.7:80/image", "http://198.18.0.7:80/image"}) {
		t.Fatalf("explicit remote proxy got a resolvable name instead of pinned address: %v", asked)
	}
}

func providerForFakeMedia() provider.Provider { return provider.Provider{Chat: "http://fake.test/v1"} }

func TestMediaGuardFakeIPLocalSOCKSUsesName(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	mediaGuardAnswers(t, map[string][]netip.Addr{"fake.test": {netip.MustParseAddr("198.18.0.7")}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	asked := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errs <- err
			return
		}
		defer conn.Close()
		var head [2]byte
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			errs <- err
			return
		}
		methods := make([]byte, int(head[1]))
		if _, err := io.ReadFull(conn, methods); err != nil {
			errs <- err
			return
		}
		if _, err := conn.Write([]byte{5, 0}); err != nil {
			errs <- err
			return
		}
		var req [4]byte
		if _, err := io.ReadFull(conn, req[:]); err != nil {
			errs <- err
			return
		}
		if req[3] != 3 {
			errs <- fmt.Errorf("SOCKS got address type %d, want domain", req[3])
			return
		}
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil {
			errs <- err
			return
		}
		host := make([]byte, int(size[0])+2)
		if _, err := io.ReadFull(conn, host); err != nil {
			errs <- err
			return
		}
		asked <- string(host[:len(host)-2]) + fmt.Sprintf(":%d", uint16(host[len(host)-2])<<8|uint16(host[len(host)-1]))
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
		if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
			errs <- err
			return
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nConnection: close\r\n\r\nsocks")
		errs <- err
	}()
	if b, err := mediaGuardGet(PublicClient(), netproxy.With(context.Background(), "socks5://"+ln.Addr().String()), "http://fake.test/image"); err != nil || b != "socks" {
		t.Fatalf("local SOCKS fake-IP: %q %v", b, err)
	}
	if got := <-asked; got != "fake.test:80" {
		t.Fatalf("SOCKS was given %s", got)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
}

func TestMediaGuardLocalProxyClassification(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://localhost:1": true, "http://LOCALHOST.:1": true, "http://127.0.0.2:1": true,
		"socks5://[::1]:1": true, "http://[::ffff:127.0.0.1]:1": true,
		"http://localhost.evil:1": false, "http://10.0.0.1:1": false, "http://203.0.113.9:1": false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := localProxy(u); got != want {
			t.Errorf("localProxy(%s)=%v", raw, got)
		}
	}
}
