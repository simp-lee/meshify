package tailscale

import (
	"context"
	"errors"
	"lanpanel/internal/host"
	"lanpanel/internal/preflight"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNewRepositoryPlanUsesStableDebianKeyringList(t *testing.T) {
	t.Parallel()

	plan, err := NewRepositoryPlan(preflight.PlatformInfo{ID: "debian", VersionID: "13"})
	if err != nil {
		t.Fatalf("NewRepositoryPlan() error = %v", err)
	}
	if plan.Distribution != "debian" || plan.Codename != "trixie" {
		t.Fatalf("plan = %#v, want debian trixie", plan)
	}
	commands := strings.Join(commandStrings(plan.Commands), "\n")
	for _, want := range []string{
		"apt-get update",
		"apt-get install -y ca-certificates curl",
		"https://pkgs.tailscale.com/stable/debian/trixie.noarmor.gpg",
		"https://pkgs.tailscale.com/stable/debian/trixie.tailscale-keyring.list",
		"apt-get install -y tailscale",
	} {
		if !strings.Contains(commands, want) {
			t.Fatalf("commands missing %q\n%s", want, commands)
		}
	}
}

func TestNewRepositoryPlanSupportsUbuntuLTSAndRejectsUnknown(t *testing.T) {
	t.Parallel()

	plan, err := NewRepositoryPlan(preflight.PlatformInfo{ID: "ubuntu", VersionID: "24.04"})
	if err != nil {
		t.Fatalf("NewRepositoryPlan() error = %v", err)
	}
	if plan.Codename != "noble" {
		t.Fatalf("Codename = %q, want noble", plan.Codename)
	}

	if _, err := NewRepositoryPlan(preflight.PlatformInfo{ID: "alpine", VersionID: "3.20"}); err == nil {
		t.Fatal("NewRepositoryPlan() error = nil, want unsupported distro failure")
	}
}

func TestNewRepositoryPlanSupportsUbuntuResolute(t *testing.T) {
	t.Parallel()

	plan, err := NewRepositoryPlan(preflight.PlatformInfo{ID: "ubuntu", VersionID: "26.04"})
	if err != nil {
		t.Fatalf("NewRepositoryPlan() error = %v", err)
	}
	if plan.Codename != "resolute" {
		t.Fatalf("Codename = %q, want resolute", plan.Codename)
	}
	commands := strings.Join(commandStrings(plan.Commands), "\n")
	for _, want := range []string{
		"https://pkgs.tailscale.com/stable/ubuntu/resolute.noarmor.gpg",
		"https://pkgs.tailscale.com/stable/ubuntu/resolute.tailscale-keyring.list",
	} {
		if !strings.Contains(commands, want) {
			t.Fatalf("commands missing %q\n%s", want, commands)
		}
	}
}

func TestNewRepositoryPlanRejectsArchivedDebianBuster(t *testing.T) {
	t.Parallel()

	_, err := NewRepositoryPlan(preflight.PlatformInfo{ID: "debian", VersionID: "10"})
	if err == nil {
		t.Fatal("NewRepositoryPlan() error = nil, want unsupported archived Debian release")
	}
	if !strings.Contains(err.Error(), "unsupported Tailscale apt repository target") {
		t.Fatalf("NewRepositoryPlan() error = %v, want unsupported target", err)
	}
}

func TestRepositoryFileCommandDoesNotHideCurlFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), "#!/bin/sh\nexit 22\n")
	target := filepath.Join(dir, "repo", "tailscale.list")
	command := repositoryFileCommand("install-test-repo", "https://pkgs.tailscale.com/stable/debian/trixie.tailscale-keyring.list", target)
	script := command.Args[1]
	if strings.Contains(script, " | ") || strings.Contains(script, " tee ") {
		t.Fatalf("repository script = %q, must not pipe curl through tee", script)
	}

	cmd := exec.Command("sh", "-c", script, "lanpanel-tailscale-repo-file", command.Args[3], target)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("repository script error = nil, want curl failure; output:\n%s", output)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("target stat error = %v, want target not written after curl failure", statErr)
	}
}

