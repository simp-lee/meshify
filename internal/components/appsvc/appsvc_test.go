package appsvc

import (
	"meshify/internal/appconfig"
	"meshify/internal/host"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func testConfig() appconfig.Config {
	cfg := appconfig.New()
	cfg.App.Name = "example-app"
	cfg.App.Domains = []string{"abc.com", "www.abc.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/opt/example-app/example-app --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/opt/example-app"
	return cfg
}

func TestNewNamesDerivesAppResourcePaths(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}

	tests := map[string]string{
		"AppName":            names.AppName,
		"VarPrefix":          names.VarPrefix,
		"SystemUser":         names.SystemUser,
		"ServiceUnit":        names.ServiceUnit,
		"RenewServiceUnit":   names.RenewServiceUnit,
		"RenewTimerUnit":     names.RenewTimerUnit,
		"VarLibDir":          names.VarLibDir,
		"EtcDir":             names.EtcDir,
		"HookDir":            names.HookDir,
		"NginxAvailablePath": names.NginxAvailablePath,
		"NginxEnabledPath":   names.NginxEnabledPath,
		"WebrootPath":        names.WebrootPath,
		"LegoDataPath":       names.LegoDataPath,
		"TLSDir":             names.TLSDir,
		"VarLibMarkerPath":   names.VarLibMarkerPath,
		"EtcMarkerPath":      names.EtcMarkerPath,
		"HookDirMarkerPath":  names.HookDirMarkerPath,
		"FullchainPath":      names.FullchainPath,
		"PrivateKeyPath":     names.PrivateKeyPath,
		"TLSMarkerPath":      names.TLSMarkerPath,
		"HookPath":           names.HookPath,
	}
	for field, got := range tests {
		if strings.Contains(got, "meshify/tls") {
			t.Fatalf("%s = %q, must not use main meshify TLS path", field, got)
		}
	}
	if names.ServiceUnit != "example-app.service" {
		t.Fatalf("ServiceUnit = %q, want example-app.service", names.ServiceUnit)
	}
	if names.TLSDir != "/etc/example-app/tls/abc.com" {
		t.Fatalf("TLSDir = %q, want app-specific cert dir", names.TLSDir)
	}
	if names.TLSMarkerPath != "/etc/example-app/tls/abc.com/.meshify-managed" {
		t.Fatalf("TLSMarkerPath = %q, want app-specific TLS marker", names.TLSMarkerPath)
	}
	if names.VarLibMarkerPath != "/var/lib/example-app/.meshify-managed" {
		t.Fatalf("VarLibMarkerPath = %q, want app root marker", names.VarLibMarkerPath)
	}
	if names.EtcMarkerPath != "/etc/example-app/.meshify-managed" {
		t.Fatalf("EtcMarkerPath = %q, want app etc marker", names.EtcMarkerPath)
	}
	if names.HookDirMarkerPath != "/usr/local/lib/meshify/apps/example-app/.meshify-managed" {
		t.Fatalf("HookDirMarkerPath = %q, want app hook dir marker", names.HookDirMarkerPath)
	}
}

func TestGuardServerNameConflictsCommandRejectsEnabledDuplicate(t *testing.T) {
	t.Parallel()

	current := "/etc/nginx/sites-enabled/example-app.conf"
	available := "/etc/nginx/sites-available/example-app.conf"
	command := guardServerNameConflictsCommand(current, available, []string{"abc.com"})
	output, err := runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		current:                   "server { server_name abc.com; }\n",
		"/etc/nginx/conf.d/other": "server { server_name other.example.com; }\n",
		"/etc/nginx/conf.d/app":   "server { server_name abc.com other.example.com; }\n",
		"/etc/nginx/nginx.conf":   "events {}\nhttp { include /etc/nginx/conf.d/*.conf; }\n",
		"/etc/nginx/mime.types":   "types {}\n",
		"/etc/nginx/fastcgi.conf": "",
		"/etc/nginx/proxy_params": "",
		"/etc/nginx/scgi_params":  "",
		"/etc/nginx/uwsgi_params": "",
		"/etc/nginx/modules.conf": "",
	}))
	if err == nil {
		t.Fatalf("guard command error = nil, want duplicate domain failure; output:\n%s", output)
	}

	output, err = runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		"/etc/nginx/conf.d/multiline-app.conf": `server {
    listen 443 ssl;
    server_name
        other.example.com
        abc.com;
}
`,
	}))
	if err == nil {
		t.Fatalf("guard command error = nil, want multiline duplicate domain failure; output:\n%s", output)
	}

	output, err = runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		current:                 "server { server_name abc.com; }\n",
		"/etc/nginx/conf.d/app": "server { server_name other.example.com; }\n",
	}))
	if err != nil {
		t.Fatalf("guard command error = %v; output:\n%s", err, output)
	}
}

