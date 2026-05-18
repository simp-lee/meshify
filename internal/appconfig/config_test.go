package appconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validListenConfig() Config {
	cfg := New()
	cfg.App.Name = "example-app"
	cfg.App.Domains = []string{"abc.com", "www.abc.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/opt/example-app"
	return cfg
}

func validUpstreamConfig() Config {
	cfg := validListenConfig()
	cfg.App.Listen = ""
	cfg.App.Upstream = "100.64.10.20:18001"
	cfg.Service = ServiceConfig{}
	return cfg
}

func TestNewAppliesDefaultsAndRequiresUserInputs(t *testing.T) {
	t.Parallel()

	cfg := New()
	if cfg.APIVersion != APIVersion {
		t.Fatalf("APIVersion = %q, want %q", cfg.APIVersion, APIVersion)
	}
	if cfg.App.ACMEChallenge != ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", cfg.App.ACMEChallenge, ACMEChallengeHTTP01)
	}
	if got := cfg.Nginx.EffectiveClientMaxBodySize(); got != DefaultNginxClientMaxBodySize {
		t.Fatalf("nginx.client_max_body_size default = %q, want %q", got, DefaultNginxClientMaxBodySize)
	}
	if !cfg.NginxHTTP2Enabled() {
		t.Fatal("nginx.http2 default = false, want true")
	}
	if got := cfg.Nginx.Proxy.EffectiveReadTimeout(); got != DefaultNginxProxyReadTimeout {
		t.Fatalf("nginx.proxy.read_timeout default = %q, want %q", got, DefaultNginxProxyReadTimeout)
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	for _, want := range []string{
		"app.name is required",
		"app.domains must contain at least one domain",
		"app.certificate_email is required",
		"one of app.listen or app.upstream is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestLoadBytesValidListenConfig(t *testing.T) {
	t.Parallel()

	cfg, err := LoadBytes([]byte(`
api_version: meshify/app/v1alpha1
app:
  name: example-app
  domains:
    - ABC.COM.
    - www.abc.com
  certificate_email: ops@example.com
  listen: 127.0.0.1:18001
service:
  exec_start: /opt/example-app/example-app --listen 127.0.0.1:18001
  working_directory: /opt/example-app
  env_file: /opt/example-app/web.env
nginx:
  client_max_body_size: 100m
  http2: true
  access_log: /var/log/nginx/example-app.access.log
  error_log: /var/log/nginx/example-app.error.log
  proxy:
    connect_timeout: 30s
    read_timeout: 600s
    send_timeout: 600s
    buffering: false
    request_buffering: false
  static_locations:
    - path: /static/
      alias: /opt/example-app/web/static/
      expires: 30d
      cache_control: public, max-age=2592000
      try_files: true
      gzip_static: true
      access_log: false
    - path: /sitemap.xml
      match: exact
      alias: /opt/example-app/web/static/sitemap.xml
`))
	if err != nil {
		t.Fatalf("LoadBytes() error = %v", err)
	}

	if got := cfg.Mode(); got != ModeListen {
		t.Fatalf("Mode() = %q, want %q", got, ModeListen)
	}
	if cfg.RequiresTailscale() {
		t.Fatal("RequiresTailscale() = true, want false")
	}
	if got := cfg.PrimaryDomain(); got != "abc.com" {
		t.Fatalf("PrimaryDomain() = %q, want abc.com", got)
	}
	if got := cfg.ResourceName(); got != "example-app" {
		t.Fatalf("ResourceName() = %q, want example-app", got)
	}
	if got := cfg.EffectiveMeshifyConfig(); got != DefaultMeshifyConfigPath {
		t.Fatalf("EffectiveMeshifyConfig() = %q, want %q", got, DefaultMeshifyConfigPath)
	}
	if got := cfg.App.Domains[0]; got != "abc.com" {
		t.Fatalf("normalized domain = %q, want abc.com", got)
	}
	if got := cfg.Service.EnvFile; got != "/opt/example-app/web.env" {
		t.Fatalf("service.env_file = %q, want /opt/example-app/web.env", got)
	}
	if len(cfg.Nginx.StaticLocations) != 2 {
		t.Fatalf("len(nginx.static_locations) = %d, want 2", len(cfg.Nginx.StaticLocations))
	}
	if got := cfg.Nginx.StaticLocations[0].Path; got != "/static/" {
		t.Fatalf("nginx.static_locations[0].path = %q, want /static/", got)
	}
	if got := cfg.Nginx.ClientMaxBodySize; got != "100m" {
		t.Fatalf("nginx.client_max_body_size = %q, want 100m", got)
	}
	if !cfg.Nginx.HTTP2Enabled() {
		t.Fatal("nginx.http2 enabled = false, want true")
	}
	if got := cfg.Nginx.Proxy.ConnectTimeout; got != "30s" {
		t.Fatalf("nginx.proxy.connect_timeout = %q, want 30s", got)
	}
	if cfg.Nginx.Proxy.Buffering == nil || *cfg.Nginx.Proxy.Buffering {
		t.Fatalf("nginx.proxy.buffering = %#v, want false", cfg.Nginx.Proxy.Buffering)
	}
	if cfg.Nginx.Proxy.RequestBuffering == nil || *cfg.Nginx.Proxy.RequestBuffering {
		t.Fatalf("nginx.proxy.request_buffering = %#v, want false", cfg.Nginx.Proxy.RequestBuffering)
	}
	if got := cfg.Nginx.StaticLocations[0].CacheControl; got != "public, max-age=2592000" {
		t.Fatalf("nginx.static_locations[0].cache_control = %q, want cache-control value", got)
	}
	if !cfg.Nginx.StaticLocations[0].GzipStatic {
		t.Fatal("nginx.static_locations[0].gzip_static = false, want true")
	}
	if cfg.Nginx.StaticLocations[0].AccessLog == nil || *cfg.Nginx.StaticLocations[0].AccessLog {
		t.Fatalf("nginx.static_locations[0].access_log = %#v, want false", cfg.Nginx.StaticLocations[0].AccessLog)
	}
	if got := cfg.Nginx.StaticLocations[1].Match; got != "exact" {
		t.Fatalf("nginx.static_locations[1].match = %q, want exact", got)
	}
}

func TestLoadBytesValidUpstreamConfigRequiresTailscale(t *testing.T) {
	t.Parallel()

	cfg := validUpstreamConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := cfg.Mode(); got != ModeUpstream {
		t.Fatalf("Mode() = %q, want %q", got, ModeUpstream)
	}
	if !cfg.RequiresTailscale() {
		t.Fatal("RequiresTailscale() = false, want true")
	}
}

func TestListenModeCanOptIntoTailscale(t *testing.T) {
	t.Parallel()

	cfg := validListenConfig()
	cfg.Tailscale.EnabledForListen = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if !cfg.RequiresTailscale() {
		t.Fatal("RequiresTailscale() = false, want true")
	}
}

func TestLoadBytesRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	if _, err := LoadBytes([]byte(`
app:
  name: example-app
  domains: [abc.com]
  certificate_email: ops@example.com
  listen: 127.0.0.1:18001
service:
  exec_start: /opt/example-app/example-app
`)); err == nil || !strings.Contains(err.Error(), "api_version is required") {
		t.Fatalf("LoadBytes() missing api_version error = %v, want required version failure", err)
	}

	if _, err := LoadBytes([]byte(`
api_version: meshify/app/v1alpha1
app:
  name: example-app
  domains: [abc.com]
  certificate_email: ops@example.com
  listen: 127.0.0.1:18001
  tailnet: true
service:
  exec_start: /opt/example-app/example-app
`)); err == nil {
		t.Fatal("LoadBytes() unknown field error = nil, want non-nil")
	}

	if _, err := LoadBytes([]byte(`
api_version: meshify/app/v1alpha1
---
api_version: meshify/app/v1alpha1
`)); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("LoadBytes() multiple documents error = %v, want multiple document failure", err)
	}
}

