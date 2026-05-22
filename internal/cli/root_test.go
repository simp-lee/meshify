package cli

import (
	"bytes"
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"meshify/internal/appconfig"
	"meshify/internal/apprender"
	"meshify/internal/assets"
	"meshify/internal/components/appsvc"
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
	"time"
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
		"meshify manages init, deploy, verify, status, and app workflows.",
		"Happy path:",
		"meshify init",
		"meshify deploy",
		"meshify verify",
		"app      管理附加 app 部署。",
		"status   Show config readiness and persisted deploy context.",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout = %q, want substring %q", stdout, want)
		}
	}
}

func TestExecute_AppHelpOutputIsChineseReadable(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCLI(t, "app", "--help")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{
		"meshify app 管理附加 Go 服务和 tailnet upstream。",
		"用法:",
		"命令:",
		"生成可编辑的 app 示例配置。",
		"按配置部署一个 app。",
		"校验 app 配置和 runtime 模板。",
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

func TestExecute_AppInitWritesExampleConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify-app.yaml")
	stdout, stderr, err := runCLI(t, "app", "init", "--config", configPath)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify app init: 已写入 app 示例配置") {
		t.Fatalf("stdout = %q, want app init summary", stdout)
	}
	loaded, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if loaded.App.Name != "example-app" {
		t.Fatalf("app.name = %q, want example-app", loaded.App.Name)
	}
}

func TestExecute_AppVerifyRejectsExampleFlag(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCLI(t, "app", "init", "--example")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined: -example") {
		t.Fatalf("error = %q, want unknown example flag", err.Error())
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want empty", stdout, stderr)
	}
}

func TestExecute_AppVerifyJSON(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify-app.yaml")
	if err := appconfig.WriteExampleFile(configPath); err != nil {
		t.Fatalf("WriteExampleFile() error = %v", err)
	}
	stdout, stderr, err := runCLI(t, "app", "verify", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	var response output.Response
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("Unmarshal() error = %v\n%s", err, stdout)
	}
	if response.Command != "app verify" || response.Status != "static-passed" {
		t.Fatalf("response = %#v, want app verify static-passed", response)
	}
	if !strings.Contains(response.Summary, "app 静态检查通过") {
		t.Fatalf("summary = %q, want Chinese app verify summary", response.Summary)
	}
	scope, ok := fieldValue(response.Fields, "verification scope")
	if !ok || !strings.Contains(scope, "static-only") {
		t.Fatalf("verification scope = %q, %v; fields = %#v", scope, ok, response.Fields)
	}
}

func TestExecute_AppVerifyMissingConfigReturnsErrorWithJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "missing-app.yaml")
	stdout, stderr, err := runCLI(t, "app", "verify", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want missing config error")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app verify" || response.Status != "missing-config" {
		t.Fatalf("response = %#v, want app verify missing-config", response)
	}
}

func TestExecute_AppDeployInvalidConfigReturnsErrorWithJSON(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "meshify-app.yaml")
	if err := os.WriteFile(configPath, []byte("api_version: wrong\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want invalid config error")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "invalid-config" {
		t.Fatalf("response = %#v, want app deploy invalid-config", response)
	}
}

func TestExecute_AppDeployPreflightBlockReturnsErrorWithJSON(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	stubPassingAppDeployPreflight(t)
	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "deploy", SudoWorks: true}
	}
	detectAppDNSFn = func(appconfig.Config) map[string]preflight.DNSProbe {
		t.Fatal("detectAppDNSFn called before app deploy root gate")
		return nil
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "blocked" {
		t.Fatalf("response = %#v, want app deploy blocked", response)
	}
	if got, ok := fieldValue(response.Fields, "check permissions"); !ok || !strings.Contains(got, "root 权限") || !strings.Contains(got, "deploy") {
		t.Fatalf("permissions field = %q, %v; fields = %#v", got, ok, response.Fields)
	}
	if !strings.Contains(err.Error(), response.Summary) {
		t.Fatalf("error = %q, want summary %q", err.Error(), response.Summary)
	}
}

func TestExecute_AppDeployBlocksInvalidAuthKeyFileEvenWhenTailscaleAlreadyLoggedIn(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.LoginServer = "https://hs.example.com"
		cfg.Tailscale.AuthKeyFile = "/run/meshify/missing-auth.key"
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)
	detectAppTailscaleAuthKeyFileStateFn = func(appconfig.Config) (bool, bool, string) {
		return true, false, "tailscale auth key file must be root-only"
	}

	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	previousInstaller := newAppFileInstallerFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "tailscale" {
			switch strings.Join(actual.Args, " ") {
			case "status --json":
				return host.Result{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`}, nil
			case "debug prefs":
				return host.Result{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`}, nil
			}
		}
		if actual.Name == "cat" && len(actual.Args) == 1 && actual.Args[0] == "/var/lib/meshify/tailscale-client.json" {
			return host.Result{Stdout: `{"login_server":"https://hs.example.com","accept_dns":false,"accept_routes":false,"shields_up":true,"managed_by":"meshify"}`}, nil
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
		newAppFileInstallerFn = previousInstaller
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want auth key preflight failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "blocked" {
		t.Fatalf("response = %#v, want app deploy blocked", response)
	}
	if got, ok := fieldValue(response.Fields, "check tailscale-auth-key-file"); !ok || !strings.Contains(got, "root-only") {
		t.Fatalf("tailscale auth key check = %q, %v; fields = %#v", got, ok, response.Fields)
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "tailscale" || actual.Name == "apt-get" || actual.Name == "install" || appDeployOrderEvent(command) == "install-files" {
			t.Fatalf("commands = %#v, wanted auth key failure before host mutations", runner.commands)
		}
	}
}

func TestExecute_AppDeployReadsAuthKeyFileOnlyWhenLoginNeeded(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.LoginServer = "https://hs.example.com"
		cfg.Tailscale.AuthKeyFile = "/run/meshify/missing-auth.key"
	})
	stubPassingAppDeployPreflight(t)
	detectAppTailscaleAuthKeyFileStateFn = func(appconfig.Config) (bool, bool, string) {
		return true, true, "tailscale.auth_key_file 已通过 root-only 校验"
	}

	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "tailscale" && strings.Join(actual.Args, " ") == "status --json" {
			return host.Result{Stdout: `{"BackendState":"NeedsLogin"}`}, nil
		}
		return host.Result{}, nil
	}}
	previousExecutor := newHostExecutorFn
	t.Cleanup(func() {
		newHostExecutorFn = previousExecutor
	})
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want auth key read failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Tailscale client 前置条件失败" {
		t.Fatalf("summary = %q, want Tailscale failure", response.Summary)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "tailscale.auth_key_file") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "apt-get" || actual.Name == "install" || appDeployOrderEvent(command) == "install-files" {
			t.Fatalf("commands = %#v, wanted auth key failure before app host mutations", runner.commands)
		}
	}
}

func TestExecute_AppDeployBlocksInvalidServiceEnvFileBeforeHostMutation(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.Service.EnvFile = "/opt/review-app/web.env"
	})
	stubPassingAppDeployPreflight(t)
	detectAppServiceEnvFileStateFn = func(appconfig.Config) (bool, bool, string) {
		return true, false, "service.env_file must be root-only"
	}

	runner := &scriptedHostRunner{}
	previousExecutor := newHostExecutorFn
	t.Cleanup(func() {
		newHostExecutorFn = previousExecutor
	})
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "blocked" {
		t.Fatalf("response = %#v, want app deploy blocked", response)
	}
	if got, ok := fieldValue(response.Fields, "check service-env-file"); !ok || !strings.Contains(got, "root-only") {
		t.Fatalf("service-env-file field = %q, %v; fields = %#v", got, ok, response.Fields)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("host commands = %#v, want no host mutation before service.env_file readiness passes", runner.commands)
	}
}

func TestExecute_AppVerifyRejectsDefaultMeshifyServerDomain(t *testing.T) {
	baseDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(baseDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	writeReviewMainConfig(t, filepath.Join(baseDir, "meshify.yaml"), "https://hs.example.com")
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Domains = []string{"hs.example.com"}
	})

	stdout, stderr, err := runCLI(t, "app", "verify", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app verify" || response.Status != "invalid-config" {
		t.Fatalf("response = %#v, want app verify invalid-config", response)
	}
	if !strings.Contains(response.Summary, "Headscale server_url") {
		t.Fatalf("summary = %q, want server_url conflict", response.Summary)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "hs.example.com") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
}

func TestExecute_AppDeployRejectsMeshifyConfigServerDomain(t *testing.T) {
	baseDir := t.TempDir()
	mainConfigPath := filepath.Join(baseDir, "meshify.yaml")
	writeReviewMainConfig(t, mainConfigPath, "https://hs.example.com")
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Domains = []string{"hs.example.com"}
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.MeshifyConfig = mainConfigPath
	})

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "invalid-config" {
		t.Fatalf("response = %#v, want app deploy invalid-config", response)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, mainConfigPath) || !strings.Contains(details, "hs.example.com") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
}

