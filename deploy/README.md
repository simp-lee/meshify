# Deploy Assets

`deploy/` is Meshify's human-maintained source asset tree. The release binary
embeds this tree, then the CLI renders or installs the runtime assets during
`meshify deploy`.

This directory is not a target-host checkout plan and not a shell workflow. The
public operator workflow is documented in
[`docs/quickstart.md`](docs/quickstart.md) and stays centered on
`init -> deploy -> verify`, with `meshify status` as a read-only follow-up.

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
      nginx/
        sites-available/
          headscale.conf.tmpl
      letsencrypt/
        renewal-hooks/
          deploy/
            reload-nginx.sh
```

## Source Roles

| Path | Role |
| --- | --- |
| `config/meshify.yaml.example` | Minimal config example with optional advanced settings. |
| `docs/quickstart.md` | Default operator workflow and first-release support matrix. |
| `docs/architecture.md` | Single-host topology, security boundary, and network behavior. |
| `docs/onboarding.md` | Shared client handoff and fresh preauth-key flow. |
| `docs/troubleshooting.md` | Recovery guidance by failed CLI command or symptom. |
| `docs/clients/*.md` | Platform-specific client setup and validation steps. |
| `templates/etc/headscale/config.yaml.tmpl` | Rendered Headscale runtime config. |
| `templates/etc/headscale/policy.hujson` | Direct-copy allow-all ACL baseline. |
| `templates/etc/nginx/sites-available/headscale.conf.tmpl` | Rendered Nginx TLS and reverse-proxy site. |
| `templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh` | Direct-copy certbot deploy hook. |

## Host Outputs

| Source asset | Host destination |
| --- | --- |
| `templates/etc/headscale/config.yaml.tmpl` | `/etc/headscale/config.yaml` |
| `templates/etc/headscale/policy.hujson` | `/etc/headscale/policy.hujson` |
| `templates/etc/nginx/sites-available/headscale.conf.tmpl` | `/etc/nginx/sites-available/headscale.conf` |
| `templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh` | `/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh` |

Markdown docs and config examples stay in the repository as source artifacts.
They are embedded for reference, but they are not installed as runtime state on
the host.

## Maintenance Rules

- Keep first-use config inputs small and keep mirror, offline package, proxy,
  DNS-01 provider, architecture, and public IP overrides in the advanced
  section.
- Never commit DNS-01 credentials, API tokens, private keys, or provider secrets
  to templates or public examples.
- Keep docs aligned with `meshify init`, `meshify deploy`, `meshify verify`, and
  `meshify status`.
- Do not reintroduce bootstrap wrappers, render scripts, validate scripts,
  repository-in-place host workflows, or shell orchestration. The only shell
  asset kept here is the certbot hook that reloads Nginx after renewal.
- Preserve the first-release matrix: Debian 13 and Ubuntu 24.04 LTS servers;
  Windows, macOS, and Debian/Ubuntu Linux client guides; Tailscale client >=
  v1.74.0.