func TestGuardDefaultServerCommandAllowsOnlyMeshifyCatchAll(t *testing.T) {
	t.Parallel()

	current := "/etc/nginx/sites-enabled/example-app.conf"
	available := "/etc/nginx/sites-available/example-app.conf"
	command := guardDefaultServerCommand(current, available)
	output, err := runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		"/etc/nginx/conf.d/custom-default.conf": "server { listen 80 default_server; root /var/www/html; }\n",
	}))
	if err == nil {
		t.Fatalf("guard command error = nil, want custom default_server failure; output:\n%s", output)
	}

	output, err = runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		"/etc/nginx/conf.d/multiline-default.conf": `server {
    listen
        80
        default_server;
    server_name custom.example.com;
    return 200;
}
`,
	}))
	if err == nil {
		t.Fatalf("guard command error = nil, want multiline custom default_server failure; output:\n%s", output)
	}

	meshify := `server {
    listen 80 default_server;
    server_name "";
    return 444;
}
server {
    listen 443 ssl default_server;
    server_name "";
    ssl_certificate /etc/meshify/tls/hs.example.com/fullchain.pem;
    return 421;
}
`
	output, err = runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		"/etc/nginx/conf.d/meshify-default.conf": meshify,
	}))
	if err != nil {
		t.Fatalf("guard command error = %v; output:\n%s", err, output)
	}

	mixed := meshify + `
server {
    listen 8080 default_server;
    server_name custom.example.com;
    return 200;
}
`
	output, err = runGuardCommandWithNginxDump(t, command, nginxDump(map[string]string{
		"/etc/nginx/conf.d/mixed-default.conf": mixed,
	}))
	if err == nil {
		t.Fatalf("guard command error = nil, want custom default_server in mixed file failure; output:\n%s", output)
	}
}

func TestCertificatePlanRepeatsDomainsAndMasksEnvFileDisplay(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.DNS01.Provider = "google"
	cfg.DNS01.EnvFile = "/etc/example-app/dns/gcloud.env"
	names, err := NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}

	plan, err := NewCertificatePlan(cfg, names)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	display := plan.Command.String()
	for _, want := range []string{
		"--domains abc.com",
		"--domains www.abc.com",
		"--dns gcloud",
		"run|renew --hook /usr/local/lib/meshify/apps/example-app/install-cert-and-reload-nginx.sh",
	} {
		if !strings.Contains(display, want) {
			t.Fatalf("command = %q, want substring %q", display, want)
		}
	}
	if strings.Contains(display, cfg.DNS01.EnvFile) {
		t.Fatalf("command display leaks env_file path: %q", display)
	}
}

func TestCertificatePlanIssueOrRenewWrapper(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	names, err := NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	plan, err := NewCertificatePlan(cfg, names)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}
	if plan.Command.Name != "sh" || len(plan.Command.Args) < 8 || plan.Command.Args[2] != "meshify-app-lego-issue-or-renew" {
		t.Fatalf("command = %#v, want issue-or-renew shell wrapper", plan.Command)
	}

	for _, tt := range []struct {
		name           string
		existingCert   bool
		wantSubstrings []string
	}{
		{
			name:           "first issue",
			wantSubstrings: []string{" run ", " --run-hook "},
		},
		{
			name:           "renew existing",
			existingCert:   true,
			wantSubstrings: []string{" renew ", " --force-cert-domains ", " --renew-hook "},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			legoPath := filepath.Join(dir, "lego")
			if tt.existingCert {
				certDir := filepath.Join(legoPath, "certificates")
				if err := os.MkdirAll(certDir, 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				for _, name := range []string{"abc.com.crt", "abc.com.key", "abc.com.json"} {
					if err := os.WriteFile(filepath.Join(certDir, name), []byte("present"), 0o600); err != nil {
						t.Fatalf("WriteFile() error = %v", err)
					}
				}
			}
			fakeLego := filepath.Join(dir, "lego-bin")
			fakeHook := filepath.Join(dir, "hook")
			outputFile := filepath.Join(dir, "args")
			writeExecutable(t, fakeLego, "#!/bin/sh\nprintf ' %s' \"$@\" > "+outputFile+"\n")
			writeExecutable(t, fakeHook, "#!/bin/sh\nexit 0\n")

			args := append([]string(nil), plan.Command.Args...)
			args[3] = fakeLego
			args[4] = legoPath
			args[6] = fakeHook
			output, err := exec.Command(plan.Command.Name, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("wrapper error = %v; output:\n%s", err, output)
			}
			got, err := os.ReadFile(outputFile)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			for _, want := range tt.wantSubstrings {
				if !strings.Contains(string(got), want) {
					t.Fatalf("lego args = %q, want substring %q", got, want)
				}
			}
		})
	}
}

