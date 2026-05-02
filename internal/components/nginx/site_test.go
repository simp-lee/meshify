package nginx

import (
	"meshify/internal/assets"
	"meshify/internal/config"
	"meshify/internal/render"
	"strings"
	"testing"
)

func TestValidateRenderedSiteAcceptsRuntimeTemplate(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := renderNginxSite(t, cfg)
	if err := ValidateRenderedSite(site, content); err != nil {
		t.Fatalf("ValidateRenderedSite() error = %v", err)
	}
}

func TestValidateRenderedSiteRejectsMissingUpgradeHeaders(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "proxy_set_header Upgrade $http_upgrade;", "", 1)
	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "WebSocket Upgrade header missing") {
		t.Fatalf("error = %q, want WebSocket header failure", err.Error())
	}
}

func TestValidateRenderedSiteRequiresNonHeadscaleDefaultServers(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.ReplaceAll(string(renderNginxSite(t, cfg)), " default_server", "")

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "catch-all default server missing") {
		t.Fatalf("error = %q, want catch-all default failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsHeadscaleDefaultServer(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "listen 80;\n    listen [::]:80;\n    server_name hs.example.com;", "listen 80 default_server;\n    listen [::]:80;\n    server_name hs.example.com;", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "Headscale server block must not claim default_server") {
		t.Fatalf("error = %q, want Headscale default_server failure", err.Error())
	}
}

func renderNginxSite(t *testing.T, cfg config.Config) []byte {
	t.Helper()

	data, err := render.NewTemplateData(cfg)
	if err != nil {
		t.Fatalf("NewTemplateData() error = %v", err)
	}
	content, err := render.NewRenderer(assets.NewLoader()).Render(assets.MustLookup("templates/etc/nginx/sites-available/headscale.conf.tmpl"), data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return content
}

func validConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
