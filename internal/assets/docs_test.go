package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
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
			"init -> verify -> deploy -> verify -> status",
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
		"pinned lego v5.1.0",
		"## Supported Scope",
		"## Server Guide",
		"## Client Guide",
		"checksums.txt",
		"[Releases](https://github.com/simp-lee/meshify/releases)",
		"do not copy the placeholder literally",
		"`verify` is a static config and runtime-template check",
		"it does not read host systemd state, certificate files, Nginx runtime state, Headscale process state, or client online state",
		"sudo systemctl status headscale.service nginx.service --no-pager --full",
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
		"Cloudflare, DigitalOcean, and Tencent Cloud require a root-only `advanced.dns01.env_file`",
		"TENCENTCLOUD_SECRET_ID_FILE=/etc/meshify/dns01/tencentcloud-secret-id",
		`provider: "tencentcloud"`,
			"Route53 and gcloud may use the host credential chain",
			"do not put raw tokens or keys directly in `env_file`",
		"v5.1.0",
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
	if !containsHan(content) {
		t.Fatal("README.zh-CN.md must remain localized")
	}
	for _, want := range []string{
		"meshify app init --config meshify-apps/abc.yaml",
		"sudo meshify app deploy --config meshify-apps/abc.yaml",
		"meshify app verify --config meshify-apps/abc.yaml",
		"deploy/config/meshify-app.yaml.example",
		"`meshify app verify`",
		"`static-passed`",
		"systemd",
		"Nginx runtime",
		"GoAccess",
		"Tailscale",
		"`listen`",
		"`meshify.yaml`",
		"`upstream`",
		"Tailscale client",
		"`tailscale.login_server`",
		"`1.25.1`",
		"`http_v2`",
		"`service.env_file`",
		"`http2 on;`",
		"`nginx.static_locations`",
		"`nginx.goaccess`",
		"auth_basic_user_file",
		"`<app-name>-goaccess.service`",
		"`logrotate`",
		"`persist true`",
		"`restore true`",
		"GoAccess `db-path` `/var/lib/<app-name>/goaccess/db`",
		"HTML",
		"Nginx",
		"`/var/lib/<app-name>/goaccess/report.html`",
		"loopback WebSocket",
		"`nginx.goaccess.websocket_path`",
		"`nginx.access_log`",
		"`/var/log/meshify/apps/<app-name>/access.log`",
		"`user:hash`",
		"root-owned",
		"primary domain",
		"secondary domain",
		"`https://<primary-domain><nginx.goaccess.path>`",
		"`421`",
		"`C.UTF-8`",
		"`zh_CN.UTF-8`",
		"`locale -a`",
		"GoAccess UI",
		"raw",
		"request serving time",
		"`nginx.error_log`",
		"`<canonical-access-log>`",
		"`<error-log>`",
		"GoAccess runtime identity",
			"外部 `access_log` 安全要求",
		"`/var/log/nginx`",
		"`ProtectHome=true`",
		"`PrivateTmp=true`",
		"journalctl -u <app-name>.service -e",
		"`proxy.read_timeout`",
		"tailnet upstream",
		"`meshify app status`",
		"`deploy/templates/app/`",
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
		"public: true",
		"realtime: false",
		"upstream metrics",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("additional Go services guide still instructs manual runtime template deployment %q", unwanted)
		}
	}
}

func TestReadmeUpstreamAppExamplesIncludeAPIVersion(t *testing.T) {
	t.Parallel()

	docs := []struct {
		name string
		path string
	}{
		{name: "README", path: "README.md"},
		{name: "README.zh-CN", path: "README.zh-CN.md"},
	}
	for _, doc := range docs {
		content := readRepoDoc(t, doc.path)
		block := readmeYAMLBlockContaining(t, content, `name: "tailapp"`)
		if !strings.Contains(block, "api_version: meshify/app/v1alpha1") {
			t.Fatalf("%s upstream app example missing api_version", doc.name)
		}
		if !strings.Contains(block, "tailscale:") || !strings.Contains(block, "login_server") || !strings.Contains(block, "auth_key_file") {
			t.Fatalf("%s upstream app example missing explicit external tailscale guidance", doc.name)
		}
	}
}

func TestReadmeDocumentsStaticLocationCacheHeaderContract(t *testing.T) {
	t.Parallel()

	english := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"Prefer either `expires` or `cache_control`",
		"if both are set, Meshify renders both directives",
	} {
		if !strings.Contains(english, want) {
			t.Fatalf("README missing static cache-header contract %q", want)
		}
	}

	chinese := readRepoDoc(t, "README.zh-CN.md")
	for _, want := range []string{
		"`expires`",
		"`cache_control`",
		"`Cache-Control`",
	} {
		if !strings.Contains(chinese, want) {
			t.Fatalf("README.zh-CN missing static cache-header contract %q", want)
		}
	}

	example := readRepoDoc(t, "deploy", "config", "meshify-app.yaml.example")
	if !strings.Contains(example, "Prefer either expires or cache_control. If both are set, both directives render.") {
		t.Fatal("app config example missing static cache-header contract")
	}
	for _, want := range []string{
		"Set false for older distro Nginx packages",
		"For app-only upstream configs without a main meshify.yaml",
		"login_server explicitly",
	} {
		if !strings.Contains(example, want) {
			t.Fatalf("app config example missing app-only/http2 guidance %q", want)
		}
	}
}