func TestCertificatePlanInstallsCachedLegoCertificateBeforeRenew(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	names, err := NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	plan, err := NewCertificatePlan(cfg, names)
	if err != nil {
		t.Fatalf("NewCertificatePlan() error = %v", err)
	}

	dir := t.TempDir()
	legoPath := filepath.Join(dir, "lego")
	certDir := filepath.Join(legoPath, "certificates")
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for name, content := range map[string]string{
		"abc.com.crt":  "cached cert",
		"abc.com.key":  "cached key",
		"abc.com.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(certDir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	hookLog := filepath.Join(dir, "hook.log")
	hook := filepath.Join(dir, "hook")
	writeExecutable(t, hook, `#!/bin/sh
printf 'hook cert=%s key=%s\n' "$LEGO_CERT_PATH" "$LEGO_CERT_KEY_PATH" >> "`+hookLog+`"
`)
	fakeLegoLog := filepath.Join(dir, "lego.log")
	fakeLego := filepath.Join(dir, "lego-bin")
	writeExecutable(t, fakeLego, `#!/bin/sh
printf 'lego %s\n' "$*" >> "`+fakeLegoLog+`"
`)

	args := append([]string(nil), plan.Command.Args...)
	args[3] = fakeLego
	args[4] = legoPath
	args[6] = hook
	output, err := exec.Command(plan.Command.Name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper error = %v; output:\n%s", err, output)
	}
	hookOutput, err := os.ReadFile(hookLog)
	if err != nil {
		t.Fatalf("ReadFile(hook log) error = %v", err)
	}
	wantHook := "hook cert=" + filepath.Join(certDir, "abc.com.crt") + " key=" + filepath.Join(certDir, "abc.com.key") + "\n"
	if string(hookOutput) != wantHook {
		t.Fatalf("hook log = %q, want %q", hookOutput, wantHook)
	}
	legoOutput, err := os.ReadFile(fakeLegoLog)
	if err != nil {
		t.Fatalf("ReadFile(lego log) error = %v", err)
	}
	if !strings.Contains(string(legoOutput), " renew ") || !strings.Contains(string(legoOutput), " --renew-hook "+hook) {
		t.Fatalf("lego log = %q, want renew after cached install hook", legoOutput)
	}
}

func TestDNS01CommandEnvFileParserMatchesSystemdStyle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	envFile := filepath.Join(dir, "dns.env")
	if err := os.WriteFile(envFile, []byte(`
# comment
; another comment
CF_DNS_API_TOKEN_FILE = '`+filepath.Join(dir, "cf token")+`'
GCE_PROJECT = "meshify-project"
EMPTY =
ignored note
`), 0o600); err != nil {
		t.Fatalf("WriteFile(envFile) error = %v", err)
	}
	printer := filepath.Join(dir, "print-env")
	writeExecutable(t, printer, `#!/bin/sh
printf 'CF=%s\nGCE=%s\nEMPTY=%s\n' "$CF_DNS_API_TOKEN_FILE" "$GCE_PROJECT" "${EMPTY-unset}"
`)
	command := legoCommandWithEnvFile(envFile, []string{"run"})
	script := command.Args[1]

	output, err := exec.Command("sh", "-c", script, "meshify-app-lego-dns01", envFile, printer).CombinedOutput()
	if err != nil {
		t.Fatalf("env parser script error = %v; output:\n%s", err, output)
	}
	want := "CF=" + filepath.Join(dir, "cf token") + "\nGCE=meshify-project\nEMPTY=unset\n"
	if string(output) != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	for _, unwanted := range []string{`. "$env_file"`, `source "$env_file"`} {
		if strings.Contains(script, unwanted) {
			t.Fatalf("script must not execute env_file through %q", unwanted)
		}
	}
}

