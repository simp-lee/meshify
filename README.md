# Meshify

[English](README.md) | [简体中文](README.zh-CN.md)

Meshify is a Go CLI for deploying a small, single-host Headscale control plane. It turns one `meshify.yaml` file into a Debian-family server running Headscale behind Nginx TLS, with embedded DERP/STUN, MagicDNS, local onboarding, and static verification.

## Quick Start

Download a published release binary for the target server. Replace `vX.Y.Z` with the release tag:

```bash
VERSION=vX.Y.Z
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ASSET=meshify_linux_amd64 ;;
  aarch64|arm64) ASSET=meshify_linux_arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

curl -LO "https://github.com/simp-lee/meshify/releases/download/${VERSION}/${ASSET}"
curl -LO "https://github.com/simp-lee/meshify/releases/download/${VERSION}/checksums.txt"
sha256sum -c --ignore-missing checksums.txt
chmod +x "${ASSET}"
sudo install -m 0755 "${ASSET}" /usr/local/bin/meshify
meshify --help
```

If you are using a source checkout instead of a release binary, run `make build` and install `./meshify` to `/usr/local/bin/meshify`.

Then run the default workflow (`init -> deploy -> verify`), followed by a read-only status check:

```bash
meshify init --config meshify.yaml
sudo meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

Before deploying, point public DNS such as `hs.example.com` at the server and allow `80/tcp`, `443/tcp`, and `3478/udp`. After verification passes, use the printed preauth key or create a fresh one, then follow the client section below.

## Supported Scope

| Area | Baseline |
| --- | --- |
| Server OS | Debian, Ubuntu, or a Debian-family distribution with apt/dpkg/systemd |
| Control plane | Headscale v0.28.0 on loopback behind Nginx |
| TLS automation | HTTP-01 or DNS-01 with a meshify-managed pinned lego v4.35.2 binary |
| Relay | Embedded Headscale DERP and STUN on `3478/udp`; no official DERP fallback |
| Clients | Windows, macOS, Debian/Ubuntu Linux |
| Client baseline | Tailscale client >= v1.74.0 |

Meshify intentionally does not include multi-host high availability, Kubernetes, Terraform, Ansible, a Web UI, OIDC/SSO, automatic SQLite backup and restore, official DERP fallback, or remote gRPC/API-key management by default.

## Server Guide

### Before You Start

- Server access: root or passwordless sudo on Debian, Ubuntu, or a Debian-family distribution that reports `debian` or `ubuntu` through `/etc/os-release`.
- Host capabilities: `apt-get`, `dpkg`, and a booted systemd runtime must be available before deploy can mutate the host.
- DNS: point the public Headscale name, for example `hs.example.com`, at the server.
- Firewall: allow `80/tcp`, `443/tcp`, and `3478/udp` in both host firewall and cloud security group.
- Packages: the server must reach the Headscale `.deb` and pinned lego archive, or you must prepare mirror/offline sources.
- Clients: prepare at least two clients from different networks for final validation.
- China mainland deployments: confirm ICP/hosting access requirements, cloud ingress, package reachability, proxy settings, and whether HTTP-01 is practical. Use DNS-01, mirrors, offline artifacts, or proxies when needed.

### Minimal Config

The public example is [`deploy/config/meshify.yaml.example`](deploy/config/meshify.yaml.example). Most first deployments only edit `default`:

```yaml
default:
  server_url: "https://hs.example.com"
  base_domain: "tailnet.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
```

`server_url` is the HTTPS Headscale endpoint clients use with `tailscale up --login-server`. It must be a DNS name and normally uses port 443. `base_domain` is the private MagicDNS suffix and must not equal the Headscale host or be its parent domain.

Use advanced mode only when you need DNS-01, Headscale mirror/offline packages, Headscale metrics port changes, offline lego archives, proxies, architecture overrides, or public IP overrides:

```bash
meshify init --advanced --config meshify.yaml
```

### ACME

Use HTTP-01 when public port 80 can reach the server:

```yaml
default:
  acme_challenge: "http-01"
```

Use DNS-01 only when port 80 cannot be used reliably or policy requires DNS validation. DNS-01 uses lego provider codes `cloudflare`, `route53`, `digitalocean`, and `gcloud`; `google` is accepted as a `gcloud` alias.

Keep raw DNS values out of `meshify.yaml`. Cloudflare and DigitalOcean require a root-only `advanced.dns01.env_file`. Route53 and gcloud may use lego's ambient credential chain when deploy and systemd renewal run with the same host identity. Raw DNS tokens or keys live in separate root-only files referenced by lego `_FILE` variables.

### Deploy

Run deploy on the target server:

```bash
sudo meshify deploy --config meshify.yaml
```

Deploy checks config, OS family, host capabilities, permissions, DNS, ports, package sources, ACME readiness, and service conflicts. Then it installs dependencies, installs lego and Headscale, renders runtime files, issues the certificate, enables services, creates the first local Headscale user and preauth key, and runs static verification.

If a step fails, fix the named issue and rerun the same deploy command. Meshify records checkpoints beside the config file under `.meshify/`.

### Verify And Status

```bash
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

