# Troubleshooting

Start with the command that failed. `meshify deploy`, `meshify verify`, and `meshify status` all report the failed step, impact, remediation, and retry command when recovery is possible.

## Config Contract Problems

- `server_url` must use HTTPS and must be a DNS name, not an IP address.
- `base_domain` must not equal the `server_url` host and must not be its parent suffix.
- `certificate_email` must be a plain email address.
- Keep DNS-01 credentials out of `meshify.yaml` and public templates. Only the provider selector belongs in config.
- Stay on the supported server matrix: Debian 13 or Ubuntu 24.04 LTS.

## Preflight Blocks

- DNS: the public Headscale host must resolve to the target server before deploy.
- Ports: `80/tcp`, `443/tcp`, and `3478/udp` must be available locally and allowed by the cloud firewall or security group.
- Services: existing Nginx can coexist by `server_name`, but conflicting Headscale or reverse-proxy ownership must be understood before deploy.
- China mainland deployments: verify ICP/hosting access requirements, cloud ingress, package reachability, proxy settings, and whether HTTP-01 or DNS-01 is the realistic certificate path.

## Package Delivery Problems

- Direct mode downloads the pinned official Headscale v0.28.0 `.deb` and verifies SHA-256 evidence before install.
- Mirror mode requires a reachable URL and explicit SHA-256 digest.
- Offline mode requires a local file path and explicit SHA-256 digest.
- If the host needs a proxy, set standard `http_proxy`, `https_proxy`, and `no_proxy` values in the advanced config.

## DNS, Ingress, And TLS Problems

- HTTP-01 requires public port 80 to reach the Nginx webroot path `/.well-known/acme-challenge/`.
- DNS-01 requires the selected provider plugin and provider credentials in the host environment.
- Nginx must serve the full certificate chain from `fullchain.pem`, not only the leaf certificate.
- If clients disconnect or DERP fails behind the reverse proxy, confirm the Nginx site still forwards HTTP/1.1 Upgrade and Connection headers.
- `certbot renew --dry-run` should pass during release validation, and the deploy hook should validate and reload Nginx.

## Host Binding And Exposure Problems

- Headscale should listen on `127.0.0.1:8080`.
- Metrics and gRPC listeners should remain loopback-only.
- Remote gRPC should not be exposed as the default management path.
- `3478/udp` must stay available for STUN when embedded DERP is enabled.
- The DERP baseline is self-hosted only. `derp.urls` should remain empty.

## Service And Checkpoint Problems

- Use `meshify status --config meshify.yaml` to see the current checkpoint and last failure after an interrupted deploy.
- If a package, certbot, Nginx, systemd, or onboarding step fails, fix the named issue and rerun the same `meshify deploy --config meshify.yaml` command.
- For Headscale start failures, inspect `systemctl status headscale.service --no-pager --full` and the rendered `/etc/headscale/config.yaml`.
- For Nginx failures, run `nginx -t` and check `/etc/nginx/sites-available/headscale.conf`.

## Client Onboarding Problems

- Re-check the preauth key and `server_url` in [onboarding.md](onboarding.md).
- Confirm the client is running Tailscale >= v1.74.0.
- If MagicDNS does not resolve, confirm the client accepted managed DNS and the configured `base_domain` is correct.
- If a peer path stays on DERP, that can be acceptable when UDP direct connectivity is blocked. The real failure is losing peer connectivity entirely.
- The embedded DERP endpoint does not provide `/generate_204`; validate actual login, `tailscale status`, `tailscale ping`, and `tailscale netcheck` before treating captive-portal probing as the root cause.

## What To Capture

- The full `meshify deploy`, `meshify verify`, or `meshify status` output.
- The edited values from the `default` section of `meshify.yaml`.
- Whether the package source mode is direct, mirror, or offline.
- Which client guide you followed: [Windows](clients/windows.md), [macOS](clients/macos.md), or [Debian/Ubuntu Linux](clients/debian-ubuntu-linux.md).
- Whether the failure affects control-plane login, MagicDNS resolution, direct path selection, or DERP fallback.
