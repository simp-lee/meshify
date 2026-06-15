package realip

import "testing"

func TestCoveredByL7HostsExactAndSingleLevelWildcard(t *testing.T) {
	t.Parallel()

	hosts := []string{"app.example.com", "*.example.net"}
	for _, domain := range []string{"app.example.com", "www.example.net"} {
		if !CoveredByL7Hosts(domain, hosts) {
			t.Fatalf("CoveredByL7Hosts(%q) = false, want true", domain)
		}
	}
	for _, domain := range []string{"example.net", "a.b.example.net", "other.example.com"} {
		if CoveredByL7Hosts(domain, hosts) {
			t.Fatalf("CoveredByL7Hosts(%q) = true, want false", domain)
		}
	}
}

func TestMissingL7HostCoverage(t *testing.T) {
	t.Parallel()

	got := MissingL7HostCoverage([]string{"app.example.com", "api.example.com", "deep.api.example.com"}, []string{"app.example.com", "*.example.com"})
	if len(got) != 1 || got[0] != "deep.api.example.com" {
		t.Fatalf("MissingL7HostCoverage() = %#v, want deep.api.example.com", got)
	}
}