`verify` re-checks rendered Headscale, ACL, Nginx, TLS hook, certificate plan, onboarding readiness, and the Tailscale client version baseline. `meshify status` is read-only and shows config readiness, completed checkpoints, warnings, and the last recoverable failure.

Expected result:

- Headscale control plane, metrics, and gRPC listeners stay on loopback.
- Nginx serves HTTP-01 challenges from `/var/lib/meshify/acme-challenges`, terminates TLS with `fullchain.pem`, and forwards HTTP/1.1 upgrade traffic for control and DERP WebSocket paths.
- Nginx uses `/etc/meshify/tls/<server>/fullchain.pem` and `/etc/meshify/tls/<server>/privkey.pem`.
- Headscale exposes STUN on `3478/udp`, uses embedded DERP, and keeps `derp.urls` empty.
- Two clients from different networks can join, resolve MagicDNS names, reach each other with `tailscale ping`, and show direct paths or DERP fallback in `tailscale netcheck`.

### Runtime Topology

```text
Internet
  |
  | 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Debian-family apt/dpkg/systemd host            |
|                                                |
| Nginx                                          |
|   - HTTP-01 webroot                            |
|   - TLS termination with fullchain.pem         |
|   - reverse proxy to 127.0.0.1:8080            |
|                                                |
| Headscale                                      |
|   - control plane on loopback                  |
|   - metrics on configurable loopback port      |
|   - gRPC on loopback                           |
|   - local admin over unix socket               |
|   - embedded DERP and STUN on 3478/udp         |
|                                                |
| meshify-managed lego                           |
|   - certificate issue/renew                    |
|   - install hook reloads Nginx after validate  |
+------------------------------------------------+
  |
  v
Clients prefer direct WireGuard paths and fall back to DERP over 443.
```

### Security Boundaries

- The Go CLI is the only intended user-facing server entrypoint.
- Public HTTP and HTTPS terminate at Nginx. Headscale control-plane traffic does not bind to a public interface.
- Explicit HTTP and HTTPS `default_server` catch-all blocks reject unmatched Host or SNI traffic instead of proxying it to Headscale.
- Headscale administration stays local through the unix socket; do not expose remote gRPC or API-key management unless you intentionally add it.
- DNS-01 provider values must stay outside `meshify.yaml`, rendered templates, deploy output, status output, and systemd units.

### Server Troubleshooting

Start with the command that failed. `meshify deploy`, `meshify verify`, and `meshify status` report the failed step, impact, remediation, and retry command when recovery is possible.

Config checks:

- `server_url` must use HTTPS, must be a DNS name, and may only omit the port or use port 443.
- `base_domain` must not equal the `server_url` host and must not be its parent suffix.
- `certificate_email` must be a plain email address.
- `acme_challenge` must be `http-01` or `dns-01`.

Preflight blocks:

- DNS must resolve the public Headscale host to the target server before deploy.
- `80/tcp`, `443/tcp`, and `3478/udp` must be available locally and allowed by the cloud firewall or security group.
- Existing Nginx can coexist by `server_name`, but Meshify owns the HTTP/HTTPS `default_server` catch-all. Disable or migrate conflicting default sites.

Package and lego failures:

- Direct Headscale source downloads the pinned Headscale v0.28.0 `.deb` and verifies SHA-256 evidence.
- Mirror mode requires a reachable URL and explicit SHA-256 digest.
- Offline mode requires a local `.deb` path and explicit SHA-256 digest.
- Offline lego mode requires `advanced.lego_source.file_path` to point at the exact pinned lego archive for `advanced.platform.arch`.

Runtime failures:

- Headscale should listen on `127.0.0.1:8080`.
- Metrics should listen on `127.0.0.1:<advanced.headscale.metrics_port>`; the default is `19090`.
- gRPC should listen on `127.0.0.1:50443`.
- `3478/udp` must stay available for STUN when embedded DERP is enabled.
- For Headscale start failures, inspect `systemctl status headscale.service --no-pager --full` and `/etc/headscale/config.yaml`.
- For Nginx failures, run `nginx -t` and check `/etc/nginx/sites-available/headscale.conf`.
- If login or DERP breaks behind Nginx, confirm HTTP/1.1 Upgrade and Connection headers are still forwarded.

Capture full `meshify deploy`, `meshify verify`, or `meshify status` output, edited `default` values, Headscale source mode, and whether the failure affects deploy, certificate issuance, Nginx, Headscale, MagicDNS, direct path selection, or DERP fallback.

## Client Guide

Use this section after `meshify deploy` and `meshify verify` pass.

### Operator Handoff

Give each client user:

- `server_url`, for example `https://hs.example.com`.
- A fresh one-time preauth key. The key printed by deploy is enough for the first device; create another key for each additional client.
- The MagicDNS suffix from `base_domain`, for example `tailnet.example.com`.
- The platform instructions from this section.

