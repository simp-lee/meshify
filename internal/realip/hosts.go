package realip

import (
	"strings"
)

func CoveredByL7Hosts(domain string, l7Hosts []string) bool {
	domain = normalizeHost(domain)
	if domain == "" {
		return false
	}
	for _, raw := range l7Hosts {
		host := normalizeHost(raw)
		if host == "" {
			continue
		}
		if host == domain {
			return true
		}
		if wildcardCovers(host, domain) {
			return true
		}
	}
	return false
}

func MissingL7HostCoverage(domains []string, l7Hosts []string) []string {
	missing := []string{}
	for _, domain := range domains {
		normalized := normalizeHost(domain)
		if normalized == "" {
			continue
		}
		if !CoveredByL7Hosts(normalized, l7Hosts) {
			missing = append(missing, normalized)
		}
	}
	return missing
}

func wildcardCovers(pattern string, domain string) bool {
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	if suffix == "" || domain == suffix || !strings.HasSuffix(domain, "."+suffix) {
		return false
	}
	left := strings.TrimSuffix(domain, "."+suffix)
	return left != "" && !strings.Contains(left, ".")
}

func normalizeHost(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimSuffix(value, ".")
}
