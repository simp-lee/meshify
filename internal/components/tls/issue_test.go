package tls

import (
	"meshify/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCertificatePlanHTTP01UsesLegoWebrootAndRunHook(t *testing.T) {
	t.Parallel()

	plan, err := NewCertificatePlan(validConfig())
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	if plan.Command.Name != LegoBinaryPath {
		t.Fatalf("Command.Name = %q, want %q", plan.Command.Name, LegoBinaryPath)
	}
	args := strings.Join(plan.Command.Args, " ")
	for _, want := range []string{
		"--path /var/lib/meshify/lego",
		"--email ops@example.com",
		"--domains hs.example.com",
		"--accept-tos",
		"--http --http.webroot /var/lib/meshify/acme-challenges",
		"run --run-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args = %q, want %q", args, want)
		}
	}
	if plan.Fullchain != "/etc/meshify/tls/hs.example.com/fullchain.pem" {
		t.Fatalf("Fullchain = %q", plan.Fullchain)
	}
}

func TestNewCertificatePlanDNS01UsesProviderAndEnvFileWrapper(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/cloudflare.env"

	plan, err := NewCertificatePlan(cfg)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	if plan.Command.Name != "sh" {
		t.Fatalf("Command.Name = %q, want env-file shell wrapper", plan.Command.Name)
	}
	if plan.Command.DisplayName != LegoBinaryPath {
		t.Fatalf("DisplayName = %q, want %q", plan.Command.DisplayName, LegoBinaryPath)
	}
	args := strings.Join(plan.Command.DisplayArgs, " ")
	for _, want := range []string{
		"--path /var/lib/meshify/lego",
		"--dns cloudflare",
		"run --run-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("args = %q, want %q", args, want)
		}
	}
	wrapper := strings.Join(plan.Command.Args, " ")
	for _, want := range []string{
		"while IFS= read -r line",
		`line=$(trim_meshify_env_value "$line")`,
		`""|"#"*|";"*) continue ;;`,
		`export "$key=$value"`,
	} {
		if !strings.Contains(wrapper, want) {
			t.Fatalf("wrapper = %q, want safe env_file parser fragment %q", wrapper, want)
		}
	}
	for _, unwanted := range []string{`. "$env_file"`, `source "$env_file"`} {
		if strings.Contains(wrapper, unwanted) {
			t.Fatalf("wrapper = %q, must not execute env_file through %q", wrapper, unwanted)
		}
	}
	if strings.Contains(plan.Command.String(), "/etc/meshify/dns01/cloudflare.env") {
		t.Fatalf("Command.String() = %q, want env_file hidden from display", plan.Command.String())
	}
}

func TestNewCertificatePlanDNS01AllowsRoute53AmbientCredentials(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "route53"

	plan, err := NewCertificatePlan(cfg)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	if plan.Command.Name != LegoBinaryPath {
		t.Fatalf("Command.Name = %q, want direct lego command for ambient credentials", plan.Command.Name)
	}
	args := strings.Join(plan.Command.Args, " ")
	if !strings.Contains(args, "--dns route53") {
		t.Fatalf("args = %q, want route53 DNS provider", args)
	}
}

func TestNewCertificatePlanDNS01RequiresProviderEnvFile(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"

	_, err := NewCertificatePlan(cfg)
	if err == nil {
		t.Fatal("NewCertificatePlan() error = nil, want missing env file failure")
	}
	if !strings.Contains(err.Error(), "advanced.dns01.env_file") {
		t.Fatalf("error = %q, want env_file failure", err.Error())
	}
}

