# M3: Connectivity + AP provisioning — design

Date: 2026-07-07
Status: Approved (Jess, 2026-07-07)
Covers issues: #6 #7 #8 #9 #10 #11 #12 #13 · Builds on ADR 0003 (accepted) and PLAN.md §M3.

## Goal

The device self-provisions over its own WiFi hotspot and stays reachable when
the configured WiFi fails: a Connectivity Manager owns wlan0, runs a layered
connectivity check, falls back to AP mode with a captive portal, and retries
the configured network on a tear-down loop with a hard AP-restore invariant.

## Architecture decision inherited from ADR 0003

wpa_supplicant (STA) + dnsmasq (DHCP/captive DNS), NO NetworkManager. The AP
daemon choice is **evaluation-gated**: wpa_supplicant native AP mode
(`mode=2`) is tried first on the real brcmfmac; hostapd is the fallback. The
software isolates this behind a thin `apDriver` seam (two implementations,
3-4 methods) so everything else is driver-agnostic and host-buildable; the
hardware evaluation picks the shipped default and the loser is deleted, not
maintained, once the verdict is recorded.

## Components

### `internal/net` (new package)

- **`Runner`** — command seam: `Run(ctx context.Context, argv ...string)
  (output string, err error)`. Production wraps os/exec; tests use a scripted
  fake (issue #13). Every external side effect (wpa_cli, ip, dhcp client,
  dnsmasq, rfkill, iw) goes through it. No exec anywhere else.
- **`apDriver`** — interface over "make the AP exist / make STA exist":
  `StartAP`, `StopAP`, `AttemptSTA`, `Status`. Implementations: `mode2`
  (single wpa_supplicant, transitions via `wpa_cli select_network`) and
  `hostapd` (daemon start/stop handoff). dnsmasq control is shared, outside
  the driver.
- **`Check`** — the layered connectivity evaluation (below).
- **`Manager`** — the state machine goroutine. Sole owner of wlan0.

### `internal/board`

Hotspot Info scene: SSID, AP password, `http://192.168.4.1`, driven by the
manager's published state (same atomic-snapshot pattern as the poller).
AP mode is a scene, not a fault code.

### `internal/web`

Captive-portal probe endpoints, AP-mode partial `/setup`, "retry WiFi now"
action, last-STA-error display.

### `cmd/trainboard`

Wires manager ↔ web ↔ poller ↔ scenes; sd_notify heartbeat aggregation.

## Manager state machine

States: `Boot → STAConnecting → Online → (degraded) → APFallback ⇄ STARetry`.

