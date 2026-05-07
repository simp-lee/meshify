# Deploy Assets

`deploy/` is Meshify's source asset tree. Release builds embed this directory,
then `meshify deploy` renders or installs the runtime files on the target host.

This directory is maintainer-facing. It is not a target-host checkout plan, a
bootstrap script collection, or a shell workflow. The public operator workflow
is documented in [`docs/quickstart.md`](docs/quickstart.md) and stays centered
on `init -> deploy -> verify`, with `meshify status` as a read-only follow-up.

## Layout

```text
deploy/
  README.md
  config/
    meshify.yaml.example
  docs/
    quickstart.md
    architecture.md
    onboarding.md
    troubleshooting.md
    clients/
      windows.md
      macos.md
      debian-ubuntu-linux.md
  templates/
    etc/
      headscale/
        config.yaml.tmpl
        policy.hujson
      systemd/
        system/
          meshify-lego-renew.service.tmpl
          meshify-lego-renew.timer
      nginx/
        sites-available/
          headscale.conf.tmpl
    usr/
      local/
        lib/
          meshify/
            hooks/
              install-lego-cert-and-reload-nginx.sh
```

## Source Roles

| Path | Role |
| --- | --- |
| `config/meshify.yaml.example` | Minimal config example plus opt-in advanced settings. |
| `docs/quickstart.md` | Default operator workflow and first-release support matrix. |
| `docs/architecture.md` | Single-host topology, security boundary, and network behavior. |
| `docs/onboarding.md` | Shared client handoff and fresh preauth-key flow. |
| `docs/troubleshooting.md` | Recovery guidance by failed CLI command or symptom. |
| `docs/clients/*.md` | Platform-specific client setup and validation steps. |
| `templates/etc/headscale/config.yaml.tmpl` | Rendered Headscale runtime config. |
| `templates/etc/headscale/policy.hujson` | Direct-copy allow-all ACL baseline. |
| `templates/etc/nginx/sites-available/headscale.conf.tmpl` | Rendered Nginx TLS and reverse-proxy site. |
| `templates/etc/systemd/system/meshify-lego-renew.service.tmpl` | Rendered lego renewal service with optional DNS-01 environment. |
| `templates/etc/systemd/system/meshify-lego-renew.timer` | Direct-copy lego renewal timer. |
| `templates/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh` | Direct-copy lego certificate install and reload hook. |

## Host Outputs

| Source asset | Host destination |
| --- | --- |
| `templates/etc/headscale/config.yaml.tmpl` | `/etc/headscale/config.yaml` |
| `templates/etc/headscale/policy.hujson` | `/etc/headscale/policy.hujson` |
| `templates/etc/nginx/sites-available/headscale.conf.tmpl` | `/etc/nginx/sites-available/headscale.conf` |
| `templates/etc/systemd/system/meshify-lego-renew.service.tmpl` | `/etc/systemd/system/meshify-lego-renew.service` |
| `templates/etc/systemd/system/meshify-lego-renew.timer` | `/etc/systemd/system/meshify-lego-renew.timer` |
| `templates/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh` | `/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh` |

Markdown docs and config examples stay in the repository as source artifacts.
They are embedded for reference, but they are not installed as runtime state on
the host.

## Maintenance Rules

- Keep first-use config inputs small. Mirror, offline package, proxy, DNS-01
  provider, architecture, and public IP overrides belong in `advanced`.
- Never commit DNS-01 credentials, API tokens, private keys, or provider secrets
  to templates or public examples.
- Keep docs aligned with `meshify init`, `meshify deploy`, `meshify verify`, and
  `meshify status`.
- Keep host paths in this document aligned with `internal/assets/catalog.go`.
- Do not reintroduce bootstrap wrappers, render scripts, validate scripts,
  repository-in-place host workflows, or shell orchestration. Shell assets must
  stay limited to small runtime hooks for host-native certificate installation
  and service reload boundaries.
- Preserve the first-release matrix: Debian 13 and Ubuntu 24.04 LTS servers;
  Windows, macOS, and Debian/Ubuntu Linux client guides; Tailscale client >=
  v1.74.0.
