package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/ChisaAlter/WhaleBridge/internal/netguard"
	"github.com/ChisaAlter/WhaleBridge/internal/netproxy"
	"github.com/ChisaAlter/WhaleBridge/internal/provider"
	"github.com/ChisaAlter/WhaleBridge/internal/settings"
)

// URLs supplied by callers or returned by vendors are fetched from public
// addresses only. A request received on loopback may have been relayed from
// elsewhere; its origin grants no access to this computer or its network.
// settings.json's fetchPrivate can name explicit hosts, host:port pairs,
// addresses or CIDRs. A vendor's own configured endpoint host:port is also
// allowed when fetching that vendor's answer, never a caller's input URL.
//
// Every DNS answer is checked and the chosen address is pinned through a
// direct dial, HTTP CONNECT, SOCKS or a plain HTTP proxy. Each redirect is
// checked again. A named host whose entire answer is a fake-IP pool is an
// exception: pure TUN dials only the checked fake address, while a proxy on
// loopback or localhost receives the name. A remote proxy cannot use that
// exception. What a fake resolver routes the name to is then its decision.
const imageRedirects = 5

var errPrivateAddress = errors.New(`it is at a private address, which magpie doesn't download from: send the image itself (a data: URL or a file) instead, or name the host in settings.json's "fetchPrivate"`)

// lookupIP resolves a download host; tests replace it with isolated answers.
var lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// fetchAllow is the explicit permission for private downloads: names
// (optionally with a port), addresses and subnets. DNS is still pinned.
type fetchAllow struct {
	hosts map[string]bool
	nets  []netip.Prefix
}

func allowFor(also []string) fetchAllow {
	a := fetchAllow{hosts: map[string]bool{}}
	add := func(entry string) {
		s := strings.TrimSpace(entry)
		if s == "" {
			return
		}
		if p, err := netip.ParsePrefix(s); err == nil {
			a.nets = append(a.nets, p.Masked())
		} else if ip, err := netip.ParseAddr(strings.Trim(s, "[]")); err == nil {
			ip = ip.WithZone("").Unmap()
			a.nets = append(a.nets, netip.PrefixFrom(ip, ip.BitLen()))
		} else if host, port, err := net.SplitHostPort(s); err == nil && host != "" && port != "" {
			a.hosts[net.JoinHostPort(fetchHost(host), port)] = true
		} else {
			a.hosts[fetchHost(s)] = true
		}
	}
	if st, err := settings.LoadChecked(); err == nil {
		// Keep the settings slice untouched: appending provider endpoints
		// into spare capacity could give another download that permission.
		for _, entry := range st.FetchPrivate {
			add(entry)
		}
	}
	for _, entry := range also {
		add(entry)
	}
	return a
}

// fetchHost canonicalizes names without changing an IPv6 interface zone.
func fetchHost(host string) string {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return ip.Unmap().String()
	}
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

func (a fetchAllow) host(host, port string) bool {
	host = fetchHost(host)
	return a.hosts[host] || a.hosts[net.JoinHostPort(host, port)]
}

func (a fetchAllow) addr(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	// netip.Prefix.Contains rejects zoned addresses. The zone chooses an
	// interface, not a different network; keep it on the actual pinned dial.
	ip = ip.WithZone("").Unmap()
	for _, p := range a.nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return net.JoinHostPort(fetchHost(u.Hostname()), fetchPort(u))
}

func fetchPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// vendorClient grants only this provider's explicitly configured endpoints,
// including their effective ports. Its caller keeps p.Via on the context.
func vendorClient(p provider.Provider) *http.Client {
	var hosts []string
	for _, raw := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
		if h := hostOf(raw); h != "" {
			hosts = append(hosts, h)
		}
	}
	return guardedClient(hosts...)
}

func publicAddr(a netip.Addr) bool { return netguard.Public(a) }

var privateDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !publicAddr(ap.Addr()) {
			return fmt.Errorf("%s: %w", address, errPrivateAddress)
		}
		return nil
	},
}

type publicTransport struct{ also []string }

// dialPinned allows a test to stand a local server in for a public/TUN dial.
var dialPinned = func(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
	return d.DialContext(ctx, network, addr)
}

