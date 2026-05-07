package nginx

import (
	"context"
	"errors"
	"meshify/internal/host"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivatorEnableTestAndReloadRunsExpectedCommands(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err != nil {
		t.Fatalf("EnableTestAndReload() error = %v", err)
	}
	if len(results) != 4 || len(runner.commands) != 4 {
		t.Fatalf("results = %d commands = %d, want 4", len(results), len(runner.commands))
	}
	if runner.commands[0].DisplayName != "disable-nginx-default-site" || !strings.Contains(strings.Join(runner.commands[0].DisplayArgs, " "), DefaultSiteEnabledPath) {
		t.Fatalf("first command = %#v, want default site disable", runner.commands[0])
	}
	if runner.commands[1].Name != "ln" || !strings.Contains(strings.Join(runner.commands[1].Args, " "), SiteEnabledPath) {
		t.Fatalf("second command = %#v, want symlink", runner.commands[1])
	}
	if runner.commands[2].Name != "nginx" || strings.Join(runner.commands[2].Args, " ") != "-t" {
		t.Fatalf("third command = %#v, want nginx -t", runner.commands[2])
	}
	if runner.commands[3].Name != "systemctl" || strings.Join(runner.commands[3].Args, " ") != "reload nginx.service" {
		t.Fatalf("fourth command = %#v, want systemctl reload nginx.service", runner.commands[3])
	}
}

func TestActivatorStopsOnConfigTestFailure(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{failAt: 2}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err == nil {
		t.Fatal("EnableTestAndReload() error = nil, want failure")
	}
	if len(results) != 3 {
		t.Fatalf("len(results) = %d, want failing command included", len(results))
	}
}

func TestActivatorDoesNotFallbackForSystemctlReloadFailure(t *testing.T) {
	t.Parallel()

	runner := &reloadRunner{systemctlErr: errors.New("reload failed")}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err == nil {
		t.Fatal("EnableTestAndReload() error = nil, want reload failure")
	}
	if len(results) != 4 || len(runner.commands) != 4 {
		t.Fatalf("results = %d commands = %d, want no fallback after systemctl service failure", len(results), len(runner.commands))
	}
}

func TestActivatorFallbacksWhenSystemctlIsMissing(t *testing.T) {
	t.Parallel()

	runner := &reloadRunner{systemctlErr: exec.ErrNotFound}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err != nil {
		t.Fatalf("EnableTestAndReload() error = %v", err)
	}
	if len(results) != 5 || len(runner.commands) != 5 {
		t.Fatalf("results = %d commands = %d, want fallback command", len(results), len(runner.commands))
	}
	if got := runner.commands[4]; got.Name != "nginx" || strings.Join(got.Args, " ") != "-s reload" {
		t.Fatalf("fallback command = %#v, want nginx -s reload", got)
	}
}

func TestActivatorDoesNotFallbackForPermissionDeniedBusError(t *testing.T) {
	t.Parallel()

	runner := &reloadRunner{systemctlErr: errors.New("Failed to connect to bus: Permission denied")}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err == nil {
		t.Fatal("EnableTestAndReload() error = nil, want permission-denied bus failure")
	}
	if len(results) != 4 || len(runner.commands) != 4 {
		t.Fatalf("results = %d commands = %d, want no fallback after permission-denied bus failure", len(results), len(runner.commands))
	}
}

func TestDisableDefaultSiteCommandRemovesDistributionSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	available := filepath.Join(root, "etc", "nginx", "sites-available", "default")
	enabled := filepath.Join(root, "etc", "nginx", "sites-enabled", "default")
	if err := os.MkdirAll(filepath.Dir(available), 0o755); err != nil {
		t.Fatalf("MkdirAll(available) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(enabled), 0o755); err != nil {
		t.Fatalf("MkdirAll(enabled) error = %v", err)
	}
	if err := os.WriteFile(available, []byte("server { listen 80 default_server; }\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink(available, enabled); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	command := disableDefaultSiteCommand(enabled, available)
	if _, err := host.NewExecutor(host.OSRunner{}, nil).Run(context.Background(), command); err != nil {
		t.Fatalf("disable default site command error = %v", err)
	}
	if _, err := os.Lstat(enabled); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(enabled) error = %v, want %v", err, os.ErrNotExist)
	}
}

func TestDisableDefaultSiteCommandRejectsCustomSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	available := filepath.Join(root, "etc", "nginx", "sites-available", "default")
	custom := filepath.Join(root, "srv", "custom-site.conf")
	enabled := filepath.Join(root, "etc", "nginx", "sites-enabled", "default")
	if err := os.MkdirAll(filepath.Dir(available), 0o755); err != nil {
		t.Fatalf("MkdirAll(available) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(custom), 0o755); err != nil {
		t.Fatalf("MkdirAll(custom) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(enabled), 0o755); err != nil {
		t.Fatalf("MkdirAll(enabled) error = %v", err)
	}
	if err := os.WriteFile(available, []byte("default\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(default) error = %v", err)
	}
	if err := os.WriteFile(custom, []byte("custom\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(custom) error = %v", err)
	}
	if err := os.Symlink(custom, enabled); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	command := disableDefaultSiteCommand(enabled, available)
	_, err := host.NewExecutor(host.OSRunner{}, nil).Run(context.Background(), command)
	if err == nil {
		t.Fatal("disable default site command error = nil, want custom symlink rejection")
	}
	if _, statErr := os.Lstat(enabled); statErr != nil {
		t.Fatalf("Lstat(enabled) error = %v, want custom symlink preserved", statErr)
	}
}

type recordingRunner struct {
	commands []host.Command
	failAt   int
}

func (runner *recordingRunner) Run(_ context.Context, command host.Command) (host.Result, error) {
	runner.commands = append(runner.commands, command)
	result := host.Result{Command: command}
	if runner.failAt > 0 && len(runner.commands) == runner.failAt+1 {
		return result, errors.New("command failed")
	}
	return result, nil
}

type reloadRunner struct {
	commands     []host.Command
	systemctlErr error
}

func (runner *reloadRunner) Run(_ context.Context, command host.Command) (host.Result, error) {
	runner.commands = append(runner.commands, command)
	result := host.Result{Command: command}
	if command.Name == "systemctl" && runner.systemctlErr != nil {
		return result, &host.CommandError{Result: result, Err: runner.systemctlErr}
	}
	return result, nil
}
