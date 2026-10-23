package provider

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/ChisaAlter/WhaleBridge/internal/iconproxy"
)

// Shared classification closes old icon-only gaps (0/8, 240/4, IPv6 special
// ranges and tunneled private addresses). Fake-IP remains a separate icon
// compatibility policy; 6to4 now follows its embedded IPv4 instead of being
// denied unconditionally.
func TestIconPublicAddressPolicy(t *testing.T) {
	for text, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "::ffff:8.8.8.8": true,
		"198.18.0.0": true, "198.19.255.255": true, "::ffff:198.18.0.7": true,
		"0.1.2.3": false, "0.255.255.255": false, "240.0.0.1": false, "250.1.2.3": false,
		"127.0.0.1": false, "::ffff:127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "192.0.2.1": false,
		"2001:db8::1": false, "2001::1": false, "100::1": false, "fec0::1": false,
		"64:ff9b:1::808:808": false, "::192.168.1.1": false,
		"64:ff9b::7f00:1": false, "64:ff9b::a00:1": false, "64:ff9b::808:808": true,
		"2002:7f00:1::": false, "2002:a00:1::": false, "2002:808:808::": true,
		// Mapped fake-IP is an actual IPv4 address after unmapping; NAT64
		// and 6to4 fake-IP are not the resolver's direct fake-IP answers.
		"64:ff9b::c612:7": false, "2002:c612:7::": false,
		"fd27:712::c612:7": false, "fe80::1": false,
	} {
		t.Run(text, func(t *testing.T) {
			if got := publicIP(net.ParseIP(text)); got != want {
				t.Fatalf("publicIP = %v, want %v", got, want)
			}
		})
	}
	for _, invalid := range []net.IP{nil, {}, {1, 2, 3}, make(net.IP, 17)} {
		if publicIP(invalid) {
			t.Fatalf("invalid address accepted: %v", invalid)
		}
	}
}

func TestIconDNSAnswersRequireEveryRealAddress(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answers []string
		allowed []string
		reject  bool
	}{
		{"fake A with Loon AAAA", []string{"198.18.1.57", "fd27:712::c612:139"}, []string{"198.18.1.57"}, false},
		{"AAAA first", []string{"fd27:712::c612:139", "198.18.1.57"}, []string{"198.18.1.57"}, false},
		{"mapped A", []string{"fd27:712::808:808", "::ffff:8.8.8.8"}, []string{"8.8.8.8"}, false},
		{"public pair", []string{"8.8.8.8", "fd27:712::808:808"}, []string{"8.8.8.8"}, false},
		{"unpaired Loon", []string{"fd27:712::c612:139"}, nil, true},
		{"mismatched Loon", []string{"8.8.8.8", "fd27:712::c612:139"}, nil, true},
		{"other ULA", []string{"198.18.1.57", "fd27:712:1::c612:139"}, nil, true},
		{"private A paired", []string{"fd27:712::a00:1", "10.0.0.1"}, nil, true},
		{"private A first", []string{"10.0.0.1", "fd27:712::a00:1"}, nil, true},
		{"mixed fake private", []string{"198.18.1.57", "10.0.0.1"}, nil, true},
		{"mixed private fake", []string{"10.0.0.1", "198.18.1.57"}, nil, true},
		{"pair plus private", []string{"fd27:712::c612:139", "198.18.1.57", "127.0.0.1"}, nil, true},
		{"private plus pair", []string{"127.0.0.1", "fd27:712::c612:139", "198.18.1.57"}, nil, true},
		{"mixed fake invalid", []string{"198.18.1.57", "invalid"}, nil, true},
		{"mixed public NAT64 private", []string{"8.8.8.8", "64:ff9b::7f00:1"}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ips []net.IPAddr
			for _, text := range tc.answers {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(text)})
			}
			got, err := iconIPs("fixture.invalid", ips)
			if (err != nil) != tc.reject {
				t.Fatalf("iconIPs error=%v, want reject=%v", err, tc.reject)
			}
			if tc.reject {
				if len(got) != 0 {
					t.Fatal("rejected mixed answers retained dial targets")
				}
				return
			}
			var addresses []string
			for _, ip := range got {
				addresses = append(addresses, ip.IP.String())
			}
			if !slices.Equal(addresses, tc.allowed) {
				t.Fatalf("dial targets %v, want %v", addresses, tc.allowed)
			}
		})
	}
}

