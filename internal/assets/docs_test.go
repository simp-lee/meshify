package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployDocsAlignWithCLIAndSupportMatrix(t *testing.T) {
	t.Parallel()

	docs := map[string]string{
		"README": readRepoDoc(t, "README.md"),
	}

	for name, content := range docs {
		lower := strings.ToLower(content)
		for _, stale := range []string{
			"later phases",
			"after later phases",
			"still stop at config validation",
			"does not perform real host execution",
			"runtime host and network verification also land",
			"future cli outputs",
			"future build step",
		} {
			if strings.Contains(lower, stale) {
				t.Fatalf("%s doc contains stale staged-workflow text %q", name, stale)
			}
		}
	}

	combined := strings.Join(mapValues(docs), "\n")
	for _, want := range []string{
		"init -> deploy -> verify",
		"meshify status",
		"Debian-family",
		"apt/dpkg/systemd",
		"Windows",
		"macOS",
		"Debian/Ubuntu Linux",
		"Tailscale client >= v1.74.0",
		"/generate_204",
		"unix socket",
		"preauth",
		"MagicDNS",
		"tailscale ping",
		"tailscale status",
		"tailscale netcheck",
		"direct",
		"DERP",
		"China mainland",
	} {
		if !strings.Contains(combined, want) {
			t.Fatalf("deploy docs missing required user guidance %q", want)
		}
	}
}

func TestRootReadmePointsToPrimaryDocs(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"meshify init --config meshify.yaml",
		"meshify deploy --config meshify.yaml",
		"meshify verify --config meshify.yaml",
		"meshify status --config meshify.yaml",
		"Debian, Ubuntu, or a Debian-family distribution with apt/dpkg/systemd",
		"pinned lego v4.35.2",
		"## Supported Scope",
		"## Server Guide",
		"## Client Guide",
		"checksums.txt",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("root README missing project entrypoint detail %q", want)
		}
	}

	for _, unwanted := range []string{
		"## Release Validation",
		"## Development And Assets",
		"## Publishing Release Assets",
		"Server gates:",
		"Client gates:",
		".github/workflows/release.yml",
		"gh attestation verify",
		"git tag -a",
		"make check",
		"make lint",
		"make tidy",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("root README should be user-facing, but contains maintainer detail %q", unwanted)
		}
	}
}

func TestClientGuideIsSelfContained(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"### Windows",
		"### macOS",
		"### Debian/Ubuntu Linux",
		"Tailscale client >= v1.74.0",
		"tailscale",
		"--login-server",
		"--auth-key",
		"--accept-dns=true",
		"status",
		"ping",
		"netcheck",
		"MagicDNS",
		"DERP",
		"/generate_204",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("clients doc missing walkthrough detail %q", want)
		}
	}
	if strings.Contains(content, "tskey-example") {
		t.Fatal("clients doc contains Tailscale.com-style auth key placeholder")
	}
}

func TestOnboardingFreshKeyFlowIsConditional(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"Only if the meshify user is missing from users list",
		"preauthkeys create --user <ID> --expiration 24h",
		"creates a key that can register one client and expires after",
		"preauthkeys create --user <ID> --expiration 24h --reusable",
		"Use the numeric user ID shown by `users list` for the `meshify` user.",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("onboarding doc missing conditional fresh-key guidance %q", want)
		}
	}
}

func TestUserGuideDocumentsRuntimeSecurityBoundaries(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"Nginx serves HTTP-01 challenges from `/var/lib/meshify/acme-challenges`",
		"Nginx uses `/etc/meshify/tls/<server>/fullchain.pem` and `/etc/meshify/tls/<server>/privkey.pem`",
		"Explicit HTTP and HTTPS `default_server` catch-all blocks reject unmatched Host or SNI traffic",
		"Existing Nginx can coexist by `server_name`",
		"Headscale exposes STUN on `3478/udp`",
		"Cloudflare and DigitalOcean require a root-only `advanced.dns01.env_file`",
		"Route53 and gcloud may use lego's ambient credential chain",
		"Raw DNS tokens or keys live in separate root-only files referenced by lego `_FILE` variables",
		"v4.35.2",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("user README missing runtime security boundary detail %q", want)
		}
	}
	if strings.Contains(content, "Nginx owns the configured `server_name`, uses `fullchain.pem`, and does not become a `default_server`") {
		t.Fatal("user README contains stale default_server wording")
	}
	if strings.Contains(content, "coexistence with other sites depends on the host's existing default server ordering") {
		t.Fatal("user README contains stale default-server ordering caveat")
	}
}

func readRepoDoc(t *testing.T, path ...string) string {
	t.Helper()

	parts := append([]string{"..", ".."}, path...)
	content, err := os.ReadFile(filepath.Join(parts...))
	if err != nil {
		t.Fatalf("ReadFile(%v) error = %v", path, err)
	}
	return string(content)
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}
