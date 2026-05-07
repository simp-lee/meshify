package nginx

import (
	"context"
	"fmt"
	"meshify/internal/host"
	"strings"
)

type Activator struct {
	executor host.Executor
}

func NewActivator(executor host.Executor) Activator {
	return Activator{executor: executor}
}

func EnsureSiteEnabledCommand() host.Command {
	return host.Command{Name: "ln", Args: []string{"-sfn", SiteAvailablePath, SiteEnabledPath}}
}

func DisableDefaultSiteCommand() host.Command {
	return disableDefaultSiteCommand(DefaultSiteEnabledPath, DefaultSiteAvailablePath)
}

func TestConfigCommand() host.Command {
	return host.Command{Name: "nginx", Args: []string{"-t"}}
}

func ReloadCommand() host.Command {
	return host.Command{Name: "systemctl", Args: []string{"reload", "nginx.service"}}
}

func FallbackReloadCommand() host.Command {
	return host.Command{Name: "nginx", Args: []string{"-s", "reload"}}
}

func (activator Activator) EnableTestAndReload(ctx context.Context) ([]host.Result, error) {
	commands := []host.Command{
		DisableDefaultSiteCommand(),
		EnsureSiteEnabledCommand(),
		TestConfigCommand(),
	}
	results := make([]host.Result, 0, len(commands)+2)
	for _, command := range commands {
		result, err := activator.executor.Run(ctx, command)
		results = append(results, result)
		if err != nil {
			return results, err
		}
	}

	result, err := activator.executor.Run(ctx, ReloadCommand())
	results = append(results, result)
	if err == nil {
		return results, nil
	}
	if !reloadFallbackAllowed(result, err) {
		return results, err
	}
	fallbackResult, fallbackErr := activator.executor.Run(ctx, FallbackReloadCommand())
	results = append(results, fallbackResult)
	if fallbackErr != nil {
		return results, fmt.Errorf("systemctl reload nginx.service failed: %w; fallback nginx -s reload failed: %v", err, fallbackErr)
	}
	return results, nil
}

func disableDefaultSiteCommand(enabledPath string, availablePath string) host.Command {
	script := `set -eu
enabled=$1
default_target=$2
if [ ! -e "$enabled" ] && [ ! -L "$enabled" ]; then
    exit 0
fi
if [ ! -L "$enabled" ]; then
    echo "$enabled exists but is not a symlink; remove or migrate it before enabling meshify's default_server site" >&2
    exit 64
fi
target=$(readlink -- "$enabled")
case "$target" in
    "$default_target"|../sites-available/default)
        rm -f -- "$enabled"
        ;;
    *)
        resolved=$(readlink -f -- "$enabled" 2>/dev/null || true)
        resolved_default=$(readlink -f -- "$default_target" 2>/dev/null || true)
        if [ -n "$resolved" ] && [ -n "$resolved_default" ] && [ "$resolved" = "$resolved_default" ]; then
            rm -f -- "$enabled"
        else
            echo "$enabled points to $target; remove or migrate it before enabling meshify's default_server site" >&2
            exit 64
        fi
        ;;
esac`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-disable-nginx-default-site", strings.TrimSpace(enabledPath), strings.TrimSpace(availablePath)},
		DisplayName: "disable-nginx-default-site",
		DisplayArgs: []string{strings.TrimSpace(enabledPath)},
	}
}

func reloadFallbackAllowed(result host.Result, err error) bool {
	if host.CommandMissing(result, err, "systemctl") {
		return true
	}
	text := strings.ToLower(result.Stdout + "\n" + result.Stderr + "\n" + err.Error())
	for _, marker := range []string{
		"system has not been booted with systemd",
		"failed to connect to bus: no such file or directory",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