func TestRepositoryFileCommandInstallsDownloadedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
out=
while [ "$#" -gt 0 ]; do
    if [ "$1" = "-o" ]; then
        shift
        out=$1
    fi
    shift || true
done
[ -n "$out" ] || exit 64
printf 'repo file\n' > "$out"
`)
	target := filepath.Join(dir, "repo", "tailscale.list")
	command := repositoryFileCommand("install-test-repo", "https://pkgs.tailscale.com/stable/debian/trixie.tailscale-keyring.list", target)

	cmd := exec.Command("sh", "-c", command.Args[1], "lanpanel-tailscale-repo-file", command.Args[3], target)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("repository script error = %v; output:\n%s", err, output)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(target) error = %v", err)
	}
	if string(content) != "repo file\n" {
		t.Fatalf("target content = %q, want downloaded content", content)
	}
	marker, err := os.ReadFile(target + ".lanpanel-managed")
	if err != nil {
		t.Fatalf("ReadFile(marker) error = %v", err)
	}
	if !strings.Contains(string(marker), "Lanpanel-managed: tailscale repo file") {
		t.Fatalf("marker = %q, want Lanpanel marker", marker)
	}
}

func TestRepositoryFileCommandRefusesForeignExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
out=
while [ "$#" -gt 0 ]; do
    if [ "$1" = "-o" ]; then
        shift
        out=$1
    fi
    shift || true
done
[ -n "$out" ] || exit 64
printf 'downloaded repo file\n' > "$out"
`)
	target := filepath.Join(dir, "repo", "tailscale.list")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll(repo) error = %v", err)
	}
	if err := os.WriteFile(target, []byte("foreign repo file\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	command := repositoryFileCommand("install-test-repo", "https://pkgs.tailscale.com/stable/debian/trixie.tailscale-keyring.list", target)

	cmd := exec.Command("sh", "-c", command.Args[1], "lanpanel-tailscale-repo-file", command.Args[3], target)
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("repository script error = nil, want foreign file refusal; output:\n%s", output)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile(target) error = %v", readErr)
	}
	if string(content) != "foreign repo file\n" {
		t.Fatalf("target content = %q, want original foreign content", content)
	}
}

func TestNewUpCommandUsesFixedPolicyAndMasksAuthKey(t *testing.T) {
	t.Parallel()

	for _, authKey := range []string{"tskey-auth-1234567890", "hskey-auth-1234567890"} {
		authKey := authKey
		t.Run(authKey, func(t *testing.T) {
			t.Parallel()
			command, err := NewUpCommand(LoginPlan{
				LoginServer: "https://hs.example.com",
				AuthKey:     authKey,
			})
			if err != nil {
				t.Fatalf("NewUpCommand() error = %v", err)
			}
			actual := strings.Join(command.Args, " ")
			for _, want := range []string{
				"up",
				"--login-server https://hs.example.com",
				"--auth-key " + authKey,
				"--accept-dns=false",
				"--accept-routes=false",
				"--shields-up",
			} {
				if !strings.Contains(actual, want) {
					t.Fatalf("args = %q, want %q", actual, want)
				}
			}
			display := command.String()
			if strings.Contains(display, authKey) {
				t.Fatalf("display leaks auth key: %q", display)
			}
			if strings.Contains(display, "--hostname") {
				t.Fatalf("display includes hostname despite empty hostname: %q", display)
			}
		})
	}
}