// dialMediaProxy is used only for the configured proxy's socket; the
// destination has already passed the address checks and is pinned below.
var dialMediaProxy = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext

func (pt publicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return nil, errors.New("the image URL must be http:// or https://")
	}
	host := req.URL.Hostname()
	port := fetchPort(req.URL)
	var addrs []netip.Addr
	literal := false
	if a, err := netip.ParseAddr(host); err == nil {
		literal = true
		addrs = []netip.Addr{a.Unmap()}
	} else {
		var err error
		addrs, err = lookupIP(req.Context(), host)
		if err != nil {
			return nil, err
		}
		addrs = dropLoon(addrs)
	}
	if len(addrs) == 0 {
		return nil, errors.New("the image URL's host has no addresses")
	}
	// Even a named permission cannot make an invalid DNS answer dialable.
	for _, a := range addrs {
		if !a.IsValid() {
			return nil, fmt.Errorf("%s: %w", host, errPrivateAddress)
		}
	}
	proxy, err := netproxy.Func(req) // choose the route before replacing the name
	if err != nil {
		return nil, err
	}
	fake := !literal && fakeAnswer(addrs)
	byName := fake && proxy != nil && localProxy(proxy)
	if fake && proxy != nil && !byName {
		fake = false
	}
	allow := allowFor(pt.also)
	named := allow.host(host, port)
	for _, a := range addrs {
		if !named && !allow.addr(a) && !publicAddr(a) && !fake {
			return nil, fmt.Errorf("%s: %w", host, errPrivateAddress)
		}
	}
	pinned := addrs[0].Unmap()
	tc := &tls.Config{}
	if upstreamTLS != nil {
		tc = upstreamTLS.Clone()
	}
	tc.ServerName = host
	t := &http.Transport{
		Proxy:                 func(*http.Request) (*url.URL, error) { return proxy, nil },
		TLSClientConfig:       tc,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		DialContext:           dialMediaProxy,
	}
	if proxy == nil {
		d := privateDialer
		if named || len(allow.nets) > 0 || fake {
			copy := *privateDialer
			copy.Control = func(_, address string, _ syscall.RawConn) error {
				ap, err := netip.ParseAddrPort(address)
				if err != nil || !ap.Addr().IsValid() {
					return fmt.Errorf("%s: %w", address, errPrivateAddress)
				}
				a := ap.Addr().Unmap()
				if (named || fake) && a == pinned || allow.addr(a) || publicAddr(a) {
					return nil
				}
				return fmt.Errorf("%s: %w", address, errPrivateAddress)
			}
			d = &copy
		}
		t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialPinned(ctx, d, network, addr)
		}
	}
	out := req.Clone(req.Context())
	u := *req.URL
	if byName {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = net.JoinHostPort(pinned.String(), port)
	}
	out.URL = &u
	if out.Host == "" {
		out.Host = req.URL.Host
	}
	// HTTP proxies resolve the absolute URL/Host themselves; use the pinned
	// IP except for a local fake-IP proxy, which needs the original name.
	if proxy != nil && req.URL.Scheme == "http" && (proxy.Scheme == "http" || proxy.Scheme == "https") {
		out.Host = out.URL.Host
	}
	return t.RoundTrip(out)
}

func dropLoon(addrs []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		if !netguard.LoonPaired(a, addrs) {
			out = append(out, a)
		}
	}
	return out
}

func fakeAnswer(addrs []netip.Addr) bool {
	return len(addrs) > 0 && slices.IndexFunc(addrs, func(a netip.Addr) bool { return !netguard.FakeIP(a) }) < 0
}

func localProxy(u *url.URL) bool {
	host := strings.TrimSuffix(u.Hostname(), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}

var publicClient = guardedClient()

// PublicClient is the guarded downloader for URLs not chosen by magpie.
func PublicClient() *http.Client { return publicClient }

func guardedClient(also ...string) *http.Client {
	return &http.Client{
		Transport: publicTransport{also: slices.Clone(also)},
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) > imageRedirects {
				return fmt.Errorf("it redirects more than %d times", imageRedirects)
			}
			return nil
		},
	}
}
