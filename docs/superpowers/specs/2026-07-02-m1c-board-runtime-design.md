# M1C — Board scenes + runtime + observability (design)

**Date:** 2026-07-02
**Status:** Approved for planning
**Milestone:** M1 — Render core + Darwin (MVP)
**Closes:** #23 (board scene contract + exact-pixel layout), #28 (runtime poller + render loop), #31 (observability)
**Depends on (merged):** M1A (`internal/display`, `internal/render`), M1B (`internal/data`, `internal/config`)

## Overview

The final software slice of M1. It composes the merged render/display foundation (M1A)
and the Darwin data client + config store (M1B) into a runnable `cmd/trainboard` that
fetches live departures and drives the SSD1322 through a complete scene set. The slice is
**software-complete and hardware-decoupled**: everything is built and verified on host
against the existing `FakeTransport` and a fixture `httpDoer`, on the documented
full-frame-flush baseline. No Pi hardware is required to build, test, or visually preview.

## Scope decisions (locked in brainstorming)

- **Software-complete slice.** Build #23, #28, #31 on host. The two hardware-measurement
  issues — **#21** (fps hardware benchmark) and **#29** (measured boot SLA) — are left for
  a separate hardware-bench session and are explicitly out of scope here.
- **Full-frame flush baseline.** The render loop flushes the whole framebuffer every tick.
  Dirty-region tracking stays deferred/deletable until the #21 benchmark proves it is
  needed (per ADR 0002 / PLAN item 3).
- **`render` stays model-agnostic.** No new `render.Element` types. `internal/board`
  composes each row from existing primitives (`StaticText`, `ScrollingText`, `Clock`) at
  fixed offsets. Layout knowledge lives in `board`, not `render`.
- **Spec-faithful degraded states.** Distinct on-screen states, including a dedicated
  `ClockNotSynced` transient so a wrong pre-NTP clock never shows and the clock-sync case
  never trips the Error path (which M3 uses to trigger AP fallback).
- **Auto-select transport + PNG preview (revisable).** `cmd/trainboard` runs the whole
  pipeline on host, writing frames to PNG, so scenes can be eyeballed without hardware.

## Packages

New packages; `render` and `display` are unchanged in interface.

| Package | Responsibility | Depends on |
|---|---|---|
| `internal/board` | Pure model→scene mapping. Owns all exact-pixel geometry constants, builds rows from `render` primitives, and selects the active scene by priority. Deterministic and golden-tested; no I/O, no goroutines. | `render`, `data`, `config` |
| `internal/runtime` | Poller (interval fetch → atomic snapshot), render loop (lock-free read → tick → full-frame flush), fetch-result → state classification, brightness/powersaving application. | `board`, `data`, `display`, `config`, `obs` |
| `internal/obs` | Bounded in-memory event ring, `slog` handler (journald-friendly), fault-code registry. | stdlib only |
| `cmd/trainboard` | Wiring: config load → data client → runtime → transport select; preview/PNG mode; flags. | all of the above |

## Scene contract + exact-pixel geometry (#23)

Board is 256×64. A departure row is 256×12. All geometry is expressed as **named
constants** derived from the reference implementation (`reference/src/trains/elements.py`
`render_departure`, and `reference/src/trains/scenes.py`), **not** by porting PIL
measurement calls. Every scene has a golden-image test against a fixture `data.Board`.

### Six scenes (all defined up front)

1. **Initialising** — pre-first-data boot screen: firmware version + "Connecting…".
   Default scene until the first successful (or failed) fetch resolves.
2. **DepartureBoard** — the primary live scene (geometry below).
3. **NoServices** — title + NRCC message carousel (3-line pages), shown when a successful
   fetch yields zero departures after filtering.
4. **Error** — fault message + on-screen fault code; shown on hard Darwin/connectivity
   failure once the stale-data grace period has elapsed.
5. **ClockNotSynced** — "Waiting for time sync…", clock element hidden. Distinct transient
   for the pre-NTP x509 case.
6. **HotspotInfo** — defined in the contract now, but **never selected in M1** (driven by
   M3 AP mode). Present so scene priority is complete and stable.

### Scene priority

`HotspotInfo > Error > ClockNotSynced > NoServices/DepartureBoard`, with `Initialising`
as the pre-first-data default. This guarantees AP-mode info (M3) is never hidden by a
stale-data Error, and a wrong-clock transient is never misread as a connectivity failure.

### DepartureBoard geometry

Row columns (from `render_departure`, row height 12):

| Column | Placement |
|---|---|
| Order (`1st`, `2nd`, …) | left, `x=0` |
| Scheduled time | centered in 28px box at `x=17` |
| Headcode *(data-unavailable)* | centered in 27px at `x=45`; **not drawn** — `rsid` is a retail ID, not a headcode. Constant retained; destination/platform shift `+27` only if a headcode source is ever added. |
| Platform | centered in 19px at `x=45` |
| Destination | left, `x=64` |
| Status | right-aligned in 40px ending at `x=256` (i.e. `x=216`) |

Scene composition:

