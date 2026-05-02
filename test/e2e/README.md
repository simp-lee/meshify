# Meshify E2E Release Validation

Run these gates before a first-release cut. They are manual or environment-backed checks because they need real hosts, public DNS, ACME, and clients from different networks.

## Server Matrix

- Debian 13: run `meshify init -> meshify deploy -> meshify verify` on a fresh host.
- Ubuntu 24.04 LTS: run the same workflow and record any compatibility differences.
- Confirm `meshify status` shows the latest deploy checkpoints and no active failure.

## Server Runtime Checks

- Headscale v0.28.0 is installed from the verified `.deb` and managed by the official systemd unit.
- Headscale control-plane traffic listens on `127.0.0.1:8080`; metrics and gRPC remain loopback-only.
- The configured Nginx `server_name` block uses `fullchain.pem` and does not use `default_server`.
- The `_` catch-all default server blocks return `444` on HTTP and `421` on HTTPS, and do not proxy to Headscale.
- HTTP/1.1 Upgrade and Connection headers survive the Nginx reverse proxy.
- `certbot renew --dry-run` passes and the deploy hook validates and reloads Nginx.
- `derp.urls` is empty, embedded DERP is present, and STUN listens on `3478/udp`.

## Client Matrix

- Join at least two clients from different network environments.
- Use a heterogeneous pair when possible: Windows plus macOS, Windows plus Debian/Ubuntu, or macOS plus Debian/Ubuntu.
- Confirm each client uses Tailscale >= v1.74.0.
- Join with the documented `--login-server`, preauth key, and `--accept-dns=true` flow or the documented platform UI equivalent.

## Connectivity Gates

- `tailscale status` shows all test clients online.
- `tailscale ping` reaches another client by MagicDNS name and by 100.x address.
- `tailscale netcheck` or platform-equivalent output records whether direct connectivity is available.
- At least one test observes a direct path when UDP traversal is available.
- At least one test blocks or restricts UDP enough to observe DERP fallback over TCP/443 while peer traffic still works.
- `tailscale debug derp-map` shows only the self-hosted DERP region.
- Client onboarding and reconnect still work even though embedded DERP does not provide `/generate_204`.

## Docs Walkthrough

- A reviewer who did not implement the feature follows `deploy/docs/quickstart.md`.
- The reviewer uses `deploy/docs/onboarding.md` and one platform guide for each tested client.
- Any mismatch between CLI output and docs is fixed before release.
