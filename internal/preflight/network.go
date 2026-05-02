package preflight

import (
	"fmt"
	"meshify/internal/config"
	"net/netip"
	"sort"
	"strings"
)

type DNSProbe struct {
	Host         string   `json:"host,omitempty"`
	ResolvedIPs  []string `json:"resolved_ips,omitempty"`
	LookupError  string   `json:"lookup_error,omitempty"`
	ExpectedIPv4 string   `json:"expected_ipv4,omitempty"`
	ExpectedIPv6 string   `json:"expected_ipv6,omitempty"`
}

type PortBinding struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	InUse    bool   `json:"in_use"`
	Process  string `json:"process,omitempty"`
}

type FirewallState struct {
	Backend        string   `json:"backend,omitempty"`
	Inspected      bool     `json:"inspected"`
	Active         bool     `json:"active"`
	AllowedPorts   []string `json:"allowed_ports,omitempty"`
	MissingPorts   []string `json:"missing_ports,omitempty"`
	DetectionError string   `json:"detection_error,omitempty"`
}

type ServiceState struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Detail string `json:"detail,omitempty"`
}

var requiredServicePorts = []PortBinding{
	{Port: 80, Protocol: "tcp"},
	{Port: 443, Protocol: "tcp"},
	{Port: 3478, Protocol: "udp"},
}

var nonPublicRoutableIPPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func CheckServerDNS(probe DNSProbe) CheckResult {
	host := strings.TrimSpace(probe.Host)
	resolved := compactStrings(probe.ResolvedIPs)
	if host == "" {
		return newCheckResult(
			"dns",
			"DNS resolution",
			StatusFail,
			SeverityError,
			"The Headscale server host is missing, so DNS cannot be checked.",
			nil,
			[]string{"Set default.server_url to a public HTTPS host name and retry preflight."},
		)
	}

	findings := []string{}
	if len(resolved) > 0 {
		findings = append(findings, fmt.Sprintf("Resolved %s to %s.", host, strings.Join(resolved, ", ")))
	}
	if errText := strings.TrimSpace(probe.LookupError); errText != "" {
		findings = append(findings, fmt.Sprintf("Resolver error: %s.", errText))
		return newCheckResult(
			"dns",
			"DNS resolution",
			StatusFail,
			SeverityError,
			fmt.Sprintf("DNS lookup failed for %s.", host),
			findings,
			[]string{fmt.Sprintf("Create or fix the public DNS record for %s before deploy.", host)},
		)
	}
	if len(resolved) == 0 {
		return newCheckResult(
			"dns",
			"DNS resolution",
			StatusFail,
			SeverityError,
			fmt.Sprintf("%s does not resolve to any public address yet.", host),
			findings,
			[]string{fmt.Sprintf("Create an A or AAAA record for %s before deploy.", host)},
		)
	}
	if len(publicRoutableIPs(resolved)) == 0 {
		return newCheckResult(
			"dns",
			"DNS resolution",
			StatusFail,
			SeverityError,
			fmt.Sprintf("%s does not resolve to a public-routable address yet.", host),
			findings,
			[]string{fmt.Sprintf("Update DNS for %s to include a public-routable A or AAAA record before deploy.", host)},
		)
	}

	missing := []string{}
	if expectedIPv4 := strings.TrimSpace(probe.ExpectedIPv4); expectedIPv4 != "" && !containsString(resolved, expectedIPv4) {
		missing = append(missing, fmt.Sprintf("Expected public IPv4 %s is missing from DNS results.", expectedIPv4))
	}
	if expectedIPv6 := strings.TrimSpace(probe.ExpectedIPv6); expectedIPv6 != "" && !containsString(resolved, expectedIPv6) {
		missing = append(missing, fmt.Sprintf("Expected public IPv6 %s is missing from DNS results.", expectedIPv6))
	}
	if len(missing) > 0 {
		findings = append(findings, missing...)
		return newCheckResult(
			"dns",
			"DNS resolution",
			StatusFail,
			SeverityError,
			fmt.Sprintf("DNS records for %s do not match the expected public addresses.", host),
			findings,
			[]string{fmt.Sprintf("Update the public DNS records for %s so they match the server's reachable IP addresses.", host)},
		)
	}

	return newCheckResult(
		"dns",
		"DNS resolution",
		StatusPass,
		SeverityInfo,
		fmt.Sprintf("DNS for %s resolves to the expected public addresses.", host),
		findings,
		nil,
	)
}

