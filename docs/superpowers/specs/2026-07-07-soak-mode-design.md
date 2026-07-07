# Burn-in soak mode — design

Date: 2026-07-07
Status: Approved (Jess, 2026-07-07)

## Purpose

The panel arrived with visible burn-in from years of the old Python board's
static layout. A soak mode cycles the OLED through full-white / full-black
recovery patterns to clear temporary image retention (and, run sparingly,
to even out differential aging). It is started and cancelled from the admin
web panel and is safe to leave running overnight.

## Requirements

- Start from the actions page with a chosen duration: **1h, 4h, or 8h**.
- Cancel at any time from the web UI; auto-stops at the deadline.
- **In-memory only**: any process restart (config apply, crash, reboot)
  ends the soak. A soak must never resume unattended.
- Works in any board state, including unconfigured/E04.
- Live preview continues to show the panel during soak.

## Architecture

Soak is an **operator override sitting above scene selection** in the
render loop — not a board state, not config. The loop keeps sole ownership
of the SPI panel throughout.

### Soak controller — `internal/runtime/soak.go`

```go
type Soak struct { mu sync.Mutex; deadline time.Time }
func (s *Soak) Start(d time.Duration, now time.Time) // deadline = now+d; while active, resets the deadline
func (s *Soak) Cancel()                              // zero deadline; no-op when idle
func (s *Soak) Remaining(now time.Time) time.Duration // 0 = inactive
```

`cmd/trainboard` constructs one `*runtime.Soak` and hands it to both the
loop and the web service layer (same wiring pattern as restart).

### Render loop — `internal/runtime/loop.go`

At the top of `step(now)`, if `soak.Remaining(now) > 0`:

- Fill the framebuffer **full-white (0xF) or full-black (0x0)** on a
  **2-second phase** derived from wall clock: `now.Unix()/2 % 2`.
- Force contrast to **0xFF** (applied once, via the existing
  last-applied-brightness tracking).
- Flush as normal; skip scene build/render entirely.

Nothing is drawn over the pattern — no countdown, no label; any static
element during soak defeats the purpose. Remaining time lives on the
status page.

On soak end (expiry or cancel): reset the loop's `brightness` tracker to
`-1` so the powersave schedule reapplies next tick; scene rendering
resumes where it left off (no forced rebuild).

### Web surface

- **Actions page**: soak section — duration select (1h/4h/8h) + Start;
  while active, remaining time + Cancel button.
- **Routes** (auth + CSRF, same middleware chain as restart):
  - `POST /actions/soak` — form field `duration` validated against the
    fixed set {1h, 4h, 8h}
  - `POST /actions/soak/cancel`
  - JSON API mirrors: `POST /api/actions/soak`,
    `POST /api/actions/soak/cancel` (M2 parity pattern)
- **Status page + `/api/status`**: soak-active indicator with remaining
  time (page already polls; countdown updates naturally).
- **E2E route-matrix tripwire**: all four new routes added.

## Error handling

- Invalid/missing duration → form error on the actions page; 400 on API.
- Start while already active → deadline reset (idempotent; not an error).
- Cancel while idle → no-op (redirect / 200).
- Flush errors unchanged: fatal, systemd restarts the unit — which also
  (correctly) ends the soak.

## Testing

- **Soak unit tests**: start → active, expiry at deadline, cancel,
  start-while-active resets deadline. All with injected `now`.
- **Loop tests** (`step(now)` + fake flusher): soak renders uniform
  0xF/0x0 packed frames; phase flips on the 2s boundary; contrast forced
  to 0xFF; after expiry the schedule contrast and scene rendering resume.
- **Web tests**: handler start/cancel/validation, auth/CSRF via the
  existing route matrix, API parity, status page + JSON soak fields.

## Out of scope

- Persisting soak across restarts (explicitly rejected).
- Anti-burn-in layout jitter for normal rendering — separate M3-backlog
  candidate.
- Greyscale sweep or checkerboard patterns; plain white/black cycling is
  the standard retention-recovery treatment and keeps the loop trivial.
