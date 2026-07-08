# 0004 — Plain-HTTP admin UI on the trusted LAN

Date: 2026-07-06
Status: Accepted (amended 2026-07-09, see Addendum)

## Context
M2 adds an embedded admin web UI (port 80) to a headless appliance on the
owner's home LAN. TLS on a LAN-only device with no stable name has poor
options: self-signed certs train users to click through warnings; per-device
Let's Encrypt needs public DNS; mkcert-style local CAs don't fit an appliance.

## Decision
Serve plain HTTP on the LAN and treat the transport as untrusted-adjacent:
session auth (argon2id-hashed admin password), per-session CSRF tokens,
Origin/Host checks on all state changes, rate limiting, write-only secret
fields, and redaction of all secrets from every response and log. AP-mode
provisioning (M3) uses a separate credential.

## Consequences
- A LAN-resident attacker can sniff the admin password in transit. Accepted
  for a hobbyist appliance; documented so it can be revisited.
- Revisit if M3/M4 changes exposure (AP mode serves the same UI to a
  captive-portal client — the AP is WPA2-protected with the provisioning
  password, keeping the same risk shape).
- If TLS becomes warranted: self-signed with pinning instructions, or
  HTTP-only-on-localhost + SSH tunnel documented as the paranoid path.

## Addendum: setup AP is now open (2026-07-09, M3.5, issue #44)

The "same risk shape" premise in the Consequences section above no longer
holds. Issue #44 (this branch) made the setup AP open — no WPA2, no
provisioning password. Anyone in radio range can now associate with the AP
without a credential. This addendum re-scopes the plain-HTTP acceptance
against that changed exposure.

- **Unprovisioned device, AP fallback:** `/setup` is open with no admin
  password set yet. Anyone in radio range during the provisioning window can
  connect to the open AP and complete first-boot setup themselves, choosing
  their own admin password. Previously this required knowing the WPA2
  provisioning password read off the device's glass; now it requires only
  physical proximity.
- **Provisioned device, AP fallback:** `/config` and all state-changing
  actions remain gated behind the admin password (unchanged — see Decision
  above). The open AP exposes only the read-only status view (SSID,
  LastSTAError) and the captive portal to an unauthenticated LAN-adjacent
  client.

This residual risk is accepted per issue #44: it requires physical
proximity to the device's radio range, the provisioning window is
short-lived (the device returns to AP fallback only when STA association
fails), and re-provisioning an already-provisioned device still requires the
admin password. No further mitigation is planned; revisit if the AP-mode
threat model changes (e.g. longer-lived AP windows, remote AP exposure).