func TestParseStatusJSONKeepsParsingNarrow(t *testing.T) {
	t.Parallel()

	status, err := ParseStatusJSON([]byte(`{"BackendState":"Running","Self":{"Online":true},"Peer":{}}`))
	if err != nil {
		t.Fatalf("ParseStatusJSON() error = %v", err)
	}
	if !status.LoggedIn || !status.Online || status.BackendState != "Running" {
		t.Fatalf("status = %#v, want logged-in running online", status)
	}

	status, err = ParseStatusJSON([]byte(`{"BackendState":"NeedsLogin"}`))
	if err != nil {
		t.Fatalf("ParseStatusJSON(needs-login) error = %v", err)
	}
	if status.LoggedIn {
		t.Fatalf("LoggedIn = true, want false when Self is missing")
	}
}

func TestMarkerMatchesExpectedLoginServerAndPolicy(t *testing.T) {
	t.Parallel()

	marker := NewMarker("https://hs.example.com", "")
	if !marker.Matches("https://hs.example.com", "") {
		t.Fatalf("marker should match expected login server: %#v", marker)
	}
	if marker.Matches("https://other.example.com", "") {
		t.Fatal("marker matched wrong login server")
	}
	marker.AcceptDNS = true
	if marker.Matches("https://hs.example.com", "") {
		t.Fatal("marker matched wrong DNS policy")
	}

	marker = NewMarker("https://hs.example.com", "app-host")
	if !marker.Matches("https://hs.example.com", "") {
		t.Fatal("marker with explicit hostname should match an empty requested hostname because empty means no override")
	}
	if !marker.Matches("https://hs.example.com", "app-host") {
		t.Fatal("marker did not match expected hostname")
	}
	if marker.Matches("https://hs.example.com", "other-host") {
		t.Fatal("marker matched wrong hostname")
	}
}

func TestParsePrefsJSONExtractsNormalizedControlURL(t *testing.T) {
	t.Parallel()

	prefs, err := ParsePrefsJSON([]byte(`{"ControlURL":"https://HS.EXAMPLE.COM:443/"}`))
	if err != nil {
		t.Fatalf("ParsePrefsJSON() error = %v", err)
	}
	if prefs.ControlURL != "https://hs.example.com" {
		t.Fatalf("ControlURL = %q, want https://hs.example.com", prefs.ControlURL)
	}
}

