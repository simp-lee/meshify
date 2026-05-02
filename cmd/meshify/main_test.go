package main

import (
	"bytes"
	"meshify/internal/config"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestRun_HelpOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no args"},
		{name: "help command", args: []string{"help"}},
		{name: "short help flag", args: []string{"-h"}},
		{name: "long help flag", args: []string{"--help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr, err := runCLI(t, tt.args...)
			if err != nil {
				t.Fatalf("run() error = %v", err)
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
			} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout = %q, want substring %q", stdout, want)
				}
			}
		})
	}
}

func TestRun_VersionOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "version command", args: []string{"version"}},
		{name: "version flag", args: []string{"--version"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr, err := runCLI(t, tt.args...)
			if err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if stdout != "meshify dev\n" {
				t.Fatalf("stdout = %q, want %q", stdout, "meshify dev\n")
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestRun_InitWritesExampleConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "meshify.yaml")
	stdout, stderr, err := runCLI(t, "init", "--config", configPath)
	if err != nil {
		t.Fatalf("run() error = %v", err)
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

	if _, err := config.LoadFile(configPath); err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
}

func TestRun_StatusMissingConfig(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "missing.yaml")
	stdout, stderr, err := runCLI(t, "status", "--config", configPath)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "meshify status: no config file found") {
		t.Fatalf("stdout = %q, want missing-config summary", stdout)
	}
	if !strings.Contains(stdout, "meshify init --config "+configPath) {
		t.Fatalf("stdout = %q, want init hint", stdout)
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := runCLI(t, "unknown")
	if err == nil {
		t.Fatal("run() error = nil, want non-nil")
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
