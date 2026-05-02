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
	if cfg.Advanced.PackageSource.Mode != PackageSourceModeDirect {
		t.Fatalf("PackageSource.Mode = %q, want %q", cfg.Advanced.PackageSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.PackageSource.Version != DefaultHeadscaleVersion {
		t.Fatalf("PackageSource.Version = %q, want %q", cfg.Advanced.PackageSource.Version, DefaultHeadscaleVersion)
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
	if cfg.Advanced.PackageSource.Mode != PackageSourceModeDirect {
		t.Fatalf("PackageSource.Mode = %q, want %q", cfg.Advanced.PackageSource.Mode, PackageSourceModeDirect)
	}
	if cfg.Advanced.PackageSource.Version != DefaultHeadscaleVersion {
		t.Fatalf("PackageSource.Version = %q, want %q", cfg.Advanced.PackageSource.Version, DefaultHeadscaleVersion)
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

func TestValidatePackageSourceModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "mirror requires url",
			mutate: func(cfg *Config) {
				cfg.Advanced.PackageSource.Mode = PackageSourceModeMirror
				cfg.Advanced.PackageSource.SHA256 = strings.Repeat("a", 64)
			},
			wantErr: "advanced.package_source.url is required when advanced.package_source.mode is mirror",
		},
		{
			name: "mirror requires sha256",
			mutate: func(cfg *Config) {
				cfg.Advanced.PackageSource.Mode = PackageSourceModeMirror
				cfg.Advanced.PackageSource.URL = "https://mirror.example.com/headscale.deb"
			},
			wantErr: "advanced.package_source.sha256 is required when advanced.package_source.mode is mirror",
		},
		{
			name: "offline requires file path",
			mutate: func(cfg *Config) {
				cfg.Advanced.PackageSource.Mode = PackageSourceModeOffline
				cfg.Advanced.PackageSource.SHA256 = strings.Repeat("b", 64)
			},
			wantErr: "advanced.package_source.file_path is required when advanced.package_source.mode is offline",
		},
		{
			name: "offline requires sha256",
			mutate: func(cfg *Config) {
				cfg.Advanced.PackageSource.Mode = PackageSourceModeOffline
				cfg.Advanced.PackageSource.FilePath = "/tmp/headscale.deb"
			},
			wantErr: "advanced.package_source.sha256 is required when advanced.package_source.mode is offline",
		},
		{
			name: "direct rejects custom source fields",
			mutate: func(cfg *Config) {
				cfg.Advanced.PackageSource.Mode = PackageSourceModeDirect
				cfg.Advanced.PackageSource.URL = "https://mirror.example.com/headscale.deb"
			},
			wantErr: "advanced.package_source.url is only allowed when advanced.package_source.mode is mirror",
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

	cfg.Advanced.DNS01.Provider = "cloudflare"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil after provider set", err)
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
}