func writeExecutable(t *testing.T, path string, content string) {
	t.Helper()

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		t.Fatalf("CreateTemp(%s) error = %v", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		t.Fatalf("WriteString(%s) error = %v", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("Close(%s) error = %v", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		t.Fatalf("Chmod(%s) error = %v", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatalf("Rename(%s, %s) error = %v", tmpPath, path, err)
	}
}

func TestReadAuthKeyFileRequiresRootOnlyMode(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "auth.key")
	if err := os.WriteFile(path, []byte("tskey-auth-test\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if os.Geteuid() == 0 {
		key, err := ReadAuthKeyFile(path)
		if err != nil {
			t.Fatalf("ReadAuthKeyFile() error = %v", err)
		}
		if key != "tskey-auth-test" {
			t.Fatalf("key = %q, want tskey-auth-test", key)
		}
	} else {
		_, err := ReadAuthKeyFile(path)
		if err == nil || (!strings.Contains(err.Error(), "owned by root") && !strings.Contains(err.Error(), "must not be writable by group or others")) {
			t.Fatalf("ReadAuthKeyFile() error = %v, want root ownership or parent directory failure", err)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if _, err := ReadAuthKeyFile(path); err == nil {
		t.Fatal("ReadAuthKeyFile() error = nil, want mode failure")
	}
}

func TestAuthKeyFileInfoRequiresRootOwner(t *testing.T) {
	t.Parallel()

	if err := validateAuthKeyFileInfo(fakeAuthKeyInfo{mode: 0o600, uid: 0}); err != nil {
		t.Fatalf("validateAuthKeyFileInfo(root) error = %v", err)
	}
	if err := validateAuthKeyFileInfo(fakeAuthKeyInfo{mode: 0o600, uid: 1000}); err == nil || !strings.Contains(err.Error(), "owned by root") {
		t.Fatalf("validateAuthKeyFileInfo(user) error = %v, want root owner failure", err)
	}
	if err := validateAuthKeyFileInfo(fakeAuthKeyInfo{mode: os.ModeNamedPipe | 0o600, uid: 0}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("validateAuthKeyFileInfo(pipe) error = %v, want regular file failure", err)
	}
}

func TestValidateAuthKeyRejectsWrongShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		raw  string
		want string
	}{
		{name: "empty", key: "", raw: "", want: "empty"},
		{name: "wrong prefix", key: "tskey-api-secret", raw: "tskey-api-secret\n", want: "tskey-auth- or hskey-auth-"},
		{name: "legacy prefix", key: "authkey-secret", raw: "authkey-secret\n", want: "tskey-auth- or hskey-auth-"},
		{name: "inline whitespace", key: "tskey-auth-secret extra", raw: "tskey-auth-secret extra\n", want: "exactly one token"},
		{name: "multiline", key: "tskey-auth-secret\nother", raw: "tskey-auth-secret\nother\n", want: "exactly one token"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateAuthKey(tt.key, tt.raw)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateAuthKey() error = %v, want %q", err, tt.want)
			}
		})
	}
	if err := validateAuthKey("tskey-auth-secret", "tskey-auth-secret\n"); err != nil {
		t.Fatalf("validateAuthKey(valid) error = %v", err)
	}
	if err := validateAuthKey("hskey-auth-secret", "hskey-auth-secret\n"); err != nil {
		t.Fatalf("validateAuthKey(headscale) error = %v", err)
	}
}

func TestReadAuthKeyFileRejectsSymlink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.key")
	if err := os.WriteFile(target, []byte("tskey-auth-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	link := filepath.Join(dir, "auth.key")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := ReadAuthKeyFile(link); err == nil {
		t.Fatal("ReadAuthKeyFile(symlink) error = nil, want failure")
	}
}

func TestReadAuthKeyFileRejectsWritableParentDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("Chmod(temp dir) error = %v", err)
	}
	path := filepath.Join(dir, "auth.key")
	if err := os.WriteFile(path, []byte("tskey-auth-secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(auth key) error = %v", err)
	}
	_, err := ReadAuthKeyFile(path)
	if err == nil || !strings.Contains(err.Error(), "must not be writable by group or others") {
		t.Fatalf("ReadAuthKeyFile() error = %v, want writable parent directory failure", err)
	}
}

