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
		"README":        readDeployDoc(t, "README.md"),
		"quickstart":    readDeployDoc(t, "docs", "quickstart.md"),
		"onboarding":    readDeployDoc(t, "docs", "onboarding.md"),
		"troubleshoot":  readDeployDoc(t, "docs", "troubleshooting.md"),
		"architecture":  readDeployDoc(t, "docs", "architecture.md"),
		"windows":       readDeployDoc(t, "docs", "clients", "windows.md"),
		"macos":         readDeployDoc(t, "docs", "clients", "macos.md"),
		"debian-ubuntu": readDeployDoc(t, "docs", "clients", "debian-ubuntu-linux.md"),
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
		"Debian 13",
		"Ubuntu 24.04 LTS",
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
			t.Fatalf("deploy docs missing required release guidance %q", want)
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
		"Debian 13, Ubuntu 24.04 LTS",
		"pinned lego v4.35.2",
		"deploy/docs/quickstart.md",
		"deploy/docs/onboarding.md",
		"deploy/docs/troubleshooting.md",
		"test/e2e/README.md",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("root README missing project entrypoint detail %q", want)
		}
	}
}

func TestClientGuidesAreSelfContained(t *testing.T) {
	t.Parallel()

	for _, path := range []string{
		filepath.Join("docs", "clients", "windows.md"),
		filepath.Join("docs", "clients", "macos.md"),
		filepath.Join("docs", "clients", "debian-ubuntu-linux.md"),
	} {
		content := readDeployDoc(t, path)
		for _, want := range []string{
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
				t.Fatalf("%s missing client walkthrough detail %q", path, want)
			}
		}
		if strings.Contains(content, "tskey-example") {
			t.Fatalf("%s contains Tailscale.com-style auth key placeholder", path)
		}
		dailySection := content
		if _, section, ok := strings.Cut(content, "## Daily Operations"); ok {
			dailySection = section
		}
		if strings.Contains(dailySection, "--auth-key") {
			t.Fatalf("%s daily operations must not suggest reusing a preauth key", path)
		}
	}
}

func TestOnboardingFreshKeyFlowIsConditional(t *testing.T) {
	t.Parallel()

	content := readDeployDoc(t, "docs", "onboarding.md")
	for _, want := range []string{
		"Only if the meshify user is missing from users list",
		"preauthkeys create --user <ID> --expiration 24h",
		"Use the numeric user ID shown by `users list` for the `meshify` user.",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("onboarding doc missing conditional fresh-key guidance %q", want)
		}
	}
}

func TestE2EReleaseGateDocumentsNginxDefaultServerIsolation(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "test", "e2e", "README.md")
	for _, want := range []string{
		"configured Nginx `server_name` blocks use `fullchain.pem`",
		"explicit HTTP/HTTPS `default_server` catch-all blocks use empty `server_name \"\"`",
		"return `444` on HTTP and `421` on HTTPS",
		"never proxy unmatched Host/SNI traffic to Headscale",
		"HTTP-01 issuance succeeds on a public host",
		"/var/lib/meshify/acme-challenges",
		"DNS-01 issuance succeeds on at least one supported lego provider",
		"Cloudflare or DigitalOcean",
		"Route53 or gcloud",
		"lego's ambient credential chain",
		"Raw DNS tokens or keys live in separate root-only files referenced by lego `_FILE` variables",
		"`meshify-lego-renew.timer` is enabled and active",
		"/opt/meshify/bin/lego --version",
		"v4.35.2",
		"/etc/meshify/tls/<server>/fullchain.pem",
		"/etc/meshify/tls/<server>/privkey.pem",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("E2E README missing Nginx default-server release gate %q", want)
		}
	}
	if strings.Contains(content, "Nginx owns the configured `server_name`, uses `fullchain.pem`, and does not become a `default_server`") {
		t.Fatal("E2E README contains stale default_server wording")
	}
	if strings.Contains(content, "coexistence with other sites depends on the host's existing default server ordering") {
		t.Fatal("E2E README contains stale default-server ordering caveat")
	}
}

func readDeployDoc(t *testing.T, path ...string) string {
	t.Helper()

	parts := append([]string{"deploy"}, path...)
	return readRepoDoc(t, parts...)
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
