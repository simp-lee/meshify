#!/bin/sh
set -eu

usage() {
  cat >&2 <<'EOF'
Usage:
  sh install.sh vX.Y.Z
  VERSION=vX.Y.Z sh install.sh

Environment:
  MESHIFY_INSTALL_DIR  Install directory, default: /usr/local/bin
  MESHIFY_REPO         GitHub repository, default: simp-lee/meshify
EOF
}

need_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "required command not found: $1" >&2
    exit 1
  fi
}

version="${1:-${VERSION:-}}"
if [ -z "$version" ]; then
  usage
  exit 2
fi
case "$version" in
  -h|--help) usage; exit 0 ;;
esac

case "$version" in
  v*) ;;
  *) version="v$version" ;;
esac

need_command uname
need_command curl
need_command awk
need_command mktemp
need_command sha256sum
need_command install
need_command id

repo="${MESHIFY_REPO:-simp-lee/meshify}"
install_dir="${MESHIFY_INSTALL_DIR:-/usr/local/bin}"
binary_name="meshify"

arch="$(uname -m)"
case "$arch" in
  x86_64) asset="meshify_linux_amd64" ;;
  aarch64|arm64) asset="meshify_linux_arm64" ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

tmpdir="$(mktemp -d "${TMPDIR:-/tmp}/meshify-install.XXXXXX")"
cleanup() {
  rm -rf "$tmpdir"
}
abort() {
  trap - EXIT HUP INT TERM
  cleanup
  exit 1
}
trap cleanup EXIT
trap abort HUP INT TERM

base_url="https://github.com/${repo}/releases/download/${version}"
asset_url="${base_url}/${asset}"
checksums_url="${base_url}/checksums.txt"

echo "Downloading ${asset} from ${repo} ${version}..."
curl -fL --proto '=https' --tlsv1.2 -o "${tmpdir}/${asset}" "$asset_url"
curl -fL --proto '=https' --tlsv1.2 -o "${tmpdir}/checksums.txt" "$checksums_url"

(
  cd "$tmpdir"
  if ! awk -v asset="$asset" '
    $2 == asset || $2 == "*" asset { print; found = 1 }
    END { if (!found) exit 1 }
  ' checksums.txt > checksums.selected; then
    echo "checksums.txt does not contain ${asset}" >&2
    exit 1
  fi

  sha256sum -c checksums.selected
  chmod +x "$asset"
)

target="${install_dir%/}/${binary_name}"
if [ "$(id -u)" -eq 0 ]; then
  install -d -m 0755 "$install_dir"
  install -m 0755 "${tmpdir}/${asset}" "$target"
else
  if install -d -m 0755 "$install_dir" 2>/dev/null && install -m 0755 "${tmpdir}/${asset}" "$target" 2>/dev/null; then
    :
  elif command -v sudo >/dev/null 2>&1; then
    sudo install -d -m 0755 "$install_dir"
    sudo install -m 0755 "${tmpdir}/${asset}" "$target"
  else
    echo "install directory is not writable and sudo is not available: ${install_dir}" >&2
    exit 1
  fi
fi

echo "Installed ${target}"
"$target" --help >/dev/null
echo "meshify is ready."