func TestEnsureUsesAuthKeyFuncWithoutRepeatingServiceEnable(t *testing.T) {
	t.Parallel()

	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"NeedsLogin"}`},
			{},
			{},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	authKeyCalls := 0
	_, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		AuthKeyFunc: func(context.Context) (string, error) {
			authKeyCalls++
			return "tskey-auth-secret", nil
		},
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if authKeyCalls != 1 {
		t.Fatalf("authKeyCalls = %d, want 1", authKeyCalls)
	}
	enableCount := 0
	upCount := 0
	for _, command := range runner.commands {
		if command.Name == "systemctl" && strings.Join(command.Args, " ") == "enable --now tailscaled.service" {
			enableCount++
		}
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			upCount++
		}
	}
	if enableCount != 1 {
		t.Fatalf("enableCount = %d, want 1; commands = %#v", enableCount, runner.commands)
	}
	if upCount != 1 {
		t.Fatalf("upCount = %d, want 1; commands = %#v", upCount, runner.commands)
	}
}

func TestEnsureFailsWhenStatusIsUnproven(t *testing.T) {
	t.Parallel()

	runner := &tailscaleEnsureRunner{
		errors: []error{
			nil,
			nil,
			errors.New("tailscale status unavailable"),
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	_, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		AuthKey:     "authkey-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "check tailscale status") {
		t.Fatalf("Ensure() error = %v, want status failure", err)
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up after unproven status: %#v", runner.commands)
		}
	}
}

func TestEnsureFailsWhenMarkerHostnameDiffers(t *testing.T) {
	t.Parallel()

	marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", "old-host"))
	if err != nil {
		t.Fatalf("MarkerInstallCommand() error = %v", err)
	}
	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{Stdout: string(marker.Stdin)},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	_, err = client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		Hostname:    "new-host",
	})
	if err == nil || !strings.Contains(err.Error(), "marker that does not match") {
		t.Fatalf("Ensure() error = %v, want marker mismatch", err)
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up after hostname mismatch: %#v", runner.commands)
		}
	}
}

func TestEnsureFailsWhenLoggedInControlURLDiffers(t *testing.T) {
	t.Parallel()

	marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", ""))
	if err != nil {
		t.Fatalf("MarkerInstallCommand() error = %v", err)
	}
	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://other.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{Stdout: string(marker.Stdin)},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	_, err = client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		AuthKey:     "authkey-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "not https://hs.example.com") {
		t.Fatalf("Ensure() error = %v, want login-server mismatch", err)
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up after login-server mismatch: %#v", runner.commands)
		}
	}
}

func TestEnsureSkipsWhenLoggedInMarkerAndControlURLMatch(t *testing.T) {
	t.Parallel()

	marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", "app-host"))
	if err != nil {
		t.Fatalf("MarkerInstallCommand() error = %v", err)
	}
	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://HS.EXAMPLE.COM:443/","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{Stdout: string(marker.Stdin)},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	result, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		Hostname:    "app-host",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if result.SkippedReason == "" {
		t.Fatal("SkippedReason is empty, want already-joined skip")
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up despite matching state: %#v", runner.commands)
		}
	}
}

func TestEnsureSkipsWhenAlreadyLoggedInWithMarkerHostnameAndNoRequestedHostname(t *testing.T) {
	t.Parallel()

	marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", "first-app-host"))
	if err != nil {
		t.Fatalf("MarkerInstallCommand() error = %v", err)
	}
	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{Stdout: string(marker.Stdin)},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	result, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if result.SkippedReason == "" {
		t.Fatal("SkippedReason is empty, want already-logged skip")
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up despite shared logged-in client: %#v", runner.commands)
		}
	}
}

func TestEnsureSkipsWhenLoggedInControlURLAndPolicyMatchWithoutMarker(t *testing.T) {
	t.Parallel()

	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{},
		},
		errors: []error{
			nil,
			nil,
			nil,
			nil,
			os.ErrNotExist,
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	result, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if result.SkippedReason == "" {
		t.Fatal("SkippedReason is empty, want already-logged skip")
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up despite matching live prefs: %#v", runner.commands)
		}
	}
}

func TestEnsureFailsWhenHostnameRequestedAndMarkerMissing(t *testing.T) {
	t.Parallel()

	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{},
		},
		errors: []error{
			nil,
			nil,
			nil,
			nil,
			os.ErrNotExist,
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	_, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		Hostname:    "app-host",
	})
	if err == nil {
		t.Fatal("Ensure() error = nil, want unproven hostname failure")
	}
	if !strings.Contains(err.Error(), "cannot prove requested hostname") {
		t.Fatalf("Ensure() error = %v, want hostname proof failure", err)
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up after unproven hostname: %#v", runner.commands)
		}
	}
}

func TestEnsureSkipsWhenLoggedInControlURLAndPolicyMatchWithMissingMarkerCommandError(t *testing.T) {
	t.Parallel()

	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`},
			{Stderr: "cat: /var/lib/lanpanel/tailscale-client.json: No such file or directory\n", ExitCode: 1},
		},
		errors: []error{
			nil,
			nil,
			nil,
			nil,
			&host.CommandError{Err: errors.New("exit status 1"), Result: host.Result{ExitCode: 1, Stderr: "cat: /var/lib/lanpanel/tailscale-client.json: No such file or directory\n"}},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	result, err := client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
	})
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if result.SkippedReason == "" {
		t.Fatal("SkippedReason is empty, want already-logged skip")
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up despite matching live prefs: %#v", runner.commands)
		}
	}
}

