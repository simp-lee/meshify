package host

import (
	"context"
	"errors"
	"io/fs"
	"meshify/internal/assets"
	"meshify/internal/render"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFileInstallerWritesFilesCreatesDirectoriesAndReportsActivations(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	installer := NewFileInstaller(nil, rootDir)
	staged := render.StagedFile{
		SourcePath:  "templates/etc/headscale/config.yaml.tmpl",
		HostPath:    "/etc/headscale/config.yaml",
		Mode:        0o600,
		Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		Content:     []byte("server_url: https://hs.example.com\n"),
	}

	result, err := installer.InstallOne(staged)
	if err != nil {
		t.Fatalf("InstallOne() error = %v", err)
	}
	if !result.Changed || !result.Created || !result.ContentChanged || !result.ModeChanged {
		t.Fatalf("result = %#v, want created changed file report", result)
	}
	if len(result.Activations) != 1 || result.Activations[0] != assets.ActivationRestartHeadscale {
		t.Fatalf("result.Activations = %v, want [%q]", result.Activations, assets.ActivationRestartHeadscale)
	}

	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != string(staged.Content) {
		t.Fatalf("content = %q, want %q", string(content), string(staged.Content))
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want %v", got, 0o600)
	}

	second, err := installer.InstallOne(staged)
	if err != nil {
		t.Fatalf("second InstallOne() error = %v", err)
	}
	if second.Changed {
		t.Fatalf("second result = %#v, want unchanged", second)
	}
	if len(second.Activations) != 0 {
		t.Fatalf("second result activations = %v, want none", second.Activations)
	}
}

func TestFileInstallerReportsModeOnlyChanges(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	installer := NewFileInstaller(nil, rootDir)
	staged := render.StagedFile{
		SourcePath: "templates/etc/nginx/sites-available/headscale.conf.tmpl",
		HostPath:   "/etc/nginx/sites-available/headscale.conf",
		Mode:       0o644,
		Content:    []byte("server_name hs.example.com;\n"),
	}

	if _, err := installer.InstallOne(staged); err != nil {
		t.Fatalf("initial InstallOne() error = %v", err)
	}

	staged.Mode = 0o600
	result, err := installer.InstallOne(staged)
	if err != nil {
		t.Fatalf("mode update InstallOne() error = %v", err)
	}
	if !result.Changed || result.ContentChanged || !result.ModeChanged {
		t.Fatalf("result = %#v, want mode-only change", result)
	}
}

func TestFileInstallerReplacesExistingFileWithRestrictiveModeBeforePostWriteChmod(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(target, []byte("old: true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	installer := NewFileInstaller(chmodFailFileSystem{chmodErr: errors.New("post-write chmod failed")}, rootDir)
	staged := render.StagedFile{
		SourcePath: "templates/etc/headscale/config.yaml.tmpl",
		HostPath:   "/etc/headscale/config.yaml",
		Mode:       0o600,
		Content:    []byte("server_url: https://hs.example.com\n"),
	}

	_, err := installer.InstallOne(staged)
	if err == nil {
		t.Fatal("InstallOne() error = nil, want post-write chmod failure")
	}

	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(content) != string(staged.Content) {
		t.Fatalf("content = %q, want %q", string(content), string(staged.Content))
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("Stat() error = %v", statErr)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after failed post-write chmod = %v, want %v", got, 0o600)
	}
}

func TestCommandFileSystemWriteFileUsesRestrictiveAtomicReplace(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	fileSystem := NewCommandFileSystem(NewExecutor(runner, nil).WithPrivilege(PrivilegeSudo))
	content := []byte("server_url: https://hs.example.com\n")

	if err := fileSystem.WriteFile("/etc/headscale/config.yaml", content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(runner.commands))
	}
	wrapped := runner.commands[0]
	if string(wrapped.Stdin) != string(content) {
		t.Fatalf("command.Stdin = %q, want staged content", string(wrapped.Stdin))
	}

	actual := unwrapSudoCommandForHostTest(t, wrapped)
	if actual.Name != "sh" {
		t.Fatalf("command.Name = %q, want sh", actual.Name)
	}
	if slices.Contains(actual.Args, "tee") {
		t.Fatalf("command.Args = %v, must not use tee write-then-chmod path", actual.Args)
	}
	if !slices.Contains(actual.Args, "/etc/headscale/config.yaml") {
		t.Fatalf("command.Args = %v, want target path argument", actual.Args)
	}
	if !slices.Contains(actual.Args, "600") {
		t.Fatalf("command.Args = %v, want final mode argument", actual.Args)
	}
	if len(actual.Args) < 2 || !strings.Contains(actual.Args[1], "mktemp") || !strings.Contains(actual.Args[1], "chmod 600") || !strings.Contains(actual.Args[1], "mv -f") {
		t.Fatalf("command.Args = %v, want temp-file chmod before atomic rename", actual.Args)
	}
}

func TestCollectModifiedPathsAndActivationsUseChangedFilesOnly(t *testing.T) {
	t.Parallel()

	results := []FileInstallResult{
		{HostPath: "/etc/headscale/config.yaml", Changed: true, Activations: []assets.Activation{assets.ActivationRestartHeadscale}},
		{HostPath: "/etc/headscale/policy.hujson", Changed: true, Activations: []assets.Activation{assets.ActivationRestartHeadscale}},
		{HostPath: "/etc/nginx/sites-available/headscale.conf", Changed: false, Activations: []assets.Activation{assets.ActivationReloadNginx}},
	}

	paths := CollectModifiedPaths(results)
	if len(paths) != 2 {
		t.Fatalf("len(paths) = %d, want 2", len(paths))
	}
	activations := CollectActivations(results)
	if len(activations) != 1 {
		t.Fatalf("len(activations) = %d, want 1", len(activations))
	}
	if activations[0] != assets.ActivationRestartHeadscale {
		t.Fatalf("activations = %v, want [%q]", activations, assets.ActivationRestartHeadscale)
	}
}

func TestFileInstallerInstallPreservesFailingResultForCheckpointTracking(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	installer := NewFileInstaller(chmodFailFileSystem{chmodErr: errors.New("chmod failed after write")}, rootDir)
	staged := render.StagedFile{
		SourcePath:  "templates/etc/headscale/config.yaml.tmpl",
		HostPath:    "/etc/headscale/config.yaml",
		Mode:        0o600,
		Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		Content:     []byte("server_url: https://hs.example.com\n"),
	}

	results, err := installer.Install([]render.StagedFile{staged})
	if err == nil {
		t.Fatal("Install() error = nil, want non-nil")
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if got := results[0]; got.HostPath != staged.HostPath || !got.Changed || !got.Created || !got.ContentChanged || !got.ModeChanged {
		t.Fatalf("results[0] = %#v, want failing changed result preserved", got)
	}

	paths := CollectModifiedPaths(results)
	if len(paths) != 1 || paths[0] != staged.HostPath {
		t.Fatalf("CollectModifiedPaths() = %v, want [%q]", paths, staged.HostPath)
	}
	activations := CollectActivations(results)
	if len(activations) != 1 || activations[0] != assets.ActivationRestartHeadscale {
		t.Fatalf("CollectActivations() = %v, want [%q]", activations, assets.ActivationRestartHeadscale)
	}

	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("ReadFile() error = %v", readErr)
	}
	if string(content) != string(staged.Content) {
		t.Fatalf("content = %q, want %q", string(content), string(staged.Content))
	}
}

func TestFileInstallerInstallDoesNotReportModifiedPathWhenWriteFails(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	installer := NewFileInstaller(writeFailFileSystem{writeErr: errors.New("permission denied")}, rootDir)
	staged := render.StagedFile{
		SourcePath:  "templates/etc/headscale/config.yaml.tmpl",
		HostPath:    "/etc/headscale/config.yaml",
		Mode:        0o600,
		Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		Content:     []byte("server_url: https://hs.example.com\n"),
	}

	results, err := installer.Install([]render.StagedFile{staged})
	if err == nil {
		t.Fatal("Install() error = nil, want non-nil")
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if got := results[0]; got.HostPath != staged.HostPath || got.Changed || !got.Created || !got.ContentChanged || !got.ModeChanged {
		t.Fatalf("results[0] = %#v, want planned change without recorded host mutation", got)
	}

	paths := CollectModifiedPaths(results)
	if len(paths) != 0 {
		t.Fatalf("CollectModifiedPaths() = %v, want none", paths)
	}
	activations := CollectActivations(results)
	if len(activations) != 0 {
		t.Fatalf("CollectActivations() = %v, want none", activations)
	}

	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("os.Stat() error = %v, want %v", statErr, os.ErrNotExist)
	}
}

type chmodFailFileSystem struct {
	OSFileSystem
	chmodErr error
}

func (fileSystem chmodFailFileSystem) Chmod(name string, mode fs.FileMode) error {
	return fileSystem.chmodErr
}

type writeFailFileSystem struct {
	OSFileSystem
	writeErr error
}

func (fileSystem writeFailFileSystem) WriteFile(name string, data []byte, perm fs.FileMode) error {
	return fileSystem.writeErr
}

type recordingRunner struct {
	commands []Command
}

func (runner *recordingRunner) Run(_ context.Context, command Command) (Result, error) {
	runner.commands = append(runner.commands, command)
	return Result{Command: command}, nil
}

func unwrapSudoCommandForHostTest(t *testing.T, command Command) Command {
	t.Helper()

	if command.Name != "sudo" {
		t.Fatalf("command.Name = %q, want sudo-wrapped command", command.Name)
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
		t.Fatalf("command.Args = %v, want wrapped command", command.Args)
	}

	return Command{Name: args[0], Args: append([]string(nil), args[1:]...)}
}
