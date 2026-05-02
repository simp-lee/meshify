package preflight

import (
	"fmt"
	"meshify/internal/config"
	"strings"
)

type ACMEState struct {
	Challenge             string `json:"challenge,omitempty"`
	ServerHost            string `json:"server_host,omitempty"`
	CertificateEmail      string `json:"certificate_email,omitempty"`
	DNSProvider           string `json:"dns_provider,omitempty"`
	HTTP01Checked         bool   `json:"http01_checked"`
	HTTP01Ready           bool   `json:"http01_ready"`
	HTTP01Detail          string `json:"http01_detail,omitempty"`
	DNSCredentialsChecked bool   `json:"dns_credentials_checked"`
	DNSCredentialsReady   bool   `json:"dns_credentials_ready"`
	DNSCredentialsDetail  string `json:"dns_credentials_detail,omitempty"`
}

func CheckACMEPrerequisites(state ACMEState) CheckResult {
	host := strings.TrimSpace(state.ServerHost)
	challenge := strings.TrimSpace(state.Challenge)
	findings := []string{}
	if host != "" {
		findings = append(findings, fmt.Sprintf("Certificate host: %s.", host))
	}
	if email := strings.TrimSpace(state.CertificateEmail); email != "" {
		findings = append(findings, fmt.Sprintf("ACME account email: %s.", email))
	}

	if host == "" || strings.TrimSpace(state.CertificateEmail) == "" {
		return newCheckResult(
			"acme",
			"ACME prerequisites",
			StatusFail,
			SeverityError,
			"ACME prerequisites are incomplete because the server host or certificate email is missing.",
			findings,
			[]string{"Set default.server_url and default.certificate_email before deploy."},
		)
	}

	switch challenge {
	case config.ACMEChallengeHTTP01:
		if state.HTTP01Checked && !state.HTTP01Ready {
			if detail := strings.TrimSpace(state.HTTP01Detail); detail != "" {
				findings = append(findings, fmt.Sprintf("HTTP-01 detail: %s.", detail))
			}
			return newCheckResult(
				"acme",
				"ACME prerequisites",
				StatusWarn,
				SeverityWarning,
				"HTTP-01 challenge routing was not reachable before deploy; meshify will verify it after Nginx is installed.",
				findings,
				[]string{"If certbot later fails, confirm public port 80 reaches the meshify-managed Nginx challenge path."},
			)
		}
		if detail := strings.TrimSpace(state.HTTP01Detail); detail != "" {
			findings = append(findings, fmt.Sprintf("HTTP-01 detail: %s.", detail))
		}
		return newCheckResult("acme", "ACME prerequisites", StatusPass, SeverityInfo, "HTTP-01 challenge routing will be verified after meshify installs and activates Nginx.", findings, nil)
	case config.ACMEChallengeDNS01:
		provider := strings.TrimSpace(state.DNSProvider)
		if provider == "" {
			return newCheckResult(
				"acme",
				"ACME prerequisites",
				StatusFail,
				SeverityError,
				"DNS-01 is selected but no DNS provider is configured.",
				findings,
				[]string{"Set advanced.dns01.provider before deploy when using dns-01."},
			)
		}
		findings = append(findings, fmt.Sprintf("DNS-01 provider: %s.", provider))
		if !state.DNSCredentialsChecked {
			if detail := strings.TrimSpace(state.DNSCredentialsDetail); detail != "" {
				findings = append(findings, fmt.Sprintf("DNS-01 detail: %s.", detail))
			}
			return newCheckResult(
				"acme",
				"ACME prerequisites",
				StatusFail,
				SeverityError,
				"DNS-01 credentials could not be confirmed automatically.",
				findings,
				[]string{"Expose valid DNS-01 provider credentials in the host environment until meshify can detect them before deploy."},
			)
		}
		if !state.DNSCredentialsReady {
			if detail := strings.TrimSpace(state.DNSCredentialsDetail); detail != "" {
				findings = append(findings, fmt.Sprintf("DNS-01 detail: %s.", detail))
			}
			return newCheckResult(
				"acme",
				"ACME prerequisites",
				StatusFail,
				SeverityError,
				"DNS-01 credentials are not ready for certificate issuance.",
				findings,
				[]string{"Fix the DNS-01 provider credentials in the host environment before deploy."},
			)
		}
		return newCheckResult("acme", "ACME prerequisites", StatusPass, SeverityInfo, "DNS-01 prerequisites look ready for certificate issuance.", findings, nil)
	default:
		return newCheckResult(
			"acme",
			"ACME prerequisites",
			StatusFail,
			SeverityError,
			fmt.Sprintf("Unsupported ACME challenge mode %q.", challenge),
			findings,
			[]string{"Use http-01 or dns-01 as the ACME challenge mode."},
		)
	}
}
