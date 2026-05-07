#!/bin/sh
set -eu

: "${LEGO_CERT_DOMAIN:?}"
: "${LEGO_CERT_PATH:?}"
: "${LEGO_CERT_KEY_PATH:?}"

target_dir="/etc/meshify/tls/$LEGO_CERT_DOMAIN"
install -d -m 0755 "$target_dir"
install -m 0644 "$LEGO_CERT_PATH" "$target_dir/fullchain.pem"
install -m 0600 "$LEGO_CERT_KEY_PATH" "$target_dir/privkey.pem"

nginx -t

reload_error="$(mktemp)"
trap 'rm -f "$reload_error"' EXIT INT TERM
if command -v systemctl >/dev/null 2>&1; then
    if systemctl reload nginx >"$reload_error" 2>&1; then
        exit 0
    else
        systemctl_status=$?
    fi
    if ! grep -qi -e "system has not been booted with systemd" -e "failed to connect to bus: no such file or directory" "$reload_error"; then
        cat "$reload_error" >&2
        exit "$systemctl_status"
    fi
fi
nginx -s reload
