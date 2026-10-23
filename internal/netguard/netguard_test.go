package netguard

import (
	"net/netip"
	"testing"
)

func TestPublic(t *testing.T) {
	for text, want := range map[string]bool{
		"0.0.0.0": false, "0.1.2.3": false, "0.255.255.255": false,
		"1.0.0.0": true, "8.8.8.8": true, "93.184.216.34": true,
		"10.0.0.1": false, "127.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.0": false, "100.127.255.255": false,
		"100.63.255.255": true, "100.128.0.0": true,
		"192.0.0.1": false, "192.0.2.1": false, "198.51.100.1": false, "203.0.113.1": false,
		"198.17.255.255": true, "198.18.0.0": false, "198.19.255.255": false, "198.20.0.0": true,
		"224.0.0.1": false, "239.255.255.255": false, "240.0.0.0": false, "250.1.2.3": false, "255.255.255.255": false,
		"::": false, "::1": false, "::192.168.1.1": false,
		"fc00::1": false, "fd27:712::c612:7": false, "fe80::1": false, "ff02::1": false,
		"100::1": false, "2001::1": false, "2001:db8::1": false, "fec0::1": false, "64:ff9b:1::808:808": false,
		"2606:4700::1111": true, "2001:4860:4860::8888": true,
		"::ffff:8.8.8.8": true, "::ffff:127.0.0.1": false, "::ffff:198.18.0.7": false,
		"64:ff9b::808:808": true, "64:ff9b::7f00:1": false, "64:ff9b::a00:1": false,
		"64:ff9b::c612:7": false, "64:ff9b::f001:203": false,
		"2002:0808:0808::": true, "2002:7f00:0001::": false, "2002:0a00:0001::": false,
		"2002:c612:7::": false, "2002:0001:0203::": false,
		// A zone identifies an interface, not a different address range.
		"2606:4700::1111%fixture": true, "fe80::1%fixture": false,
		"2001:db8::1%fixture": false, "64:ff9b::7f00:1%fixture": false,
		"2002:7f00:1::%fixture": false, "::ffff:127.0.0.1%fixture": false,
	} {
		t.Run(text, func(t *testing.T) {
			if got := Public(netip.MustParseAddr(text)); got != want {
				t.Fatalf("Public = %v, want %v", got, want)
			}
		})
	}
	if Public(netip.Addr{}) {
		t.Fatal("an invalid address became public")
	}
}

// Each embedded IPv4 form must retain the same classification, including
// fake-IP and reserved networks, rather than inheriting the IPv6 prefix.
func TestPublicEmbeddedIPv4(t *testing.T) {
	for _, text := range []string{"8.8.8.8", "0.1.2.3", "10.1.2.3", "127.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.7", "192.0.2.1", "240.0.0.1"} {
		v4 := netip.MustParseAddr(text)
		b := v4.As4()
		mapped := [16]byte{10: 0xff, 11: 0xff, 12: b[0], 13: b[1], 14: b[2], 15: b[3]}
		nat := [16]byte{0: 0x00, 1: 0x64, 2: 0xff, 3: 0x9b, 12: b[0], 13: b[1], 14: b[2], 15: b[3]}
		six := [16]byte{0: 0x20, 1: 0x02, 2: b[0], 3: b[1], 4: b[2], 5: b[3]}
		for _, raw := range [][16]byte{mapped, nat, six} {
			a := netip.AddrFrom16(raw)
			for _, withZone := range []netip.Addr{a, a.WithZone("fixture")} {
				if Public(withZone) != Public(v4) {
					t.Errorf("%s differs from embedded %s", withZone, v4)
				}
			}
		}
	}
}

func TestFakeIP(t *testing.T) {
	for text, want := range map[string]bool{
		"198.18.0.0": true, "198.18.0.7": true, "198.19.255.255": true, "::ffff:198.18.0.7": true,
		"::ffff:198.18.0.7%fixture": true,
		"198.17.255.255":            false, "198.20.0.0": false, "127.0.0.1": false, "10.0.0.1": false,
		"fd27:712::c612:7": false, "2002:c612:7::": false, "64:ff9b::c612:7": false,
	} {
		if got := FakeIP(netip.MustParseAddr(text)); got != want {
			t.Errorf("FakeIP(%s) = %v, want %v", text, got, want)
		}
	}
	if FakeIP(netip.Addr{}) {
		t.Fatal("invalid address became fake-IP")
	}
}

func TestLoonPaired(t *testing.T) {
	addresses := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	for _, tc := range []struct {
		name, address string
		answers       []netip.Addr
		paired        bool
	}{
		{"fake A", "fd27:712::c612:139", addresses("198.18.1.57", "fd27:712::c612:139"), true},
		{"public A", "fd27:712::808:808", addresses("8.8.8.8"), true},
		{"mapped A", "fd27:712::808:808", addresses("::ffff:8.8.8.8"), true},
		{"zoned pair", "fd27:712::808:808%fixture", addresses("::ffff:8.8.8.8%fixture"), true},
		// Pairing alone does not authorize a private A: callers judge it.
		{"private A", "fd27:712::a00:1", addresses("10.0.0.1"), true},
		{"unpaired", "fd27:712::c612:139", addresses("fd27:712::c612:139"), false},
		{"other A", "fd27:712::c612:139", addresses("8.8.8.8"), false},
		{"other ULA", "fd27:712:1::808:808", addresses("8.8.8.8"), false},
		{"IPv4", "8.8.8.8", addresses("8.8.8.8"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := LoonPaired(netip.MustParseAddr(tc.address), tc.answers); got != tc.paired {
				t.Fatalf("paired=%v, want %v", got, tc.paired)
			}
		})
	}
	if LoonPaired(netip.Addr{}, []netip.Addr{{}}) {
		t.Fatal("invalid address paired")
	}
}
