# Train Departure Display — Ground-Up Rewrite Design

**Date:** 2026-07-02
**Status:** Superseded by `PLAN.md` + ADRs 0001–0003. Historical brainstorming artifact.

> ⚠️ **Superseded:** This doc records the initial brainstorm. Key decisions changed
> during grilling — most importantly the **data source is Darwin Lite (OpenLDBWS), NOT
> RealTimeTrains** (see ADR 0001). For the authoritative plan use `PLAN.md`; for domain
> language use `CONTEXT.md`. Do not implement from this file.

## Overview

A ground-up rewrite of the UK train departure display for a Raspberry Pi Zero W 2
driving a 256×64 SSD1322 SPI OLED. The rewrite replaces the current Python +
`luma.oled` implementation and its external data/config services with a single,
self-contained Go binary that talks directly to the RealTimeTrains API, stores its
own configuration locally, and provisions/updates itself.

## Goals

- **Faster boot** — single static binary, minimal runtime deps, board visible ASAP.
- **Better OLED driving** — native 4-bit greyscale (16 levels) + dirty-region
  flushing, replacing today's 1-bit rendering.
- **Self-contained** — no external data/config middleman; direct RTT API + local config.
- **Zero-touch first run** — AP mode + web UI provisioning out of the box.
- **Maintainable in the field** — mDNS discovery, OTG networking, GitHub self-update.
- Preserve the current board's visual format and animations exactly.

## Decisions locked in brainstorming

- **Stack:** Go, compiled to a single static binary, systemd-managed.
- **Milestone 1 scope:** render core + direct RTT (the MVP).
- **Data source:** RealTimeTrains REST API only (`api.rtt.io`), per-device credentials.
- **Config model:** fully local on-device; no remote config server.
- **Plan scope:** all five milestones (M1–M5) at skeleton altitude.

## Current state (reference implementation)

`UK-Train-Departure-Display/` — Python 3 + `luma.oled` + Pillow, ~1,200 LOC.

- 256×64 SSD1322 driven in **1-bit mode**, everything yellow.
- A `Board` owns a luma `viewport`; **scenes** register **snapshots/hotspots** that
  self-redraw on a fast `0.04s` interval.
- Animated elements: `Clock` (large time + tall seconds), `ScrollingText`
  (scroll-up-then-left with pauses), `NextService` (roll-up reveal),
  `RemainingServices` (vertical carousel).
- `render_departure` row layout: **ordinal · scheduled · [headcode] · platform ·
  destination · status (right-aligned)**.
- Scenes: `Initialising` (serial/version/IP), `NoServices` (paged message carousel),
  `DepartureBoard`, `Clock`. Brightness power-saving on a time schedule.
- Data: pre-digested Darwin-derived JSON from `ldb.prod.a51.li`; per-device config
  from a remote server keyed by MAC. The Pi does almost no data logic today.

## Target architecture — one Go binary, deep modules behind narrow interfaces

```
trainboard (single static binary, systemd-managed)
├── display/   SSD1322 driver — SPI framebuffer, 4-bit greyscale, dirty-region flush
├── render/    framebuffer + TTF rasterization (Dot Matrix fonts) + scene/element engine
├── board/     scenes & layout: Initialising, DepartureBoard, NoServices, Clock
├── data/      RTT API client + mapping to internal Departure/Stop/Location model
├── config/    local config load/save/validate (station, filters, brightness, creds)
├── web/       embedded HTTP server + static assets (M2+)
├── net/       wifi / AP / OTG / mDNS control (M3–M4)
└── update/    GitHub-release self-update (M5)
```

**Isolation:** `display` takes a framebuffer → emits SPI bytes; `render` takes a scene
graph → produces a framebuffer; `data` takes RTT JSON → emits the internal model;
`board` composes model → scene. No luma/Pillow — we own the driver.

### Display driver ("better OLED driving")

Drive the SSD1322 in native **4-bit greyscale** for anti-aliased text and smooth
brightness fades, with **dirty-rectangle flushing** so fast scroll animations only push
changed columns over SPI. SPI via `periph.io`. ⚠️ Exact frame budget / SPI clock — open.

### Rendering — preserve the current look exactly

Reproduce the row format 1:1, the roll-up next-service reveal, the calling-at scroll,
the vertical carousel of remaining services, the big clock, and the paged
disruption-message carousel. Same Dot Matrix TTFs, rasterized via
`golang.org/x/image/font` + truetype. The scene/snapshot model ports to a Go tick loop.

### Data — direct RTT

`data` owns an RTT client (`api.rtt.io`, HTTP Basic auth, per-device creds from local
config), fetches the configured station's departure board, and maps to the existing
`Departure/Stop/Location/State` shape so `board` stays source-agnostic. ⚠️ RTT's payload
differs from the old Darwin feed — calling-point granularity and disruption/NRCC
"messages" may not exist; the NoServices message carousel's content is open.

## Milestones

Each milestone: TDD red/green → golangci-lint → tests gate → Codex review, tracked as a
GitHub project with milestones/issues.

- **M1 — Render core + RTT (MVP):** display driver, render engine, board scenes, RTT
  client, local config, systemd unit, fast boot. A working board fed from RTT.
- **M2 — Config Web UI:** embedded HTTP server, edit config, apply/restart, simple auth.
- **M3 — Provisioning / AP mode:** first-boot captive portal (hostapd + dnsmasq) → enter
  wifi + RTT creds → switch to client mode.
- **M4 — OTG + mDNS:** USB-gadget ethernet auto-config; advertise `trainboard.local` for
  LAN discovery.
- **M5 — Self-update:** poll GitHub releases, verify, swap binary, restart via systemd.

## Cross-cutting

- golangci-lint; unit tests as a hard pre-push gate.
- GitHub project: milestones + issues per task.
- Codex review at each milestone close.
- Fast boot: minimal systemd dependencies; show the initialising screen immediately.

## Open questions (for grilling)

- SSD1322 frame budget, SPI clock, and update strategy to sustain the fast animations.
- RTT payload mapping: calling points, disruption messages, cancellation/late reasons,
  headcode/TOC/length availability vs the old feed.
- Config format & storage (file vs embedded DB) and schema/migration.
- Web UI auth model and whether live preview is worth it.
- AP-mode trigger conditions and client-mode handoff mechanics.
- OTG gadget mode specifics; mDNS service records.
- Self-update: signature/verification, rollback, and release channel.
- Cross-compilation & CI packaging for the Pi Zero W 2 (ARM).
```
