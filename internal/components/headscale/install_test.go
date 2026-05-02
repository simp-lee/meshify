package headscale

import (
	"context"
	"errors"
	"meshify/internal/config"
	"meshify/internal/host"
	"strings"
	"testing"
)

func TestNewPackagePlanDirectUsesOfficialReleaseArtifact(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	plan, err := NewPackagePlan(cfg, InstallPlanOptions{OfficialPackageSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatalf("NewPackagePlan() error = %v", err)
	}

	if plan.Mode != config.PackageSourceModeDirect {
		t.Fatalf("Mode = %q, want %q", plan.Mode, config.PackageSourceModeDirect)
	}
	if plan.Version != config.DefaultHeadscaleVersion {
		t.Fatalf("Version = %q, want %q", plan.Version, config.DefaultHeadscaleVersion)
	}
	if plan.Arch != config.ArchAMD64 {
		t.Fatalf("Arch = %q, want %q", plan.Arch, config.ArchAMD64)
	}
	if plan.AssetName != "headscale_0.28.0_linux_amd64.deb" {
		t.Fatalf("AssetName = %q", plan.AssetName)
	}
	if plan.SourceURL != "https://github.com/juanfont/headscale/releases/download/v0.28.0/headscale_0.28.0_linux_amd64.deb" {
		t.Fatalf("SourceURL = %q", plan.SourceURL)
	}
	if plan.ChecksumsURL != "https://github.com/juanfont/headscale/releases/download/v0.28.0/checksums.txt" {
		t.Fatalf("ChecksumsURL = %q", plan.ChecksumsURL)
	}
	if plan.RequiresOfficialDigest {
		t.Fatal("RequiresOfficialDigest = true, want false after digest provided")
	}
	if plan.InstallPath() != "/var/cache/meshify/headscale_0.28.0_linux_amd64.deb" {
		t.Fatalf("InstallPath() = %q", plan.InstallPath())
	}
}

func TestNewInstallPlanBuildsIntegrityCheckedInstallCommands(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	plan, err := NewInstallPlan(cfg, InstallPlanOptions{
		CacheDir:              "/tmp/meshify-packages",
		OfficialPackageSHA256: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("NewInstallPlan() error = %v", err)
	}
	if len(plan.Commands) != 4 {
		t.Fatalf("len(Commands) = %d, want 4", len(plan.Commands))
	}
	if plan.Commands[0].Name != "mkdir" {
		t.Fatalf("Commands[0].Name = %q, want mkdir", plan.Commands[0].Name)
	}
	if plan.Commands[1].Name != "curl" || !strings.Contains(strings.Join(plan.Commands[1].Args, " "), plan.Package.SourceURL) {
		t.Fatalf("Commands[1] = %#v, want curl download command", plan.Commands[1])
	}
	if plan.Commands[2].Name != "sha256sum" || !strings.Contains(string(plan.Commands[2].Stdin), strings.Repeat("b", 64)) {
		t.Fatalf("Commands[2] = %#v, want sha256sum check", plan.Commands[2])
	}
	if got := strings.Join(plan.Commands[3].Args, " "); got != "install -y /tmp/meshify-packages/headscale_0.28.0_linux_amd64.deb" {
		t.Fatalf("apt args = %q", got)
	}
	if got := plan.Commands[3].Env["DEBIAN_FRONTEND"]; got != "noninteractive" {
		t.Fatalf("apt DEBIAN_FRONTEND = %q, want noninteractive", got)
	}
}

func TestNewPackagePlanRejectsInstallWithoutDigest(t *testing.T) {
	t.Parallel()

	_, err := NewPackagePlan(validConfig(), InstallPlanOptions{})
	if err == nil {
		t.Fatal("NewPackagePlan() error = nil, want digest error")
	}
	if !strings.Contains(err.Error(), "SHA-256 digest is required") {
		t.Fatalf("error = %q, want digest failure", err.Error())
	}
}

func TestNewPackagePlanRejectsUnsupportedHeadscaleVersion(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Advanced.PackageSource.Version = "0.29.0"
	_, err := NewPackagePlan(cfg, InstallPlanOptions{OfficialPackageSHA256: strings.Repeat("a", 64)})
	if err == nil {
		t.Fatal("NewPackagePlan() error = nil, want version failure")
	}
	if !strings.Contains(err.Error(), "headscale version must be 0.28.0") {
		t.Fatalf("error = %q, want version guardrail", err.Error())
	}
}

func TestNewPackagePlanSupportsMirrorAndOfflineSources(t *testing.T) {
	t.Parallel()

	mirror := validConfig()
	mirror.Advanced.PackageSource.Mode = config.PackageSourceModeMirror
	mirror.Advanced.PackageSource.URL = "https://mirror.example.com/headscale.deb"
	mirror.Advanced.PackageSource.SHA256 = strings.Repeat("c", 64)

	mirrorPlan, err := NewPackagePlan(mirror, InstallPlanOptions{})
	if err != nil {
		t.Fatalf("NewPackagePlan(mirror) error = %v", err)
	}
	if mirrorPlan.SourceURL != "https://mirror.example.com/headscale.deb" {
		t.Fatalf("mirror SourceURL = %q", mirrorPlan.SourceURL)
	}

	offline := validConfig()
	offline.Advanced.PackageSource.Mode = config.PackageSourceModeOffline
	offline.Advanced.PackageSource.FilePath = "/srv/packages/headscale.deb"
	offline.Advanced.PackageSource.SHA256 = strings.Repeat("d", 64)

	offlinePlan, err := NewPackagePlan(offline, InstallPlanOptions{})
	if err != nil {
		t.Fatalf("NewPackagePlan(offline) error = %v", err)
	}
	if offlinePlan.InstallPath() != "/srv/packages/headscale.deb" {
		t.Fatalf("offline InstallPath() = %q", offlinePlan.InstallPath())
	}
}

func TestInstallerRunsPlanCommandsInOrder(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	installer := NewInstaller(host.NewExecutor(runner, nil))
	plan := InstallPlan{Commands: []host.Command{
		{Name: "sha256sum", Args: []string{"--check", "-"}},
		{Name: "apt-get", Args: []string{"install", "-y", "/tmp/headscale.deb"}},
	}}

	results, err := installer.Install(context.Background(), plan)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(results) != 2 || len(runner.commands) != 2 {
		t.Fatalf("results = %d commands = %d, want 2", len(results), len(runner.commands))
	}
	if runner.commands[0].Name != "sha256sum" || runner.commands[1].Name != "apt-get" {
		t.Fatalf("commands = %#v, want plan order", runner.commands)
	}
}

func TestInstallerStopsOnFirstCommandFailure(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{errAt: 1}
	installer := NewInstaller(host.NewExecutor(runner, nil))
	plan := InstallPlan{Commands: []host.Command{
		{Name: "sha256sum"},
		{Name: "apt-get"},
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
