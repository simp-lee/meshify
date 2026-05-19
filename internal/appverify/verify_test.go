package appverify

import (
	"meshify/internal/appassets"
	"meshify/internal/appconfig"
	"meshify/internal/apprender"
	"meshify/internal/components/appsvc"
	"strings"
	"testing"
)

func testAppConfig() appconfig.Config {
	cfg := appconfig.New()
	cfg.App.Name = "api"
	cfg.App.Domains = []string{"api.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/api/api --listen 127.0.0.1:18001"
	return cfg
}

func TestStaticReportRejectsForeignMarkerPrefix(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	report := StaticReport(cfg, []apprender.StagedFile{{
		SourcePath: "templates/app/service.tmpl",
		HostPath:   "/etc/systemd/system/api.service",
		Content:    []byte("# " + appsvc.ManagedMarker("api-admin") + "\n"),
	}})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want foreign marker failure")
	}
	if got := checkSummary(report, "ownership"); !strings.Contains(got, "different Meshify app") {
		t.Fatalf("ownership summary = %q, want foreign marker detail", got)
	}
}

func TestStaticReportRejectsSensitiveValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "tailscale auth key", content: "tskey-auth-secret-value", want: "tskey-auth-"},
		{name: "headscale auth key", content: "hskey-auth-secret-value", want: "hskey-auth-"},
		{name: "legacy tailscale auth key", content: "authkey-secret-value", want: "authkey-"},
		{name: "raw dns token", content: "CF_DNS_API_TOKEN=secret-value", want: "CF_DNS_API_TOKEN"},
		{name: "raw dns token in systemd environment", content: "Environment=DO_AUTH_TOKEN=secret-value", want: "DO_AUTH_TOKEN"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := testAppConfig()
			report := StaticReport(cfg, []apprender.StagedFile{{
				SourcePath: "templates/app/service.tmpl",
				HostPath:   "/etc/systemd/system/api.service",
				Content:    []byte("# " + appsvc.ManagedMarker("api") + "\n" + tt.content + "\n"),
			}})
			if report.FailedCount() == 0 {
				t.Fatal("FailedCount() = 0, want sensitive-value failure")
			}
			if got := checkSummary(report, "secrets"); !strings.Contains(got, tt.want) {
				t.Fatalf("secrets summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaticReportAllowsCredentialFileReferences(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	report := StaticReport(cfg, []apprender.StagedFile{{
		SourcePath: "templates/app/service.tmpl",
		HostPath:   "/etc/systemd/system/api.service",
		Content:    []byte("# " + appsvc.ManagedMarker("api") + "\nCF_DNS_API_TOKEN_FILE=/run/secrets/cf-token\n"),
	}})
	if got := checkSummary(report, "secrets"); !strings.Contains(got, "未包含") {
		t.Fatalf("secrets summary = %q, want pass", got)
	}
}

func TestStaticReportRejectsLegacyLegoV4Forms(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	report := StaticReport(cfg, []apprender.StagedFile{{
		SourcePath: "templates/app/lego-renew.service.tmpl",
		HostPath:   "/etc/systemd/system/api-lego-renew.service",
		Content:    []byte("# " + appsvc.ManagedMarker("api") + "\nExecStart=/opt/meshify/bin/lego renew --renew-hook /hook\n"),
	}})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want legacy lego v4 failure")
	}
	if got := checkSummary(report, "lego-v5"); !strings.Contains(got, "--renew-hook") {
		t.Fatalf("lego-v5 summary = %q, want stale hook detail", got)
	}
}

func TestStaticReportRejectsSystemdUnsafeConfig(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	cfg.Service.ExecStart = "/opt/api/api --listen 127.0.0.1:18001 ; /bin/true"
	report := StaticReport(cfg, nil)
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want config failure")
	}
	if got := checkSummary(report, "config"); !strings.Contains(got, "service.exec_start must not contain systemd separators") {
		t.Fatalf("config summary = %q, want unsafe exec_start detail", got)
	}

	cfg = testAppConfig()
	cfg.App.CertificateEmail = "o'ps@example.com"
	report = StaticReport(cfg, nil)
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want config failure")
	}
	if got := checkSummary(report, "config"); !strings.Contains(got, "app.certificate_email must be a systemd-safe email token") {
		t.Fatalf("config summary = %q, want unsafe email detail", got)
	}
}

