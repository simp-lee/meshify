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
		"pinned lego v5.0.4",
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
		"v5.0.4",
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

func TestChineseReadmeDocumentsAppCLI(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.zh-CN.md")
	for _, want := range []string{
		"meshify app init --config meshify-apps/abc.yaml",
		"sudo meshify app deploy --config meshify-apps/abc.yaml",
		"meshify app verify --config meshify-apps/abc.yaml",
		"deploy/config/meshify-app.yaml.example",
		"`meshify app verify` 是 app 流程的静态配置和模板检查",
		"状态通过时 CLI 输出 `static-passed`",
		"它不读取宿主机上的已部署文件、systemd 状态、证书 SAN、Nginx runtime 或 Tailscale 在线状态",
		"`listen` 表示本机 app 模式",
		"`service.env_file`",
		"`http2 on;`",
		"`nginx.static_locations`",
		"`proxy.read_timeout`",
		"`upstream` 表示 tailnet upstream 模式",
		"upstream` 模式自动需要 Tailscale client",
		"app 首版没有独立 checkpoint store，因此不提供 `meshify app status`",
		"release binary 的 app runtime 模板唯一来源是 `deploy/templates/app/`",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("additional Go services guide missing CLI guidance %q", want)
		}
	}

	for _, unwanted := range []string{
		"docs/additional-go-services",
		"docs/templates/extra-go-service",
		"sudo cp docs/templates/extra-go-service",
		"sudoedit /etc/nginx/sites-available/example-app.conf",
		"复制并编辑 systemd unit",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("additional Go services guide still instructs manual runtime template deployment %q", unwanted)
		}
	}
}

func TestAppRuntimeTemplatesAreCanonicalDeployAssets(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(filepath.Join("..", "..", "deploy", "config", "meshify-app.yaml.example")); err != nil {
		t.Fatalf("canonical app config example is not present: %v", err)
	}
	if _, ok := Lookup("config/meshify-app.yaml.example"); !ok {
		t.Fatal("app config example is missing from embedded asset catalog")
	}

	templatePaths := readDeployAppTemplatePaths(t)
	templateSet := make(map[string]struct{}, len(templatePaths))
	for _, sourcePath := range templatePaths {
		templateSet[sourcePath] = struct{}{}
		asset, ok := Lookup(sourcePath)
		if !ok {
			t.Fatalf("app runtime template %q is missing from embedded asset catalog", sourcePath)
		}
		if asset.Role != RoleRuntime || asset.ContentMode != ContentModeRender {
			t.Fatalf("catalog asset %q role/mode = %s/%s, want runtime/render", sourcePath, asset.Role, asset.ContentMode)
		}
		if _, err := NewLoader().Read(sourcePath); err != nil {
			t.Fatalf("embedded loader cannot read app runtime template %q: %v", sourcePath, err)
		}
	}
	for _, asset := range Catalog() {
		if !strings.HasPrefix(asset.SourcePath, "templates/app/") {
			continue
		}
		if _, ok := templateSet[asset.SourcePath]; !ok {
			t.Fatalf("embedded asset catalog has app runtime template %q that is not present under deploy/templates/app", asset.SourcePath)
		}
	}

	nginxTemplate := readRepoDoc(t, "deploy", "templates", "app", "nginx.conf.tmpl")
	for _, want := range []string{
		"map $http_host ${{ .VarPrefix }}_host_header_valid",
		"map $http_host ${{ .VarPrefix }}_validated_host",
		"map $ssl_server_name ${{ .VarPrefix }}_sni_valid",
		"if (${{ .VarPrefix }}_sni_valid = 0)",
		"if (${{ .VarPrefix }}_host_header_valid = 0)",
		"return 421;",
		"http2 on;",
		"client_max_body_size {{ .ClientMaxBodySize }};",
		"proxy_set_header Host ${{ .VarPrefix }}_validated_host;",
		"proxy_set_header X-Forwarded-Host ${{ .VarPrefix }}_validated_host;",
		"proxy_set_header Connection ${{ .VarPrefix }}_connection_upgrade;",
		"proxy_read_timeout {{ .Proxy.ReadTimeout }};",
	} {
		if !strings.Contains(nginxTemplate, want) {
			t.Fatalf("app nginx runtime template missing boundary %q", want)
		}
	}
}

func readDeployAppTemplatePaths(t *testing.T) []string {
	t.Helper()

	root := filepath.Join("..", "..", "deploy", "templates", "app")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", root, err)
	}
	paths := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected directory in deploy/templates/app: %s", entry.Name())
		}
		paths = append(paths, filepath.ToSlash(filepath.Join("templates", "app", entry.Name())))
	}
	if len(paths) == 0 {
		t.Fatal("deploy/templates/app has no runtime templates")
	}
	return paths
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
