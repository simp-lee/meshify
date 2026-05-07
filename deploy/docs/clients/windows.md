# Windows Client Guide

## Preparation

- Use Windows 10 or Windows 11 with permission to install VPN software.
- Install Tailscale client >= v1.74.0.
- Get three values from the operator: `server_url`, a preauth key, and the
  MagicDNS suffix.
- Make sure outbound HTTPS to `server_url` is not blocked.

## Install Tailscale

Install Tailscale from the official Windows installer or the Microsoft Store.
After installation, open an elevated PowerShell for the first custom
login-server setup.

## Join The Tailnet

Replace the sample server and key with the operator-provided values:

```powershell
"C:\Program Files\Tailscale\tailscale.exe" up --login-server https://hs.example.com --auth-key <preauth-key> --accept-dns=true
```

If you need platform-specific help from the server, open
`https://hs.example.com/windows` and follow the Headscale-provided Windows
guidance.

## Verify Connectivity

```powershell
"C:\Program Files\Tailscale\tailscale.exe" status
"C:\Program Files\Tailscale\tailscale.exe" ping peer-name.tailnet.example.com
"C:\Program Files\Tailscale\tailscale.exe" netcheck
```

Success means the client is online, MagicDNS resolves peer names, and peer
traffic works. Direct paths are preferred when UDP traversal works. DERP paths
are acceptable when UDP is blocked, as long as peer traffic still succeeds.

For deeper DERP troubleshooting, run the optional debug command:

```powershell
"C:\Program Files\Tailscale\tailscale.exe" debug derp-map
```

It should show only the self-hosted DERP region.

## Daily Operations

```powershell
"C:\Program Files\Tailscale\tailscale.exe" down
"C:\Program Files\Tailscale\tailscale.exe" up --login-server https://hs.example.com --accept-dns=true
```

- Upgrade by installing the newer official package or updating through the
  Microsoft Store.
- Uninstall from Windows Settings when the device should leave the tailnet
  permanently.
- Ask the operator for a fresh preauth key only when re-authenticating a
  logged-out client or joining a new device.

## Common Issues

- Windows Firewall or endpoint protection blocked the network adapter.
- The wrong `server_url` or expired preauth key was used.
- DNS resolution fails because managed DNS was not accepted.
- The client remains on DERP because the current network blocks UDP direct
  connectivity.
- Captive portal probing may behave differently because embedded DERP does not
  provide `/generate_204`; validate real `status`, `ping`, MagicDNS, and
  `netcheck` output before treating that as a deployment failure.

For the shared operator handoff, see [../onboarding.md](../onboarding.md). For
deployment-side failures, see [../troubleshooting.md](../troubleshooting.md).
