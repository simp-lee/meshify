# Architecture

Meshify targets one public Headscale endpoint on one Debian 13 or Ubuntu 24.04
LTS host. The CLI owns config generation, host mutation, certificate issuance,
static verification, and Day 1 onboarding.

## Runtime Topology

```text
Internet
  |
  | 80/tcp, 443/tcp
  v
+------------------------------------------------+
| Debian 13 or Ubuntu 24.04 LTS host             |
|                                                |
| Nginx                                          |
|   - HTTP-01 webroot                            |
|   - TLS termination with fullchain.pem         |
|   - reverse proxy to 127.0.0.1:8080            |
|                                                |
| Headscale                                      |
|   - control plane on loopback                  |
|   - metrics and gRPC on loopback               |
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

## Product Boundary

- The Go CLI is the only intended user-facing entrypoint.
- `deploy/` holds source assets only: config example, operator docs, and
  install-time templates.
- The release build embeds this tree into the CLI so the host does not depend on
  a repo checkout.
- Shell is not a public workflow layer. The retained shell asset is the small
  lego certificate hook that installs renewed cert/key material and reloads
  Nginx after validation.

## Template Boundary

- [../templates/etc/headscale/config.yaml.tmpl](../templates/etc/headscale/config.yaml.tmpl)
  keeps Headscale on loopback, enables embedded DERP, disables external DERP
  maps, disables logtail, and points policy loading at `policy.hujson`.
- [../templates/etc/headscale/policy.hujson](../templates/etc/headscale/policy.hujson)
  carries the default allow-all ACL baseline.
- [../templates/etc/nginx/sites-available/headscale.conf.tmpl](../templates/etc/nginx/sites-available/headscale.conf.tmpl)
  provides the reverse proxy contract for TLS, HTTP-01 reachability,
  default-server isolation, and WebSocket or upgrade forwarding.
- [../templates/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh](../templates/usr/local/lib/meshify/hooks/install-lego-cert-and-reload-nginx.sh)
  installs lego-issued certificate material into
  `/etc/meshify/tls/<server>/fullchain.pem` and
  `/etc/meshify/tls/<server>/privkey.pem`, validates Nginx, then reloads it.
- [../templates/etc/systemd/system/meshify-lego-renew.service.tmpl](../templates/etc/systemd/system/meshify-lego-renew.service.tmpl)
  and [../templates/etc/systemd/system/meshify-lego-renew.timer](../templates/etc/systemd/system/meshify-lego-renew.timer)
  own automated lego renewals.

## Support Matrix

- Server deployment scope: Debian 13 and Ubuntu 24.04 LTS
- Client documentation scope: Windows, macOS, Debian/Ubuntu Linux
- Tailscale client baseline: v1.74.0 or newer

## Security Boundary

- Public HTTP and HTTPS terminate at Nginx. Headscale control-plane traffic is
  not bound to a public interface.
- Nginx configured `server_name` blocks serve Meshify's certificate paths.
  Explicit HTTP and HTTPS `default_server` catch-all blocks reject unmatched
  Host or SNI traffic instead of proxying it to Headscale.
- Headscale administration stays local through the unix socket. Remote gRPC and
  API-key management are not the default Day 1 management path.
- DNS-01 provider credentials must stay outside `meshify.yaml`, rendered
  templates, deploy output, and systemd units. Use root-only files and lego
  `_FILE` variables for raw token or key material.

## Network Behavior

- The cloud server is the Headscale control plane and the self-hosted DERP
  fallback relay.
- Clients should prefer direct WireGuard paths when UDP traversal works.
- When UDP traversal is blocked or unstable, clients can fall back to the
  embedded DERP path over public TCP/443.
- Embedded DERP does not provide `/generate_204`; client validation should use
  login success, `tailscale status`, `tailscale ping`, MagicDNS resolution, and
  `tailscale netcheck`.

## Out Of Scope

- Bootstrap, render, or validate wrapper scripts
- Repository-in-place host workflows
- Automatic SQLite backup or restore orchestration
- Official DERP fallback, multi-host topologies, or remote gRPC-by-default management