func TestDNS01CommandEnvFileParserRejectsUnsafeSyntax(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "export", content: "export CF_DNS_API_TOKEN_FILE=/run/secrets/cf\n", want: "unsupported export syntax"},
		{name: "invalid variable name", content: "CF-DNS-API-TOKEN=/run/secrets/cf\n", want: "unsupported DNS env_file variable name"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			envFile := filepath.Join(dir, "dns.env")
			if err := os.WriteFile(envFile, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile(envFile) error = %v", err)
			}
			command := legoCommandWithEnvFile(envFile, []string{"run"})
			output, err := exec.Command("sh", "-c", command.Args[1], "meshify-app-lego-dns01", envFile, "true").CombinedOutput()
			if err == nil {
				t.Fatalf("env parser script error = nil, want failure; output:\n%s", output)
			}
			if !strings.Contains(string(output), tt.want) {
				t.Fatalf("output = %q, want substring %q", output, tt.want)
			}
		})
	}
}

func TestGuardTLSOwnershipCommandProtectsCertificateTargets(t *testing.T) {
	t.Parallel()

	baseNames, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}

	tests := []struct {
		name    string
		prepare func(t *testing.T, names Names)
		wantErr bool
		want    string
	}{
		{name: "empty dir installs marker", wantErr: false},
		{
			name: "matching marker",
			prepare: func(t *testing.T, names Names) {
				writeFile(t, names.TLSMarkerPath, ManagedMarker(names.AppName)+"\n", 0o600)
			},
			wantErr: false,
		},
		{
			name: "foreign marker",
			prepare: func(t *testing.T, names Names) {
				writeFile(t, names.TLSMarkerPath, ManagedMarker("other-app")+"\n", 0o600)
			},
			wantErr: true,
			want:    "managed by a different Meshify app",
		},
		{
			name: "existing cert without marker",
			prepare: func(t *testing.T, names Names) {
				writeFile(t, names.FullchainPath, "foreign cert\n", 0o644)
			},
			wantErr: true,
			want:    "refusing to overwrite non-Meshify TLS file",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			names := baseNames
			names.TLSDir = filepath.Join(dir, "tls")
			names.FullchainPath = filepath.Join(names.TLSDir, "fullchain.pem")
			names.PrivateKeyPath = filepath.Join(names.TLSDir, "privkey.pem")
			names.TLSMarkerPath = filepath.Join(names.TLSDir, ".meshify-managed")
			if tt.prepare != nil {
				tt.prepare(t, names)
			}
			command := GuardTLSOwnershipCommand(names)
			args := append([]string{"-c", command.Args[1]}, command.Args[2:]...)
			output, err := exec.Command("sh", args...).CombinedOutput()
			if tt.wantErr && err == nil {
				t.Fatalf("script error = nil, want failure; output:\n%s", output)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("script error = %v; output:\n%s", err, output)
			}
			if tt.want != "" && !strings.Contains(string(output), tt.want) {
				t.Fatalf("output = %q, want %q", output, tt.want)
			}
			if !tt.wantErr {
				content, err := os.ReadFile(names.TLSMarkerPath)
				if err != nil {
					t.Fatalf("ReadFile(marker) error = %v", err)
				}
				if strings.TrimSpace(string(content)) != ManagedMarker(names.AppName) {
					t.Fatalf("marker = %q, want %q", content, ManagedMarker(names.AppName))
				}
			}
		})
	}
}

