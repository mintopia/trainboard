# Plan Review Log: Train Departure Display — Go rewrite

Act 1 (grill-with-docs) complete — plan locked, CONTEXT.md + ADRs 0001-0003 written.
MAX_ROUNDS=5.

## Round 1 — Codex (thread 019f2061-4d51-7101-b0ce-3c847d11fa0b)

VERDICT: REVISE. 25 findings (paraphrased):

1. SSD1322 partial updates not as cheap as "a few hundred bytes"; scrolling dirties a
   wide strip — require an M1 hardware benchmark (256x12/256x24/full-frame) before 60fps.
2. "Sub-pixel-smooth scroll" on pixel-addressed RAM may smear dot-matrix; define scroll
   as integer-pixel faithful OR measured greyscale-blended with golden frames.
3. Reference animates ~25fps (0.04s); target 25/30fps for parity, 60fps optional after
   hardware proof.
4. Error scene listed in M1 but Hotspot Info (needed by M3) not in scene contract early.
5. "1:1 row layout" doesn't preserve exact pixel geometry incl. headcode-dependent
   platform/destination shift — encode as named constants + golden-image tests.
6. Darwin GetDepBoardWithDetails under-specified: optional/missing fields, SOAP faults,
   namespaces, lt*/et* times, circular routes, multi-destination — write a mapping doc +
   fixtures (empty/cancelled/delayed/bus/multi-dest/missing-platform/SOAP-fault).
7. Destination filter must be "calls at CRS" (subsequentCallingPoints), not final dest —
   name it callingAtCRS, test against calling points.
8. Missing "departed" filtering from reference — add departure-visibility rule + fixtures.
9. No cross-midnight handling — store full local datetimes; test 23:xx/00:xx/DST/cutoff.
10. NRCC messages are HTML + station-filtered in reference — add sanitize/strip/entity-
    decode/station-filter/length-limit + malicious-markup fixtures.
11. Secrets plaintext + plain-HTTP actions; write-only UI doesn't stop CSRF/LAN/AP
    attackers rebooting/updating — add local admin auth, CSRF, origin checks, rate
    limits, redacted logs, separate AP provisioning creds.
12. No snapshot/locking model — poll/render/config races; publish immutable board
    snapshots via atomic pointer/channel; transactional config writes.
13. 5-min stale→Error can collide with AP fallback and hide provisioning — scene priority:
    Hotspot Info overrides Error while AP active.
14. "Board visible in a few s" has no budget/baseline — set a measured boot SLA with
    systemd-analyze; separate power-on-to-logo from power-on-to-live-data.
15. ~45s AP trigger can falsely fail on weak wifi/slow DHCP — distinguish association/
    DHCP/DNS/captive/Darwin reachability; backoff; don't retry while AP client connected.
16. Failed hostapd restart after failed STA strands device — rollback state machine +
    watchdog verifying AP SSID + DHCP up before declaring fallback restored.
17. Saving creds then tearing down AP can strand user on bad creds — bounded creds test,
    restore AP with submitted error preserved.
18. dwc2 single-port gadget/host auto-switch is fragile — gadget Ethernet as supported
    default; USB-host dongle as hardware-validated optional mode with manual fallback.
19. mDNS: handle interface churn, duplicate names, multicast perms, AP-vs-LAN separation —
    per-interface bind, link up/down, probe/rename on conflict.
20. Single pinned minisign key = rotation/compromise double fault — signed keyring with
    key IDs, overlapping trust windows, expiry, emergency recovery path.
21. Health-check rollback can corrupt both slots / oscillate — A/B slots + manifest,
    fsync/rename, boot-attempt counters outside the binary, rollback suppression after a
    known-good mark.
22. Release assets not bound to version/arch/downgrade — verify a signed manifest with
    version/commit/arch/hash/min-rollback/channel to stop replay/downgrade.
23. Fake-runner net tests insufficient — add on-hardware destructive matrix (bad PSK,
    missing SSID, DHCP timeout, hostapd crash, wpa hang, reboot mid-transition, AP client
    connected during retry).
24. design spec still says RTT-only vs ADR 0001 Darwin-only — supersede/update it.
25. Observability underpowered — structured journald logs, bounded local event history,
    /status diagnostics, on-screen fault codes; confirm Sentry replacement.

### Claude's response (final arbiter)

