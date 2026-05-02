package config

import (
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"strings"
)

type validationErrors []string

func (errs validationErrors) Error() string {
	return strings.Join(errs, "; ")
}

func (c Config) Validate() error {
	var errs validationErrors

	if strings.TrimSpace(c.APIVersion) == "" {
		errs = append(errs, "api_version is required")
	} else if c.APIVersion != APIVersion {
		errs = append(errs, fmt.Sprintf("api_version must be %q", APIVersion))
	}

	serverHost := ""
	serverURL := strings.TrimSpace(c.Default.ServerURL)
	if serverURL == "" {
		errs = append(errs, "default.server_url is required")
	} else {
		parsedURL, err := url.Parse(serverURL)
		if err != nil {
			errs = append(errs, fmt.Sprintf("default.server_url must be a valid URL: %v", err))
		} else {
			if parsedURL.Scheme != "https" {
				errs = append(errs, "default.server_url must use https")
			}
			serverHost = normalizeDomain(parsedURL.Hostname())
			if serverHost == "" {
				errs = append(errs, "default.server_url host is required")
			} else if !looksLikeDNSName(serverHost) {
				errs = append(errs, "default.server_url host must be a DNS name")
			}
		}
	}

	baseDomain := normalizeDomain(c.Default.BaseDomain)
	if baseDomain == "" {
		errs = append(errs, "default.base_domain is required")
	} else if !looksLikeDNSName(baseDomain) {
		errs = append(errs, "default.base_domain must be a DNS name")
	}

	if baseDomain != "" && serverHost != "" {
		if baseDomain == serverHost {
			errs = append(errs, "default.base_domain must not equal default.server_url host")
		}
		if hasDomainSuffix(serverHost, baseDomain) {
			errs = append(errs, "default.base_domain must not be a parent domain of default.server_url host")
		}
	}

	email := strings.TrimSpace(c.Default.CertificateEmail)
	if email == "" {
		errs = append(errs, "default.certificate_email is required")
	} else {
		address, err := mail.ParseAddress(email)
		if err != nil || address.Address != email {
			errs = append(errs, "default.certificate_email must be a plain email address")
		}
	}

	acmeChallenge := strings.TrimSpace(c.Default.ACMEChallenge)
	switch acmeChallenge {
	case ACMEChallengeHTTP01, ACMEChallengeDNS01:
	case "":
		errs = append(errs, "default.acme_challenge is required")
	default:
		errs = append(errs, "default.acme_challenge must be one of: http-01, dns-01")
	}

	validatePackageSource(&errs, c.Advanced.PackageSource)
	validateProxyURL(&errs, "advanced.proxy.http_proxy", c.Advanced.Proxy.HTTPProxy)
	validateProxyURL(&errs, "advanced.proxy.https_proxy", c.Advanced.Proxy.HTTPSProxy)
	validateDNS01(&errs, acmeChallenge, c.Advanced.DNS01)
	validateIPOverride(&errs, "advanced.network.public_ipv4", c.Advanced.Network.PublicIPv4, false)
	validateIPOverride(&errs, "advanced.network.public_ipv6", c.Advanced.Network.PublicIPv6, true)
	validatePlatform(&errs, c.Advanced.Platform)

	if len(errs) == 0 {
		return nil
	}

	return errs
}