func TestIconPublicDialChecksAndPinsLiteralAddress(t *testing.T) {
	for _, tc := range []struct {
		addr    string
		allowed bool
	}{
		{"8.8.8.8:443", true}, {"198.18.0.7:443", true}, {"[64:ff9b::808:808]:443", true},
		{"127.0.0.1:443", false}, {"0.1.2.3:443", false}, {"240.0.0.1:443", false},
		{"[64:ff9b::7f00:1]:443", false}, {"[2002:7f00:1::]:443", false}, {"[fd27:712::c612:7]:443", false},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			var dialed string
			stop := errors.New("fake dial stopped before network")
			_, err := publicDial(context.Background(), func(_ context.Context, network, addr string) (net.Conn, error) { dialed = addr; return nil, stop }, "tcp", tc.addr)
			if tc.allowed {
				if !errors.Is(err, stop) || dialed != tc.addr {
					t.Fatalf("dialed=%q err=%v", dialed, err)
				}
			} else if err == nil || dialed != "" {
				t.Fatalf("rejected address reached dial: %q %v", dialed, err)
			}
		})
	}
}

func TestIconGuardPreservesProxyAndChecksRedirect(t *testing.T) {
	for _, host := range []string{"8.8.8.8", "198.18.0.7"} {
		t.Run(host, func(t *testing.T) {
			var calls int
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Host != host {
					t.Errorf("proxy received host %q, want %q", r.URL.Host, host)
				}
				http.Redirect(w, r, "http://127.0.0.1/private", http.StatusFound)
			}))
			t.Cleanup(proxy.Close)
			proxyURL, _ := url.Parse(proxy.URL)
			previous := http.DefaultTransport
			transport := previous.(*http.Transport).Clone()
			transport.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			client := guardClient()
			t.Cleanup(client.CloseIdleConnections)
			res, err := client.Get("http://" + host + "/icon")
			if res != nil {
				res.Body.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "not public") || calls != 1 {
				t.Fatalf("redirect guard: requests=%d err=%v", calls, err)
			}
		})
	}
}

func TestFetchIconKeepsIconProxyAndContentStorage(t *testing.T) {
	isolate(t)
	image := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1" height="1"><rect width="1" height="1"/></svg>`)
	var requests int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/api/icon" || r.URL.Query().Get("url") != "https://supplier.invalid/icon.svg" || r.Header.Get("Accept") != "image/*" {
			t.Errorf("icon proxy request %s headers=%v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		_, _ = w.Write(image)
	}))
	t.Cleanup(proxy.Close)
	t.Setenv("MAGPIE_ICON_PROXY", proxy.URL+"/api/icon")
	previous := iconClient
	iconClient = func() *http.Client { return proxy.Client() }
	t.Cleanup(func() { iconClient = previous })
	icon, err := FetchIcon(context.Background(), "https://supplier.invalid/icon.svg")
	if err != nil || requests != 1 || !strings.HasPrefix(icon, iconPrefix) {
		t.Fatalf("icon=%q requests=%d err=%v", icon, requests, err)
	}
	stored, err := os.ReadFile(IconFile(strings.TrimPrefix(icon, iconPrefix)))
	if err != nil || !bytes.Equal(stored, image) {
		t.Fatalf("stored icon mismatch: %v", err)
	}
	if again, err := StoreIcon(image); err != nil || again != icon {
		t.Fatal("same content no longer reuses the stored icon")
	}
	for _, raw := range []string{"http://supplier.invalid/icon.svg", "https://localhost/icon.svg", "https://user:password@supplier.invalid/icon.svg"} {
		if _, err := FetchIcon(context.Background(), raw); err == nil || requests != 1 {
			t.Fatalf("invalid icon URL reached proxy: %q", raw)
		}
	}
	// With no override the existing proxy default still wraps foreign hosts.
	t.Setenv("MAGPIE_ICON_PROXY", "")
	if got := iconproxy.URL("https://supplier.invalid/icon.svg"); !strings.HasPrefix(got, iconproxy.Base+"?url=") {
		t.Fatalf("default proxy changed: %s", got)
	}
}