func TestEnsureSystemUserCommandRejectsExistingIdentityConflicts(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	command := EnsureSystemUserCommands(names)[0]
	script := command.Args[1]

	tests := []struct {
		name     string
		scenario string
		want     string
	}{
		{
			name:     "existing group without user",
			scenario: "group-only",
			want:     "already exists without matching app user",
		},
		{
			name:     "existing user without group",
			scenario: "user-only",
			want:     "already exists without matching app group",
		},
		{
			name:     "mismatched primary group",
			scenario: "mismatched-group",
			want:     "primary group does not match app group",
		},
		{
			name:     "mismatched home",
			scenario: "mismatched-home",
			want:     "expected /var/lib/example-app",
		},
		{
			name:     "login shell",
			scenario: "login-shell",
			want:     "expected /usr/sbin/nologin",
		},
		{
			name:     "regular uid",
			scenario: "regular-uid",
			want:     "not a non-root system identity",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			output, err := runSystemUserScript(t, script, tt.scenario)
			if err == nil {
				t.Fatalf("script error = nil, want failure; output:\n%s", output)
			}
			if !strings.Contains(output, tt.want) {
				t.Fatalf("script output = %q, want substring %q", output, tt.want)
			}
		})
	}
}

func TestEnsureSystemUserCommandAcceptsMatchingSystemIdentity(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	output, err := runSystemUserScript(t, EnsureSystemUserCommands(names)[0].Args[1], "matching")
	if err != nil {
		t.Fatalf("script error = %v; output:\n%s", err, output)
	}
}

func TestGuardSystemUserCommandRejectsConflictsWithoutCreating(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	script := GuardSystemUserCommand(names).Args[1]

	output, err := runSystemUserGuardScript(t, script, "group-only")
	if err == nil {
		t.Fatalf("guard script error = nil, want group conflict; output:\n%s", output)
	}
	if !strings.Contains(output, "already exists without matching app user") {
		t.Fatalf("guard output = %q, want group conflict", output)
	}

	output, err = runSystemUserGuardScript(t, script, "missing")
	if err != nil {
		t.Fatalf("guard script for missing identity error = %v; output:\n%s", err, output)
	}
}

func TestGuardRootDirectoriesCommandCreatesAndProtectsMarkers(t *testing.T) {
	t.Parallel()

	baseNames, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	dir := t.TempDir()
	names := baseNames
	names.VarLibDir = filepath.Join(dir, "var-lib", "example-app")
	names.EtcDir = filepath.Join(dir, "etc", "example-app")
	names.HookDir = filepath.Join(dir, "hooks", "example-app")
	names.VarLibMarkerPath = filepath.Join(names.VarLibDir, ".meshify-managed")
	names.EtcMarkerPath = filepath.Join(names.EtcDir, ".meshify-managed")
	names.HookDirMarkerPath = filepath.Join(names.HookDir, ".meshify-managed")

	command := GuardRootDirectoriesCommand(names)
	output, err := exec.Command("sh", append([]string{"-c", command.Args[1]}, command.Args[2:]...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("root directory guard error = %v; output:\n%s", err, output)
	}
	for _, marker := range []string{names.VarLibMarkerPath, names.EtcMarkerPath, names.HookDirMarkerPath} {
		content, err := os.ReadFile(marker)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", marker, err)
		}
		if strings.TrimSpace(string(content)) != ManagedMarker(names.AppName) {
			t.Fatalf("marker %s = %q, want %q", marker, content, ManagedMarker(names.AppName))
		}
	}

	foreign := baseNames
	foreign.VarLibDir = filepath.Join(dir, "foreign-var-lib")
	foreign.EtcDir = filepath.Join(dir, "foreign-etc")
	foreign.HookDir = filepath.Join(dir, "foreign-hooks")
	foreign.VarLibMarkerPath = filepath.Join(foreign.VarLibDir, ".meshify-managed")
	foreign.EtcMarkerPath = filepath.Join(foreign.EtcDir, ".meshify-managed")
	foreign.HookDirMarkerPath = filepath.Join(foreign.HookDir, ".meshify-managed")
	if err := os.MkdirAll(foreign.EtcDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(foreign) error = %v", err)
	}
	output, err = exec.Command("sh", append([]string{"-c", GuardRootDirectoriesCommand(foreign).Args[1]}, GuardRootDirectoriesCommand(foreign).Args[2:]...)...).CombinedOutput()
	if err == nil {
		t.Fatalf("root directory guard error = nil, want foreign directory failure; output:\n%s", output)
	}
	if !strings.Contains(string(output), "must be owned by root") {
		t.Fatalf("output = %q, want root ownership refusal", output)
	}
	info, statErr := os.Stat(foreign.EtcDir)
	if statErr != nil {
		t.Fatalf("Stat(foreign.EtcDir) error = %v", statErr)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("foreign dir mode = %o, want unchanged 0700", info.Mode().Perm())
	}
}