func validatePackageSource(errs *validationErrors, source PackageSourceConfig) {
	mode := strings.TrimSpace(source.Mode)
	version := strings.TrimSpace(source.Version)
	urlValue := strings.TrimSpace(source.URL)
	sha256 := strings.TrimSpace(source.SHA256)
	filePath := strings.TrimSpace(source.FilePath)

	if mode == "" {
		*errs = append(*errs, "advanced.package_source.mode is required")
		return
	}
	if version == "" {
		*errs = append(*errs, "advanced.package_source.version is required")
	}

	switch mode {
	case PackageSourceModeDirect:
		if urlValue != "" {
			*errs = append(*errs, "advanced.package_source.url is only allowed when advanced.package_source.mode is mirror")
		}
		if filePath != "" {
			*errs = append(*errs, "advanced.package_source.file_path is only allowed when advanced.package_source.mode is offline")
		}
	case PackageSourceModeMirror:
		if urlValue == "" {
			*errs = append(*errs, "advanced.package_source.url is required when advanced.package_source.mode is mirror")
		} else {
			validateAbsoluteURL(errs, "advanced.package_source.url", urlValue)
		}
		if filePath != "" {
			*errs = append(*errs, "advanced.package_source.file_path is only allowed when advanced.package_source.mode is offline")
		}
		if sha256 == "" {
			*errs = append(*errs, "advanced.package_source.sha256 is required when advanced.package_source.mode is mirror")
		}
	case PackageSourceModeOffline:
		if filePath == "" {
			*errs = append(*errs, "advanced.package_source.file_path is required when advanced.package_source.mode is offline")
		} else if filepath.Clean(filePath) == "." {
			*errs = append(*errs, "advanced.package_source.file_path must be a valid file path")
		}
		if urlValue != "" {
			*errs = append(*errs, "advanced.package_source.url is only allowed when advanced.package_source.mode is mirror")
		}
		if sha256 == "" {
			*errs = append(*errs, "advanced.package_source.sha256 is required when advanced.package_source.mode is offline")
		}
	default:
		*errs = append(*errs, "advanced.package_source.mode must be one of: direct, mirror, offline")
	}

	if sha256 != "" && !isHexSHA256(sha256) {
		*errs = append(*errs, "advanced.package_source.sha256 must be a 64-character lowercase hexadecimal SHA-256 digest")
	}
}

func validateDNS01(errs *validationErrors, acmeChallenge string, dns01 DNS01Config) {
	provider := strings.TrimSpace(dns01.Provider)
	if acmeChallenge == ACMEChallengeDNS01 && provider == "" {
		*errs = append(*errs, "advanced.dns01.provider is required when default.acme_challenge is dns-01")
	}
}

func validateProxyURL(errs *validationErrors, field string, raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}

	parsedURL, err := url.Parse(raw)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		*errs = append(*errs, field+" must be an absolute URL")
	}
}

func validateAbsoluteURL(errs *validationErrors, field string, raw string) {
	parsedURL, err := url.Parse(raw)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		*errs = append(*errs, field+" must be an absolute URL")
	}
}

func validateIPOverride(errs *validationErrors, field string, raw string, wantIPv6 bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}

	ip := net.ParseIP(raw)
	if ip == nil {
		*errs = append(*errs, field+" must be a valid IP address")
		return
	}

	if wantIPv6 {
		if ip.To4() != nil {
			*errs = append(*errs, field+" must be an IPv6 address")
		}
		return
	}

	if ip.To4() == nil {
		*errs = append(*errs, field+" must be an IPv4 address")
	}
}

func validatePlatform(errs *validationErrors, platform PlatformConfig) {
	arch := strings.TrimSpace(platform.Arch)
	if arch == "" {
		*errs = append(*errs, "advanced.platform.arch is required")
		return
	}

	switch arch {
	case ArchAMD64, ArchARM64:
		return
	default:
		*errs = append(*errs, "advanced.platform.arch must be one of: amd64, arm64")
	}
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimSuffix(value, ".")
}

func hasDomainSuffix(host string, domain string) bool {
	return host != domain && strings.HasSuffix(host, "."+domain)
}

func looksLikeDNSName(value string) bool {
	if value == "" || len(value) > 253 {
		return false
	}
	if net.ParseIP(value) != nil {
		return false
	}

	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}

	for _, label := range labels {
		if label == "" || len(label) > 63 {
			return false
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}

	return true
}

func isHexSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}

	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}

	return true
}
