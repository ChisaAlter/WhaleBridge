package gateway

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

func mediaGuardSettings(t *testing.T, allow ...string) {
	t.Helper()
	st := settings.Load()
	st.Proxy = "direct"
	st.FetchPrivate = allow
	if err := settings.Save(st); err != nil {
		t.Fatal(err)
	}
}

func mediaGuardAnswers(t *testing.T, answers map[string][]netip.Addr) {
	t.Helper()
	old := lookupIP
	lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := answers[host]; ok {
			return slices.Clone(a), nil
		}
		return nil, fmt.Errorf("no fixture address for %s", host)
	}
	t.Cleanup(func() { lookupIP = old })
}

func mediaGuardGet(c *http.Client, ctx context.Context, raw string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return "", err
	}
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return string(b), err
}

// Stand in for a public destination, after the actual dial Control check.
func mediaGuardDial(t *testing.T, srv *httptest.Server) func() []string {
	t.Helper()
	old := dialPinned
	var mu sync.Mutex
	var got []string
	dialPinned = func(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
		mu.Lock()
		got = append(got, addr)
		mu.Unlock()
		if d.Control != nil {
			if err := d.Control(network, addr, nil); err != nil {
				return nil, err
			}
		}
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(func() { dialPinned = old })
	return func() []string { mu.Lock(); defer mu.Unlock(); return slices.Clone(got) }
}

func TestMediaGuardExplicitPrivatePermissions(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		io.WriteString(w, "private media")
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.Listener.Addr().String(), "127.0.0.1:")
	mediaGuardAnswers(t, map[string][]netip.Addr{"inside.test": {netip.MustParseAddr("127.0.0.1")}})
	for _, raw := range []string{srv.URL, "http://inside.test:" + port, "http://[::ffff:127.0.0.1]:" + port} {
		if _, err := mediaGuardGet(PublicClient(), context.Background(), raw); !errors.Is(err, errPrivateAddress) {
			t.Fatalf("implicit private fetch %s: %v", raw, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("unlisted private server was reached")
	}
	for _, allow := range []string{"inside.test", "inside.test:" + port, "127.0.0.1", "127.0.0.0/8"} {
		mediaGuardSettings(t, allow)
		if b, err := mediaGuardGet(PublicClient(), context.Background(), "http://inside.test:"+port); err != nil || b != "private media" {
			t.Fatalf("explicit %s: %q %v", allow, b, err)
		}
	}
	mediaGuardSettings(t, "inside.test:1")
	if _, err := mediaGuardGet(PublicClient(), context.Background(), "http://inside.test:"+port); !errors.Is(err, errPrivateAddress) {
		t.Fatal("a host permission on another port authorized this port")
	}
}

func TestMediaGuardAllowCanonicalAndNoSliceAliasing(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t, " Nas.LAN. ", "box.lan:8443", "10.1.0.0/16", "[fd00::1]", "fe80::/10")
	a := allowFor([]string{"Own.LAN.:80"})
	for _, c := range []struct{ host, port string }{{"NAS.lan", "80"}, {"nas.lan.", "443"}, {"box.lan", "8443"}, {"OWN.lan", "80"}} {
		if !a.host(c.host, c.port) {
			t.Fatalf("named permission missing for %+v", c)
		}
	}
	if a.host("box.lan", "443") || a.host("own.lan", "81") || a.host("evilbox.lan", "8443") {
		t.Fatal("exact host/port permission broadened")
	}
	for _, addr := range []string{"10.1.2.3", "::ffff:10.1.2.3", "fd00::1", "fe80::1%Ethernet"} {
		if !a.addr(netip.MustParseAddr(addr)) {
			t.Fatalf("explicit address/CIDR not recognized: %s", addr)
		}
	}
	if a.addr(netip.Addr{}) || a.addr(netip.MustParseAddr("10.2.2.3")) {
		t.Fatal("invalid or other subnet was authorized")
	}
	if allowFor(nil).host("own.lan", "80") {
		t.Fatal("provider permission leaked into settings storage")
	}
	for raw, want := range map[string]string{
		"http://Box.LAN./v1": "box.lan:80", "https://Box.lan/v1": "box.lan:443",
		"https://[fd00::1]/v1": "[fd00::1]:443", "http://[fe80::1%25Ethernet]:8080": "[fe80::1%Ethernet]:8080",
	} {
		if got := hostOf(raw); got != want {
			t.Fatalf("hostOf(%s)=%s, want %s", raw, got, want)
		}
	}
}

func TestMediaGuardVendorExactEndpointsAndRedirect(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { otherHits.Add(1); io.WriteString(w, "other") }))
	defer other.Close()
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		io.WriteString(w, "vendor")
	}))
	defer own.Close()
	p := provider.Provider{Chat: own.URL + "/v1"}
	c := vendorClient(p)
	if b, err := mediaGuardGet(c, context.Background(), own.URL); err != nil || b != "vendor" {
		t.Fatalf("own endpoint: %q %v", b, err)
	}
	if _, err := mediaGuardGet(PublicClient(), context.Background(), own.URL); !errors.Is(err, errPrivateAddress) {
		t.Fatal("vendor endpoint authorized public input client")
	}
	if _, err := mediaGuardGet(c, context.Background(), own.URL+"/redirect"); !errors.Is(err, errPrivateAddress) || otherHits.Load() != 0 {
		t.Fatalf("redirect to another port reached a private service: %v, hits=%d", err, otherHits.Load())
	}
	mediaGuardSettings(t, hostOf(other.URL))
	if b, err := mediaGuardGet(c, context.Background(), own.URL+"/redirect"); err != nil || b != "other" {
		t.Fatalf("explicit redirect target: %q %v", b, err)
	}
	pt := vendorClient(provider.Provider{Chat: "http://own.test/api", Responses: "https://own.test/v1", Anthropic: "http://second.test:444", Gemini: "https://third.test"}).Transport.(publicTransport)
	allow := allowFor(pt.also)
	if !allow.host("OWN.TEST.", "80") || !allow.host("own.test", "443") || allow.host("own.test", "444") || !allow.host("second.test", "444") || !allow.host("third.test", "443") {
		t.Fatal("vendor endpoint default port or host normalization failed")
	}
}