func TestGuardServiceAccessCommandChecksAppUserPermissions(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	command := GuardServiceAccessCommand(names, "/opt/example-app/example-app", "/opt/example-app")
	script := command.Args[1]
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "runuser"), `#!/bin/sh
case "$SCENARIO:$5:$6" in
  binary-denied:-x:/opt/example-app/example-app) exit 1 ;;
  workdir-not-dir:-d:/opt/example-app) exit 1 ;;
  workdir-denied:-x:/opt/example-app) exit 1 ;;
esac
exit 0
`)

	tests := []struct {
		name     string
		scenario string
		want     string
	}{
		{name: "binary denied", scenario: "binary-denied", want: "cannot execute service binary"},
		{name: "working directory missing", scenario: "workdir-not-dir", want: "cannot access working_directory"},
		{name: "working directory denied", scenario: "workdir-denied", want: "cannot enter working_directory"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			output, err := runServiceAccessScript(t, script, dir, tt.scenario)
			if err == nil {
				t.Fatalf("service access guard error = nil, want failure; output:\n%s", output)
			}
			if !strings.Contains(output, tt.want) {
				t.Fatalf("output = %q, want substring %q", output, tt.want)
			}
		})
	}

	output, err := runServiceAccessScript(t, script, dir, "ok")
	if err != nil {
		t.Fatalf("service access guard error = %v; output:\n%s", err, output)
	}
}

func TestManagedMarkerRejectsForeignFiles(t *testing.T) {
	t.Parallel()

	if err := CheckManagedContent("example-app", []byte("# "+ManagedMarker("example-app"))); err != nil {
		t.Fatalf("CheckManagedContent() error = %v", err)
	}
	if err := CheckManagedContent("api", []byte("# "+ManagedMarker("api-admin"))); err == nil {
		t.Fatal("CheckManagedContent() error = nil, want prefix-name foreign marker failure")
	}
	if err := CheckManagedContent("example-app", []byte("# "+ManagedMarker("example-app")+"\n# "+ManagedMarker("other-app"))); err == nil {
		t.Fatal("CheckManagedContent() error = nil, want mixed marker failure")
	}
	if err := CheckManagedContent("example-app", []byte("# Meshify-managed: app.name=other")); err == nil {
		t.Fatal("CheckManagedContent() error = nil, want foreign marker failure")
	}
	if err := CheckManagedContent("example-app", []byte("plain config")); err == nil {
		t.Fatal("CheckManagedContent() error = nil, want unmanaged failure")
	}
}

