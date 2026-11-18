package app

import (
	"net"
	"net/netip"
)

func isPublicUnicastIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	address, ok := netipFromIP(ip)
	if !ok || !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, blocked := range clippingReservedIPPrefixes {
		if blocked.Contains(address) {
			return false
		}
	}
	return true
}

// netipFromIP is isolated to make all address forms, including IPv4-mapped
// IPv6, pass through the same special-purpose range checks.
func netipFromIP(ip net.IP) (netip.Addr, bool) {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

var clippingReservedIPPrefixes = mustClippingPrefixes([]string{
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
	"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20",
})

func mustClippingPrefixes(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefixes = append(prefixes, netip.MustParsePrefix(value))
	}
	return prefixes
}
