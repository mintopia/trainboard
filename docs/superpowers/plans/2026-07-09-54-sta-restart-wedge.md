# #54 — STA restart wedge fix

**Root cause (diagnosed on hardware):** wpa_supplicant lives in trainboard.service's cgroup and dies on every service stop (KillMode=control-group default). The STA path (`staAttempt` → `wpa_cli reconfigure`) assumes a running daemon and fails instantly; #47's fast retry re-fires into the same wall. Only `StartAP` has `ensureDaemon` (spawn + ctrl-socket wait). Result: every cold/warm start pays one failed STA + AP fallback + 5-min retry cadence.

**Considered and rejected:** `KillMode=process` (daemon survives restarts → zero-outage warm restart) — conflicts with the M3 owned-lifecycle/clean-slate design and #46's kill-before-start discipline, and does nothing for cold boot. The grace loop covers both cases robustly.

## Changes

### 1. Driver: STA path ensures the daemon (internal/net)
- `staAttempt` (driver.go) gains an `assocPolls int` parameter (assoc wait budget). mode2 passes `staAssocPolls = 20` (10s — covers post-spawn scan+associate AND the AP→STA switch, the observed 18:26 retry failure); hostapdDriver keeps `pollAttempts` (10).
- `mode2Driver.AttemptSTA`: before `staAttempt`, write the conf (existing renderConf/writeFile pieces — conf must exist before a spawn) and call `d.ensureDaemon(ctx)` — identical spawn + socket-wait StartAP uses. `staAttempt`'s own write+reconfigure stays (idempotent for the already-running branch).
- FakeRunner tests: cold daemon (first `wpa_cli status` errs) → AttemptSTA spawns wpa_supplicant, waits for socket, associates; warm daemon → no spawn, reconfigure path.

### 2. Manager: boot grace period replaces the fast retry (internal/net/manager.go)
- New package vars (test-shrinkable like staAttemptBound): `bootSTAGraceWindow = 90 * time.Second`, `bootSTARetryPause = 5 * time.Second`.
- `bootSTA` becomes a grace loop: attempt `toSTA`; on success done; `errNoWifiConfigured` → immediate return (unconfigured devices keep fast AP); ctx cancelled → return; past `m.now().Add(bootSTAGraceWindow)` deadline → return last error (concede to AP). Between attempts: log the failure + reason (Change 3), `m.d.Beat()` if non-nil, wait `bootSTARetryPause` via `m.d.After` (select against ctx.Done).
- Delete `firstAttempt`/`fastRetried`/`fastRetryThreshold` (subsumed: an instant failure just retries 5s later, and again, for 90s). Update/replace their tests.
- Watchdog arithmetic (update the comment near runAPWait's): per-iteration Beat keeps the max un-beaten gap at [last attempt ≤45s] + [toAP ≤40s] ≈ 85s < 150s deadline — no worse than today's single-attempt chain; the loop never lengthens a single gap.
- Injected-clock tests: instant-failure storm converges within window (attempts repeat, Beats fire); success mid-window → phaseOnlineWait; no credentials → single attempt, straight to AP; window exhaustion → AP; ctx cancel mid-pause exits cleanly.

### 3. Observability: log STA attempt failures
- Grace loop: `net: manager: STA attempt failed; retrying within grace window` with err + remaining.
- `runAPWait`'s scheduled-retry failure (currently silent): log err before restoring AP.

## Gate
Standard: vet, golangci-lint, `go test -race ./... -count=1`. Hardware verify (attended): `systemctl restart trainboard` should rejoin STA in seconds without an AP detour.
