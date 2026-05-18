package tailscale

import (
	"fmt"
	"meshify/internal/host"
	"meshify/internal/preflight"
	"strings"
)

type RepositoryPlan struct {
	Distribution string
	Codename     string
	Commands     []host.Command
}

var supportedCodenames = map[string]map[string]struct{}{
	"debian": {
		"bullseye": {}, "bookworm": {}, "trixie": {}, "forky": {}, "sid": {},
	},
	"ubuntu": {
		"xenial": {}, "bionic": {}, "focal": {}, "jammy": {}, "noble": {}, "oracular": {}, "plucky": {}, "questing": {}, "resolute": {},
	},
}

func NewRepositoryPlan(platform preflight.PlatformInfo) (RepositoryPlan, error) {
	distribution := normalizedDistribution(platform)
	codename := normalizeCodename(platform.VersionID)
	if distribution == "" {
		return RepositoryPlan{}, fmt.Errorf("tailscale apt repository supports Debian and Ubuntu hosts only")
	}
	if _, ok := supportedCodenames[distribution][codename]; !ok {
		return RepositoryPlan{}, fmt.Errorf("unsupported Tailscale apt repository target %s %q", distribution, platform.VersionID)
	}

	baseURL := fmt.Sprintf("https://pkgs.tailscale.com/stable/%s/%s", distribution, codename)
	keyURL := baseURL + ".noarmor.gpg"
	listURL := baseURL + ".tailscale-keyring.list"
	return RepositoryPlan{
		Distribution: distribution,
		Codename:     codename,
		Commands: []host.Command{
			{Name: "apt-get", Args: []string{"update"}, Env: map[string]string{"DEBIAN_FRONTEND": "noninteractive"}},
			{Name: "apt-get", Args: []string{"install", "-y", "ca-certificates", "curl"}, Env: map[string]string{"DEBIAN_FRONTEND": "noninteractive"}},
			repositoryFileCommand("install-tailscale-keyring", keyURL, "/usr/share/keyrings/tailscale-archive-keyring.gpg"),
			repositoryFileCommand("install-tailscale-apt-source", listURL, "/etc/apt/sources.list.d/tailscale.list"),
			{Name: "apt-get", Args: []string{"update"}, Env: map[string]string{"DEBIAN_FRONTEND": "noninteractive"}},
			{Name: "apt-get", Args: []string{"install", "-y", "tailscale"}, Env: map[string]string{"DEBIAN_FRONTEND": "noninteractive"}},
		},
	}, nil
}

func repositoryFileCommand(displayName string, url string, target string) host.Command {
	script := `set -eu
url=$1
target=$2
marker="$target.meshify-managed"
dir=${target%/*}
expected_marker="Meshify-managed: tailscale repo file"
install -d -m 0755 "$dir"
tmp=$(mktemp "$dir/.meshify-tailscale.XXXXXX")
marker_tmp=$(mktemp "$dir/.meshify-tailscale-marker.XXXXXX")
trap 'rm -f "$tmp" "$marker_tmp"' EXIT INT TERM
curl -fsSL "$url" -o "$tmp"

if [ -L "$target" ] || [ -L "$marker" ]; then
    echo "$target or $marker is a symlink; refusing to overwrite package source files" >&2
    exit 1
fi
if [ -e "$target" ]; then
    if [ -e "$marker" ]; then
        first_line=$(sed -n '1p' "$marker")
        if [ "$first_line" != "$expected_marker" ]; then
            echo "$marker exists but is not a Meshify marker; refusing to overwrite $target" >&2
            exit 1
        fi
    elif cmp -s "$target" "$tmp"; then
        exit 0
    else
        echo "$target exists and is not Meshify-managed; refusing to overwrite it" >&2
        exit 1
    fi
fi

install -m 0644 "$tmp" "$target"
printf '%s\nurl=%s\n' "$expected_marker" "$url" > "$marker_tmp"
install -m 0644 "$marker_tmp" "$marker"`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-tailscale-repo-file", url, target},
		DisplayName: displayName,
		DisplayArgs: []string{url},
	}
}

func normalizedDistribution(platform preflight.PlatformInfo) string {
	id := strings.ToLower(strings.TrimSpace(platform.ID))
	switch id {
	case "debian", "ubuntu":
		return id
	}
	for _, token := range strings.Fields(strings.ToLower(platform.IDLike)) {
		switch token {
		case "debian", "ubuntu":
			return token
		}
	}
	return ""
}

func normalizeCodename(versionID string) string {
	value := strings.ToLower(strings.TrimSpace(versionID))
	switch value {
	case "10":
		return "buster"
	case "11":
		return "bullseye"
	case "12":
		return "bookworm"
	case "13":
		return "trixie"
	case "16.04":
		return "xenial"
	case "18.04":
		return "bionic"
	case "20.04":
		return "focal"
	case "22.04":
		return "jammy"
	case "24.04":
		return "noble"
	case "24.10":
		return "oracular"
	case "25.04":
		return "plucky"
	case "25.10":
		return "questing"
	case "26.04":
		return "resolute"
	default:
		return value
	}
}