Accepted all 25 — they are legitimate and materially de-risk the plan. Actions taken in
PLAN.md revision:
- Rendering: target **25/30fps parity, 60fps aspirational after M1 hardware benchmark**;
  **integer-pixel scrolling** as the faithful default (greyscale for glyph edges only;
  blended sub-pixel only if a golden-frame test proves it doesn't smear); exact **row
  geometry as named constants + golden-image tests**; **Hotspot Info added to the scene
  contract** and a **scene-priority order** (Hotspot Info > Error).
- Data: **callingAtCRS** (calls-at) filter; **departed/visibility** filtering;
  **cross-midnight/DST** datetime reconstruction; **HTML sanitize + station-filter** for
  NRCC messages; a **Darwin mapping doc + edge-case fixtures** (incl. SOAP faults).
- Concurrency: **immutable board snapshot via atomic pointer**, **transactional config**.
- Security: **local admin auth + CSRF + origin checks + rate limits + redacted logs**,
  AP provisioning creds distinct from LAN admin.
- Net: **layered connectivity probes + backoff**, **AP-restore watchdog invariant**,
  **bounded creds test before AP teardown**, don't-retry-while-AP-client-connected.
- OTG: **gadget default; host-dongle optional + hardware-validated + manual fallback**.
- mDNS: **per-interface bind, churn handling, conflict probe/rename, AP/LAN separation**.
- Update: **A/B slots + signed manifest (version/arch/hash/min-rollback/channel) +
  external boot-attempt counter + keyring with rotation/recovery**.
- Boot: **measured SLA (power-on→logo vs →live-data) via systemd-analyze**.
- Observability: **slog→journald + bounded local event ring + /status + on-screen fault
  codes**; Sentry confirmed dropped.
- Marked the design spec **superseded** re: data source.
Nothing rejected. Proceeding to Round 2 for re-review.

## Round 2 — Codex (same thread)

VERDICT: REVISE. Confirmed Round 1 largely resolved. 9 follow-ups:
1. ADR 0002 still says sub-pixel/60fps/"few hundred bytes" — realign to revised plan.
2. ADR 0003 lacks restore-invariant/watchdog/no-retry-while-provisioning/layered states.
3. "test creds before tearing down AP" contradicts AP+STA-can't-coexist — validate
   syntax while AP up, warn, tear down for bounded STA attempt, restore with error.
4. "never retry STA while client connected" — idle associated phone blocks recovery
   forever; gate on active provisioning (DHCP/HTTP activity) + user-triggered retry.
5. Reference hides only `departed`, not `arrived` (Arrived is a visible status).
6. `lt*`/`et*` imprecise — name exact LDBWS fields (board std/etd, CP st/et, sentinels).
7. SOAP AccessToken needs concrete nested TokenValue/namespace + request-byte fixtures.
8. "both slots fail → hold last-known-good" inconsistent — define explicit double-fault
   path (never overwrite known-good; manual recovery mode).
9. Wall-clock key expiry breaks on an RTC-less Pi before NTP — use signed version
   epochs / min-rollback-version instead.

### Claude's response (final arbiter)

Accepted all 9. Actions: rewrote ADR 0002 (integer-pixel scroll, benchmark-gated 25-30fps,
dropped the byte/fps claims) and ADR 0003 (layered connectivity states, AP-restore
invariant + watchdog, active-provisioning retry suppression + manual retry, credential
handoff). PLAN.md: pinned the exact SOAP envelope/namespace + LDBWS field names
(std/etd, st/et, sentinels) with request-byte fixtures; hide only `departed` and keep
"Arrived"; provisioning retry gated on activity not association; credential handoff
reworded to AP-up syntax check → warn → bounded STA attempt → restore-with-error;
self-update keyring uses version epochs (no wall-clock expiry); explicit double-fault
path (inactive-slot-only writes + manual recovery mode). Nothing rejected. On to Round 3.

## Round 3 — Codex (same thread)

**VERDICT: APPROVED.** All nine Round 2 findings confirmed resolved; ADR 0002 aligns with
the render plan, ADR 0003 aligns with the layered connectivity/AP-restore model; no
remaining blocker in scope.

**Act 2 converged in 3 rounds.** Next: independent Fable 5 expert review (per user
request), then user sign-off gate.

## Independent review — Fable 5 (post-Codex, second model)

Caught a **systematic gap Codex introduced**: `data`-layer requirements were hardened
against the old push-port *reference*, not the actual OpenLDBWS schema (cited Open Rail
Data wiki). 13 findings; all accepted:

- **HIGH** — OpenLDBWS lacks push-port fields: no `departed`/`arrived` flags, no `ssd`,
  no headcode (`rsid` is a retail ID). → drop departed filtering (server-side); status
  from `etd` only; cross-midnight from `std` vs `generatedAt`; headcode marked
  data-unavailable. (Reverses Codex R2 #5 "keep Arrived" — unimplementable.)
- **HIGH** — `GetDepBoardWithDetails` caps at 10 rows; client-side filters can zero out.
  → push destination filter server-side (`filterCrs`+`filterType=to`); document ceiling +
  `timeWindow`; "10 fetched, all filtered" fixture.
- **HIGH** — first-boot AP dead-on-arrival via rfkill/regulatory domain. → set country +
  `rfkill unblock` first; "rfkill blocked" fault code.
- **MED-HIGH** — rollback can't run inside a binary that won't exec. → external launcher
  shim does slot-select + boot-attempt counting, never A/B-updated.
- **MED** — SOAP token namespace is the **Token** ns, not commontypes; live probe made a
  required mapping-doc gate.
- **MED** — wlan0 ownership contested + DHCP client unnamed; **evaluate wpa_supplicant
  AP mode (`mode=2`)** to drop the hostapd handoff.
- **MED** — no-RTC clock skew breaks TLS to Darwin pre-NTP → classify x509 time errors as
  "clock-not-synced" transient, not connectivity failure.
- **MED** — SSD1322/SPI: `spidev` 4096 bufsiz chunking, column offset 0x1C + 4px align,
  and **full-frame baseline** (dirty-region likely deletable).
- **MED** — font stack: `x/image/font/opentype` (not frozen freetype/truetype); derive
  constants from reference frames.
- **LOW-MED** — RNDIS deprecated → **CDC-NCM (+ECM)** composite.
- **LOW-MED** — full mDNS probing ~ writing a stack → announce-only + collision-safe
  suffix.
- **LOW** — `WatchdogSec` heartbeat must aggregate all critical goroutines.
- **LOW** — add reference features: powersaving brightness schedule + `layout.times`.

All folded into PLAN.md + ADRs 0001/0002/0003. Fable's verdict: plan is
implementation-ready once these land; strongest = concurrency + update design; weakest
(now fixed) = the `data` milestone's schema assumptions.

**Both review passes complete. Holding at the user sign-off gate — no code.**