func TestExecute_AppVerifyRejectsCustomHeadscaleMetricsPortConflict(t *testing.T) {
	baseDir := t.TempDir()
	previousDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(baseDir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousDir); err != nil {
			t.Fatalf("restore Chdir() error = %v", err)
		}
	})
	mainCfg := config.ExampleConfig()
	mainCfg.Advanced.Headscale.MetricsPort = 18001
	if err := mainCfg.WriteFile(filepath.Join(baseDir, "meshify.yaml")); err != nil {
		t.Fatalf("WriteFile(main config) error = %v", err)
	}
	configPath := writeReviewAppConfig(t)

	stdout, stderr, err := runCLI(t, "app", "verify", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app verify" || response.Status != "invalid-config" {
		t.Fatalf("response = %#v, want app verify invalid-config", response)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "metrics port 18001") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
}

func TestExecute_AppDeployRejectsCustomHeadscaleMetricsPortConflict(t *testing.T) {
	baseDir := t.TempDir()
	mainConfigPath := filepath.Join(baseDir, "meshify.yaml")
	mainCfg := config.ExampleConfig()
	mainCfg.Advanced.Headscale.MetricsPort = 18001
	if err := mainCfg.WriteFile(mainConfigPath); err != nil {
		t.Fatalf("WriteFile(main config) error = %v", err)
	}
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.Tailscale.MeshifyConfig = mainConfigPath
	})

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "invalid-config" {
		t.Fatalf("response = %#v, want app deploy invalid-config", response)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "metrics port 18001") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
}

func TestExecute_AppDeployStaticFailureReturnsErrorWithJSON(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	stubPassingAppDeployPreflight(t)
	previousStage := stageAppRuntimeFilesFn
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return []apprender.StagedFile{{
			SourcePath: "templates/app/nginx.conf.tmpl",
			HostPath:   filepath.Join(t.TempDir(), "review-app.conf"),
			Mode:       0o644,
			Content:    []byte("server { listen 443 ssl; }"),
		}}, nil
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "failed" {
		t.Fatalf("response = %#v, want app deploy failed", response)
	}
	if !strings.Contains(response.Summary, "app verify 发现") {
		t.Fatalf("summary = %q, want static verify failure", response.Summary)
	}
}

func TestExecute_AppVerifyStaticFailureReturnsErrorWithJSON(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	previousStage := stageAppRuntimeFilesFn
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return []apprender.StagedFile{{
			SourcePath: "templates/app/nginx.conf.tmpl",
			HostPath:   filepath.Join(t.TempDir(), "review-app.conf"),
			Mode:       0o644,
			Content:    []byte("server { listen 443 ssl; }"),
		}}, nil
	}

	stdout, stderr, err := runCLI(t, "app", "verify", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app verify" || response.Status != "failed" {
		t.Fatalf("response = %#v, want app verify failed", response)
	}
}

func TestExecute_AppDeployOwnershipBlockReturnsErrorWithJSON(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)
	previousStage := stageAppRuntimeFilesFn
	previousFileSystem := newAppHostFileSystemFn
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppHostFileSystemFn = previousFileSystem
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppHostFileSystemFn = func(host.Executor, host.PrivilegeStrategy) host.FileSystem {
		return readOnlyAppFileSystem{files: map[string][]byte{
			staged[0].HostPath: []byte("foreign content\n"),
		}}
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "blocked" {
		t.Fatalf("response = %#v, want app deploy blocked", response)
	}
	if !strings.Contains(response.Summary, "目标文件存在") {
		t.Fatalf("summary = %q, want ownership block", response.Summary)
	}
}

func TestExecute_AppDeployHostFailureReturnsErrorWithJSON(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.Name == "apt-get" && slices.Contains(command.Args, "update") {
			return host.Result{ExitCode: 100, Stderr: "apt failed"}, errors.New("apt update failed")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Command != "app deploy" || response.Status != "failed" {
		t.Fatalf("response = %#v, want app deploy failed", response)
	}
	if response.Summary != "安装 app 宿主机依赖失败" {
		t.Fatalf("summary = %q, want host dependency failure", response.Summary)
	}
	if !strings.Contains(err.Error(), "apt update failed") {
		t.Fatalf("error = %q, want apt failure", err.Error())
	}
}

func TestExecute_AppDeployInstallsFullHostDependencySet(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.Name == "mkdir" {
			return host.Result{ExitCode: 1}, errors.New("stop after dependency install")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	_, _, err = runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	var installArgs []string
	for _, command := range runner.commands {
		if command.Name == "apt-get" && len(command.Args) >= 2 && command.Args[0] == "install" {
			installArgs = command.Args
			break
		}
	}
	if len(installArgs) == 0 {
		t.Fatalf("commands = %#v, want apt-get install", runner.commands)
	}
	for _, want := range []string{"nginx", "ca-certificates", "curl", "tar", "openssl"} {
		if !slices.Contains(installArgs, want) {
			t.Fatalf("apt-get install args = %#v, want %s", installArgs, want)
		}
	}
}

func TestExecute_AppDeploySurfacesPreflightWarningsOnSuccess(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
		cfg.DNS01.Provider = "route53"
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)
	detectAppDNSFn = func(cfg appconfig.Config) map[string]preflight.DNSProbe {
		return map[string]preflight.DNSProbe{
			cfg.App.Domains[0]: {Host: cfg.App.Domains[0], ResolvedIPs: []string{"8.8.8.8"}},
		}
	}
	detectAppDNSCredentialStateFn = func(appconfig.Config) (bool, bool, string) {
		return true, true, "ready"
	}

	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	previousInstaller := newAppFileInstallerFn
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
		newAppFileInstallerFn = previousInstaller
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(&scriptedHostRunner{}, env)
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v\nstdout=%s", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Status != "applied" {
		t.Fatalf("response = %#v, want applied", response)
	}
	warning, ok := fieldValue(response.Fields, "preflight warning dns:app.example.com")
	if !ok || !strings.Contains(warning, "未能确认") {
		t.Fatalf("preflight warning field = %q, %v; fields = %#v", warning, ok, response.Fields)
	}
	if !slices.ContainsFunc(response.NextSteps, func(step string) bool {
		return strings.Contains(step, "advanced.network.public_ipv4")
	}) {
		t.Fatalf("next steps = %#v, want DNS warning remediation", response.NextSteps)
	}
}

func TestExecute_AppDeployBlocksHTTP01WhenHostAlignmentIsUnproven(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	stubPassingAppDeployPreflight(t)
	detectAppDNSFn = func(cfg appconfig.Config) map[string]preflight.DNSProbe {
		return map[string]preflight.DNSProbe{
			cfg.App.Domains[0]: {Host: cfg.App.Domains[0], ResolvedIPs: []string{"8.8.8.8"}},
		}
	}

	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		newHostExecutorFn = previousExecutor
	})
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want HTTP-01 DNS alignment block")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Status != "blocked" {
		t.Fatalf("response = %#v, want blocked", response)
	}
	dnsCheck, ok := fieldValue(response.Fields, "check dns:app.example.com")
	if !ok || !strings.Contains(dnsCheck, "未能确认") {
		t.Fatalf("dns check = %q, %v; fields = %#v", dnsCheck, ok, response.Fields)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("host commands = %#v, want block before host mutation", runner.commands)
	}
}

func TestDetectAppExpectedPublicIPsFallsBackToCurrentHostProbe(t *testing.T) {
	previousDetect := detectAppCurrentPublicIPsFn
	t.Cleanup(func() {
		detectAppCurrentPublicIPsFn = previousDetect
	})
	detectAppCurrentPublicIPsFn = func(*http.Client) (string, string) {
		return "8.8.8.8", "2001:4860:4860::8888"
	}

	cfg := appconfig.New()
	cfg.Tailscale.LoginServer = "https://hs.example.com"
	ipv4, ipv6 := detectAppExpectedPublicIPs(cfg)
	if ipv4 != "8.8.8.8" || ipv6 != "2001:4860:4860::8888" {
		t.Fatalf("detectAppExpectedPublicIPs() = %q, %q; want detected public IPs", ipv4, ipv6)
	}
}

func TestExecute_AppDeployFileInstallFailureReportsModifiedPaths(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{
			results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}},
			err:     errors.New("write failed"),
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "写入 app runtime 文件失败" {
		t.Fatalf("summary = %q, want file install failure", response.Summary)
	}
	paths, ok := fieldValue(response.Fields, "modified paths")
	if !ok || !strings.Contains(paths, "/etc/nginx/sites-available/review-app.conf") {
		t.Fatalf("modified paths = %q, %v; fields = %#v", paths, ok, response.Fields)
	}
}

