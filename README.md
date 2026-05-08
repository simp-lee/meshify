# Meshify

Meshify is a Go CLI for deploying a small, single-host Headscale control plane.
It turns one `meshify.yaml` file into a supported Debian 13 or Ubuntu 24.04 LTS
host running Headscale behind Nginx TLS, with embedded DERP/STUN, MagicDNS, a
local onboarding path, and static verification checks.

The operator path is intentionally short:

```bash
meshify init --config meshify.yaml
meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
```

Use `meshify status --config meshify.yaml` for a read-only summary of config
validity, deploy checkpoints, warnings, and the last recoverable failure.

## What Meshify Manages

| Area | Baseline |
| --- | --- |
| Server OS | Debian 13, Ubuntu 24.04 LTS |
| Control plane | Headscale v0.28.0 on loopback behind Nginx |
| TLS automation | HTTP-01 or DNS-01 through a meshify-managed pinned lego v4.35.2 binary |
| Relay | Embedded Headscale DERP and STUN on `3478/udp`; no official DERP fallback |
| Client guides | Windows, macOS, Debian/Ubuntu Linux |
| Client baseline | Tailscale client >= v1.74.0 |

Meshify is intentionally narrow for the first release. It does not include
multi-host high availability, Kubernetes, Terraform, Ansible, a Headscale Web
UI, OIDC/SSO, automatic SQLite backup/restore orchestration, remote gRPC/API-key
management by default, or official DERP fallback.

## Quick Start

Build the CLI from this repository:

```bash
make build
```

On the target server, point public DNS at the host, allow `80/tcp`, `443/tcp`,
and `3478/udp`, then generate a config:

```bash
./meshify init --config meshify.yaml
```

For non-interactive config generation, use:

```bash
./meshify init --example --config meshify.yaml
```

Deploy and verify:

```bash
./meshify deploy --config meshify.yaml
./meshify verify --config meshify.yaml
./meshify status --config meshify.yaml
```

After the server passes verification, follow the onboarding guide and the
matching client guide from the documentation map below.

## Configuration

The config example lives at
[`deploy/config/meshify.yaml.example`](deploy/config/meshify.yaml.example).
Most first deployments only edit the `default` section:

- `server_url`
- `base_domain`
- `certificate_email`
- `acme_challenge`

Use `meshify init --advanced --config meshify.yaml` only for DNS-01, Headscale
package mirrors or offline packages, lego offline archives, proxies,
architecture overrides, or public IP overrides. The server must either reach the
pinned lego v4.35.2 GitHub release archive through normal egress or the
configured proxy, or use `advanced.lego_source.mode: "offline"` with a local
copy of the exact pinned archive for the configured architecture. Headscale
package overrides live under `advanced.headscale_source`.

DNS-01 uses lego provider codes `cloudflare`, `route53`, `digitalocean`, and
`gcloud`; `google` is accepted as an alias for `gcloud`. Cloudflare and
DigitalOcean require a root-only `advanced.dns01.env_file`. Route53 and gcloud
may use lego's ambient credential chain when deploy and
`meshify-lego-renew.service` run with the same host identity. For gcloud ambient
mode, the project must also be available through Google Cloud metadata, or set
`GCE_PROJECT` in the env file. Because systemd environment files are not a
secret store, keep raw DNS tokens and keys in separate root-only files and
reference them with lego `_FILE` variables. Do not put DNS provider credentials,
API tokens, or other secrets in repository templates or public config examples.

## Documentation

- [`deploy/docs/getting-started.zh-CN.md`](deploy/docs/getting-started.zh-CN.md):
  Chinese beginner guide covering prerequisites, config concepts, deployment,
  DNS, ACME, Nginx coexistence, and client onboarding.
- [`deploy/docs/quickstart.md`](deploy/docs/quickstart.md): operator workflow,
  prerequisites, expected result, and scope boundaries.
- [`deploy/docs/architecture.md`](deploy/docs/architecture.md): runtime
  topology and security boundary.
- [`deploy/docs/onboarding.md`](deploy/docs/onboarding.md): shared Day 1 client
  handoff and fresh preauth-key flow.
- [`deploy/docs/troubleshooting.md`](deploy/docs/troubleshooting.md): failure
  routing by config, preflight, package, TLS, service, and client symptoms.
- Client guides:
  [`Windows`](deploy/docs/clients/windows.md),
  [`macOS`](deploy/docs/clients/macos.md),
  [`Debian/Ubuntu Linux`](deploy/docs/clients/debian-ubuntu-linux.md).
- [`deploy/README.md`](deploy/README.md): embedded deployment asset inventory
  for maintainers.
- [`test/e2e/README.md`](test/e2e/README.md): manual release validation gates
  that require real hosts, DNS, ACME, and clients from different networks.

## Development

Meshify requires the Go version declared in [`go.mod`](go.mod).

```bash
make check
```

Useful targets:

- `make build`: build `./cmd/meshify` into `./meshify`
- `make test`: run `go test ./...`
- `make lint`: run `golangci-lint run ./...`
- `make tidy`: run `go mod tidy`

The `deploy/` tree is embedded into the release binary through
[`deploy_embed.go`](deploy_embed.go). Keep user-facing behavior aligned with the
CLI commands `init`, `deploy`, `verify`, and `status`.