func TestLoadBytesDefaultsACMEChallengeButNotAPIVersion(t *testing.T) {
	t.Parallel()

	cfg, err := LoadBytes([]byte(`
api_version: meshify/app/v1alpha1
app:
  name: example-app
  domains: [abc.com]
  certificate_email: ops@example.com
  listen: 127.0.0.1:18001
service:
  exec_start: /opt/example-app/example-app
`))
	if err != nil {
		t.Fatalf("LoadBytes() error = %v", err)
	}
	if cfg.App.ACMEChallenge != ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", cfg.App.ACMEChallenge, ACMEChallengeHTTP01)
	}
}

func TestValidateRejectsModeConflicts(t *testing.T) {
	t.Parallel()

	both := validListenConfig()
	both.App.Upstream = "100.64.10.20:18001"
	expectValidationError(t, both, "app.listen and app.upstream must not both be set")

	neither := validListenConfig()
	neither.App.Listen = ""
	expectValidationError(t, neither, "one of app.listen or app.upstream is required")
}

func TestValidateRejectsUnsafeAppNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{name: "Example", want: "app.name must start with a lowercase letter"},
		{name: "-example", want: "app.name must start with a lowercase letter"},
		{name: "example-", want: "app.name must start with a lowercase letter"},
		{name: "0", want: "app.name must start with a lowercase letter"},
		{name: "123", want: "app.name must start with a lowercase letter"},
		{name: "1-app", want: "app.name must start with a lowercase letter"},
		{name: "app-name-that-is-longer-than-32-chars", want: "app.name must be 32 characters or shorter"},
		{name: "meshify", want: "app.name is reserved"},
		{name: "bin", want: "app.name is reserved"},
		{name: "daemon", want: "app.name is reserved"},
		{name: "nogroup", want: "app.name is reserved"},
		{name: "operator", want: "app.name is reserved"},
		{name: "nobody", want: "app.name is reserved"},
		{name: "messagebus", want: "app.name is reserved"},
		{name: "sshd", want: "app.name is reserved"},
		{name: "staff", want: "app.name is reserved"},
		{name: "sudo", want: "app.name is reserved"},
		{name: "syslog", want: "app.name is reserved"},
		{name: "users", want: "app.name is reserved"},
		{name: "systemd-journal", want: "app.name is reserved"},
		{name: "systemd-network", want: "app.name is reserved"},
		{name: "systemd-resolve", want: "app.name is reserved"},
		{name: "systemd-timesync", want: "app.name is reserved"},
		{name: "www-data", want: "app.name is reserved"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validListenConfig()
			cfg.App.Name = tt.name
			expectValidationError(t, cfg, tt.want)
		})
	}

	cfg := validListenConfig()
	cfg.App.Name = "www"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with app.name www error = %v", err)
	}
}

