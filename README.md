# Meshify

[English](README.md) | [Chinese](README.zh-CN.md)

Meshify is a Go server deployment tool, not a VPN client. It is for individuals or small teams who want a private Tailscale/Headscale network without hand-configuring Headscale, Nginx, HTTPS certificates, and systemd. Bring one Debian/Ubuntu cloud server, one domain name, and root or sudo access; Meshify deploys from `meshify.yaml`. You can also use `meshify app` to publish same-host or tailnet HTTP/WebSocket services to public HTTPS, with an optional GoAccess real-time access-log dashboard. If you only want to run your own Go web service on a cloud server, you can use only the app workflow and skip the private Tailscale/Headscale network.

## Is Meshify A Fit?

| What you want | Fit |
| --- | --- |
| Use the official Tailscale client to join your own private network | Yes. Meshify deploys the Headscale control plane. |
| Automatically configure Headscale, Nginx, certificate renewal, and baseline verification on one cloud server | Yes. This is the core goal of this repository. |
| Publish same-host Go web services or tailnet HTTP/WebSocket services to HTTPS, with an optional access-log dashboard | Yes. Use `meshify app` and optionally enable `nginx.goaccess`. |
| Publish only a Go web service on the cloud server without a private Tailscale/Headscale network | Yes. Use `meshify app` in `listen` mode. |
| Build multi-host high availability, Kubernetes, Terraform, Ansible, Web UI, or OIDC/SSO | No. These are outside the current scope. |
| Find a Tailscale client, general reverse proxy framework, or Go SDK | No. This repository is mainly a CLI, config examples, and runtime templates. |

## Quick Start

Prepare three things before deploy:

- A Debian/Ubuntu or Debian-family server where you can deploy as root or with sudo.
- A public DNS name, for example `hs.example.com`, pointed at the server.
- `80/tcp`, `443/tcp`, and `3478/udp` allowed in both the host firewall and cloud security group.

If you only want to deploy a same-host Go web service and are not creating a private network, you only need an app domain plus `80/tcp` and `443/tcp`; `3478/udp` is only for the main Headscale deployment.

