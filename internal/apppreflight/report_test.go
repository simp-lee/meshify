package apppreflight

import (
	"meshify/internal/appconfig"
	"meshify/internal/preflight"
	"strings"
	"testing"
)

func TestBuildReportChecksAuthKeyFileReadiness(t *testing.T) {
	t.Parallel()

	cfg := appconfig.New()
	cfg.App.Name = "tailapp"
	cfg.App.Domains = []string{"tailapp.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Upstream = "100.64.10.20:18001"
	cfg.Tailscale.AuthKeyFile = "/run/meshify/auth.key"
	report := BuildReport(cfg, Inputs{
		Permissions:                 preflight.PermissionState{IsRoot: true},
		DNS:                         map[string]preflight.DNSProbe{"tailapp.example.com": {Host: "tailapp.example.com", ResolvedIPs: []string{"8.8.8.8"}, ExpectedIPv4: "8.8.8.8"}},
		Ports:                       availableAppPorts(),
		ServiceBinaryOK:             true,
		TailscaleRequired:           true,
		TailscaleAuthKeyFile:        cfg.Tailscale.AuthKeyFile,
		TailscaleAuthKeyFileChecked: true,
		TailscaleAuthKeyFileReady:   false,
		TailscaleAuthKeyFileDetail:  "tailscale auth key file must be root-only",
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want auth key file readiness failure")
	}
	if got := checkSummary(report, "tailscale-auth-key-file"); !strings.Contains(got, "root-only") {
		t.Fatalf("tailscale-auth-key-file summary = %q, want readiness detail", got)
	}

	report = BuildReport(cfg, Inputs{
		Permissions: preflight.PermissionState{IsRoot: true},
		DNS: map[string]preflight.DNSProbe{"tailapp.example.com": {
			Host: "tailapp.example.com", ResolvedIPs: []string{"8.8.8.8"}, ExpectedIPv4: "8.8.8.8",
		}},
		Ports:                       availableAppPorts(),
		ServiceBinaryOK:             true,
		TailscaleRequired:           true,
		TailscaleAuthKeyFile:        cfg.Tailscale.AuthKeyFile,
		TailscaleAuthKeyFileChecked: true,
		TailscaleAuthKeyFileReady:   true,
		TailscaleAuthKeyFileDetail:  "tailscale.auth_key_file 已通过 root-only 校验",
	})
	if report.FailedCount() != 0 {
		t.Fatalf("FailedCount() = %d, want auth key file readiness pass", report.FailedCount())
	}
}

func TestBuildReportRequiresCurrentRootForAppDeploy(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	report := BuildReport(cfg, Inputs{
		Permissions:     preflight.PermissionState{User: "deploy", SudoWorks: true},
		DNS:             validDNS(cfg),
		Ports:           availableAppPorts(),
		ServiceBinaryOK: true,
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want non-root failure even when sudo works")
	}
	if got := checkSummary(report, "permissions"); !strings.Contains(got, "root 权限") {
		t.Fatalf("permissions summary = %q, want root-only app deploy requirement", got)
	}
}

func TestBuildReportChecksServiceEnvFileReadiness(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	cfg.Service.EnvFile = "/opt/app/web.env"
	report := BuildReport(cfg, Inputs{
		Permissions:           preflight.PermissionState{IsRoot: true},
		DNS:                   validDNS(cfg),
		Ports:                 availableAppPorts(),
		ServiceBinaryOK:       true,
		ServiceEnvFile:        cfg.Service.EnvFile,
		ServiceEnvFileChecked: true,
		ServiceEnvFileReady:   false,
		ServiceEnvFileDetail:  "service.env_file must be root-only",
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want service env file readiness failure")
	}
	if got := checkSummary(report, "service-env-file"); !strings.Contains(got, "root-only") {
		t.Fatalf("service-env-file summary = %q, want readiness detail", got)
	}

	report = BuildReport(cfg, Inputs{
		Permissions:           preflight.PermissionState{IsRoot: true},
		DNS:                   validDNS(cfg),
		Ports:                 availableAppPorts(),
		ServiceBinaryOK:       true,
		ServiceEnvFile:        cfg.Service.EnvFile,
		ServiceEnvFileChecked: true,
		ServiceEnvFileReady:   true,
		ServiceEnvFileDetail:  "service.env_file 已通过 root-only 校验",
	})
	if report.FailedCount() != 0 {
		t.Fatalf("FailedCount() = %d, want service env file readiness pass", report.FailedCount())
	}
}

func TestBuildReportRejectsDNSWithoutPublicOrExpectedAddress(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	report := BuildReport(cfg, Inputs{
		Permissions:     preflight.PermissionState{IsRoot: true},
		DNS:             map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", ResolvedIPs: []string{"127.0.0.1"}}},
		Ports:           availableAppPorts(),
		ServiceBinaryOK: true,
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want private DNS failure")
	}

	report = BuildReport(cfg, Inputs{
		Permissions: preflight.PermissionState{IsRoot: true},
		DNS: map[string]preflight.DNSProbe{"app.example.com": {
			Host:         "app.example.com",
			ResolvedIPs:  []string{"8.8.8.8"},
			ExpectedIPv4: "1.1.1.1",
		}},
		Ports:           availableAppPorts(),
		ServiceBinaryOK: true,
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want missing expected DNS failure")
	}

	report = BuildReport(cfg, Inputs{
		Permissions:     preflight.PermissionState{IsRoot: true},
		DNS:             map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", ResolvedIPs: []string{"8.8.8.8"}}},
		Ports:           availableAppPorts(),
		ServiceBinaryOK: true,
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want HTTP-01 unproven host alignment failure")
	}
}

func TestBuildReportWarnsWhenDNSHostAlignmentIsUnproven(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.DNS01.Provider = "route53"
	report := BuildReport(cfg, Inputs{
		Permissions:           preflight.PermissionState{IsRoot: true},
		DNS:                   map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", ResolvedIPs: []string{"8.8.8.8"}}},
		Ports:                 availableAppPorts(),
		ServiceBinaryOK:       true,
		DNSCredentialsChecked: true,
		DNSCredentialsReady:   true,
	})
	if report.FailedCount() != 0 {
		t.Fatalf("FailedCount() = %d, want warning-only DNS uncertainty", report.FailedCount())
	}
	if got := checkSummary(report, "dns:app.example.com"); !strings.Contains(got, "未能确认") {
		t.Fatalf("dns summary = %q, want unproven host alignment warning", got)
	}
}

func TestBuildReportDoesNotBlockDNS01WhenPublicDNSIsNotReady(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.DNS01.Provider = "route53"

	tests := []struct {
		name string
		dns  map[string]preflight.DNSProbe
		want string
	}{
		{
			name: "lookup missing",
			dns:  map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", LookupError: "no such host"}},
			want: "DNS 解析未确认",
		},
		{
			name: "private only",
			dns:  map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", ResolvedIPs: []string{"127.0.0.1"}}},
			want: "未解析到公网可路由地址",
		},
		{
			name: "expected mismatch",
			dns:  map[string]preflight.DNSProbe{"app.example.com": {Host: "app.example.com", ResolvedIPs: []string{"8.8.8.8"}, ExpectedIPv4: "1.1.1.1"}},
			want: "缺少期望地址",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report := BuildReport(cfg, Inputs{
				Permissions:           preflight.PermissionState{IsRoot: true},
				DNS:                   tt.dns,
				Ports:                 availableAppPorts(),
				ServiceBinaryOK:       true,
				DNSCredentialsChecked: true,
				DNSCredentialsReady:   true,
			})
			if report.FailedCount() != 0 {
				t.Fatalf("FailedCount() = %d, want DNS-01 warning only", report.FailedCount())
			}
			if got := checkSummary(report, "dns:app.example.com"); !strings.Contains(got, tt.want) || !strings.Contains(got, "不因此阻断部署") {
				t.Fatalf("dns summary = %q, want warning containing %q", got, tt.want)
			}
		})
	}
}

func TestBuildReportChecksAppPorts(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	report := BuildReport(cfg, Inputs{
		Permissions:     preflight.PermissionState{IsRoot: true},
		DNS:             validDNS(cfg),
		Ports:           []preflight.PortBinding{{Port: 80, Protocol: "tcp", InUse: true, Process: "caddy"}, {Port: 443, Protocol: "tcp"}},
		ServiceBinaryOK: true,
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want non-nginx port conflict")
	}

	report = BuildReport(cfg, Inputs{
		Permissions:     preflight.PermissionState{IsRoot: true},
		DNS:             validDNS(cfg),
		Ports:           []preflight.PortBinding{{Port: 80, Protocol: "tcp", InUse: true, Process: "nginx"}, {Port: 443, Protocol: "tcp"}},
		ServiceBinaryOK: true,
	})
	if report.FailedCount() != 0 {
		t.Fatalf("FailedCount() = %d, want nginx coexistence warning only", report.FailedCount())
	}
}

func TestBuildReportChecksDNS01Credentials(t *testing.T) {
	t.Parallel()

	cfg := validAppConfig()
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.DNS01.Provider = "cloudflare"
	cfg.DNS01.EnvFile = "/etc/example-app/dns/cloudflare.env"

	report := BuildReport(cfg, Inputs{
		Permissions:           preflight.PermissionState{IsRoot: true},
		DNS:                   validDNS(cfg),
		Ports:                 availableAppPorts(),
		ServiceBinaryOK:       true,
		DNSCredentialsChecked: true,
		DNSCredentialsReady:   false,
		DNSCredentialsDetail:  "env_file contains raw DNS token",
	})
	if report.FailedCount() == 0 {
		t.Fatal("FailedCount() = 0, want DNS credentials failure")
	}

	report = BuildReport(cfg, Inputs{
		Permissions:           preflight.PermissionState{IsRoot: true},
		DNS:                   validDNS(cfg),
		Ports:                 availableAppPorts(),
		ServiceBinaryOK:       true,
		DNSCredentialsChecked: true,
		DNSCredentialsReady:   true,
		DNSCredentialsDetail:  "DNS-01 provider env_file 已通过校验",
	})
	if report.FailedCount() != 0 {
		t.Fatalf("FailedCount() = %d, want DNS credentials pass", report.FailedCount())
	}
}

func validAppConfig() appconfig.Config {
	cfg := appconfig.New()
	cfg.App.Name = "app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/app/app --listen 127.0.0.1:18001"
	return cfg
}

func validDNS(cfg appconfig.Config) map[string]preflight.DNSProbe {
	return map[string]preflight.DNSProbe{
		cfg.App.Domains[0]: {Host: cfg.App.Domains[0], ResolvedIPs: []string{"8.8.8.8"}, ExpectedIPv4: "8.8.8.8"},
	}
}

func availableAppPorts() []preflight.PortBinding {
	return []preflight.PortBinding{
		{Port: 80, Protocol: "tcp"},
		{Port: 443, Protocol: "tcp"},
	}
}

func checkSummary(report Report, id string) string {
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Summary
		}
	}
	return ""
}
