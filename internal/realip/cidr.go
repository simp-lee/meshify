package realip

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

var disallowedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("100:0:0:1::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var ipv6GlobalUnicastPrefix = netip.MustParsePrefix("2000::/3")

func CanonicalCIDRs(values []string) ([]string, error) {
	seen := map[netip.Prefix]struct{}{}
	canonical := make([]netip.Prefix, 0, len(values))
	for _, raw := range values {
		prefix, err := parseTrustedPrefix(raw)
		if err != nil {
			return nil, err
		}
		if err := validateTrustedPrefix(prefix); err != nil {
			return nil, err
		}
		if _, ok := seen[prefix]; ok {
			continue
		}
		seen[prefix] = struct{}{}
		canonical = append(canonical, prefix)
	}
	if len(canonical) == 0 {
		return nil, fmt.Errorf("trusted CIDR list is empty")
	}
	slices.SortFunc(canonical, func(left netip.Prefix, right netip.Prefix) int {
		leftAddr := left.Addr()
		rightAddr := right.Addr()
		if cmp := leftAddr.Compare(rightAddr); cmp != 0 {
			return cmp
		}
		return left.Bits() - right.Bits()
	})
	out := make([]string, 0, len(canonical))
	for _, prefix := range canonical {
		out = append(out, prefix.String())
	}
	return out, nil
}

func parseTrustedPrefix(raw string) (netip.Prefix, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Prefix{}, fmt.Errorf("trusted CIDR entry is empty")
	}
	if strings.Contains(value, "/") {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("trusted CIDR %q is invalid: %w", value, err)
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("trusted IP %q is invalid: %w", value, err)
	}
	addr = addr.Unmap()
	if addr.Is4() {
		return netip.PrefixFrom(addr, 32), nil
	}
	return netip.PrefixFrom(addr, 128), nil
}

func validateTrustedPrefix(prefix netip.Prefix) error {
	addr := prefix.Addr().Unmap()
	prefix = netip.PrefixFrom(addr, prefix.Bits()).Masked()
	if !prefix.IsValid() {
		return fmt.Errorf("trusted CIDR %q is invalid", prefix.String())
	}
	if (addr.Is4() && prefix.Bits() == 0) || (addr.Is6() && prefix.Bits() == 0) {
		return fmt.Errorf("trusted CIDR %q must not trust all sources", prefix.String())
	}
	if addr.IsUnspecified() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsPrivate() {
		return fmt.Errorf("trusted CIDR %q is not a public EdgeOne origin ACL range", prefix.String())
	}
	if addr.Is6() && !prefixWithin(prefix, ipv6GlobalUnicastPrefix) {
		return fmt.Errorf("trusted CIDR %q must be within IPv6 global unicast range %q", prefix.String(), ipv6GlobalUnicastPrefix.String())
	}
	for _, blocked := range disallowedPrefixes {
		if prefixesOverlap(prefix, blocked) {
			return fmt.Errorf("trusted CIDR %q overlaps disallowed range %q", prefix.String(), blocked.String())
		}
	}
	return nil
}

func prefixWithin(child netip.Prefix, parent netip.Prefix) bool {
	if child.Addr().Is4() != parent.Addr().Is4() {
		return false
	}
	return child.Bits() >= parent.Bits() && parent.Contains(child.Addr())
}

func prefixesOverlap(left netip.Prefix, right netip.Prefix) bool {
	if left.Addr().Is4() != right.Addr().Is4() {
		return false
	}
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}