Keep Headscale administration local. The default runtime config uses `/var/run/headscale/headscale.sock`; do not expose remote gRPC or API-key management for Day 1.

### Create A Fresh Preauth Key

`meshify deploy` creates the initial `meshify` user and a one-time preauth key when Headscale is running. Headscale preauth keys are not reusable by default: the command below creates a key that can register one client and expires after 24 hours. To onboard more clients, run the command again and give each client a different key.

```bash
sudo headscale --config /etc/headscale/config.yaml users list
# Only if the meshify user is missing from users list:
sudo headscale --config /etc/headscale/config.yaml users create meshify
sudo headscale --config /etc/headscale/config.yaml users list
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h
```

Use the numeric user ID shown by `users list` for the `meshify` user. Use a short expiration for one-time onboarding. Only when you intentionally want one key to register multiple clients, add `--reusable`:

```bash
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h --reusable
```

Treat reusable keys as a convenience for controlled automation or short maintenance windows, not as the default handout for end-user devices.

### Shared Validation

Every supported client should:

- install Tailscale client >= v1.74.0
- join with the supplied `server_url` and preauth key
- accept managed DNS with `--accept-dns=true` or the platform UI equivalent
- appear online in `tailscale status`
- reach another node with `tailscale ping`
- show path information with `tailscale netcheck`

Validate at least two clients from different networks, such as home broadband plus office network, or home broadband plus phone hotspot. Direct WireGuard paths are preferred when UDP traversal works. DERP fallback over TCP/443 is acceptable when UDP direct connectivity is blocked and peer traffic still works.

`tailscale debug derp-map` is optional; it should show only the self-hosted DERP region. Embedded DERP does not provide `/generate_204`; validate real login, `tailscale status`, `tailscale ping`, MagicDNS, and `tailscale netcheck` before treating captive-portal probing as a deployment failure.

### Windows

Install Tailscale client >= v1.74.0 from <https://tailscale.com/download/windows> or Microsoft Store. For custom login server setup, open Administrator PowerShell.

```powershell
& "$env:ProgramFiles\Tailscale\tailscale.exe" version
& "$env:ProgramFiles\Tailscale\tailscale.exe" up --login-server https://hs.example.com --auth-key "<preauth-key>" --accept-dns=true --hostname=laptop
& "$env:ProgramFiles\Tailscale\tailscale.exe" status
& "$env:ProgramFiles\Tailscale\tailscale.exe" ping peer-name.tailnet.example.com
& "$env:ProgramFiles\Tailscale\tailscale.exe" netcheck
```

Daily operations:

```powershell
& "$env:ProgramFiles\Tailscale\tailscale.exe" down
& "$env:ProgramFiles\Tailscale\tailscale.exe" up --login-server https://hs.example.com --accept-dns=true
```

### macOS

Install Tailscale client >= v1.74.0 from <https://tailscale.com/download/mac>. The standalone package is the usual choice; the Mac App Store version is also supported.

Graphical flow: Option-click the Tailscale menu bar icon, open Debug, choose a custom login server, enter `server_url`, then finish the key or login flow.

CLI flow when the installed build provides `tailscale`:

```bash
tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
```

Daily operations:

```bash
tailscale down
tailscale up --login-server https://hs.example.com --accept-dns=true
```

### Debian/Ubuntu Linux

Install Tailscale client >= v1.74.0 from the official Linux package source:

```bash
curl -fsSL https://tailscale.com/install.sh | sh
tailscale version
systemctl status tailscaled --no-pager --full
sudo tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
```

If `curl | sh` is not allowed, follow the Debian or Ubuntu steps at <https://tailscale.com/download/linux>, or use an operator-provided package.

Daily operations:

```bash
sudo tailscale down
sudo tailscale up --login-server https://hs.example.com --accept-dns=true
```

### Client Troubleshooting

- Re-check `server_url`, preauth key freshness, and whether the key has already been consumed.
- Confirm the client is running Tailscale client >= v1.74.0.
- If MagicDNS does not resolve, confirm managed DNS was accepted and `base_domain` is correct.
- If a peer path stays on DERP, that can be acceptable when UDP direct connectivity is blocked. The real failure is losing peer connectivity entirely.
- If `tailscale` is missing, reinstall from the platform section above.
- Windows failures often involve firewall or endpoint-security software blocking the virtual adapter.
- macOS failures often involve unapproved VPN prompts or captive portal Wi-Fi.
- Linux failures often involve `tailscaled` not running or `/dev/net/tun` missing.
- Embedded DERP does not provide `/generate_204`; validate actual login, `tailscale status`, `tailscale ping`, MagicDNS, and `tailscale netcheck`.

Capture the platform, client version, `server_url`, whether managed DNS was accepted, and whether the failure affects login, MagicDNS, direct path selection, or DERP fallback.
