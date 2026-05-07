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

func TestValidateRenderedSiteRejectsPseudoCatchAllServers(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "server_name hs.example.com;", "server_name _;", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "pseudo catch-all server_name _") {
		t.Fatalf("error = %q, want pseudo catch-all failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsNamedServerDefaultOwnership(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "listen 80;\n    listen [::]:80;\n    server_name hs.example.com;", "listen 80 default_server;\n    listen [::]:80 default_server;\n    server_name hs.example.com;", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "named Nginx site must not be the default_server") {
		t.Fatalf("error = %q, want named default_server ownership failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsMissingDefaultCatchAll(t *testing.T) {
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
	if !strings.Contains(err.Error(), "missing HTTP default_server catch-all") || !strings.Contains(err.Error(), "missing HTTPS default_server catch-all") {
		t.Fatalf("error = %q, want missing default catch-all failures", err.Error())
	}
}

func TestValidateRenderedSiteRejectsDeprecatedHTTP2ListenSyntax(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "listen 443 ssl default_server;", "listen 443 ssl http2 default_server;", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "deprecated listen ... http2 syntax") {
		t.Fatalf("error = %q, want deprecated http2 listen failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsDefaultCatchAllProxy(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "return 444;", "proxy_pass http://headscale_upstream;", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "default_server catch-all must not proxy to Headscale") {
		t.Fatalf("error = %q, want catch-all proxy failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsMissingHostSNIGuards(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "if ($meshify_host_header_valid = 0)", "if ($meshify_other_guard = 0)", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "missing if ($meshify_host_header_valid = 0) guard") {
		t.Fatalf("error = %q, want Host guard failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsIncompleteSNIGuardMap(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)), "map $ssl_server_name $meshify_sni_valid {\n    default 0;\n    \"hs.example.com\" 1;\n}", "map $ssl_server_name $meshify_sni_valid {\n    default 0;\n}", 1)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "HTTPS SNI guard allowlist missing") {
		t.Fatalf("error = %q, want SNI guard map failure", err.Error())
	}
}

func TestValidateRenderedSiteRejectsHostSNIGuardsAfterProxy(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	site, err := NewSiteConfig(cfg)
	if err != nil {
		t.Fatalf("NewSiteConfig() error = %v", err)
	}
	content := strings.Replace(string(renderNginxSite(t, cfg)),
		"    if ($meshify_sni_valid = 0) {\n        return 421;\n    }\n\n    if ($meshify_host_header_valid = 0) {\n        return 421;\n    }\n\n    location / {\n        proxy_pass http://headscale_upstream;",
		"    location / {\n        proxy_pass http://headscale_upstream;\n    }\n\n    if ($meshify_sni_valid = 0) {\n        return 421;\n    }\n\n    if ($meshify_host_header_valid = 0) {\n        return 421;\n    }\n\n    location /placeholder {",
		1,
	)

	err = ValidateRenderedSite(site, []byte(content))
	if err == nil {
		t.Fatal("ValidateRenderedSite() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "must reject Host/SNI mismatches before proxying") {
		t.Fatalf("error = %q, want Host/SNI guard ordering failure", err.Error())
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
