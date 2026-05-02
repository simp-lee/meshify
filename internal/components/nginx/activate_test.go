package nginx

import (
	"context"
	"errors"
	"meshify/internal/host"
	"os/exec"
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
	if len(results) != 3 || len(runner.commands) != 3 {
		t.Fatalf("results = %d commands = %d, want 3", len(results), len(runner.commands))
	}
	if runner.commands[0].Name != "ln" || !strings.Contains(strings.Join(runner.commands[0].Args, " "), SiteEnabledPath) {
		t.Fatalf("first command = %#v, want symlink", runner.commands[0])
	}
	if runner.commands[1].Name != "nginx" || strings.Join(runner.commands[1].Args, " ") != "-t" {
		t.Fatalf("second command = %#v, want nginx -t", runner.commands[1])
	}
	if runner.commands[2].Name != "systemctl" || strings.Join(runner.commands[2].Args, " ") != "reload nginx.service" {
		t.Fatalf("third command = %#v, want systemctl reload nginx.service", runner.commands[2])
	}
}

func TestActivatorStopsOnConfigTestFailure(t *testing.T) {
	t.Parallel()

	runner := &recordingRunner{failAt: 1}
	activator := NewActivator(host.NewExecutor(runner, nil))
	results, err := activator.EnableTestAndReload(context.Background())
	if err == nil {
		t.Fatal("EnableTestAndReload() error = nil, want failure")
	}
	if len(results) != 2 {
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
	if len(results) != 3 || len(runner.commands) != 3 {
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
	if len(results) != 4 || len(runner.commands) != 4 {
		t.Fatalf("results = %d commands = %d, want fallback command", len(results), len(runner.commands))
	}
	if got := runner.commands[3]; got.Name != "nginx" || strings.Join(got.Args, " ") != "-s reload" {
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
	if len(results) != 3 || len(runner.commands) != 3 {
		t.Fatalf("results = %d commands = %d, want no fallback after permission-denied bus failure", len(results), len(runner.commands))
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
