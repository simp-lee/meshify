package cli

import (
	"bytes"
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"meshify/internal/assets"
	"meshify/internal/components/headscale"
	legocomponent "meshify/internal/components/lego"
	"meshify/internal/config"
	"meshify/internal/host"
	"meshify/internal/output"
	"meshify/internal/preflight"
	"meshify/internal/render"
	"meshify/internal/state"
	"meshify/internal/workflow"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := Execute(args, &stdout, &stderr, "test")
	return stdout.String(), stderr.String(), err
}

func fieldValue(fields []output.Field, label string) (string, bool) {
	for _, field := range fields {
		if field.Label == label {
			return field.Value, true
		}
	}
	return "", false
}

func TestExecute_HelpOutput(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCLI(t)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	for _, want := range []string{
		"meshify manages init, deploy, verify, and status workflows.",
		"Happy path:",
		"meshify init",
		"meshify deploy",
		"meshify verify",
		"status   Show config readiness and persisted deploy context.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestParsePlatformInfoFromOSReleaseFallsBackToUsrLib(t *testing.T) {
	t.Parallel()

	readPaths := []string{}
	info := parsePlatformInfoFromOSRelease(func(path string) ([]byte, error) {
		readPaths = append(readPaths, path)
		switch path {
		case "/etc/os-release":
			return nil, os.ErrNotExist
		case "/usr/lib/os-release":
			return []byte("ID=debian\nID_LIKE=debian\nVERSION_ID=12\nPRETTY_NAME=\"Debian GNU/Linux 12\"\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}, "/etc/os-release", "/usr/lib/os-release")

	if strings.Join(readPaths, ",") != "/etc/os-release,/usr/lib/os-release" {
		t.Fatalf("read paths = %v, want /etc then /usr/lib fallback", readPaths)
	}
	if info.ID != "debian" || info.IDLike != "debian" || info.VersionID != "12" {
		t.Fatalf("platform info = %#v, want Debian fallback data", info)
	}
}

func TestExecute_InitWritesExampleConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	stdout, stderr, err := runCLI(t, "init", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify init: wrote example config") {
		t.Fatalf("stdout = %q, want init summary", stdout)
	}
	if !strings.Contains(stdout, configPath) {
		t.Fatalf("stdout = %q, want config path %q", stdout, configPath)
	}

	loaded, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if loaded.Default.ServerURL != "https://hs.example.com" {
		t.Fatalf("default.server_url = %q, want example value", loaded.Default.ServerURL)
	}
	if loaded.Default.BaseDomain != "tailnet.example.com" {
		t.Fatalf("default.base_domain = %q, want example value", loaded.Default.BaseDomain)
	}
	if loaded.Default.CertificateEmail != "ops@example.com" {
		t.Fatalf("default.certificate_email = %q, want example value", loaded.Default.CertificateEmail)
	}
}

func TestExecute_InitInvalidFormatDoesNotWriteConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	stdout, stderr, err := runCLI(t, "init", "--config", configPath, "--format", "yaml")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "unsupported output format \"yaml\"") {
		t.Fatalf("error = %q, want unsupported format", err.Error())
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("os.Stat() error = %v, want %v", statErr, os.ErrNotExist)
	}
}

func TestExecute_InitRejectsAdvancedExampleFlagCombo(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	stdout, stderr, err := runCLI(t, "init", "--config", configPath, "--advanced", "--example")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "--advanced and --example cannot be used together") {
		t.Fatalf("error = %q, want invalid-flag-combo error", err.Error())
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("os.Stat() error = %v, want %v", statErr, os.ErrNotExist)
	}
}

func TestExecute_InitAdvancedWithoutPromptInputDoesNotWriteConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	stdout, stderr, err := runCLI(t, "init", "--config", configPath, "--advanced")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "guided init requires interactive input") {
		t.Fatalf("error = %q, want prompt-unavailable error", err.Error())
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if _, statErr := os.Stat(configPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("os.Stat() error = %v, want %v", statErr, os.ErrNotExist)
	}
}