func TestExecute_AppDeployPostInstallFailureReportsModifiedPaths(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.Name == "systemctl" && strings.Join(command.Args, " ") == "daemon-reload" {
			return host.Result{ExitCode: 1, Stderr: "daemon reload failed"}, errors.New("daemon reload failed")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "systemd daemon-reload 失败" {
		t.Fatalf("summary = %q, want daemon-reload failure", response.Summary)
	}
	paths, ok := fieldValue(response.Fields, "modified paths")
	if !ok || !strings.Contains(paths, "/etc/nginx/sites-available/review-app.conf") {
		t.Fatalf("modified paths = %q, %v; fields = %#v", paths, ok, response.Fields)
	}
}

func TestExecute_AppDeployEnablesNginxBeforeRuntimeActivation(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	events := []string{}
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "systemctl" && strings.Join(actual.Args, " ") == "enable --now nginx.service" {
			events = append(events, "systemctl-enable-now nginx.service")
		}
		if event := appDeployOrderEvent(command); event != "" {
			events = append(events, event)
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return recordingAppInstaller{
			events:  &events,
			results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}},
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v\nstdout=%s", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	assertEventBefore(t, events, "systemctl-enable-now nginx.service", "install-files")
	assertEventBefore(t, events, "systemctl-enable-now nginx.service", "nginx-reload")
}

func TestExecute_AppDeployCertificateFailureReportsCommandEffects(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}

	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "sh" && len(actual.Args) >= 3 && actual.Args[2] == "meshify-app-lego-issue-or-renew" {
			return host.Result{ExitCode: 1, Stderr: "lego failed"}, errors.New("lego failed")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "申请 app TLS 证书失败" {
		t.Fatalf("summary = %q, want certificate failure", response.Summary)
	}
	paths, ok := fieldValue(response.Fields, "modified paths")
	if !ok {
		t.Fatalf("fields = %#v, want modified paths", response.Fields)
	}
	for _, want := range []string{
		"/etc/nginx/sites-available/review-app.conf",
		names.TLSMarkerPath,
		names.FullchainPath,
		names.PrivateKeyPath,
		names.NginxEnabledPath,
	} {
		if !strings.Contains(paths, want) {
			t.Fatalf("modified paths = %q, want %s", paths, want)
		}
	}
	actions, ok := fieldValue(response.Fields, "host actions")
	if !ok || !strings.Contains(actions, "reloaded Nginx") {
		t.Fatalf("host actions = %q, %v; fields = %#v", actions, ok, response.Fields)
	}
}

func TestExecute_AppDeployUpstreamRemovesManagedListenService(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.LoginServer = "https://hs.example.com"
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	servicePath := "/etc/systemd/system/" + names.ServiceUnit

	events := []string{}
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if event := appDeployOrderEvent(command); event != "" {
			events = append(events, event)
		}
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "tailscale" && strings.Join(actual.Args, " ") == "status --json" {
			return host.Result{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`}, nil
		}
		if actual.Name == "tailscale" && strings.Join(actual.Args, " ") == "debug prefs" {
			return host.Result{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`}, nil
		}
		if actual.Name == "cat" && len(actual.Args) == 1 && actual.Args[0] == "/var/lib/meshify/tailscale-client.json" {
			return host.Result{Stdout: `{"login_server":"https://hs.example.com","accept_dns":false,"accept_routes":false,"shields_up":true,"managed_by":"meshify"}`}, nil
		}
		if actual.Name == "sh" && len(actual.Args) >= 3 && actual.Args[2] == "meshify-app-remove-stale-service" {
			return host.Result{Stdout: servicePath + "\n"}, nil
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return recordingAppInstaller{
			events:  &events,
			results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}},
		}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v\nstdout=%s", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Status != "applied" {
		t.Fatalf("response = %#v, want applied", response)
	}
	assertEventBefore(t, events, "install-files", "remove-stale-service")
	assertEventBefore(t, events, "nginx-reload", "remove-stale-service")
	assertEventBefore(t, events, "lego-run", "remove-stale-service")
	removeIndex := slices.Index(events, "remove-stale-service")
	lastDaemonReloadIndex := -1
	for index, event := range events {
		if event == "systemd-daemon-reload" {
			lastDaemonReloadIndex = index
		}
	}
	if removeIndex < 0 || lastDaemonReloadIndex < 0 || removeIndex > lastDaemonReloadIndex {
		t.Fatalf("events = %v, want stale service removal before final daemon-reload", events)
	}
	paths, ok := fieldValue(response.Fields, "modified paths")
	if !ok || !strings.Contains(paths, servicePath) {
		t.Fatalf("modified paths = %q, %v; fields = %#v", paths, ok, response.Fields)
	}
	if slices.Contains(events, "systemctl-enable review-app.service") || slices.Contains(events, "systemctl-restart review-app.service") {
		t.Fatalf("events = %v, did not expect local app service activation", events)
	}
}

func TestExecute_AppDeployEnabledSiteGuardBlocksForeignPath(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-nginx-enabled-site" {
			return host.Result{ExitCode: 1, Stderr: "foreign enabled site"}, errors.New("foreign enabled site")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Status != "blocked" || response.Summary != "Nginx enabled site 已存在且不属于当前 app" {
		t.Fatalf("response = %#v, want pre-write enabled-site block", response)
	}
	if details, ok := fieldValue(response.Fields, "details"); !ok || !strings.Contains(details, "foreign enabled site") {
		t.Fatalf("details = %q, %v; fields = %#v", details, ok, response.Fields)
	}
	if installerCalled {
		t.Fatal("app file installer was called before enabled-site guard passed")
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "ln" || actual.Name == "apt-get" || actual.Name == "mkdir" || actual.Name == "/opt/meshify/bin/lego" {
			t.Fatalf("commands = %#v, wanted enabled-site guard to block before host mutations", runner.commands)
		}
	}
}

