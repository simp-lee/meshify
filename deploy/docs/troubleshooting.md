# Troubleshooting

Start with the command that failed. `meshify deploy`, `meshify verify`, and
`meshify status` report the failed step, impact, remediation, and retry command
when recovery is possible.

## Config Contract Problems

- `server_url` must use HTTPS and must be a DNS name, not an IP address.
- `server_url` may omit the port or use port 443; other explicit ports are
  rejected.
- `base_domain` must not equal the `server_url` host and must not be its parent
  suffix.
- `certificate_email` must be a plain email address.
- `acme_challenge` must be `http-01` or `dns-01`.
- Keep DNS-01 credential contents out of `meshify.yaml` and public templates.
  Cloudflare and DigitalOcean require a root-only lego `env_file`. Route53 and
  gcloud may use lego's ambient credential chain when deploy and renewal use the
  same host identity. For gcloud ambient mode, the project must also be
  available to lego through Google Cloud metadata; use an env file with
  `GCE_PROJECT` when it is not. The same root-only env file may also carry
  non-secret provider settings such as `AWS_HOSTED_ZONE_ID`, `AWS_PROFILE`, or
  `GCE_ZONE_ID`.
- When explicit provider variables are needed, use a root-only lego `env_file`.
  Because systemd environment files are not a secret store, put raw DNS tokens
  or keys in separate root-only files and reference them with lego `_FILE`
  variables.
- Stay on the supported server matrix: Debian 13 or Ubuntu 24.04 LTS.

## Preflight Blocks

- DNS: the public Headscale host must resolve to the target server before deploy.
- Ports: `80/tcp`, `443/tcp`, and `3478/udp` must be available locally and
  allowed by the cloud firewall or security group.
- Services: existing Nginx can coexist by `server_name`, but Meshify owns the
  HTTP/HTTPS `default_server` catch-all. Disable or migrate any existing default
  site, and understand conflicting Headscale or reverse-proxy ownership before
  deploy.
- China mainland deployments: verify ICP/hosting access requirements, cloud
  ingress, package reachability, proxy settings, and whether HTTP-01 or DNS-01
  is the realistic certificate path.

## Package Delivery Problems

- `advanced.headscale_source.mode: "direct"` downloads the pinned official
  Headscale v0.28.0 `.deb` and verifies SHA-256 evidence before install.
- `advanced.headscale_source.mode: "mirror"` requires a reachable URL and
  explicit SHA-256 digest.
- `advanced.headscale_source.mode: "offline"` requires a local `.deb` file path
  and explicit SHA-256 digest.
- If the host needs a proxy, set standard `http_proxy`, `https_proxy`, and
  `no_proxy` values in the advanced config. The same proxy path must also allow
  the pinned lego v4.35.2 GitHub release archive unless the host reaches it
  directly or `advanced.lego_source.mode` is `offline`.
- Offline lego mode requires `advanced.lego_source.file_path` to point at the
  exact pinned lego archive for `advanced.platform.arch`. Meshify verifies the
  built-in archive SHA-256 before installing `/opt/meshify/bin/lego`.

## DNS, Ingress, And TLS Problems

- HTTP-01 requires public port 80 to reach the Nginx challenge URL backed by
  `/var/lib/meshify/acme-challenges`.
- DNS-01 uses pinned lego provider codes `cloudflare`, `route53`,
  `digitalocean`, and `gcloud`; `google` is accepted as a `gcloud` alias.
  Cloudflare and DigitalOcean require `advanced.dns01.env_file`; Route53 and
  gcloud may use lego's ambient credential chain when deploy and systemd renewal
  share the same host identity. Their env_file may also carry non-secret
  provider settings such as `AWS_HOSTED_ZONE_ID`, `AWS_PROFILE`, `GCE_PROJECT`,
  or `GCE_ZONE_ID`.
- Nginx must serve `/etc/meshify/tls/<server>/fullchain.pem` and
  `/etc/meshify/tls/<server>/privkey.pem`.
- If clients disconnect or DERP fails behind the reverse proxy, confirm the
  Nginx site still forwards HTTP/1.1 Upgrade and Connection headers.
- `systemctl start meshify-lego-renew.service` should complete when a renewal is
  due or report that no renewal is needed; the lego hook should validate and
  reload Nginx after an actual renewal.

## Host Binding And Exposure Problems

- Headscale should listen on `127.0.0.1:8080`.
- Metrics and gRPC listeners should remain loopback-only.
- Remote gRPC should not be exposed as the default management path.
- `3478/udp` must stay available for STUN when embedded DERP is enabled.
- The DERP baseline is self-hosted only. `derp.urls` should remain empty.

## Service And Checkpoint Problems

- Use `meshify status --config meshify.yaml` to see the current checkpoint and
  last failure after an interrupted deploy.
- If a package, lego, Nginx, systemd, or onboarding step fails, fix the named
  issue and rerun the same `meshify deploy --config meshify.yaml` command.
- For Headscale start failures, inspect
  `systemctl status headscale.service --no-pager --full` and the rendered
  `/etc/headscale/config.yaml`.
- For Nginx failures, run `nginx -t` and check `/etc/nginx/sites-available/headscale.conf`.

## Client Onboarding Problems

- Re-check the preauth key and `server_url` in [onboarding.md](onboarding.md).
- Confirm the client is running Tailscale >= v1.74.0.
- If MagicDNS does not resolve, confirm the client accepted managed DNS and the
  configured `base_domain` is correct.
- If a peer path stays on DERP, that can be acceptable when UDP direct
  connectivity is blocked. The real failure is losing peer connectivity
  entirely.
- The embedded DERP endpoint does not provide `/generate_204`; validate actual
  login, `tailscale status`, `tailscale ping`, and `tailscale netcheck` before
  treating captive-portal probing as the root cause.

## What To Capture

- The full `meshify deploy`, `meshify verify`, or `meshify status` output.
- The edited values from the `default` section of `meshify.yaml`.
- Whether the Headscale source mode is direct, mirror, or offline.
- Which client guide you followed: [Windows](clients/windows.md),
  [macOS](clients/macos.md), or
  [Debian/Ubuntu Linux](clients/debian-ubuntu-linux.md).
- Whether the failure affects control-plane login, MagicDNS resolution, direct path selection, or DERP fallback.