func TestNewCertificatePlanDNS01RejectsRoute53CredentialsFile(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "route53"
	cfg.Advanced.DNS01.CredentialsFile = "/root/.aws/credentials"

	_, err := NewCertificatePlan(cfg)
	if err == nil {
		t.Fatal("NewCertificatePlan() error = nil, want unsupported credentials_file failure")
	}
	if !strings.Contains(err.Error(), "advanced.dns01.credentials_file is no longer supported") {
		t.Fatalf("error = %q, want unsupported credentials_file failure", err.Error())
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

func TestCanonicalDNSProviderUsesLegoProviderCodes(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"cloudflare":     "cloudflare",
		"route53":        "route53",
		"digitalocean":   "digitalocean",
		"google":         "gcloud",
		"gcloud":         "gcloud",
		"  cloudflare  ": "cloudflare",
	}
	for provider, want := range tests {
		provider := provider
		want := want
		t.Run(provider, func(t *testing.T) {
			t.Parallel()

			got, err := CanonicalDNSProvider(provider)
			if err != nil {
				t.Fatalf("CanonicalDNSProvider() error = %v", err)
			}
			if got != want {
				t.Fatalf("CanonicalDNSProvider() = %q, want %q", got, want)
			}
		})
	}
}

func TestHTTP01BootstrapCommandsPrepareWebrootAndTemporaryCertificate(t *testing.T) {
	t.Parallel()

	commands := HTTP01BootstrapCommands("hs.example.com")
	if len(commands) != 2 {
		t.Fatalf("len(commands) = %d, want 2", len(commands))
	}
	firstArgs := strings.Join(commands[0].Args, " ")
	for _, want := range []string{"/var/lib/meshify/acme-challenges", "/var/lib/meshify/lego", "/etc/meshify/tls/hs.example.com"} {
		if commands[0].Name != "mkdir" || !strings.Contains(firstArgs, want) {
			t.Fatalf("first command = %#v, want mkdir for %s", commands[0], want)
		}
	}
	if commands[1].Name != "sh" || !strings.Contains(strings.Join(commands[1].Args, " "), "openssl req -x509") {
		t.Fatalf("second command = %#v, want temporary certificate shell guard", commands[1])
	}
}

func TestValidateRenewalServiceAcceptsRenderedHTTP01Service(t *testing.T) {
	t.Parallel()

	content := []byte(`[Unit]
Wants=network-online.target
Requires=nginx.service
After=network-online.target nginx.service

[Service]
Type=oneshot
ExecStart=/opt/meshify/bin/lego --path /var/lib/meshify/lego --email ops@example.com --domains hs.example.com --http --http.webroot /var/lib/meshify/acme-challenges renew --renew-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh
`)
	if err := ValidateRenewalService(content); err != nil {
		t.Fatalf("ValidateRenewalService() error = %v", err)
	}
}

func TestValidateRenewalServiceAcceptsRenderedDNS01Service(t *testing.T) {
	t.Parallel()

	content := []byte(`[Unit]
Wants=network-online.target
Requires=nginx.service
After=network-online.target nginx.service

[Service]
Type=oneshot
EnvironmentFile=/etc/meshify/dns01/cloudflare.env
ExecStart=/opt/meshify/bin/lego --path /var/lib/meshify/lego --email ops@example.com --domains hs.example.com --dns cloudflare renew --renew-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh
`)
	if err := ValidateRenewalService(content); err != nil {
		t.Fatalf("ValidateRenewalService() error = %v", err)
	}
}

func TestValidateRenewalServiceAcceptsDNS01WithAmbientCredentials(t *testing.T) {
	t.Parallel()

	content := []byte(`[Unit]
Wants=network-online.target
Requires=nginx.service
After=network-online.target nginx.service

[Service]
Type=oneshot
ExecStart=/opt/meshify/bin/lego --path /var/lib/meshify/lego --email ops@example.com --domains hs.example.com --dns route53 renew --renew-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh
`)
	if err := ValidateRenewalService(content); err != nil {
		t.Fatalf("ValidateRenewalService() error = %v", err)
	}
}

func TestValidateRenewalServiceRejectsMissingChallengeFlags(t *testing.T) {
	t.Parallel()

	content := []byte(`[Unit]
Wants=network-online.target
Requires=nginx.service
After=network-online.target nginx.service

[Service]
Type=oneshot
ExecStart=/opt/meshify/bin/lego --path /var/lib/meshify/lego --email ops@example.com --domains hs.example.com renew --renew-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh
`)
	err := ValidateRenewalService(content)
	if err == nil {
		t.Fatal("ValidateRenewalService() error = nil, want missing challenge flag failure")
	}
	if !strings.Contains(err.Error(), "missing ACME challenge flag") {
		t.Fatalf("error = %q, want missing challenge flag failure", err.Error())
	}
}

func TestValidateRenewalServiceRejectsMissingNginxRequirement(t *testing.T) {
	t.Parallel()

	content := []byte(`[Unit]
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
ExecStart=/opt/meshify/bin/lego --path /var/lib/meshify/lego --email ops@example.com --domains hs.example.com --http --http.webroot /var/lib/meshify/acme-challenges renew --renew-hook /usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh
`)
	err := ValidateRenewalService(content)
	if err == nil {
		t.Fatal("ValidateRenewalService() error = nil, want Nginx requirement failure")
	}
	if !strings.Contains(err.Error(), "Nginx requirement") {
		t.Fatalf("error = %q, want Nginx requirement failure", err.Error())
	}
}

func TestValidateRenewalTimerAcceptsLegoRenewTimer(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "templates", "etc", "systemd", "system", "meshify-lego-renew.timer"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := ValidateRenewalTimer(content); err != nil {
		t.Fatalf("ValidateRenewalTimer() error = %v", err)
	}
}

func TestValidateReloadHookAcceptsDeployHook(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "templates", "usr", "local", "lib", "meshify", "hooks", "install-lego-cert-and-reload-nginx.sh"))
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

func TestValidateReloadHookRejectsReloadOnlyLegacyHook(t *testing.T) {
	t.Parallel()

	content := []byte(`#!/bin/sh
set -eu
nginx -t
systemctl reload nginx
nginx -s reload
`)
	err := ValidateReloadHook(content)
	if err == nil {
		t.Fatal("ValidateReloadHook() error = nil, want missing lego certificate install contract")
	}
	if !strings.Contains(err.Error(), "LEGO_CERT_PATH") || !strings.Contains(err.Error(), "fullchain install") {
		t.Fatalf("error = %q, want lego hook contract failure", err.Error())
	}
}

func TestValidateReloadHookRejectsLetsEncryptHookAssumptions(t *testing.T) {
	t.Parallel()

	content := []byte(`#!/bin/sh
set -eu
: "${LEGO_CERT_DOMAIN:?}"
: "${LEGO_CERT_PATH:?}"
: "${LEGO_CERT_KEY_PATH:?}"
target_dir="/etc/meshify/tls/$LEGO_CERT_DOMAIN"
install -d -m 0755 "$target_dir"
install -m 0644 "$LEGO_CERT_PATH" "$target_dir/fullchain.pem"
install -m 0600 "$LEGO_CERT_KEY_PATH" "$target_dir/privkey.pem"
legacy_dir="/etc/letsencrypt/live/$LEGO_CERT_DOMAIN"
nginx -t
systemctl reload nginx
nginx -s reload
`)
	err := ValidateReloadHook(content)
	if err == nil {
		t.Fatal("ValidateReloadHook() error = nil, want stale letsencrypt path failure")
	}
	if !strings.Contains(err.Error(), "must not reference Certbot or /etc/letsencrypt") {
		t.Fatalf("error = %q, want stale letsencrypt path failure", err.Error())
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
