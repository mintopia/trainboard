# Design Brief — Trainboard Web UI Redesign ("Wayfinding")

Confirmed 2026-07-09. Mockups: claude.ai artifact `49ba031e` (iteration 3).
Companion strategic doc: `/PRODUCT.md`.

## 1. Feature Summary

Full visual and UX redesign of the Pi-served admin UI in the **Wayfinding** direction:
UK station-signage language (navy totem, yellow notice band, signal-green health) set in
Rail Alphabet, phone-first. Covers all surfaces: login, first-run setup (AP and non-AP
variants), status, configuration, actions, and every wait/interstitial state.
Server-rendered Go templates + htmx stays; no SPA.

## 2. Primary User Action

- **Status (home):** confirm the board is healthy in one glance — state line + live board preview.
- **Setup:** get from hotspot to live departures without fear — route-line progress; every
  wait states its duration and fallback.
- **Config/Actions:** change a thing and know its exact consequence before committing.

## 3. Design Direction

**Color strategy: Restrained-plus.** White ground; navy `#002f63` reserved for
brand/structure/primary actions; yellow `#ffd41f` exclusively for information that needs
attention (notices, focus rings); signal green `#00733b` / red `#d4351c` only for state.
The amber board preview is the sole atmospheric element.

**Scene:** a first-time builder on their sofa, phone in hand, fresh Pi blinking across the
room, on a captive portal at 9pm — the UI reads like calm official signage announcing the
next station, not a machine room.

**Anchors:** BR corporate-identity signage (totem, Rail Alphabet, yellow information band) ·
GOV.UK content plainness · phone Settings apps (config list topology).

**Type:** Rail Alphabet Light on navy; Rail Alphabet Dark for headings/nav/buttons on
white; system Helvetica stack for body/forms; mono only inside the board preview and event
timestamps.

## 4. Scope

Production-ready, whole surface. Constraints: served by a Pi Zero W 2 (templates + one CSS
file + htmx; fonts as subset woff2; page-weight target < 150KB); works in a captive-portal
webview; WCAG AA (≥4.5:1 body contrast, 44px targets, visible focus, reduced-motion
alternatives, no color-only state).

## 5. Layout Strategy

Every page: navy totem → 6px yellow band → tab nav (Status / Configuration / Actions) →
content. Status: state line → board → notices → facts → events (glanceable → detailed).
**Config is a settings list** (Departures / Display / Network / Updates / Admin): each row
shows current values as prose and opens a sub-page with its own scoped save bar. Setup
replaces tabs with **route-line progress** (Hotspot joined → Your WiFi → Admin password →
Departures live).

## 6. Key States

- **Status:** running · degraded (stale data / Darwin errors — amber dot + reason) · fault
  (red + E-code + what-to-do) · hotspot mode (yellow notice + setup link) · update
  available · rolled-back notice · soak running.
- **Board preview:** live · stale (greyed + "data 4 min old") · unavailable (empty frame + reason).
- **Setup:** each route-line stop; the ~20s hotspot-drop wait (written fallback
  instructions that survive the server vanishing); join-failure with error + retry; done.
- **Config:** list view; sub-pages with inline validation errors, unknown-CRS state,
  unsaved-changes guard; save returns to list with the row summary updated.
- **Actions:** idle rows · inline yellow confirm expanded · in-flight ("Restarting… back in
  ~15s" with auto-reconnect) · soak-running variant.
- **Interstitials:** applying-config, rebooting, OTA installing — all reuse route-line +
  honest duration + fallback instructions.
- **Login:** totem + single password field; wrong-password state; rate-limited state with wait time.

## 7. Interaction Model

- htmx polling for events and state line; no page-load choreography; transitions ≤200ms;
  reduced-motion honored (marquee → static truncated line).
- **Board preview rendered client-side** from a new `GET /api/board` JSON row-model
  endpoint (reusing the render package's layout output): crisp text, CSS marquee for
  calling points, per-second clock. **Replaces `/preview.png`, which is deleted** — one
  render path, no per-second PNG encode on the Pi.
- CRS fields resolve station names as-you-type via bundled station table (debounced htmx GET).
- All destructive/disruptive actions: inline confirm in the yellow-notice vocabulary; no `confirm()`.
- Post-action reconnect: wait pages poll for the server's return and auto-navigate.

## 8. Content Requirements

- Voice: plain, specific, consequence-first ("Full power cycle — the board is dark for
  about a minute"). Jargon translated: "Only trains towards" (not "Destination CRS"); TOC
  codes get name hints.
- Every wait: expected duration + "if this fails" instruction, written to survive disconnection.
- **Data:** bundled CRS→station-name table (source: Rail Delivery Group open station list;
  compiled into the binary).
- **Fonts: DECIDED — ship them.** Rail Alphabet clone TTFs (BritishRailDarkNormal /
  BritishRailLightNormal, from `~/Downloads/British-Rail-Fonts-Rail-Alphabet`) → subset
  woff2, served locally. Note: clones carry no license metadata; Jess accepted shipping
  them 2026-07-09. Helvetica stack remains the built-in fallback either way.

## 9. Implementation-time References

impeccable product-register rules (component state completeness) · `clarify` for the final
copy pass · `harden` for disconnection/edge states.

## 10. Resolved Questions

1. Fonts: **ship** (see §8).
2. `/preview.png`: **delete** — the JSON renderer is the only path.

Shipped in feat/web-ui-wayfinding — 2026-07-10.
