// Package netguard classifies addresses used by download guards. Each caller
// chooses how to handle the answers: pin public addresses, discard paired
// synthetic records, or apply its own explicit fake-IP compatibility policy.
package netguard

import "net/netip"

// specialNets excludes special-use ranges in addition to those net/netip
// already identifies as loopback, private, link-local or multicast.
var specialNets = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this" network
		"100.64.0.0/10",   // CGNAT, Tailscale
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking; fake-IP resolvers (FakeIP)
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, broadcast
		"::/96",           // IPv4-compatible (deprecated)
		"100::/64",        // discard
		"2001::/32",       // Teredo
		"2001:db8::/32",   // documentation
		"fec0::/10",       // site-local (deprecated)
		"64:ff9b:1::/48",  // local-use NAT64
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var (
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
	fakeNet = netip.MustParsePrefix("198.18.0.0/15")
	loonNet = netip.MustParsePrefix("fd27:712::/96")
)

// Public reports whether a reaches the open internet. IPv4-mapped, NAT64
// and 6to4 forms inherit the embedded IPv4 address's classification. Fake-IP
// answers are not public; callers must opt into FakeIP independently.
func Public(a netip.Addr) bool {
	a = a.WithZone("").Unmap() // a scope identifier cannot bypass range checks
	if !a.IsValid() || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return false
	}
	for _, n := range specialNets {
		if n.Contains(a) {
			return false
		}
	}
	if a.Is6() {
		b := a.As16()
		if nat64.Contains(a) {
			return Public(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		}
		if b[0] == 0x20 && b[1] == 0x02 {
			return Public(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		}
	}
	return true
}

// FakeIP reports RFC 2544's 198.18.0.0/15 benchmarking range, used by
// Clash, Surge and Loon to stand in for names. This says nothing about the
// destination the resolver or proxy will choose for that name.
func FakeIP(a netip.Addr) bool { return fakeNet.Contains(a.WithZone("").Unmap()) }

// LoonPaired identifies a synthesized AAAA record in fd27:712::/96 only
// when its embedded A record is present among answers. The caller discards
// that AAAA and must still judge the matching A address on its own. Pairing
// never makes a private A, an unpaired AAAA or the rest of fc00::/7 public.
func LoonPaired(a netip.Addr, answers []netip.Addr) bool {
	a = a.WithZone("")
	if !loonNet.Contains(a) {
		return false
	}
	b := a.As16()
	v4 := netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	for _, other := range answers {
		if other.WithZone("").Unmap() == v4 {
			return true
		}
	}
	return false
}