| Element | Position | Notes |
|---|---|---|
| NextService row | `(0, 0)`, 256×12 | departure row 1, with vertical scroll-in animation |
| calling-at label | `ScrollingText (0, 12)` w=42 | "Calling at:" |
| calling-at list | `ScrollingText (42, 12)` w=214 | intermediate stops |
| service_info | `ScrollingText (0, 24)` w=256 | operator / coach info |
| RemainingServices | `(0, 36)` w=256 | vertical-scrolling list, rows ordered 2..n |
| Clock | `(0, 50)` h=14 | boldlarge `HH:MM` + boldtall `:SS` |

## Runtime state machine (#28)

### Poller

A goroutine fetches via the `data` client every `config.Board.RefreshSeconds` (default
60). Each result is mapped to an immutable `board.Snapshot` (board data + classified
state + timestamp) and published via `atomic.Pointer`. The poller never blocks the render
loop, and the render loop never blocks the poller.

`config.Board.Services` → `data.Filter.MaxServices` (client-side trim). `data.Request.NumRows`
stays fixed at 10 (the LDBWS WithDetails cap, clamped in `buildEnvelope`) so server-side
capping cannot cause a false `NoServices` (per M1B carry-over notes).

### Render loop

Fixed ~25fps tick (0.04s, reference parity). Each tick:

1. Load the current snapshot lock-free (`atomic.Pointer.Load`).
2. Select the active scene by priority.
3. `Scene.Render(fb, tick, now)`.
4. **Full-frame flush** the framebuffer to the transport.

### Fetch-result classification

| Condition | Resulting state |
|---|---|
| Fetch success, ≥1 departure after filtering | DepartureBoard |
| Fetch success, 0 departures after filtering | NoServices |
| Fetch error, last good board < 5 min old | keep last board (stale grace; unchanged frame) |
| Fetch error, last good board ≥ 5 min old, or never succeeded | Error |
| **x509 time-validity error** from the HTTPS call | **ClockNotSynced** (transient; NOT a connectivity failure — must not later trip AP fallback) |

The 5-minute grace matches the ADR 0003 data-staleness window. Clock-not-synced must be
classified from the specific x509 time-validity error, distinct from generic transport or
DNS failure.

### Brightness / powersaving

Each tick, evaluate `config.Powersaving.BrightnessAt(now)`; issue the SSD1322 contrast
command **only when the computed value differs from the last one applied**, so a redundant
SPI command is not sent every frame.

## Observability (#31)

- **slog** → stderr in a journald-friendly format; systemd captures stderr to the journal.
- **Bounded event ring**: fixed-capacity (~256 entries), thread-safe, oldest-evicted.
  Records fetch failures, state transitions, per-tick flush/render timing, and boot
  timings. Exposed via a read accessor for the M2 `/status` page (page itself is M2).
- **Fault codes**: a small enum surfaced in a screen corner during Error / ClockNotSynced
  for field diagnosis, e.g. `E01` Darwin unreachable, `E02` auth rejected, `E03`
  clock-not-synced, `E04` config error. The exact code list is finalised during planning.

## cmd/trainboard wiring + verification (revisable)

- **Transport auto-select:** real periph SPI when `--production` is set (or hardware is
  detected); otherwise a **headless preview transport** that writes rate-limited PNG
  frames to `--preview-dir`. This runs the entire pipeline on host against a fixture or
  live Darwin and doubles as a preview source the M2 status page can reuse.
- **Config:** loaded from `/var/lib/trainboard/config.json`, with a dev override flag.
- **systemd unit:** starts early, **no `network-online.target` dependency**, with a
  `WatchdogSec` placeholder that M3's connectivity manager will own. The healthy render
  loop must not pet the watchdog on behalf of connectivity (per PLAN item 15 note).

## Testing strategy (host-only, TDD)

- **board:** golden-image test per scene (extends the M1A golden harness) with exact-pixel
  assertions against fixture `data.Board`s (empty, single, full, cancelled, delayed,
  missing-platform, long-destination).
- **runtime:** fake `httpDoer` (fixtures) + `FakeTransport` + an injectable clock. Assert
  every state transition including stale-grace boundary, clock-not-synced classification,
  and offline→Error at the 5-minute edge. Assert snapshot atomicity / no data race under
  concurrent poll + render (`-race`).
- **obs:** ring capacity bound, eviction order, concurrency safety.
- **End-to-end:** config → data(fixture) → runtime → FakeTransport; assert scene selection
  and produced frames across a scripted sequence of fetch outcomes.
- Gate: `make check` green (vet + golangci-lint + tests, incl. `-race`); linux/arm64 build.

## Out of scope (explicit)

- **#21** fps hardware benchmark and **#29** measured boot SLA — require a real Pi Zero 2 W
  + SSD1322; separate hardware-bench session.
- AP / connectivity orchestration and **driving** the HotspotInfo scene — M3.
- `/status` web page and admin UI — M2 (the ring is exposed now; the page consumes it later).
- Dirty-region flush — deferred/deletable until #21 proves it necessary.
- Headcode feature — no LDBWS data source; constant retained but unfed.
