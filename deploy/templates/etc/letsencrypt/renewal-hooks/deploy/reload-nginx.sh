#!/bin/sh
set -eu

if command -v nginx >/dev/null 2>&1; then
    nginx -t
fi

if command -v systemctl >/dev/null 2>&1; then
    systemctl reload nginx
else
    nginx -s reload
fi