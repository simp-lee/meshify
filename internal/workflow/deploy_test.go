package workflow

import (
	"meshify/internal/config"
	"slices"
	"strings"
	"testing"
)

func TestNewDeployPlanOrdersHappyPathThroughVerification(t *testing.T) {
	t.Parallel()

	plan, err := NewDeployPlan(validDeployConfig())
	if err != nil {
		t.Fatalf("NewDeployPlan() error = %v", err)
	}
	keys := plan.Keys()
	for _, want := range []string{
		"preflight",
		"render-runtime-assets",
		"install-headscale-package",
		"install-runtime-assets",
		"issue-certificate",
		"enable-services",
		"onboarding",
		"verify",
	} {
		if !slices.Contains(keys, want) {
			t.Fatalf("keys = %v, want %q", keys, want)
		}
	}
	if !strings.Contains(plan.Summary(), "ordered steps") {
		t.Fatalf("Summary() = %q", plan.Summary())
	}
}

func TestNewDeployPlanDescribesDNS01WhenConfigured(t *testing.T) {
	t.Parallel()

	cfg := validDeployConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"
	plan, err := NewDeployPlan(cfg)
	if err != nil {
		t.Fatalf("NewDeployPlan() error = %v", err)
	}
	found := false
	for _, step := range plan.Steps {
		if step.Key == "issue-certificate" && strings.Contains(step.Description, "DNS-01") {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan.Steps = %#v, want DNS-01 certificate step", plan.Steps)
	}
}

func validDeployConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
