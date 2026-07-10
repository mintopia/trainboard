# Product

## Register

product

## Users

Two audiences, one surface:

1. **Jess (owner/maintainer)** — checks on or reconfigures her own board from her phone, usually at home in the evening, often while the physical amber OLED is glowing across the room. Knows every knob; wants speed and glanceable health.
2. **OSS installers** — hobbyists who built their own trainboard from this repo. They arrive at the web UI mid-flash of adrenaline: fresh Pi, first boot, joined the `trainboard` hotspot, phone in hand. They have never seen this UI before and are anxious about bricking the thing. First-run setup (AP mode → WiFi join → Darwin token → first departures) is their entire first impression of the project.

Primary device is a phone; desktop is secondary. The UI is served from a Pi Zero W 2 — payloads must stay tiny (no heavy frameworks; currently htmx + one small CSS file).

## Product Purpose

Admin and setup UI for a Raspberry Pi UK train departure board (SSD1322 256×64 OLED). It exists so the device never needs SSH: first-time provisioning, WiFi recovery, board configuration (station, platforms, operators), OTA software updates, and at-a-glance health (live panel preview, state, events). Success = an OSS installer gets from first boot to live departures without opening the README twice, and Jess can diagnose "why is the board blank" in under ten seconds.

## Brand Personality

**Provisional — visual direction is being explored via mockups (2026-07-09).**

Working hypothesis: *precise, reassuring, railway-literate*. The product it controls is a lovingly-recreated piece of UK railway furniture; the admin UI should feel like it was made by the same people — confident about rail domain details (CRS codes, TOCs, Darwin), calm when things go wrong (WiFi drops, rollbacks), never corporate. Voice: plain, specific, slightly warm; explains consequences before destructive actions ("the hotspot will drop for ~20 seconds").

## Anti-references

- **Generic IoT vendor portals** (TP-Link/Tuya style) — cluttered dashboards, icon soup, badge-count noise.
- **Default Bootstrap/admin-template look** — the "works but nobody designed it" feel the current UI has.
- **SaaS dashboard clichés** — hero metrics, card grids, gradient accents. This is a tool for one small device, not an analytics product.
- **Faux-terminal kitsch** — if a retro/CRT direction is chosen it must be executed as considered industrial design, not scanline-overlay cosplay.

## Design Principles

1. **The board is the hero.** The live panel preview is the single most informative element — health, config, and data freshness all visible in one image. Design around it.
2. **Anxiety-aware flows.** Setup and recovery involve real dead air (hotspot drops, reboots, OTA restarts). Every wait needs honest progress, expected duration, and a "what to do if this fails" path.
3. **Glanceable health, progressive detail.** State first (one word + color), facts second, event log last. Never make Jess scan a table to learn the board is fine.
4. **Consequences before commitment.** Any action that restarts/reboots/drops connectivity says so up front, in specific terms, not via `confirm()` popups.
5. **Weightless by design.** Server-rendered HTML + htmx + one stylesheet. Design decisions must survive a Pi Zero serving them over 2.4GHz WiFi.

## Accessibility & Inclusion

WCAG AA baseline: ≥4.5:1 body contrast (audit any amber-dim-on-black text), 44px touch targets (already present — keep), visible focus states, reduced-motion alternatives for any animation, no color-only state signalling (pair color with text/shape in status and event levels).
