package preflight

import (
	"meshify/internal/config"
	"strings"
	"testing"
)

func TestCheckACMEPrerequisites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		state       ACMEState
		wantStatus  Status
		wantSummary string
		wantText    string
	}{
		{
			name: "http01 passes when predeploy route probe is deferred",
			state: ACMEState{
				Challenge:        config.ACMEChallengeHTTP01,
				ServerHost:       "hs.example.com",
				CertificateEmail: "ops@example.com",
			},
			wantStatus:  StatusPass,
			wantSummary: "HTTP-01 challenge routing will be verified after meshify installs and activates Nginx.",
			wantText:    "Certificate host: hs.example.com.",
		},
		{
			name: "http01 warns when optional probe reports blocked routing",
			state: ACMEState{
				Challenge:        config.ACMEChallengeHTTP01,
				ServerHost:       "hs.example.com",
				CertificateEmail: "ops@example.com",
				HTTP01Checked:    true,
				HTTP01Ready:      false,
				HTTP01Detail:     "http://hs.example.com/.well-known/acme-challenge/meshify-preflight returned 403.",
			},
			wantStatus:  StatusWarn,
			wantSummary: "HTTP-01 challenge routing was not reachable before deploy; meshify will verify it after Nginx is installed.",
			wantText:    "HTTP-01 detail",
		},
		{
			name: "http01 passes when probe confirms reachability",
			state: ACMEState{
				Challenge:        config.ACMEChallengeHTTP01,
				ServerHost:       "hs.example.com",
				CertificateEmail: "ops@example.com",
				HTTP01Checked:    true,
				HTTP01Ready:      true,
			},
			wantStatus:  StatusPass,
			wantSummary: "HTTP-01 challenge routing will be verified after meshify installs and activates Nginx.",
		},
		{
			name: "dns01 fails when provider is missing",
			state: ACMEState{
				Challenge:        config.ACMEChallengeDNS01,
				ServerHost:       "hs.example.com",
				CertificateEmail: "ops@example.com",
			},
			wantStatus:  StatusFail,
			wantSummary: "DNS-01 is selected but no DNS provider is configured.",
		},
		{
			name: "dns01 fails when credentials are not confirmed",
			state: ACMEState{
				Challenge:        config.ACMEChallengeDNS01,
				ServerHost:       "hs.example.com",
				CertificateEmail: "ops@example.com",
				DNSProvider:      "cloudflare",
			},
			wantStatus:  StatusFail,
			wantSummary: "DNS-01 credentials could not be confirmed automatically.",
			wantText:    "DNS-01 provider: cloudflare.",
		},
		{
			name: "dns01 passes when credentials are ready",
			state: ACMEState{
				Challenge:             config.ACMEChallengeDNS01,
				ServerHost:            "hs.example.com",
				CertificateEmail:      "ops@example.com",
				DNSProvider:           "cloudflare",
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   true,
			},
			wantStatus:  StatusPass,
			wantSummary: "DNS-01 prerequisites look ready for certificate issuance.",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := CheckACMEPrerequisites(tt.state)
			if result.Status != tt.wantStatus {
				t.Fatalf("CheckACMEPrerequisites() status = %q, want %q", result.Status, tt.wantStatus)
			}
			if result.Summary != tt.wantSummary {
				t.Fatalf("CheckACMEPrerequisites() summary = %q, want %q", result.Summary, tt.wantSummary)
			}
			if tt.wantText != "" && !strings.Contains(strings.Join(result.Findings, "\n"), tt.wantText) {
				t.Fatalf("CheckACMEPrerequisites() findings = %q, want substring %q", strings.Join(result.Findings, " | "), tt.wantText)
			}
		})
	}
}
