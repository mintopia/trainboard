# Headcodes + Polish Round — Design

**Date:** 2026-07-10
**Status:** Approved
**Scope:** Four items from Jess's on-device/desktop feedback: web preview clock
fix, optional headcode column (RTT-enriched), desktop config index skip, and
update-button feedback.

## Background

The board fetches departures from Darwin OpenLDBWS (SOAP). The reference
implementation showed train headcodes (`1A23` style) because its upstream was
Darwin Push Port data (via the old prepared-data proxy), where `trainId` is
the headcode. Public LDBWS has no headcode field (`serviceID` is opaque,
`rsid` is a retail service ID). Decision: enrich LDBWS departures from the
RealTimeTrains API. This is **not** a full RTT migration — RTT is used only
to fill headcodes.

Carriage count was raised and needs **no work**: Darwin `length` already
flows to the service-info line (`ServiceInfoText`, "… formed of N coaches").

## 1. Web preview clock — seconds vertically offset (bug)

Web-only; the panel is unaffected (it blits font bitmaps at exact pixel
tops: HH:MM at y=50, :SS at y=55 via `clockSecondsDrop=5`).

**Root cause:** CSS half-leading skew in `internal/web/static/board.js`.
The HH:MM span sets `line-height:14px` with a 20px font (negative
half-leading raises glyphs ~3px); the :SS span inherits the stage's
`line-height:12px` with a 10px font (positive half-leading lowers glyphs
~1px). Net: seconds render ~4px lower relative to the hours than on glass.

**Fix:** explicit line-heights on both clock spans that zero out the
half-leading difference, adjusting the drop constant if needed so the
rendered result matches the panel. Verified by side-by-side screenshot
against a panel golden render (fixture dev-run + screenshot recipe).
JS/CSS only; no Go changes.

## 2. Optional headcode column

### Config

- `LayoutConfig.Headcodes bool`, JSON key `layout.headcodes`, **default
  false**. Checkbox on the Display page ("Show train headcodes") next to
  the calling-point-times toggle, with a hint when RTT credentials are
  missing.
- New `RTTConfig { Username, Password string }` (`rtt.username`,
  `rtt.password`). Password write-only in the web UI like the Darwin token
  ("unchanged" placeholder). Fields on the Network page below the Darwin
  token with a register link to https://api.rtt.io/.

### Data (`internal/data`)

- New RTT client: `GET https://api.rtt.io/api/v1/json/search/{crs}` with
  HTTP basic auth. Parses per-service `trainIdentity`,
  `gbttBookedDeparture` (HHMM), destination CRS/tiploc.
- `data.Departure` gains `Headcode string`.
- Enrichment step in the fetch path: when the toggle is on and creds are
  set, fetch the RTT lineup once per refresh; match each Darwin departure
  by booked departure time + destination; fill `Headcode` on match.
- **Non-fatal:** any RTT failure (auth, network, no match) logs an event
  and leaves headcodes blank. The board never degrades because of RTT.

### Panel render (`internal/board`)

`rowElements` takes the layout flag. When headcodes are on, the row becomes
`order | sched | headcode | platform | destination | status` — matching the
reference board and the reserved constants:

- headcode centered at `ColHeadcodeX=45`, `ColHeadcodeW=27`
- platform shifts +27 (x=72, w=19)
- destination starts at x=91; its box shrinks by 27 (status column fixed)

When off (default), geometry is unchanged. Golden-image tests cover rows
with and without headcodes.

### Web preview

`/api/board` adds `headcode` per service and a `headcodes` layout flag;
`board.js#rowInto` mirrors the same column shift so preview and glass agree.

## 3. Desktop: skip the config index

`/config` (section list) is kept as the **mobile** navigation. On desktop
(≥64rem shell, where the `cfgnav` master-detail rail exists) the index page
runs a tiny inline script that immediately
`location.replace("/config/departures")` — covering nav clicks and direct
visits without server-side viewport guessing. The "‹ Configuration" backrow
is already `display:none` on desktop, so no bounce loop. No route changes.

## 4. Update button feedback

- **No update available:** `handleUpdateCheck` redirects to `/?checked=1`
  (instead of `/`). The status page shows a calm notice — "You're up to
  date — vX.Y.Z is the latest release (checked just now)" — when the flag
  is set, no `.Available`, and no `.LastError`. Errors keep the existing
  red text.
- **In progress:** small status-page JS. Submitting "Install vX" disables
  the update buttons and swaps in an "Installing update — downloading and
  verifying…" notice with a CSS spinner until the (synchronous) apply
  responds and hands off to the existing `/restarting` reconnect flow.
  "Check for updates" becomes "Checking…" while in flight. No backend
  changes beyond the redirect; apply stays synchronous (indeterminate
  progress was chosen over a real progress bar).

## Testing

Red/green TDD throughout:

- RTT response parsing + departure matching (fixtures), enrichment
  fallback on RTT failure
- Config round-trip for `layout.headcodes` + `rtt.*`, form handling on
  Display/Network pages
- Board golden images: headcode rows on/off
- Handler tests: `/actions/update/check` redirect target; status template
  renders the up-to-date notice only under the right conditions
- Existing e2e route table extended where forms change
- Lint + full test gates before push; GitHub issues on a new milestone;
  Codex review of the finished work

## Out of scope

- Full RTT migration (explicitly declined)
- RTT as a carriage-count fallback
- Real download progress bar for updates
