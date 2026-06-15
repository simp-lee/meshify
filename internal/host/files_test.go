package host

import (
	"context"
	"errors"
	"io/fs"
	"lanpanel/internal/assets"
	"lanpanel/internal/render"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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
	info, err := os.Stat(filepath.Join(rootDir, "etc", "nginx", "sites-available", "headscale.conf"))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func TestFileInstallerRejectsSymlinkTargetBeforeReadOrWrite(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "safe-target")
	if err := os.WriteFile(target, []byte("server_url: https://hs.example.com\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	link := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	installer := NewFileInstaller(nil, rootDir)
	staged := render.StagedFile{
		SourcePath: "templates/etc/headscale/config.yaml.tmpl",
		HostPath:   "/etc/headscale/config.yaml",
		Mode:       0o644,
		Content:    []byte("server_url: https://hs.example.com\n"),
	}
	if _, err := installer.InstallOne(staged); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("InstallOne() error = %v, want symlink refusal", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat(target) error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("target mode = %v, want unchanged 0600", got)
	}
	if linkInfo, err := os.Lstat(link); err != nil || linkInfo.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Lstat(link) = %#v, %v; want retained symlink", linkInfo, err)
	}
}

func TestFileInstallerRejectsNonRegularTargetsBeforeRead(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		create func(t *testing.T, path string)
	}{
		{
			name: "directory",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("Mkdir(target) error = %v", err)
				}
			},
		},
		{
			name: "fifo",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatalf("Mkfifo(target) error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rootDir := t.TempDir()
			target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			tt.create(t, target)

			installer := NewFileInstaller(nil, rootDir)
			staged := render.StagedFile{
				SourcePath: "templates/etc/headscale/config.yaml.tmpl",
				HostPath:   "/etc/headscale/config.yaml",
				Mode:       0o600,
				Content:    []byte("server_url: https://hs.example.com\n"),
			}
			if _, err := installer.InstallOne(staged); err == nil || !strings.Contains(err.Error(), "not a regular file") {
				t.Fatalf("InstallOne() error = %v, want non-regular refusal", err)
			}
		})
	}
}

