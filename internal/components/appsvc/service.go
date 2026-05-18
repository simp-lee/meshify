package appsvc

import (
	"meshify/internal/host"
	"strings"
)

func EnsureSystemUserCommands(names Names) []host.Command {
	script := `set -eu
name=$1
home=$2
shell=/usr/sbin/nologin

group_line="$(getent group "$name" || true)"
passwd_line="$(getent passwd "$name" || true)"

if [ -n "$group_line" ] && [ -z "$passwd_line" ]; then
    echo "app group $name already exists without matching app user; refusing to reuse" >&2
    exit 1
fi
if [ -z "$group_line" ] && [ -n "$passwd_line" ]; then
    echo "app user $name already exists without matching app group; refusing to reuse" >&2
    exit 1
fi

if [ -n "$group_line" ] && [ -n "$passwd_line" ]; then
    group_gid="$(printf '%s\n' "$group_line" | awk -F: '{print $3}')"
    user_uid="$(printf '%s\n' "$passwd_line" | awk -F: '{print $3}')"
    user_gid="$(printf '%s\n' "$passwd_line" | awk -F: '{print $4}')"
    user_home="$(printf '%s\n' "$passwd_line" | awk -F: '{print $6}')"
    user_shell="$(printf '%s\n' "$passwd_line" | awk -F: '{print $7}')"

    if [ "$user_gid" != "$group_gid" ]; then
        echo "app user $name primary group does not match app group; refusing to reuse" >&2
        exit 1
    fi
    if [ "$user_home" != "$home" ]; then
        echo "app user $name home is $user_home, expected $home; refusing to reuse" >&2
        exit 1
    fi
    if [ "$user_shell" != "$shell" ]; then
        echo "app user $name shell is $user_shell, expected $shell; refusing to reuse" >&2
        exit 1
    fi
    if [ "$user_uid" -eq 0 ] || [ "$user_uid" -ge 1000 ] || [ "$group_gid" -eq 0 ] || [ "$group_gid" -ge 1000 ]; then
        echo "app user/group $name is not a non-root system identity; refusing to reuse" >&2
        exit 1
    fi
    exit 0
fi

groupadd --system "$name"
useradd --system --gid "$name" --home-dir "$home" --shell "$shell" "$name"`
	return []host.Command{{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-user", names.SystemUser, names.VarLibDir},
		DisplayName: "ensure-app-user",
		DisplayArgs: []string{names.SystemUser},
	}}
}

func GuardSystemUserCommand(names Names) host.Command {
	script := `set -eu
name=$1
home=$2
shell=/usr/sbin/nologin

group_line="$(getent group "$name" || true)"
passwd_line="$(getent passwd "$name" || true)"

if [ -n "$group_line" ] && [ -z "$passwd_line" ]; then
    echo "app group $name already exists without matching app user; refusing to reuse" >&2
    exit 1
fi
if [ -z "$group_line" ] && [ -n "$passwd_line" ]; then
    echo "app user $name already exists without matching app group; refusing to reuse" >&2
    exit 1
fi
if [ -z "$group_line" ] && [ -z "$passwd_line" ]; then
    exit 0
fi

group_gid="$(printf '%s\n' "$group_line" | awk -F: '{print $3}')"
user_uid="$(printf '%s\n' "$passwd_line" | awk -F: '{print $3}')"
user_gid="$(printf '%s\n' "$passwd_line" | awk -F: '{print $4}')"
user_home="$(printf '%s\n' "$passwd_line" | awk -F: '{print $6}')"
user_shell="$(printf '%s\n' "$passwd_line" | awk -F: '{print $7}')"

if [ "$user_gid" != "$group_gid" ]; then
    echo "app user $name primary group does not match app group; refusing to reuse" >&2
    exit 1
fi
if [ "$user_home" != "$home" ]; then
    echo "app user $name home is $user_home, expected $home; refusing to reuse" >&2
    exit 1
fi
if [ "$user_shell" != "$shell" ]; then
    echo "app user $name shell is $user_shell, expected $shell; refusing to reuse" >&2
    exit 1
fi
if [ "$user_uid" -eq 0 ] || [ "$user_uid" -ge 1000 ] || [ "$group_gid" -eq 0 ] || [ "$group_gid" -ge 1000 ]; then
    echo "app user/group $name is not a non-root system identity; refusing to reuse" >&2
    exit 1
fi`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-user-guard", names.SystemUser, names.VarLibDir},
		DisplayName: "guard-app-user",
		DisplayArgs: []string{names.SystemUser},
	}
}

