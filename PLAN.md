# Plan: Train Departure Display — ground-up Go rewrite
_Locked via grill-with-docs — by Claude + Jess. Terms per CONTEXT.md. Hardened via Codex Act 2._

## Goal

Replace the Python/luma train Departure Board with a single self-contained Go binary on
a Pi Zero 2 W (DietPi, arm64) that boots fast, drives the SSD1322 OLED smoothly, sources
live data directly from Darwin Lite (OpenLDBWS), stores its own config locally, and can
provision, discover, and update itself in the field — reproducing the current board's
look and content while adding a web UI, AP-mode provisioning, OTG networking, mDNS
discovery, and GitHub-release self-update.

## Approach

Built as deep modules behind narrow interfaces in one binary:
`display` (SSD1322 SPI driver) · `render` (framebuffer + TTF rasterization + scene
engine) · `board` (scenes/layout) · `data` (Darwin client + mapping) · `config` ·
`web` · `net` (Connectivity Manager) · `update`. `board`/`render` are source- and
transport-agnostic.

**State model (concurrency):** the Darwin poller publishes an **immutable board snapshot
via an atomic pointer**; the render loop reads the current snapshot lock-free. Config
writes are **transactional** (write-temp + fsync + atomic rename). No shared mutable
state between poll, render, web, and net.

Delivered in five milestones. Each: TDD red/green → `golangci-lint`/`go vet` → tests as
a hard gate → Codex review at close. Tracked as a GitHub Project (milestones + issues),
set up after sign-off.

### M1 — Render core + Darwin (MVP)
1. `display`: native Go SSD1322 driver over SPI (periph.io for **SPI/GPIO transport
   only** — no prebuilt panel driver exists), 4-bit greyscale framebuffer,
   `contrast`-based brightness. **SPI mechanics to encode + golden-byte test:** `spidev`
   default `bufsiz` is 4096 B while a full frame is 8192 B → **chunk writes** (or verify
   periph.io `MaxTxSize`); column addressing is in **4-pixel/2-byte units** and a
   256-wide panel sits at **column offset 0x1C** in the 480-wide RAM → any partial write
   must be 4-pixel-aligned + offset. SPI behind an interface; in-memory fake device.
2. **Render performance is proven, not assumed.** **Full-frame flush every frame is the
   benchmarked baseline** (8 KB @ 10–16 MHz ≈ 4–7 ms comfortably clears 25–30fps, and the
   reference scenes dirty most of the panel per tick anyway). An early M1 **hardware
   benchmark** (256×12 / 256×24 / full-frame timings) decides whether **dirty-region
   tracking is worth building at all** — treat it as likely-deletable complexity, not a
   given. **Target 25–30fps (reference parity — Python runs ~25fps at 0.04s); 60fps only
   if the benchmark supports it.**
3. `render`: greyscale framebuffer, Dot Matrix rasterization via **`x/image/font/opentype`
   (sfnt)** — *not* the frozen `freetype/truetype`. Cached glyph buffers, anti-aliased
   glyph edges. **Scrolling is integer-pixel** (faithful); greyscale is for glyph edges,
   not motion (blended sub-pixel only if a golden-frame test proves no smearing). Note
   sfnt does no hinting, and Go metrics differ from PIL's FreeType → **derive layout
   constants from captured reference frames, not by porting PIL measurement calls**; do
   an early AA-vs-1-bit-threshold visual check of the fonts at 10px/20px. Scene/element
   engine (Clock, StaticText, ScrollingText, NextService, RemainingServices).
   Golden-image tests.
4. `board`: full **scene contract up front** — Initialising, DepartureBoard, NoServices,
   Error, **Hotspot Info**, Clock — even though Hotspot Info isn't driven until M3.
   **Scene priority:** Hotspot Info > Error > NoServices/DepartureBoard, so AP-mode info
   is never hidden by a stale-data Error. Row layout reproduces the reference **exact
   pixel geometry** as **named constants + golden-image tests**. ⚠️ **Headcode has no
   LDBWS source** (`rsid` is a retail ID like "GW1234", not a headcode) — keep the
   layout constant but mark the headcode feature **data-unavailable** unless another
   source is added.
