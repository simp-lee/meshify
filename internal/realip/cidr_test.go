package realip

import (
	"strings"
	"testing"
)

func TestCanonicalCIDRsCanonicalizesMasksDedupeAndSorts(t *testing.T) {
	t.Parallel()

	got, err := CanonicalCIDRs([]string{"9.9.9.9", "8.8.8.8/24", "9.9.9.9/32", "2606:4700:4700::1111"})
	if err != nil {
		t.Fatalf("CanonicalCIDRs() error = %v", err)
	}
	want := []string{"8.8.8.0/24", "9.9.9.9/32", "2606:4700:4700::1111/128"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("CanonicalCIDRs() = %#v, want %#v", got, want)
	}
}

func TestCanonicalCIDRsRejectsUnsafeRanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input []string
		want  string
	}{
		{name: "empty list", input: nil, want: "trusted CIDR list is empty"},
		{name: "empty entry", input: []string{""}, want: "trusted CIDR entry is empty"},
		{name: "invalid", input: []string{"not-ip"}, want: "invalid"},
		{name: "trust all ipv4", input: []string{"0.0.0.0/0"}, want: "trust all"},
		{name: "trust all ipv6", input: []string{"::/0"}, want: "trust all"},
		{name: "private", input: []string{"10.0.0.0/8"}, want: "not a public EdgeOne"},
		{name: "loopback", input: []string{"127.0.0.1"}, want: "not a public EdgeOne"},
		{name: "link local", input: []string{"169.254.1.1"}, want: "not a public EdgeOne"},
		{name: "multicast", input: []string{"224.0.0.1"}, want: "not a public EdgeOne"},
		{name: "documentation", input: []string{"203.0.113.0/24"}, want: "overlaps disallowed range"},
		{name: "deprecated 6to4 relay anycast", input: []string{"192.88.99.0/24"}, want: "overlaps disallowed range"},
		{name: "as112 special purpose ipv4", input: []string{"192.31.196.1"}, want: "overlaps disallowed range"},
		{name: "amt special purpose ipv4", input: []string{"192.52.193.0/24"}, want: "overlaps disallowed range"},
		{name: "ipv6 reserved low address", input: []string{"::2/128"}, want: "must be within IPv6 global unicast"},
		{name: "ipv6 reserved 4000 range", input: []string{"4000::/3"}, want: "must be within IPv6 global unicast"},
		{name: "ipv6 reserved high range", input: []string{"8000::/1"}, want: "must be within IPv6 global unicast"},
		{name: "unique local ipv6", input: []string{"fc00::/7"}, want: "not a public EdgeOne"},
		{name: "well known nat64 ipv6", input: []string{"64:ff9b::/96"}, want: "must be within IPv6 global unicast"},
		{name: "dummy ipv6 prefix", input: []string{"100:0:0:1::/64"}, want: "must be within IPv6 global unicast"},
		{name: "teredo ipv6", input: []string{"2001::/32"}, want: "overlaps disallowed range"},
		{name: "orchidv2 ipv6", input: []string{"2001:20::/28"}, want: "overlaps disallowed range"},
		{name: "six to four ipv6", input: []string{"2002::/16"}, want: "overlaps disallowed range"},
		{name: "sr sid ipv6", input: []string{"5f00::/16"}, want: "must be within IPv6 global unicast"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := CanonicalCIDRs(tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CanonicalCIDRs(%#v) error = %v, want substring %q", tt.input, err, tt.want)
			}
		})
	}
}
