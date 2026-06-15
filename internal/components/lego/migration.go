package lego

import (
	"lanpanel/internal/host"
	"strings"
)

const MigrationMarkerName = ".lanpanel-lego-v5-ready"

func MigrationGateCommand(dataPath string) host.Command {
	dataPath = strings.TrimSpace(dataPath)
	script := `set -eu
lego=$1
lego_path=$2
marker="$lego_path/.lanpanel-lego-v5-ready"

if [ -z "$lego_path" ]; then
    echo "lego data path is required for v5 storage migration" >&2
    exit 1
fi

if [ -e "$marker" ]; then
    exit 0
fi

if [ ! -e "$lego_path" ]; then
    install -d -m 0755 "$lego_path"
    touch "$marker"
    exit 0
fi

if [ ! -d "$lego_path" ]; then
    echo "$lego_path exists but is not a directory; refusing lego v5 storage migration" >&2
    exit 1
fi

has_entries=false
for entry in "$lego_path"/* "$lego_path"/.[!.]* "$lego_path"/..?*; do
    [ -e "$entry" ] || continue
    has_entries=true
    break
done

if [ "$has_entries" = false ]; then
    touch "$marker"
    exit 0
fi

if [ ! -d "$lego_path/accounts" ] && [ ! -d "$lego_path/certificates" ]; then
    echo "$lego_path contains files but no lego accounts or certificates directory; refusing lego v5 storage migration" >&2
    exit 1
fi

backup=$(mktemp -d "$lego_path.lanpanel-v4-backup.XXXXXX")
cp -a "$lego_path/." "$backup/"
if ! printf 'y\n' | "$lego" migrate --path "$lego_path"; then
    echo "lego v5 storage migration failed for $lego_path; backup preserved at $backup" >&2
    exit 1
fi
touch "$marker"`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "lanpanel-lego-v5-migration-gate", BinaryPath, dataPath},
		DisplayName: "lanpanel-lego-v5-migration-gate",
		DisplayArgs: []string{dataPath},
	}
}
