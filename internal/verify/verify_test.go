package verify

import (
	"meshify/internal/assets"
	"meshify/internal/config"
	"meshify/internal/render"
	"strings"
	"testing"
)

func TestStaticReportPassesRenderedRuntimeAssets(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	staged, err := render.StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	report := StaticReport(cfg, staged)
	if report.Status() != StatusPass {
		t.Fatalf("Status() = %q; checks = %#v", report.Status(), report.Checks)
	}
	if !strings.Contains(SummarizeChecks(report.Checks), "pass:client-version") {
		t.Fatalf("SummarizeChecks() = %q, want client version check", SummarizeChecks(report.Checks))
	}
}

func TestStaticReportFailsWhenRuntimeAssetMissing(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	staged, err := render.StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	filtered := staged[:0]
	for _, file := range staged {
		if file.SourcePath != "templates/etc/headscale/policy.hujson" {
			filtered = append(filtered, file)
		}
	}
	report := StaticReport(cfg, filtered)
	if report.Status() != StatusFail {
		t.Fatalf("Status() = %q, want fail", report.Status())
	}
	if !strings.Contains(report.Summary(), "verification checks failed") {
		t.Fatalf("Summary() = %q", report.Summary())
	}
}

func TestRequiredActivationsDeduplicatesRuntimeActions(t *testing.T) {
	t.Parallel()

	staged := []render.StagedFile{
		{Activations: []assets.Activation{assets.ActivationRestartHeadscale}},
		{Activations: []assets.Activation{assets.ActivationRestartHeadscale, assets.ActivationReloadNginx}},
	}
	activations := RequiredActivations(staged)
	if len(activations) != 2 {
		t.Fatalf("len(activations) = %d, want 2", len(activations))
	}
}

func validConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
