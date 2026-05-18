package tailscale

import (
	"encoding/json"
	"fmt"
	"meshify/internal/host"
	"strings"
)

type LoginPlan struct {
	LoginServer string
	Hostname    string
	AuthKey     string
}

func NewUpCommand(plan LoginPlan) (host.Command, error) {
	loginServer := strings.TrimSpace(plan.LoginServer)
	authKey := strings.TrimSpace(plan.AuthKey)
	if loginServer == "" {
		return host.Command{}, fmt.Errorf("tailscale login server is required")
	}
	if authKey == "" {
		return host.Command{}, fmt.Errorf("tailscale auth key is required")
	}
	args := []string{
		"up",
		"--login-server", loginServer,
		"--auth-key", authKey,
		"--accept-dns=false",
		"--accept-routes=false",
		"--shields-up",
	}
	if hostname := strings.TrimSpace(plan.Hostname); hostname != "" {
		args = append(args, "--hostname", hostname)
	}
	displayArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		displayArgs = append(displayArgs, args[i])
		if args[i] == "--auth-key" && i+1 < len(args) {
			i++
			displayArgs = append(displayArgs, Mask(args[i]))
		}
	}
	return host.Command{Name: "tailscale", Args: args, DisplayName: "tailscale", DisplayArgs: displayArgs}, nil
}

func MarkerInstallCommand(marker Marker) (host.Command, error) {
	data, err := json.Marshal(marker)
	if err != nil {
		return host.Command{}, fmt.Errorf("marshal tailscale marker: %w", err)
	}
	script := `set -eu
install -d -m 0700 /var/lib/meshify
tmp=$(mktemp /var/lib/meshify/tailscale-client.json.XXXXXX)
trap 'rm -f "$tmp"' EXIT INT TERM
cat > "$tmp"
chmod 0600 "$tmp"
mv "$tmp" /var/lib/meshify/tailscale-client.json`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-tailscale-marker"},
		Stdin:       data,
		DisplayName: "install-tailscale-marker",
		DisplayArgs: []string{MarkerPath},
	}, nil
}