func GuardRootDirectoriesCommand(names Names) host.Command {
	script := `set -eu
app_name=$1
shift
pairs=$*
expected_marker="Meshify-managed: app.name=$app_name"

fail() {
    echo "$1" >&2
    exit 1
}

refuse_writable() {
    target=$1
    label=$2
    if find "$target" -maxdepth 0 \( -perm -020 -o -perm -002 \) -print -quit | grep -q .; then
        fail "$label $target must not be writable by group or others"
    fi
}

require_root_owned() {
    target=$1
    label=$2
    owner=$(stat -c %u "$target")
    if [ "$owner" != "0" ]; then
        fail "$label $target must be owned by root"
    fi
}

while [ "$#" -gt 0 ]; do
    dir=$1
    marker=$2
    shift 2

    if [ -L "$dir" ]; then
        fail "$dir is a symlink; refusing to use it as a Meshify app root"
    fi
    if [ -e "$dir" ] && [ ! -d "$dir" ]; then
        fail "$dir exists and is not a directory; refusing to use it as a Meshify app root"
    fi
    if [ ! -d "$dir" ]; then
        continue
    fi
    require_root_owned "$dir" "app root directory"
    refuse_writable "$dir" "app root directory"
    if [ -L "$marker" ]; then
        fail "$marker is a symlink; refusing to trust app root ownership"
    fi
    if [ ! -e "$marker" ]; then
        fail "$dir exists without $marker; refusing to write into a non-Meshify app root"
    fi
    if [ ! -f "$marker" ]; then
        fail "$marker is not a regular file; refusing to trust app root ownership"
    fi
    require_root_owned "$marker" "app root marker"
    refuse_writable "$marker" "app root marker"
    actual_marker=$(cat "$marker")
    if [ "$actual_marker" != "$expected_marker" ]; then
        fail "$dir is managed by a different Meshify app; refusing to write into it"
    fi
done

set -- $pairs
while [ "$#" -gt 0 ]; do
    dir=$1
    marker=$2
    shift 2

    if [ -d "$dir" ]; then
        continue
    fi
    install -d -m 0755 "$dir"
    tmp=$(mktemp "$dir/.meshify-managed.XXXXXX")
    trap 'rm -f "$tmp"' EXIT INT TERM
    printf '%s\n' "$expected_marker" > "$tmp"
    chmod 0644 "$tmp"
    mv "$tmp" "$marker"
    trap - EXIT INT TERM
done`
	args := []string{
		"-c", script, "meshify-app-root-dirs", names.AppName,
		names.VarLibDir, names.VarLibMarkerPath,
		names.EtcDir, names.EtcMarkerPath,
		names.HookDir, names.HookDirMarkerPath,
	}
	return host.Command{
		Name:        "sh",
		Args:        args,
		DisplayName: "guard-app-root-directories",
		DisplayArgs: []string{names.VarLibDir, names.EtcDir, names.HookDir},
	}
}

func EnsureDirectoryCommands(names Names) []host.Command {
	return []host.Command{
		{Name: "install", Args: []string{"-d", "-m", "0755", "--", names.WebrootPath, names.LegoDataPath}},
		{Name: "install", Args: []string{"-d", "-m", "0755", "--", names.TLSDir}},
	}
}

func GuardServiceAccessCommand(names Names, binaryPath string, workingDirectory string) host.Command {
	script := `set -eu
user=$1
binary=$2
working_dir=$3

if ! command -v runuser >/dev/null 2>&1; then
    echo "runuser is required to verify app service user access" >&2
    exit 1
fi
if ! runuser -u "$user" -- test -x "$binary"; then
    echo "app service user $user cannot execute service binary $binary" >&2
    exit 1
fi
if [ -n "$working_dir" ]; then
    if ! runuser -u "$user" -- test -d "$working_dir"; then
        echo "app service user $user cannot access working_directory $working_dir as a directory" >&2
        exit 1
    fi
    if ! runuser -u "$user" -- test -x "$working_dir"; then
        echo "app service user $user cannot enter working_directory $working_dir" >&2
        exit 1
    fi
fi`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-service-access", names.SystemUser, strings.TrimSpace(binaryPath), strings.TrimSpace(workingDirectory)},
		DisplayName: "guard-app-service-access",
		DisplayArgs: []string{names.SystemUser, strings.TrimSpace(binaryPath)},
	}
}

func RemoveManagedServiceUnitCommand(names Names) host.Command {
	unitPath := "/etc/systemd/system/" + names.ServiceUnit
	script := `set -eu
unit=$1
unit_path=$2
app_name=$3
marker="Meshify-managed: app.name=$app_name"

if [ ! -e "$unit_path" ]; then
    exit 0
fi
if ! grep -Fqx "# $marker" "$unit_path" && ! grep -Fqx "$marker" "$unit_path"; then
    echo "$unit_path exists but is not a Meshify-managed service for app $app_name; refusing to remove it" >&2
    exit 1
fi

systemctl disable --now "$unit"
rm -f -- "$unit_path"
printf '%s\n' "$unit_path"`
	return host.Command{
		Name:        "sh",
		Args:        []string{"-c", script, "meshify-app-remove-stale-service", names.ServiceUnit, unitPath, names.AppName},
		DisplayName: "remove-stale-app-service",
		DisplayArgs: []string{unitPath},
	}
}
