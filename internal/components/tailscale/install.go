package tailscale

import (
	"context"
	"errors"
	"fmt"
	"lanpanel/internal/host"
	"lanpanel/internal/preflight"
	"os"
	"strings"
)

var ErrAuthKeyRequired = errors.New("tailscale auth key is required when client is not already logged in")

type AuthKeyFunc func(context.Context) (string, error)

type Client struct {
	executor host.Executor
	platform preflight.PlatformInfo
}

type EnsurePlan struct {
	Required    bool
	LoginServer string
	Hostname    string
	AuthKey     string
	AuthKeyFunc AuthKeyFunc
}

type EnsureResult struct {
	SkippedReason string
	CommandsRun   []string
}

func NewClient(executor host.Executor, platform preflight.PlatformInfo) Client {
	return Client{executor: executor, platform: platform}
}

func (client Client) Ensure(ctx context.Context, plan EnsurePlan) (EnsureResult, error) {
	if !plan.Required {
		return EnsureResult{SkippedReason: "app does not require tailnet access"}, nil
	}
	loginServer := strings.TrimSpace(plan.LoginServer)
	if loginServer == "" {
		return EnsureResult{}, fmt.Errorf("tailscale login server is required")
	}
	result := EnsureResult{}
	record := func(command host.Command) {
		result.CommandsRun = append(result.CommandsRun, command.String())
	}

	if _, err := client.executor.Run(ctx, host.Command{Name: "tailscale", Args: []string{"version"}}); err != nil {
		repo, planErr := NewRepositoryPlan(client.platform)
		if planErr != nil {
			return result, planErr
		}
		for _, command := range repo.Commands {
			record(command)
			if _, runErr := client.executor.Run(ctx, command); runErr != nil {
				return result, runErr
			}
		}
	}

	enableCommand := host.Command{Name: "systemctl", Args: []string{"enable", "--now", "tailscaled.service"}}
	record(enableCommand)
	if _, err := client.executor.Run(ctx, enableCommand); err != nil {
		return result, err
	}

	statusCommand := host.Command{Name: "tailscale", Args: []string{"status", "--json"}}
	statusResult, statusErr := client.executor.Run(ctx, statusCommand)
	if statusErr == nil {
		status, err := ParseStatusJSON([]byte(statusResult.Stdout))
		if err != nil {
			return result, err
		}
		if status.LoggedIn {
			if status.BackendState != "Running" || !status.Online {
				return result, fmt.Errorf("tailscale is logged in but not ready (state=%s online=%t); refusing to continue until the client is running and online", status.BackendState, status.Online)
			}
			prefsResult, prefsErr := client.executor.Run(ctx, host.Command{Name: "tailscale", Args: []string{"debug", "prefs"}})
			if prefsErr != nil {
				return result, fmt.Errorf("check tailscale prefs: %w", prefsErr)
			}
			prefs, err := ParsePrefsJSON([]byte(prefsResult.Stdout))
			if err != nil {
				return result, err
			}
			if prefs.ControlURL == "" {
				return result, fmt.Errorf("tailscale is already logged in, but Lanpanel cannot prove its login server; refusing to reset or rejoin automatically")
			}
			if prefs.ControlURL != normalizeControlURL(loginServer) {
				return result, fmt.Errorf("tailscale is already logged in to %s, not %s; refusing to reset or rejoin automatically", prefs.ControlURL, loginServer)
			}
			if !prefs.MatchesLanpanelPolicy() {
				return result, fmt.Errorf("tailscale is already logged in but live policy does not match Lanpanel app policy (%s); refusing to change policy, reset, or rejoin automatically", prefs.PolicySummary())
			}
			markerResult, markerErr := client.executor.Run(ctx, host.Command{Name: "cat", Args: []string{MarkerPath}})
			if markerErr != nil {
				if !commandRefersToMissingMarker(markerResult, markerErr) {
					return result, markerErr
				}
				if strings.TrimSpace(plan.Hostname) != "" {
					return result, fmt.Errorf("tailscale is already logged in, but Lanpanel cannot prove requested hostname %q without %s; refusing to reset or rejoin automatically", strings.TrimSpace(plan.Hostname), MarkerPath)
				}
			} else {
				marker, err := ParseMarker([]byte(markerResult.Stdout))
				if err != nil {
					return result, err
				}
				if !marker.Matches(loginServer, plan.Hostname) {
					return result, fmt.Errorf("tailscale is already logged in with a marker that does not match %s; refusing to reset or rejoin automatically", loginServer)
				}
			}
			result.SkippedReason = "tailscale client is already installed, running, and logged in to the expected login server"
			return result, nil
		}
	} else {
		return result, fmt.Errorf("check tailscale status: %w", statusErr)
	}

	authKey, err := plan.resolveAuthKey(ctx)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(authKey) == "" {
		return result, ErrAuthKeyRequired
	}
	upCommand, err := NewUpCommand(LoginPlan{LoginServer: loginServer, Hostname: plan.Hostname, AuthKey: authKey})
	if err != nil {
		return result, err
	}
	record(upCommand)
	if _, err := client.executor.Run(ctx, upCommand); err != nil {
		return result, fmt.Errorf("run tailscale up: %w", err)
	}
	markerCommand, err := MarkerInstallCommand(NewMarker(loginServer, plan.Hostname))
	if err != nil {
		return result, err
	}
	record(markerCommand)
	if _, err := client.executor.Run(ctx, markerCommand); err != nil {
		return result, err
	}
	return result, nil
}

func (plan EnsurePlan) resolveAuthKey(ctx context.Context) (string, error) {
	if authKey := strings.TrimSpace(plan.AuthKey); authKey != "" {
		return authKey, nil
	}
	if plan.AuthKeyFunc == nil {
		return "", ErrAuthKeyRequired
	}
	return plan.AuthKeyFunc(ctx)
}

func commandRefersToMissingMarker(result host.Result, err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	text := strings.ToLower(strings.Join([]string{err.Error(), result.Stderr, result.Stdout}, "\n"))
	return strings.Contains(text, "no such file") || strings.Contains(text, "not found")
}
