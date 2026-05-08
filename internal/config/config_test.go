package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func validConfig() Config {
	cfg := New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}

func TestNewAppliesDefaultsAndRequiresUserInputs(t *testing.T) {
	t.Parallel()

	cfg := New()

	if cfg.APIVersion != APIVersion {
		t.Fatalf("APIVersion = %q, want %q", cfg.APIVersion, APIVersion)
	}
	if cfg.Default.ACMEChallenge != ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", cfg.Default.ACMEChallenge, ACMEChallengeHTTP01)
	}
	if cfg.Advanced.HeadscaleSource.Mode != PackageSourceModeDirect {
		t.Fatalf("HeadscaleSource.Mode = %q, want %q", cfg.Advanced.HeadscaleSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.HeadscaleSource.Version != DefaultHeadscaleVersion {
		t.Fatalf("HeadscaleSource.Version = %q, want %q", cfg.Advanced.HeadscaleSource.Version, DefaultHeadscaleVersion)
	}
	if cfg.Advanced.LegoSource.Mode != PackageSourceModeDirect {
		t.Fatalf("LegoSource.Mode = %q, want %q", cfg.Advanced.LegoSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.Platform.Arch != ArchAMD64 {
		t.Fatalf("Platform.Arch = %q, want %q", cfg.Advanced.Platform.Arch, ArchAMD64)
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}

	for _, want := range []string{
		"default.server_url is required",
		"default.base_domain is required",
		"default.certificate_email is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %q, want substring %q", err.Error(), want)
		}
	}
}

func TestLoadBytesAppliesDefaultsAndAllowsEmptyIPv6Override(t *testing.T) {
	t.Parallel()

	cfg, err := LoadBytes([]byte(`
default:
  server_url: https://hs.example.com
  base_domain: tailnet.example.com
  certificate_email: ops@example.com
`))
	if err != nil {
		t.Fatalf("LoadBytes() error = %v", err)
	}

	if cfg.APIVersion != APIVersion {
		t.Fatalf("APIVersion = %q, want %q", cfg.APIVersion, APIVersion)
	}
	if cfg.Default.ACMEChallenge != ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", cfg.Default.ACMEChallenge, ACMEChallengeHTTP01)
	}
	if cfg.Advanced.HeadscaleSource.Mode != PackageSourceModeDirect {
		t.Fatalf("HeadscaleSource.Mode = %q, want %q", cfg.Advanced.HeadscaleSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.HeadscaleSource.Version != DefaultHeadscaleVersion {
		t.Fatalf("HeadscaleSource.Version = %q, want %q", cfg.Advanced.HeadscaleSource.Version, DefaultHeadscaleVersion)
	}
	if cfg.Advanced.LegoSource.Mode != PackageSourceModeDirect {
		t.Fatalf("LegoSource.Mode = %q, want %q", cfg.Advanced.LegoSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.Network.PublicIPv6 != "" {
		t.Fatalf("PublicIPv6 = %q, want empty", cfg.Advanced.Network.PublicIPv6)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsServerURLWithoutHTTPS(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ServerURL = "http://hs.example.com"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "default.server_url must use https") {
		t.Fatalf("Validate() error = %q, want https failure", err.Error())
	}
}

func TestValidateRejectsServerURLWithUnsupportedPort(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ServerURL = "https://hs.example.com:8443"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-443 port failure")
	}
	if !strings.Contains(err.Error(), "default.server_url must not specify a port other than 443") {
		t.Fatalf("Validate() error = %q, want non-443 port failure", err.Error())
	}

	cfg.Default.ServerURL = "https://hs.example.com:443"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for explicit 443", err)
	}
}

func TestValidateRejectsCertificateEmailSystemdSpecifiers(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.CertificateEmail = "ops%prod@example.com"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want percent failure")
	}
	if !strings.Contains(err.Error(), "default.certificate_email must not contain %") {
		t.Fatalf("Validate() error = %q, want percent failure", err.Error())
	}
}

func TestValidateRejectsBaseDomainConflictsWithServerHost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		baseDomain string
		want       string
	}{
		{name: "same host", baseDomain: "hs.example.com", want: "default.base_domain must not equal default.server_url host"},
		{name: "parent domain", baseDomain: "example.com", want: "default.base_domain must not be a parent domain of default.server_url host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			cfg.Default.BaseDomain = tt.baseDomain

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestValidateProxyAllowsHTTPProxyEnvironmentForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		httpProxy  string
		httpsProxy string
	}{
		{name: "absolute URL", httpProxy: "http://proxy.internal:8080"},
		{name: "host port", httpProxy: "proxy.internal:8080"},
		{name: "host without port", httpsProxy: "secure-proxy.internal"},
		{name: "IPv6 host port", httpsProxy: "[2001:db8::10]:8443"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			cfg.Advanced.Proxy.HTTPProxy = tt.httpProxy
			cfg.Advanced.Proxy.HTTPSProxy = tt.httpsProxy

			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestValidateProxyRejectsMalformedProxyEnvironmentValues(t *testing.T) {
	t.Parallel()

	tests := []string{
		"bad host",
		"http://",
		"http://proxy.internal:bad",
		"//proxy.internal:8080",
	}

	for _, value := range tests {
		value := value
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			cfg.Advanced.Proxy.HTTPProxy = value

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want proxy validation failure")
			}
			if !strings.Contains(err.Error(), "advanced.proxy.http_proxy must be a proxy URL or host[:port]") {
				t.Fatalf("Validate() error = %q, want proxy validation failure", err.Error())
			}
		})
	}
}

func TestValidateHeadscaleSourceModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "mirror requires url",
			mutate: func(cfg *Config) {
				cfg.Advanced.HeadscaleSource.Mode = PackageSourceModeMirror
				cfg.Advanced.HeadscaleSource.SHA256 = strings.Repeat("a", 64)
			},
			wantErr: "advanced.headscale_source.url is required when advanced.headscale_source.mode is mirror",
		},
		{
			name: "mirror requires sha256",
			mutate: func(cfg *Config) {
				cfg.Advanced.HeadscaleSource.Mode = PackageSourceModeMirror
				cfg.Advanced.HeadscaleSource.URL = "https://mirror.example.com/headscale.deb"
			},
			wantErr: "advanced.headscale_source.sha256 is required when advanced.headscale_source.mode is mirror",
		},
		{
			name: "offline requires file path",
			mutate: func(cfg *Config) {
				cfg.Advanced.HeadscaleSource.Mode = PackageSourceModeOffline
				cfg.Advanced.HeadscaleSource.SHA256 = strings.Repeat("b", 64)
			},
			wantErr: "advanced.headscale_source.file_path is required when advanced.headscale_source.mode is offline",
		},
		{
			name: "offline requires sha256",
			mutate: func(cfg *Config) {
				cfg.Advanced.HeadscaleSource.Mode = PackageSourceModeOffline
				cfg.Advanced.HeadscaleSource.FilePath = "/tmp/headscale.deb"
			},
			wantErr: "advanced.headscale_source.sha256 is required when advanced.headscale_source.mode is offline",
		},
		{
			name: "direct rejects custom source fields",
			mutate: func(cfg *Config) {
				cfg.Advanced.HeadscaleSource.Mode = PackageSourceModeDirect
				cfg.Advanced.HeadscaleSource.URL = "https://mirror.example.com/headscale.deb"
			},
			wantErr: "advanced.headscale_source.url is only allowed when advanced.headscale_source.mode is mirror",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateLegoSourceModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "offline requires file path",
			mutate: func(cfg *Config) {
				cfg.Advanced.LegoSource.Mode = PackageSourceModeOffline
			},
			wantErr: "advanced.lego_source.file_path is required when advanced.lego_source.mode is offline",
		},
		{
			name: "direct rejects file path",
			mutate: func(cfg *Config) {
				cfg.Advanced.LegoSource.Mode = PackageSourceModeDirect
				cfg.Advanced.LegoSource.FilePath = "/tmp/lego.tar.gz"
			},
			wantErr: "advanced.lego_source.file_path is only allowed when advanced.lego_source.mode is offline",
		},
		{
			name: "unsupported mode",
			mutate: func(cfg *Config) {
				cfg.Advanced.LegoSource.Mode = "mirror"
			},
			wantErr: "advanced.lego_source.mode must be one of: direct, offline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want non-nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}

	cfg := validConfig()
	cfg.Advanced.LegoSource.Mode = PackageSourceModeOffline
	cfg.Advanced.LegoSource.FilePath = "/srv/packages/lego_v4.35.2_linux_amd64.tar.gz"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for offline lego archive", err)
	}
}