func TestExecute_DeployJSONSummary(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	previousPermissionState := detectPermissionStateFn
	previousPlatformInfo := detectPlatformInfoFn
	previousHostCapabilityState := detectHostCapabilityStateFn
	previousDNSProbe := detectDNSProbeFn
	previousPortBindings := detectPortBindingsFn
	previousFirewallState := detectFirewallStateFn
	previousServiceStates := detectServiceStatesFn
	previousACMEState := detectACMEStateFn
	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		detectPermissionStateFn = previousPermissionState
		detectPlatformInfoFn = previousPlatformInfo
		detectHostCapabilityStateFn = previousHostCapabilityState
		detectDNSProbeFn = previousDNSProbe
		detectPortBindingsFn = previousPortBindings
		detectFirewallStateFn = previousFirewallState
		detectServiceStatesFn = previousServiceStates
		detectACMEStateFn = previousACMEState
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}
	detectPlatformInfoFn = func() preflight.PlatformInfo {
		return preflight.PlatformInfo{ID: "debian", VersionID: "13", PrettyName: "Debian GNU/Linux 13"}
	}
	detectHostCapabilityStateFn = passingHostCapabilities
	detectDNSProbeFn = func(serverURL string) preflight.DNSProbe {
		return preflight.DNSProbe{Host: "hs.example.com", ResolvedIPs: []string{"8.8.8.8"}}
	}
	detectPortBindingsFn = func(config.Config) []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp", InUse: false},
			{Port: 443, Protocol: "tcp", InUse: false},
			{Port: 3478, Protocol: "udp", InUse: true, Process: "coturn"},
		}
	}
	detectFirewallStateFn = func() preflight.FirewallState {
		return preflight.FirewallState{
			Inspected:    true,
			Active:       true,
			AllowedPorts: []string{"80/tcp", "443/tcp", "3478/udp"},
		}
	}
	detectServiceStatesFn = func() []preflight.ServiceState {
		return []preflight.ServiceState{{Name: "nginx", Active: true, Detail: "running"}}
	}
	probePackageURLFn = func(_ *http.Client, rawURL string) (bool, bool, string) {
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(_ *http.Client, rawURL string) (string, error) {
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(_ *http.Client, version string, arch string) (string, error) {
		return strings.Repeat("a", 64), nil
	}
	detectACMEStateFn = func(cfg config.Config) preflight.ACMEState {
		return preflight.ACMEState{HTTP01Checked: true, HTTP01Ready: true}
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for blocking preflight")
	}
	if !strings.Contains(err.Error(), "blocked by") {
		t.Fatalf("error = %q, want blocking-preflight text", err.Error())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var response struct {
		Command   string                  `json:"command"`
		Status    preflight.Status        `json:"status"`
		Summary   string                  `json:"summary"`
		Checks    []preflight.CheckResult `json:"checks"`
		NextSteps []string                `json:"next_steps"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
	}
	if response.Command != "deploy" {
		t.Fatalf("response.Command = %q, want %q", response.Command, "deploy")
	}
	if response.Status != preflight.StatusFail {
		t.Fatalf("response.Status = %q, want %q", response.Status, preflight.StatusFail)
	}
	if !strings.Contains(response.Summary, "blocked by") {
		t.Fatalf("response.Summary = %q, want blocked-summary text", response.Summary)
	}
	if len(response.Checks) == 0 {
		t.Fatal("response.Checks = empty, want preflight checks")
	}
	if response.Checks[0].ID != "permissions" {
		t.Fatalf("first check id = %q, want %q", response.Checks[0].ID, "permissions")
	}

	checksByID := map[string]preflight.CheckResult{}
	for _, check := range response.Checks {
		checksByID[check.ID] = check
	}

	if checksByID["ports"].Status != preflight.StatusFail {
		t.Fatalf("ports status = %q, want %q", checksByID["ports"].Status, preflight.StatusFail)
	}
	if checksByID["firewall"].Status != preflight.StatusPass {
		t.Fatalf("firewall status = %q, want %q", checksByID["firewall"].Status, preflight.StatusPass)
	}
	if checksByID["services"].Status != preflight.StatusWarn {
		t.Fatalf("services status = %q, want %q", checksByID["services"].Status, preflight.StatusWarn)
	}
	if checksByID["package-source"].Status != preflight.StatusPass {
		t.Fatalf("package-source status = %q, want %q", checksByID["package-source"].Status, preflight.StatusPass)
	}
	if checksByID["acme"].Status != preflight.StatusPass {
		t.Fatalf("acme status = %q, want %q", checksByID["acme"].Status, preflight.StatusPass)
	}
	if len(response.NextSteps) == 0 {
		t.Fatal("response.NextSteps = empty, want remediation steps")
	}
}

func TestExecute_DeployJSONBlocksOnManualHostChecks(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	previousPermissionState := detectPermissionStateFn
	previousPlatformInfo := detectPlatformInfoFn
	previousHostCapabilityState := detectHostCapabilityStateFn
	previousDNSProbe := detectDNSProbeFn
	previousPortBindings := detectPortBindingsFn
	previousFirewallState := detectFirewallStateFn
	previousServiceStates := detectServiceStatesFn
	previousACMEState := detectACMEStateFn
	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		detectPermissionStateFn = previousPermissionState
		detectPlatformInfoFn = previousPlatformInfo
		detectHostCapabilityStateFn = previousHostCapabilityState
		detectDNSProbeFn = previousDNSProbe
		detectPortBindingsFn = previousPortBindings
		detectFirewallStateFn = previousFirewallState
		detectServiceStatesFn = previousServiceStates
		detectACMEStateFn = previousACMEState
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}
	detectPlatformInfoFn = func() preflight.PlatformInfo {
		return preflight.PlatformInfo{ID: "debian", VersionID: "13", PrettyName: "Debian GNU/Linux 13"}
	}
	detectHostCapabilityStateFn = passingHostCapabilities
	detectDNSProbeFn = func(serverURL string) preflight.DNSProbe {
		return preflight.DNSProbe{Host: "hs.example.com", ResolvedIPs: []string{"8.8.8.8"}}
	}
	detectPortBindingsFn = func(config.Config) []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp", InUse: false},
			{Port: 443, Protocol: "tcp", InUse: false},
		}
	}
	detectFirewallStateFn = func() preflight.FirewallState {
		return preflight.FirewallState{
			Inspected:    true,
			Active:       true,
			AllowedPorts: []string{"80/tcp", "443/tcp", "3478/udp"},
		}
	}
	detectServiceStatesFn = func() []preflight.ServiceState {
		return []preflight.ServiceState{{Name: "nginx", Active: true, Detail: "running"}}
	}
	probePackageURLFn = func(_ *http.Client, rawURL string) (bool, bool, string) {
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(_ *http.Client, rawURL string) (string, error) {
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(_ *http.Client, version string, arch string) (string, error) {
		return strings.Repeat("a", 64), nil
	}
	detectACMEStateFn = func(cfg config.Config) preflight.ACMEState {
		return preflight.ACMEState{HTTP01Checked: true, HTTP01Ready: true}
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for blocking manual preflight")
	}
	if !strings.Contains(err.Error(), "waiting on") {
		t.Fatalf("error = %q, want manual-blocking text", err.Error())
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	var response struct {
		Command string           `json:"command"`
		Status  preflight.Status `json:"status"`
		Summary string           `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
	}
	if response.Command != "deploy" {
		t.Fatalf("response.Command = %q, want %q", response.Command, "deploy")
	}
	if response.Status != preflight.StatusManual {
		t.Fatalf("response.Status = %q, want %q", response.Status, preflight.StatusManual)
	}
	if !strings.Contains(response.Summary, "waiting on") {
		t.Fatalf("response.Summary = %q, want manual summary", response.Summary)
	}
}

func TestDetectDNSCredentialStateRequiresOfficialProviderCredentials(t *testing.T) {
	clearEnv := func(keys ...string) {
		t.Helper()
		for _, key := range keys {
			t.Setenv(key, "")
		}
	}

	t.Run("cloudflare env_file is validated with official lego environment", func(t *testing.T) {
		clearEnv(
			"CLOUDFLARE_EMAIL",
			"CLOUDFLARE_API_KEY",
			"CF_API_EMAIL",
			"CF_API_KEY",
			"CLOUDFLARE_DNS_API_TOKEN",
			"CF_DNS_API_TOKEN",
			"CLOUDFLARE_ZONE_API_TOKEN",
			"CF_ZONE_API_TOKEN",
		)
		dir := t.TempDir()
		tokenFile := filepath.Join(dir, "cloudflare-token")
		if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(dir, "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, tokenFile, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "cloudflare", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file guidance", detail)
		}
	})

	t.Run("cloudflare unreadable env_file is validated through deploy privileges", func(t *testing.T) {
		dir := t.TempDir()
		tokenFile := filepath.Join(dir, "cloudflare-token")
		if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(dir, "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, tokenFile, envFile)
		previousRead := readDNSCredentialsFileFn
		previousPermission := detectPermissionStateFn
		previousExecutor := newHostExecutorFn
		runner := &scriptedHostRunner{}
		t.Cleanup(func() {
			readDNSCredentialsFileFn = previousRead
			detectPermissionStateFn = previousPermission
			newHostExecutorFn = previousExecutor
		})
		readDNSCredentialsFileFn = func(string) ([]byte, error) {
			return nil, &os.PathError{Op: "open", Path: envFile, Err: os.ErrPermission}
		}
		detectPermissionStateFn = func() preflight.PermissionState {
			return preflight.PermissionState{User: "deployer", SudoInstalled: true, SudoWorks: true}
		}
		runner.run = func(command host.Command) (host.Result, error) {
			actual := unwrapMaybeSudoHostCommand(command)
			if actual.Name != "cat" {
				t.Fatalf("command = %q, want privileged cat", command.String())
			}
			return host.Result{Command: command, Stdout: "CLOUDFLARE_DNS_API_TOKEN_FILE=" + tokenFile + "\n"}, nil
		}
		newHostExecutorFn = func(env map[string]string) host.Executor {
			return host.NewExecutor(runner, env)
		}

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "cloudflare", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true; detail = %q", detail)
		}
		if !strings.Contains(detail, "Validated env_file content through deploy privileges") {
			t.Fatalf("detail = %q, want privileged validation detail", detail)
		}
		if len(runner.commands) != 1 || runner.commands[0].Name != "sudo" || strings.Contains(runner.commands[0].String(), envFile) {
			t.Fatalf("commands = %#v, want sudo validation with redacted display path", runner.commands)
		}
	})

	t.Run("cloudflare unreadable env_file is not ready without deploy privileges", func(t *testing.T) {
		dir := t.TempDir()
		tokenFile := filepath.Join(dir, "cloudflare-token")
		if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(dir, "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, tokenFile, envFile)
		previousRead := readDNSCredentialsFileFn
		previousPermission := detectPermissionStateFn
		t.Cleanup(func() {
			readDNSCredentialsFileFn = previousRead
			detectPermissionStateFn = previousPermission
		})
		readDNSCredentialsFileFn = func(string) ([]byte, error) {
			return nil, &os.PathError{Op: "open", Path: envFile, Err: os.ErrPermission}
		}
		detectPermissionStateFn = func() preflight.PermissionState {
			return preflight.PermissionState{User: "deployer", SudoInstalled: true}
		}

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "cloudflare", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false without privileged validation")
		}
		if !strings.Contains(detail, "deploy privileges are not available") {
			t.Fatalf("detail = %q, want privilege validation failure", detail)
		}
	})

	t.Run("cloudflare env_file with group or other permissions is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN=token-value\n"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "cloudflare", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for world-readable credential env_file")
		}
		if !strings.Contains(detail, "readable only by root") {
			t.Fatalf("detail = %q, want file permission guidance", detail)
		}
	})

	t.Run("cloudflare env_file with invalid variables is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("UNRELATED_TOKEN=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false")
		}
		if !strings.Contains(detail, "env_file for DNS provider cloudflare") {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("cloudflare env_file with raw token is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for raw DNS token in systemd env_file")
		}
		if !strings.Contains(detail, "CLOUDFLARE_DNS_API_TOKEN must not be set directly") {
			t.Fatalf("detail = %q, want raw-secret rejection", detail)
		}
	})

	t.Run("cloudflare missing token file referenced by env_file is not ready", func(t *testing.T) {
		missingFile := filepath.Join(t.TempDir(), "missing-cf-token")
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CF_DNS_API_TOKEN_FILE="+missingFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for missing Cloudflare token file")
		}
		if !strings.Contains(detail, "credential file references") || !strings.Contains(detail, "CF_DNS_API_TOKEN_FILE") || !strings.Contains(detail, missingFile) {
			t.Fatalf("detail = %q, want Cloudflare _FILE reference failure", detail)
		}
	})

	t.Run("cloudflare relative token file referenced by env_file is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CF_DNS_API_TOKEN_FILE=cf-token\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for relative Cloudflare token file")
		}
		if !strings.Contains(detail, "CF_DNS_API_TOKEN_FILE") || !strings.Contains(detail, "must be absolute") {
			t.Fatalf("detail = %q, want relative _FILE path failure", detail)
		}
	})

	t.Run("digitalocean missing token file referenced by env_file is not ready", func(t *testing.T) {
		missingFile := filepath.Join(t.TempDir(), "missing-do-token")
		envFile := filepath.Join(t.TempDir(), "digitalocean.env")
		if err := os.WriteFile(envFile, []byte("DO_AUTH_TOKEN_FILE="+missingFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "digitalocean",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for missing DigitalOcean token file")
		}
		if !strings.Contains(detail, "credential file references") || !strings.Contains(detail, "DO_AUTH_TOKEN_FILE") || !strings.Contains(detail, missingFile) {
			t.Fatalf("detail = %q, want DigitalOcean _FILE reference failure", detail)
		}
	})

	t.Run("digitalocean raw token in env_file is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "digitalocean.env")
		if err := os.WriteFile(envFile, []byte("DO_AUTH_TOKEN=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "digitalocean",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for raw DigitalOcean token in systemd env_file")
		}
		if !strings.Contains(detail, "DO_AUTH_TOKEN must not be set directly") {
			t.Fatalf("detail = %q, want raw DigitalOcean token rejection", detail)
		}
	})

	t.Run("cloudflare env_file with shell export syntax is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("export CLOUDFLARE_DNS_API_TOKEN=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for shell export syntax")
		}
		if !strings.Contains(detail, "systemd EnvironmentFile") || !strings.Contains(detail, "without export") {
			t.Fatalf("detail = %q, want systemd EnvironmentFile syntax guidance", detail)
		}
	})

	t.Run("cloudflare env_file with invalid variable name is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("CF-DNS-API-TOKEN=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for invalid env_file variable name")
		}
		if !strings.Contains(detail, "systemd EnvironmentFile") || !strings.Contains(detail, "invalid environment variable name") {
			t.Fatalf("detail = %q, want invalid variable name guidance", detail)
		}
	})

	t.Run("cloudflare env_file with lowercase variable name is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "cloudflare.env")
		if err := os.WriteFile(envFile, []byte("cf_dns_api_token=token-value\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  envFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for lowercase lego variable")
		}
		if !strings.Contains(detail, "exact uppercase lego variable name CF_DNS_API_TOKEN") {
			t.Fatalf("detail = %q, want exact uppercase variable guidance", detail)
		}
	})

	t.Run("cloudflare missing env_file is not ready", func(t *testing.T) {
		missingFile := filepath.Join(t.TempDir(), "missing-cloudflare.env")

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{
			Provider: "cloudflare",
			EnvFile:  missingFile,
		})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false")
		}
		if !strings.Contains(detail, "not ready") || !strings.Contains(detail, missingFile) {
			t.Fatalf("detail = %q, want missing env_file detail", detail)
		}
	})

	t.Run("provider names are matched by supported alias only", func(t *testing.T) {
		clearEnv("CLOUDFLARE_DNS_API_TOKEN", "CF_DNS_API_TOKEN")
		t.Setenv("CLOUDFLARE_DNS_API_TOKEN", "token-value")

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "not-cloudflare"})
		if checked {
			t.Fatal("checked = true, want false for unsupported provider alias")
		}
		if ready {
			t.Fatal("ready = true, want false for unsupported provider alias")
		}
		if !strings.Contains(detail, "unsupported DNS-01 provider") {
			t.Fatalf("detail = %q, want unsupported-provider detail", detail)
		}
	})

	t.Run("route53 raw access key pair is not deploy-ready across sudo", func(t *testing.T) {
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)
		t.Setenv("AWS_ACCESS_KEY_ID", "key")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53"})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for raw AWS secrets that meshify will not pass through sudo")
		}
		if !strings.Contains(detail, "will not pass raw AWS secrets") || !strings.Contains(detail, "advanced.dns01.env_file") {
			t.Fatalf("detail = %q, want safe Route53 credential guidance", detail)
		}
	})

	t.Run("route53 shared credentials file in env_file is sufficient", func(t *testing.T) {
		credentialsFile := filepath.Join(t.TempDir(), "credentials")
		if err := os.WriteFile(credentialsFile, []byte("[default]\naws_access_key_id=key\naws_secret_access_key=secret\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(t.TempDir(), "route53.env")
		content := "# meshify route53\n; systemd-style comment\nignored note\nAWS_SHARED_CREDENTIALS_FILE='" + credentialsFile + "'\n"
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, credentialsFile, envFile)
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatal("ready = false, want true")
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("route53 hosted zone only env_file can supplement ambient credentials", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "route53.env")
		if err := os.WriteFile(envFile, []byte("AWS_HOSTED_ZONE_ID=Z1234567890\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true for AWS_HOSTED_ZONE_ID plus ambient credentials; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("route53 raw key pair in env_file is not ready", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "route53.env")
		if err := os.WriteFile(envFile, []byte("AWS_ACCESS_KEY_ID=key\nAWS_SECRET_ACCESS_KEY=secret\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for raw Route53 secrets in systemd env_file")
		}
		if !strings.Contains(detail, "AWS_ACCESS_KEY_ID must not be set directly") {
			t.Fatalf("detail = %q, want raw AWS credential rejection", detail)
		}
	})

	t.Run("route53 missing shared credentials file referenced by env_file is not ready", func(t *testing.T) {
		missingFile := filepath.Join(t.TempDir(), "missing-aws-config")
		envFile := filepath.Join(t.TempDir(), "route53.env")
		if err := os.WriteFile(envFile, []byte("AWS_SHARED_CREDENTIALS_FILE="+missingFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false")
		}
		if !strings.Contains(detail, "credential file references") || !strings.Contains(detail, missingFile) {
			t.Fatalf("detail = %q, want missing config file detail", detail)
		}
	})

	t.Run("route53 without env_file uses ambient credential chain", func(t *testing.T) {
		clearEnv(
			"AWS_ACCESS_KEY_ID",
			"AWS_SECRET_ACCESS_KEY",
			"AWS_CONFIG_FILE",
			"AWS_SHARED_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "route53"})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatal("ready = false, want true for route53 ambient credentials")
		}
		if !strings.Contains(detail, "ambient credential chain") {
			t.Fatalf("detail = %q, want ambient credential detail", detail)
		}
	})

	t.Run("google application credentials with project in env_file is sufficient", func(t *testing.T) {
		credentialsFile := filepath.Join(t.TempDir(), "google.json")
		if err := os.WriteFile(credentialsFile, []byte(`{"type":"service_account"}`+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_PROJECT=meshify-project\nGOOGLE_APPLICATION_CREDENTIALS="+credentialsFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, credentialsFile, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatal("ready = false, want true")
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google application credentials without project in env_file can use metadata project", func(t *testing.T) {
		credentialsFile := filepath.Join(t.TempDir(), "google.json")
		if err := os.WriteFile(credentialsFile, []byte(`{"type":"service_account"}`+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GOOGLE_APPLICATION_CREDENTIALS="+credentialsFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, credentialsFile, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true for GOOGLE_APPLICATION_CREDENTIALS plus ambient project; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google zone id only env_file can supplement ambient credentials", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_ZONE_ID=meshify-zone\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true for GCE_ZONE_ID plus ambient credentials; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google project only in env_file can supplement ambient ADC", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_PROJECT=meshify-project\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true for GCE_PROJECT plus ambient ADC; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google official gcloud service account file in env_file is sufficient", func(t *testing.T) {
		credentialsFile := filepath.Join(t.TempDir(), "gcloud.json")
		if err := os.WriteFile(credentialsFile, []byte(`{"type":"service_account","project_id":"meshify-project"}`+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_SERVICE_ACCOUNT_FILE="+credentialsFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, credentialsFile, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google official gcloud impersonation in env_file is sufficient", func(t *testing.T) {
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_PROJECT=meshify-project\nGCE_IMPERSONATE_SERVICE_ACCOUNT=target-sa@meshify-project.iam.gserviceaccount.com\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_IMPERSONATE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatalf("ready = false, want true; detail = %q", detail)
		}
		if !strings.Contains(detail, "lego env_file") || !strings.Contains(detail, envFile) {
			t.Fatalf("detail = %q, want env_file detail", detail)
		}
	})

	t.Run("google missing application credentials file referenced by env_file is not ready", func(t *testing.T) {
		missingFile := filepath.Join(t.TempDir(), "missing-google.json")
		envFile := filepath.Join(t.TempDir(), "gcloud.env")
		if err := os.WriteFile(envFile, []byte("GCE_PROJECT=meshify-project\nGOOGLE_APPLICATION_CREDENTIALS="+missingFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, envFile)
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false")
		}
		if !strings.Contains(detail, "credential file references") || !strings.Contains(detail, missingFile) {
			t.Fatalf("detail = %q, want missing Google credentials file detail", detail)
		}
	})

	t.Run("google without env_file uses ambient credential chain", func(t *testing.T) {
		clearEnv(
			"GCE_PROJECT",
			"GCE_SERVICE_ACCOUNT",
			"GCE_SERVICE_ACCOUNT_FILE",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_APPLICATION_CREDENTIALS_FILE",
		)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "google"})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if !ready {
			t.Fatal("ready = false, want true for gcloud ambient credentials")
		}
		if !strings.Contains(detail, "ambient credential chain") {
			t.Fatalf("detail = %q, want ambient credential detail", detail)
		}
	})
}

func TestStageDeployFilesUsesMeshifyLegoRenewalAssets(t *testing.T) {
	cfg := config.ExampleConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "route53"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/route53.env"

	files, err := stageDeployFiles(cfg)
	if err != nil {
		t.Fatalf("stageDeployFiles() error = %v", err)
	}
	byHostPath := map[string]render.StagedFile{}
	for _, file := range files {
		byHostPath[file.HostPath] = file
		if strings.Contains(file.HostPath, "certbot") || strings.Contains(file.HostPath, "letsencrypt") {
			t.Fatalf("staged files include legacy certbot asset %#v", file)
		}
	}
	if _, ok := byHostPath["/etc/systemd/system/meshify-lego-renew.service"]; !ok {
		t.Fatalf("staged files = %#v, want lego renewal service", files)
	}
	if _, ok := byHostPath["/etc/systemd/system/meshify-lego-renew.timer"]; !ok {
		t.Fatalf("staged files = %#v, want lego renewal timer", files)
	}
}

func TestInspectDNSCredentialsFileDoesNotApproveUninspectablePath(t *testing.T) {
	previousStat := statDNSCredentialsFileFn
	t.Cleanup(func() {
		statDNSCredentialsFileFn = previousStat
	})
	statDNSCredentialsFileFn = func(path string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "stat", Path: path, Err: os.ErrPermission}
	}

	ready, detail := inspectDNSCredentialsFile("/root/cloudflare.ini")
	if ready {
		t.Fatalf("ready = true, want false when current user cannot inspect file metadata; detail = %q", detail)
	}
	if !strings.Contains(detail, "cannot be inspected") {
		t.Fatalf("detail = %q, want inspection failure guidance", detail)
	}
}

func withRootOwnedStatForPaths(t *testing.T, paths ...string) {
	t.Helper()
	previousStat := statDNSCredentialsFileFn
	pathSet := map[string]struct{}{}
	for _, path := range paths {
		pathSet[path] = struct{}{}
	}
	t.Cleanup(func() {
		statDNSCredentialsFileFn = previousStat
	})
	statDNSCredentialsFileFn = func(path string) (os.FileInfo, error) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if _, ok := pathSet[path]; !ok {
			return info, nil
		}
		return rootOwnedFileInfo{FileInfo: info}, nil
	}
}

type rootOwnedFileInfo struct {
	os.FileInfo
}

func (info rootOwnedFileInfo) Sys() any {
	return struct {
		UID uint32
	}{UID: 0}
}

func TestDeployDesiredStateDigestIgnoresShellCredentialSecretValues(t *testing.T) {
	cfg := config.ExampleConfig()
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "route53"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/route53.env"

	t.Setenv("AWS_ACCESS_KEY_ID", "key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "first-secret")
	first, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest(first) error = %v", err)
	}

	t.Setenv("AWS_SECRET_ACCESS_KEY", "second-secret")
	second, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest(second) error = %v", err)
	}
	if first != second {
		t.Fatalf("desired state digest changed after secret value changed: first=%q second=%q", first, second)
	}
}

func TestParseUFWAllowedPortsRecognizesApplicationProfiles(t *testing.T) {
	t.Parallel()

	raw := `Status: active

To                         Action      From
--                         ------      ----
Nginx Full                 ALLOW       Anywhere
3478/udp                   ALLOW       Anywhere
Nginx Full (v6)            ALLOW       Anywhere (v6)
`

	got := parseUFWAllowedPorts(raw)
	want := map[string]struct{}{
		"80/tcp":   {},
		"443/tcp":  {},
		"3478/udp": {},
	}

	if len(got) != len(want) {
		t.Fatalf("parseUFWAllowedPorts() len = %d, want %d; got = %#v", len(got), len(want), got)
	}
	for _, port := range got {
		if _, ok := want[port]; !ok {
			t.Fatalf("parseUFWAllowedPorts() unexpected port %q in %#v", port, got)
		}
		delete(want, port)
	}
	if len(want) != 0 {
		t.Fatalf("parseUFWAllowedPorts() missing ports %#v", want)
	}
}

func TestParseSSBindingsUsesLocalAddressColumn(t *testing.T) {
	t.Parallel()

	tcpRaw := `LISTEN 0 4096 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=123,fd=6))
LISTEN 0 4096 [::]:443 [::]:* users:(("nginx",pid=123,fd=7))
`
	tcpBindings, tcpDetected := parseSSBindings(tcpRaw, "tcp", []int{80, 443})
	if !tcpDetected {
		t.Fatal("parseSSBindings(tcp) detected = false, want true")
	}
	if !tcpBindings[80].InUse || tcpBindings[80].Process != "nginx" {
		t.Fatalf("tcpBindings[80] = %#v, want nginx listener", tcpBindings[80])
	}
	if !tcpBindings[443].InUse || tcpBindings[443].Process != "nginx" {
		t.Fatalf("tcpBindings[443] = %#v, want nginx listener", tcpBindings[443])
	}

	udpRaw := `UNCONN 0 0 0.0.0.0:3478 0.0.0.0:* users:(("headscale",pid=456,fd=9))
`
	udpBindings, udpDetected := parseSSBindings(udpRaw, "udp", []int{3478})
	if !udpDetected {
		t.Fatal("parseSSBindings(udp) detected = false, want true")
	}
	if !udpBindings[3478].InUse || udpBindings[3478].Process != "headscale" {
		t.Fatalf("udpBindings[3478] = %#v, want headscale listener", udpBindings[3478])
	}
}

func TestParseSSBindingsTreatsUnparseableOutputAsIncomplete(t *testing.T) {
	t.Parallel()

	_, detected := parseSSBindings("LISTEN unexpected-output-without-local-address\n", "tcp", []int{80})
	if detected {
		t.Fatal("parseSSBindings() detected = true, want false for unparseable non-empty output")
	}

	bindings, detected := parseSSBindings("LISTEN 0 4096 127.0.0.1:22 0.0.0.0:*\n", "tcp", []int{80})
	if !detected {
		t.Fatal("parseSSBindings() detected = false, want true for parseable non-required listener")
	}
	if len(bindings) != 0 {
		t.Fatalf("bindings = %#v, want no required port bindings", bindings)
	}
}

func TestNFTRulesetAllowsPortMatchesExactPortsAndRanges(t *testing.T) {
	t.Parallel()

	ruleset := `
table inet filter {
  chain input {
    tcp dport { 80, 443, 8443-8445 } accept
    udp dport { 3478 } accept
  }
}`

	for _, tc := range []struct {
		protocol string
		port     int
	}{
		{protocol: "tcp", port: 80},
		{protocol: "tcp", port: 443},
		{protocol: "tcp", port: 8444},
		{protocol: "udp", port: 3478},
	} {
		if !nftRulesetAllowsPort(ruleset, tc.protocol, tc.port) {
			t.Fatalf("nftRulesetAllowsPort(%q, %d) = false, want true", tc.protocol, tc.port)
		}
	}
}

func TestNFTRulesetAllowsPortDoesNotMatchSubstrings(t *testing.T) {
	t.Parallel()

	ruleset := `
table inet filter {
  chain input {
    tcp dport { 4430 } accept
    udp dport { 34780 } accept
  }
}`

	if nftRulesetAllowsPort(ruleset, "tcp", 443) {
		t.Fatal("nftRulesetAllowsPort(tcp, 443) = true, want false for 4430")
	}
	if nftRulesetAllowsPort(ruleset, "udp", 3478) {
		t.Fatal("nftRulesetAllowsPort(udp, 3478) = true, want false for 34780")
	}
}

func TestExecute_DeployInstallsRuntimeAssetsAndStatusShowsPersistedCheckpoint(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	cfg.Advanced.Proxy.HTTPProxy = "http://proxy.internal:8080"
	cfg.Advanced.Proxy.HTTPSProxy = "https://proxy.internal:8443"
	cfg.Advanced.Proxy.NoProxy = "127.0.0.1,localhost"
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	hostRoot := filepath.Join(baseDir, "host")
	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return host.NewFileInstaller(nil, hostRoot)
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify deploy: preflight passed, server components were installed, runtime assets were applied, and verification checks passed") {
		t.Fatalf("stdout = %q, want deploy success summary", stdout)
	}
	if !strings.Contains(stdout, "checkpoint path: "+checkpointPath) {
		t.Fatalf("stdout = %q, want checkpoint path %q", stdout, checkpointPath)
	}
	if !strings.Contains(stdout, "modified paths: 6 total: /etc/headscale/config.yaml") {
		t.Fatalf("stdout = %q, want modified path details", stdout)
	}
	if got := len(runner.commands); got < 18 {
		t.Fatalf("len(commands) = %d, want full deploy command sequence", got)
	}
	for key, want := range map[string]string{
		"http_proxy":  "http://proxy.internal:8080",
		"HTTP_PROXY":  "http://proxy.internal:8080",
		"https_proxy": "https://proxy.internal:8443",
		"HTTPS_PROXY": "https://proxy.internal:8443",
		"no_proxy":    "127.0.0.1,localhost",
		"NO_PROXY":    "127.0.0.1,localhost",
	} {
		if got := runner.commands[0].Env[key]; got != want {
			t.Fatalf("command.Env[%q] = %q, want %q", key, got, want)
		}
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty after successful deploy finalization", checkpoint.CurrentCheckpoint)
	}
	if want := []string{
		"package-manager-ready",
		"package-architecture-confirmed",
		"host-dependencies-installed",
		"lego-installed",
		"headscale-package-installed",
		"runtime-assets-installed",
		"tls-bootstrap-ready",
		"nginx-site-activated",
		"lego-command-ready",
		"certificate-issued",
		"systemd-daemon-reloaded",
		"services-enabled",
		"onboarding-ready",
		"static-verify-passed",
	}; strings.Join(checkpoint.CompletedCheckpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("CompletedCheckpoints = %v, want %v", checkpoint.CompletedCheckpoints, want)
	}
	if checkpoint.DesiredStateDigest == "" {
		t.Fatal("DesiredStateDigest = empty, want persisted desired state fingerprint")
	}
	if len(checkpoint.ModifiedPaths) != 6 {
		t.Fatalf("len(ModifiedPaths) = %d, want 6", len(checkpoint.ModifiedPaths))
	}
	if len(checkpoint.ActivationHistory) != 2 {
		t.Fatalf("len(ActivationHistory) = %d, want 2", len(checkpoint.ActivationHistory))
	}
	if checkpoint.LastFailure != nil {
		t.Fatalf("LastFailure = %#v, want nil", checkpoint.LastFailure)
	}

	content, err := os.ReadFile(filepath.Join(hostRoot, "etc", "headscale", "config.yaml"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(content), `server_url: "https://hs.example.com"`) {
		t.Fatalf("config content = %q, want rendered server_url", string(content))
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "meshify status: config is valid; last deploy context is available") {
		t.Fatalf("status stdout = %q, want last deploy summary", statusStdout)
	}
	if strings.Contains(statusStdout, "current checkpoint:") {
		t.Fatalf("status stdout = %q, do not want resumable checkpoint after successful deploy", statusStdout)
	}
	if !strings.Contains(statusStdout, "completed checkpoints: package-manager-ready, package-architecture-confirmed, host-dependencies-installed, lego-installed, headscale-package-installed, runtime-assets-installed, tls-bootstrap-ready, nginx-site-activated, lego-command-ready, certificate-issued, systemd-daemon-reloaded, services-enabled, onboarding-ready, static-verify-passed") {
		t.Fatalf("status stdout = %q, want checkpoint history", statusStdout)
	}
	if !strings.Contains(statusStdout, "modified paths: 6 total: /etc/headscale/config.yaml") {
		t.Fatalf("status stdout = %q, want modified path details", statusStdout)
	}
	if !strings.Contains(statusStdout, "checkpoint path: "+checkpointPath) {
		t.Fatalf("status stdout = %q, want checkpoint path", statusStdout)
	}
	if !strings.Contains(statusStdout, "minimum client version: Tailscale >= v1.74.0") {
		t.Fatalf("status stdout = %q, want minimum client version", statusStdout)
	}
}

func TestExecute_DeployHTTP01FreshHostDoesNotRequirePreexistingChallengeRoute(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	cfg := config.ExampleConfig()
	cfg.Default.ServerURL = "https://fresh-host.invalid"
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectDNSProbeFn = func(string) preflight.DNSProbe {
		return preflight.DNSProbe{Host: "fresh-host.invalid", ResolvedIPs: []string{"8.8.8.8"}}
	}
	detectACMEStateFn = detectACMEState

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v; stdout = %q", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify deploy: preflight passed") {
		t.Fatalf("stdout = %q, want deploy to proceed beyond preflight", stdout)
	}
	if strings.Contains(stdout, "HTTP-01 readiness could not be confirmed") {
		t.Fatalf("stdout = %q, do not want preinstall HTTP-01 route failure", stdout)
	}
}

func TestDetectPackageSourceStateUsesHeadscaleComponentOfficialPackageURLs(t *testing.T) {
	cfg := config.ExampleConfig()
	packagePlan, err := headscale.NewPackagePlan(cfg, headscale.InstallPlanOptions{
		OfficialPackageSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("NewPackagePlan() error = %v", err)
	}

	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	var probedURL string
	var probedURLs []string
	var hashedURLs []string
	var lookupVersion string
	var lookupArch string
	probePackageURLFn = func(_ *http.Client, rawURL string) (bool, bool, string) {
		probedURL = rawURL
		probedURLs = append(probedURLs, rawURL)
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(_ *http.Client, rawURL string) (string, error) {
		hashedURLs = append(hashedURLs, rawURL)
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(_ *http.Client, version string, arch string) (string, error) {
		lookupVersion = version
		lookupArch = arch
		return strings.Repeat("a", 64), nil
	}

	state := detectPackageSourceState(cfg)
	if probedURL != packagePlan.SourceURL {
		t.Fatalf("probedURL = %q, want component SourceURL %q", probedURL, packagePlan.SourceURL)
	}
	if !slices.Contains(probedURLs, packagePlan.SourceURL) {
		t.Fatalf("probedURLs = %#v, want Headscale component SourceURL %q", probedURLs, packagePlan.SourceURL)
	}
	if !slices.Contains(hashedURLs, packagePlan.SourceURL) {
		t.Fatalf("hashedURLs = %#v, want Headscale component SourceURL %q", hashedURLs, packagePlan.SourceURL)
	}
	if lookupVersion != packagePlan.Version || lookupArch != packagePlan.Arch {
		t.Fatalf("lookup version/arch = %q/%q, want %q/%q", lookupVersion, lookupArch, packagePlan.Version, packagePlan.Arch)
	}
	if state.ExpectedSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("ExpectedSHA256 = %q, want official digest", state.ExpectedSHA256)
	}
	legoURL := legocomponent.OfficialArchiveURL(legocomponent.Version, config.ArchAMD64)
	if !slices.Contains(probedURLs, legoURL) || !slices.Contains(hashedURLs, legoURL) {
		t.Fatalf("probedURLs = %#v hashedURLs = %#v, want lego URL %q", probedURLs, hashedURLs, legoURL)
	}
}

func TestDetectPackageSourceStateUsesOfflineLegoArchiveWithoutRemoteProbe(t *testing.T) {
	cfg := config.ExampleConfig()
	archivePath := filepath.Join(t.TempDir(), "lego_v4.35.2_linux_amd64.tar.gz")
	if err := os.WriteFile(archivePath, []byte("not the real archive"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg.Advanced.LegoSource.Mode = config.PackageSourceModeOffline
	cfg.Advanced.LegoSource.FilePath = archivePath

	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	var probedURLs []string
	var hashedURLs []string
	probePackageURLFn = func(_ *http.Client, rawURL string) (bool, bool, string) {
		probedURLs = append(probedURLs, rawURL)
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(_ *http.Client, rawURL string) (string, error) {
		hashedURLs = append(hashedURLs, rawURL)
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(_ *http.Client, version string, arch string) (string, error) {
		return strings.Repeat("a", 64), nil
	}

	state := detectPackageSourceState(cfg)
	if state.LegoMode != config.PackageSourceModeOffline {
		t.Fatalf("LegoMode = %q, want offline", state.LegoMode)
	}
	if state.LegoURL != "" {
		t.Fatalf("LegoURL = %q, want empty for offline", state.LegoURL)
	}
	if state.LegoFilePath != archivePath || !state.LegoFileExists || !state.LegoIntegrityChecked || state.LegoActualSHA256 == "" {
		t.Fatalf("offline lego state = %#v, want local archive inspected", state)
	}
	for _, rawURL := range append(probedURLs, hashedURLs...) {
		if strings.Contains(rawURL, "go-acme/lego") {
			t.Fatalf("remote lego URL %q was probed/hashed for offline archive", rawURL)
		}
	}
}

func TestExecute_DeployChecksArchitectureBeforePackageMutations(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	cfg.Advanced.Platform.Arch = config.ArchARM64
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{err: errors.New("unexpected file install")}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			if strings.Join(actual.Args, " ") != "--version" {
				t.Fatalf("unexpected package mutation before architecture check: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "dpkg":
			return host.Result{Command: command, Stdout: "amd64\n"}, nil
		default:
			t.Fatalf("unexpected host command %q", command.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want architecture mismatch")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `matching dpkg architecture "amd64" to config target "arm64"`) {
		t.Fatalf("stdout = %q, want architecture mismatch detail", stdout)
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "apt-get" && strings.Join(actual.Args, " ") != "--version" {
			t.Fatalf("ran package mutation before architecture mismatch: %q", command.String())
		}
	}
}

func TestExecute_DeployDNS01InstallsHostDependenciesWithoutCertbotPlugins(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/cloudflare.env"
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectACMEStateFn = func(config.Config) preflight.ACMEState {
		return preflight.ACMEState{
			DNSCredentialsChecked: true,
			DNSCredentialsReady:   true,
			DNSCredentialsDetail:  "test credentials ready",
		}
	}

	hostRoot := filepath.Join(baseDir, "host")
	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return host.NewFileInstaller(nil, hostRoot)
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	_, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	foundInstall := false
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name != "apt-get" || len(actual.Args) < 3 || actual.Args[0] != "install" {
			continue
		}
		args := strings.Join(actual.Args, " ")
		if !strings.Contains(args, "nginx") {
			continue
		}
		foundInstall = true
		for _, want := range []string{"nginx", "ca-certificates", "curl", "tar", "openssl"} {
			if !strings.Contains(args, want) {
				t.Fatalf("apt-get install args = %q, want %q", args, want)
			}
		}
		for _, unwanted := range []string{"certbot", "python3-certbot"} {
			if strings.Contains(args, unwanted) {
				t.Fatalf("apt-get install args = %q, want no %q", args, unwanted)
			}
		}
	}
	if !foundInstall {
		t.Fatalf("commands = %#v, want apt-get install command", runner.commands)
	}
}

func TestExecute_DeployDNS01SourcesEnvFileThroughSudo(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	cfg.Default.ACMEChallenge = config.ACMEChallengeDNS01
	cfg.Advanced.DNS01.Provider = "cloudflare"
	cfg.Advanced.DNS01.EnvFile = "/etc/meshify/dns01/cloudflare.env"
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}
	detectACMEStateFn = func(config.Config) preflight.ACMEState {
		return preflight.ACMEState{
			DNSCredentialsChecked: true,
			DNSCredentialsReady:   true,
			DNSCredentialsDetail:  "test cloudflare env_file ready",
		}
	}

	hostRoot := filepath.Join(baseDir, "host")
	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return host.NewFileInstaller(nil, hostRoot)
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	_, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	foundLegoIssue := false
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name != "sh" || !strings.Contains(strings.Join(actual.Args, " "), "meshify-lego-dns01") {
			continue
		}
		foundLegoIssue = true
		if command.Name != "sudo" {
			t.Fatalf("lego issuance command = %q, want sudo-wrapped command", command.String())
		}
		actualArgs := strings.Join(actual.Args, " ")
		if !strings.Contains(actualArgs, "/etc/meshify/dns01/cloudflare.env") {
			t.Fatalf("sudo shell args = %q, want env_file path", actualArgs)
		}
		display := command.String()
		if !strings.Contains(display, "/opt/meshify/bin/lego --path /var/lib/meshify/lego") || !strings.Contains(display, "--dns cloudflare") {
			t.Fatalf("lego display command = %q, want lego DNS-01 command", display)
		}
		if strings.Contains(display, "/etc/meshify/dns01/cloudflare.env") {
			t.Fatalf("lego display command = %q, want env_file hidden from display", display)
		}
	}
	if !foundLegoIssue {
		t.Fatalf("commands = %#v, want lego issuance shell wrapper", runner.commands)
	}
}

func TestExecute_DeployResumesFromRecordedCheckpoint(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	desiredStateDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest: desiredStateDigest,
		CurrentCheckpoint:  deployCheckpointRuntimeAssetsInstalled,
		CompletedCheckpoints: []string{
			deployCheckpointPackageManagerReady,
			deployCheckpointPackageArchitectureConfirmed,
			deployCheckpointHostDependenciesInstalled,
			deployCheckpointLegoInstalled,
			deployCheckpointHeadscalePackageInstalled,
			deployCheckpointRuntimeAssetsInstalled,
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousStageRuntime := stageRuntimeFilesFn
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		stageRuntimeFilesFn = previousStageRuntime
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	stageCalls := 0
	stageRuntimeFilesFn = func(cfg config.Config) ([]render.StagedFile, error) {
		stageCalls++
		return previousStageRuntime(cfg)
	}
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{err: errors.New("unexpected file install during resumed deploy")}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get", "dpkg", "curl", "sha256sum", "tar", "chmod":
			t.Fatalf("unexpected resumed host command %q", command.String())
		}
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify deploy: preflight passed; runtime assets already match the desired state and verification checks passed") {
		t.Fatalf("stdout = %q, want resumed deploy summary", stdout)
	}
	if stageCalls != 2 {
		t.Fatalf("stageRuntimeFilesFn() calls = %d, want digest and static verify staging passes during resumed deploy", stageCalls)
	}
	if got := len(runner.commands); got < 10 {
		t.Fatalf("len(commands) = %d, want resumed certificate/service/onboarding commands", got)
	}
	foundLegoIssue := false
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == legocomponent.BinaryPath && strings.Join(actual.Args, " ") != "--version" {
			foundLegoIssue = true
			break
		}
	}
	if runner.commands[0].Name != "mkdir" || !foundLegoIssue {
		t.Fatalf("commands = %#v, want HTTP-01 bootstrap before lego issuance", runner.commands)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty after successful deploy finalization", checkpoint.CurrentCheckpoint)
	}
	if want := []string{
		deployCheckpointPackageManagerReady,
		deployCheckpointPackageArchitectureConfirmed,
		deployCheckpointHostDependenciesInstalled,
		deployCheckpointLegoInstalled,
		deployCheckpointHeadscalePackageInstalled,
		deployCheckpointRuntimeAssetsInstalled,
		deployCheckpointTLSBootstrapReady,
		deployCheckpointNginxActivated,
		deployCheckpointLegoCommandReady,
		deployCheckpointCertificateIssued,
		deployCheckpointSystemdDaemonReloaded,
		deployCheckpointServicesEnabled,
		deployCheckpointOnboardingReady,
		deployCheckpointStaticVerifyPassed,
	}; strings.Join(checkpoint.CompletedCheckpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("CompletedCheckpoints = %v, want %v", checkpoint.CompletedCheckpoints, want)
	}
	if checkpoint.LastFailure != nil {
		t.Fatalf("LastFailure = %#v, want nil", checkpoint.LastFailure)
	}
}

func TestExecute_DeployClearsServicesCheckpointWhenOnboardingFails(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	desiredStateDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest: desiredStateDigest,
		CurrentCheckpoint:  deployCheckpointServicesEnabled,
		CompletedCheckpoints: []string{
			deployCheckpointPackageManagerReady,
			deployCheckpointPackageArchitectureConfirmed,
			deployCheckpointHostDependenciesInstalled,
			deployCheckpointLegoInstalled,
			deployCheckpointHeadscalePackageInstalled,
			deployCheckpointRuntimeAssetsInstalled,
			deployCheckpointTLSBootstrapReady,
			deployCheckpointNginxActivated,
			deployCheckpointLegoCommandReady,
			deployCheckpointCertificateIssued,
			deployCheckpointSystemdDaemonReloaded,
			deployCheckpointServicesEnabled,
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	previousOnboarder := newHeadscaleOnboarderFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
		newHeadscaleOnboarderFn = previousOnboarder
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name != "systemctl" || strings.Join(actual.Args, " ") != "restart headscale.service" {
			t.Fatalf("unexpected host command %q", command.String())
		}
		return host.Result{Command: command}, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}
	newHeadscaleOnboarderFn = func(host.Executor) headscaleOnboarder {
		return stubHeadscaleOnboarder{err: errors.New("headscale users list timed out")}
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want onboarding failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify deploy: create onboarding preauthkey failed") {
		t.Fatalf("stdout = %q, want onboarding failure summary", stdout)
	}
	if !strings.Contains(stdout, "journalctl -u headscale.service") {
		t.Fatalf("stdout = %q, want service-health remediation", stdout)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %d, want resumed Headscale restart before onboarding", len(runner.commands))
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if slices.Contains(checkpoint.CompletedCheckpoints, deployCheckpointServicesEnabled) {
		t.Fatalf("CompletedCheckpoints = %v, do not want stale services checkpoint after onboarding failure", checkpoint.CompletedCheckpoints)
	}
	if checkpoint.CurrentCheckpoint == deployCheckpointServicesEnabled {
		t.Fatalf("CurrentCheckpoint = %q, want services checkpoint cleared", checkpoint.CurrentCheckpoint)
	}
	if checkpoint.LastFailure == nil || checkpoint.LastFailure.Step != "create onboarding preauthkey" {
		t.Fatalf("LastFailure = %#v, want onboarding failure snapshot", checkpoint.LastFailure)
	}
}

func TestExecute_DeployIgnoresCompletedCheckpointWhenDesiredStateChanges(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	originalCfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	originalDigest, err := deployDesiredStateDigest(originalCfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest() error = %v", err)
	}
	originalCfg.Default.ServerURL = "https://hs-changed.example.com"
	if err := originalCfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	updatedCfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile(updated) error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest: originalDigest,
		CurrentCheckpoint:  deployCheckpointRuntimeAssetsInstalled,
		CompletedCheckpoints: []string{
			deployCheckpointPackageManagerReady,
			deployCheckpointPackageArchitectureConfirmed,
			deployCheckpointRuntimeAssetsInstalled,
		},
		ModifiedPaths:     []string{"/etc/headscale/config.yaml"},
		ActivationHistory: []assets.Activation{assets.ActivationRestartHeadscale},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousStageRuntime := stageRuntimeFilesFn
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	stageCalled := false
	t.Cleanup(func() {
		stageRuntimeFilesFn = previousStageRuntime
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	expectedStagedFiles, err := previousStageRuntime(updatedCfg)
	if err != nil {
		t.Fatalf("stageRuntimeFilesFn(updated) error = %v", err)
	}
	expectedDeployFiles := append([]render.StagedFile(nil), expectedStagedFiles...)
	updatedDigest, err := deployDesiredStateDigestForStaged(updatedCfg, expectedDeployFiles)
	if err != nil {
		t.Fatalf("deployDesiredStateDigestForStaged(updated) error = %v", err)
	}
	stageRuntimeFilesFn = func(config.Config) ([]render.StagedFile, error) {
		stageCalled = true
		return append([]render.StagedFile(nil), expectedStagedFiles...), nil
	}
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{
			HostPath:    "/etc/headscale/config.yaml",
			Changed:     true,
			Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		}}}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !stageCalled {
		t.Fatal("stageRuntimeFilesFn() was not called, want runtime staging for changed desired state")
	}
	if !strings.Contains(stdout, "meshify deploy: preflight passed, server components were installed, runtime assets were applied, and verification checks passed") {
		t.Fatalf("stdout = %q, want fresh deploy summary", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty after successful deploy finalization", checkpoint.CurrentCheckpoint)
	}
	if checkpoint.DesiredStateDigest != updatedDigest {
		t.Fatalf("DesiredStateDigest = %q, want %q", checkpoint.DesiredStateDigest, updatedDigest)
	}
	if len(checkpoint.ModifiedPaths) != 1 || checkpoint.ModifiedPaths[0] != "/etc/headscale/config.yaml" {
		t.Fatalf("ModifiedPaths = %v, want fresh run modifications only", checkpoint.ModifiedPaths)
	}
	if len(checkpoint.ActivationHistory) != 1 || checkpoint.ActivationHistory[0] != assets.ActivationRestartHeadscale {
		t.Fatalf("ActivationHistory = %v, want fresh run activations only", checkpoint.ActivationHistory)
	}
}

func TestDeployDesiredStateDigestTracksStagedRuntimeOutput(t *testing.T) {
	cfg := config.ExampleConfig()
	previousStageRuntime := stageRuntimeFilesFn
	t.Cleanup(func() {
		stageRuntimeFilesFn = previousStageRuntime
	})

	baseline := []render.StagedFile{{
		SourcePath:  "templates/etc/headscale/config.yaml.tmpl",
		HostPath:    "/etc/headscale/config.yaml",
		ContentMode: assets.ContentModeRender,
		Mode:        0o600,
		Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		Content:     []byte("server_url: https://hs.example.com\n"),
	}}

	tests := []struct {
		name   string
		staged []render.StagedFile
	}{
		{
			name: "rendered content",
			staged: []render.StagedFile{{
				SourcePath:  baseline[0].SourcePath,
				HostPath:    baseline[0].HostPath,
				ContentMode: baseline[0].ContentMode,
				Mode:        baseline[0].Mode,
				Activations: append([]assets.Activation(nil), baseline[0].Activations...),
				Content:     []byte("server_url: https://changed.example.com\n"),
			}},
		},
		{
			name: "host path",
			staged: []render.StagedFile{{
				SourcePath:  baseline[0].SourcePath,
				HostPath:    "/etc/headscale/config-alt.yaml",
				ContentMode: baseline[0].ContentMode,
				Mode:        baseline[0].Mode,
				Activations: append([]assets.Activation(nil), baseline[0].Activations...),
				Content:     append([]byte(nil), baseline[0].Content...),
			}},
		},
		{
			name: "mode",
			staged: []render.StagedFile{{
				SourcePath:  baseline[0].SourcePath,
				HostPath:    baseline[0].HostPath,
				ContentMode: baseline[0].ContentMode,
				Mode:        0o644,
				Activations: append([]assets.Activation(nil), baseline[0].Activations...),
				Content:     append([]byte(nil), baseline[0].Content...),
			}},
		},
		{
			name: "activations",
			staged: []render.StagedFile{{
				SourcePath:  baseline[0].SourcePath,
				HostPath:    baseline[0].HostPath,
				ContentMode: baseline[0].ContentMode,
				Mode:        baseline[0].Mode,
				Activations: []assets.Activation{assets.ActivationReloadNginx},
				Content:     append([]byte(nil), baseline[0].Content...),
			}},
		},
	}

	stageRuntimeFilesFn = func(config.Config) ([]render.StagedFile, error) {
		return append([]render.StagedFile(nil), baseline...), nil
	}
	baselineDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest(baseline) error = %v", err)
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stageRuntimeFilesFn = func(config.Config) ([]render.StagedFile, error) {
				return append([]render.StagedFile(nil), tc.staged...), nil
			}

			changedDigest, err := deployDesiredStateDigest(cfg)
			if err != nil {
				t.Fatalf("deployDesiredStateDigest(%s) error = %v", tc.name, err)
			}
			if changedDigest == baselineDigest {
				t.Fatalf("deployDesiredStateDigest() = %q, want digest change when staged %s changes", changedDigest, tc.name)
			}
		})
	}
}

func TestExecute_DeployDeferredMissingLegoPersistsFailureAndStatusShowsCheckpoint(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	hostRoot := filepath.Join(baseDir, "host")
	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return host.NewFileInstaller(nil, hostRoot)
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	legoVersionChecks := 0
	runner.run = func(command host.Command) (host.Result, error) {
		switch command.Name {
		case "apt-get", "mkdir", "sh", "curl", "sha256sum", "tar", "chmod", "ln", "nginx":
			return host.Result{}, nil
		case "/opt/meshify/bin/lego":
			if strings.Join(command.Args, " ") == "--version" {
				legoVersionChecks++
			}
			if legoVersionChecks > 1 && strings.Join(command.Args, " ") == "--version" {
				result := host.Result{Command: command}
				return result, &host.CommandError{Result: result, Err: exec.ErrNotFound}
			}
			return host.Result{}, nil
		case "dpkg":
			return host.Result{Stdout: "amd64\n"}, nil
		case "systemctl":
			result := host.Result{Command: command, Stderr: "Failed to connect to bus: No such file or directory"}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 1")}
		default:
			t.Fatalf("unexpected host command %q", command.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want deferred lego failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "check lego command failed") {
		t.Fatalf("error = %q, want lego command failure", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: check lego command failed: running /opt/meshify/bin/lego --version to confirm certificate tooling reachability") {
		t.Fatalf("stdout = %q, want deferred lego failure summary", stdout)
	}
	if strings.Contains(stdout, "Join at least two clients") {
		t.Fatalf("stdout = %q, do not want client join next step before certificate issuance", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != deployCheckpointLegoCommandDeferred {
		t.Fatalf("CurrentCheckpoint = %q, want %q", checkpoint.CurrentCheckpoint, deployCheckpointLegoCommandDeferred)
	}
	if want := []string{
		deployCheckpointPackageManagerReady,
		deployCheckpointPackageArchitectureConfirmed,
		deployCheckpointHostDependenciesInstalled,
		deployCheckpointLegoInstalled,
		deployCheckpointHeadscalePackageInstalled,
		deployCheckpointRuntimeAssetsInstalled,
		deployCheckpointTLSBootstrapReady,
		deployCheckpointNginxActivated,
		deployCheckpointLegoCommandDeferred,
	}; strings.Join(checkpoint.CompletedCheckpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("CompletedCheckpoints = %v, want %v", checkpoint.CompletedCheckpoints, want)
	}
	if checkpoint.LastFailure == nil || checkpoint.LastFailure.Step != "check lego command" {
		t.Fatalf("LastFailure = %#v, want lego command failure", checkpoint.LastFailure)
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "meshify status: check lego command failed: running /opt/meshify/bin/lego --version to confirm certificate tooling reachability") {
		t.Fatalf("status stdout = %q, want persisted lego failure summary", statusStdout)
	}
	if !strings.Contains(statusStdout, "current checkpoint: lego-command-deferred") {
		t.Fatalf("status stdout = %q, want deferred current checkpoint", statusStdout)
	}
	if !strings.Contains(statusStdout, "completed checkpoints: package-manager-ready, package-architecture-confirmed, host-dependencies-installed, lego-installed, headscale-package-installed, runtime-assets-installed, tls-bootstrap-ready, nginx-site-activated, lego-command-deferred") {
		t.Fatalf("status stdout = %q, want deferred checkpoint history", statusStdout)
	}
	if !strings.Contains(statusStdout, "warnings: lego is not installed; public certificate issuance was deferred and Nginx remains on the temporary HTTP-01 bootstrap certificate") {
		t.Fatalf("status stdout = %q, want deferred lego warning", statusStdout)
	}
	if !strings.Contains(statusStdout, "minimum client version: Tailscale >= v1.74.0") {
		t.Fatalf("status stdout = %q, want minimum client version", statusStdout)
	}
}

func TestExecute_DeployDeferredSystemdPersistsFailureBeforeServicesAndOnboarding(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	hostRoot := filepath.Join(baseDir, "host")
	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return host.NewFileInstaller(nil, hostRoot)
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get", "mkdir", "sh", "curl", "sha256sum", "tar", "chmod", "ln", "nginx", "/opt/meshify/bin/lego":
			return host.Result{Command: command}, nil
		case "dpkg":
			return host.Result{Command: command, Stdout: "amd64\n"}, nil
		case "systemctl":
			result := host.Result{Command: command, Stderr: "Failed to connect to bus: No such file or directory", ExitCode: 1}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 1")}
		default:
			t.Fatalf("unexpected host command %q", command.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want deferred systemd failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "reload systemd failed") {
		t.Fatalf("error = %q, want systemd reload failure", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: reload systemd failed: running systemctl daemon-reload to confirm service manager reachability") {
		t.Fatalf("stdout = %q, want deferred systemd failure summary", stdout)
	}
	if strings.Contains(stdout, "Join at least two clients") {
		t.Fatalf("stdout = %q, do not want client join next step before service readiness", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != deployCheckpointSystemdDaemonReloadDeferred {
		t.Fatalf("CurrentCheckpoint = %q, want %q", checkpoint.CurrentCheckpoint, deployCheckpointSystemdDaemonReloadDeferred)
	}
	if want := []string{
		deployCheckpointPackageManagerReady,
		deployCheckpointPackageArchitectureConfirmed,
		deployCheckpointHostDependenciesInstalled,
		deployCheckpointLegoInstalled,
		deployCheckpointHeadscalePackageInstalled,
		deployCheckpointRuntimeAssetsInstalled,
		deployCheckpointTLSBootstrapReady,
		deployCheckpointNginxActivated,
		deployCheckpointLegoCommandReady,
		deployCheckpointCertificateIssued,
		deployCheckpointSystemdDaemonReloadDeferred,
	}; strings.Join(checkpoint.CompletedCheckpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("CompletedCheckpoints = %v, want %v", checkpoint.CompletedCheckpoints, want)
	}
	if checkpoint.LastFailure == nil || checkpoint.LastFailure.Step != "reload systemd" {
		t.Fatalf("LastFailure = %#v, want systemd failure", checkpoint.LastFailure)
	}
	for _, notWant := range []string{deployCheckpointServicesEnabled, deployCheckpointOnboardingReady, deployCheckpointStaticVerifyPassed} {
		if slices.Contains(checkpoint.CompletedCheckpoints, notWant) {
			t.Fatalf("CompletedCheckpoints = %v, do not want %s", checkpoint.CompletedCheckpoints, notWant)
		}
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "meshify status: reload systemd failed: running systemctl daemon-reload to confirm service manager reachability") {
		t.Fatalf("status stdout = %q, want persisted systemd failure summary", statusStdout)
	}
	if !strings.Contains(statusStdout, "current checkpoint: systemd-daemon-reload-deferred") {
		t.Fatalf("status stdout = %q, want deferred systemd current checkpoint", statusStdout)
	}
	if !strings.Contains(statusStdout, "warnings: systemd is unavailable; service enablement and onboarding were deferred") {
		t.Fatalf("status stdout = %q, want systemd warning", statusStdout)
	}
}

func TestExecute_DeployRerunClearsDeferredLegoCheckpointAfterSuccess(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	desiredStateDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest: desiredStateDigest,
		CurrentCheckpoint:  deployCheckpointLegoCommandDeferred,
		CompletedCheckpoints: []string{
			deployCheckpointPackageManagerReady,
			deployCheckpointPackageArchitectureConfirmed,
			deployCheckpointHostDependenciesInstalled,
			deployCheckpointLegoInstalled,
			deployCheckpointHeadscalePackageInstalled,
			deployCheckpointRuntimeAssetsInstalled,
			deployCheckpointTLSBootstrapReady,
			deployCheckpointNginxActivated,
			deployCheckpointLegoCommandDeferred,
		},
		LastFailure: &workflow.FailureSnapshot{
			Summary: "check lego command failed: running /opt/meshify/bin/lego --version to confirm certificate tooling reachability",
			Step:    "check lego command",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{err: errors.New("unexpected file install during deferred rerun")}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v; stdout = %q", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "lego is not installed") {
		t.Fatalf("stdout = %q, want deferred warning cleared after successful rerun", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != "" {
		t.Fatalf("CurrentCheckpoint = %q, want empty after successful rerun", checkpoint.CurrentCheckpoint)
	}
	if checkpoint.LastFailure != nil {
		t.Fatalf("LastFailure = %#v, want nil after successful rerun", checkpoint.LastFailure)
	}
	if slices.Contains(checkpoint.CompletedCheckpoints, deployCheckpointLegoCommandDeferred) {
		t.Fatalf("CompletedCheckpoints = %v, want deferred checkpoint removed after successful rerun", checkpoint.CompletedCheckpoints)
	}
	if !slices.Contains(checkpoint.CompletedCheckpoints, deployCheckpointLegoCommandReady) {
		t.Fatalf("CompletedCheckpoints = %v, want lego ready checkpoint", checkpoint.CompletedCheckpoints)
	}
}

func TestExecute_DeployUsesSudoOnlyForPrivilegedHostMutationsAndDefersMissingLego(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	newDeployFileInstallerFn = func(_ host.Executor, privilege host.PrivilegeStrategy) stagedFileInstaller {
		if privilege != host.PrivilegeSudo {
			t.Fatalf("privilege = %v, want %v", privilege, host.PrivilegeSudo)
		}
		return stubFileInstaller{}
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			if strings.Join(actual.Args, " ") == "--version" {
				if command.Name == "sudo" {
					t.Fatalf("package-manager probe was sudo-wrapped: %q", command.String())
				}
				return host.Result{Command: command}, nil
			}
			if command.Name != "sudo" {
				t.Fatalf("package mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "mkdir", "sh", "curl", "sha256sum", "tar", "chmod", "ln", "nginx":
			if command.Name != "sudo" {
				t.Fatalf("privileged mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "/opt/meshify/bin/lego":
			if strings.Join(actual.Args, " ") == "--version" && command.Name != "sudo" {
				result := host.Result{Command: command}
				return result, &host.CommandError{Result: result, Err: exec.ErrNotFound}
			}
			if command.Name != "sudo" {
				t.Fatalf("privileged lego command was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "dpkg":
			if command.Name == "sudo" {
				t.Fatalf("architecture probe was sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command, Stdout: "amd64\n"}, nil
		case "systemctl":
			if command.Name != "sudo" {
				t.Fatalf("systemd mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "headscale":
			if command.Name != "sudo" {
				t.Fatalf("onboarding mutation was not sudo-wrapped: %q", command.String())
			}
			switch args := strings.Join(actual.Args, " "); {
			case strings.Contains(args, "users list"):
				return host.Result{Command: command, Stdout: "ID | Name\n1 | meshify\n"}, nil
			case strings.Contains(args, "preauthkeys create"):
				return host.Result{Command: command, Stdout: "tskey-test\n"}, nil
			default:
				return host.Result{Command: command}, nil
			}
		default:
			t.Fatalf("unexpected host command %q", actual.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want deferred lego failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "check lego command failed") {
		t.Fatalf("error = %q, want lego command failure", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: check lego command failed") {
		t.Fatalf("stdout = %q, want lego deferred failure", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !slices.Contains(checkpoint.CompletedCheckpoints, deployCheckpointLegoCommandDeferred) {
		t.Fatalf("CompletedCheckpoints = %v, want lego deferred checkpoint", checkpoint.CompletedCheckpoints)
	}
	if slices.Contains(checkpoint.CompletedCheckpoints, deployCheckpointSystemdDaemonReloaded) {
		t.Fatalf("CompletedCheckpoints = %v, do not want systemd checkpoint after deferred lego", checkpoint.CompletedCheckpoints)
	}
	if checkpoint.CurrentCheckpoint != deployCheckpointLegoCommandDeferred {
		t.Fatalf("CurrentCheckpoint = %q, want %q", checkpoint.CurrentCheckpoint, deployCheckpointLegoCommandDeferred)
	}
	if len(runner.commands) == 0 {
		t.Fatal("commands = nil, want host commands")
	}
	sawUnprivilegedProbe := false
	sawPrivilegedMutation := false
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			if strings.Join(actual.Args, " ") == "--version" {
				if command.Name == "sudo" {
					t.Fatalf("package-manager probe was sudo-wrapped: %q", command.String())
				}
				sawUnprivilegedProbe = true
				continue
			}
			if command.Name != "sudo" {
				t.Fatalf("package mutation was not sudo-wrapped: %q", command.String())
			}
			sawPrivilegedMutation = true
		case "dpkg":
			if command.Name == "sudo" {
				t.Fatalf("architecture probe was sudo-wrapped: %q", command.String())
			}
			sawUnprivilegedProbe = true
		case "/opt/meshify/bin/lego":
			if strings.Join(actual.Args, " ") == "--version" {
				if command.Name != "sudo" {
					sawUnprivilegedProbe = true
					continue
				}
				sawPrivilegedMutation = true
				continue
			}
			if command.Name != "sudo" {
				t.Fatalf("certificate mutation was not sudo-wrapped: %q", command.String())
			}
			sawPrivilegedMutation = true
		default:
			if command.Name != "sudo" {
				t.Fatalf("privileged command %q was not sudo-wrapped", command.String())
			}
			sawPrivilegedMutation = true
		}
	}
	if !sawUnprivilegedProbe {
		t.Fatal("sawUnprivilegedProbe = false, want at least one read-only probe without sudo")
	}
	if !sawPrivilegedMutation {
		t.Fatal("sawPrivilegedMutation = false, want sudo-wrapped mutations")
	}
}

func TestSystemdCommandDeferredRejectsPermissionDeniedBusErrors(t *testing.T) {
	result := host.Result{Stderr: "Failed to connect to bus: Permission denied", ExitCode: 1}
	err := &host.CommandError{Result: result, Err: errors.New("exit status 1")}

	if systemdCommandDeferred(result, err) {
		t.Fatal("systemdCommandDeferred() = true, want false for permission-denied bus errors")
	}
}

func TestExecute_DeployUsesConfiguredProxyForGoPreflightProbes(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	packageBody := []byte("meshify-package-probe")
	packageDigest := sha256.Sum256(packageBody)

	var (
		mu            sync.Mutex
		proxyRequests []string
		unexpected    []string
	)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proxyRequests = append(proxyRequests, r.Method+" "+r.URL.String())
		mu.Unlock()

		switch {
		case r.Method == http.MethodHead && r.URL.Host == "packages.invalid" && r.URL.Path == "/headscale.deb":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Host == "packages.invalid" && r.URL.Path == "/headscale.deb":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(packageBody)
		case r.Method == http.MethodHead && r.URL.Host == "github.com" && strings.Contains(r.URL.Path, "/go-acme/lego/releases/download/"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Host == "hs-proxy.invalid" && r.URL.Path == "/.well-known/acme-challenge/meshify-preflight":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		default:
			mu.Lock()
			unexpected = append(unexpected, r.Method+" "+r.URL.String())
			mu.Unlock()
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(proxyServer.Close)

	cfg := config.ExampleConfig()
	cfg.Default.ServerURL = "https://hs-proxy.invalid"
	cfg.Advanced.HeadscaleSource.Mode = config.PackageSourceModeMirror
	cfg.Advanced.HeadscaleSource.URL = "http://packages.invalid/headscale.deb"
	cfg.Advanced.HeadscaleSource.SHA256 = hex.EncodeToString(packageDigest[:])
	cfg.Advanced.Proxy.HTTPProxy = proxyServer.URL
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	for _, key := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(key, "")
	}

	previousPermissionState := detectPermissionStateFn
	previousPlatformInfo := detectPlatformInfoFn
	previousHostCapabilityState := detectHostCapabilityStateFn
	previousDNSProbe := detectDNSProbeFn
	previousPortBindings := detectPortBindingsFn
	previousFirewallState := detectFirewallStateFn
	previousServiceStates := detectServiceStatesFn
	previousPackageSourceState := detectPackageSourceStateFn
	previousACMEState := detectACMEStateFn
	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	previousInstaller := newDeployFileInstallerFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	t.Cleanup(func() {
		detectPermissionStateFn = previousPermissionState
		detectPlatformInfoFn = previousPlatformInfo
		detectHostCapabilityStateFn = previousHostCapabilityState
		detectDNSProbeFn = previousDNSProbe
		detectPortBindingsFn = previousPortBindings
		detectFirewallStateFn = previousFirewallState
		detectServiceStatesFn = previousServiceStates
		detectPackageSourceStateFn = previousPackageSourceState
		detectACMEStateFn = previousACMEState
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
		newDeployFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})

	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}
	detectPlatformInfoFn = func() preflight.PlatformInfo {
		return preflight.PlatformInfo{ID: "debian", VersionID: "13", PrettyName: "Debian GNU/Linux 13"}
	}
	detectHostCapabilityStateFn = passingHostCapabilities
	detectDNSProbeFn = func(string) preflight.DNSProbe {
		return preflight.DNSProbe{Host: "hs-proxy.invalid", ResolvedIPs: []string{"8.8.8.8"}}
	}
	detectPortBindingsFn = func(config.Config) []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp"},
			{Port: 443, Protocol: "tcp"},
			{Port: 8080, Protocol: "tcp"},
			{Port: config.DefaultHeadscaleMetricsPort, Protocol: "tcp"},
			{Port: 50443, Protocol: "tcp"},
			{Port: 3478, Protocol: "udp"},
		}
	}
	detectFirewallStateFn = func() preflight.FirewallState {
		return preflight.FirewallState{Inspected: true, Active: true, AllowedPorts: []string{"80/tcp", "443/tcp", "3478/udp"}}
	}
	detectServiceStatesFn = func() []preflight.ServiceState {
		return []preflight.ServiceState{}
	}
	detectPackageSourceStateFn = detectPackageSourceState
	detectACMEStateFn = detectACMEState
	probePackageURLFn = func(client *http.Client, rawURL string) (bool, bool, string) {
		if strings.Contains(rawURL, "github.com/go-acme/lego") {
			return true, true, rawURL + " returned 200."
		}
		return probePackageURL(client, rawURL)
	}
	hashRemoteArtifactFn = func(client *http.Client, rawURL string) (string, error) {
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return hashRemoteArtifact(client, rawURL)
	}
	lookupOfficialPackageDigestFn = lookupOfficialPackageDigest
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{}
	}

	runner := &scriptedHostRunner{}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v; stdout = %q", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}

	mu.Lock()
	gotRequests := append([]string(nil), proxyRequests...)
	gotUnexpected := append([]string(nil), unexpected...)
	mu.Unlock()
	if len(gotUnexpected) != 0 {
		t.Fatalf("unexpected proxy requests = %v", gotUnexpected)
	}
	for _, want := range []string{
		http.MethodHead + " http://packages.invalid/headscale.deb",
		http.MethodGet + " http://packages.invalid/headscale.deb",
	} {
		found := slices.Contains(gotRequests, want)
		if !found {
			t.Fatalf("proxy requests = %v, want %q", gotRequests, want)
		}
	}
}

func TestDeployProxyFuncHonorsStandardNoProxySemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		proxy      config.ProxyConfig
		requestURL string
		wantProxy  string
	}{
		{
			name: "wildcard subdomain exclusion",
			proxy: config.ProxyConfig{
				HTTPSProxy: "https://secure-proxy.internal:8443",
				NoProxy:    "*.example.com",
			},
			requestURL: "https://nested.example.com/runtime",
			wantProxy:  "<nil>",
		},
		{
			name: "exact domain exclusion also covers subdomains",
			proxy: config.ProxyConfig{
				HTTPProxy: "http://proxy.internal:8080",
				NoProxy:   "example.com",
			},
			requestURL: "http://api.example.com/package",
			wantProxy:  "<nil>",
		},
		{
			name: "port scoped exclusion",
			proxy: config.ProxyConfig{
				HTTPSProxy: "https://secure-proxy.internal:8443",
				NoProxy:    "packages.example.com:8443",
			},
			requestURL: "https://packages.example.com:8443/headscale.deb",
			wantProxy:  "<nil>",
		},
		{
			name: "port scoped exclusion does not bypass other ports",
			proxy: config.ProxyConfig{
				HTTPSProxy: "https://secure-proxy.internal:8443",
				NoProxy:    "packages.example.com:8443",
			},
			requestURL: "https://packages.example.com/runtime",
			wantProxy:  "https://secure-proxy.internal:8443",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request, err := http.NewRequest(http.MethodGet, tc.requestURL, nil)
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}

			proxyURL, err := deployProxyFunc(tc.proxy)(request)
			if err != nil {
				t.Fatalf("deployProxyFunc() error = %v", err)
			}

			got := "<nil>"
			if proxyURL != nil {
				got = proxyURL.String()
			}
			if got != tc.wantProxy {
				t.Fatalf("deployProxyFunc() = %q, want %q", got, tc.wantProxy)
			}
		})
	}
}

func TestDeployProxyConfiguredTreatsNoProxyAsExplicitProxyConfiguration(t *testing.T) {
	t.Parallel()

	if !deployProxyConfigured(config.ProxyConfig{NoProxy: "packages.invalid"}) {
		t.Fatal("deployProxyConfigured() = false, want true when no_proxy is configured")
	}
}

func TestExecute_DeployHostCommandFailurePersistsRecoveryPointAndStatusShowsIt(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{err: errors.New("unexpected file install")}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			return host.Result{}, nil
		case "dpkg":
			result := host.Result{Command: command, ExitCode: 2}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 2")}
		default:
			t.Fatalf("unexpected host command %q", command.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "confirm package architecture failed: collecting host package architecture via dpkg") {
		t.Fatalf("error = %q, want host command failure summary", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: confirm package architecture failed: collecting host package architecture via dpkg") {
		t.Fatalf("stdout = %q, want host command failure response", stdout)
	}
	if !strings.Contains(stdout, "details: dpkg --print-architecture exited with status 2") {
		t.Fatalf("stdout = %q, want sanitized host command detail", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if checkpoint.CurrentCheckpoint != deployCheckpointPackageManagerReady {
		t.Fatalf("CurrentCheckpoint = %q, want %q", checkpoint.CurrentCheckpoint, deployCheckpointPackageManagerReady)
	}
	if want := []string{deployCheckpointPackageManagerReady}; strings.Join(checkpoint.CompletedCheckpoints, ",") != strings.Join(want, ",") {
		t.Fatalf("CompletedCheckpoints = %v, want %v", checkpoint.CompletedCheckpoints, want)
	}
	if checkpoint.LastFailure == nil {
		t.Fatal("LastFailure = nil, want persisted failure snapshot")
	}
	if checkpoint.LastFailure.Step != "confirm package architecture" {
		t.Fatalf("LastFailure.Step = %q, want %q", checkpoint.LastFailure.Step, "confirm package architecture")
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "current checkpoint: package-manager-ready") {
		t.Fatalf("status stdout = %q, want recovery checkpoint", statusStdout)
	}
	if !strings.Contains(statusStdout, "completed checkpoints: package-manager-ready") {
		t.Fatalf("status stdout = %q, want checkpoint history", statusStdout)
	}
	if !strings.Contains(statusStdout, "step: confirm package architecture") {
		t.Fatalf("status stdout = %q, want host command failure step", statusStdout)
	}
}

func TestExecute_StatusSuppressesStaleDeployContextAfterConfigChange(t *testing.T) {
	tests := []struct {
		name       string
		checkpoint state.Checkpoint
		unwanted   []string
	}{
		{
			name: "resumable checkpoint",
			checkpoint: state.Checkpoint{
				CurrentCheckpoint: deployCheckpointRuntimeAssetsInstalled,
				CompletedCheckpoints: []string{
					deployCheckpointPackageManagerReady,
					deployCheckpointPackageArchitectureConfirmed,
					deployCheckpointRuntimeAssetsInstalled,
				},
				ModifiedPaths:     []string{"/etc/headscale/config.yaml"},
				ActivationHistory: []assets.Activation{assets.ActivationRestartHeadscale},
			},
			unwanted: []string{
				"meshify status: config is valid; resumable deploy checkpoint is available",
				"current checkpoint:",
				"completed checkpoints:",
				"modified paths:",
				"activation history:",
			},
		},
		{
			name: "failed deploy snapshot",
			checkpoint: state.Checkpoint{
				CurrentCheckpoint:    deployCheckpointPackageManagerReady,
				CompletedCheckpoints: []string{deployCheckpointPackageManagerReady},
				LastFailure: &workflow.FailureSnapshot{
					Summary: "confirm package architecture failed: collecting host package architecture via dpkg",
					Step:    "confirm package architecture",
					Details: "dpkg --print-architecture exited with status 2",
				},
			},
			unwanted: []string{
				"meshify status: confirm package architecture failed: collecting host package architecture via dpkg",
				"current checkpoint:",
				"completed checkpoints:",
				"step: confirm package architecture",
				"details: dpkg --print-architecture exited with status 2",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			baseDir := t.TempDir()
			configPath := filepath.Join(baseDir, "meshify.yaml")
			if err := config.WriteExampleFile(configPath); err != nil {
				t.Fatalf("WriteExampleFile() error = %v", err)
			}

			originalCfg, err := config.LoadFile(configPath)
			if err != nil {
				t.Fatalf("LoadFile() error = %v", err)
			}
			originalDigest, err := deployDesiredStateDigest(originalCfg)
			if err != nil {
				t.Fatalf("deployDesiredStateDigest() error = %v", err)
			}

			originalCfg.Default.ServerURL = "https://hs-changed.example.com"
			if err := originalCfg.WriteFile(configPath); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
			previousCheckpointPath := checkpointPathForConfigFn
			previousStore := checkpointStoreForConfigFn
			t.Cleanup(func() {
				checkpointPathForConfigFn = previousCheckpointPath
				checkpointStoreForConfigFn = previousStore
			})
			checkpointPathForConfigFn = func(string) string {
				return checkpointPath
			}
			checkpointStoreForConfigFn = func(string) state.Store {
				return state.NewStore(checkpointPath)
			}

			checkpoint := tt.checkpoint
			checkpoint.DesiredStateDigest = originalDigest
			if err := state.NewStore(checkpointPath).Save(checkpoint); err != nil {
				t.Fatalf("Save() error = %v", err)
			}

			statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
			if err != nil {
				t.Fatalf("status Execute() error = %v", err)
			}
			if statusStderr != "" {
				t.Fatalf("status stderr = %q, want empty", statusStderr)
			}
			if !strings.Contains(statusStdout, "meshify status: config is valid; persisted deploy context is stale for the current desired state") {
				t.Fatalf("status stdout = %q, want stale deploy context summary", statusStdout)
			}
			if !strings.Contains(statusStdout, "stale context: config changed since the recorded deploy context was saved; meshify will ignore that recovery data on the next deploy") {
				t.Fatalf("status stdout = %q, want stale context explanation", statusStdout)
			}
			if !strings.Contains(statusStdout, "checkpoint path: "+checkpointPath) {
				t.Fatalf("status stdout = %q, want checkpoint path", statusStdout)
			}
			if !strings.Contains(statusStdout, "minimum client version: Tailscale >= v1.74.0") {
				t.Fatalf("status stdout = %q, want minimum client version", statusStdout)
			}
			if !strings.Contains(statusStdout, "meshify deploy --config "+configPath) {
				t.Fatalf("status stdout = %q, want deploy next step", statusStdout)
			}
			for _, unwanted := range tt.unwanted {
				if strings.Contains(statusStdout, unwanted) {
					t.Fatalf("status stdout = %q, do not want stale field %q", statusStdout, unwanted)
				}
			}
		})
	}
}

func TestExecute_StatusTreatsDigestlessDeployContextAsStale(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	t.Cleanup(func() {
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}

	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		CurrentCheckpoint:    deployCheckpointRuntimeAssetsInstalled,
		CompletedCheckpoints: []string{deployCheckpointPackageManagerReady, deployCheckpointRuntimeAssetsInstalled},
		LastFailure: &workflow.FailureSnapshot{
			Summary: "install runtime assets failed: writing runtime files to host paths",
			Step:    "install runtime assets",
			Details: "write /etc/headscale/config.yaml: permission denied",
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "meshify status: config is valid; persisted deploy context is missing its desired-state fingerprint") {
		t.Fatalf("status stdout = %q, want digestless stale summary", statusStdout)
	}
	if !strings.Contains(statusStdout, "stale context: checkpoint data has no desired-state fingerprint; meshify will ignore that recovery data on the next deploy") {
		t.Fatalf("status stdout = %q, want digestless stale explanation", statusStdout)
	}
	if !strings.Contains(statusStdout, "meshify deploy --config "+configPath) {
		t.Fatalf("status stdout = %q, want deploy next step", statusStdout)
	}
	if !strings.Contains(statusStdout, "minimum client version: Tailscale >= v1.74.0") {
		t.Fatalf("status stdout = %q, want minimum client version", statusStdout)
	}
	for _, unwanted := range []string{
		"current checkpoint:",
		"completed checkpoints:",
		"step: install runtime assets",
		"details: write /etc/headscale/config.yaml: permission denied",
	} {
		if strings.Contains(statusStdout, unwanted) {
			t.Fatalf("status stdout = %q, do not want stale field %q", statusStdout, unwanted)
		}
	}
}

func TestExecute_DeployHostCommandFailureStillWritesReadableFailureWhenCheckpointSaveFails(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointDir := filepath.Join(baseDir, "state")
	checkpointPath := filepath.Join(checkpointDir, "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{err: errors.New("unexpected file install")}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		switch command.Name {
		case "apt-get":
			return host.Result{}, nil
		case "dpkg":
			if err := os.Remove(checkpointPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Remove(checkpointPath) error = %v", err)
			}
			if err := os.Remove(checkpointDir); err != nil {
				t.Fatalf("Remove(checkpointDir) error = %v", err)
			}
			if err := os.WriteFile(checkpointDir, []byte("blocked"), 0o600); err != nil {
				t.Fatalf("WriteFile(checkpointDir) error = %v", err)
			}
			result := host.Result{Command: command, ExitCode: 2}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 2")}
		default:
			t.Fatalf("unexpected host command %q", command.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "confirm package architecture failed: collecting host package architecture via dpkg") {
		t.Fatalf("error = %q, want original failure summary", err.Error())
	}
	if !strings.Contains(err.Error(), "could not save recovery point") {
		t.Fatalf("error = %q, want checkpoint warning", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: confirm package architecture failed: collecting host package architecture via dpkg") {
		t.Fatalf("stdout = %q, want failure response", stdout)
	}
	if !strings.Contains(stdout, "details: dpkg --print-architecture exited with status 2") {
		t.Fatalf("stdout = %q, want sanitized host command detail", stdout)
	}
	if !strings.Contains(stdout, "checkpoint warning: could not save recovery point:") {
		t.Fatalf("stdout = %q, want checkpoint warning", stdout)
	}
}

func TestExecute_DeployUsesSudoForPrivilegedHostMutations(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)
	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deployer", SudoWorks: true}
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	newDeployFileInstallerFn = func(executor host.Executor, privilege host.PrivilegeStrategy) stagedFileInstaller {
		if privilege != host.PrivilegeSudo {
			t.Fatalf("privilege = %v, want %v", privilege, host.PrivilegeSudo)
		}
		return host.NewFileInstaller(host.NewCommandFileSystem(executor), "")
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			if strings.Join(actual.Args, " ") == "--version" {
				if command.Name == "sudo" {
					t.Fatalf("package-manager probe was sudo-wrapped: %q", command.String())
				}
				return host.Result{Command: command}, nil
			}
			if command.Name != "sudo" {
				t.Fatalf("package mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "dpkg":
			if command.Name == "sudo" {
				t.Fatalf("architecture probe was sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command, Stdout: "amd64\n"}, nil
		case "/opt/meshify/bin/lego":
			if strings.Join(actual.Args, " ") == "--version" {
				return host.Result{Command: command}, nil
			}
			if command.Name != "sudo" {
				t.Fatalf("certificate mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "mkdir", "sh", "chmod", "curl", "sha256sum", "tar", "ln", "nginx", "systemctl":
			if command.Name != "sudo" {
				t.Fatalf("privileged mutation was not sudo-wrapped: %q", command.String())
			}
			return host.Result{Command: command}, nil
		case "headscale":
			if command.Name != "sudo" {
				t.Fatalf("onboarding mutation was not sudo-wrapped: %q", command.String())
			}
			switch args := strings.Join(actual.Args, " "); {
			case strings.Contains(args, "users list"):
				return host.Result{Command: command, Stdout: "ID | Name\n1 | meshify\n"}, nil
			case strings.Contains(args, "preauthkeys create"):
				return host.Result{Command: command, Stdout: "tskey-test\n"}, nil
			default:
				return host.Result{Command: command}, nil
			}
		case "cat", "stat":
			if command.Name != "sudo" {
				t.Fatalf("privileged file inspection was not sudo-wrapped: %q", command.String())
			}
			result := host.Result{
				Command:  command,
				Stderr:   actual.Name + ": /etc/headscale/config.yaml: No such file or directory",
				ExitCode: 1,
			}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 1")}
		default:
			t.Fatalf("unexpected host command %q", actual.String())
			return host.Result{}, nil
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v; stdout = %q", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify deploy: preflight passed") {
		t.Fatalf("stdout = %q, want deploy success summary", stdout)
	}

	sawPrivilegedPackageCommand := false
	sawPrivilegedFileWrite := false
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "apt-get":
			if command.Name == "sudo" && strings.Join(actual.Args, " ") != "--version" {
				sawPrivilegedPackageCommand = true
			}
		case "sh":
			if command.Name == "sudo" && slices.Contains(actual.Args, "/etc/headscale/config.yaml") {
				sawPrivilegedFileWrite = true
			}
		}
	}
	if !sawPrivilegedPackageCommand {
		t.Fatalf("commands = %#v, want sudo-wrapped apt-get", runner.commands)
	}
	if !sawPrivilegedFileWrite {
		t.Fatalf("commands = %#v, want sudo-wrapped write to /etc/headscale/config.yaml", runner.commands)
	}
}

func TestExecute_CommandsFormatCheckpointLoadFailures(t *testing.T) {
	tests := []struct {
		name        string
		command     string
		prepare     func(t *testing.T, checkpointPath string)
		wantDetails string
		wantNext    string
	}{
		{
			name:    "deploy malformed checkpoint",
			command: "deploy",
			prepare: func(t *testing.T, checkpointPath string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
				if err := os.WriteFile(checkpointPath, []byte("{"), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			},
			wantDetails: "details: decode checkpoint:",
			wantNext:    "Remove the unreadable checkpoint",
		},
		{
			name:    "status unreadable checkpoint",
			command: "status",
			prepare: func(t *testing.T, checkpointPath string) {
				t.Helper()
				if err := os.MkdirAll(checkpointPath, 0o755); err != nil {
					t.Fatalf("MkdirAll() error = %v", err)
				}
			},
			wantDetails: "details: read checkpoint:",
			wantNext:    "Repair or remove the checkpoint",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			baseDir := t.TempDir()
			configPath := filepath.Join(baseDir, "meshify.yaml")
			if err := config.WriteExampleFile(configPath); err != nil {
				t.Fatalf("WriteExampleFile() error = %v", err)
			}

			checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
			previousCheckpointPath := checkpointPathForConfigFn
			previousStore := checkpointStoreForConfigFn
			t.Cleanup(func() {
				checkpointPathForConfigFn = previousCheckpointPath
				checkpointStoreForConfigFn = previousStore
			})
			checkpointPathForConfigFn = func(string) string {
				return checkpointPath
			}
			checkpointStoreForConfigFn = func(string) state.Store {
				return state.NewStore(checkpointPath)
			}
			tc.prepare(t, checkpointPath)

			stdout, stderr, err := runCLI(t, tc.command, "--config", configPath)
			if err == nil {
				t.Fatal("Execute() error = nil, want non-nil")
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			if !strings.Contains(err.Error(), "load deploy checkpoint failed") {
				t.Fatalf("error = %q, want formatted checkpoint failure", err.Error())
			}
			if !strings.Contains(stdout, "meshify "+tc.command+": load deploy checkpoint failed: reading persisted deploy recovery state") {
				t.Fatalf("stdout = %q, want formatted checkpoint failure summary", stdout)
			}
			if !strings.Contains(stdout, "checkpoint path: "+checkpointPath) {
				t.Fatalf("stdout = %q, want checkpoint path", stdout)
			}
			if tc.command == "status" && !strings.Contains(stdout, "minimum client version: Tailscale >= v1.74.0") {
				t.Fatalf("stdout = %q, want minimum client version", stdout)
			}
			if !strings.Contains(stdout, tc.wantDetails) {
				t.Fatalf("stdout = %q, want details substring %q", stdout, tc.wantDetails)
			}
			if !strings.Contains(stdout, tc.wantNext) {
				t.Fatalf("stdout = %q, want next step substring %q", stdout, tc.wantNext)
			}
		})
	}
}

func TestExecute_DeployFormatsDesiredStateFingerprintFailures(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousStageRuntime := stageRuntimeFilesFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	t.Cleanup(func() {
		stageRuntimeFilesFn = previousStageRuntime
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
	})
	stageRuntimeFilesFn = func(config.Config) ([]render.StagedFile, error) {
		return nil, errors.New("render runtime manifest: missing template value")
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "fingerprint desired state failed") {
		t.Fatalf("error = %q, want formatted fingerprint failure", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: fingerprint desired state failed: building the current runtime asset fingerprint") {
		t.Fatalf("stdout = %q, want formatted fingerprint failure summary", stdout)
	}
	if !strings.Contains(stdout, "details: render runtime manifest: missing template value") {
		t.Fatalf("stdout = %q, want sanitized fingerprint failure details", stdout)
	}

	checkpoint, loadErr := state.NewStore(checkpointPath).Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if checkpoint.LastFailure == nil {
		t.Fatal("LastFailure = nil, want persisted fingerprint failure snapshot")
	}
	if checkpoint.LastFailure.Step != "fingerprint desired state" {
		t.Fatalf("LastFailure.Step = %q, want %q", checkpoint.LastFailure.Step, "fingerprint desired state")
	}
	if checkpoint.LastFailure.Details != "render runtime manifest: missing template value" {
		t.Fatalf("LastFailure.Details = %q, want sanitized staging failure detail", checkpoint.LastFailure.Details)
	}
}

func TestExecute_StatusFormatsDesiredStateFingerprintFailures(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest:   "prior-digest",
		CurrentCheckpoint:    deployCheckpointPackageManagerReady,
		CompletedCheckpoints: []string{deployCheckpointPackageManagerReady},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousStageRuntime := stageRuntimeFilesFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	t.Cleanup(func() {
		stageRuntimeFilesFn = previousStageRuntime
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
	})
	stageRuntimeFilesFn = func(config.Config) ([]render.StagedFile, error) {
		return nil, errors.New("render runtime manifest: missing template value")
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}

	stdout, stderr, err := runCLI(t, "status", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "fingerprint desired state failed") {
		t.Fatalf("error = %q, want formatted fingerprint failure", err.Error())
	}
	if !strings.Contains(stdout, "meshify status: fingerprint desired state failed: building the current runtime asset fingerprint") {
		t.Fatalf("stdout = %q, want formatted fingerprint failure summary", stdout)
	}
	if !strings.Contains(stdout, "details: render runtime manifest: missing template value") {
		t.Fatalf("stdout = %q, want sanitized fingerprint failure details", stdout)
	}
	if !strings.Contains(stdout, "minimum client version: Tailscale >= v1.74.0") {
		t.Fatalf("stdout = %q, want minimum client version", stdout)
	}
}

func TestExecute_DeployFailurePersistsFailureSnapshotAndStatusShowsIt(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}

	stubPassingDeployPreflight(t)

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousInstaller := newDeployFileInstallerFn
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	previousSystemd := newHostSystemdFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newDeployFileInstallerFn = previousInstaller
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
		newHostSystemdFn = previousSystemd
	})
	newDeployFileInstallerFn = func(_ host.Executor, _ host.PrivilegeStrategy) stagedFileInstaller {
		return stubFileInstaller{
			results: []host.FileInstallResult{{
				HostPath:    "/etc/headscale/config.yaml",
				Changed:     true,
				Activations: []assets.Activation{assets.ActivationRestartHeadscale},
			}},
			err: errors.New("write /etc/headscale/config.yaml: permission denied\nraw shell spew"),
		}
	}
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		return successfulDeployHostResult(command)
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newHostSystemdFn = func(executor host.Executor) host.Systemd {
		return host.NewSystemd(executor)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(err.Error(), "install runtime assets failed: writing runtime files to host paths") {
		t.Fatalf("error = %q, want failure summary", err.Error())
	}
	if !strings.Contains(stdout, "meshify deploy: install runtime assets failed: writing runtime files to host paths") {
		t.Fatalf("stdout = %q, want failure response", stdout)
	}
	if !strings.Contains(stdout, "details: write /etc/headscale/config.yaml: permission denied") {
		t.Fatalf("stdout = %q, want sanitized details", stdout)
	}

	checkpoint, err := state.NewStore(checkpointPath).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(checkpoint.ModifiedPaths) != 1 || checkpoint.ModifiedPaths[0] != "/etc/headscale/config.yaml" {
		t.Fatalf("ModifiedPaths = %v, want partial install tracking", checkpoint.ModifiedPaths)
	}
	if checkpoint.LastFailure == nil {
		t.Fatal("LastFailure = nil, want persisted failure snapshot")
	}
	if checkpoint.LastFailure.Summary != "install runtime assets failed: writing runtime files to host paths" {
		t.Fatalf("LastFailure.Summary = %q, want persisted summary", checkpoint.LastFailure.Summary)
	}
	if checkpoint.LastFailure.Details != "write /etc/headscale/config.yaml: permission denied" {
		t.Fatalf("LastFailure.Details = %q, want sanitized details", checkpoint.LastFailure.Details)
	}

	statusStdout, statusStderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("status Execute() error = %v", err)
	}
	if statusStderr != "" {
		t.Fatalf("status stderr = %q, want empty", statusStderr)
	}
	if !strings.Contains(statusStdout, "meshify status: install runtime assets failed: writing runtime files to host paths") {
		t.Fatalf("status stdout = %q, want persisted failure summary", statusStdout)
	}
	if !strings.Contains(statusStdout, "details: write /etc/headscale/config.yaml: permission denied") {
		t.Fatalf("status stdout = %q, want sanitized failure details", statusStdout)
	}
	if !strings.Contains(statusStdout, "modified paths: 1 total: /etc/headscale/config.yaml") {
		t.Fatalf("status stdout = %q, want modified path details", statusStdout)
	}
	if !strings.Contains(statusStdout, "checkpoint path: "+checkpointPath) {
		t.Fatalf("status stdout = %q, want checkpoint path", statusStdout)
	}
	if !strings.Contains(statusStdout, "minimum client version: Tailscale >= v1.74.0") {
		t.Fatalf("status stdout = %q, want minimum client version", statusStdout)
	}
}

type stubFileInstaller struct {
	results []host.FileInstallResult
	err     error
}

func (installer stubFileInstaller) Install(_ []render.StagedFile) ([]host.FileInstallResult, error) {
	return append([]host.FileInstallResult(nil), installer.results...), installer.err
}

type stubHeadscaleOnboarder struct {
	key string
	err error
}

func (onboarder stubHeadscaleOnboarder) CreatePreAuthKey(stdcontext.Context, headscale.OnboardingPlan) (string, []host.Result, error) {
	return onboarder.key, nil, onboarder.err
}

func unwrapMaybeSudoHostCommand(command host.Command) host.Command {
	if command.Name != "sudo" {
		return command
	}

	args := append([]string(nil), command.Args...)
	if len(args) > 0 && args[0] == "-n" {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "env" {
		args = args[1:]
		for len(args) > 0 && strings.Contains(args[0], "=") {
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return command
	}

	return host.Command{Name: args[0], Args: append([]string(nil), args[1:]...)}
}

func successfulDeployHostResult(command host.Command) (host.Result, error) {
	actual := unwrapMaybeSudoHostCommand(command)
	switch actual.Name {
	case "dpkg":
		return host.Result{Command: command, Stdout: "amd64\n"}, nil
	case "headscale":
		args := strings.Join(actual.Args, " ")
		switch {
		case strings.Contains(args, "users list"):
			return host.Result{Command: command, Stdout: "ID | Name\n1 | meshify\n"}, nil
		case strings.Contains(args, "preauthkeys create"):
			return host.Result{Command: command, Stdout: "tskey-test\n"}, nil
		default:
			return host.Result{Command: command}, nil
		}
	case "apt-get", "mkdir", "sh", "curl", "sha256sum", "tar", "chmod", "/opt/meshify/bin/lego", "ln", "nginx", "systemctl":
		return host.Result{Command: command}, nil
	default:
		return host.Result{Command: command}, nil
	}
}

func testLegoArchiveHash(t *testing.T, rawURL string) (string, bool) {
	t.Helper()
	if !strings.Contains(rawURL, "lego_") {
		return "", false
	}
	arch := config.ArchAMD64
	if strings.Contains(rawURL, "_arm64") {
		arch = config.ArchARM64
	}
	sha, err := legocomponent.ArchiveSHA256(arch)
	if err != nil {
		t.Fatalf("ArchiveSHA256() error = %v", err)
	}
	return sha, true
}

type scriptedHostRunner struct {
	commands []host.Command
	run      func(command host.Command) (host.Result, error)
}

func (runner *scriptedHostRunner) Run(_ stdcontext.Context, command host.Command) (host.Result, error) {
	runner.commands = append(runner.commands, command)
	if runner.run == nil {
		return host.Result{Command: command}, nil
	}
	result, err := runner.run(command)
	result.Command = command
	return result, err
}

func stubPassingDeployPreflight(t *testing.T) {
	t.Helper()

	previousPermissionState := detectPermissionStateFn
	previousPlatformInfo := detectPlatformInfoFn
	previousHostCapabilityState := detectHostCapabilityStateFn
	previousDNSProbe := detectDNSProbeFn
	previousPortBindings := detectPortBindingsFn
	previousFirewallState := detectFirewallStateFn
	previousServiceStates := detectServiceStatesFn
	previousACMEState := detectACMEStateFn
	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		detectPermissionStateFn = previousPermissionState
		detectPlatformInfoFn = previousPlatformInfo
		detectHostCapabilityStateFn = previousHostCapabilityState
		detectDNSProbeFn = previousDNSProbe
		detectPortBindingsFn = previousPortBindings
		detectFirewallStateFn = previousFirewallState
		detectServiceStatesFn = previousServiceStates
		detectACMEStateFn = previousACMEState
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "root", IsRoot: true}
	}
	detectPlatformInfoFn = func() preflight.PlatformInfo {
		return preflight.PlatformInfo{ID: "debian", VersionID: "13", PrettyName: "Debian GNU/Linux 13"}
	}
	detectHostCapabilityStateFn = passingHostCapabilities
	detectDNSProbeFn = func(string) preflight.DNSProbe {
		return preflight.DNSProbe{Host: "hs.example.com", ResolvedIPs: []string{"8.8.8.8"}}
	}
	detectPortBindingsFn = func(config.Config) []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp"},
			{Port: 443, Protocol: "tcp"},
			{Port: 8080, Protocol: "tcp"},
			{Port: config.DefaultHeadscaleMetricsPort, Protocol: "tcp"},
			{Port: 50443, Protocol: "tcp"},
			{Port: 3478, Protocol: "udp"},
		}
	}
	detectFirewallStateFn = func() preflight.FirewallState {
		return preflight.FirewallState{
			Inspected:    true,
			Active:       true,
			AllowedPorts: []string{"80/tcp", "443/tcp", "3478/udp"},
		}
	}
	detectServiceStatesFn = func() []preflight.ServiceState {
		return []preflight.ServiceState{}
	}
	probePackageURLFn = func(_ *http.Client, rawURL string) (bool, bool, string) {
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(_ *http.Client, rawURL string) (string, error) {
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(_ *http.Client, version string, arch string) (string, error) {
		return strings.Repeat("a", 64), nil
	}
	detectACMEStateFn = func(config.Config) preflight.ACMEState {
		return preflight.ACMEState{HTTP01Checked: true, HTTP01Ready: true}
	}
}

func passingHostCapabilities() preflight.HostCapabilityState {
	return preflight.HostCapabilityState{
		AptGetAvailable:         true,
		DpkgAvailable:           true,
		SystemctlAvailable:      true,
		SystemdRuntimeAvailable: true,
	}
}

func TestExecute_DeployAndVerifyJSONMissingConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "deploy", command: "deploy"},
		{name: "verify", command: "verify"},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configPath := filepath.Join(t.TempDir(), "missing.yaml")
			stdout, stderr, err := runCLI(t, tc.command, "--config", configPath, "--format", "json")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}

			var response output.Response
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
			}
			if response.Command != tc.command {
				t.Fatalf("response.Command = %q, want %q", response.Command, tc.command)
			}
			if response.Status != "missing-config" {
				t.Fatalf("response.Status = %q, want %q", response.Status, "missing-config")
			}
			if response.Summary != "no config file found" {
				t.Fatalf("response.Summary = %q, want %q", response.Summary, "no config file found")
			}
			if len(response.Fields) < 1 || response.Fields[0].Value != configPath {
				t.Fatalf("response.Fields = %#v, want config path %q", response.Fields, configPath)
			}
			if len(response.NextSteps) != 1 || !strings.Contains(response.NextSteps[0], "meshify init --config "+configPath) {
				t.Fatalf("response.NextSteps = %#v, want init hint", response.NextSteps)
			}
		})
	}
}

func TestExecute_DeployAndVerifyJSONInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
	}{
		{name: "deploy", command: "deploy"},
		{name: "verify", command: "verify"},
	}

	for _, tt := range tests {
		tc := tt
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			configPath := filepath.Join(t.TempDir(), "meshify.yaml")
			if err := config.WriteExampleFile(configPath); err != nil {
				t.Fatalf("WriteExampleFile() error = %v", err)
			}

			content, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatalf("os.ReadFile() error = %v", err)
			}
			invalid := strings.Replace(string(content), "https://hs.example.com", "http://hs.example.com", 1)
			if invalid == string(content) {
				t.Fatal("expected example config to contain the default server URL")
			}
			if err := os.WriteFile(configPath, []byte(invalid), 0o600); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}

			stdout, stderr, err := runCLI(t, tc.command, "--config", configPath, "--format", "json")
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}

			var response output.Response
			if err := json.Unmarshal([]byte(stdout), &response); err != nil {
				t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
			}
			if response.Command != tc.command {
				t.Fatalf("response.Command = %q, want %q", response.Command, tc.command)
			}
			if response.Status != "invalid-config" {
				t.Fatalf("response.Status = %q, want %q", response.Status, "invalid-config")
			}
			if response.Summary != "config file exists but failed validation" {
				t.Fatalf("response.Summary = %q, want %q", response.Summary, "config file exists but failed validation")
			}
			if len(response.Fields) < 2 {
				t.Fatalf("response.Fields = %#v, want config path and details", response.Fields)
			}
			if response.Fields[0].Value != configPath {
				t.Fatalf("first field value = %q, want %q", response.Fields[0].Value, configPath)
			}
			if !strings.Contains(response.Fields[1].Value, "default.server_url must use https") {
				t.Fatalf("details = %q, want validation error", response.Fields[1].Value)
			}
			if len(response.NextSteps) != 1 || !strings.Contains(response.NextSteps[0], "meshify "+tc.command+" --config "+configPath) {
				t.Fatalf("response.NextSteps = %#v, want rerun hint", response.NextSteps)
			}
		})
	}
}

func TestExecute_VerifyReportsInvalidConfigDetails(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	data, err := config.ExampleYAML()
	if err != nil {
		t.Fatalf("ExampleYAML() error = %v", err)
	}
	raw := strings.Replace(string(data), `acme_challenge: "http-01"`, `acme_challenge: "dns-01"`, 1)
	raw = strings.Replace(raw, `provider: ""`, `provider: "unsupported"`, 1)
	if err := os.WriteFile(configPath, []byte(raw), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	stdout, stderr, err := runCLI(t, "verify", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify verify: config file exists but failed validation") {
		t.Fatalf("stdout = %q, want invalid-config summary", stdout)
	}
	if !strings.Contains(stdout, "unsupported DNS-01 provider \"unsupported\"") {
		t.Fatalf("stdout = %q, want unsupported provider detail", stdout)
	}

	jsonStdout, jsonStderr, err := runCLI(t, "verify", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("JSON Execute() error = %v", err)
	}
	if jsonStderr != "" {
		t.Fatalf("JSON stderr = %q, want empty", jsonStderr)
	}
	var response output.Response
	if err := json.Unmarshal([]byte(jsonStdout), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, jsonStdout)
	}
	if response.Status != "invalid-config" {
		t.Fatalf("response.Status = %q, want invalid-config", response.Status)
	}
	value, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(value, "unsupported DNS-01 provider \"unsupported\"") {
		t.Fatalf("details field = %q, %v; fields = %#v", value, ok, response.Fields)
	}
}

func TestExecute_StatusJSONIncludesClientVersionForDeployHistory(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "meshify.yaml")
	if err := config.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	desiredStateDigest, err := deployDesiredStateDigest(cfg)
	if err != nil {
		t.Fatalf("deployDesiredStateDigest() error = %v", err)
	}

	checkpointPath := filepath.Join(baseDir, "state", "checkpoint.json")
	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	t.Cleanup(func() {
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	if err := state.NewStore(checkpointPath).Save(state.Checkpoint{
		DesiredStateDigest:   desiredStateDigest,
		CompletedCheckpoints: []string{deployCheckpointPackageManagerReady},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	stdout, stderr, err := runCLI(t, "status", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var response output.Response
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
	}
	if response.Status != "deploy-history" {
		t.Fatalf("response.Status = %q, want deploy-history", response.Status)
	}
	value, ok := fieldValue(response.Fields, "minimum client version")
	if !ok || value != "Tailscale >= v1.74.0" {
		t.Fatalf("minimum client version field = %q, %v; fields = %#v", value, ok, response.Fields)
	}
}

func TestExecute_StatusMissingConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "missing.yaml")
	stdout, stderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify status: no config file found") {
		t.Fatalf("stdout = %q, want missing-config summary", stdout)
	}
	if !strings.Contains(stdout, configPath) {
		t.Fatalf("stdout = %q, want config path %q", stdout, configPath)
	}
	if !strings.Contains(stdout, "meshify init --config "+configPath) {
		t.Fatalf("stdout = %q, want init hint", stdout)
	}
}

func TestExecute_UnknownCommand(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCLI(t, "unknown")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if err.Error() != "unknown command \"unknown\"" {
		t.Fatalf("error = %q, want %q", err.Error(), "unknown command \"unknown\"")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "Commands:") {
		t.Fatalf("stderr = %q, want help output", stderr)
	}
	if !strings.Contains(stderr, "deploy   Run preflight checks and apply the Headscale, Nginx, TLS, service, and onboarding workflow.") {
		t.Fatalf("stderr = %q, want deploy summary", stderr)
	}
}