func TestExecute_AppDeployTLSOwnershipGuardBlocksBeforeRuntimeInstall(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-app-tls" {
			return host.Result{ExitCode: 1, Stderr: "foreign tls files"}, errors.New("foreign tls files")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "app TLS 证书目录所有权检查失败" {
		t.Fatalf("summary = %q, want TLS ownership failure", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before TLS ownership guard passed")
	}
	for _, command := range runner.commands {
		if appDeployOrderEvent(command) == "install-files" {
			t.Fatalf("commands = %#v, wanted TLS guard to block before runtime install", runner.commands)
		}
	}
}

func TestExecute_AppDeployServiceAccessGuardBlocksBeforeRuntimeInstall(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-app-service-access" {
			return host.Result{ExitCode: 1, Stderr: "app user cannot execute root-only binary"}, errors.New("app user cannot execute root-only binary")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want service access guard failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "app service 用户访问检查失败" {
		t.Fatalf("summary = %q, want service access failure", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before service access guard passed")
	}
	for _, command := range runner.commands {
		if appDeployOrderEvent(command) == "install-files" || appDeployOrderEvent(command) == "lego-run" {
			t.Fatalf("commands = %#v, wanted service access guard to block before runtime or certificate changes", runner.commands)
		}
	}
}

func TestExecute_AppDeployRootDirectoryGuardBlocksBeforeRuntimeInstall(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-app-root-directories" {
			return host.Result{ExitCode: 1, Stderr: "/etc/review-app exists without marker"}, errors.New("foreign app root")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want root directory guard failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "app 目录根所有权检查失败" {
		t.Fatalf("summary = %q, want app root ownership failure", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before app root directory guard passed")
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if command.DisplayName == "ensure-app-user" {
			t.Fatalf("commands = %#v, wanted root guard to block before app user creation", runner.commands)
		}
		if actual.Name == "install" || appDeployOrderEvent(command) == "install-files" {
			t.Fatalf("commands = %#v, wanted root guard to block before app directory/runtime writes", runner.commands)
		}
	}
}

func TestExecute_AppDeployBlocksNginxDomainConflictBeforeRuntimeInstall(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-nginx-server-names" {
			return host.Result{ExitCode: 1, Stderr: "duplicate server_name"}, errors.New("duplicate server_name")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want Nginx server_name conflict")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Nginx server_name 冲突" {
		t.Fatalf("summary = %q, want server_name conflict", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before Nginx server_name guard passed")
	}
}

func TestExecute_AppDeployBlocksNginxDefaultServerConflictBeforeRuntimeInstall(t *testing.T) {
	configPath := writeReviewAppConfig(t)
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		if command.DisplayName == "guard-nginx-default-server" {
			return host.Result{ExitCode: 1, Stderr: "custom default_server"}, errors.New("custom default_server")
		}
		return host.Result{}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want Nginx default_server conflict")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Nginx default_server 冲突" {
		t.Fatalf("summary = %q, want default_server conflict", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before Nginx default_server guard passed")
	}
}

func TestExecute_AppDeployHappyPathOrder(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*appconfig.Config)
		wantBefore [][2]string
		wantAbsent []string
	}{
		{
			name: "listen http01",
			wantBefore: [][2]string{
				{"install-files", "systemd-daemon-reload"},
				{"systemd-daemon-reload", "http01-bootstrap"},
				{"http01-bootstrap", "nginx-enabled-guard-activate"},
				{"nginx-enabled-guard-activate", "nginx-enable"},
				{"nginx-enable", "lego-migrate"},
				{"lego-migrate", "lego-run"},
				{"lego-run", "systemctl-enable review-app.service"},
				{"systemctl-restart review-app.service", "systemctl-enable review-app-lego-renew.timer"},
			},
		},
		{
			name: "listen dns01",
			configure: func(cfg *appconfig.Config) {
				cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
				cfg.DNS01.Provider = "route53"
			},
			wantBefore: [][2]string{
				{"install-files", "systemd-daemon-reload"},
				{"systemd-daemon-reload", "lego-migrate"},
				{"lego-migrate", "lego-run"},
				{"lego-run", "nginx-enabled-guard-activate"},
				{"nginx-enabled-guard-activate", "nginx-enable"},
				{"nginx-enable", "systemctl-enable review-app.service"},
			},
		},
		{
			name: "upstream http01",
			configure: func(cfg *appconfig.Config) {
				cfg.App.Listen = ""
				cfg.App.Upstream = "100.64.10.20:18001"
				cfg.Service.ExecStart = ""
				cfg.Service.WorkingDirectory = ""
				cfg.Tailscale.LoginServer = "https://hs.example.com"
			},
			wantBefore: [][2]string{
				{"tailscale-status", "install-files"},
				{"install-files", "systemd-daemon-reload"},
				{"systemd-daemon-reload", "http01-bootstrap"},
				{"http01-bootstrap", "nginx-enabled-guard-activate"},
				{"nginx-enable", "lego-migrate"},
				{"lego-migrate", "lego-run"},
				{"lego-run", "systemctl-enable review-app-lego-renew.timer"},
			},
			wantAbsent: []string{"systemctl-enable review-app.service", "systemctl-restart review-app.service"},
		},
		{
			name: "upstream dns01",
			configure: func(cfg *appconfig.Config) {
				cfg.App.Listen = ""
				cfg.App.Upstream = "100.64.10.20:18001"
				cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
				cfg.Service.ExecStart = ""
				cfg.Service.WorkingDirectory = ""
				cfg.DNS01.Provider = "route53"
				cfg.Tailscale.LoginServer = "https://hs.example.com"
			},
			wantBefore: [][2]string{
				{"tailscale-status", "install-files"},
				{"install-files", "systemd-daemon-reload"},
				{"systemd-daemon-reload", "lego-migrate"},
				{"lego-migrate", "lego-run"},
				{"lego-run", "nginx-enabled-guard-activate"},
				{"nginx-enable", "systemctl-enable review-app-lego-renew.timer"},
			},
			wantAbsent: []string{"systemctl-enable review-app.service", "systemctl-restart review-app.service"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			configPath := writeReviewAppConfigWith(t, tt.configure)
			cfg, err := appconfig.LoadFile(configPath)
			if err != nil {
				t.Fatalf("LoadFile() error = %v", err)
			}
			staged := stagedRuntimeWithTempHostPaths(t, cfg)
			stubPassingAppDeployPreflight(t)
			if cfg.App.ACMEChallenge == appconfig.ACMEChallengeDNS01 {
				detectAppDNSCredentialStateFn = func(appconfig.Config) (bool, bool, string) {
					return true, true, "ready"
				}
			}

			events := []string{}
			enabledSiteGuardCount := 0
			previousStage := stageAppRuntimeFilesFn
			previousInstaller := newAppFileInstallerFn
			previousExecutor := newHostExecutorFn
			runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
				if event := appDeployOrderEvent(command); event != "" {
					if event == "nginx-enabled-guard" {
						enabledSiteGuardCount++
						if enabledSiteGuardCount == 1 {
							event = "nginx-enabled-guard-prewrite"
						} else {
							event = "nginx-enabled-guard-activate"
						}
					}
					events = append(events, event)
				}
				actual := unwrapMaybeSudoHostCommand(command)
				if actual.Name == "tailscale" && strings.Join(actual.Args, " ") == "status --json" {
					return host.Result{Stdout: `{"BackendState":"Running","Self":{"Online":true}}`}, nil
				}
				if actual.Name == "tailscale" && strings.Join(actual.Args, " ") == "debug prefs" {
					return host.Result{Stdout: `{"ControlURL":"https://hs.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`}, nil
				}
				if actual.Name == "cat" && len(actual.Args) == 1 && actual.Args[0] == "/var/lib/meshify/tailscale-client.json" {
					return host.Result{Stdout: `{"login_server":"https://hs.example.com","accept_dns":false,"accept_routes":false,"shields_up":true,"managed_by":"meshify"}`}, nil
				}
				return host.Result{}, nil
			}}
			t.Cleanup(func() {
				stageAppRuntimeFilesFn = previousStage
				newAppFileInstallerFn = previousInstaller
				newHostExecutorFn = previousExecutor
			})
			stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
				return staged, nil
			}
			newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
				return recordingAppInstaller{
					events:  &events,
					results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}},
				}
			}
			newHostExecutorFn = func(env map[string]string) host.Executor {
				return host.NewExecutor(runner, env)
			}

			stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
			if err != nil {
				t.Fatalf("Execute() error = %v\nstdout=%s", err, stdout)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
			response := mustDecodeResponse(t, stdout)
			if response.Status != "applied" {
				t.Fatalf("response = %#v, want applied", response)
			}
			for _, pair := range tt.wantBefore {
				assertEventBefore(t, events, pair[0], pair[1])
			}
			assertEventBefore(t, events, "nginx-enabled-guard-prewrite", "install-files")
			for _, absent := range tt.wantAbsent {
				if slices.Contains(events, absent) {
					t.Fatalf("events = %v, did not expect %q", events, absent)
				}
			}
		})
	}
}

func TestExecute_AppDeployChecksTailscaleBeforeAppHostMutations(t *testing.T) {
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.LoginServer = "https://hs.example.com"
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	installerCalled := false
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "tailscale" {
			switch strings.Join(actual.Args, " ") {
			case "version":
				return host.Result{Command: command, Stdout: "1.80.0\n"}, nil
			case "status --json":
				return host.Result{Command: command, Stdout: `{"BackendState":"Running","Self":{"Online":true}}`}, nil
			case "debug prefs":
				return host.Result{Command: command, Stdout: `{"ControlURL":"https://other.example.com","RouteAll":false,"CorpDNS":false,"ShieldsUp":true}`}, nil
			}
		}
		return host.Result{Command: command}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		installerCalled = true
		return stubFileInstaller{}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Tailscale client 前置条件失败" {
		t.Fatalf("summary = %q, want Tailscale failure", response.Summary)
	}
	if installerCalled {
		t.Fatal("app file installer was called before Tailscale correctness was proven")
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "apt-get" || actual.Name == "mkdir" {
			t.Fatalf("commands = %#v, want no app host dependency or directory mutation before Tailscale failure", runner.commands)
		}
	}
}

func TestExecute_AppDeployCreatesLocalHeadscalePreauthKeyForDerivedLoginServer(t *testing.T) {
	baseDir := t.TempDir()
	mainConfigPath := filepath.Join(baseDir, "meshify.yaml")
	writeReviewMainConfig(t, mainConfigPath, "https://hs.example.com")
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.MeshifyConfig = mainConfigPath
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	secret := "tskey-auth-secret-cli-regression"
	previousStage := stageAppRuntimeFilesFn
	previousInstaller := newAppFileInstallerFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "tailscale":
			if strings.Join(actual.Args, " ") == "status --json" {
				return host.Result{Command: command, Stdout: `{"BackendState":"NeedsLogin"}`}, nil
			}
		case "headscale":
			args := strings.Join(actual.Args, " ")
			switch {
			case strings.Contains(args, "users list --output json"):
				return host.Result{Command: command, Stdout: `[{"id":2,"name":"meshify"}]`}, nil
			case strings.Contains(args, "preauthkeys create"):
				return host.Result{Command: command, Stdout: secret + "\n"}, nil
			}
		}
		return host.Result{Command: command}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newAppFileInstallerFn = previousInstaller
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newAppFileInstallerFn = func(host.Executor, host.PrivilegeStrategy) appStagedFileInstaller {
		return stubFileInstaller{results: []host.FileInstallResult{{HostPath: "/etc/nginx/sites-available/review-app.conf", Changed: true}}}
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err != nil {
		t.Fatalf("Execute() error = %v\nstdout=%s", err, stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Status != "applied" {
		t.Fatalf("response = %#v, want applied", response)
	}
	if strings.Contains(stdout, secret) {
		t.Fatalf("stdout leaked local Headscale preauth key: %q", stdout)
	}

	preauthFound := false
	tailscaleUpFound := false
	tailscaledEnableCount := 0
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		args := strings.Join(actual.Args, " ")
		if actual.Name == "systemctl" && args == "enable --now tailscaled.service" {
			tailscaledEnableCount++
		}
		if actual.Name == "headscale" && strings.Contains(args, "preauthkeys create") {
			preauthFound = true
			if !strings.Contains(args, "--expiration 1h") {
				t.Fatalf("preauth command args = %q, want 1h expiration", args)
			}
			if strings.Contains(args, "--reusable") {
				t.Fatalf("preauth command args = %q, must not be reusable", args)
			}
		}
		if actual.Name == "tailscale" && len(actual.Args) > 0 && actual.Args[0] == "up" {
			tailscaleUpFound = true
			if !slices.Contains(actual.Args, "--login-server") || !slices.Contains(actual.Args, "https://hs.example.com") {
				t.Fatalf("tailscale up args = %#v, want derived login server", actual.Args)
			}
			if !slices.Contains(actual.Args, secret) {
				t.Fatalf("tailscale up args = %#v, want auth key passed to child process", actual.Args)
			}
			if strings.Contains(command.String(), secret) {
				t.Fatalf("tailscale up display leaked auth key: %q", command.String())
			}
			if !strings.Contains(command.String(), "<redacted>") {
				t.Fatalf("tailscale up display = %q, want redacted auth key", command.String())
			}
		}
	}
	if !preauthFound {
		t.Fatalf("commands = %#v, want local Headscale preauth creation", runner.commands)
	}
	if !tailscaleUpFound {
		t.Fatalf("commands = %#v, want tailscale up", runner.commands)
	}
	if tailscaledEnableCount != 1 {
		t.Fatalf("tailscaled enable count = %d, want 1; commands = %#v", tailscaledEnableCount, runner.commands)
	}
}

func TestExecute_AppDeployMasksLocalHeadscalePreauthKeyWhenPreauthCreationFails(t *testing.T) {
	baseDir := t.TempDir()
	mainConfigPath := filepath.Join(baseDir, "meshify.yaml")
	writeReviewMainConfig(t, mainConfigPath, "https://hs.example.com")
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.MeshifyConfig = mainConfigPath
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	secret := "tskey-auth-secret-cli-regression"
	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "tailscale":
			if strings.Join(actual.Args, " ") == "status --json" {
				return host.Result{Command: command, Stdout: `{"BackendState":"NeedsLogin"}`}, nil
			}
		case "headscale":
			args := strings.Join(actual.Args, " ")
			switch {
			case strings.Contains(args, "users list --output json"):
				return host.Result{Command: command, Stdout: `[{"id":2,"name":"meshify"}]`}, nil
			case strings.Contains(args, "preauthkeys create"):
				return host.Result{Command: command, Stdout: secret + "\n", Stderr: "created " + secret + " but failed", ExitCode: 1}, errors.New("headscale failed with " + secret)
			}
		}
		return host.Result{Command: command}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("failure output leaked local Headscale preauth key:\nstdout=%s\nerr=%v", stdout, err)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Tailscale client 前置条件失败" {
		t.Fatalf("summary = %q, want Tailscale failure", response.Summary)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "<redacted>") {
		t.Fatalf("details = %q, %v; want redacted auth key", details, ok)
	}
}