func TestValidateDNS01RequiresProviderWhenEnabled(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = ACMEChallengeDNS01

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "advanced.dns01.provider is required when default.acme_challenge is dns-01") {
		t.Fatalf("Validate() error = %q, want provider failure", err.Error())
	}

	cfg.Advanced.DNS01.Provider = "azure"
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want unsupported provider failure")
	}
	if !strings.Contains(err.Error(), "unsupported DNS-01 provider") {
		t.Fatalf("Validate() error = %q, want unsupported provider failure", err.Error())
	}

	cfg.Advanced.DNS01.Provider = "cloudflare"
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want env file failure for cloudflare")
	}
	if !strings.Contains(err.Error(), "advanced.dns01.env_file is required for DNS-01 renewal with lego DNS provider cloudflare") {
		t.Fatalf("Validate() error = %q, want env file failure", err.Error())
	}

	cfg.Advanced.DNS01.EnvFile = "cloudflare.env"
	err = cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want absolute env path failure")
	}
	if !strings.Contains(err.Error(), "advanced.dns01.env_file must be an absolute path when set") {
		t.Fatalf("Validate() error = %q, want absolute path failure", err.Error())
	}

	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{
			name: "newline injection",
			path: "/etc/meshify/dns01/cloudflare.env\nExecStart=/bin/false",
			want: "must not contain whitespace or control characters",
		},
		{
			name: "space",
			path: "/etc/meshify/dns01/cloudflare env",
			want: "must not contain whitespace or control characters",
		},
		{
			name: "specifier",
			path: "/etc/meshify/dns01/%i.env",
			want: "must not contain systemd glob, specifier, quote, or escape characters",
		},
		{
			name: "glob",
			path: "/etc/meshify/dns01/*.env",
			want: "must not contain systemd glob, specifier, quote, or escape characters",
		},
		{
			name: "dot segment",
			path: "/etc/meshify/dns01/../cloudflare.env",
			want: "must be a clean absolute path",
		},
	} {
		t.Run("unsafe env file "+tt.name, func(t *testing.T) {
			cfg.Advanced.DNS01.EnvFile = tt.path
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want unsafe env file failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}

	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/cloudflare.env"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil after provider env file set", err)
	}

	cfg.Advanced.DNS01.Provider = "route53"
	cfg.Advanced.DNS01.EnvFile = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for route53 ambient credentials", err)
	}
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/route53.env"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for route53 env file", err)
	}

	cfg.Advanced.DNS01.Provider = "google"
	cfg.Advanced.DNS01.EnvFile = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for gcloud ambient credentials", err)
	}
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/gcloud.env"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for google alias with env file", err)
	}
}

