package render

import (
	"bytes"
	"meshify/internal/assets"
	"os"
	"path/filepath"
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
	if len(staged) != 4 {
		t.Fatalf("len(StageRuntime()) = %d, want 4", len(staged))
	}

	bySource := make(map[string]StagedFile, len(staged))
	for _, file := range staged {
		bySource[file.SourcePath] = file
	}

	headscale := bySource["templates/etc/headscale/config.yaml.tmpl"]
	if headscale.HostPath != "/etc/headscale/config.yaml" {
		t.Fatalf("HostPath = %q, want %q", headscale.HostPath, "/etc/headscale/config.yaml")
	}
	if headscale.Mode != 0o600 {
		t.Fatalf("Mode = %v, want %v", headscale.Mode, 0o600)
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

	hook := bySource["templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh"]
	if hook.HostPath != "/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh" {
		t.Fatalf("HostPath = %q, want %q", hook.HostPath, "/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh")
	}
	if hook.Mode != 0o755 {
		t.Fatalf("Mode = %v, want %v", hook.Mode, 0o755)
	}
	if len(hook.Activations) != 0 {
		t.Fatalf("Activations = %v, want none", hook.Activations)
	}
}