- Boot: first-boot prerequisites (#6): `rfkill unblock wifi`, regulatory
  country set (default GB, from config), verify wlan0 exists and is excluded
  from dhcpcd/ifupdown (flash script writes the exclusions; the manager
  *verifies* and surfaces E05-class faults rather than fixing silently).
- No wifi configured → straight to APFallback (first-run).
- STAConnecting runs the layered check with per-layer backoff; full failure
  → APFallback.
- APFallback: AP up + dnsmasq up + portal live. Every **5 minutes**: tear
  down AP → bounded STA attempt (≤45s through the layers) → Online on
  success; on failure **restore the AP and VERIFY it** — beacon present
  (driver Status) AND dnsmasq alive — before declaring fallback restored.
  Verification failure → escalate: restart daemons once, then stop petting
  the watchdog (systemd reboots, boot re-enters APFallback cleanly).
- Retry suppression: skip the 5-min attempt while a provisioning session is
  active — recent DHCP lease AND recent HTTP activity on the AP subnet (both
  within 90s), not mere association. `POST /actions/wifi-retry` ("retry
  now") is always honoured.
- Online: manager keeps watching (assoc lost → re-run layered check →
  eventual APFallback). On transition to Online it pokes the poller for an
  immediate refetch (closes the boot-time live-data gap).

## Layered connectivity check

Association → DHCP lease → DNS resolve (the Darwin hostname) → captive-trap
detection (fetch a known-204 URL; a 200/redirect means a hotel-style captive
network — surfaced as its own stage) → Darwin fetch OK (delegated: the
poller's next success is the signal). Each layer: own timeout + backoff,
own on-screen designation. New fault codes: **E05** = rfkill/regulatory
blocked; **E06** = connectivity stage failure, shown with the failing stage
(e.g. "E06 DHCP"). E01 stays "Darwin unreachable with network up".

## Config + provisioning semantics

- `wifi` config section (SSID/PSK, currently inert) goes live: read by the
  manager at boot; changes apply-by-restart like all config.
- **Two validation tiers** (replaces today's all-or-nothing `Validate`):
  *connectivity-valid* (wifi shape + web password present) vs *board-valid*
  (origin CRS + Darwin token too). `SetInitialPassword`'s full-validation
  demand is relaxed accordingly.
- **AP-mode first-run `/setup` collects WiFi credentials + admin password
  ONLY** (Jess's decision). Saves a connectivity-valid config. After the
  device joins the LAN, the board shows **E04 plus the device URL
  on-screen**; `/config` (normal authed UI) collects CRS/token to reach
  board-valid. `NeedsSetup` stays password-presence-based.
- **Credential handoff** (#12): syntactic validation while the AP is up →
  response page warns "hotspot drops ~20s, rejoin it if this fails" → AP
  torn down → bounded STA attempt → on failure AP restored and the error
  persisted (in-memory) for display to the reconnecting user; on success the
  device is on the LAN (portal page left on the phone explains reconnecting
  to their own WiFi).

## AP identity + captive portal

- SSID `Trainboard-XXXX` (XXXX = last 4 hex of wlan0 MAC, stable).
  Password: existing `Provisioning.APPassword` (M2 storage + regenerate
  button already shipped). WPA2-PSK.
- Static `192.168.4.1/24` on wlan0 in AP mode; dnsmasq DHCP range
  192.168.4.10-100, lease 10m; wildcard DNS → 192.168.4.1.
- Web server answers OS probes pre-auth — `/generate_204`,
  `/hotspot-detect.html`, `/ncsi.txt` — redirecting (or 200-with-content
  where the OS requires) into `/setup` or `/login`. Route-matrix tripwire
  gains every probe route.
- **Host/origin handling revisit** (M2 backlog): AP-mode allowlist =
  `192.168.4.1`, `trainboard.local`, plus probe hosts answered pre-auth
  without widening any authed route. DNS-rebinding posture re-checked in
  the security review of the final plan task.

## Watchdog + sd_notify aggregation (#9)

`WatchdogSec=30` goes live in `deploy/trainboard.service`. New tiny
aggregator in `cmd/trainboard` (or `internal/obs`): components (render loop,
poller, manager) each call `Beat(name)` on their natural cadence; a
goroutine calls `sd_notify(WATCHDOG=1)` only while EVERY registered
component has beaten within its deadline (render 5s, poller
2×interval+fetchTimeout, manager 90s). A deadlocked manager therefore
reboots the device even while rendering continues — and boot re-enters
APFallback safely (AP-restore invariant holds across reboot).

## Hardware evaluation protocol (gates the driver default; Jess as rescue)

Scripted on the bench Pi, each experiment under a dead-man switch (restore
known-good STA config + reboot after N minutes regardless of outcome):

1. mode=2 AP up: beacons? phone associates? dnsmasq lease? portal loads?
2. AP↔STA toggle ×10 via `select_network`: reliability, timing per
   transition, state after each.
3. Bad-PSK STA attempt → AP restore path exercises for real.

Verdict: mode=2 passes all → default driver; any wedge/flake → hostapd.
Recorded as an addendum to ADR 0003. The destructive matrix (#13) runs in
the same or a later bench session: bad PSK, missing SSID, DHCP timeout,
daemon crash, reboot mid-transition, client associated during retry.

## Testing

- Host (bulk of coverage): fake-Runner Manager tests — every transition,
  the AP-restore invariant (incl. verification failure escalation), retry
  suppression logic, retry-now, link-loss re-check; Check layer tests per
  stage; web tests for probes (pre-auth, correct bodies), partial setup,
  retry-now action, last-error display; Hotspot Info scene goldens;
  validation-tier tests.
- Hardware: evaluation protocol + destructive matrix, scripted checklists,
  results into the repo (docs/benchmarks or issue comments).

## Delivery

One spec (this), **two plans**:
1. **M3a (host-buildable, ships first):** prerequisites verification, net
   package (Runner/Manager/Check/drivers), scenes, watchdog aggregation,
   validation tiers, poller poke.
2. **M3b (portal + handoff + bench):** captive portal, partial setup,
   handoff flow, retry-now, evaluation protocol scripts + destructive
   matrix, ADR addendum.

## Out of scope

- mDNS (M4, #15). OTG (M4, #14). Self-update (M5).
- WPA3/enterprise networks; multiple stored WiFi networks (one network,
  matching config shape).
- Anti-burn-in layout jitter (separate backlog note).
