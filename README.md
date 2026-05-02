# Meshify

Meshify is a Go CLI for deploying a small, single-host Headscale server. It
turns one config file into a Debian or Ubuntu host running Headscale behind
Nginx TLS, with embedded DERP/STUN, MagicDNS, an initial preauth key, and
static verification checks.

The intended operator path is:

```bash
meshify init --config meshify.yaml
meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
```

Use `meshify status --config meshify.yaml` for a read-only summary of config
validity, deploy checkpoints, warnings, and the last recoverable failure.

## Current Scope

| Area | Supported baseline |
| --- | --- |
| Server OS | Debian 13, Ubuntu 24.04 LTS |
| Server components | Headscale v0.28.0, Nginx, certbot, systemd |
| Client guides | Windows, macOS, Debian/Ubuntu Linux |
| Client baseline | Tailscale client >= v1.74.0 |

Meshify is intentionally narrow for the first release. It does not include
multi-host high availability, Kubernetes, Terraform, Ansible, a Headscale Web
UI, OIDC/SSO, automatic SQLite backup/restore orchestration, remote gRPC/API-key
management by default, or official DERP fallback.

## Quick Start

Build the CLI:

```bash
make build
```

On the target server, prepare public DNS for the Headscale host, allow
`80/tcp`, `443/tcp`, and `3478/udp`, then create and review the config:

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
Most first deployments should only edit the `default` section:

- `server_url`
- `base_domain`
- `certificate_email`
- `acme_challenge`

Use `meshify init --advanced --config meshify.yaml` only when you need DNS-01,
package mirrors, offline packages, proxies, architecture overrides, or public
IP overrides. Do not put DNS provider credentials, API tokens, or other secrets
in repository templates or public config examples; supply provider credentials
through the host environment.

## Documentation

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
