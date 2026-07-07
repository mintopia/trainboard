# 0004 — Plain-HTTP admin UI on the trusted LAN

Date: 2026-07-06
Status: Accepted

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
