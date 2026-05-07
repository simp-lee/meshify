# Meshify E2E Release Validation

Run these gates before a first-release cut. They are manual or
environment-backed checks because they need real hosts, public DNS, ACME, and
clients from different networks.

## Server Matrix

- Debian 13: run `meshify init -> meshify deploy -> meshify verify` on a fresh
  host.
- Ubuntu 24.04 LTS: run the same workflow and record any compatibility
  differences.
- Confirm `meshify status` shows the latest deploy checkpoints and no active
  failure.

## Server Runtime Checks

- Headscale v0.28.0 is installed from the verified `.deb` and managed by the
  official systemd unit.
- `/opt/meshify/bin/lego --version` reports the meshify-managed pinned lego `v4.35.2`.
- Headscale control-plane traffic listens on `127.0.0.1:8080`; metrics and gRPC
  remain loopback-only.
- The configured Nginx `server_name` blocks use `fullchain.pem`.
- Confirm explicit HTTP/HTTPS `default_server` catch-all blocks use empty `server_name ""`.
- They return `444` on HTTP and `421` on HTTPS, and never proxy unmatched Host/SNI traffic to Headscale.
- HTTP/1.1 Upgrade and Connection headers survive the Nginx reverse proxy.
- HTTP-01 issuance succeeds on a public host with port `80/tcp` reaching the
  Nginx webroot `/var/lib/meshify/acme-challenges`.
- DNS-01 issuance succeeds on at least one supported lego provider.
- For Cloudflare or DigitalOcean, use a root-only `advanced.dns01.env_file`.
- For Route53 or gcloud, verify either a root-only env file or lego's ambient credential chain.
- GCloud ambient mode also needs the project from Google Cloud metadata;
  otherwise set `GCE_PROJECT` in the env file.
- Raw DNS tokens or keys live in separate root-only files referenced by lego `_FILE` variables.
- No DNS secret value appears in `meshify.yaml`, deploy output, status output,
  systemd unit files, or rendered templates.
- Nginx serves the meshify-managed stable certificate paths
  `/etc/meshify/tls/<server>/fullchain.pem` and
  `/etc/meshify/tls/<server>/privkey.pem`.
- `meshify-lego-renew.timer` is enabled and active.
- `systemctl start meshify-lego-renew.service` exercises the lego renewal path,
  and the hook validates and reloads Nginx after an actual renewal.
- `derp.urls` is empty, embedded DERP is present, and STUN listens on `3478/udp`.

## Client Matrix

- Join at least two clients from different network environments.
- Use a heterogeneous pair when possible: Windows plus macOS, Windows plus
  Debian/Ubuntu, or macOS plus Debian/Ubuntu.
- Confirm each client uses Tailscale >= v1.74.0.
- Join with the documented `--login-server`, preauth key, and
  `--accept-dns=true` flow or the documented platform UI equivalent.

## Connectivity Gates

- `tailscale status` shows all test clients online.
- `tailscale ping` reaches another client by MagicDNS name and by 100.x address.
- `tailscale netcheck` or platform-equivalent output records whether direct
  connectivity is available.
- At least one test observes a direct path when UDP traversal is available.
- At least one test blocks or restricts UDP enough to observe DERP fallback over
  TCP/443 while peer traffic still works.
- `tailscale debug derp-map` shows only the self-hosted DERP region.
- Client onboarding and reconnect still work even though embedded DERP does not
  provide `/generate_204`.

## Docs Walkthrough

- A reviewer who did not implement the feature follows
  `deploy/docs/quickstart.md`.
- The reviewer uses `deploy/docs/onboarding.md` and one platform guide for each
  tested client.
- Any mismatch between CLI output and docs is fixed before release.