func TestValidateRejectsInvalidDomains(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		domains []string
		want    string
	}{
		{name: "empty", domains: nil, want: "app.domains must contain at least one domain"},
		{name: "wildcard", domains: []string{"*.example.com"}, want: "app.domains[0] must be an exact DNS name"},
		{name: "ip", domains: []string{"100.64.10.20"}, want: "app.domains[0] must be an exact DNS name"},
		{name: "duplicate", domains: []string{"abc.com", "ABC.COM."}, want: "app.domains must not contain duplicate domains"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validListenConfig()
			cfg.App.Domains = tt.domains
			expectValidationError(t, cfg, tt.want)
		})
	}
}

func TestValidateRejectsSystemdUnsafeCertificateEmails(t *testing.T) {
	t.Parallel()

	tests := []string{
		"o'ps@example.com",
		"ops$token@example.com",
		"ops%team@example.com",
	}
	for _, email := range tests {
		email := email
		t.Run(email, func(t *testing.T) {
			t.Parallel()
			cfg := validListenConfig()
			cfg.App.CertificateEmail = email
			expectValidationError(t, cfg, "app.certificate_email must be a systemd-safe email token")
		})
	}
}

func TestValidateRejectsListenAddressesOutsideLoopbackOrReservedPorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		listen string
		want   string
	}{
		{name: "public", listen: "0.0.0.0:18001", want: "app.listen must use a loopback host"},
		{name: "public ipv4", listen: "203.0.113.10:18001", want: "app.listen must use a loopback host"},
		{name: "reserved", listen: "127.0.0.1:8080", want: "app.listen must not reuse"},
		{name: "bad port", listen: "127.0.0.1:bad", want: "app.listen port must be between 1 and 65535"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validListenConfig()
			cfg.App.Listen = tt.listen
			expectValidationError(t, cfg, tt.want)
		})
	}
}

