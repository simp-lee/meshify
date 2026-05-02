# Quickstart

Meshify's default path is one config file and three core commands:

```bash
meshify init --config meshify.yaml
meshify deploy --config meshify.yaml
meshify verify --config meshify.yaml
```

Use `meshify status --config meshify.yaml` whenever you want a non-mutating summary of config readiness, persisted deploy checkpoints, warnings, and the last recoverable failure.

## Support Matrix

| Area | First release support |
| --- | --- |
| Server OS | Debian 13, Ubuntu 24.04 LTS |
| Server components | Headscale v0.28.0, Nginx, certbot, systemd |
| Client docs | Windows, macOS, Debian/Ubuntu Linux |
| Client baseline | Tailscale client >= v1.74.0 |

Other server distributions and client platforms are outside the first release support matrix.

## Before You Start

- Point the public DNS record for the Headscale server host, such as `hs.example.com`, at the target server.
- Pick a MagicDNS base domain, such as `tailnet.example.com`, that is not equal to the Headscale host and is not its parent domain.
- Allow `80/tcp`, `443/tcp`, and `3478/udp` in the host firewall and cloud security group.
- Confirm root or passwordless sudo access on the server.
- Confirm the server can obtain the Headscale v0.28.0 `.deb` directly, from a mirror, or from an offline file with a SHA-256 digest.
- Prepare at least two clients in different network environments for final validation.
- For China mainland public deployments, confirm ICP/hosting access requirements, cloud ingress rules, package reachability, and whether HTTP-01 is practical. Use DNS-01, a mirror, an offline package, or proxy settings when needed.

## Minimal Inputs

Default guided mode asks for:

- `server_url`
- `base_domain`
- `certificate_email`
- `acme_challenge`

Keep `acme_challenge: "http-01"` unless public port 80 or local policy makes DNS-01 safer. Advanced guided mode is opt-in and adds DNS-01 provider selection, package mirror/offline inputs, proxy values, architecture, and public IP overrides.

## Deploy Flow

1. Run `meshify init --config meshify.yaml` and answer the guided prompts.
2. Review `meshify.yaml`. Most first deployments only need the `default` section. Use `meshify init --example --config meshify.yaml` only when you need a non-interactive starting file.
3. Run `meshify deploy --config meshify.yaml` on the target server.
4. Read the deploy output. It records checkpoints for package setup, Headscale install, runtime assets, certificate issuance, Nginx activation, service enablement, onboarding, and static verification. If a step fails, rerun the same deploy command after fixing the issue.
5. Run `meshify verify --config meshify.yaml` to re-check rendered Headscale, ACL, Nginx, TLS hook, certificate plan, onboarding readiness, and the Tailscale client version baseline.
6. Run `meshify status --config meshify.yaml` if you need the persisted checkpoint, warning, or failure summary.
7. Follow [onboarding.md](onboarding.md), then the matching client guide:
   - [Windows](clients/windows.md)
   - [macOS](clients/macos.md)
   - [Debian/Ubuntu Linux](clients/debian-ubuntu-linux.md)

## Expected Result

- Headscale listens only on loopback for the control plane and local auxiliary listeners.
- Nginx owns public `80/tcp` and `443/tcp`, serves HTTP-01 challenges, terminates TLS with the full chain, and forwards HTTP/1.1 upgrade traffic for control and DERP WebSocket paths.
- Headscale exposes STUN on `3478/udp` and uses only the embedded DERP region. `derp.urls` remains empty.
- The first local Headscale user and preauth key are created through local CLI management over the unix socket.
- Two clients from different networks can join, resolve MagicDNS names, reach each other, and show either direct paths or DERP fallback when UDP direct connectivity is unavailable.

## Scope Boundaries

- No bootstrap, render, or validate shell scripts are part of the user workflow.
- No automatic SQLite backup, restore orchestration, Terraform, Ansible, Kubernetes, Web UI, OIDC, or remote gRPC management is included.
- The cloud server is the control plane and fallback DERP relay. It is not meant to force all peer traffic through the server when direct WireGuard paths are available.
