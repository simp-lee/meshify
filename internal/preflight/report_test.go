package preflight

import (
	"meshify/internal/config"
	"strings"
	"testing"
)

func TestBuildReportDoesNotBlockOnAdvisoryManualChecklist(t *testing.T) {
	t.Parallel()

	report := BuildReport(config.ExampleConfig(), Inputs{
		Permissions: PermissionState{User: "deployer", SudoWorks: true},
		Platform:    PlatformInfo{ID: "debian", VersionID: "13", PrettyName: "Debian GNU/Linux 13"},
		DNS:         DNSProbe{Host: "hs.example.com", ResolvedIPs: []string{"8.8.8.8"}},
		Ports: []PortBinding{
			{Port: 80, Protocol: "tcp", InUse: false},
			{Port: 443, Protocol: "tcp", InUse: false},
			{Port: 3478, Protocol: "udp", InUse: false},
		},
		Firewall: FirewallState{Inspected: true, Active: true, AllowedPorts: []string{"80/tcp", "443/tcp", "3478/udp"}},
		Services: []ServiceState{},
		PackageSource: PackageSourceState{
			Mode:                config.PackageSourceModeDirect,
			Version:             config.DefaultHeadscaleVersion,
			ExpectedSHA256:      strings.Repeat("a", 64),
			ReachabilityChecked: true,
			Reachable:           true,
			IntegrityChecked:    true,
			ActualSHA256:        strings.Repeat("a", 64),
		},
		ACME: ACMEState{HTTP01Checked: true, HTTP01Ready: true},
	})

	if len(report.ManualChecklists) == 0 {
		t.Fatal("BuildReport() manual checklists = empty, want advisory checklist output")
	}
	if report.ManualCount() != 0 {
		t.Fatalf("BuildReport() manual count = %d, want 0 blocking manual checks", report.ManualCount())
	}
	if report.OverallStatus() != StatusPass {
		t.Fatalf("BuildReport() overall status = %q, want %q", report.OverallStatus(), StatusPass)
	}
	if report.Summary() != "all checks passed" {
		t.Fatalf("BuildReport() summary = %q, want %q", report.Summary(), "all checks passed")
	}
}