func CheckPortAvailability(bindings []PortBinding) CheckResult {
	if len(bindings) == 0 {
		return newCheckResult(
			"ports",
			"Port occupancy",
			StatusManual,
			SeverityManual,
			"Port occupancy still needs host-side confirmation.",
			[]string{"Required ports: 80/tcp, 443/tcp, and 3478/udp."},
			[]string{"Confirm that 80/tcp, 443/tcp, and 3478/udp are free before running deploy."},
		)
	}

	sorted := append([]PortBinding(nil), bindings...)
	sort.Slice(sorted, func(i int, j int) bool {
		if sorted[i].Port == sorted[j].Port {
			return strings.ToLower(sorted[i].Protocol) < strings.ToLower(sorted[j].Protocol)
		}
		return sorted[i].Port < sorted[j].Port
	})

	provided := make(map[string]PortBinding, len(sorted))
	for _, binding := range sorted {
		provided[portLabel(binding)] = binding
	}

	missing := []string{}
	blocking := []string{}
	review := []string{}
	available := []string{}
	for _, required := range requiredServicePorts {
		binding, ok := provided[portLabel(required)]
		if !ok {
			missing = append(missing, fmt.Sprintf("Missing occupancy probe for %s.", portLabel(required)))
			continue
		}

		label := portLabel(binding)
		if binding.InUse {
			process := strings.TrimSpace(binding.Process)
			if process == "" {
				process = "another process"
			}
			if isReviewableHTTPPortBinding(binding, process) {
				review = append(review, fmt.Sprintf("%s is already in use by %s and needs a coexistence review.", label, process))
				continue
			}
			blocking = append(blocking, fmt.Sprintf("%s is already in use by %s.", label, process))
			continue
		}
		available = append(available, fmt.Sprintf("%s is available.", label))
	}

	if len(blocking) > 0 {
		findings := append([]string{}, blocking...)
		findings = append(findings, review...)
		findings = append(findings, missing...)
		return newCheckResult(
			"ports",
			"Port occupancy",
			StatusFail,
			SeverityError,
			"Required service ports have incompatible listeners.",
			findings,
			[]string{"Stop or move the incompatible listeners before deploy, and only keep 80/tcp or 443/tcp occupied when an explicit web-service coexistence review is complete."},
		)
	}

	if len(missing) > 0 {
		findings := append([]string{}, review...)
		findings = append(findings, available...)
		findings = append(findings, missing...)
		return newCheckResult(
			"ports",
			"Port occupancy",
			StatusManual,
			SeverityManual,
			"Port occupancy still needs complete host-side confirmation.",
			findings,
			[]string{"Collect occupancy results for 80/tcp, 443/tcp, and 3478/udp before deploy."},
		)
	}

	if len(review) > 0 {
		findings := append([]string{}, review...)
		findings = append(findings, available...)
		return newCheckResult(
			"ports",
			"Port occupancy",
			StatusWarn,
			SeverityWarning,
			"Existing web listeners need a coexistence review before deploy.",
			findings,
			[]string{"Review server_name ownership, ACME challenge routing, and any existing 80/443 virtual hosts before deploy."},
		)
	}

	return newCheckResult(
		"ports",
		"Port occupancy",
		StatusPass,
		SeverityInfo,
		"Required service ports are available.",
		available,
		nil,
	)
}

func CheckFirewall(state FirewallState) CheckResult {
	backend := strings.TrimSpace(state.Backend)
	if !state.Inspected {
		findings := []string{}
		if backend != "" {
			findings = append(findings, fmt.Sprintf("Detected firewall backend: %s.", backend))
		}
		if errText := strings.TrimSpace(state.DetectionError); errText != "" {
			findings = append(findings, fmt.Sprintf("Detection detail: %s.", errText))
		}
		return newCheckResult(
			"firewall",
			"Host firewall",
			StatusManual,
			SeverityManual,
			"Local firewall rules still need confirmation.",
			findings,
			[]string{"Confirm that the host firewall allows 80/tcp, 443/tcp, and 3478/udp before deploy."},
		)
	}

	if len(state.MissingPorts) > 0 {
		findings := make([]string, 0, len(state.MissingPorts)+1)
		if backend != "" {
			findings = append(findings, fmt.Sprintf("Firewall backend: %s.", backend))
		}
		for _, port := range compactStrings(state.MissingPorts) {
			findings = append(findings, fmt.Sprintf("Missing allow rule for %s.", port))
		}
		return newCheckResult(
			"firewall",
			"Host firewall",
			StatusFail,
			SeverityError,
			"Host firewall rules are blocking required service ports.",
			findings,
			[]string{"Allow 80/tcp, 443/tcp, and 3478/udp through the host firewall before deploy."},
		)
	}

	findings := []string{}
	if backend != "" {
		findings = append(findings, fmt.Sprintf("Firewall backend: %s.", backend))
	}
	if !state.Active {
		findings = append(findings, "No active host firewall rules were reported.")
	}
	for _, port := range compactStrings(state.AllowedPorts) {
		findings = append(findings, fmt.Sprintf("Allow rule present for %s.", port))
	}

	return newCheckResult(
		"firewall",
		"Host firewall",
		StatusPass,
		SeverityInfo,
		"Host firewall rules will not block required service ports.",
		findings,
		nil,
	)
}