func TestEnglishReadmeDocumentsAppGoAccess(t *testing.T) {
	t.Parallel()

	content := readRepoDoc(t, "README.md")
	for _, want := range []string{
		"`nginx.goaccess` is optional and default-off",
		"auth_basic_user_file",
		"`<app-name>-goaccess.service`",
		"GoAccess App Log Dashboard",
		"installs `logrotate`",
		"`db-path` at `/var/lib/<app-name>/goaccess/db`",
		"real-time HTML dashboard",
		"Nginx serves `/var/lib/<app-name>/goaccess/report.html`",
		"live updates through its loopback WebSocket",
		"`nginx.goaccess.websocket_path`",
		"does not expose a static-only GoAccess mode",
		"`persist true` and `restore true`",
		"`nginx.access_log` is empty",
		"`/var/log/meshify/apps/<app-name>/access.log`",
		"regular, non-empty file",
		"`user:hash` credential line",
		"user and hash contain no whitespace",
		"not accessible by other local users",
		"Every parent directory for `auth_basic_user_file` must be root-owned",
		"The GoAccess dashboard is primary-domain only",
		"Dashboard requests on secondary domains redirect",
		"`https://<primary-domain><nginx.goaccess.path>`",
		"WebSocket requests on secondary domains return `421`",
		"`C.UTF-8`",
		"`zh_CN.UTF-8`",
		"Deploy checks `locale -a` and fails early",
		"Language changes only GoAccess UI text",
		"raw log fields",
		"request serving time",
		"`nginx.error_log`",
		"must stay outside Meshify-managed app and GoAccess runtime paths",
		"must not equal the GoAccess canonical access log",
		"Other loopback IP literals are valid",
			"external canonical access log",
		"During deploy, Meshify creates or confirms the GoAccess runtime identity before the final readability check",
		"Do not place explicit GoAccess access logs under",
		"`/var/log/nginx`",
		"`ProtectHome=true` and `PrivateTmp=true`",
		"Use the configured `nginx.access_log` path for `<canonical-access-log>`",
		"Use the configured `nginx.error_log` path for `<error-log>`",
		"does not create them, chown foreign log roots, or install managed logrotate",
		"journalctl -u <app-name>.service -e",
		"Static locations with `access_log: false`",
		"GoAccess process state",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("README missing GoAccess app guidance %q", want)
		}
	}
	for _, unwanted := range []string{
		"public: true",
		"realtime: false",
		"public GoAccess dashboard",
		"no-auth GoAccess dashboard",
		"GoAccess analyzes error logs",
		"error log dashboard",
		"upstream metrics",
		"first-class upstream",
	} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("README contains unsupported GoAccess mode or claim %q", unwanted)
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
	example := readRepoDoc(t, "deploy", "config", "meshify-app.yaml.example")
	for _, want := range []string{
		"enabled: false",
		"auth_basic_user_file",
		"regular, non-empty htpasswd file",
		"user:hash credential line",
		"Credential line user and hash fields must not contain whitespace",
		"not accessible by other local users",
		"Every parent directory must be root-owned",
		"not writable by group or others",
		"searchable by the Nginx runtime user",
		"With GoAccess enabled and access_log empty",
		"/var/log/meshify/apps/<app-name>/access.log",
		"manages logrotate",
		"exact /var/log/meshify/apps/<app-name>/access.log path is still managed",
		"or install Meshify logrotate for them",
		"Deploy creates or confirms the GoAccess runtime user before the final readability check",
		"GoAccess rejects explicit access_log paths under /home, /root, /run/user",
		"/var/log/nginx",
		"ProtectHome/PrivateTmp",
		"persist true",
		"restore true",
		"db-path",
		"real-time HTML dashboard",
		"Nginx serves",
		"WebSocket updates at websocket_path",
		"language en requires C.UTF-8",
		"zh-CN requires both C.UTF-8 and zh_CN.UTF-8",
		"changes only GoAccess UI text",
		"defaults to 127.0.0.1:<app-derived-port>",
		"must be a loopback IP literal such as 127.0.0.1:<port> or [::1]:<port>",
		"With GoAccess enabled, error_log must not equal the GoAccess canonical",
	} {
		if !strings.Contains(example, want) {
			t.Fatalf("app config example missing GoAccess guidance %q", want)
		}
	}
	for _, unwanted := range []string{
		"public:",
		"realtime:",
		"unauthenticated",
		"public GoAccess dashboard",
		"error log dashboard",
		"upstream metrics",
	} {
		if strings.Contains(example, unwanted) {
			t.Fatalf("app config example contains unsupported GoAccess field or claim %q", unwanted)
		}
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

func readmeYAMLBlockContaining(t *testing.T, content string, marker string) string {
	t.Helper()

	markerIndex := strings.Index(content, marker)
	if markerIndex < 0 {
		t.Fatalf("README marker %q missing", marker)
	}
	beforeMarker := content[:markerIndex]
	start := strings.LastIndex(beforeMarker, "```yaml")
	if start < 0 {
		t.Fatalf("README marker %q missing preceding YAML block", marker)
	}
	afterFence := content[start+len("```yaml"):]
	end := strings.Index(afterFence, "```")
	if end < 0 {
		t.Fatalf("README marker %q YAML block is unterminated", marker)
	}
	return afterFence[:end]
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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
