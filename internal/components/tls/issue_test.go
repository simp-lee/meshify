package tls

import (
	"meshify/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCertificatePlanHTTP01UsesWebrootAndDeployHook(t *testing.T) {
	t.Parallel()

	plan, err := NewCertificatePlan(validConfig())
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	args := strings.Join(plan.Command.Args, " ")
	for _, want := range []string{
		"certonly",
		"--non-interactive",
		"--agree-tos",
		"--email ops@example.com",
		"--cert-name hs.example.com",
		"-d hs.example.com",
		"--deploy-hook /etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh",
		"--webroot --webroot-path /var/www/certbot",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args = %q, want %q", args, want)
		}
	}
	if plan.Fullchain != "/etc/letsencrypt/live/hs.example.com/fullchain.pem" {
		t.Fatalf("Fullchain = %q", plan.Fullchain)
	}
}

func TestNewCertificatePlanDNS01UsesProviderAuthenticator(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"
	cfg.Advanced.DNS01.Zone = "example.com"

	plan, err := NewCertificatePlan(cfg)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	args := strings.Join(plan.Command.Args, " ")
	for _, want := range []string{"--authenticator dns-cloudflare", "--preferred-challenges dns"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args = %q, want %q", args, want)
		}
	}
}

func TestNewCertificatePlanDNS01RejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "example-provider"

	_, err := NewCertificatePlan(cfg)
	if err == nil {
		t.Fatal("NewCertificatePlan() error = nil, want unsupported provider failure")
	}
	if !strings.Contains(err.Error(), "unsupported DNS-01 provider") {
		t.Fatalf("error = %q, want unsupported provider failure", err.Error())
	}
}

func TestDNSPluginNameCanonicalizesSupportedAliases(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"cloudflare":     "dns-cloudflare",
		"dns-cloudflare": "dns-cloudflare",
		"aws":            "dns-route53",
		"route53":        "dns-route53",
		"digitalocean":   "dns-digitalocean",
		"do":             "dns-digitalocean",
		"google":         "dns-google",
		"gcloud":         "dns-google",
		"azure":          "dns-azure",
		"DNS_CLOUDFLARE": "dns-cloudflare",
		"  cloudflare  ": "dns-cloudflare",
	}
	for provider, want := range tests {
		provider := provider
		want := want
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			got, err := DNSPluginName(provider)
			if err != nil {
				t.Fatalf("DNSPluginName() error = %v", err)
			}
			if got != want {
				t.Fatalf("DNSPluginName() = %q, want %q", got, want)
			}
		})
	}
}

func TestRenewDryRunCommandCanRunDeployHooksWhenAvailable(t *testing.T) {
	t.Parallel()

	command := RenewDryRunCommand(true)
	if got := strings.Join(command.Args, " "); got != "renew --dry-run --no-random-sleep-on-renew --run-deploy-hooks" {
		t.Fatalf("args = %q", got)
	}
}

func TestHTTP01BootstrapCommandsPrepareWebrootAndTemporaryCertificate(t *testing.T) {
	t.Parallel()

	commands := HTTP01BootstrapCommands("hs.example.com")
	if len(commands) != 3 {
		t.Fatalf("len(commands) = %d, want 3", len(commands))
	}
	if commands[0].Name != "mkdir" || !strings.Contains(strings.Join(commands[0].Args, " "), "/var/www/certbot") {
		t.Fatalf("first command = %#v, want webroot mkdir", commands[0])
	}
	if commands[2].Name != "sh" || !strings.Contains(strings.Join(commands[2].Args, " "), "openssl req -x509") {
		t.Fatalf("third command = %#v, want temporary certificate shell guard", commands[2])
	}
}

func TestValidateReloadHookAcceptsDeployHook(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "templates", "etc", "letsencrypt", "renewal-hooks", "deploy", "reload-nginx.sh"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := ValidateReloadHook(content); err != nil {
		t.Fatalf("ValidateReloadHook() error = %v", err)
	}
}

func TestValidateReloadHookRejectsCommentOnlyCommands(t *testing.T) {
	t.Parallel()

	content := []byte(`#!/bin/sh
set -eu
# nginx -t
# systemctl reload nginx
# nginx -s reload
`)
	err := ValidateReloadHook(content)
	if err == nil {
		t.Fatal("ValidateReloadHook() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "reload hook missing nginx -t") {
		t.Fatalf("error = %q, want active command failure", err.Error())
	}
}

func TestValidateReloadHookRequiresTestBeforeReload(t *testing.T) {
	t.Parallel()

	content := []byte(`#!/bin/sh
set -eu
systemctl reload nginx
nginx -t
nginx -s reload
`)
	err := ValidateReloadHook(content)
	if err == nil {
		t.Fatal("ValidateReloadHook() error = nil, want ordering failure")
	}
	if !strings.Contains(err.Error(), "before systemctl reload nginx") {
		t.Fatalf("error = %q, want ordering failure", err.Error())
	}
}

func validConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