func CheckServiceConflicts(services []ServiceState) CheckResult {
	if services == nil {
		return newCheckResult(
			"services",
			"Local service conflicts",
			StatusManual,
			SeverityManual,
			"Local service conflict checks still need host-side confirmation.",
			nil,
			[]string{"Inspect local web services and any existing Headscale units before deploy."},
		)
	}

	activeServices := []ServiceState{}
	for _, service := range services {
		if service.Active {
			activeServices = append(activeServices, service)
		}
	}
	if len(activeServices) == 0 {
		return newCheckResult("services", "Local service conflicts", StatusPass, SeverityInfo, "No known conflicting local services were reported.", nil, nil)
	}

	blockingHeadscale := []string{}
	blockingWeb := []string{}
	review := []string{}
	findings := []string{}
	for _, service := range activeServices {
		name := strings.ToLower(strings.TrimSpace(service.Name))
		label := strings.TrimSpace(service.Name)
		if label == "" {
			label = "unknown service"
		}
		line := label
		if detail := strings.TrimSpace(service.Detail); detail != "" {
			line = fmt.Sprintf("%s (%s)", label, detail)
		}
		findings = append(findings, fmt.Sprintf("Active service detected: %s.", line))
		if name == "headscale" {
			blockingHeadscale = append(blockingHeadscale, label)
			continue
		}
		switch name {
		case "nginx":
			review = append(review, label)
		case "apache2", "caddy", "traefik":
			blockingWeb = append(blockingWeb, label)
		}
	}

	if len(blockingHeadscale) > 0 || len(blockingWeb) > 0 {
		summary := "Incompatible local services would conflict with a fresh meshify deployment."
		remediations := []string{"Stop or migrate the incompatible local services before deploy."}
		switch {
		case len(blockingHeadscale) > 0 && len(blockingWeb) == 0:
			summary = "Existing Headscale services would conflict with a fresh meshify deployment."
			remediations = []string{"Stop or migrate the existing Headscale service before deploy."}
		case len(blockingHeadscale) == 0 && len(blockingWeb) > 0:
			summary = "Existing non-Nginx web services would conflict with meshify-managed Nginx and ACME."
			remediations = []string{"Stop or move Apache, Caddy, or Traefik listeners from 80/443 before deploy, or migrate the site behind meshify-managed Nginx."}
		}
		return newCheckResult(
			"services",
			"Local service conflicts",
			StatusFail,
			SeverityError,
			summary,
			findings,
			remediations,
		)
	}

	if len(review) > 0 {
		return newCheckResult(
			"services",
			"Local service conflicts",
			StatusWarn,
			SeverityWarning,
			"Existing local web services need a coexistence review before deploy.",
			findings,
			[]string{"Review server_name ownership, ACME challenge routing, and any existing 80/443 virtual hosts before deploy."},
		)
	}

	return newCheckResult("services", "Local service conflicts", StatusPass, SeverityInfo, "No blocking local service conflicts were reported.", findings, nil)
}

func BuildManualChecklists(cfg config.Config) []ManualChecklist {
	host := parseServerHost(cfg.Default.ServerURL)
	if host == "" {
		host = "the Headscale server host"
	}

	items := []string{
		"Confirm the cloud security group allows 80/tcp, 443/tcp, and 3478/udp to this host.",
		"Confirm the cloud provider or upstream network allows public ingress on 80/tcp, 443/tcp, and 3478/udp.",
		fmt.Sprintf("Confirm %s is publicly reachable from the client networks that will join the tailnet.", host),
		"For China mainland deployments, confirm ICP filing and any cloud-provider internet access prerequisites before public launch.",
	}

	switch cfg.Default.ACMEChallenge {
	case config.ACMEChallengeDNS01:
		items = append(items, "Confirm DNS-01 provider credentials are present in the host environment before deploy.")
	default:
		items = append(items, "Confirm HTTP-01 challenge traffic can reach Nginx on 80/tcp without a CDN or upstream proxy blocking /.well-known/acme-challenge/.")
	}

	return []ManualChecklist{{
		Title: "Cloud and compliance review",
		Items: items,
	}}
}

func containsString(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func portLabel(binding PortBinding) string {
	protocol := strings.ToLower(strings.TrimSpace(binding.Protocol))
	if protocol == "" {
		protocol = "tcp"
	}
	return fmt.Sprintf("%d/%s", binding.Port, protocol)
}

func isReviewableHTTPPortBinding(binding PortBinding, process string) bool {
	if binding.Port != 80 && binding.Port != 443 {
		return false
	}
	if strings.ToLower(strings.TrimSpace(binding.Protocol)) != "tcp" {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(process)) {
	case "nginx":
		return true
	default:
		return false
	}
}

func publicRoutableIPs(values []string) []string {
	public := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if isPublicRoutableIP(value) {
			public = append(public, value)
		}
	}
	return public
}

func isPublicRoutableIP(value string) bool {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicRoutableIPPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