func TestValidateRejectsInvalidUpstreamAddresses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		upstream string
		want     string
	}{
		{name: "magic dns", upstream: "node.tailnet.ts.net:18001", want: "app.upstream must use a fixed Tailscale IPv4 address"},
		{name: "outside tailnet", upstream: "192.168.1.10:18001", want: "app.upstream must use a fixed Tailscale IPv4 address"},
		{name: "database", upstream: "100.64.10.20:5432", want: "app.upstream must not expose common database ports"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := validUpstreamConfig()
			cfg.App.Upstream = tt.upstream
			expectValidationError(t, cfg, tt.want)
		})
	}
}

func TestValidateServiceRules(t *testing.T) {
	t.Parallel()

	missingExec := validListenConfig()
	missingExec.Service.ExecStart = ""
	expectValidationError(t, missingExec, "service.exec_start is required when app.listen is set")

	relativeExec := validListenConfig()
	relativeExec.Service.ExecStart = "example-app --listen 127.0.0.1:18001"
	expectValidationError(t, relativeExec, "service.exec_start must start with an absolute business binary path")

	commandSeparatorExec := validListenConfig()
	commandSeparatorExec.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001 ; /bin/true"
	expectValidationError(t, commandSeparatorExec, "service.exec_start must not contain systemd separators")

	variableExec := validListenConfig()
	variableExec.Service.ExecStart = "/opt/example-app/example-app --listen ${APP_LISTEN}"
	expectValidationError(t, variableExec, "service.exec_start must not contain systemd separators")

	quotedExec := validListenConfig()
	quotedExec.Service.ExecStart = `/opt/example-app/example-app --name "example app"`
	expectValidationError(t, quotedExec, "service.exec_start must not contain systemd separators")

	relativeDir := validListenConfig()
	relativeDir.Service.WorkingDirectory = "opt/example-app"
	expectValidationError(t, relativeDir, "service.working_directory must be an absolute directory path")

	relativeEnvFile := validListenConfig()
	relativeEnvFile.Service.EnvFile = "web.env"
	expectValidationError(t, relativeEnvFile, "service.env_file must be an absolute path")

	badEnvFile := validListenConfig()
	badEnvFile.Service.EnvFile = "/opt/example-app/web env"
	expectValidationError(t, badEnvFile, "service.env_file must not contain whitespace")

	upstreamWithService := validUpstreamConfig()
	upstreamWithService.Service.ExecStart = "/opt/example-app/example-app"
	expectValidationError(t, upstreamWithService, "service.exec_start must be empty when app.upstream is set")

	upstreamWithServiceEnv := validUpstreamConfig()
	upstreamWithServiceEnv.Service.EnvFile = "/opt/example-app/web.env"
	expectValidationError(t, upstreamWithServiceEnv, "service.env_file must be empty when app.upstream is set")
}

func TestValidateDNS01Rules(t *testing.T) {
	t.Parallel()

	missingProvider := validListenConfig()
	missingProvider.App.ACMEChallenge = ACMEChallengeDNS01
	expectValidationError(t, missingProvider, "dns01.provider is required when app.acme_challenge is dns-01")

	missingEnvFile := validListenConfig()
	missingEnvFile.App.ACMEChallenge = ACMEChallengeDNS01
	missingEnvFile.DNS01.Provider = "cloudflare"
	expectValidationError(t, missingEnvFile, "dns01.env_file is required for DNS-01 renewal with lego DNS provider cloudflare")

	valid := validListenConfig()
	valid.App.ACMEChallenge = ACMEChallengeDNS01
	valid.DNS01.Provider = "cloudflare"
	valid.DNS01.EnvFile = "/etc/meshify/dns/cloudflare.env"
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	badPath := valid
	badPath.DNS01.EnvFile = "relative.env"
	expectValidationError(t, badPath, "dns01.env_file must be an absolute path")
}

