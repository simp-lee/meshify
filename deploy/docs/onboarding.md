# Onboarding

Use this guide after `meshify deploy` and `meshify verify` pass on the server.
It defines the shared Day 1 handoff before users follow a platform-specific
client guide.

## Operator Checklist

- Confirm `meshify verify --config meshify.yaml` reports passed checks.
- Confirm the deploy output or `meshify status --config meshify.yaml` shows the
  latest deploy checkpoints and no active failure.
- Keep the Headscale administration path local. The default runtime config uses
  `/var/run/headscale/headscale.sock`; do not expose remote gRPC or API-key
  management for Day 1.
- Hand each client user:
  - `server_url`, for example `https://hs.example.com`
  - the preauth key printed by deploy or created locally
  - the MagicDNS suffix from `base_domain`
  - the matching client guide

## Create A Fresh Preauth Key

`meshify deploy` creates the initial `meshify` user and a preauth key when
Headscale is running. To create another key later, run the local Headscale CLI
on the server:

```bash
sudo headscale --config /etc/headscale/config.yaml users list
# Only if the meshify user is missing from users list:
sudo headscale --config /etc/headscale/config.yaml users create meshify
sudo headscale --config /etc/headscale/config.yaml users list
sudo headscale --config /etc/headscale/config.yaml preauthkeys create --user <ID> --expiration 24h
```

Use the numeric user ID shown by `users list` for the `meshify` user. Use a
short expiration for one-time onboarding. Add the Headscale reusable flag only
when you intentionally want one key for multiple clients.

## Client Handoff Goals

Every supported client should:

- run Tailscale client >= v1.74.0
- join with the supplied `server_url` and preauth key
- accept managed DNS so MagicDNS works
- appear online in `tailscale status`
- reach another node with `tailscale ping`
- show path information with `tailscale netcheck` or equivalent UI output

## Network Validation

Validate at least two clients from different networks, such as home broadband
plus office network, or home broadband plus phone hotspot.

- When UDP direct connectivity works, peer traffic should prefer direct WireGuard paths.
- When UDP direct connectivity is blocked or unstable, peer traffic may use the
  embedded DERP relay over TCP/443. DERP fallback is acceptable when peer traffic
  still works.
- For deeper DERP troubleshooting, `tailscale debug derp-map` is an optional
  debug command and should show only the self-hosted DERP region for this
  deployment.
- The embedded DERP endpoint does not provide `/generate_204`. If a client or
  captive portal check behaves oddly, verify real login, `tailscale status`,
  `tailscale ping`, and MagicDNS before treating that probe as a deployment
  failure.

## Pick A Platform Guide

- [Windows](clients/windows.md)
- [macOS](clients/macos.md)
- [Debian/Ubuntu Linux](clients/debian-ubuntu-linux.md)

## Common Handoff Mistakes

- Giving clients the wrong `server_url`
- Reusing an expired or already-consumed preauth key
- Forgetting to accept DNS management on the client
- Using a Tailscale client older than v1.74.0
- Treating any DERP path as a failure even when UDP direct connectivity is blocked and peer traffic is healthy

For deployment-side issues, return to [quickstart.md](quickstart.md) or
[troubleshooting.md](troubleshooting.md).
