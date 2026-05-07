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
			name: "dns01 fails when required DNS env file is not ready",
			state: ACMEState{
				Challenge:             config.ACMEChallengeDNS01,
				ServerHost:            "hs.example.com",
				CertificateEmail:      "ops@example.com",
				DNSProvider:           "cloudflare",
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   false,
				DNSCredentialsDetail:  "DNS provider \"cloudflare\" requires advanced.dns01.env_file so initial issuance and meshify-lego-renew.service use the same provider environment.",
			},
			wantStatus:  StatusFail,
			wantSummary: "DNS-01 credentials are not ready for certificate issuance or renewal.",
			wantText:    "requires advanced.dns01.env_file",
		},
		{
			name: "dns01 passes for digitalocean on debian because lego is distro-independent",
			state: ACMEState{
				Challenge:             config.ACMEChallengeDNS01,
				ServerHost:            "hs.example.com",
				CertificateEmail:      "ops@example.com",
				PlatformID:            "debian",
				PlatformVersion:       "13",
				DNSProvider:           "digitalocean",
				DNSCredentialEnvFile:  "/etc/meshify/dns01/digitalocean.env",
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   true,
			},
			wantStatus:  StatusPass,
			wantSummary: "DNS-01 prerequisites look ready for certificate issuance.",
			wantText:    "DNS-01 env file: /etc/meshify/dns01/digitalocean.env.",
		},
		{
			name: "dns01 passes for google application credentials on ubuntu",
			state: ACMEState{
				Challenge:             config.ACMEChallengeDNS01,
				ServerHost:            "hs.example.com",
				CertificateEmail:      "ops@example.com",
				PlatformID:            "ubuntu",
				PlatformVersion:       "24.04",
				DNSProvider:           "google",
				DNSCredentialEnvFile:  "/etc/meshify/dns01/gcloud.env",
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   true,
				DNSCredentialsDetail:  "Using lego env_file for DNS provider \"gcloud\": /etc/meshify/dns01/gcloud.env.",
			},
			wantStatus:  StatusPass,
			wantSummary: "DNS-01 prerequisites look ready for certificate issuance.",
			wantText:    "DNS-01 provider: google.",
		},
		{
			name: "dns01 passes for route53 ambient credential chain",
			state: ACMEState{
				Challenge:             config.ACMEChallengeDNS01,
				ServerHost:            "hs.example.com",
				CertificateEmail:      "ops@example.com",
				DNSProvider:           "route53",
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   true,
				DNSCredentialsDetail:  "Using lego ambient credential chain for DNS provider \"route53\".",
			},
			wantStatus:  StatusPass,
			wantSummary: "DNS-01 prerequisites look ready for certificate issuance.",
			wantText:    "DNS-01 provider: route53.",
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