func TestValidateNginxStaticLocationRules(t *testing.T) {
	t.Parallel()

	valid := validListenConfig()
	staticAccessLog := false
	proxyBuffering := false
	valid.Nginx.StaticLocations = []NginxStaticLocationConfig{
		{
			Path:         "/static/",
			Alias:        "/opt/example-app/web/static/",
			Expires:      "30d",
			CacheControl: "public, max-age=2592000",
			TryFiles:     true,
			GzipStatic:   true,
			AccessLog:    &staticAccessLog,
		},
		{
			Path:  "/sitemap.xml",
			Match: "exact",
			Alias: "/opt/example-app/web/static/sitemap.xml",
		},
	}
	valid.Nginx.ClientMaxBodySize = "100m"
	valid.Nginx.AccessLog = "/var/log/nginx/example-app.access.log"
	valid.Nginx.ErrorLog = "/var/log/nginx/example-app.error.log"
	valid.Nginx.Proxy.ConnectTimeout = "30s"
	valid.Nginx.Proxy.ReadTimeout = "600s"
	valid.Nginx.Proxy.SendTimeout = "600s"
	valid.Nginx.Proxy.Buffering = &proxyBuffering
	valid.Nginx.Proxy.RequestBuffering = &proxyBuffering
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	missingPath := validListenConfig()
	missingPath.Nginx.StaticLocations = []NginxStaticLocationConfig{{Alias: "/opt/example-app/web/static/"}}
	expectValidationError(t, missingPath, "nginx.static_locations[0].path is required")

	relativeAlias := validListenConfig()
	relativeAlias.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "web/static/"}}
	expectValidationError(t, relativeAlias, "nginx.static_locations[0].alias must be an absolute path")

	prefixWithoutSlash := validListenConfig()
	prefixWithoutSlash.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static", Alias: "/opt/example-app/web/static/"}}
	expectValidationError(t, prefixWithoutSlash, "nginx.static_locations[0].path must end with /")

	prefixAliasWithoutSlash := validListenConfig()
	prefixAliasWithoutSlash.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "/opt/example-app/web/static"}}
	expectValidationError(t, prefixAliasWithoutSlash, "nginx.static_locations[0].alias must end with /")

	exactWithSlash := validListenConfig()
	exactWithSlash.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/sitemap.xml/", Match: "exact", Alias: "/opt/example-app/web/static/sitemap.xml"}}
	expectValidationError(t, exactWithSlash, "nginx.static_locations[0].path must not end with /")

	overlapACME := validListenConfig()
	overlapACME.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/.well-known/", Alias: "/opt/example-app/web/static/"}}
	expectValidationError(t, overlapACME, "nginx.static_locations[0].path must not overlap /.well-known/acme-challenge/")

	commentedStaticPath := validListenConfig()
	commentedStaticPath.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/#/", Alias: "/opt/example-app/web/static/"}}
	expectValidationError(t, commentedStaticPath, "nginx.static_locations[0].path must not contain")

	commentedStaticAlias := validListenConfig()
	commentedStaticAlias.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "/opt/example-app/web/#/"}}
	expectValidationError(t, commentedStaticAlias, "nginx.static_locations[0].alias must not contain")

	injectedHeader := validListenConfig()
	injectedHeader.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "/opt/example-app/web/static/", CacheControl: "public\";\nreturn 200"}}
	expectValidationError(t, injectedHeader, "nginx.static_locations[0].cache_control must not contain")

	badExpires := validListenConfig()
	badExpires.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "/opt/example-app/web/static/", Expires: "30 days"}}
	expectValidationError(t, badExpires, "nginx.static_locations[0].expires must be off, epoch, max, or a simple nginx time")

	duplicatePath := validListenConfig()
	duplicatePath.Nginx.StaticLocations = []NginxStaticLocationConfig{
		{Path: "/static/", Alias: "/opt/example-app/web/static/"},
		{Path: "/static/", Alias: "/opt/example-app/web/static2/"},
	}
	expectValidationError(t, duplicatePath, "nginx.static_locations[1].path must not duplicate another static location")

	badClientSize := validListenConfig()
	badClientSize.Nginx.ClientMaxBodySize = "20 mb"
	expectValidationError(t, badClientSize, "nginx.client_max_body_size must be a simple nginx size")

	badProxyTimeout := validListenConfig()
	badProxyTimeout.Nginx.Proxy.ReadTimeout = "five minutes"
	expectValidationError(t, badProxyTimeout, "nginx.proxy.read_timeout must be a simple nginx time")

	badAccessLog := validListenConfig()
	badAccessLog.Nginx.AccessLog = "relative.log"
	expectValidationError(t, badAccessLog, "nginx.access_log must be an absolute path")

	commentedAccessLog := validListenConfig()
	commentedAccessLog.Nginx.AccessLog = "/var/log/nginx/example#app.access.log"
	expectValidationError(t, commentedAccessLog, "nginx.access_log must not contain")

	badErrorLog := validListenConfig()
	badErrorLog.Nginx.ErrorLog = "/var/log/nginx/error log"
	expectValidationError(t, badErrorLog, "nginx.error_log must not contain whitespace")

	commentedErrorLog := validListenConfig()
	commentedErrorLog.Nginx.ErrorLog = "/var/log/nginx/example#app.error.log"
	expectValidationError(t, commentedErrorLog, "nginx.error_log must not contain")

	staticAccessLogOn := true
	badStaticAccessLog := validListenConfig()
	badStaticAccessLog.Nginx.StaticLocations = []NginxStaticLocationConfig{{Path: "/static/", Alias: "/opt/example-app/web/static/", AccessLog: &staticAccessLogOn}}
	expectValidationError(t, badStaticAccessLog, "nginx.static_locations[0].access_log only supports false")
}