func TestStaticReportRejectsMissingRuntimeFiles(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	staged := mustStageRuntime(t, cfg)
	report := StaticReport(cfg, withoutSource(staged, appassets.RenewTimerTemplate))
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want missing renew timer failure")
	}
	if got := checkSummary(report, "templates"); !strings.Contains(got, "缺少 app runtime 文件: "+appassets.RenewTimerTemplate) {
		t.Fatalf("templates summary = %q, want missing renew timer detail", got)
	}

	report = StaticReport(cfg, withoutSource(staged, appassets.ServiceTemplate))
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want missing listen service failure")
	}
	if got := checkSummary(report, "templates"); !strings.Contains(got, appassets.ServiceTemplate) {
		t.Fatalf("templates summary = %q, want missing service detail", got)
	}
}

func TestStaticReportRejectsRuntimeHostPathDrift(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	staged := mustStageRuntime(t, cfg)
	staged[0].HostPath = "/tmp/wrong-app-target"
	report := StaticReport(cfg, staged)
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want runtime host path drift failure")
	}
	if got := checkSummary(report, "templates"); !strings.Contains(got, "目标路径与 runtime catalog 不一致") {
		t.Fatalf("templates summary = %q, want host path drift detail", got)
	}
}

func TestStaticReportValidatesSNIGuard(t *testing.T) {
	t.Parallel()

	cfg := testAppConfig()
	tests := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{
			name: "missing SNI map",
			mutate: func(content string) string {
				return strings.Replace(content, "map $ssl_server_name $api_sni_valid", "map $host $api_sni_valid", 1)
			},
			want: "SNI allowlist map",
		},
		{
			name: "missing SNI entry",
			mutate: func(content string) string {
				return strings.Replace(content, "map $ssl_server_name $api_sni_valid {\n    default 0;\n    \"api.example.com\" 1;\n}", "map $ssl_server_name $api_sni_valid {\n    default 0;\n}", 1)
			},
			want: "SNI allowlist for api.example.com",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			staged := mustStageRuntime(t, cfg)
			for i := range staged {
				if staged[i].SourcePath != appassets.NginxTemplate {
					continue
				}
				staged[i].Content = []byte(tt.mutate(string(staged[i].Content)))
				break
			}
			report := StaticReport(cfg, staged)
			if report.FailedCount() == 0 {
				t.Fatal("FailedCount() = 0, want missing SNI guard failure")
			}
			if got := checkSummary(report, "nginx"); !strings.Contains(got, tt.want) {
				t.Fatalf("nginx summary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStaticReportReportsTailscaleRequirement(t *testing.T) {
	t.Parallel()

	listen := testAppConfig()
	report := StaticReport(listen, []apprender.StagedFile{{
		SourcePath: "templates/app/service.tmpl",
		HostPath:   "/etc/systemd/system/api.service",
		Content:    []byte("# " + appsvc.ManagedMarker("api") + "\n"),
	}})
	if got := checkSummary(report, "tailscale"); !strings.Contains(got, "不需要") {
		t.Fatalf("listen tailscale summary = %q, want not-required summary", got)
	}

	upstream := testAppConfig()
	upstream.App.Listen = ""
	upstream.App.Upstream = "100.64.10.20:18001"
	upstream.Service = appconfig.ServiceConfig{}
	report = StaticReport(upstream, []apprender.StagedFile{{
		SourcePath: "templates/app/service.tmpl",
		HostPath:   "/etc/systemd/system/api.service",
		Content:    []byte("# " + appsvc.ManagedMarker("api") + "\n"),
	}})
	if got := checkSummary(report, "tailscale"); !strings.Contains(got, "需要") || strings.Contains(got, "不需要") {
		t.Fatalf("upstream tailscale summary = %q, want required summary", got)
	}
}

func mustStageRuntime(t *testing.T, cfg appconfig.Config) []apprender.StagedFile {
	t.Helper()

	staged, err := apprender.StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	return staged
}

func withoutSource(staged []apprender.StagedFile, sourcePath string) []apprender.StagedFile {
	filtered := make([]apprender.StagedFile, 0, len(staged))
	for _, file := range staged {
		if file.SourcePath == sourcePath {
			continue
		}
		filtered = append(filtered, file)
	}
	return filtered
}

func checkSummary(report Report, id string) string {
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Summary
		}
	}
	return ""
}
