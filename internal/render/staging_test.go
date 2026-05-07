package render

import (
	"bytes"
	"meshify/internal/assets"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStageRuntimeBuildsRuntimeOutputs(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.Network.PublicIPv4 = "203.0.113.10"

	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	if len(staged) != 6 {
		t.Fatalf("len(StageRuntime()) = %d, want 6", len(staged))
	}

	bySource := make(map[string]StagedFile, len(staged))
	for _, file := range staged {
		bySource[file.SourcePath] = file
	}

	headscale := bySource["templates/etc/headscale/config.yaml.tmpl"]
	if headscale.HostPath != "/etc/headscale/config.yaml" {
		t.Fatalf("HostPath = %q, want %q", headscale.HostPath, "/etc/headscale/config.yaml")
	}
	if headscale.Mode != 0o644 {
		t.Fatalf("Mode = %v, want %v", headscale.Mode, 0o644)
	}
	if !bytes.Contains(headscale.Content, []byte(`server_url: "https://hs.example.com"`)) {
		t.Fatalf("headscale content missing rendered server_url\n%s", string(headscale.Content))
	}

	nginx := bySource["templates/etc/nginx/sites-available/headscale.conf.tmpl"]
	if nginx.HostPath != "/etc/nginx/sites-available/headscale.conf" {
		t.Fatalf("HostPath = %q, want %q", nginx.HostPath, "/etc/nginx/sites-available/headscale.conf")
	}
	if len(nginx.Activations) != 1 || nginx.Activations[0] != assets.ActivationReloadNginx {
		t.Fatalf("Activations = %v, want [%q]", nginx.Activations, assets.ActivationReloadNginx)
	}
	if !bytes.Contains(nginx.Content, []byte("server_name hs.example.com;")) {
		t.Fatalf("nginx content missing rendered server_name\n%s", string(nginx.Content))
	}

	policy := bySource["templates/etc/headscale/policy.hujson"]
	wantPolicy, err := os.ReadFile(filepath.Join("..", "..", "deploy", "templates", "etc", "headscale", "policy.hujson"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(policy.Content, wantPolicy) {
		t.Fatal("policy content mismatch")
	}

	hook := bySource["templates/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh"]
	if hook.HostPath != "/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh" {
		t.Fatalf("HostPath = %q, want %q", hook.HostPath, "/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh")
	}
	if hook.Mode != 0o755 {
		t.Fatalf("Mode = %v, want %v", hook.Mode, 0o755)
	}
	if len(hook.Activations) != 0 {
		t.Fatalf("Activations = %v, want none", hook.Activations)
	}

	renewService := bySource["templates/etc/systemd/system/meshify-lego-renew.service.tmpl"]
	if renewService.HostPath != "/etc/systemd/system/meshify-lego-renew.service" {
		t.Fatalf("HostPath = %q, want %q", renewService.HostPath, "/etc/systemd/system/meshify-lego-renew.service")
	}
	if !bytes.Contains(renewService.Content, []byte("Requires=nginx.service")) {
		t.Fatalf("renew service content missing Nginx requirement\n%s", string(renewService.Content))
	}
	if !bytes.Contains(renewService.Content, []byte("/opt/meshify/bin/lego --path /var/lib/meshify/lego")) {
		t.Fatalf("renew service content missing lego command\n%s", string(renewService.Content))
	}
	if !bytes.Contains(renewService.Content, []byte("--http --http.webroot /var/lib/meshify/acme-challenges")) {
		t.Fatalf("renew service content missing HTTP-01 webroot\n%s", string(renewService.Content))
	}

	renewTimer := bySource["templates/etc/systemd/system/meshify-lego-renew.timer"]
	if renewTimer.HostPath != "/etc/systemd/system/meshify-lego-renew.timer" {
		t.Fatalf("HostPath = %q, want %q", renewTimer.HostPath, "/etc/systemd/system/meshify-lego-renew.timer")
	}
	if !bytes.Contains(renewTimer.Content, []byte("RandomizedDelaySec=1h")) {
		t.Fatalf("renew timer content missing randomized delay\n%s", string(renewTimer.Content))
	}
}

func TestStageRuntimeRendersDNSRenewalEnvironmentFile(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = "dns-01"
	cfg.Advanced.DNS01.Provider = "cloudflare"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/cloudflare.env"

	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	bySource := make(map[string]StagedFile, len(staged))
	for _, file := range staged {
		bySource[file.SourcePath] = file
	}
	renewService := bySource["templates/etc/systemd/system/meshify-lego-renew.service.tmpl"]
	text := string(renewService.Content)
	for _, want := range []string{
		"EnvironmentFile=/etc/meshify/dns01/cloudflare.env",
		"--dns cloudflare renew --renew-hook",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("renew service content missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "--http.webroot") {
		t.Fatalf("renew service content = %q, want DNS renewal without HTTP webroot", text)
	}
}

func TestStageRuntimeRendersCanonicalDNSProviderForRenewal(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Default.ACMEChallenge = "dns-01"
	cfg.Advanced.DNS01.Provider = "google"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/gcloud.env"

	staged, err := StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	bySource := make(map[string]StagedFile, len(staged))
	for _, file := range staged {
		bySource[file.SourcePath] = file
	}
	renewService := bySource["templates/etc/systemd/system/meshify-lego-renew.service.tmpl"]
	text := string(renewService.Content)
	if !strings.Contains(text, "--dns gcloud renew --renew-hook") {
		t.Fatalf("renew service content missing canonical gcloud provider\n%s", text)
	}
	if !strings.Contains(text, "EnvironmentFile=/etc/meshify/dns01/gcloud.env") {
		t.Fatalf("renew service content missing DNS env file\n%s", text)
	}
	if strings.Contains(text, "--dns google") {
		t.Fatalf("renew service content uses non-lego alias\n%s", text)
	}
}