func TestEnsureFailsWhenLoggedInPolicyDrifts(t *testing.T) {
	t.Parallel()

	marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", ""))
	if err != nil {
		t.Fatalf("MarkerInstallCommand() error = %v", err)
	}
	runner := &tailscaleEnsureRunner{
		results: []host.Result{
			{},
			{},
			{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`},
			{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":true,"CorpDNS":true,"ShieldsUp":false}`},
			{Stdout: string(marker.Stdin)},
		},
	}
	client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
	_, err = client.Ensure(context.Background(), EnsurePlan{
		Required:    true,
		LoginServer: "https://hs.example.com",
		AuthKey:     "tskey-auth-secret",
	})
	if err == nil || !strings.Contains(err.Error(), "live policy does not match") {
		t.Fatalf("Ensure() error = %v, want live policy drift failure", err)
	}
	for _, command := range runner.commands {
		if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
			t.Fatalf("Ensure() ran tailscale up after live policy drift: %#v", runner.commands)
		}
	}
}

func TestEnsureFailsWhenLoggedInClientIsNotReady(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status string
	}{
		{
			name:   "stopped",
			status: `{"BackendState":"Stopped","Self":{"Online":true}}`,
		},
		{
			name:   "offline",
			status: `{"BackendState":"Running","Self":{"Online":false}}`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			marker, err := MarkerInstallCommand(NewMarker("https://hs.example.com", ""))
			if err != nil {
				t.Fatalf("MarkerInstallCommand() error = %v", err)
			}
			runner := &tailscaleEnsureRunner{
				results: []host.Result{
					{},
					{},
					{Stdout: tt.status},
					{Stdout: string(marker.Stdin)},
				},
			}
			client := NewClient(host.NewExecutor(runner, nil), preflight.PlatformInfo{})
			_, err = client.Ensure(context.Background(), EnsurePlan{
				Required:    true,
				LoginServer: "https://hs.example.com",
				AuthKey:     "authkey-secret",
			})
			if err == nil || !strings.Contains(err.Error(), "not ready") {
				t.Fatalf("Ensure() error = %v, want not-ready failure", err)
			}
			for _, command := range runner.commands {
				if command.Name == "tailscale" && len(command.Args) > 0 && command.Args[0] == "up" {
					t.Fatalf("Ensure() ran tailscale up after not-ready status: %#v", runner.commands)
				}
			}
		})
	}
}

func commandStrings(commands []host.Command) []string {
	values := make([]string, 0, len(commands))
	for _, command := range commands {
		values = append(values, command.String())
	}
	return values
}

type fakeAuthKeyInfo struct {
	mode os.FileMode
	uid  uint32
	dir  bool
}

func (info fakeAuthKeyInfo) Name() string       { return "auth.key" }
func (info fakeAuthKeyInfo) Size() int64        { return 12 }
func (info fakeAuthKeyInfo) Mode() os.FileMode  { return info.mode }
func (info fakeAuthKeyInfo) ModTime() time.Time { return time.Time{} }
func (info fakeAuthKeyInfo) IsDir() bool        { return info.dir }
func (info fakeAuthKeyInfo) Sys() any           { return &syscall.Stat_t{Uid: info.uid} }

type tailscaleEnsureRunner struct {
	commands []host.Command
	results  []host.Result
	errors   []error
}

func (runner *tailscaleEnsureRunner) Run(_ context.Context, command host.Command) (host.Result, error) {
	index := len(runner.commands)
	runner.commands = append(runner.commands, command)
	result := host.Result{Command: command}
	if index < len(runner.results) {
		result = runner.results[index]
		result.Command = command
	}
	if index < len(runner.errors) && runner.errors[index] != nil {
		return result, runner.errors[index]
	}
	return result, nil
}
