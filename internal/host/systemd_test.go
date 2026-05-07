package host

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSystemdRestartUsesSystemctl(t *testing.T) {
	t.Parallel()

	runner := &captureRunner{}
	manager := NewSystemd(NewExecutor(runner, nil))

	if _, err := manager.Restart(context.Background(), "headscale.service"); err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(runner.commands))
	}
	if command := runner.commands[0]; command.Name != "systemctl" {
		t.Fatalf("command.Name = %q, want %q", command.Name, "systemctl")
	} else if got := strings.Join(command.Args, " "); got != "restart headscale.service" {
		t.Fatalf("command.Args = %q, want %q", got, "restart headscale.service")
	}
}

func TestSystemdIsActiveTreatsInactiveAsFalse(t *testing.T) {
	t.Parallel()

	runner := &captureRunner{}
	runner.err = &CommandError{
		Result: Result{
			Stdout:   "inactive\n",
			ExitCode: 3,
		},
		Err: errors.New("exit status 3"),
	}
	manager := NewSystemd(NewExecutor(runner, nil))

	active, err := manager.IsActive(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("IsActive() error = %v", err)
	}
	if active {
		t.Fatal("IsActive() = true, want false")
	}
}

func TestSystemdIsActiveTreatsKnownNonActiveStatesAsFalse(t *testing.T) {
	t.Parallel()

	for _, state := range []string{"activating", "deactivating", "maintenance"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			runner := &captureRunner{}
			runner.err = &CommandError{
				Result: Result{
					Stdout:   state + "\n",
					ExitCode: 3,
				},
				Err: errors.New("exit status 3"),
			}
			manager := NewSystemd(NewExecutor(runner, nil))

			active, err := manager.IsActive(context.Background(), "nginx.service")
			if err != nil {
				t.Fatalf("IsActive() error = %v", err)
			}
			if active {
				t.Fatal("IsActive() = true, want false")
			}
		})
	}
}

func TestSystemdIsActiveTrustsSuccessfulExitStatus(t *testing.T) {
	t.Parallel()

	runner := &captureRunner{result: Result{Stdout: "reloading\n"}}
	manager := NewSystemd(NewExecutor(runner, nil))

	active, err := manager.IsActive(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("IsActive() error = %v", err)
	}
	if !active {
		t.Fatal("IsActive() = false, want true")
	}
}
