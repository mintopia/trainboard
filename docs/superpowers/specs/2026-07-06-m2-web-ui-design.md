# M2 — Config Web UI (design)

**Date:** 2026-07-06
**Status:** Approved for planning (auth/port/preview/scope confirmed by Jess 2026-07-06)
**Milestone:** M2 — Config Web UI
**Closes:** #1 (embedded server + JSON endpoints), #2 (security), #3 (static-first UI), #4 (design pass)
**Explicitly open:** #5 (SPA fallback) — exercised only if the static UI can't hit the design bar.
**Depends on (merged):** M1 (`display`, `render`, `board`, `data`, `config`, `runtime`, `obs`, `cmd/trainboard`)

## Overview

A phone-first admin UI embedded in the trainboard binary: manage board config and the
Darwin token, see live status (including the OLED's actual frame), and trigger actions —
all server-rendered (`html/template` + htmx), with JSON endpoints from day one so an SPA
pivot stays cheap. Security is not optional: session auth, CSRF, Origin/Host checks,
rate limiting, redacted logs.

## Locked decisions

- **Auth: session login page.** First boot (no password set) forces a `/setup` page that
  sets the admin password (min 8 chars), stored as an **argon2id** hash in config
  (`web.passwordHash`). Then a login form → HttpOnly, SameSite=Strict session cookie;
  in-memory session store (device reboot = re-login; fine). Logout button. Setup and
  login are rate-limited. AP provisioning credentials (M3) are a **separate** config
  field, never the admin password.
- **Port 80** (`http://trainboard.local/`); plain HTTP on the trusted LAN is the
  documented accepted residual risk (ADR to record this), mitigated by auth + CSRF +
  Origin/Host checks + rate limiting + write-only secrets.
- **Live panel preview: yes.** `/preview.png` serves the current frame; the status page
  polls it (~1s htmx). In production the runtime flushes to **both** the SPI panel and
  the in-memory preview (tee Flusher in `cmd/trainboard`); preview-only on host.
- **Static-first, no SPA.** Issues #1–#4 only.

## Package: `internal/web`

| Piece | Responsibility |
|---|---|
| `Server` | `http.Server` on `:80` (flag-overridable for dev/tests), stdlib `ServeMux` patterns, graceful shutdown on ctx cancel. |
| service layer | Config read/update (Validate + transactional Save), status assembly, actions. Handlers stay thin; the service is what a future SPA/API reuses. |
| templates/static | `go:embed`ded `html/template` pages + one CSS file + htmx (vendored, no CDN). Amber-on-black, phone-first. |
| middleware | session auth → CSRF (per-session token, all POSTs) → Origin/Host check (reject cross-origin state changes) → rate limit (token bucket: 5/min on auth endpoints, 30/min on state-changing) → redacted request logging to `obs`. |

### Integration seams (existing code touched)

- **`cmd/trainboard`:** start `web.Server` alongside poller+loop; tee Flusher (panel +
  preview sink) under `--production`; pass the obs ring, a snapshot source, config path,
  and an `apply` callback to the server.
- **`internal/config`:** new `WebConfig { PasswordHash string; SetupToken … }` +
  `ProvisioningConfig { APPassword string }` fields (versioned doc stays v1 — new fields
  default empty; Validate rules added). Wifi desired-credentials fields (`wifi.ssid`,
  write-only `wifi.psk`) stored now, **applied by M3** (documented as inert in M2).
- **`cmd/trainboard/preview.go`:** preview sink additionally keeps the latest PNG in
  memory behind a getter for `/preview.png` (no disk round-trip for the web path).
- **`internal/obs`:** none (ring accessor already exists).

### Apply model (config changes)

**Apply-by-restart.** Saving config responds 200 with a "restarting…" page, then the
process exits cleanly (~500ms later, response flushed); systemd `Restart=always` brings
it back with the new config (~seconds; board shows Initialising). No in-process reload
plumbing in M2. Dev mode without systemd simply exits — documented. Reboot action:
`systemctl reboot` equivalent via `exec.Command`, auth+CSRF-gated, confirmation dialog.
Update action: **disabled placeholder** (M5).

## Pages & endpoints

| Page | Content |
|---|---|
| `/setup` (first boot only) | set admin password |
| `/login`, `/logout` | session |
| `/` status | IP(s), version, uptime, connectivity/board state, last fetch, live preview, recent events (ring, newest first) |
| `/config` | board settings (origin/destination CRS, platforms, TOCs, services, refresh, cutoff, time window, layout.times, replacements), powersaving schedule, Darwin token (**write-only**: never echoed, blank = keep), wifi credentials (write-only PSK; "applied after provisioning support lands" note), regenerate AP password |
| `/actions` (or on status) | restart, reboot (confirm), update (disabled) |

JSON (same service, same auth/CSRF via header token): `GET /api/status`,
`GET /api/config` (redacted), `PUT /api/config`, `GET /api/events`, `POST /api/actions/restart|reboot`.
`GET /preview.png` (auth-gated like everything else).

## Security invariants (testable)

1. Every route except `/setup` (pre-password), `/login`, and static assets 302s to login
   without a valid session.
2. All state-changing requests require a valid CSRF token AND a same-host Origin (or no
   Origin + same Host); failures are 403 + obs event.
3. Darwin token and wifi PSK never appear in any response body, template, JSON, or log
   (config's existing redaction reused; `GET /api/config` returns the Redacted form).
4. Rate limits return 429 and log; auth endpoints back off.
5. Session cookies: HttpOnly, SameSite=Strict, 7-day expiry, rotated on login.
6. Password hashing: argon2id (`golang.org/x/crypto/argon2` — one new dependency,
   x/crypto is already an indirect dep via periph? if not: accepted, it's supply-chain
   trivial and vendored by Go modules).

## Design pass (#4)

After the functional UI lands: a dedicated design task applies the impeccable-style
checklist (typography, spacing, hierarchy, color, states) to the templates/CSS — amber
(#FFB000-ish) on black, Dot-Matrix-flavoured headings (system font stack otherwise),
dark-only theme, honest to the physical board's aesthetic. Verified by rendering pages
and eyeballing screenshots/HTML; the in-browser audit is the final acceptance step (Jess).

## Testing strategy (host-only, TDD)

- Service + middleware: unit tests (httptest) for every security invariant above.
- Handlers: table-driven httptest — auth flows (setup→login→session), config round-trip
  (save → Validate → file content), write-only token semantics (blank keeps old, set
  replaces, never echoed), actions gated.
- E2E: full server against fixture runtime sources (fake snapshot fn, real ring, temp
  config file); scripted: setup → login → change refresh → apply callback fired.
- Gate: `make check` (vet + lint + `-race` tests), linux/arm64 build.

## Out of scope

- Applying wifi credentials / AP mode / captive portal — M3 (fields stored, inert).
- Self-update — M5 (button placeholder).
- SPA (#5) — only if the static UI fails the design bar.
- HTTPS/TLS — documented residual risk on trusted LAN (revisit if M3 changes exposure).
