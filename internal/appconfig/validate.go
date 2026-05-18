package appconfig

import (
	"fmt"
	"meshify/internal/acme"
	"net"
	"net/mail"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type validationErrors []string

var nginxExpiresValuePattern = regexp.MustCompile(`^(?:off|epoch|max|[+-]?[0-9]+(?:ms|s|m|h|d|w|M|y)?)$`)
var nginxSizeValuePattern = regexp.MustCompile(`^[0-9]+(?:[kKmMgG])?$`)
var nginxTimeValuePattern = regexp.MustCompile(`^[0-9]+(?:ms|s|m|h|d|w|M|y)?$`)
var systemdSafeEmailPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+@[A-Za-z0-9.-]+$`)

func (errs validationErrors) Error() string {
	return strings.Join(errs, "; ")
}

func (c *Config) normalize() {
	c.APIVersion = strings.TrimSpace(c.APIVersion)
	c.App.Name = strings.TrimSpace(c.App.Name)
	c.App.CertificateEmail = strings.TrimSpace(c.App.CertificateEmail)
	c.App.ACMEChallenge = strings.TrimSpace(c.App.ACMEChallenge)
	c.App.Listen = strings.TrimSpace(c.App.Listen)
	c.App.Upstream = strings.TrimSpace(c.App.Upstream)
	for i, domain := range c.App.Domains {
		c.App.Domains[i] = normalizeDomain(domain)
	}
	c.Service.ExecStart = strings.TrimSpace(c.Service.ExecStart)
	c.Service.WorkingDirectory = strings.TrimSpace(c.Service.WorkingDirectory)
	c.Service.EnvFile = strings.TrimSpace(c.Service.EnvFile)
	c.Nginx.ClientMaxBodySize = strings.TrimSpace(c.Nginx.ClientMaxBodySize)
	c.Nginx.AccessLog = strings.TrimSpace(c.Nginx.AccessLog)
	c.Nginx.ErrorLog = strings.TrimSpace(c.Nginx.ErrorLog)
	c.Nginx.Proxy.ConnectTimeout = strings.TrimSpace(c.Nginx.Proxy.ConnectTimeout)
	c.Nginx.Proxy.ReadTimeout = strings.TrimSpace(c.Nginx.Proxy.ReadTimeout)
	c.Nginx.Proxy.SendTimeout = strings.TrimSpace(c.Nginx.Proxy.SendTimeout)
	for i := range c.Nginx.StaticLocations {
		c.Nginx.StaticLocations[i].Path = strings.TrimSpace(c.Nginx.StaticLocations[i].Path)
		c.Nginx.StaticLocations[i].Match = strings.TrimSpace(c.Nginx.StaticLocations[i].Match)
		c.Nginx.StaticLocations[i].Alias = strings.TrimSpace(c.Nginx.StaticLocations[i].Alias)
		c.Nginx.StaticLocations[i].Expires = strings.TrimSpace(c.Nginx.StaticLocations[i].Expires)
		c.Nginx.StaticLocations[i].CacheControl = strings.TrimSpace(c.Nginx.StaticLocations[i].CacheControl)
	}
	c.DNS01.Provider = strings.TrimSpace(c.DNS01.Provider)
	c.DNS01.EnvFile = strings.TrimSpace(c.DNS01.EnvFile)
	c.Tailscale.MeshifyConfig = strings.TrimSpace(c.Tailscale.MeshifyConfig)
	c.Tailscale.LoginServer = normalizeLoginServer(c.Tailscale.LoginServer)
	c.Tailscale.Hostname = strings.TrimSpace(c.Tailscale.Hostname)
	c.Tailscale.AuthKeyFile = strings.TrimSpace(c.Tailscale.AuthKeyFile)
}

func (c Config) Validate() error {
	var errs validationErrors

	if c.APIVersion == "" {
		errs = append(errs, "api_version is required")
	} else if c.APIVersion != APIVersion {
		errs = append(errs, fmt.Sprintf("api_version must be %q", APIVersion))
	}

	validateAppName(&errs, c.App.Name)
	validateDomains(&errs, c.App.Domains)
	validateEmail(&errs, "app.certificate_email", c.App.CertificateEmail)
	validateACMEChallenge(&errs, c.App.ACMEChallenge)
	validateMode(&errs, c)
	validateService(&errs, c)
	validateNginx(&errs, c.Nginx)
	validateDNS01(&errs, c.App.ACMEChallenge, c.DNS01)
	validateTailscale(&errs, c)

	if len(errs) == 0 {
		return nil
	}
	return errs
}

func validateAppName(errs *validationErrors, name string) {
	if name == "" {
		*errs = append(*errs, "app.name is required")
		return
	}
	if len(name) > 32 {
		*errs = append(*errs, "app.name must be 32 characters or shorter")
	}
	if !isSafeAppName(name) {
		*errs = append(*errs, "app.name must start with a lowercase letter, contain only lowercase letters, digits, and hyphens, and must not end with a hyphen")
	}
	if isReservedAppName(name) {
		*errs = append(*errs, "app.name is reserved by meshify, Headscale, Nginx, or Tailscale")
	}
}

func validateDomains(errs *validationErrors, domains []string) {
	if len(domains) == 0 {
		*errs = append(*errs, "app.domains must contain at least one domain")
		return
	}
	seen := make(map[string]struct{}, len(domains))
	for i, domain := range domains {
		field := fmt.Sprintf("app.domains[%d]", i)
		normalized := normalizeDomain(domain)
		if normalized == "" {
			*errs = append(*errs, field+" is required")
			continue
		}
		if !looksLikeDNSName(normalized) {
			*errs = append(*errs, field+" must be an exact DNS name")
			continue
		}
		if strings.Contains(normalized, "*") {
			*errs = append(*errs, field+" must not be a wildcard domain")
			continue
		}
		if _, ok := seen[normalized]; ok {
			*errs = append(*errs, "app.domains must not contain duplicate domains")
			continue
		}
		seen[normalized] = struct{}{}
	}
}

func validateEmail(errs *validationErrors, field string, email string) {
	if email == "" {
		*errs = append(*errs, field+" is required")
		return
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		*errs = append(*errs, field+" must be a plain email address")
	}
	if strings.Contains(email, "%") {
		*errs = append(*errs, field+" must not contain % because systemd unit files treat it as a specifier")
	}
	if !systemdSafeEmailPattern.MatchString(email) || containsControlOrSpace(email) || strings.ContainsAny(email, "\"'\\;{}$") {
		*errs = append(*errs, field+" must be a systemd-safe email token using only letters, digits, dot, underscore, plus, and hyphen")
	}
}

func validateACMEChallenge(errs *validationErrors, challenge string) {
	switch challenge {
	case ACMEChallengeHTTP01, ACMEChallengeDNS01:
	case "":
		*errs = append(*errs, "app.acme_challenge is required")
	default:
		*errs = append(*errs, "app.acme_challenge must be one of: http-01, dns-01")
	}
}

func validateMode(errs *validationErrors, c Config) {
	hasListen := c.App.Listen != ""
	hasUpstream := c.App.Upstream != ""
	switch {
	case hasListen && hasUpstream:
		*errs = append(*errs, "app.listen and app.upstream must not both be set")
	case !hasListen && !hasUpstream:
		*errs = append(*errs, "one of app.listen or app.upstream is required")
	case hasListen:
		validateListenAddress(errs, "app.listen", c.App.Listen)
	case hasUpstream:
		validateUpstreamAddress(errs, "app.upstream", c.App.Upstream)
	}
}

func validateService(errs *validationErrors, c Config) {
	switch c.Mode() {
	case ModeListen:
		validateExecStart(errs, c.Service.ExecStart)
		validateWorkingDirectory(errs, c.Service.WorkingDirectory)
		validateServiceEnvFile(errs, c.Service.EnvFile)
	case ModeUpstream:
		if c.Service.ExecStart != "" {
			*errs = append(*errs, "service.exec_start must be empty when app.upstream is set")
		}
		if c.Service.WorkingDirectory != "" {
			*errs = append(*errs, "service.working_directory must be empty when app.upstream is set")
		}
		if c.Service.EnvFile != "" {
			*errs = append(*errs, "service.env_file must be empty when app.upstream is set")
		}
	}
}

func validateExecStart(errs *validationErrors, execStart string) {
	if execStart == "" {
		*errs = append(*errs, "service.exec_start is required when app.listen is set")
		return
	}
	if containsControl(execStart) {
		*errs = append(*errs, "service.exec_start must not contain control characters")
	}
	if strings.Contains(execStart, "%") {
		*errs = append(*errs, "service.exec_start must not contain % because systemd treats it as a specifier")
	}
	if strings.ContainsAny(execStart, ";\"'\\${}") {
		*errs = append(*errs, "service.exec_start must not contain systemd separators, variables, quotes, or escapes")
	}
	fields := strings.Fields(execStart)
	if len(fields) == 0 {
		*errs = append(*errs, "service.exec_start is required when app.listen is set")
		return
	}
	binary := fields[0]
	if !filepath.IsAbs(binary) {
		*errs = append(*errs, "service.exec_start must start with an absolute business binary path")
		return
	}
	if filepath.Clean(binary) != binary {
		*errs = append(*errs, "service.exec_start binary path must be clean and absolute")
	}
	if strings.ContainsAny(binary, "\"'\\") {
		*errs = append(*errs, "service.exec_start binary path must not contain quotes or escapes")
	}
}

func validateWorkingDirectory(errs *validationErrors, dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		*errs = append(*errs, "service.working_directory must be an absolute directory path when set")
		return
	}
	if filepath.Clean(dir) != dir {
		*errs = append(*errs, "service.working_directory must be clean and absolute")
	}
	if containsControl(dir) || strings.Contains(dir, "%") || strings.ContainsAny(dir, "\"'\\") {
		*errs = append(*errs, "service.working_directory must not contain control characters, systemd specifiers, quotes, or escapes")
	}
}

func validateServiceEnvFile(errs *validationErrors, path string) {
	if path == "" {
		return
	}
	validatePathField(errs, "service.env_file", path, true)
}

func validateNginx(errs *validationErrors, cfg NginxConfig) {
	if cfg.ClientMaxBodySize != "" && !nginxSizeValuePattern.MatchString(cfg.ClientMaxBodySize) {
		*errs = append(*errs, "nginx.client_max_body_size must be a simple nginx size such as 20m")
	}
	validateNginxOptionalLogPath(errs, "nginx.access_log", cfg.AccessLog, true)
	validateNginxOptionalLogPath(errs, "nginx.error_log", cfg.ErrorLog, false)
	validateNginxProxy(errs, cfg.Proxy)

	seenPaths := map[string]struct{}{}
	for index, location := range cfg.StaticLocations {
		field := fmt.Sprintf("nginx.static_locations[%d]", index)
		match := location.Match
		if match == "" {
			match = "prefix"
		}
		validateNginxLocationPath(errs, field+".path", location.Path)
		validateNginxAliasPath(errs, field+".alias", location.Alias)
		switch match {
		case "prefix":
			if location.Path != "" && !strings.HasSuffix(location.Path, "/") {
				*errs = append(*errs, field+".path must end with / for prefix static locations")
			}
			if location.Alias != "" && !strings.HasSuffix(location.Alias, "/") {
				*errs = append(*errs, field+".alias must end with / for prefix static locations")
			}
		case "exact":
			if strings.HasSuffix(location.Path, "/") {
				*errs = append(*errs, field+".path must not end with / for exact static locations")
			}
			if strings.HasSuffix(location.Alias, "/") {
				*errs = append(*errs, field+".alias must not end with / for exact static locations")
			}
		default:
			*errs = append(*errs, field+".match must be empty, prefix, or exact")
		}
		if location.Expires != "" && !nginxExpiresValuePattern.MatchString(location.Expires) {
			*errs = append(*errs, field+".expires must be off, epoch, max, or a simple nginx time such as 30d")
		}
		if location.CacheControl != "" {
			validateNginxQuotedValue(errs, field+".cache_control", location.CacheControl)
		}
		if location.AccessLog != nil && *location.AccessLog {
			*errs = append(*errs, field+".access_log only supports false when set")
		}
		if location.Path == "" {
			continue
		}
		if _, exists := seenPaths[location.Path]; exists {
			*errs = append(*errs, field+".path must not duplicate another static location")
		}
		seenPaths[location.Path] = struct{}{}
	}
}

func validateNginxProxy(errs *validationErrors, proxy NginxProxyConfig) {
	if proxy.ConnectTimeout != "" && !nginxTimeValuePattern.MatchString(proxy.ConnectTimeout) {
		*errs = append(*errs, "nginx.proxy.connect_timeout must be a simple nginx time such as 30s")
	}
	if proxy.ReadTimeout != "" && !nginxTimeValuePattern.MatchString(proxy.ReadTimeout) {
		*errs = append(*errs, "nginx.proxy.read_timeout must be a simple nginx time such as 600s")
	}
	if proxy.SendTimeout != "" && !nginxTimeValuePattern.MatchString(proxy.SendTimeout) {
		*errs = append(*errs, "nginx.proxy.send_timeout must be a simple nginx time such as 600s")
	}
}

func validateNginxOptionalLogPath(errs *validationErrors, field string, path string, allowOff bool) {
	if path == "" {
		return
	}
	if allowOff && path == "off" {
		return
	}
	if !filepath.IsAbs(path) {
		*errs = append(*errs, field+" must be an absolute path when set")
		return
	}
	if filepath.Clean(path) != path {
		*errs = append(*errs, field+" must be a clean absolute path without . or .. segments")
	}
	if containsControlOrSpace(path) || strings.ContainsAny(path, "#*?[]%\"'\\;{}$") {
		*errs = append(*errs, field+" must not contain whitespace, comment, glob, specifier, quote, escape, variable, semicolon, brace, or control characters")
	}
}

func validateNginxLocationPath(errs *validationErrors, field string, path string) {
	if path == "" {
		*errs = append(*errs, field+" is required")
		return
	}
	if !strings.HasPrefix(path, "/") {
		*errs = append(*errs, field+" must start with /")
		return
	}
	if path == "/" {
		*errs = append(*errs, field+" must not be / because location / is reserved for the app proxy")
	}
	if nginxPathOverlapsACMEChallenge(path) {
		*errs = append(*errs, field+" must not overlap /.well-known/acme-challenge/")
	}
	if containsDotPathSegment(path) {
		*errs = append(*errs, field+" must not contain . or .. path segments")
	}
	if containsControlOrSpace(path) || strings.ContainsAny(path, "#\"'\\;{}$") {
		*errs = append(*errs, field+" must not contain whitespace, comments, quotes, escapes, variables, semicolons, braces, or control characters")
	}
}

func validateNginxAliasPath(errs *validationErrors, field string, path string) {
	if path == "" {
		*errs = append(*errs, field+" is required")
		return
	}
	if !filepath.IsAbs(path) {
		*errs = append(*errs, field+" must be an absolute path")
		return
	}
	cleanTarget := strings.TrimRight(path, "/")
	if cleanTarget == "" {
		*errs = append(*errs, field+" must be an absolute path")
		return
	}
	if filepath.Clean(cleanTarget) != cleanTarget {
		*errs = append(*errs, field+" must be a clean absolute path without . or .. segments")
	}
	if containsControlOrSpace(path) || strings.ContainsAny(path, "#*?[]%\"'\\;{}$") {
		*errs = append(*errs, field+" must not contain whitespace, comment, glob, specifier, quote, escape, variable, semicolon, brace, or control characters")
	}
}

func validateNginxQuotedValue(errs *validationErrors, field string, value string) {
	if containsControl(value) || strings.ContainsAny(value, "\"'\\;{}$") {
		*errs = append(*errs, field+" must not contain quotes, escapes, variables, semicolons, braces, or control characters")
	}
}

func nginxPathOverlapsACMEChallenge(path string) bool {
	const challengePath = "/.well-known/acme-challenge/"
	return strings.HasPrefix(path, challengePath) || strings.HasPrefix(challengePath, path)
}

func containsDotPathSegment(path string) bool {
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func containsControlOrSpace(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func validateDNS01(errs *validationErrors, challenge string, dns01 DNS01Config) {
	provider := strings.TrimSpace(dns01.Provider)
	envFile := strings.TrimSpace(dns01.EnvFile)
	var providerInfo acme.DNSProviderInfo
	providerValid := false

	if challenge == ACMEChallengeDNS01 && provider == "" {
		*errs = append(*errs, "dns01.provider is required when app.acme_challenge is dns-01")
	}
	if provider != "" {
		info, err := acme.DNSProvider(provider)
		if err != nil {
			*errs = append(*errs, err.Error())
		} else {
			providerInfo = info
			providerValid = true
		}
	}
	if envFile != "" && provider == "" {
		*errs = append(*errs, "dns01.provider is required when dns01.env_file is set")
	}
	if envFile != "" {
		validatePathField(errs, "dns01.env_file", envFile, true)
	}
	if challenge == ACMEChallengeDNS01 && providerValid && envFile == "" && providerInfo.EnvFileRequired {
		*errs = append(*errs, "dns01.env_file is required for DNS-01 renewal with lego DNS provider "+providerInfo.LegoCode)
	}
}

func validateTailscale(errs *validationErrors, c Config) {
	if c.Tailscale.LoginServer != "" {
		validateLoginServer(errs, c.Tailscale.LoginServer)
		validateLoginServerDoesNotReuseAppDomain(errs, c.Tailscale.LoginServer, c.App.Domains)
		if c.Tailscale.MeshifyConfig != "" {
			*errs = append(*errs, "tailscale.meshify_config must be empty when tailscale.login_server is set")
		}
	}
	if c.Tailscale.MeshifyConfig != "" {
		validateConfigPathField(errs, "tailscale.meshify_config", c.Tailscale.MeshifyConfig)
	}
	if c.Tailscale.Hostname != "" && !isSafeHostname(c.Tailscale.Hostname) {
		*errs = append(*errs, "tailscale.hostname must contain only lowercase letters, digits, and hyphens, and must not start or end with a hyphen")
	}
	if c.Tailscale.AuthKeyFile != "" {
		validatePathField(errs, "tailscale.auth_key_file", c.Tailscale.AuthKeyFile, true)
	}
}

func validateLoginServer(errs *validationErrors, raw string) {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		*errs = append(*errs, fmt.Sprintf("tailscale.login_server must be a valid URL: %v", err))
		return
	}
	if parsedURL.Scheme != "https" {
		*errs = append(*errs, "tailscale.login_server must use https")
	}
	if parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		*errs = append(*errs, "tailscale.login_server must be an origin URL without userinfo, query, or fragment")
	}
	if parsedURL.Path != "" && parsedURL.Path != "/" {
		*errs = append(*errs, "tailscale.login_server must not include a path")
	}
	if parsedURL.Hostname() == "" {
		*errs = append(*errs, "tailscale.login_server host is required")
	} else if !looksLikeDNSName(normalizeDomain(parsedURL.Hostname())) {
		*errs = append(*errs, "tailscale.login_server host must be a DNS name")
	}
	if port := parsedURL.Port(); port != "" && port != "443" {
		*errs = append(*errs, "tailscale.login_server must not specify a port other than 443")
	}
}

func validateLoginServerDoesNotReuseAppDomain(errs *validationErrors, raw string, domains []string) {
	parsedURL, err := url.Parse(raw)
	if err != nil {
		return
	}
	loginHost := normalizeDomain(parsedURL.Hostname())
	if loginHost == "" {
		return
	}
	for _, domain := range domains {
		if normalizeDomain(domain) == loginHost {
			*errs = append(*errs, "tailscale.login_server host must not reuse an app domain")
			return
		}
	}
}

func validatePathField(errs *validationErrors, field string, path string, requireAbs bool) {
	if requireAbs && !filepath.IsAbs(path) {
		*errs = append(*errs, field+" must be an absolute path when set")
		return
	}
	if filepath.Clean(path) != path {
		*errs = append(*errs, field+" must be a clean path without . or .. segments")
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			*errs = append(*errs, field+" must not contain whitespace or control characters")
			break
		}
	}
	if strings.ContainsAny(path, "*?[]%\"'\\") {
		*errs = append(*errs, field+" must not contain glob, specifier, quote, or escape characters")
	}
}

func validateConfigPathField(errs *validationErrors, field string, path string) {
	if filepath.Clean(path) == "." {
		*errs = append(*errs, field+" must be a valid file path")
	}
	if containsControl(path) {
		*errs = append(*errs, field+" must not contain control characters")
	}
}

func validateListenAddress(errs *validationErrors, field string, address string) {
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		*errs = append(*errs, field+" must be in host:port format")
		return
	}
	if !isLoopbackHost(host) {
		*errs = append(*errs, field+" must use a loopback host such as 127.0.0.1 or ::1")
	}
	port, ok := validatePort(errs, field, portString)
	if !ok {
		return
	}
	if isReservedMeshifyPort(port) {
		*errs = append(*errs, field+" must not reuse Meshify, Headscale, Nginx, or Tailscale reserved ports")
	}
}

func validateUpstreamAddress(errs *validationErrors, field string, address string) {
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		*errs = append(*errs, field+" must be in host:port format")
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() == nil || !isTailscaleIPv4(ip) {
		*errs = append(*errs, field+" must use a fixed Tailscale IPv4 address in 100.64.0.0/10")
	}
	port, ok := validatePort(errs, field, portString)
	if !ok {
		return
	}
	if isCommonDatabasePort(port) {
		*errs = append(*errs, field+" must not expose common database ports; app upstreams must be HTTP or WebSocket services")
	}
}

func validatePort(errs *validationErrors, field string, portString string) (int, bool) {
	port, err := strconv.Atoi(portString)
	if err != nil || port < 1 || port > 65535 {
		*errs = append(*errs, field+" port must be between 1 and 65535")
		return 0, false
	}
	return port, true
}

func normalizeDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.TrimSuffix(value, ".")
}

func normalizeLoginServer(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsedURL, err := url.Parse(value)
	if err != nil {
		return value
	}
	if parsedURL.Scheme == "" || parsedURL.Hostname() == "" || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return value
	}
	if parsedURL.Path != "" && parsedURL.Path != "/" {
		return value
	}
	scheme := strings.ToLower(parsedURL.Scheme)
	host := normalizeDomain(parsedURL.Hostname())
	port := parsedURL.Port()
	if scheme != "https" || host == "" || (port != "" && port != "443") {
		return value
	}
	return "https://" + host
}

func looksLikeDNSName(value string) bool {
	if value == "" || len(value) > 253 || strings.Contains(value, "*") {
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
		if !isSafeDNSLabel(label) {
			return false
		}
	}
	return true
}

func isSafeDNSLabel(label string) bool {
	if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		return false
	}
	for _, r := range label {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func isSafeName(value string) bool {
	return isSafeDNSLabel(value) && !strings.Contains(value, ".")
}

func isSafeAppName(value string) bool {
	return isSafeName(value) && value[0] >= 'a' && value[0] <= 'z'
}

func isSafeHostname(value string) bool {
	return isSafeName(value)
}

func isReservedAppName(name string) bool {
	switch name {
	case "adm",
		"audio",
		"backup",
		"bin",
		"bluetooth",
		"cdrom",
		"crontab",
		"daemon",
		"default",
		"dialout",
		"dip",
		"disk",
		"fax",
		"floppy",
		"games",
		"gpio",
		"gnats",
		"headscale",
		"i2c",
		"input",
		"irc",
		"kvm",
		"landscape",
		"list",
		"lp",
		"lpadmin",
		"lxd",
		"mail",
		"man",
		"messagebus",
		"meshify",
		"news",
		"nginx",
		"nogroup",
		"nobody",
		"operator",
		"plugdev",
		"pollinate",
		"proxy",
		"rdma",
		"render",
		"root",
		"sambashare",
		"scanner",
		"shadow",
		"spi",
		"sshd",
		"ssl-cert",
		"staff",
		"sync",
		"sys",
		"syslog",
		"sudo",
		"systemd-network",
		"systemd-resolve",
		"systemd-journal",
		"systemd-timesync",
		"tape",
		"tailscale",
		"tailscaled",
		"tty",
		"uucp",
		"users",
		"utmp",
		"uuidd",
		"video",
		"voice",
		"www-data":
		return true
	default:
		return false
	}
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && !ip.IsUnspecified()
}

func isTailscaleIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	return ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}

func isReservedMeshifyPort(port int) bool {
	switch port {
	case 80, 443, 3478, 8080, 50443, 19090:
		return true
	default:
		return false
	}
}

func isCommonDatabasePort(port int) bool {
	switch port {
	case 3306, 5432, 5433, 6379, 6380, 9200, 9300, 27017:
		return true
	default:
		return false
	}
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
