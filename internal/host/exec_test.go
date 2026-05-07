package host

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

type captureRunner struct {
	commands []Command
	result   Result
	err      error
}

func (runner *captureRunner) Run(_ context.Context, command Command) (Result, error) {
	runner.commands = append(runner.commands, command)
	result := runner.result
	result.Command = command
	return result, runner.err
}

func TestExecutorAptGetAddsProxyEnvAndNonInteractiveMode(t *testing.T) {
	t.Parallel()

	runner := &captureRunner{}
	executor := NewExecutor(runner, ProxyEnv("http://proxy.internal:8080", "https://proxy.internal:8443", "127.0.0.1,localhost"))

	if _, err := executor.AptGet(context.Background(), "install", "-y", "nginx"); err != nil {
		t.Fatalf("AptGet() error = %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("len(commands) = %d, want 1", len(runner.commands))
	}

	command := runner.commands[0]
	if command.Name != "apt-get" {
		t.Fatalf("command.Name = %q, want %q", command.Name, "apt-get")
	}
	if got := strings.Join(command.Args, " "); got != "install -y nginx" {
		t.Fatalf("command.Args = %q, want %q", got, "install -y nginx")
	}
	for key, want := range map[string]string{
		"DEBIAN_FRONTEND": "noninteractive",
		"http_proxy":      "http://proxy.internal:8080",
		"HTTP_PROXY":      "http://proxy.internal:8080",
		"https_proxy":     "https://proxy.internal:8443",
		"HTTPS_PROXY":     "https://proxy.internal:8443",
		"no_proxy":        "127.0.0.1,localhost",
		"NO_PROXY":        "127.0.0.1,localhost",
	} {
		if got := command.Env[key]; got != want {
			t.Fatalf("command.Env[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestProxyEnvNormalizesHostPortProxyForExternalCommands(t *testing.T) {
	t.Parallel()

	env := ProxyEnv("proxy.internal:8080", "[2001:db8::10]:8443", "127.0.0.1,localhost")
	for key, want := range map[string]string{
		"http_proxy":  "http://proxy.internal:8080",
		"HTTP_PROXY":  "http://proxy.internal:8080",
		"https_proxy": "http://[2001:db8::10]:8443",
		"HTTPS_PROXY": "http://[2001:db8::10]:8443",
		"no_proxy":    "127.0.0.1,localhost",
		"NO_PROXY":    "127.0.0.1,localhost",
	} {
		if got := env[key]; got != want {
			t.Fatalf("ProxyEnv()[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestCommandErrorOmitsShellSpew(t *testing.T) {
	t.Parallel()

	err := &CommandError{
		Result: Result{
			Command:  Command{Name: "apt-get", Args: []string{"install", "headscale"}},
			ExitCode: 100,
			Stderr:   "temporary mirror failure\nvery long shell spew",
		},
		Err: errors.New("exit status 100"),
	}

	message := err.Error()
	if !strings.Contains(message, "apt-get install headscale exited with status 100") {
		t.Fatalf("Error() = %q, want sanitized command summary", message)
	}
	if strings.Contains(message, "temporary mirror failure") {
		t.Fatalf("Error() = %q, do not want raw stderr", message)
	}
}

func TestCommandMissingDoesNotTreatMissingSudoAsWrappedCommand(t *testing.T) {
	t.Parallel()

	result := Result{
		Command: Command{
			Name:        "sudo",
			Args:        []string{"-n", "/opt/meshify/bin/lego", "--version"},
			DisplayName: "/opt/meshify/bin/lego",
			DisplayArgs: []string{"--version"},
		},
	}
	err := &CommandError{Result: result, Err: exec.ErrNotFound}

	if CommandMissing(result, err, "/opt/meshify/bin/lego") {
		t.Fatal("CommandMissing() = true, want false when sudo is the missing executable")
	}
	if !CommandMissing(result, err, "sudo") {
		t.Fatal("CommandMissing() = false, want true for missing sudo")
	}
	if !CommandMissing(result, err) {
		t.Fatal("CommandMissing() = false, want true when no explicit command filter is supplied")
	}
}
