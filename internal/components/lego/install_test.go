package lego

import (
	"context"
	"errors"
	"strings"
	"testing"

	"meshify/internal/config"
	"meshify/internal/host"
)

func TestNewArchivePlanDirectPinsOfficialReleaseArtifact(t *testing.T) {
	t.Parallel()

	plan, err := NewArchivePlan(validConfig(), InstallPlanOptions{})
	if err != nil {
		t.Fatalf("NewArchivePlan() error = %v", err)
	}

	if plan.Mode != config.PackageSourceModeDirect {
		t.Fatalf("Mode = %q, want %q", plan.Mode, config.PackageSourceModeDirect)
	}
	if plan.Version != Version {
		t.Fatalf("Version = %q, want %q", plan.Version, Version)
	}
	if plan.Arch != config.ArchAMD64 {
		t.Fatalf("Arch = %q, want %q", plan.Arch, config.ArchAMD64)
	}
	if plan.AssetName != "lego_v4.35.2_linux_amd64.tar.gz" {
		t.Fatalf("AssetName = %q", plan.AssetName)
	}
	if plan.SourceURL != "https://github.com/go-acme/lego/releases/download/v4.35.2/lego_v4.35.2_linux_amd64.tar.gz" {
		t.Fatalf("SourceURL = %q", plan.SourceURL)
	}
	if plan.ChecksumsURL != "https://github.com/go-acme/lego/releases/download/v4.35.2/lego_4.35.2_checksums.txt" {
		t.Fatalf("ChecksumsURL = %q", plan.ChecksumsURL)
	}
	if plan.ExpectedSHA256 != sha256LinuxAMD64 {
		t.Fatalf("ExpectedSHA256 = %q", plan.ExpectedSHA256)
	}
	if plan.InstallPath() != "/var/cache/meshify/lego_v4.35.2_linux_amd64.tar.gz" {
		t.Fatalf("InstallPath() = %q", plan.InstallPath())
	}
	if plan.BinaryPath != BinaryPath {
		t.Fatalf("BinaryPath = %q, want %q", plan.BinaryPath, BinaryPath)
	}
}

func TestNewArchivePlanPinsARM64SHA256(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.Platform.Arch = config.ArchARM64
	plan, err := NewArchivePlan(cfg, InstallPlanOptions{})
	if err != nil {
		t.Fatalf("NewArchivePlan() error = %v", err)
	}

	if plan.AssetName != "lego_v4.35.2_linux_arm64.tar.gz" {
		t.Fatalf("AssetName = %q", plan.AssetName)
	}
	if plan.ExpectedSHA256 != sha256LinuxARM64 {
		t.Fatalf("ExpectedSHA256 = %q", plan.ExpectedSHA256)
	}
}

func TestNewArchivePlanRejectsLatestAndUnknownArchitecture(t *testing.T) {
	t.Parallel()

	if _, err := NewArchivePlan(validConfig(), InstallPlanOptions{Version: "latest"}); err == nil || !strings.Contains(err.Error(), "latest is not allowed") {
		t.Fatalf("NewArchivePlan(latest) error = %v, want latest rejection", err)
	}

	if _, err := ArchiveSHA256("riscv64"); err == nil || !strings.Contains(err.Error(), "unsupported lego archive architecture") {
		t.Fatalf("ArchiveSHA256() error = %v, want architecture rejection", err)
	}
}

