package apprender

import (
	"meshify/internal/appconfig"
	"meshify/internal/components/appsvc"
	"strings"
	"testing"
)

func listenConfig() appconfig.Config {
	cfg := appconfig.New()
	cfg.App.Name = "example-app"
	cfg.App.Domains = []string{"abc.com", "www.abc.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/opt/example-app"
	return cfg
}

func TestStageRuntimeRendersListenModeAssets(t *testing.T) {
	t.Parallel()

	cfg := listenConfig()
	cfg.Service.EnvFile = "/opt/example-app/web.env"
	staticAccessLog := false
	proxyBuffering := false
	cfg.Nginx.ClientMaxBodySize = "100m"
	cfg.Nginx.AccessLog = "/var/log/nginx/example-app.access.log"
	cfg.Nginx.ErrorLog = "/var/log/nginx/example-app.error.log"
	cfg.Nginx.Proxy.ConnectTimeout = "30s"
	cfg.Nginx.Proxy.ReadTimeout = "600s"
	cfg.Nginx.Proxy.SendTimeout = "600s"
	cfg.Nginx.Proxy.Buffering = &proxyBuffering
	cfg.Nginx.Proxy.RequestBuffering = &proxyBuffering
	cfg.Nginx.StaticLocations = []appconfig.NginxStaticLocationConfig{
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
		{
			Path:  "/sitemaps/",
			Alias: "/opt/example-app/web/static/sitemaps/",
		},
	}
	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	if len(staged) != 5 {
		t.Fatalf("len(staged) = %d, want 5", len(staged))
	}

	var nginxContent []byte
	var hookContent []byte
	var serviceContent []byte
	for _, file := range staged {
		if !strings.Contains(string(file.Content), appsvc.ManagedMarker("example-app")) {
			t.Fatalf("%s missing managed marker", file.SourcePath)
		}
		if file.SourcePath == "templates/app/nginx.conf.tmpl" {
			nginxContent = file.Content
		}
		if file.SourcePath == "templates/app/install-cert-and-reload-nginx.sh.tmpl" {
			hookContent = file.Content
		}
		if file.SourcePath == "templates/app/service.tmpl" {
			serviceContent = file.Content
		}
	}
	if len(nginxContent) == 0 {
		t.Fatal("nginx content not staged")
	}
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	if err := appsvc.ValidateRenderedNginx(cfg, names, nginxContent); err != nil {
		t.Fatalf("ValidateRenderedNginx() error = %v\n%s", err, nginxContent)
	}
	nginxText := string(nginxContent)
	for _, want := range []string{
		"access_log /var/log/nginx/example-app.access.log;",
		"error_log /var/log/nginx/example-app.error.log;",
		"http2 on;",
		"client_max_body_size 100m;",
		"location /static/ {\n        alias /opt/example-app/web/static/;",
		"expires 30d;",
		`add_header Cache-Control "public, max-age=2592000";`,
		"try_files $uri =404;",
		"gzip_static on;",
		"access_log off;",
		"location = /sitemap.xml {\n        alias /opt/example-app/web/static/sitemap.xml;",
		"location /sitemaps/ {\n        alias /opt/example-app/web/static/sitemaps/;",
		"proxy_connect_timeout 30s;",
		"proxy_read_timeout 600s;",
		"proxy_send_timeout 600s;",
		"proxy_buffering off;",
		"proxy_request_buffering off;",
	} {
		if !strings.Contains(nginxText, want) {
			t.Fatalf("nginx content missing static location fragment %q\n%s", want, nginxText)
		}
	}
	staticIndex := strings.Index(nginxText, "location /static/ {")
	proxyIndex := strings.LastIndex(nginxText, "location / {\n        proxy_pass")
	if staticIndex < 0 || proxyIndex < 0 || staticIndex > proxyIndex {
		t.Fatalf("static locations must render before proxy location\n%s", nginxText)
	}
	hookText := string(hookContent)
	for _, want := range []string{
		`marker="/etc/example-app/tls/abc.com/.meshify-managed"`,
		`expected_marker="Meshify-managed: app.name=example-app"`,
		"certificate target is not owned by Meshify",
	} {
		if !strings.Contains(hookText, want) {
			t.Fatalf("hook content missing %q\n%s", want, hookText)
		}
	}
	serviceText := string(serviceContent)
	if !strings.Contains(serviceText, "EnvironmentFile=/opt/example-app/web.env") {
		t.Fatalf("service content missing service env file\n%s", serviceText)
	}
}

func TestStageRuntimeRendersUpstreamWithoutService(t *testing.T) {
	t.Parallel()

	cfg := listenConfig()
	cfg.App.Listen = ""
	cfg.App.Upstream = "100.64.10.20:18001"
	cfg.Service = appconfig.ServiceConfig{}
	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	if len(staged) != 4 {
		t.Fatalf("len(staged) = %d, want 4", len(staged))
	}
	for _, file := range staged {
		if strings.Contains(file.HostPath, "example-app.service") {
			t.Fatalf("upstream mode staged app service: %#v", file)
		}
		if file.SourcePath == "templates/app/nginx.conf.tmpl" && !strings.Contains(string(file.Content), "server 100.64.10.20:18001;") {
			t.Fatalf("nginx upstream not rendered:\n%s", file.Content)
		}
	}
}

func TestStageRuntimeRejectsSystemdUnsafeValues(t *testing.T) {
	t.Parallel()

	cfg := listenConfig()
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001 ; /bin/true"
	if _, err := StageRuntime(cfg); err == nil || !strings.Contains(err.Error(), "service.exec_start must not contain systemd separators") {
		t.Fatalf("StageRuntime() error = %v, want systemd-safe exec_start validation", err)
	}

	cfg = listenConfig()
	cfg.App.CertificateEmail = "o'ps@example.com"
	if _, err := StageRuntime(cfg); err == nil || !strings.Contains(err.Error(), "app.certificate_email must be a systemd-safe email token") {
		t.Fatalf("StageRuntime() error = %v, want systemd-safe email validation", err)
	}
}

func TestRenewServiceRendersAllSANDomains(t *testing.T) {
	t.Parallel()

	cfg := listenConfig()
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.DNS01.Provider = "cloudflare"
	cfg.DNS01.EnvFile = "/etc/example-app/dns/cloudflare.env"
	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	for _, file := range staged {
		if file.SourcePath != "templates/app/lego-renew.service.tmpl" {
			continue
		}
		text := string(file.Content)
		for _, want := range []string{
			"EnvironmentFile=/etc/example-app/dns/cloudflare.env",
			"--domains abc.com --domains www.abc.com",
			"--dns cloudflare",
			"migrate --path \"$lego_path\"",
			"--force-cert-domains --deploy-hook /usr/local/lib/meshify/apps/example-app/install-cert-and-reload-nginx.sh",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("renew service missing %q\n%s", want, text)
			}
		}
		return
	}
	t.Fatal("renew service not staged")
}

func TestRenderedNginxValidationRejectsMissingHTTPSHostGuard(t *testing.T) {
	t.Parallel()

	cfg := listenConfig()
	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	var nginxContent []byte
	for _, file := range staged {
		if file.SourcePath == "templates/app/nginx.conf.tmpl" {
			nginxContent = file.Content
			break
		}
	}
	if len(nginxContent) == 0 {
		t.Fatal("nginx content not staged")
	}
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	httpsGuard := "    if ($example_app_host_header_valid = 0) {\n        return 421;\n    }\n"
	text := string(nginxContent)
	index := strings.LastIndex(text, httpsGuard)
	if index < 0 {
		t.Fatalf("HTTPS host guard fixture not found:\n%s", text)
	}
	broken := text[:index] + text[index+len(httpsGuard):]
	err = appsvc.ValidateRenderedNginx(cfg, names, []byte(broken))
	if err == nil || !strings.Contains(err.Error(), "HTTPS Host allowlist before proxy location") {
		t.Fatalf("ValidateRenderedNginx() error = %v, want HTTPS Host guard failure", err)
	}
}
