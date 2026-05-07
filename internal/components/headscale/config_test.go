package headscale

import (
	"meshify/internal/assets"
	"meshify/internal/config"
	"meshify/internal/render"
	"strings"
	"testing"
)

func TestValidateRuntimeConfigAcceptsRenderedTemplateGuardrails(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.Network.PublicIPv4 = "203.0.113.10"
	cfg.Advanced.Network.PublicIPv6 = "2001:db8::10"
	content := renderHeadscaleConfig(t, cfg)

	if err := ValidateRuntimeConfig(cfg, content); err != nil {
		t.Fatalf("ValidateRuntimeConfig() error = %v", err)
	}

	parsed, err := ParseRuntimeConfig(content)
	if err != nil {
		t.Fatalf("ParseRuntimeConfig() error = %v", err)
	}
	if parsed.DERP.Server.IPv4 != "203.0.113.10" {
		t.Fatalf("DERP IPv4 = %q, want configured override", parsed.DERP.Server.IPv4)
	}
	if parsed.DERP.Server.IPv6 != "2001:db8::10" {
		t.Fatalf("DERP IPv6 = %q, want configured override", parsed.DERP.Server.IPv6)
	}
}

func TestParseRuntimeConfigRejectsMultipleYAMLDocuments(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	content := string(renderHeadscaleConfig(t, cfg)) + "\n---\nserver_url: https://shadow.example.com\n"

	_, err := ParseRuntimeConfig([]byte(content))
	if err == nil {
		t.Fatal("ParseRuntimeConfig() error = nil, want multiple document failure")
	}
	if !strings.Contains(err.Error(), "exactly one YAML document") {
		t.Fatalf("error = %q, want single document failure", err.Error())
	}
}

func TestValidateRuntimeConfigRejectsPublicControlPlaneListeners(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	content := string(renderHeadscaleConfig(t, cfg))
	content = strings.Replace(content, `listen_addr: "127.0.0.1:8080"`, `listen_addr: "0.0.0.0:8080"`, 1)

	err := ValidateRuntimeConfig(cfg, []byte(content))
	if err == nil {
		t.Fatal("ValidateRuntimeConfig() error = nil, want listener failure")
	}
	if !strings.Contains(err.Error(), `listen_addr must be "127.0.0.1:8080"`) {
		t.Fatalf("error = %q, want listen_addr guardrail", err.Error())
	}
}

func TestValidateRuntimeConfigRejectsExternalDERPAndTelemetry(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	content := string(renderHeadscaleConfig(t, cfg))
	content = strings.Replace(content, "  urls: []", "  urls:\n    - https://controlplane.tailscale.com/derpmap/default", 1)
	content = strings.Replace(content, "disable_check_updates: true", "disable_check_updates: false", 1)
	content = strings.Replace(content, "  enabled: false", "  enabled: true", 1)

	err := ValidateRuntimeConfig(cfg, []byte(content))
	if err == nil {
		t.Fatal("ValidateRuntimeConfig() error = nil, want DERP/logtail failure")
	}
	for _, want := range []string{
		"derp.urls must be empty",
		"disable_check_updates must be true",
		"logtail.enabled must be false",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err.Error(), want)
		}
	}
}

func renderHeadscaleConfig(t *testing.T, cfg config.Config) []byte {
	t.Helper()

	data, err := render.NewTemplateData(cfg)
	if err != nil {
		t.Fatalf("NewTemplateData() error = %v", err)
	}
	renderer := render.NewRenderer(assets.NewLoader())
	content, err := renderer.Render(assets.MustLookup("templates/etc/headscale/config.yaml.tmpl"), data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return content
}