func TestValidateTailscaleRules(t *testing.T) {
	t.Parallel()

	cfg := validUpstreamConfig()
	cfg.Tailscale.LoginServer = "http://hs.example.com"
	expectValidationError(t, cfg, "tailscale.login_server must use https")

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://user:pass@hs.example.com"
	expectValidationError(t, cfg, "tailscale.login_server must be an origin URL")

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://hs.example.com/headscale"
	expectValidationError(t, cfg, "tailscale.login_server must not include a path")

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://hs.example.com?token=secret"
	expectValidationError(t, cfg, "tailscale.login_server must be an origin URL")

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://HS.EXAMPLE.COM:443/"
	cfg.normalize()
	if cfg.Tailscale.LoginServer != "https://hs.example.com" {
		t.Fatalf("normalized login_server = %q, want https://hs.example.com", cfg.Tailscale.LoginServer)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with canonical login_server error = %v", err)
	}

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://hs.example.com"
	cfg.Tailscale.MeshifyConfig = "meshify.yaml"
	expectValidationError(t, cfg, "tailscale.meshify_config must be empty when tailscale.login_server is set")

	cfg = validUpstreamConfig()
	cfg.Tailscale.LoginServer = "https://abc.com"
	expectValidationError(t, cfg, "tailscale.login_server host must not reuse an app domain")

	cfg = validUpstreamConfig()
	cfg.Tailscale.MeshifyConfig = "./meshify.yaml"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with relative meshify config error = %v", err)
	}

	cfg = validUpstreamConfig()
	cfg.Tailscale.Hostname = "Bad_Host"
	expectValidationError(t, cfg, "tailscale.hostname must contain only lowercase")

	cfg = validUpstreamConfig()
	cfg.Tailscale.AuthKeyFile = "auth.key"
	expectValidationError(t, cfg, "tailscale.auth_key_file must be an absolute path")

	cfg = validUpstreamConfig()
	cfg.Tailscale.AuthKeyFile = "/run/meshify/tailscale-auth.key"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestExampleYAMLMatchesDeployTemplateAndDoesNotCarrySecrets(t *testing.T) {
	t.Parallel()

	got, err := ExampleYAML()
	if err != nil {
		t.Fatalf("ExampleYAML() error = %v", err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config", "meshify-app.yaml.example"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("ExampleYAML() mismatch with deploy example\n got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(string(got), "authkey-") || strings.Contains(string(got), "CF_DNS_API_TOKEN=") {
		t.Fatalf("ExampleYAML() contains secret-looking value:\n%s", got)
	}
	if _, err := LoadBytes(got); err != nil {
		t.Fatalf("LoadBytes(ExampleYAML()) error = %v", err)
	}
}

func TestWriteFileUsesStrictPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "meshify-app.yaml")
	if err := validListenConfig().WriteFile(path); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func expectValidationError(t *testing.T, cfg Config, want string) {
	t.Helper()

	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate() error = nil, want substring %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate() error = %q, want substring %q", err.Error(), want)
	}
}