func TestGuardEnabledSiteCommandProtectsForeignEnabledPath(t *testing.T) {
	t.Parallel()

	names, err := NewNames(testConfig())
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	command := GuardEnabledSiteCommand(names)
	script := command.Args[1]
	dir := t.TempDir()
	available := filepath.Join(dir, "sites-available", "example-app.conf")
	enabled := filepath.Join(dir, "sites-enabled", "example-app.conf")
	if err := os.MkdirAll(filepath.Dir(available), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(enabled), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(available, []byte("# app\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	tests := []struct {
		name    string
		prepare func(t *testing.T)
		wantErr bool
		want    string
	}{
		{name: "missing", prepare: func(t *testing.T) {}, wantErr: false},
		{
			name: "matching symlink",
			prepare: func(t *testing.T) {
				if err := os.Symlink(available, enabled); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
			},
			wantErr: false,
		},
		{
			name: "foreign symlink",
			prepare: func(t *testing.T) {
				if err := os.Symlink(filepath.Join(dir, "other.conf"), enabled); err != nil {
					t.Fatalf("Symlink() error = %v", err)
				}
			},
			wantErr: true,
			want:    "refusing to replace it",
		},
		{
			name: "regular file",
			prepare: func(t *testing.T) {
				if err := os.WriteFile(enabled, []byte("# foreign\n"), 0o644); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			wantErr: true,
			want:    "not a Meshify-managed app symlink",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(enabled); err != nil && !os.IsNotExist(err) {
				t.Fatalf("Remove() error = %v", err)
			}
			tt.prepare(t)
			cmd := exec.Command("sh", "-c", script, "meshify-app-nginx-enabled-guard", enabled, available)
			output, err := cmd.CombinedOutput()
			if tt.wantErr && err == nil {
				t.Fatalf("script error = nil, want failure; output:\n%s", output)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("script error = %v; output:\n%s", err, output)
			}
			if tt.want != "" && !strings.Contains(string(output), tt.want) {
				t.Fatalf("script output = %q, want %q", output, tt.want)
			}
		})
	}
}

func runServiceAccessScript(t *testing.T, script string, binDir string, scenario string) (string, error) {
	t.Helper()

	cmd := exec.Command("sh", "-c", script, "meshify-app-service-access", "example-app", "/opt/example-app/example-app", "/opt/example-app")
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "SCENARIO="+scenario)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runSystemUserGuardScript(t *testing.T, script string, scenario string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "getent"), `#!/bin/sh
case "$1:$2:$SCENARIO" in
  group:example-app:group-only) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:group-only) exit 2 ;;
  group:example-app:missing) exit 2 ;;
  passwd:example-app:missing) exit 2 ;;
esac
exit 2
`)
	writeExecutable(t, filepath.Join(dir, "groupadd"), "#!/bin/sh\necho unexpected groupadd >&2\nexit 99\n")
	writeExecutable(t, filepath.Join(dir, "useradd"), "#!/bin/sh\necho unexpected useradd >&2\nexit 99\n")

	cmd := exec.Command("sh", "-c", script, "meshify-app-user-guard", "example-app", "/var/lib/example-app")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SCENARIO="+scenario)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runSystemUserScript(t *testing.T, script string, scenario string) (string, error) {
	t.Helper()

	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "getent"), `#!/bin/sh
case "$1:$2:$SCENARIO" in
  group:example-app:group-only) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:group-only) exit 2 ;;
  group:example-app:user-only) exit 2 ;;
  passwd:example-app:user-only) echo "example-app:x:998:998::/var/lib/example-app:/usr/sbin/nologin"; exit 0 ;;
  group:example-app:mismatched-group) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:mismatched-group) echo "example-app:x:998:997::/var/lib/example-app:/usr/sbin/nologin"; exit 0 ;;
  group:example-app:mismatched-home) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:mismatched-home) echo "example-app:x:998:998::/srv/example-app:/usr/sbin/nologin"; exit 0 ;;
  group:example-app:login-shell) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:login-shell) echo "example-app:x:998:998::/var/lib/example-app:/bin/sh"; exit 0 ;;
  group:example-app:regular-uid) echo "example-app:x:1001:"; exit 0 ;;
  passwd:example-app:regular-uid) echo "example-app:x:1001:1001::/var/lib/example-app:/usr/sbin/nologin"; exit 0 ;;
  group:example-app:matching) echo "example-app:x:998:"; exit 0 ;;
  passwd:example-app:matching) echo "example-app:x:998:998::/var/lib/example-app:/usr/sbin/nologin"; exit 0 ;;
esac
exit 2
`)
	writeExecutable(t, filepath.Join(dir, "groupadd"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(dir, "useradd"), "#!/bin/sh\nexit 0\n")

	cmd := exec.Command("sh", "-c", script, "meshify-app-user", "example-app", "/var/lib/example-app")
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SCENARIO="+scenario)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func runGuardCommandWithNginxDump(t *testing.T, command host.Command, dump string) ([]byte, error) {
	t.Helper()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", binDir, err)
	}
	dumpPath := filepath.Join(dir, "nginx.dump")
	if err := os.WriteFile(dumpPath, []byte(dump), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", dumpPath, err)
	}
	writeExecutable(t, filepath.Join(binDir, "nginx"), `#!/bin/sh
if [ "$1" = "-T" ]; then
    cat "$NGINX_DUMP_PATH"
    exit 0
fi
exit 64
`)

	cmd := exec.Command("sh", append([]string{"-c", command.Args[1]}, command.Args[2:]...)...)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"), "NGINX_DUMP_PATH="+dumpPath)
	return cmd.CombinedOutput()
}

func nginxDump(sections map[string]string) string {
	paths := make([]string, 0, len(sections))
	for path := range sections {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var builder strings.Builder
	for _, path := range paths {
		builder.WriteString("# configuration file ")
		builder.WriteString(path)
		builder.WriteString(":\n")
		builder.WriteString(sections[path])
		if !strings.HasSuffix(sections[path], "\n") {
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func writeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
