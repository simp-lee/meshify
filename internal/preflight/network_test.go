package preflight

import (
	"strings"
	"testing"
)

func TestCheckServerDNSMatchesExpectedPublicAddresses(t *testing.T) {
	t.Parallel()

	result := CheckServerDNS(DNSProbe{
		Host:         "hs.example.com",
		ResolvedIPs:  []string{"8.8.8.8", "2001:4860:4860::8888"},
		ExpectedIPv4: "8.8.8.8",
		ExpectedIPv6: "2001:4860:4860::8888",
	})

	if result.Status != StatusPass {
		t.Fatalf("CheckServerDNS() status = %q, want %q", result.Status, StatusPass)
	}
	if len(result.Findings) == 0 {
		t.Fatal("CheckServerDNS() findings = empty, want resolution detail")
	}
}

func TestCheckServerDNSRequiresPublicRoutableAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ips  []string
	}{
		{name: "loopback", ips: []string{"127.0.0.1"}},
		{name: "private", ips: []string{"10.0.0.12", "fd00::12"}},
		{name: "link local", ips: []string{"169.254.10.20", "fe80::1"}},
		{name: "multicast", ips: []string{"224.0.0.1", "ff02::1"}},
		{name: "documentation", ips: []string{"192.0.2.10", "198.51.100.10", "203.0.113.10", "2001:db8::10", "3fff::10"}},
		{name: "cgnat", ips: []string{"100.64.0.10"}},
		{name: "benchmarking", ips: []string{"198.18.0.10"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := CheckServerDNS(DNSProbe{
				Host:        "hs.example.com",
				ResolvedIPs: tt.ips,
			})

			if result.Status != StatusFail {
				t.Fatalf("CheckServerDNS() status = %q, want %q", result.Status, StatusFail)
			}
			if !strings.Contains(result.Summary, "public-routable") {
				t.Fatalf("CheckServerDNS() summary = %q, want public-routable detail", result.Summary)
			}
		})
	}
}

func TestCheckServerDNSPassesWithPublicRoutableAddress(t *testing.T) {
	t.Parallel()

	result := CheckServerDNS(DNSProbe{
		Host:        "hs.example.com",
		ResolvedIPs: []string{"10.0.0.12", "8.8.4.4"},
	})

	if result.Status != StatusPass {
		t.Fatalf("CheckServerDNS() status = %q, want %q", result.Status, StatusPass)
	}
}

func TestCheckServerDNSFailsWhenExpectedAddressMissing(t *testing.T) {
	t.Parallel()

	result := CheckServerDNS(DNSProbe{
		Host:         "hs.example.com",
		ResolvedIPs:  []string{"8.8.8.8"},
		ExpectedIPv4: "1.1.1.1",
	})

	if result.Status != StatusFail {
		t.Fatalf("CheckServerDNS() status = %q, want %q", result.Status, StatusFail)
	}
	if !strings.Contains(strings.Join(result.Findings, "\n"), "1.1.1.1") {
		t.Fatalf("CheckServerDNS() findings = %q, want missing address detail", strings.Join(result.Findings, " | "))
	}
}

func TestCheckPortAvailabilityFlagsConflicts(t *testing.T) {
	t.Parallel()

	result := CheckPortAvailability([]PortBinding{
		{Port: 80, Protocol: "tcp", InUse: true, Process: "nginx"},
		{Port: 443, Protocol: "tcp", InUse: false},
		{Port: 3478, Protocol: "udp", InUse: true, Process: "coturn"},
	})

	if result.Status != StatusFail {
		t.Fatalf("CheckPortAvailability() status = %q, want %q", result.Status, StatusFail)
	}
	joined := strings.Join(result.Findings, "\n")
	for _, want := range []string{"80/tcp", "3478/udp", "nginx", "coturn"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CheckPortAvailability() findings = %q, want substring %q", joined, want)
		}
	}
}