func TestNewInstallPlanBuildsVerifiedInstallCommands(t *testing.T) {
	t.Parallel()

	plan, err := NewInstallPlan(validConfig(), InstallPlanOptions{CacheDir: "/tmp/meshify-cache"})
	if err != nil {
		t.Fatalf("NewInstallPlan() error = %v", err)
	}
	if len(plan.Commands) != 7 {
		t.Fatalf("len(Commands) = %d, want 7", len(plan.Commands))
	}
	if plan.Commands[0].Name != "mkdir" || !strings.Contains(strings.Join(plan.Commands[0].Args, " "), "/tmp/meshify-cache") {
		t.Fatalf("Commands[0] = %#v, want cache mkdir", plan.Commands[0])
	}
	if plan.Commands[1].Name != "curl" || !strings.Contains(strings.Join(plan.Commands[1].Args, " "), plan.Archive.SourceURL) {
		t.Fatalf("Commands[1] = %#v, want curl download", plan.Commands[1])
	}
	if plan.Commands[2].Name != "sha256sum" || !strings.Contains(string(plan.Commands[2].Stdin), sha256LinuxAMD64+"  /tmp/meshify-cache/lego_v4.35.2_linux_amd64.tar.gz") {
		t.Fatalf("Commands[2] = %#v, want sha256 check before install", plan.Commands[2])
	}
	if got := plan.Commands[3].String(); got != "mkdir -p -m 0755 -- /opt/meshify/bin" {
		t.Fatalf("Commands[3] = %q", got)
	}
	if got := plan.Commands[4].String(); got != "tar -xzf /tmp/meshify-cache/lego_v4.35.2_linux_amd64.tar.gz -C /opt/meshify/bin lego" {
		t.Fatalf("Commands[4] = %q", got)
	}
	if got := plan.Commands[5].String(); got != "chmod 0755 /opt/meshify/bin/lego" {
		t.Fatalf("Commands[5] = %q", got)
	}
	if got := plan.Commands[6].String(); got != "/opt/meshify/bin/lego --version" {
		t.Fatalf("Commands[6] = %q", got)
	}
}

func TestNewInstallPlanSupportsOfflineArchiveWithPinnedDigest(t *testing.T) {
	t.Parallel()

	plan, err := NewInstallPlan(validConfig(), InstallPlanOptions{OfflineSourcePath: "/srv/packages/lego_v4.35.2_linux_amd64.tar.gz"})
	if err != nil {
		t.Fatalf("NewInstallPlan() error = %v", err)
	}
	if plan.Archive.Mode != config.PackageSourceModeOffline {
		t.Fatalf("Mode = %q, want offline", plan.Archive.Mode)
	}
	if plan.Archive.SourceURL != "" {
		t.Fatalf("SourceURL = %q, want empty for offline", plan.Archive.SourceURL)
	}
	if plan.Archive.InstallPath() != "/srv/packages/lego_v4.35.2_linux_amd64.tar.gz" {
		t.Fatalf("InstallPath() = %q", plan.Archive.InstallPath())
	}
	if plan.Commands[0].Name != "sha256sum" || !strings.Contains(string(plan.Commands[0].Stdin), plan.Archive.InstallPath()) {
		t.Fatalf("Commands[0] = %#v, want offline sha256 check first", plan.Commands[0])
	}
}

func TestInstallerRunsPlanCommandsInOrder(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	installer := NewInstaller(host.NewExecutor(runner, nil))
	plan := InstallPlan{Commands: []host.Command{
		{Name: "sha256sum"},
		{Name: "tar"},
	}}

	results, err := installer.Install(context.Background(), plan)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(results) != 2 || len(runner.commands) != 2 {
		t.Fatalf("results = %d commands = %d, want 2", len(results), len(runner.commands))
	}
	if runner.commands[0].Name != "sha256sum" || runner.commands[1].Name != "tar" {
		t.Fatalf("commands = %#v, want plan order", runner.commands)
	}
}

func TestInstallerStopsOnFirstCommandFailure(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{errAt: 1}
	installer := NewInstaller(host.NewExecutor(runner, nil))
	plan := InstallPlan{Commands: []host.Command{
		{Name: "sha256sum"},
		{Name: "tar"},
	}}

	results, err := installer.Install(context.Background(), plan)
	if err == nil {
		t.Fatal("Install() error = nil, want failure")
	}
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want failing result included", len(results))
	}
}

type recordingRunner struct {
	commands []host.Command
	errAt    int
}

func (runner *recordingRunner) Run(_ context.Context, command host.Command) (host.Result, error) {
	runner.commands = append(runner.commands, command)
	result := host.Result{Command: command}
	if runner.errAt > 0 && len(runner.commands) == runner.errAt+1 {
		return result, errors.New("command failed")
	}
	return result, nil
}

func validConfig() config.Config {
	cfg := config.New()
	cfg.Default.ServerURL = "https://hs.example.com"
	cfg.Default.BaseDomain = "tailnet.example.com"
	cfg.Default.CertificateEmail = "ops@example.com"
	return cfg
}
