package render

import (
	"meshify/internal/assets"
	"meshify/internal/config"
	"strings"
	"testing"
)

func TestNewTemplateDataDerivesTemplateFields(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.Network.PublicIPv4 = "203.0.113.10"

	data, err := NewTemplateData(cfg)
	if err != nil {
		t.Fatalf("NewTemplateData() error = %v", err)
	}

	if data.ServerURL != "https://hs.example.com" {
		t.Fatalf("ServerURL = %q, want %q", data.ServerURL, "https://hs.example.com")
	}
	if data.ServerName != "hs.example.com" {
		t.Fatalf("ServerName = %q, want %q", data.ServerName, "hs.example.com")
	}
	if data.BaseDomain != "tailnet.example.com" {
		t.Fatalf("BaseDomain = %q, want %q", data.BaseDomain, "tailnet.example.com")
	}
	if data.CertificateEmail != "ops@example.com" {
		t.Fatalf("CertificateEmail = %q, want %q", data.CertificateEmail, "ops@example.com")
	}
	if data.ACMEChallenge != config.ACMEChallengeHTTP01 {
		t.Fatalf("ACMEChallenge = %q, want %q", data.ACMEChallenge, config.ACMEChallengeHTTP01)
	}
	if data.HeadscaleMetricsPort != config.DefaultHeadscaleMetricsPort {
		t.Fatalf("HeadscaleMetricsPort = %d, want %d", data.HeadscaleMetricsPort, config.DefaultHeadscaleMetricsPort)
	}
	if data.PublicIPv4 != "203.0.113.10" {
		t.Fatalf("PublicIPv4 = %q, want %q", data.PublicIPv4, "203.0.113.10")
	}
	if data.PublicIPv6 != "" {
		t.Fatalf("PublicIPv6 = %q, want empty", data.PublicIPv6)
	}
}

func TestNewTemplateDataCanonicalizesLegoDNSProvider(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "google"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/gcloud.env"

	data, err := NewTemplateData(cfg)
	if err != nil {
		t.Fatalf("NewTemplateData() error = %v", err)
	}
	if data.DNSProvider != "gcloud" {
		t.Fatalf("DNSProvider = %q, want canonical lego provider code gcloud", data.DNSProvider)
	}
}

func TestRendererRenderUsesTemplateData(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.Network.PublicIPv4 = "203.0.113.10"

	data, err := NewTemplateData(cfg)
	if err != nil {
		t.Fatalf("NewTemplateData() error = %v", err)
	}

	renderer := NewRenderer(assets.NewLoader())
	asset := assets.MustLookup("templates/etc/headscale/config.yaml.tmpl")
	got, err := renderer.Render(asset, data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	text := string(got)
	for _, want := range []string{
		`server_url: "https://hs.example.com"`,
		`metrics_listen_addr: "127.0.0.1:19090"`,
		`base_domain: "tailnet.example.com"`,
		`ipv4: "203.0.113.10"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("Render() output missing %q\n%s", want, text)
		}
	}

	if strings.Contains(text, `ipv6:`) {
		t.Fatalf("Render() output unexpectedly contains ipv6 stanza\n%s", text)
	}
}

func validConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
