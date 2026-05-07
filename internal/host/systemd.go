package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Systemd struct {
	executor Executor
}

func NewSystemd(executor Executor) Systemd {
	return Systemd{executor: executor}
}

func (systemd Systemd) DaemonReload(ctx context.Context) (Result, error) {
	return systemd.executor.Systemctl(ctx, "daemon-reload")
}

func (systemd Systemd) Enable(ctx context.Context, units ...string) (Result, error) {
	return systemd.run(ctx, "enable", units...)
}

func (systemd Systemd) Start(ctx context.Context, units ...string) (Result, error) {
	return systemd.run(ctx, "start", units...)
}

func (systemd Systemd) Restart(ctx context.Context, units ...string) (Result, error) {
	return systemd.run(ctx, "restart", units...)
}

func (systemd Systemd) Reload(ctx context.Context, units ...string) (Result, error) {
	return systemd.run(ctx, "reload", units...)
}

func (systemd Systemd) Status(ctx context.Context, unit string) (Result, error) {
	if strings.TrimSpace(unit) == "" {
		return Result{}, fmt.Errorf("systemd unit is required")
	}
	return systemd.executor.Systemctl(ctx, "status", "--no-pager", "--full", unit)
}

func (systemd Systemd) IsActive(ctx context.Context, unit string) (bool, error) {
	if strings.TrimSpace(unit) == "" {
		return false, fmt.Errorf("systemd unit is required")
	}

	result, err := systemd.executor.Systemctl(ctx, "is-active", unit)
	state := strings.TrimSpace(result.Stdout)
	if err == nil {
		return true, nil
	}

	var commandErr *CommandError
	if errors.As(err, &commandErr) {
		if state == "" {
			state = strings.TrimSpace(commandErr.Result.Stdout)
		}
		if state == "" {
			state = strings.TrimSpace(result.Stderr)
		}
		if state == "" {
			state = strings.TrimSpace(commandErr.Result.Stderr)
		}
		switch state {
		case "inactive", "failed", "unknown", "activating", "deactivating", "maintenance":
			return false, nil
		}
	}

	return false, err
}

func (systemd Systemd) run(ctx context.Context, action string, units ...string) (Result, error) {
	if len(units) == 0 {
		return Result{}, fmt.Errorf("at least one systemd unit is required")
	}

	args := make([]string, 0, len(units)+1)
	args = append(args, action)
	for _, unit := range units {
		trimmed := strings.TrimSpace(unit)
		if trimmed == "" {
			return Result{}, fmt.Errorf("systemd unit is required")
		}
		args = append(args, trimmed)
	}

	return systemd.executor.Systemctl(ctx, args...)
}