func TestCheckPortAvailabilityRequiresCompleteProbeSet(t *testing.T) {
	t.Parallel()

	result := CheckPortAvailability([]PortBinding{
		{Port: 80, Protocol: "tcp", InUse: false},
		{Port: 443, Protocol: "tcp", InUse: false},
	})

	if result.Status != StatusManual {
		t.Fatalf("CheckPortAvailability() status = %q, want %q", result.Status, StatusManual)
	}
	if !strings.Contains(strings.Join(result.Findings, "\n"), "3478/udp") {
		t.Fatalf("CheckPortAvailability() findings = %q, want missing probe detail", strings.Join(result.Findings, " | "))
	}
}

func TestCheckPortAvailabilityAllowsReviewableWebCoexistence(t *testing.T) {
	t.Parallel()

	result := CheckPortAvailability([]PortBinding{
		{Port: 80, Protocol: "tcp", InUse: true, Process: "nginx"},
		{Port: 443, Protocol: "tcp", InUse: false},
		{Port: 3478, Protocol: "udp", InUse: false},
	})

	if result.Status != StatusWarn {
		t.Fatalf("CheckPortAvailability() status = %q, want %q", result.Status, StatusWarn)
	}
	if !strings.Contains(strings.Join(result.Findings, "\n"), "coexistence review") {
		t.Fatalf("CheckPortAvailability() findings = %q, want coexistence guidance", strings.Join(result.Findings, " | "))
	}
}

func TestCheckPortAvailabilityBlocksNonNginxWebListeners(t *testing.T) {
	t.Parallel()

	for _, process := range []string{"apache2", "caddy", "traefik"} {
		process := process
		t.Run(process, func(t *testing.T) {
			t.Parallel()

			result := CheckPortAvailability([]PortBinding{
				{Port: 80, Protocol: "tcp", InUse: true, Process: process},
				{Port: 443, Protocol: "tcp", InUse: false},
				{Port: 3478, Protocol: "udp", InUse: false},
			})

			if result.Status != StatusFail {
				t.Fatalf("CheckPortAvailability() status = %q, want %q", result.Status, StatusFail)
			}
			if !strings.Contains(strings.Join(result.Findings, "\n"), process) {
				t.Fatalf("CheckPortAvailability() findings = %q, want process detail", strings.Join(result.Findings, " | "))
			}
		})
	}
}

func TestCheckServiceConflictsRequiresInspection(t *testing.T) {
	t.Parallel()

	result := CheckServiceConflicts(nil)

	if result.Status != StatusManual {
		t.Fatalf("CheckServiceConflicts() status = %q, want %q", result.Status, StatusManual)
	}
	if len(result.Remediations) == 0 {
		t.Fatal("CheckServiceConflicts() remediations = empty, want inspection guidance")
	}
}

func TestCheckServiceConflictsBlocksNonNginxWebServices(t *testing.T) {
	t.Parallel()

	for _, service := range []string{"apache2", "caddy", "traefik"} {
		service := service
		t.Run(service, func(t *testing.T) {
			t.Parallel()

			result := CheckServiceConflicts([]ServiceState{{Name: service, Active: true}})

			if result.Status != StatusFail {
				t.Fatalf("CheckServiceConflicts() status = %q, want %q", result.Status, StatusFail)
			}
			if !strings.Contains(strings.Join(result.Findings, "\n"), service) {
				t.Fatalf("CheckServiceConflicts() findings = %q, want service detail", strings.Join(result.Findings, " | "))
			}
			if strings.Contains(result.Summary, "Headscale") {
				t.Fatalf("CheckServiceConflicts() summary = %q, do not want Headscale-only guidance", result.Summary)
			}
			if !strings.Contains(strings.Join(result.Remediations, "\n"), "Apache, Caddy, or Traefik") {
				t.Fatalf("CheckServiceConflicts() remediations = %q, want web-service guidance", strings.Join(result.Remediations, " | "))
			}
		})
	}
}
