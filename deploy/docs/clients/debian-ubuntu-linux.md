# Debian And Ubuntu Linux Client Guide

## Preparation

- Use Debian or Ubuntu with sudo access and a working `/dev/net/tun`.
- Install Tailscale client >= v1.74.0.
- Get three values from the operator: `server_url`, a preauth key, and the
  MagicDNS suffix.
- Ensure the host can reach the self-hosted control plane over outbound HTTPS.

## Install Tailscale

Use the official Tailscale package feed for your distribution, or install a
package file supplied by the operator. Confirm the daemon is running:

```bash
systemctl status tailscaled --no-pager --full
```

## Join The Tailnet

```bash
sudo tailscale up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true
```

If the operator uses an interactive login instead of a preauth key, follow the
printed sign-in URL from the same command.

## Verify Connectivity

```bash
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
```

Success means the node is online, MagicDNS resolves peer names, and peer traffic
works. Direct paths are preferred when UDP traversal works. DERP fallback over
TCP/443 is acceptable when UDP direct connectivity is blocked. For deeper DERP
troubleshooting, `tailscale debug derp-map` is an optional debug command and
should show only the self-hosted DERP region.

## Daily Operations

```bash
sudo tailscale down
sudo tailscale up --login-server https://hs.example.com --accept-dns=true
```

- Upgrade Tailscale with the same package source you used for installation.
- Remove the client with your package manager if the device should leave the
  tailnet permanently.
- Ask the operator for a fresh preauth key only when re-authenticating a
  logged-out client or joining a new device.

## Common Issues

- `tailscaled` is not running.
- `/dev/net/tun` is unavailable or blocked by the host environment.
- The wrong `server_url` or auth key was used.
- Managed DNS was not accepted, so MagicDNS names do not resolve.
- The node prefers DERP because UDP direct connectivity is unavailable on the
  current network.
- Embedded DERP does not provide `/generate_204`; validate actual login,
  `status`, `ping`, MagicDNS, and `netcheck` output before treating
  captive-portal probing as the root cause.

For the shared operator handoff, see [../onboarding.md](../onboarding.md). For
deployment-side failures, see [../troubleshooting.md](../troubleshooting.md).
