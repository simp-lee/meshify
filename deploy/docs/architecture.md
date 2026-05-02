# Architecture

Meshify targets a single-host deployment that exposes one public Headscale endpoint, one embedded DERP region, and one novice-oriented CLI workflow.

## Runtime Topology

```text
                Internet
                   |
        80/tcp and 443/tcp to Nginx
                   |
        +---------------------------+
        |        Debian/Ubuntu      |
        |                           |
        |  Nginx TLS termination    |
        |    -> localhost:8080      |
        |                           |
        |  Headscale control plane  |
        |    listen_addr 127.0.0.1  |
        |    grpc/metrics loopback  |
        |    unix socket admin      |
        |                           |
        |  Embedded DERP + STUN     |
        |    3478/udp               |
        +---------------------------+
                   |
        Clients prefer direct WireGuard
        and fall back to DERP over 443
```

## Product Boundary

- The Go CLI is the only intended user-facing entrypoint.
- `deploy/` holds source assets only: config example, operator docs, and install-time templates.
- The release build embeds this tree into the CLI so the host does not depend on a repo checkout.
- Shell is not a public workflow layer. The only retained shell asset is the certbot deploy hook that reloads Nginx after renewal.

## Template Boundary

- [../templates/etc/headscale/config.yaml.tmpl](../templates/etc/headscale/config.yaml.tmpl) keeps Headscale on loopback, enables embedded DERP, disables external DERP maps, disables logtail, and points policy loading at `policy.hujson`.
- [../templates/etc/headscale/policy.hujson](../templates/etc/headscale/policy.hujson) carries the default allow-all ACL baseline.
- [../templates/etc/nginx/sites-available/headscale.conf.tmpl](../templates/etc/nginx/sites-available/headscale.conf.tmpl) provides the reverse proxy contract for TLS, HTTP-01 reachability, and WebSocket or upgrade forwarding.
- [../templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh](../templates/etc/letsencrypt/renewal-hooks/deploy/reload-nginx.sh) is the minimal runtime hook used after certbot renewals.

## Support Matrix

- Server deployment scope: Debian 13 and Ubuntu 24.04 LTS
- Client documentation scope: Windows, macOS, Debian/Ubuntu Linux
- Tailscale client baseline: v1.74.0 or newer

## Network Behavior

- The cloud server is the Headscale control plane and the self-hosted DERP fallback relay.
- Clients should prefer direct WireGuard paths when UDP traversal works.
- When UDP traversal is blocked or unstable, clients can fall back to the embedded DERP path over public TCP/443.
- Embedded DERP does not provide `/generate_204`; client validation should use login success, `tailscale status`, `tailscale ping`, MagicDNS resolution, and `tailscale netcheck`.

## Out Of Scope

- Bootstrap, render, or validate wrapper scripts
- Repository-in-place host workflows
- Automatic SQLite backup or restore orchestration
- Official DERP fallback, multi-host topologies, or remote gRPC-by-default management
