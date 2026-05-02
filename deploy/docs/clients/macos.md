# macOS Client Guide

## Preparation

- Use a supported macOS release with permission to approve VPN prompts.
- Install Tailscale client >= v1.74.0.
- Get `server_url`, a preauth key, and the MagicDNS suffix from the operator.
- Make sure the Mac can reach the server over outbound HTTPS and TCP/443.

## Install Tailscale

Install Tailscale from the Mac App Store or the official macOS package. Approve the VPN and system prompts during first launch.

## Join The Tailnet

App path: launch Tailscale, choose the self-hosted control-server flow if prompted, enter the operator-provided `server_url`, and complete the auth-key or login handoff.

CLI path, when your install channel exposes the `tailscale` command:

```bash
tailscale up --login-server https://hs.example.com --auth-key tskey-example --accept-dns=true
```

## Verify Connectivity

```bash
tailscale status
tailscale ping peer-name.tailnet.example.com
tailscale netcheck
tailscale debug derp-map
```

Success means the client is online, MagicDNS resolves peer names, and peer traffic works. Direct paths are preferred when UDP traversal works. DERP fallback over TCP/443 is acceptable when UDP direct connectivity is blocked. The DERP map should show only the self-hosted DERP region.

## Daily Operations

```bash
tailscale down
tailscale up --login-server https://hs.example.com --auth-key tskey-example --accept-dns=true
```

- Use the app menu to disconnect or reconnect if you prefer the GUI.
- Upgrade through the App Store or by installing the newer package from the same source you used initially.
- Remove the app from Applications if you need to uninstall, then ask the operator to expire the device or key if needed.

## Common Issues

- macOS blocked the VPN permission or system extension prompt.
- A captive portal or restrictive Wi-Fi network delayed connectivity checks even though the tailnet itself works.
- DNS settings were not accepted, so MagicDNS names do not resolve.
- The client stayed on DERP because UDP direct connectivity was unavailable.
- Embedded DERP does not provide `/generate_204`; validate actual login, `status`, `ping`, MagicDNS, and `netcheck` output before treating captive-portal probing as the root cause.

For the shared operator handoff, see [../onboarding.md](../onboarding.md). For deployment-side failures, see [../troubleshooting.md](../troubleshooting.md).