func TestExecute_AppDeployMasksLocalHeadscalePreauthKeyWhenTailscaleUpFails(t *testing.T) {
	baseDir := t.TempDir()
	mainConfigPath := filepath.Join(baseDir, "meshify.yaml")
	writeReviewMainConfig(t, mainConfigPath, "https://hs.example.com")
	configPath := writeReviewAppConfigWith(t, func(cfg *appconfig.Config) {
		cfg.App.Listen = ""
		cfg.App.Upstream = "100.64.10.20:18001"
		cfg.Service.ExecStart = ""
		cfg.Service.WorkingDirectory = ""
		cfg.Tailscale.MeshifyConfig = mainConfigPath
	})
	cfg, err := appconfig.LoadFile(configPath)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	staged := stagedRuntimeWithTempHostPaths(t, cfg)
	stubPassingAppDeployPreflight(t)

	secret := "tskey-auth-secret-cli-regression"
	previousStage := stageAppRuntimeFilesFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "tailscale":
			if strings.Join(actual.Args, " ") == "status --json" {
				return host.Result{Command: command, Stdout: `{"BackendState":"NeedsLogin"}`}, nil
			}
			if len(actual.Args) > 0 && actual.Args[0] == "up" {
				return host.Result{Command: command, Stderr: "failed with " + secret, ExitCode: 1}, errors.New("tailscale rejected " + secret)
			}
		case "headscale":
			args := strings.Join(actual.Args, " ")
			switch {
			case strings.Contains(args, "users list --output json"):
				return host.Result{Command: command, Stdout: `[{"id":2,"name":"meshify"}]`}, nil
			case strings.Contains(args, "preauthkeys create"):
				return host.Result{Command: command, Stdout: secret + "\n"}, nil
			}
		}
		return host.Result{Command: command}, nil
	}}
	t.Cleanup(func() {
		stageAppRuntimeFilesFn = previousStage
		newHostExecutorFn = previousExecutor
	})
	stageAppRuntimeFilesFn = func(appconfig.Config) ([]apprender.StagedFile, error) {
		return staged, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "app", "deploy", "--config", configPath, "--format", "json")
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, secret) || strings.Contains(err.Error(), secret) {
		t.Fatalf("failure output leaked local Headscale preauth key:\nstdout=%s\nerr=%v", stdout, err)
	}
	response := mustDecodeResponse(t, stdout)
	if response.Summary != "Tailscale client 前置条件失败" {
		t.Fatalf("summary = %q, want Tailscale failure", response.Summary)
	}
	details, ok := fieldValue(response.Fields, "details")
	if !ok || !strings.Contains(details, "<redacted>") {
		t.Fatalf("details = %q, %v; want redacted auth key", details, ok)
	}
}

func mustDecodeResponse(t *testing.T, stdout string) output.Response {
	t.Helper()

	var response output.Response
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; stdout = %q", err, stdout)
	}
	return response
}

func writeReviewAppConfig(t *testing.T) string {
	t.Helper()

	return writeReviewAppConfigWith(t, nil)
}

func writeReviewAppConfigWith(t *testing.T, configure func(*appconfig.Config)) string {
	t.Helper()

	configPath := filepath.Join(t.TempDir(), "meshify-app.yaml")
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"
	cfg.Service.WorkingDirectory = "/tmp"
	if configure != nil {
		configure(&cfg)
	}
	if err := cfg.WriteFile(configPath); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return configPath
}

func writeReviewMainConfig(t *testing.T, path string, serverURL string) {
	t.Helper()

	cfg := config.ExampleConfig()
	cfg.Default.ServerURL = serverURL
	cfg.Default.BaseDomain = "tailnet.example.com"
	if err := cfg.WriteFile(path); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func appDeployOrderEvent(command host.Command) string {
	if command.DisplayName == "guard-nginx-enabled-site" {
		return "nginx-enabled-guard"
	}
	actual := unwrapMaybeSudoHostCommand(command)
	switch actual.Name {
	case "tailscale":
		if len(actual.Args) > 0 && actual.Args[0] == "status" {
			return "tailscale-status"
		}
	case "systemctl":
		switch strings.Join(actual.Args, " ") {
		case "daemon-reload":
			return "systemd-daemon-reload"
		case "enable review-app.service":
			return "systemctl-enable review-app.service"
		case "restart review-app.service":
			return "systemctl-restart review-app.service"
		case "enable review-app-lego-renew.timer":
			return "systemctl-enable review-app-lego-renew.timer"
		case "start review-app-lego-renew.timer":
			return "systemctl-start review-app-lego-renew.timer"
		case "reload-or-restart nginx.service":
			return "nginx-reload"
		}
	case "sh":
		if len(actual.Args) >= 3 {
			switch actual.Args[2] {
			case "meshify-app-tls-bootstrap":
				return "http01-bootstrap"
			case "meshify-app-remove-stale-service":
				return "remove-stale-service"
			case "meshify-lego-v5-migration-gate":
				return "lego-migrate"
			case "meshify-app-lego-issue-or-renew":
				return "lego-run"
			case "meshify-app-lego-dns01":
				return "lego-run"
			}
		}
	case "ln":
		return "nginx-enable"
	case "nginx":
		if strings.Join(actual.Args, " ") == "-t" {
			return "nginx-test"
		}
	case "/opt/meshify/bin/lego":
		if len(actual.Args) == 1 && actual.Args[0] == "--version" {
			return "lego-version"
		}
		return "lego-run"
	}
	return ""
}

func assertEventBefore(t *testing.T, events []string, before string, after string) {
	t.Helper()

	beforeIndex := slices.Index(events, before)
	afterIndex := slices.Index(events, after)
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("events = %v, want %q before %q", events, before, after)
	}
}

type recordingAppInstaller struct {
	events  *[]string
	results []host.FileInstallResult
}

func (installer recordingAppInstaller) Install(_ []render.StagedFile) ([]host.FileInstallResult, error) {
	*installer.events = append(*installer.events, "install-files")
	return append([]host.FileInstallResult(nil), installer.results...), nil
}

func stagedRuntimeWithTempHostPaths(t *testing.T, cfg appconfig.Config) []apprender.StagedFile {
	t.Helper()

	staged, err := apprender.StageRuntime(cfg)
	if err != nil {
		t.Fatalf("StageRuntime() error = %v", err)
	}
	return staged
}

type readOnlyAppFileSystem struct {
	files map[string][]byte
}

func (fileSystem readOnlyAppFileSystem) MkdirAll(string, fs.FileMode) error {
	return nil
}

func (fileSystem readOnlyAppFileSystem) ReadFile(name string) ([]byte, error) {
	content, ok := fileSystem.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), content...), nil
}

func (fileSystem readOnlyAppFileSystem) WriteFile(string, []byte, fs.FileMode) error {
	return nil
}

func (fileSystem readOnlyAppFileSystem) Chmod(string, fs.FileMode) error {
	return nil
}

func (fileSystem readOnlyAppFileSystem) Stat(string) (fs.FileInfo, error) {
	return nil, os.ErrNotExist
}