Install a published release binary on the target server. First open [Releases](https://github.com/simp-lee/meshify/releases) and copy the latest stable release tag, then replace `vX.Y.Z` below with that tag; do not copy the placeholder literally.

```bash
VERSION=vX.Y.Z
curl -fsSL https://raw.githubusercontent.com/simp-lee/meshify/main/scripts/install.sh | sh -s -- "${VERSION}"
meshify --help
```

The install script detects `x86_64` and `arm64`/`aarch64`, downloads the matching GitHub release asset for `VERSION`, verifies it against `checksums.txt`, and installs `meshify` to `/usr/local/bin/meshify`.

If you are using a source checkout instead of a release binary, run `make build` and install `./meshify` to `/usr/local/bin/meshify`.

Then run the default workflow (`init -> verify -> deploy -> verify -> status`). Before deploy, inspect `meshify.yaml`; if it still contains example values, change at least `default.server_url`, `default.base_domain`, and `default.certificate_email` to your real values.

```bash
meshify init --config meshify.yaml
# Check meshify.yaml; if it still has example values, edit default first.
meshify verify --config meshify.yaml
sudo meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
meshify status --config meshify.yaml
```

After `deploy` succeeds, the CLI prints an initial preauth key. Use it for the first client, then create a fresh key for each additional client.

The default workflow above deploys a private Tailscale/Headscale network. If you only want to publish a same-host Go service, skip the main `meshify init` and `meshify deploy` workflow and go to "Additional Go Services" below.

## Supported Scope

| Area | Baseline |
| --- | --- |
| Server OS | Debian, Ubuntu, or a Debian-family distribution with apt/dpkg/systemd |
| Control plane | Headscale v0.28.0 on loopback behind Nginx |
| TLS automation | HTTP-01 or DNS-01 with a meshify-managed pinned lego v5.1.0 binary |
| Relay | Embedded Headscale DERP and STUN on `3478/udp`; no official DERP fallback |
| Clients | Windows, macOS, Debian/Ubuntu Linux |
| Client baseline | Tailscale client >= v1.74.0 |

Meshify intentionally keeps the scope small: no multi-host high availability, Kubernetes, Terraform, Ansible, Web UI, OIDC/SSO, automatic SQLite backup and restore, official DERP fallback, or remote gRPC/API-key management by default.

## Server Guide

### Before You Start

- Server access: root or sudo access on Debian, Ubuntu, or a Debian-family distribution that reports `debian` or `ubuntu` through `/etc/os-release`; when running `meshify deploy` directly as a non-root user, `sudo -n true` must pass.
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

Field meanings:

| Field | Purpose |
| --- | --- |
| `server_url` | HTTPS endpoint clients use with `tailscale up --login-server`; it must be a DNS name and normally uses port 443 |
| `base_domain` | Private MagicDNS suffix; it must not equal the Headscale hostname or be its parent domain |
| `certificate_email` | ACME registration email |
| `acme_challenge` | `http-01` or `dns-01` |

Use advanced mode only when you need DNS-01, Headscale mirror/offline packages, Headscale metrics port changes, offline lego archives, package probe timeout overrides, proxies, architecture overrides, or public IP overrides:

```bash
meshify init --advanced --config meshify.yaml
```

For slow but reachable GitHub release downloads, raise the package source probe timeouts:

```yaml
advanced:
  package_probe:
    reachability_timeout: "30s"
    artifact_timeout: "5m"
```

### ACME

Use HTTP-01 when public port 80 can reach the server:

```yaml
default:
  acme_challenge: "http-01"
```

Use DNS-01 only when public port 80 is unreliable or policy requires DNS validation. DNS-01 uses lego provider codes `cloudflare`, `route53`, `digitalocean`, `gcloud`, and `tencentcloud`; `google` is accepted as a `gcloud` alias.

Keep DNS API values out of `meshify.yaml`, and do not put raw tokens or keys directly in `env_file`. Cloudflare, DigitalOcean, and Tencent Cloud require a root-only `advanced.dns01.env_file` that references root-only secret files with provider-supported `_FILE` variables. Route53 and gcloud may use the host credential chain, or `env_file` may contain only provider-supported settings or credential file paths.

For Tencent Cloud DNS / DNSPod, create Tencent Cloud API credentials with permission to manage DNSPod records, store the SecretId and SecretKey in root-only files, and reference those files from the lego env file:

```bash
sudo install -d -m 0700 /etc/meshify/dns01
printf '%s' '<Tencent Cloud SecretId>' | sudo tee /etc/meshify/dns01/tencentcloud-secret-id >/dev/null
printf '%s' '<Tencent Cloud SecretKey>' | sudo tee /etc/meshify/dns01/tencentcloud-secret-key >/dev/null
sudo chmod 0600 /etc/meshify/dns01/tencentcloud-secret-id /etc/meshify/dns01/tencentcloud-secret-key

sudo tee /etc/meshify/dns01/tencentcloud.env >/dev/null <<'EOF'
TENCENTCLOUD_SECRET_ID_FILE=/etc/meshify/dns01/tencentcloud-secret-id
TENCENTCLOUD_SECRET_KEY_FILE=/etc/meshify/dns01/tencentcloud-secret-key
TENCENTCLOUD_PROPAGATION_TIMEOUT=900
TENCENTCLOUD_POLLING_INTERVAL=10
TENCENTCLOUD_TTL=600
TENCENTCLOUD_HTTP_TIMEOUT=60
LEGO_DNS_RESOLVERS=119.29.29.29:53
LEGO_DNS_TIMEOUT=30
LEGO_DNS_PROPAGATION_DISABLE_ANS=true
EOF
sudo chmod 0600 /etc/meshify/dns01/tencentcloud.env
```

The `LEGO_DNS_*` values pin lego's DNS zone lookup to DNSPod's public resolver, keep recursive TXT polling active, and skip the authoritative nameserver propagation check that can fail under Tencent Cloud EdgeOne / DNSPod hosted access. With `TENCENTCLOUD_POLLING_INTERVAL=10`, lego checks every 10 seconds and continues as soon as the TXT record is visible to the configured recursive resolver.

Then set DNS-01 in `meshify.yaml`:

```yaml
default:
  acme_challenge: "dns-01"

advanced:
  dns01:
    provider: "tencentcloud"
    env_file: "/etc/meshify/dns01/tencentcloud.env"
```

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

`verify` is a static config and runtime-template check. It re-checks rendered Headscale, ACL, Nginx, TLS hook, certificate plan, onboarding readiness, and the Tailscale client version baseline; it does not read host systemd state, certificate files, Nginx runtime state, Headscale process state, or client online state. `meshify status` is read-only and shows config readiness, completed checkpoints, warnings, and the last recoverable failure.

After deploy, confirm host runtime state with system commands:

```bash
sudo systemctl status headscale.service nginx.service --no-pager --full
sudo nginx -t
curl -I https://hs.example.com
```

Expected result:

- Headscale control plane, metrics, and gRPC listeners stay on loopback.
- Nginx serves HTTP-01 challenges from `/var/lib/meshify/acme-challenges`, terminates TLS with `fullchain.pem`, and forwards HTTP/1.1 upgrade traffic for control and DERP WebSocket paths.
- Nginx uses `/etc/meshify/tls/<server>/fullchain.pem` and `/etc/meshify/tls/<server>/privkey.pem`.
- Headscale exposes STUN on `3478/udp`, uses embedded DERP, and keeps `derp.urls` empty.
- Two clients from different networks can join, resolve MagicDNS names, reach each other with `tailscale ping`, and show direct paths or DERP fallback in `tailscale netcheck`.

### Runtime Topology

The main deployment topology looks like this. The public internet should only see Nginx HTTP/HTTPS and the Headscale STUN port; Headscale control plane, metrics, and gRPC stay on loopback.

```text
Internet, ACME CA, and Tailscale clients
  |                         \
  | 443/tcp control + DERP   \ 3478/udp STUN
  | 80/tcp HTTP-01            \
  v                            v
+------------------------------------------------+
| Debian-family apt/dpkg/systemd host            |
|                                                |
| Nginx                                          |
|   - HTTP-01 webroot                            |
|   - TLS termination with fullchain.pem         |
|   - reverse proxy to 127.0.0.1:8080            |
|   - HTTP/1.1 upgrade for DERP WebSocket        |
|                                                |
| Headscale                                      |
|   - control plane on 127.0.0.1:8080            |
|   - metrics on 127.0.0.1:<metrics_port>        |
|   - gRPC on 127.0.0.1:50443                    |
|   - local admin over unix socket               |
|   - embedded DERP over HTTPS proxy path        |
|   - STUN on 3478/udp                           |
|                                                |
| meshify-managed lego                           |
|   - certificate issue/renew                    |
|   - install hook reloads Nginx after validate  |
+------------------------------------------------+
```

Clients prefer direct WireGuard paths. When direct connectivity fails, embedded DERP behind Nginx falls back over 443.

If you deploy a Go service on the same host, the topology adds an app site. The app port still listens on loopback and is not exposed directly to the public internet.

```text
Internet
  |
  | app domain: 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Nginx app site                                 |
|   - app certificate and Host/SNI allowlist     |
|   - optional static alias locations            |
|   - proxy / to 127.0.0.1:18001                 |
|                                                |
| Optional GoAccess log dashboard                |
|   - reads canonical Nginx access log           |
|   - live updates over loopback WebSocket       |
|   - Nginx serves report.html and proxies /ws   |
|   - logrotate for meshify-managed access log   |
|                                                |
| example-app.service                            |
|   - runs as app.name system user               |
|   - ExecStart from service.exec_start          |
|   - listens on loopback only                   |
+------------------------------------------------+
```

For a same-host Go service only, the app site above can be the complete topology: public traffic enters Nginx, Nginx proxies to the local Go service, and no Headscale or Tailscale client is required.

If you publish an HTTP/WebSocket service on another tailnet node, the public entrypoint is still this cloud server, but the upstream goes through the local Tailscale client to a fixed `100.64.x.y:port`.

```text
Internet
  |
  | app domain: 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Nginx app site                                 |
|   - app certificate and Host/SNI allowlist     |
|   - proxy / to fixed tailnet upstream          |
|                                                |
| Tailscale client on this host                  |
|   - logged in to expected login server         |
+------------------------------------------------+
  |
  | tailnet HTTP/WebSocket
  v
100.64.x.y:port on another tailnet node
```

### Additional Go Services

To run your own Go web service on the same cloud server, or to publish a tailnet HTTP/WebSocket service through this server, use a standalone app config and the `meshify app` workflow.

If you do not want a private Tailscale/Headscale network and only want public HTTPS for a Go service on this cloud server, use `listen` mode here. This mode does not require a prior Headscale deployment; keep `upstream: ""` and do not change `tailscale.enabled_for_listen` to `true`, and Meshify only manages the local app, Nginx, certificates, and systemd.

For a first deployment, use the shortest path: generate config, edit config, run static verification, then deploy.

```bash
meshify app init --config meshify-apps/abc.yaml
# Edit meshify-apps/abc.yaml
meshify app verify --config meshify-apps/abc.yaml
sudo meshify app deploy --config meshify-apps/abc.yaml
```

The app workflow does not have `--example`: `meshify app init` already writes the editable example config. `--example` only applies to the main `meshify init` command. The app config example source is [`deploy/config/meshify-app.yaml.example`](deploy/config/meshify-app.yaml.example); `meshify app init` writes the same editable structure.

Use one config file per app, commonly under `meshify-apps/`:

```text
meshify.yaml
meshify-apps/abc.yaml
meshify-apps/admin.yaml
meshify-apps/tailapp.yaml
```

A same-host `listen` app-only deployment can omit `meshify.yaml`; the layout above is just a common shape when one repo manages both the main private network and multiple apps.

An `upstream` app must use the Tailscale client; a `listen` app uses it only when `tailscale.enabled_for_listen: true`. When `tailscale.login_server` is empty, Meshify reads `default.server_url` from `tailscale.meshify_config`; when `tailscale.meshify_config` is empty, it defaults to `./meshify.yaml` in the command's current working directory, not beside `--config`. If the main config lives elsewhere, set `tailscale.meshify_config` explicitly. If there is no main config, set `tailscale.login_server` explicitly and provide a root-only `tailscale.auth_key_file` or pre-login this cloud server to the expected tailnet.

Deploy checks whether the Tailscale client is installed, running, and logged in to the expected login server; when that is already true, it skips install and re-login. Automatic login always adds `--accept-dns=false --accept-routes=false --shields-up`. If the machine is already logged in to a login server Meshify cannot prove matches, deploy fails and does not log out, reset state, or rejoin automatically.

The config filename is not the deployment identity. `app.name` names the systemd unit, Nginx site, and certificate directory, so renaming a config file does not rename installed runtime resources.

#### Choose A Mode

| Mode | Use it when | You prepare |
| --- | --- | --- |
| `listen` | The Go service runs on this cloud server | Install the business binary at the absolute path used by the first token of `service.exec_start`, and make it listen on loopback |
| `upstream` | The backend runs on another tailnet node, for example `100.64.10.20:18001` | Confirm the cloud server's Tailscale client can reach that fixed HTTP/WebSocket upstream |

`listen` means local app mode: the Go service runs on the same cloud server and listens on loopback, such as `127.0.0.1:18001`. Meshify generates the app systemd service, Nginx site, certificate, hook, and renewal timer. It verifies that the business binary exists and is executable; it does not copy your binary.

`upstream` means tailnet upstream mode: public Nginx proxies to a fixed HTTP/WebSocket address on another tailnet node, such as `100.64.10.20:18001`. This mode does not generate a local app service. `listen` and `upstream` are mutually exclusive; `upstream` mode automatically requires the Tailscale client. Use `upstream` only for HTTP/WebSocket services, not PostgreSQL, Redis, MySQL, or other database ports.

The default app config enables `nginx.http2: true`. The target host's Nginx must be `1.25.1` or newer and include the `http_v2` module; if you use an older Debian/Ubuntu distro Nginx package, set `nginx.http2: false` in the app config before deploy.

#### Minimal App Config

`listen` mode example:

```yaml
api_version: meshify/app/v1alpha1

app:
  name: "example-app"
  domains:
    - "abc.com"
    - "www.abc.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
  listen: "127.0.0.1:18001"
  upstream: ""

service:
  exec_start: "/opt/example-app/example-app --listen 127.0.0.1:18001"
  working_directory: "/opt/example-app"
  env_file: ""

tailscale:
  enabled_for_listen: false
```

Replace `abc.com` with your real app domain, and replace `/opt/example-app/example-app` with the Go binary you already uploaded to the server. Your Go program listens on `127.0.0.1:18001`; public users visit the Nginx HTTPS site, such as `https://abc.com`, and you should not expose `18001` directly to the internet.

`upstream` mode example:

```yaml
api_version: meshify/app/v1alpha1

app:
  name: "tailapp"
  domains:
    - "tailapp.example.com"
  certificate_email: "ops@example.com"
  acme_challenge: "http-01"
  listen: ""
  upstream: "100.64.10.20:18001"

service:
  exec_start: ""
  working_directory: ""
  env_file: ""

tailscale:
  # If there is no main meshify.yaml beside this app config, set login_server explicitly.
  # login_server: "https://hs.example.com"
  # auth_key_file: "/etc/meshify/tailscale/app-auth-key"
```

Multiple domains belong in one `app.domains` list. They are written to the same Nginx `server_name`, the same certificate SAN set, and the same Host/SNI allowlist. The first app release does not create canonical redirects between names such as `abc.com` and `www.abc.com`; they serve the same app by default.

If the app needs a systemd environment file such as `web.env`, set `service.env_file` to that absolute path. Deploy checks that it is a root-owned, root-only file and renders it as `EnvironmentFile=`. `service.env_file` is only used in `listen` mode.

#### Advanced App Config

For a first deployment, you can usually leave the `nginx` section alone. Revisit these fields when you need large uploads, long connections, SSE, WebSocket behavior, or static files:

| Config | Default or purpose |
| --- | --- |
| `nginx.client_max_body_size` | Defaults to `20m` |
| `nginx.http2` | Enabled by default and renders the modern `http2 on;` directive |
| `nginx.access_log` / `nginx.error_log` | Optional per-app log files; `access_log` may also be `off` |
| `proxy.read_timeout` | App proxy read timeout, default `600s` |
| `proxy.send_timeout` | App proxy send timeout, default `600s` |
| `proxy.connect_timeout` | Optional connect timeout |
| `proxy.buffering` / `proxy.request_buffering` | Useful for streaming responses, SSE, or upload forwarding |
| `nginx.static_locations` | Publishes static files such as `/static/`, `/sitemap.xml`, or `/sitemaps/` from your app release directory |

Keep `nginx.access_log: ""` by default. Set another path only when you need an existing log directory, an external collector, or a custom rotation policy.

| Config | Behavior | Automatic rotation |
| --- | --- | --- |
| `nginx.access_log: ""` + GoAccess disabled | Meshify does not render a per-app `access_log`; Nginx inherits the global log configuration. Debian/Ubuntu commonly uses `/var/log/nginx/access.log` | Not by Meshify; usually handled by distro Nginx/logrotate |
| `nginx.access_log: ""` + GoAccess enabled | Meshify uses and creates `/var/log/meshify/apps/<app-name>/access.log`, then renders the log format required by GoAccess | Yes: daily, 14 rotations, compressed; rotation reloads Nginx and restarts GoAccess |
| Explicit non-managed `nginx.access_log` path + GoAccess disabled | Meshify only writes the path into the Nginx config; it does not create the file or directory | No; configure logrotate yourself |
| Explicit non-managed `nginx.access_log` path + GoAccess enabled | The path becomes the external canonical access log. Prepare it before deploy and satisfy the GoAccess safety rules below | No; rotate it yourself and handle Nginx reopen/reload plus GoAccess restart |

`nginx.static_locations` renders before the app proxy location and supports optional `default_type`, `expires`, `Cache-Control`, `try_files $uri =404`, `gzip_static on`, and `access_log off`. Prefer either `expires` or `cache_control` for one location, because Nginx `expires` also emits a `Cache-Control` header; if both are set, Meshify renders both directives. Meshify does not copy static file contents; publish them with the same release process that installs the app binary.

When `nginx.http2` is true or any static location sets `gzip_static: true`, app deploy checks `nginx -V` before writing runtime files. `http2 on;` requires Nginx `1.25.1` or newer and the `http_v2` module.

#### GoAccess App Log Dashboard

`nginx.goaccess` is optional and default-off. When enabled, it provides a basic-auth protected real-time HTML dashboard at `https://<primary-domain>/_meshify/apps/<app-name>/goaccess` by default. Meshify installs or checks `goaccess`, renders the GoAccess config, `<app-name>-goaccess.service`, the report at `/var/lib/<app-name>/goaccess/report.html`, and the GoAccess `db-path` at `/var/lib/<app-name>/goaccess/db` with `persist true` and `restore true`; when the access log is Meshify-managed, Meshify also installs `logrotate`.

Enabled GoAccess is always a real-time HTML dashboard in this integration. Nginx serves `/var/lib/<app-name>/goaccess/report.html`; the GoAccess service supplies live updates through its loopback WebSocket, and Meshify proxies that channel at `nginx.goaccess.websocket_path`. Meshify does not expose a static-only GoAccess mode.

```yaml
nginx:
  access_log: ""
  error_log: "/var/log/nginx/example-app.error.log"
  goaccess:
    enabled: true
    language: "en" # en | zh-CN
    log_format: "enhanced" # enhanced | combined
    auth_basic_user_file: "/etc/example-app/goaccess.htpasswd"
    auth_cidr_allowlist: [] # optional CIDRs; basic auth is still required
    # path: "/_meshify/apps/example-app/goaccess"
    # websocket_path: "/_meshify/apps/example-app/goaccess/ws"
    websocket_listen: "" # usually leave empty; defaults to 127.0.0.1:<app-derived-port>
```

Before enabling GoAccess, create the htpasswd file. It must be an existing regular, non-empty file with at least one `user:hash` credential line whose user and hash contain no whitespace. Keep it root-owned, not group-writable, not accessible by other local users, and readable by the Nginx runtime user. Every parent directory for `auth_basic_user_file` must be root-owned, not group/other writable, and searchable by the Nginx runtime user. When it is under `/etc/<app-name>`, use only the direct `/etc/<app-name>/goaccess.htpasswd` path.

When `auth_cidr_allowlist` is empty, only basic auth is required. When it is non-empty, Nginx requires both a matching source IP and successful basic auth. It is not an IP allowlist that bypasses the password check.

On Debian/Ubuntu, create it with `htpasswd` from `apache2-utils`:

```bash
sudo apt update
sudo apt install -y apache2-utils
sudo install -d -o root -g root -m 0755 /etc/example-app
sudo htpasswd -c -m /etc/example-app/goaccess.htpasswd goaccess
sudo chown root:www-data /etc/example-app/goaccess.htpasswd
sudo chmod 0640 /etc/example-app/goaccess.htpasswd
```

Use `-c` only when creating the file for the first time; remove `-c` when adding or updating users, otherwise existing credentials are replaced:

```bash
# Do not use -c when adding or updating users, because it replaces the existing htpasswd file.
sudo htpasswd -m /etc/example-app/goaccess.htpasswd another-user
```

Meshify does not ship a copyable htpasswd template, and you should not reuse example hashes; this file is the dashboard password database and must be generated for each deployment.

GoAccess also needs the system locales selected by `nginx.goaccess.language`. Debian/Ubuntu normally provides `C.UTF-8`; when using `language: "zh-CN"`, generate `zh_CN.UTF-8` before deploy:

```bash
sudo apt install -y locales
sudo sed -i 's/^# *zh_CN.UTF-8 UTF-8/zh_CN.UTF-8 UTF-8/' /etc/locale.gen
sudo locale-gen zh_CN.UTF-8
locale -a | grep -Ei '^(C|C\.utf8|zh_CN\.utf8|zh_CN\.UTF-8)$'
```

Keep the login shell locale valid too. For example, if `env` shows `LANG=en_US.UTF-8`, `locale -a` must list `en_US.utf8`; otherwise generate it or switch the host default to `C.UTF-8` with `sudo update-locale LANG=C.UTF-8`. Meshify runs GoAccess `--version`, `--help`, and compatibility probes under `LANG=C LC_ALL=C` so dependency checks do not depend on the operator's SSH locale. The deployed GoAccess systemd service still uses the UTF-8 locale selected by `nginx.goaccess.language`.

Key rules:

| Item | Rule |
| --- | --- |
| `nginx.access_log` | Cannot be `off` when GoAccess is enabled; see the log path table above for other behavior |
| External `access_log` safety | Must already exist, be a regular non-symlink file, be owned by root or www-data, not be group/other writable, and have root-owned parent directories that are not group/other writable. Meshify does not create them, chown foreign log roots, or install managed logrotate. During deploy, Meshify creates or confirms the GoAccess runtime identity before the final readability check |
| Forbidden paths | Do not place explicit GoAccess access logs under another app's `/var/log/meshify/apps/`, a nested path under the current app, `/home`, `/root`, `/run/user`, `/tmp`, `/var/tmp`, or `/var/log/nginx`; `ProtectHome=true` and `PrivateTmp=true` hide the private paths, and distro logrotate commonly owns `/var/log/nginx/*.log` |
| `error_log` | GoAccess does not parse it; it must not equal the GoAccess canonical access log or htpasswd file, and must stay outside Meshify-managed app and GoAccess runtime paths |
| `path` / `websocket_path` | Usually leave empty and let Meshify derive them from `app.name`; update bookmarks and monitoring checks if you set them explicitly |
| `websocket_listen` | Usually leave empty; if set, it must be a loopback IP literal, not `localhost`, a wildcard, or a conflicting port. Other loopback IP literals are valid |
| `language` | `en` uses `C.UTF-8`; `zh-CN` uses `zh_CN.UTF-8` for GoAccess UI text and keeps `LC_TIME=C.UTF-8` for parsing. Deploy checks `locale -a` and fails early if the required locale is missing. Language changes only GoAccess UI text; raw log fields stay unchanged |
| `log_format` | `enhanced` is the default and includes Host plus request serving time; `combined` is the compatibility mode without serving-time metrics |

The GoAccess dashboard is primary-domain only. Dashboard requests on secondary domains redirect to `https://<primary-domain><nginx.goaccess.path>`, and WebSocket requests on secondary domains return `421`. Static locations with `access_log: false` stay out of GoAccess. GoAccess covers request volume, visitors, URLs, status codes, IP/Host, referrer, User-Agent, bandwidth, and visit time; `enhanced` mode also includes request serving time.

After deploy or refresh, check:

```bash
meshify app verify --config meshify-apps/example-app.yaml
sudo nginx -t
systemctl status example-app-goaccess.service --no-pager --full
curl -I https://app.example.com/_meshify/apps/example-app/goaccess
```

Use these commands when troubleshooting:

```bash
# Check the GoAccess real-time dashboard service status.
systemctl status <app-name>-goaccess.service --no-pager --full

# Read the latest GoAccess service logs.
journalctl -u <app-name>-goaccess.service -e

# Follow the canonical access log used by GoAccess.
tail -f <canonical-access-log>

# Follow the Nginx error log.
tail -f <error-log>

# Listen mode only: read the local business app service logs.
journalctl -u <app-name>.service -e

# Upstream mode only: check the fixed tailnet upstream from this host.
curl -I http://<app.upstream>
```

Use the configured `nginx.access_log` path for `<canonical-access-log>`, or `/var/log/meshify/apps/<app-name>/access.log` when `nginx.access_log` is empty. Use the configured `nginx.error_log` path for `<error-log>`, or Nginx's default error log when `nginx.error_log` is empty.

#### App Verify And Status

`meshify app verify` is a static config/template check for the app workflow. A passing run prints `static-passed`. It validates schema, template rendering, Nginx Host/SNI guards, certificate paths, systemd planning, Tailscale requirement inference, GoAccess dashboard/WebSocket runtime files when enabled, and sensitive-value leakage. It does not read deployed host files, systemd state, certificate SANs, Nginx runtime state, GoAccess process state, or Tailscale online state. `meshify app deploy` also runs the same kind of static checks.

The main `meshify status` command reads the main deployment checkpoint, activation history, and last recoverable failure. The first app release has no separate checkpoint store, so there is no `meshify app status`.

The release binary's app runtime templates only come from `deploy/templates/app/`, and `meshify app deploy` renders and installs them automatically.

#### App Pre-Deploy Checklist

- `app.domains` resolve to the current cloud server and do not reuse the main Headscale `server_url` host.
- For app sites, only public `80/tcp` and `443/tcp` are exposed through Nginx; app ports such as `18001` are not public. If the same host also runs the main Headscale deployment, `3478/udp` is still required for STUN.
- If the same host also manages the main Headscale deployment, `app.listen` and `nginx.goaccess.websocket_listen` must not reuse the Headscale metrics port.
- In `listen` mode, the business binary is installed and the first token of `service.exec_start` is an absolute executable path.
- If `service.env_file` is set, it points to a root-owned, root-only file.
- If `nginx.goaccess.enabled` is true, prepare the htpasswd file as described above; any explicit external `nginx.access_log` must already exist and satisfy the external log safety requirements.
- `nginx.static_locations` aliases point at files or directories published with the app release.
- In `upstream` mode, the backend is a fixed `100.64.x.y:port` and is reachable from the cloud server.
- DNS-01 `dns01.env_file` and Tailscale `tailscale.auth_key_file` are root-only files.

Use `curl`, `nginx -t`, certificate inspection, and `systemctl` for deployed host-state validation. Use `tailscale status` only for `upstream` mode or a `listen` app with `tailscale.enabled_for_listen: true`. `upstream` mode has no local app service, so skip the `<app-name>.service` check and verify the fixed tailnet upstream from the cloud server instead.

### Security Boundaries

- The Go CLI is the only intended user-facing server entrypoint.
- Public HTTP and HTTPS terminate at Nginx. Headscale control-plane traffic does not bind to a public interface.
- Explicit HTTP and HTTPS `default_server` catch-all blocks reject unmatched Host or SNI traffic instead of proxying it to Headscale.
- Headscale administration stays local through the unix socket; do not expose remote gRPC or API-key management unless you intentionally add it.
- DNS-01 provider values must stay outside `meshify.yaml`, rendered templates, deploy output, status output, and systemd units.
- App ports should only listen on loopback or be fixed tailnet upstreams; do not expose private app ports directly to the public internet.

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
- Offline lego mode requires `advanced.lego_source.file_path` to point at the exact pinned lego v5.1.0 archive for `advanced.platform.arch`.
- Existing certificates created with lego v4 are migrated automatically before issuance or renewal. If migration fails, inspect the reported lego data path, fix permissions or unexpected files, and rerun deploy.

Runtime failures:

- Headscale should listen on `127.0.0.1:8080`.
- Metrics should listen on `127.0.0.1:<advanced.headscale.metrics_port>`; the default is `19090`.
- gRPC should listen on `127.0.0.1:50443`.
- `3478/udp` must stay available for STUN when embedded DERP is enabled.
- For Headscale start failures, inspect `systemctl status headscale.service --no-pager --full` and `/etc/headscale/config.yaml`.
- For Nginx failures, run `nginx -t` and check `/etc/nginx/sites-available/headscale.conf`.
- If login or DERP breaks behind Nginx, confirm HTTP/1.1 Upgrade and Connection headers are still forwarded.

App runtime failures:

- In `listen` mode, confirm the business binary exists, is executable, and is actually listening on `app.listen`.
- In `listen` mode, inspect `systemctl status <app-name>.service --no-pager --full`.
- In `upstream` mode, first test whether the fixed upstream is reachable from the cloud server.
- For app certificate or proxy issues, run `nginx -t` and inspect `/etc/nginx/sites-available/<app-name>.conf`.
- For static file 404s, confirm the `nginx.static_locations` `alias` files were published by the app release process.

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

Graphical flow for an already-authenticated App Store or standalone client: select the Tailscale menu bar icon, open Settings, choose Accounts, select the down arrow in the lower-left corner, enter `server_url`, then add the account. On a fresh client with no existing tailnet account, use the CLI flow.

CLI flow when the installed build provides `tailscale`:

```bash
tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true --hostname=laptop
tailscale set --hostname=<name>
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
sudo tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true --hostname=laptop
tailscale set --hostname=<name>
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