func TestMediaGuardBadSettingsFailClosed(t *testing.T) {
	fresh(t)
	if err := os.MkdirAll(settings.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings.Path(), []byte(`{"fetchPrivate":["inside.test"],"broken":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.LoadChecked(); err == nil {
		t.Fatal("fixture settings were not invalid")
	}
	a := allowFor(nil)
	if a.host("inside.test", "80") || a.addr(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("malformed settings granted a private permission")
	}
	if !allowFor([]string{"own.test:80"}).host("own.test", "80") {
		t.Fatal("bad settings erased a separately explicit vendor endpoint")
	}
}

func TestMediaGuardDNSAllAnswersRebindingAndInvalid(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, r.Host) }))
	defer srv.Close()
	dialled := mediaGuardDial(t, srv)
	mediaGuardAnswers(t, map[string][]netip.Addr{
		"mixed.test":   {netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")},
		"invalid.test": {netip.MustParseAddr("93.184.216.34"), {}},
		"private.test": {netip.MustParseAddr("192.168.1.2")},
	})
	for _, host := range []string{"mixed.test", "invalid.test", "private.test"} {
		if _, err := mediaGuardGet(PublicClient(), context.Background(), "http://"+host); !errors.Is(err, errPrivateAddress) {
			t.Fatalf("unsafe DNS answer %s: %v", host, err)
		}
	}
	mediaGuardSettings(t, "invalid.test")
	if _, err := mediaGuardGet(PublicClient(), context.Background(), "http://invalid.test"); !errors.Is(err, errPrivateAddress) {
		t.Fatal("named authorization accepted an invalid DNS address")
	}
	if len(dialled()) != 0 {
		t.Fatal("unsafe DNS was dialled")
	}
	mediaGuardSettings(t)
	var lookups atomic.Int32
	lookupIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		if lookups.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if b, err := mediaGuardGet(PublicClient(), context.Background(), "http://rebinding.test/image"); err != nil || b != "rebinding.test" {
		t.Fatalf("first pinned request: %q %v", b, err)
	}
	if got := dialled(); !slices.Equal(got, []string{"93.184.216.34:80"}) || lookups.Load() != 1 {
		t.Fatalf("DNS was resolved again after check: %v, lookups=%d", got, lookups.Load())
	}
	if _, err := mediaGuardGet(PublicClient(), context.Background(), "http://rebinding.test/image"); !errors.Is(err, errPrivateAddress) {
		t.Fatal("later private DNS response was not rechecked")
	}
}

func TestMediaGuardZonePinnedAndRedirectLimit(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t, "fe80::/10")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loop" {
			http.Redirect(w, r, "/loop", http.StatusFound)
			return
		}
		io.WriteString(w, "zoned")
	}))
	defer srv.Close()
	dialled := mediaGuardDial(t, srv)
	if b, err := mediaGuardGet(PublicClient(), context.Background(), "http://[fe80::1%25Ethernet]/image"); err != nil || b != "zoned" {
		t.Fatalf("explicit scoped address: %q %v", b, err)
	}
	if got := dialled(); !slices.Equal(got, []string{"[fe80::1%Ethernet]:80"}) {
		t.Fatalf("IPv6 zone was lost: %v", got)
	}
	if _, err := mediaGuardGet(PublicClient(), context.Background(), "http://[fe80::1%25Ethernet]/loop"); err == nil || !strings.Contains(err.Error(), "redirects more than 5") {
		t.Fatalf("redirect limit: %v", err)
	}
}

func TestMediaGuardNamedDialStaysPinned(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t, "named.test")
	mediaGuardAnswers(t, map[string][]netip.Addr{"named.test": {netip.MustParseAddr("127.0.0.1")}})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "named") }))
	defer srv.Close()
	old := dialPinned
	var checked bool
	dialPinned = func(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
		if err := d.Control(network, addr, nil); err != nil {
			return nil, err
		}
		for _, other := range []string{"127.0.0.2:80", "192.168.1.1:80", "[::1]:80", "not-an-address"} {
			if d.Control(network, other, nil) == nil {
				t.Errorf("named permission for %s let an unpinned address through: %s", addr, other)
			}
		}
		checked = true
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(func() { dialPinned = old })
	if b, err := mediaGuardGet(PublicClient(), context.Background(), "http://named.test"); err != nil || b != "named" || !checked {
		t.Fatalf("named final dial check: %q %v, checked=%v", b, err, checked)
	}
}

func TestMediaGuardTLSRootsAreClonedAndSNIIsCheckedHost(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "trusted media") }))
	defer srv.Close()
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	old := upstreamTLS
	upstreamTLS = &tls.Config{RootCAs: roots, ServerName: "untouched.fixture"}
	t.Cleanup(func() { upstreamTLS = old })
	p := provider.Provider{Chat: srv.URL}
	if b, err := mediaGuardGet(vendorClient(p), context.Background(), srv.URL); err != nil || b != "trusted media" {
		t.Fatalf("custom root/SNI: %q %v", b, err)
	}
	if upstreamTLS.ServerName != "untouched.fixture" || upstreamTLS.RootCAs != roots {
		t.Fatal("request mutated the shared TLS configuration")
	}
}

func TestMediaGuardProviderViaAndPublicAddressPinning(t *testing.T) {
	fresh(t)
	mediaGuardSettings(t)
	mediaGuardAnswers(t, map[string][]netip.Addr{"cdn.test": {netip.MustParseAddr("93.184.216.34")}})
	var asked []string
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.String()+" Host="+r.Host)
		io.WriteString(w, "proxy media")
	}))
	defer px.Close()
	p := provider.Provider{Proxy: px.URL}
	if b, err := mediaGuardGet(PublicClient(), p.Via(context.Background()), "http://cdn.test/a"); err != nil || b != "proxy media" {
		t.Fatalf("provider proxy path: %q %v", b, err)
	}
	if !slices.Equal(asked, []string{"http://93.184.216.34:80/a Host=93.184.216.34:80"}) {
		t.Fatalf("proxy got unpinned name: %v", asked)
	}
	if _, err := mediaGuardGet(PublicClient(), netproxy.Hold(context.Background(), errors.New("fixture hold")), "http://cdn.test/a"); err == nil || !strings.Contains(err.Error(), "fixture hold") {
		t.Fatalf("held request lost proxy context: %v", err)
	}
}
