package workflow

import (
	"fmt"
	"meshify/internal/config"
)

type DeployStep struct {
	Key         string
	Description string
}

type DeployPlan struct {
	Steps []DeployStep
}

func NewDeployPlan(cfg config.Config) (DeployPlan, error) {
	if err := cfg.Validate(); err != nil {
		return DeployPlan{}, err
	}
	steps := []DeployStep{
		{Key: "preflight", Description: "Run blocking host and network preflight checks before system writes."},
		{Key: "install-host-dependencies", Description: "Install Nginx, certbot and any selected DNS-01 plugin through the host package manager."},
		{Key: "install-headscale-package", Description: "Install the verified Headscale v0.28.0 .deb using the official systemd unit."},
		{Key: "render-runtime-assets", Description: "Render Headscale, Nginx and renewal-hook runtime assets from deploy/."},
		{Key: "install-runtime-assets", Description: "Write Headscale config, policy, Nginx site and renewal hook to host paths."},
		{Key: "issue-certificate", Description: certificateDescription(cfg)},
		{Key: "enable-services", Description: "Reload systemd, enable Headscale and Nginx, and restart affected services."},
		{Key: "onboarding", Description: "Create the first local user and preauthkey through Headscale local CLI management."},
		{Key: "verify", Description: "Run static and host readiness checks before directing users to client onboarding."},
	}
	return DeployPlan{Steps: steps}, nil
}

func (plan DeployPlan) Keys() []string {
	keys := make([]string, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		keys = append(keys, step.Key)
	}
	return keys
}

func (plan DeployPlan) Summary() string {
	if len(plan.Steps) == 0 {
		return "deploy plan has no steps"
	}
	return fmt.Sprintf("deploy plan contains %d ordered steps from preflight through verification", len(plan.Steps))
}

func certificateDescription(cfg config.Config) string {
	switch cfg.Default.ACMEChallenge {
	case config.ACMEChallengeDNS01:
		return "Issue or renew the certificate using DNS-01 with externally supplied provider credentials."
	default:
		return "Issue or renew the certificate using HTTP-01 webroot without stopping Nginx."
	}
}