func stubPassingAppDeployPreflight(t *testing.T) {
	t.Helper()

	previousPermissionState := detectPermissionStateFn
	previousDetectAppDNS := detectAppDNSFn
	previousDetectAppCurrentPublicIPs := detectAppCurrentPublicIPsFn
	previousDetectAppPortBindings := detectAppPortBindingsFn
	previousDetectAppDNSCredentialState := detectAppDNSCredentialStateFn
	previousDetectAppServiceEnvFileState := detectAppServiceEnvFileStateFn
	previousDetectAppTailscaleAuthKeyFileState := detectAppTailscaleAuthKeyFileStateFn
	previousStatAppServiceBinary := statAppServiceBinaryFn
	previousEnsureAppNginxCompatibility := ensureAppNginxCompatibilityFn
	previousAppHostFileSystem := newAppHostFileSystemFn
	t.Cleanup(func() {
		detectPermissionStateFn = previousPermissionState
		detectAppDNSFn = previousDetectAppDNS
		detectAppCurrentPublicIPsFn = previousDetectAppCurrentPublicIPs
		detectAppPortBindingsFn = previousDetectAppPortBindings
		detectAppDNSCredentialStateFn = previousDetectAppDNSCredentialState
		detectAppServiceEnvFileStateFn = previousDetectAppServiceEnvFileState
		detectAppTailscaleAuthKeyFileStateFn = previousDetectAppTailscaleAuthKeyFileState
		statAppServiceBinaryFn = previousStatAppServiceBinary
		ensureAppNginxCompatibilityFn = previousEnsureAppNginxCompatibility
		newAppHostFileSystemFn = previousAppHostFileSystem
	})

	detectPermissionStateFn = func() preflight.PermissionState {
		return preflight.PermissionState{User: "root", IsRoot: true}
	}
	detectAppDNSFn = func(cfg appconfig.Config) map[string]preflight.DNSProbe {
		probes := make(map[string]preflight.DNSProbe, len(cfg.App.Domains))
		for _, domain := range cfg.App.Domains {
			probes[domain] = preflight.DNSProbe{Host: domain, ResolvedIPs: []string{"8.8.8.8"}, ExpectedIPv4: "8.8.8.8"}
		}
		return probes
	}
	detectAppPortBindingsFn = func() []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp"},
			{Port: 443, Protocol: "tcp"},
		}
	}
	detectAppDNSCredentialStateFn = func(appconfig.Config) (bool, bool, string) {
		return false, false, ""
	}
	detectAppServiceEnvFileStateFn = func(appconfig.Config) (bool, bool, string) {
		return false, false, ""
	}
	detectAppTailscaleAuthKeyFileStateFn = func(appconfig.Config) (bool, bool, string) {
		return false, false, ""
	}
	statAppServiceBinaryFn = func(string) (os.FileInfo, error) {
		return os.Stat("/bin/true")
	}
	ensureAppNginxCompatibilityFn = func(stdcontext.Context, appconfig.Config, host.Executor) error {
		return nil
	}
	newAppHostFileSystemFn = func(host.Executor, host.PrivilegeStrategy) host.FileSystem {
		return readOnlyAppFileSystem{}
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

	t.Run("tencentcloud env_file is validated with secret file references", func(t *testing.T) {
		dir := t.TempDir()
		secretIDFile := filepath.Join(dir, "tencent-secret-id")
		secretKeyFile := filepath.Join(dir, "tencent-secret-key")
		if err := os.WriteFile(secretIDFile, []byte("secret-id\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.WriteFile(secretKeyFile, []byte("secret-key\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(dir, "tencentcloud.env")
		content := "TENCENTCLOUD_SECRET_ID_FILE=" + secretIDFile + "\nTENCENTCLOUD_SECRET_KEY_FILE=" + secretKeyFile + "\nTENCENTCLOUD_REGION=ap-guangzhou\n"
		if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, secretIDFile, secretKeyFile, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "tencentcloud", EnvFile: envFile})
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

	t.Run("tencentcloud raw secret in env_file is not ready", func(t *testing.T) {
		secretKeyFile := filepath.Join(t.TempDir(), "tencent-secret-key")
		if err := os.WriteFile(secretKeyFile, []byte("secret-key\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		envFile := filepath.Join(t.TempDir(), "tencentcloud.env")
		if err := os.WriteFile(envFile, []byte("TENCENTCLOUD_SECRET_ID=secret-id\nTENCENTCLOUD_SECRET_KEY_FILE="+secretKeyFile+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		withRootOwnedStatForPaths(t, secretKeyFile, envFile)

		checked, ready, detail := detectDNSCredentialState(config.DNS01Config{Provider: "tencentcloud", EnvFile: envFile})
		if !checked {
			t.Fatal("checked = false, want true")
		}
		if ready {
			t.Fatal("ready = true, want false for raw Tencent Cloud secret in systemd env_file")
		}
		if !strings.Contains(detail, "TENCENTCLOUD_SECRET_ID must not be set directly") {
			t.Fatalf("detail = %q, want raw Tencent Cloud secret rejection", detail)
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

func TestDetectAppServiceEnvFileStateRequiresRootOnlyFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "web.env")
	if err := os.Chmod(filepath.Dir(envFile), 0o700); err != nil {
		t.Fatalf("Chmod(temp dir) error = %v", err)
	}
	if err := os.WriteFile(envFile, []byte("WEB_ENV=production\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"
	cfg.Service.EnvFile = envFile

	previousLstat := lstatAppServicePathFn
	t.Cleanup(func() {
		lstatAppServicePathFn = previousLstat
	})
	lstatAppServicePathFn = func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		return rootOwnedFileInfo{FileInfo: info}, nil
	}

	checked, ready, detail := detectAppServiceEnvFileState(cfg)
	if !checked || !ready {
		t.Fatalf("detectAppServiceEnvFileState() = checked %t ready %t detail %q, want ready", checked, ready, detail)
	}

	if err := os.Chmod(envFile, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	checked, ready, detail = detectAppServiceEnvFileState(cfg)
	if !checked || ready || !strings.Contains(detail, "root-only") {
		t.Fatalf("detectAppServiceEnvFileState() = checked %t ready %t detail %q, want root-only failure", checked, ready, detail)
	}

	if err := os.Chmod(envFile, 0o600); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	linkPath := filepath.Join(filepath.Dir(envFile), "web-link.env")
	if err := os.Symlink(envFile, linkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	cfg.Service.EnvFile = linkPath
	checked, ready, detail = detectAppServiceEnvFileState(cfg)
	if !checked || ready || !strings.Contains(detail, "must not be a symlink") {
		t.Fatalf("detectAppServiceEnvFileState() = checked %t ready %t detail %q, want symlink failure", checked, ready, detail)
	}

	writableDir := filepath.Join(filepath.Dir(envFile), "writable")
	if err := os.Mkdir(writableDir, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.Chmod(writableDir, 0o777); err != nil {
		t.Fatalf("Chmod(writableDir) error = %v", err)
	}
	writableEnvFile := filepath.Join(writableDir, "web.env")
	if err := os.WriteFile(writableEnvFile, []byte("WEB_ENV=production\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(writableEnvFile) error = %v", err)
	}
	cfg.Service.EnvFile = writableEnvFile
	checked, ready, detail = detectAppServiceEnvFileState(cfg)
	if !checked || ready || !strings.Contains(detail, "must not be writable by group or others") {
		t.Fatalf("detectAppServiceEnvFileState() = checked %t ready %t detail %q, want writable parent failure", checked, ready, detail)
	}
}

func TestDetectAppDNSCredentialStateRequiresRootOnlyEnvFileAndReferences(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("Chmod(temp dir) error = %v", err)
	}
	tokenFile := filepath.Join(dir, "cloudflare-token")
	if err := os.WriteFile(tokenFile, []byte("token-value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(tokenFile) error = %v", err)
	}
	envFile := filepath.Join(dir, "cloudflare.env")
	if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenFile+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(envFile) error = %v", err)
	}
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.App.ACMEChallenge = appconfig.ACMEChallengeDNS01
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"
	cfg.DNS01.Provider = "cloudflare"
	cfg.DNS01.EnvFile = envFile

	previousLstat := lstatAppServicePathFn
	t.Cleanup(func() {
		lstatAppServicePathFn = previousLstat
	})
	lstatAppServicePathFn = func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		return rootOwnedFileInfo{FileInfo: info}, nil
	}

	checked, ready, detail := detectAppDNSCredentialState(cfg)
	if !checked || !ready {
		t.Fatalf("detectAppDNSCredentialState() = checked %t ready %t detail %q, want ready", checked, ready, detail)
	}

	envLink := filepath.Join(dir, "cloudflare-link.env")
	if err := os.Symlink(envFile, envLink); err != nil {
		t.Fatalf("Symlink(envFile) error = %v", err)
	}
	cfg.DNS01.EnvFile = envLink
	checked, ready, detail = detectAppDNSCredentialState(cfg)
	if !checked || ready || !strings.Contains(detail, "dns01.env_file must not be a symlink") {
		t.Fatalf("detectAppDNSCredentialState() = checked %t ready %t detail %q, want env_file symlink failure", checked, ready, detail)
	}

	cfg.DNS01.EnvFile = envFile
	tokenLink := filepath.Join(dir, "cloudflare-token-link")
	if err := os.Symlink(tokenFile, tokenLink); err != nil {
		t.Fatalf("Symlink(tokenFile) error = %v", err)
	}
	if err := os.WriteFile(envFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenLink+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(envFile token symlink) error = %v", err)
	}
	checked, ready, detail = detectAppDNSCredentialState(cfg)
	if !checked || ready || !strings.Contains(detail, "CLOUDFLARE_DNS_API_TOKEN_FILE") || !strings.Contains(detail, "must not be a symlink") {
		t.Fatalf("detectAppDNSCredentialState() = checked %t ready %t detail %q, want referenced credential symlink failure", checked, ready, detail)
	}

	writableDir := filepath.Join(dir, "writable")
	if err := os.Mkdir(writableDir, 0o700); err != nil {
		t.Fatalf("Mkdir(writableDir) error = %v", err)
	}
	if err := os.Chmod(writableDir, 0o777); err != nil {
		t.Fatalf("Chmod(writableDir) error = %v", err)
	}
	writableEnvFile := filepath.Join(writableDir, "cloudflare.env")
	if err := os.WriteFile(writableEnvFile, []byte("CLOUDFLARE_DNS_API_TOKEN_FILE="+tokenFile+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(writableEnvFile) error = %v", err)
	}
	cfg.DNS01.EnvFile = writableEnvFile
	checked, ready, detail = detectAppDNSCredentialState(cfg)
	if !checked || ready || !strings.Contains(detail, "dns01.env_file parent directory") || !strings.Contains(detail, "must not be writable by group or others") {
		t.Fatalf("detectAppDNSCredentialState() = checked %t ready %t detail %q, want writable parent failure", checked, ready, detail)
	}
}

func TestEnsureAppNginxRuntimeCompatibilityChecksHTTP2DirectiveVersion(t *testing.T) {
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"

	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "nginx" && strings.Join(actual.Args, " ") == "-V" {
			return host.Result{Stderr: "nginx version: nginx/1.24.0\nconfigure arguments: --with-http_v2_module --with-http_gzip_static_module"}, nil
		}
		return host.Result{}, nil
	}}
	err := ensureAppNginxRuntimeCompatibility(stdcontext.Background(), cfg, host.NewExecutor(runner, nil))
	if err == nil || !strings.Contains(err.Error(), "require nginx >= 1.25.1") {
		t.Fatalf("ensureAppNginxRuntimeCompatibility() error = %v, want nginx version failure", err)
	}

	disabled := false
	cfg.Nginx.HTTP2 = &disabled
	runner.commands = nil
	if err := ensureAppNginxRuntimeCompatibility(stdcontext.Background(), cfg, host.NewExecutor(runner, nil)); err != nil {
		t.Fatalf("ensureAppNginxRuntimeCompatibility() with http2 disabled error = %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("commands = %#v, want no nginx -v when http2 disabled", runner.commands)
	}

	enabled := true
	cfg.Nginx.HTTP2 = &enabled
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "nginx" && strings.Join(actual.Args, " ") == "-V" {
			return host.Result{Stderr: "nginx version: nginx/1.26.3\nconfigure arguments: --with-http_v2_module --with-http_gzip_static_module"}, nil
		}
		return host.Result{}, nil
	}
	if err := ensureAppNginxRuntimeCompatibility(stdcontext.Background(), cfg, host.NewExecutor(runner, nil)); err != nil {
		t.Fatalf("ensureAppNginxRuntimeCompatibility() with nginx 1.26.3 error = %v", err)
	}
}

func TestEnsureAppNginxRuntimeCompatibilityChecksGzipStaticModule(t *testing.T) {
	disabled := false
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"
	cfg.Nginx.HTTP2 = &disabled
	cfg.Nginx.StaticLocations = []appconfig.NginxStaticLocationConfig{{
		Path:       "/static/",
		Alias:      "/opt/review-app/static/",
		GzipStatic: true,
	}}

	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "nginx" && strings.Join(actual.Args, " ") == "-V" {
			return host.Result{Stderr: "nginx version: nginx/1.26.3\nconfigure arguments: --with-http_v2_module"}, nil
		}
		return host.Result{}, nil
	}}
	err := ensureAppNginxRuntimeCompatibility(stdcontext.Background(), cfg, host.NewExecutor(runner, nil))
	if err == nil || !strings.Contains(err.Error(), "--with-http_gzip_static_module") {
		t.Fatalf("ensureAppNginxRuntimeCompatibility() error = %v, want gzip_static module failure", err)
	}
}

func TestEnsureAppHostDependenciesFailsWhenLegoArchitectureCannotBeDetected(t *testing.T) {
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		switch actual.Name {
		case "/opt/meshify/bin/lego":
			if strings.Join(actual.Args, " ") == "--version" {
				return host.Result{Stderr: "lego: command not found"}, errors.New("lego: command not found")
			}
		case "dpkg":
			return host.Result{ExitCode: 2, Stderr: "dpkg failed"}, errors.New("dpkg failed")
		}
		return host.Result{}, nil
	}}

	err := ensureAppHostDependencies(stdcontext.Background(), host.NewExecutor(runner, nil))
	if err == nil || !strings.Contains(err.Error(), "detect package architecture with dpkg --print-architecture") {
		t.Fatalf("ensureAppHostDependencies() error = %v, want explicit dpkg architecture failure", err)
	}
}

func TestActivateAppNginxDoesNotFallbackAfterSystemctlReloadFailure(t *testing.T) {
	cfg := appconfig.New()
	cfg.App.Name = "review-app"
	cfg.App.Domains = []string{"app.example.com"}
	cfg.App.CertificateEmail = "ops@example.com"
	cfg.App.Listen = "127.0.0.1:18001"
	cfg.Service.ExecStart = "/bin/true --listen 127.0.0.1:18001"
	names, err := appsvc.NewNames(cfg)
	if err != nil {
		t.Fatalf("NewNames() error = %v", err)
	}
	runner := &scriptedHostRunner{run: func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "systemctl" && strings.Join(actual.Args, " ") == "reload-or-restart nginx.service" {
			return host.Result{ExitCode: 1, Stderr: "reload failed"}, errors.New("reload failed")
		}
		return host.Result{}, nil
	}}

	err = activateAppNginx(stdcontext.Background(), host.NewExecutor(runner, nil), names)
	if err == nil || !strings.Contains(err.Error(), "reload failed") {
		t.Fatalf("activateAppNginx() error = %v, want systemctl reload failure", err)
	}
	for _, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "nginx" && strings.Join(actual.Args, " ") == "-s reload" {
			t.Fatalf("commands = %#v, must not fallback to nginx -s reload", runner.commands)
		}
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

func TestDetectPackageSourceStateUsesConfiguredPackageProbeTimeouts(t *testing.T) {
	cfg := config.ExampleConfig()
	cfg.Advanced.PackageProbe.ReachabilityTimeout = "45s"
	cfg.Advanced.PackageProbe.ArtifactTimeout = "7m"

	previousProbePackageURL := probePackageURLFn
	previousHashRemoteArtifact := hashRemoteArtifactFn
	previousLookupOfficialPackageDigest := lookupOfficialPackageDigestFn
	t.Cleanup(func() {
		probePackageURLFn = previousProbePackageURL
		hashRemoteArtifactFn = previousHashRemoteArtifact
		lookupOfficialPackageDigestFn = previousLookupOfficialPackageDigest
	})

	var probeTimeouts []time.Duration
	var artifactTimeouts []time.Duration
	probePackageURLFn = func(client *http.Client, rawURL string) (bool, bool, string) {
		probeTimeouts = append(probeTimeouts, client.Timeout)
		return true, true, rawURL + " returned 200."
	}
	hashRemoteArtifactFn = func(client *http.Client, rawURL string) (string, error) {
		artifactTimeouts = append(artifactTimeouts, client.Timeout)
		if sha, ok := testLegoArchiveHash(t, rawURL); ok {
			return sha, nil
		}
		return strings.Repeat("a", 64), nil
	}
	lookupOfficialPackageDigestFn = func(client *http.Client, version string, arch string) (string, error) {
		artifactTimeouts = append(artifactTimeouts, client.Timeout)
		return strings.Repeat("a", 64), nil
	}

	state := detectPackageSourceState(cfg)
	if !state.Reachable || !state.LegoReachable || !state.IntegrityChecked || !state.LegoIntegrityChecked {
		t.Fatalf("package source state = %#v, want remote package and lego archive verified", state)
	}
	if len(probeTimeouts) == 0 {
		t.Fatal("probeTimeouts is empty, want package URL probes")
	}
	for _, got := range probeTimeouts {
		if got != 45*time.Second {
			t.Fatalf("probe timeout = %v, want 45s; all probe timeouts = %v", got, probeTimeouts)
		}
	}
	if len(artifactTimeouts) == 0 {
		t.Fatal("artifactTimeouts is empty, want artifact checksum probes")
	}
	for _, got := range artifactTimeouts {
		if got != 7*time.Minute {
			t.Fatalf("artifact timeout = %v, want 7m; all artifact timeouts = %v", got, artifactTimeouts)
		}
	}
}

func TestDetectPackageSourceStateUsesOfflineLegoArchiveWithoutRemoteProbe(t *testing.T) {
	cfg := config.ExampleConfig()
	archivePath := filepath.Join(t.TempDir(), "lego_v5.0.4_linux_amd64.tar.gz")
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
		if !strings.Contains(display, "/opt/meshify/bin/lego run --path /var/lib/meshify/lego") || !strings.Contains(display, "--dns cloudflare") || !strings.Contains(display, "--deploy-hook") {
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
	detectPortBindingsFn = func(config.Config) []preflight.PortBinding {
		return []preflight.PortBinding{
			{Port: 80, Protocol: "tcp", InUse: true, Process: "nginx"},
			{Port: 443, Protocol: "tcp", InUse: true, Process: "nginx"},
			{Port: 8080, Protocol: "tcp", InUse: true, Process: "headscale"},
			{Port: config.DefaultHeadscaleMetricsPort, Protocol: "tcp", InUse: true, Process: "headscale"},
			{Port: 50443, Protocol: "tcp", InUse: true, Process: "headscale"},
			{Port: 3478, Protocol: "udp", InUse: true, Process: "headscale"},
		}
	}
	detectServiceStatesFn = func() []preflight.ServiceState {
		return []preflight.ServiceState{
			{Name: "headscale", Active: true, Detail: "running"},
			{Name: "nginx", Active: true, Detail: "running"},
		}
	}

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
	migrationIndex := -1
	issueIndex := -1
	for index, command := range runner.commands {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "sh" && len(actual.Args) >= 3 && actual.Args[2] == "meshify-lego-v5-migration-gate" {
			migrationIndex = index
		}
		if actual.Name == legocomponent.BinaryPath && strings.Join(actual.Args, " ") != "--version" {
			issueIndex = index
		}
	}
	if runner.commands[0].Name != "mkdir" || migrationIndex < 0 || issueIndex < 0 || migrationIndex >= issueIndex {
		t.Fatalf("commands = %#v, want HTTP-01 bootstrap and lego migration before lego issuance", runner.commands)
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

func TestDeployManagedServiceStateRequiresMatchingDesiredState(t *testing.T) {
	matching := deployManagedServiceState(state.Checkpoint{
		DesiredStateDigest: "digest-a",
		CompletedCheckpoints: []string{
			deployCheckpointHostDependenciesInstalled,
			deployCheckpointHeadscalePackageInstalled,
		},
	}, "digest-a")
	if !matching.Nginx || !matching.Headscale {
		t.Fatalf("matching managed state = %#v, want Nginx and Headscale managed", matching)
	}

	stale := deployManagedServiceState(state.Checkpoint{
		DesiredStateDigest: "digest-a",
		CompletedCheckpoints: []string{
			deployCheckpointHostDependenciesInstalled,
			deployCheckpointHeadscalePackageInstalled,
		},
	}, "digest-b")
	if stale.Nginx || stale.Headscale {
		t.Fatalf("stale managed state = %#v, want no managed services", stale)
	}
}

func TestHTTP01ChallengeRouteCommandTargetsLocalNginxWithHostHeader(t *testing.T) {
	command := http01ChallengeRouteCommand("hs.example.com", "/var/lib/meshify/acme-challenges")

	if command.Name != "sh" {
		t.Fatalf("command.Name = %q, want sh", command.Name)
	}
	args := strings.Join(command.Args, " ")
	for _, want := range []string{
		"meshify-http01-route-check",
		"/var/lib/meshify/acme-challenges",
		"--noproxy '*'",
		"--resolve \"$server_name:80:127.0.0.1\"",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("command args = %q, want %q", args, want)
		}
	}
	if display := command.String(); !strings.Contains(display, "--resolve hs.example.com:80:127.0.0.1") || !strings.Contains(display, "/.well-known/acme-challenge/<token>") {
		t.Fatalf("display command = %q, want local HTTP-01 probe", display)
	}
}

func TestCertificateIssueRemediationsAreChallengeSpecific(t *testing.T) {
	http01 := strings.Join(certificateIssueRemediations(config.ACMEChallengeHTTP01, "hs.example.com"), "\n")
	if !strings.Contains(http01, "For HTTP-01") || !strings.Contains(http01, "hs.example.com") {
		t.Fatalf("HTTP-01 remediations = %q, want HTTP-01 public-port guidance", http01)
	}
	if strings.Contains(http01, "DNS-01") {
		t.Fatalf("HTTP-01 remediations = %q, do not want DNS-01 guidance", http01)
	}

	dns01 := strings.Join(certificateIssueRemediations(config.ACMEChallengeDNS01, "hs.example.com"), "\n")
	if strings.Contains(dns01, "For HTTP-01") {
		t.Fatalf("DNS-01 remediations = %q, do not want HTTP-01 guidance", dns01)
	}
	if !strings.Contains(dns01, "DNS-01 provider credentials") {
		t.Fatalf("DNS-01 remediations = %q, want generic ACME guidance", dns01)
	}
}

func TestCommandErrorWithOutputIncludesFirstOutputLine(t *testing.T) {
	result := host.Result{
		Command:  host.Command{Name: "/opt/meshify/bin/lego", Args: []string{"run"}},
		ExitCode: 1,
		Stderr:   "could not obtain certificates: connection refused\nverbose details",
	}
	err := commandErrorWithOutput(result, &host.CommandError{Result: result, Err: errors.New("exit status 1")})

	message := err.Error()
	if !strings.Contains(message, "output: could not obtain certificates: connection refused") {
		t.Fatalf("error = %q, want first output line", message)
	}
	if strings.Contains(message, "verbose details") {
		t.Fatalf("error = %q, do not want multiline command output", message)
	}
}

func TestCommandErrorWithOutputPrefersDiagnosticLine(t *testing.T) {
	result := host.Result{
		Command:  host.Command{Name: "/opt/meshify/bin/lego", Args: []string{"run"}},
		ExitCode: 1,
		Stdout: strings.Join([]string{
			"2026-05-21T22:47:59+08:00 INFO Private key saved. filepath=/var/lib/meshify/lego/accounts/acme-v02.api.letsencrypt.org/ops@example.com/ops@example.com.key",
			"2026-05-21T22:48:00+08:00 INFO Could not find the solver. domain=hs.example.com type=tls-alpn-01 solvers=http-01",
			"2026-05-21T22:48:01+08:00 ERR Could not obtain certificates error=\"one or more domains had a problem\"",
		}, "\n"),
	}
	err := commandErrorWithOutput(result, &host.CommandError{Result: result, Err: errors.New("exit status 1")})

	message := err.Error()
	if !strings.Contains(message, "ERR Could not obtain certificates") {
		t.Fatalf("error = %q, want diagnostic lego output line", message)
	}
	if strings.Contains(message, "Private key saved") {
		t.Fatalf("error = %q, do not want earlier non-diagnostic lego output", message)
	}
	if strings.Contains(message, "Could not find the solver") {
		t.Fatalf("error = %q, do not want normal lego solver-selection info", message)
	}
}

func TestExecute_DeployCertificateFailureIncludesLegoOutput(t *testing.T) {
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
		CurrentCheckpoint:  deployCheckpointLegoCommandReady,
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
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	previousCheckpointPath := checkpointPathForConfigFn
	previousStore := checkpointStoreForConfigFn
	previousExecutor := newHostExecutorFn
	runner := &scriptedHostRunner{}
	t.Cleanup(func() {
		checkpointPathForConfigFn = previousCheckpointPath
		checkpointStoreForConfigFn = previousStore
		newHostExecutorFn = previousExecutor
	})
	checkpointPathForConfigFn = func(string) string {
		return checkpointPath
	}
	checkpointStoreForConfigFn = func(string) state.Store {
		return state.NewStore(checkpointPath)
	}
	runner.run = func(command host.Command) (host.Result, error) {
		actual := unwrapMaybeSudoHostCommand(command)
		if actual.Name == "sh" && len(actual.Args) >= 3 {
			switch actual.Args[2] {
			case "meshify-lego-v5-migration-gate", "meshify-http01-route-check":
				return host.Result{Command: command}, nil
			}
		}
		if actual.Name == legocomponent.BinaryPath {
			result := host.Result{
				Command:  command,
				ExitCode: 1,
				Stderr:   "could not obtain certificates: acme: error: connection refused\nfull lego trace",
			}
			return result, &host.CommandError{Result: result, Err: errors.New("exit status 1")}
		}
		t.Fatalf("unexpected host command %q", command.String())
		return host.Result{}, nil
	}
	newHostExecutorFn = func(env map[string]string) host.Executor {
		return host.NewExecutor(runner, env)
	}

	stdout, stderr, err := runCLI(t, "deploy", "--config", configPath)
	if err == nil {
		t.Fatal("Execute() error = nil, want certificate failure")
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "output: could not obtain certificates: acme: error: connection refused") {
		t.Fatalf("stdout = %q, want lego output detail", stdout)
	}
	if strings.Contains(stdout, "full lego trace") {
		t.Fatalf("stdout = %q, do not want multiline lego trace", stdout)
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