5. `data`: Darwin Lite (OpenLDBWS) `GetDepBoardWithDetails` — hand-rolled SOAP envelope.
   **Write the mapping doc schema-first from the LDBWS WSDL + a captured live response,
   THEN re-derive each behaviour** (do not port the old push-port JSON feed's fields).
   Token header is `<AccessToken><TokenValue>…</TokenValue></AccessToken>` in the **Token
   namespace `http://thalesgroup.com/RTTI/2013-11-28/Token/types`** (NOT commontypes; a
   wrong namespace yields "unauthorized"), with the ldb12 request namespace for the body.
   The exact envelope is pinned + fixture-tested at request-bytes level, and the
   **env-gated live probe is a REQUIRED gate for the mapping doc** (fixtures alone can
   lock in a wrong pin). **`numRows` caps at 10 for WithDetails** — note the ceiling and
   `timeWindow` param. Fields: board `std`/`etd`, calling-point `st`/`et`/`at`, plus
   cancelled/delayed sentinels (`etd`="Cancelled"/"Delayed"/"On time"). Fixtures cover
   empty, cancelled, delayed, bus, multi-destination, missing-platform, circular-route,
   SOAP-fault, and optional/missing fields.
6. Filtering: **destination (callingAtCRS) is pushed SERVER-SIDE** via the request's
   `filterCrs` + `filterType=to` (Darwin evaluates calls-at) — critical because the
   10-row WithDetails cap + client-side filtering can otherwise yield a false NoServices.
   Platform + TOC filter client-side; service count, cutoff hours, station-name
   replacements. **No `departed`/`arrived` flags exist on an LDBWS departures board** —
   Darwin drops departed services server-side, and status derives purely from `etd`
   (On time / Exp HH:MM / Cancelled / Delayed). Fixture: "10 fetched, all filtered out".
   NRCC messages **HTML-sanitized → text** (entity-decode, tag-strip, station-filter,
   length-limit; malicious-markup fixtures).
7. **Cross-midnight/DST time handling:** LDBWS gives no `ssd`/origin-time, so reconstruct
   each service's full local `time.Time` from **`std` vs the board `generatedAt`** with a
   window heuristic (e.g. `std` more than ~6h in the past ⇒ next day); tests for 23:xx /
   00:xx boundaries, DST transitions, and cutoff windows.
8. `config`: JSON at `/var/lib/trainboard/config.json`, mode 0600, `version` field,
   validation, sane defaults, transactional writes. Holds the Darwin token (secret,
   plaintext at rest, redacted in all logs). Schema includes the reference's
   **powersaving brightness schedule** (start/end/brightness, cross-midnight window) and
   the **calling-point-times toggle** (`layout.times`).
9. Runtime: poller publishes snapshots on the refresh interval (~60s, configurable);
   render loop animates from the atomic snapshot. Offline: keep last board 5 min then
   Error Scene (subject to scene priority). systemd unit starts early, **no
   `network-online.target` dependency**. ⚠️ Because it starts before NTP on an RTC-less
   Pi, **classify x509 time-validity errors from the Darwin HTTPS call as a distinct
   "clock-not-synced" transient** (wait for timesyncd) — not a Darwin/connectivity
   failure (which would wrongly trip the AP fallback).
10. Boot: **measured SLA** — capture a `systemd-analyze blame`/`critical-chain` baseline
    on DietPi; track **power-on→logo** separately from **power-on→live-data**; prune
    services to hit the budget. (Budget set from the baseline, not guessed.)

### M2 — Config Web UI
1. `web`: embedded HTTP server (`go:embed`), config logic in a service behind thin
   handlers, **JSON endpoints from day one** (cheap SPA pivot later).
2. **Security (not optional):** local **admin authentication**, **CSRF tokens**, **Origin/
   Host checks**, **rate limiting** on state-changing actions, **redacted logs**. AP
   **provisioning credentials are distinct** from the LAN admin credential. Plain HTTP on
   the trusted LAN is an explicitly accepted residual risk (documented), mitigated by the
   above; reboot/update/config actions are auth+CSRF-gated.
3. UI (static-first): server-rendered `html/template` + minimal vanilla JS/htmx, phone-
   first responsive. Manages config, Darwin token (write-only field), wifi credentials,
   status page (IP, version, connectivity, last fetch, event history), actions (restart,
   update, reboot), regenerate AP password.
4. **Design pass with the impeccable skill suite** (shape → frontend-design →
   typeset/layout/colorize → adapt → polish → audit). Theme cue: amber-on-black.
5. Fallback: pivot to a JS SPA + JSON API as a fast follow if static can't hit the bar.

### M3 — Provisioning / AP mode
0. **First-boot prerequisites (or AP is dead-on-arrival):** set a default **wireless
   regulatory country + `rfkill unblock wifi`** before the first AP attempt (a virgin
   image soft-blocks wlan0 and hostapd needs `country_code` on the CYW43436). Treat
   **"rfkill blocked" as a detected on-screen fault code**, not a silent hostapd failure.
   Also: **exclude wlan0 from DietPi's own network scripts/dhcpcd** (deny/mask) so only
   the Connectivity Manager owns it, and **name the STA DHCP client** explicitly.
1. `net` Connectivity Manager orchestrates `wpa_supplicant`/`wpa_cli` (STA),
   `hostapd` (AP), `dnsmasq` (DHCP + captive DNS). No NetworkManager (ADR 0003).
   ⚠️ **Evaluate `wpa_supplicant` native AP mode (`mode=2`) FIRST** — it collapses AP↔STA
   into `wpa_cli select_network` (one daemon, no hostapd start/stop handoff — the riskiest
   transition), with dnsmasq kept for DHCP/captive DNS. Only fall back to hostapd if it
   proves inadequate on brcmfmac.
2. **Layered connectivity check** (not a blunt 45s timeout): distinguish association →
   DHCP → DNS → captive-portal → Darwin reachability, each a distinct state with backoff.
   Enter AP mode on first run (no wifi configured) or after layered failure.
3. **AP-restore is a hard invariant.** Tear-down-retry every 5 min (ADR 0003) runs a
   rollback state machine: attempt STA; on success resume; **on failure, verify the AP
   SSID is beaconing and the DHCP lease service is up before declaring fallback
   restored**, guarded by a **systemd `WatchdogSec`**. Since it's one binary, the
   **`sd_notify` heartbeat must aggregate every critical goroutine** (render + poller +
   Connectivity Manager) — a healthy render loop must not pet the watchdog while the
   Connectivity Manager is deadlocked. **Suppress the STA retry only while a user is
   actively provisioning** (recent
   DHCP lease + HTTP activity on the AP), not on mere association, and always offer a
   user-triggered "retry now" — so an idle associated phone can't block auto-recovery.
4. AP identity: SSID `Trainboard-XXXX`, per-device random WPA2 password (persisted,
   regenerable), shown on the **Hotspot Info Scene** with the AP IP (`192.168.4.1`).
5. Captive portal: `dnsmasq` wildcards DNS to the AP IP; web server answers OS probe URLs
   (`/generate_204`, `/hotspot-detect.html`, `/ncsi.txt`) to auto-pop setup.
6. Handoff: **syntactically validate submitted credentials while the AP stays up, warn
   the user the hotspot will briefly drop, then tear down the AP for a bounded STA
   attempt** (AP+STA can't coexist); on failure restore the AP with the error preserved
   for the reconnecting user.
7. Tests: fake command runner for state logic **plus an on-hardware destructive matrix**
   (bad PSK, missing SSID, DHCP timeout, hostapd crash, wpa hang, reboot mid-transition,
   AP client connected during retry).

### M4 — OTG networking + mDNS
1. OTG: **gadget Ethernet is the supported default**, as a **CDC-NCM (+ECM legacy)
   composite** — *not RNDIS* (deprecated/blocked in current Windows 11 and being removed
   from the kernel); NCM+ECM is driven natively by Windows 10 1903+/11, macOS and Linux.
   Pi appears as a USB NIC over the cable (static/link-local + reachable web UI).
   **USB-host dongle is an optional, hardware-validated mode with manual fallback**
   (single-port `dwc2` role switching depends on cable ID/power state + configfs teardown
   — not assumed seamless).
2. mDNS: advertise `trainboard-XXXX.local` + `_http._tcp` **per-interface** with
   interface-churn handling and AP-vs-LAN separation (don't advertise LAN-only names
   inside the AP). ⚠️ Full RFC 6762 probing/conflict-rename exceeds what
   hashicorp/mdns or grandcat/zeroconf implement — **default to announce-only with the
   collision-safe `Trainboard-XXXX` suffix**, and only budget custom code (pion/mdns
   base) if true conflict handling proves necessary. No avahi daemon.

### M5 — Self-update from GitHub releases
1. **A/B slot** binary update (not in-place overwrite): download the arm64 asset to the
   inactive slot, verify a **signed release manifest** (minisign) binding **version,
   commit, arch, asset SHA256, minimum-rollback-version, channel** — rejecting replayed/
   downgraded/mismatched-arch assets — then switch the active slot via fsync+rename.
2. **Keyring, not a single pinned key:** ship a trusted-key set with key IDs, overlapping
   trust windows, and an emergency recovery path, so key rotation/compromise isn't a
   double fault. **No wall-clock key expiry** — a headless Pi has no RTC and would reject
   valid keys after power loss before NTP; trust is bounded by **signed version epochs +
   minimum-rollback-version**, with time-based checks only when a trusted clock exists.
3. Recovery is driven by a **tiny stable launcher outside the payload** (a shim
   `ExecStartPre`/`OnFailure` unit) — because the health check runs *inside* the new
   binary and a slot that segfaults on exec can't flip its own symlink or count its own
   failures. The launcher reads the **boot-attempt counter stored outside the binary**,
   selects the slot, and flips to known-good after N failed starts; **the launcher itself
   is never updated by the A/B mechanism** (or only via a separate, conservative path).
   If the new slot fails a
   post-restart health check (board renders + web UI responds within N s), roll back to
   the previous slot; **suppress further rollback once a known-good mark is set** to
   prevent oscillation. **Double-fault path (both slots fail):** updates only ever write
   the *inactive* slot, so the known-good slot is never overwritten — recovery re-selects
   it and enters a **manual recovery mode** (AP up + web UI + on-screen fault code)
   rather than bootlooping.
4. Trigger: periodic check → "update available" in web UI (+ subtle on-screen hint) →
   **manual apply**; opt-in auto-apply. Stable channel default; opt-in prereleases.

## Cross-cutting

- **Observability:** `log/slog` → journald; a **bounded in-memory event ring** (fetch
  failures, render FPS/SPI-flush timing, network transitions, update attempts, boot
  timings) surfaced on a **`/status`** page; **on-screen fault codes** for field
  diagnosis. **Sentry dropped** (confirmed); no external error service.
- **Testing:** SPI golden-byte, render golden-image, Darwin fixtures (+ env-gated live
  probe), net fake-runner + on-hardware matrix, config round-trip/validation, update A/B
  + signature + rollback simulation. TDD throughout.
- **CI:** GitHub Actions — PRs run tests+lint+vet (hard pre-merge gate); tags cross-
  compile arm64, produce the signed manifest + minisign signature, attach to the release.

## Key decisions & tradeoffs

- **Darwin Lite (OpenLDBWS), not RTT** — reproduces the board 1:1 in one call; accept
  SOAP/XML in `data`. → ADR 0001.
- **Greyscale + native SSD1322 driver**, integer-pixel scroll, fps proven on hardware. → ADR 0002.
- **hostapd/wpa_supplicant + AP-fallback tear-down-retry with restore-invariant + watchdog** — lean/fast boot over NetworkManager. → ADR 0003.
- **DietPi arm64**; boot won via early network-independent start + service pruning, to a measured SLA.
- **Config local JSON (0600), transactional**; token write-only + redacted; invalid → AP mode.
- **Static-first web UI + JSON API**, with real auth/CSRF; designed with impeccable.
- **Lean/no-daemon networking**: embedded mDNS, self-owned AP/STA control.
- **A/B signed-manifest self-update with keyring + external boot counter** — a broken or
  tampered release must not strand a headless device.

## Risks / open questions

- **AP/STA state machine** remains the highest-risk code (device-stranding); mitigated by
  the restore-invariant + watchdog + on-hardware matrix, but needs real-device soak.
- SSD1322 fps ceiling is unknown until the M1 hardware benchmark; render architecture is
  gated on it.
- OpenLDBWS Lite rate/usage limits vs refresh interval; `length` often absent — degrade
  cleanly.
- OTG host-dongle role switching may prove unreliable on the Zero 2 W — it's optional.
- minisign key custody in CI (Actions secret) and the recovery-key escrow process.
- _Unconfirmed cross-cutting (decided while user away):_ module path/owner, Go/CI
  specifics.

## Out of scope

- OS-image / full-system updates (binary A/B self-update only).
- Cloud config sync / fleet management backend.
- Non-Darwin data sources (RTT credential kept on file, unused).
- TLS on the LAN web UI (residual risk accepted, mitigated by auth/CSRF).
- Arrivals boards / multi-station per device (single origin station per board).