func TestLoadBytesRejectsUnsupportedAdvancedFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "zone",
			yaml: `
default:
  server_url: https://hs.example.com
  base_domain: tailnet.example.com
  certificate_email: ops@example.com
advanced:
  dns01:
    zone: example.com
`,
			want: "field zone not found",
		},
		{
			name: "credentials_file",
			yaml: `
default:
  server_url: https://hs.example.com
  base_domain: tailnet.example.com
  certificate_email: ops@example.com
advanced:
  dns01:
    credentials_file: /etc/meshify/dns01/cloudflare.env
`,
			want: "field credentials_file not found",
		},
		{
			name: "old package_source",
			yaml: `
default:
  server_url: https://hs.example.com
  base_domain: tailnet.example.com
  certificate_email: ops@example.com
advanced:
  package_source:
    mode: direct
    version: 0.28.0
`,
			want: "field package_source not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadBytes([]byte(tt.yaml))
			if err == nil {
				t.Fatal("LoadBytes() error = nil, want unknown field failure")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("LoadBytes() error = %q, want substring %q", err.Error(), tt.want)
			}
		})
	}
}

func TestLoadBytesRejectsMultipleYAMLDocuments(t *testing.T) {
	t.Parallel()

	_, err := LoadBytes([]byte(`
default:
  server_url: https://hs.example.com
  base_domain: tailnet.example.com
  certificate_email: ops@example.com
---
default:
  server_url: https://ignored.example.com
  base_domain: ignored.example.com
  certificate_email: ignored@example.com
`))
	if err == nil {
		t.Fatal("LoadBytes() error = nil, want multiple document failure")
	}
	if !strings.Contains(err.Error(), "multiple YAML documents are not supported") {
		t.Fatalf("LoadBytes() error = %q, want multiple document failure", err.Error())
	}
}

func TestExportAndLoadFileRoundTrip(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "meshify.yaml")
	want := validConfig()
	want.Advanced.Proxy.HTTPProxy = "http://proxy.internal:8080"
	want.Advanced.Proxy.NoProxy = "127.0.0.1,localhost"
	want.Advanced.Network.PublicIPv4 = "203.0.113.10"
	want.Advanced.Platform.Arch = ArchARM64

	if err := want.WriteFile(path); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestExampleYAMLMatchesPublicTemplate(t *testing.T) {
	t.Parallel()

	got, err := ExampleYAML()
	if err != nil {
		t.Fatalf("ExampleYAML() error = %v", err)
	}

	want, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config", "meshify.yaml.example"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if string(got) != string(want) {
		t.Fatalf("ExampleYAML() mismatch\n got:\n%s\nwant:\n%s", got, want)
	}

	cfg, err := LoadBytes(got)
	if err != nil {
		t.Fatalf("LoadBytes() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if strings.Contains(string(got), "secret") || strings.Contains(string(got), "token") {
		t.Fatalf("example template unexpectedly contains credential-like fields:\n%s", got)
	}
	if strings.Contains(string(got), "credentials_file") {
		t.Fatalf("example template still contains old credentials_file field:\n%s", got)
	}
	if !strings.Contains(string(got), "env_file") {
		t.Fatalf("example template does not contain env_file field:\n%s", got)
	}
}