func TestFileInstallerReplacesExistingFileWithRequestedModeWithoutPostWriteChmod(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(target, []byte("old: true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	installer := NewFileInstaller(nil, rootDir)
	staged := render.StagedFile{
		SourcePath: "templates/etc/headscale/config.yaml.tmpl",
		HostPath:   "/etc/headscale/config.yaml",
		Mode:       0o600,
		Content:    []byte("server_url: https://hs.example.com\n"),
	}

	_, err := installer.InstallOne(staged)
	if err != nil {
		t.Fatalf("InstallOne() error = %v", err)
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
		t.Fatalf("mode after replace = %v, want %v", got, 0o600)
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

func TestCommandFileSystemStatParsesLocaleIndependentModeBits(t *testing.T) {
	t.Parallel()

	regularRunner := &captureRunner{result: Result{Stdout: "81a4 123 1700000000 0 0\n"}}
	fileSystem := NewCommandFileSystem(NewExecutor(regularRunner, nil))
	regularInfo, err := fileSystem.Stat("/etc/headscale/config.yaml")
	if err != nil {
		t.Fatalf("Stat(regular) error = %v", err)
	}
	if !regularInfo.Mode().IsRegular() || regularInfo.Mode().Perm() != 0o644 {
		t.Fatalf("regular mode = %v, want regular 0644", regularInfo.Mode())
	}
	if len(regularRunner.commands) != 1 || !slices.Contains(regularRunner.commands[0].Args, "-L") {
		t.Fatalf("regular stat command = %#v, want dereference flag", regularRunner.commands)
	}
	if regularInfo.Size() != 123 || regularInfo.ModTime().Unix() != 1700000000 {
		t.Fatalf("regular size/mtime = %d/%d, want 123/1700000000", regularInfo.Size(), regularInfo.ModTime().Unix())
	}
	owner, ok := regularInfo.Sys().(struct {
		UID uint64
		GID uint64
	})
	if !ok || owner.UID != 0 || owner.GID != 0 {
		t.Fatalf("regular owner = %#v, %v; want uid/gid 0", regularInfo.Sys(), ok)
	}

	symlinkRunner := &captureRunner{result: Result{Stdout: "a1ff 7 1700000001 0 0\n"}}
	fileSystem = NewCommandFileSystem(NewExecutor(symlinkRunner, nil))
	symlinkInfo, err := fileSystem.Lstat("/etc/headscale/config.yaml")
	if err != nil {
		t.Fatalf("Lstat(symlink) error = %v", err)
	}
	if symlinkInfo.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("symlink mode = %v, want symlink", symlinkInfo.Mode())
	}
	if len(symlinkRunner.commands) != 1 || slices.Contains(symlinkRunner.commands[0].Args, "-L") {
		t.Fatalf("symlink stat command = %#v, want no dereference flag", symlinkRunner.commands)
	}
	if symlinkInfo.Size() != 7 || symlinkInfo.ModTime().Unix() != 1700000001 {
		t.Fatalf("symlink size/mtime = %d/%d, want 7/1700000001", symlinkInfo.Size(), symlinkInfo.ModTime().Unix())
	}

	directoryRunner := &captureRunner{result: Result{Stdout: "41ed 4096 1700000002 0 0\n"}}
	fileSystem = NewCommandFileSystem(NewExecutor(directoryRunner, nil))
	directoryInfo, err := fileSystem.Lstat("/etc/headscale")
	if err != nil {
		t.Fatalf("Lstat(directory) error = %v", err)
	}
	if !directoryInfo.IsDir() || directoryInfo.Mode().Perm() != 0o755 {
		t.Fatalf("directory mode = %v IsDir = %v, want directory 0755", directoryInfo.Mode(), directoryInfo.IsDir())
	}
	if directoryInfo.Size() != 4096 || directoryInfo.ModTime().Unix() != 1700000002 {
		t.Fatalf("directory size/mtime = %d/%d, want 4096/1700000002", directoryInfo.Size(), directoryInfo.ModTime().Unix())
	}

	specialRunner := &captureRunner{result: Result{Stdout: "8fed 0 1700000003 0 0\n"}}
	fileSystem = NewCommandFileSystem(NewExecutor(specialRunner, nil))
	specialInfo, err := fileSystem.Stat("/usr/local/bin/app")
	if err != nil {
		t.Fatalf("Stat(special) error = %v", err)
	}
	wantSpecial := fs.FileMode(0o755) | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if got := specialInfo.Mode(); got != wantSpecial {
		t.Fatalf("special mode = %v, want %v", got, wantSpecial)
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

func TestFileInstallerInstallPreservesModeOnlyWriteFailureResultForCheckpointTracking(t *testing.T) {
	t.Parallel()

	rootDir := t.TempDir()
	target := filepath.Join(rootDir, "etc", "headscale", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := []byte("server_url: https://hs.example.com\n")
	if err := os.WriteFile(target, content, 0o644); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	installer := NewFileInstaller(writeFailFileSystem{writeErr: errors.New("mode-only write failed")}, rootDir)
	staged := render.StagedFile{
		SourcePath:  "templates/etc/headscale/config.yaml.tmpl",
		HostPath:    "/etc/headscale/config.yaml",
		Mode:        0o600,
		Activations: []assets.Activation{assets.ActivationRestartHeadscale},
		Content:     content,
	}

	results, err := installer.Install([]render.StagedFile{staged})
	if err == nil {
		t.Fatal("Install() error = nil, want non-nil")
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if got := results[0]; got.HostPath != staged.HostPath || got.Changed || got.Created || got.ContentChanged || !got.ModeChanged {
		t.Fatalf("results[0] = %#v, want failing mode-only result without recorded mutation", got)
	}

	paths := CollectModifiedPaths(results)
	if len(paths) != 0 {
		t.Fatalf("CollectModifiedPaths() = %v, want none", paths)
	}
	activations := CollectActivations(results)
	if len(activations) != 0 {
		t.Fatalf("CollectActivations() = %v, want none", activations)
	}

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
