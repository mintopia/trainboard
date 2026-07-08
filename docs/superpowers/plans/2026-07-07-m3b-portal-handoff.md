# M3b: Captive Portal + Credential Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The user-facing half of M3: captive portal on the AP, WiFi+password partial setup, credential handoff with AP-restore error reporting, E05/E06 on-glass, the deferred M3a quality items, and the bench-session protocol artifacts.

**Architecture:** The web layer reaches the connectivity Manager only through new func seams on `web.Sources`/`web.Actions` (M2/M3a idiom; `internal/web` never imports `internal/net`). The composite snapshot source grows fault injection so connectivity stages render on the panel. The Manager itself changes minimally (one Status field, one bounded call, country threading). Spec: `docs/superpowers/specs/2026-07-07-m3-connectivity-ap-design.md` (Delivery plan 2); deferred-items list in PR #42's body.

**Tech Stack:** Go stdlib only. No new dependencies.

## Global Constraints

- Branch `feat/m3b-portal` off `main`. TDD red→green per task; `make check` exit 0 before any push.
- `internal/web` must NOT import `internal/net`; `internal/net` must NOT import `internal/web`. All cross-talk via funcs wired in `cmd/trainboard`.
- No `os/exec` outside `internal/net/runner.go` (grep-gated). No raw `time.Sleep`/`time.Now` in `internal/net` logic (injected seams only).
- AP subnet literals: AP IP `192.168.4.1`, subnet `192.168.4.0/24`. Probe paths exactly: `/generate_204`, `/hotspot-detect.html`, `/ncsi.txt`.
- AP-mode `/setup` collects **WiFi credentials + admin password ONLY** (operator decision); LAN-mode `/setup` keeps its current fields. Partial saves go through `config.SaveConnectivity`.
- Suppression signal: HTTP activity **from the AP subnet** (which implies a DHCP lease — conscious simplification of the spec's "lease AND HTTP" pair; a mere association without traffic still never suppresses). Record this in the code comment where NoteProvisioning is wired.
- E2E route-matrix tripwire (`internal/web/e2e_test.go`) gains a row for EVERY new route, including the pre-auth probe routes (documented exception arm like /setup's).
- The existing E2E/security invariants must hold: probe routes are the ONLY new pre-auth surface, and they leak nothing (no config, no status, no redirect with secrets).

---

### Task 1: Web connectivity seams

**Files:**
- Modify: `internal/web/service.go` (Sources ~line 17, Actions ~line 27)
- Modify: `internal/web/handlers_config_test.go` (`newConfigTestServer` fakes)
- Test: `internal/web/service_test.go` (append)

**Interfaces:**
- Produces (consumed by Tasks 2, 3, 5):

```go
// Sources gains (nil-tolerated, like SoakRemaining):
	// Hotspot reports the connectivity manager's AP-mode identity; nil =
	// not in AP mode (or --manage-network off).
	Hotspot func() *board.Hotspot
	// LastSTAError is the most recent failed WiFi-join error, preserved
	// across AP restore for the reconnecting provisioning user; "" = none.
	LastSTAError func() string

// Actions gains (nil-tolerated):
	// WifiRetry asks the manager to attempt the configured WiFi now
	// (tears the AP down; the hotspot drops). No-op when nil.
	WifiRetry func()
	// NoteProvisioning marks live provisioning activity so the manager
	// suppresses its periodic retry. No-op when nil.
	NoteProvisioning func()

// Service methods:
func (s *Service) Hotspot() *board.Hotspot      // nil-safe
func (s *Service) LastSTAError() string          // nil-safe ""
func (s *Service) WifiRetryNow()                 // nil-safe no-op
func (s *Service) MarkProvisioning()             // nil-safe no-op
```

- Harness: `newConfigTestServer` wires working fakes backed by plain vars (`var hs *board.Hotspot; var lastErr string; retries := 0; provNotes := 0`) and RETURNS access to them — extend the helper's return or add a small struct; keep every existing call site compiling (Go lets you add a trailing return via a struct field instead: add `srv.testConn = ...`? NO — simplest compatible shape: package-level helper `newConnTestServer(t)` that wraps `newConfigTestServer` and returns `(srv, svc, path, applyCh, conn *connFakes)` with `type connFakes struct{ hs *board.Hotspot; lastErr string; retries, provNotes int; mu sync.Mutex }` wired into the Sources/Actions before `NewServer`. Refactor `newConfigTestServer` minimally so both exist).

- [ ] **Step 1: Write the failing tests**

```go
func TestServiceConnectivitySeams(t *testing.T) {
	srv, svc, _, _, conn := newConnTestServer(t)
	_ = srv
	if got := svc.Hotspot(); got != nil {
		t.Fatalf("no AP mode: Hotspot() = %v, want nil", got)
	}
	conn.set(&board.Hotspot{SSID: "Trainboard-AB12", Password: "pw", Addr: "192.168.4.1"}, "join failed: wrong PSK")
	if got := svc.Hotspot(); got == nil || got.SSID != "Trainboard-AB12" {
		t.Fatalf("Hotspot() = %v", got)
	}
	if got := svc.LastSTAError(); got != "join failed: wrong PSK" {
		t.Fatalf("LastSTAError() = %q", got)
	}
	svc.WifiRetryNow()
	svc.MarkProvisioning()
	if r, p := conn.counts(); r != 1 || p != 1 {
		t.Fatalf("retry/prov counts = %d/%d, want 1/1", r, p)
	}
}

func TestServiceConnectivityNilSeamsSafe(t *testing.T) {
	src := Sources{Snapshot: func() *board.Snapshot { return nil }, Ring: obs.NewRing(1),
		PreviewPNG: func() []byte { return nil }, StartedAt: time.Now()}
	svc := NewService("/nonexistent", src, Actions{}, testLog())
	if svc.Hotspot() != nil || svc.LastSTAError() != "" {
		t.Fatal("nil seams must read as inactive")
	}
	svc.WifiRetryNow()      // must not panic
	svc.MarkProvisioning()  // must not panic
}
```

- [ ] **Step 2: Red** — `go test ./internal/web/ -run TestServiceConnectivity` → FAIL undefined.
- [ ] **Step 3: Implement** the fields/methods exactly as the Interfaces block; wire `connFakes` (mutex-guarded set/counts helpers) in the test harness.
- [ ] **Step 4: Green** — full `go test -race ./internal/web/`.
- [ ] **Step 5: Commit** — `feat(web): connectivity seams — hotspot state, last STA error, retry-now, provisioning note`

---

### Task 2: Provisioning-activity middleware + wifi-retry action

**Files:**
- Modify: `internal/web/middleware.go` (new middleware), `internal/web/server.go` (route + Handler chain), `internal/web/handlers_actions.go` (+ template `internal/web/templates/actions.html`), `internal/web/handlers_api.go` (API mirror)
- Test: `internal/web/middleware_test.go`, `internal/web/handlers_actions_test.go`, `internal/web/e2e_test.go` (matrix rows)

**Interfaces:**
- Consumes Task 1's `Service.MarkProvisioning`/`WifiRetryNow`/`Hotspot`.
- Produces: `noteProvisioning(svc *Service) middleware` — outermost-adjacent middleware calling `svc.MarkProvisioning()` for any request whose `RemoteAddr` host parses inside `192.168.4.0/24`; routes `POST /actions/wifi-retry` (auth+CSRF chain like restart; 302 → `/actions`) and `POST /api/actions/wifi-retry` (apiJSONErrors chain; 200 `{"status":"retrying"}`).

Behavior details:
- Middleware sits in `Handler()`'s chain after `logRequests` (every request counts, including probes and static — the point is "a human is actively using the AP").
- Parse once: `ip, _, err := net.SplitHostPort(r.RemoteAddr)`; `apNet = net.IPNet{IP: net.IPv4(192,168,4,0), Mask: net.CIDRMask(24,32)}` as a package var; on parse failure do nothing.
- Actions page gains, inside a `{{if .HotspotActive}}` block, copy: "Board is in hotspot mode — retry the configured WiFi now (the hotspot will drop for ~20 seconds)" + the retry form; `actionsPageData` gains `HotspotActive bool` (from `svc.Hotspot() != nil`).

- [ ] **Step 1: Failing tests** — middleware: requests with RemoteAddr `192.168.4.55:41000` increment the fake's provNotes; `192.168.3.10:5` and garbage RemoteAddr don't. Actions: authed POST /actions/wifi-retry → 302 `/actions` + retries==1; API mirror → 200 JSON + retries increments; actions page shows the retry form ONLY when hotspot fake set. Matrix rows for both routes (302→/actions form; API 200). Write fully, following each file's existing style.
- [ ] **Step 2: Red.** **Step 3: Implement.** **Step 4: Green** (`-race`, full package). **Step 5: Commit** — `feat(web): AP provisioning-activity notes + wifi retry-now action`

---

### Task 3: Captive-portal probe endpoints + AP-mode host handling

**Files:**
- Modify: `internal/web/server.go` (routes + setupGate), `internal/web/middleware.go` (originCheck exemption if needed — read it first)
- Create: `internal/web/handlers_portal.go`
- Test: `internal/web/handlers_portal_test.go`, `internal/web/e2e_test.go`

**Interfaces:**
- Consumes `Service.Hotspot()`.
- Produces routes (NO auth, NO CSRF — they must work for a just-associated phone with wildcard DNS):
  - `GET /generate_204` — AP mode: `302 Location: http://192.168.4.1/setup` (a non-204 answer is what pops Android's "sign in" sheet). Not AP mode: `404`.
  - `GET /hotspot-detect.html` — AP mode: 200 HTML `<html><body>Redirecting to setup… <a href="http://192.168.4.1/setup">setup</a></body></html>` with `Content-Type: text/html` and a `<meta http-equiv="refresh" content="0;url=http://192.168.4.1/setup">` (iOS CNA renders this and follows). Not AP: 404. (Anything ≠ "Success" pops the sheet; a redirect status can confuse older CNAs, hence 200+meta.)
  - `GET /ncsi.txt` — AP mode: `302` to the setup URL (≠ "Microsoft NCSI" body triggers Windows). Not AP: 404.
- setupGate change: its redirect target becomes ABSOLUTE `http://192.168.4.1/setup` when `svc.Hotspot() != nil` (a probe-following phone carries `Host: connectivitycheck.gstatic.com` — a relative `/setup` redirect would resolve against the wrong host and the CNA would re-resolve it via wildcard DNS back to us anyway, but the absolute form is deterministic and matches the on-screen URL). LAN mode unchanged (relative).
- originCheck audit: it gates state-changing requests by Origin==Host. AP-mode form POSTs arrive with `Host: 192.168.4.1` and `Origin: http://192.168.4.1` — matching; no change expected. VERIFY with a test rather than assuming; if the CNA context strips Origin, absent-Origin is already allowed. Do not widen originCheck.

- [ ] **Step 1: Failing tests** — table per probe path × {AP mode, not AP} asserting exact status/Location/body-contains; setupGate AP-mode absolute redirect (request with `Host: connectivitycheck.gstatic.com`, no session, path `/` → 302 `http://192.168.4.1/setup`); matrix rows: the three probes listed in the documented pre-auth exception arm (like /setup — read the matrix's exception comment and extend it) asserting AP-mode 302/200 AND non-AP 404 so the tripwire pins both.
- [ ] **Step 2: Red.** **Step 3: Implement** (probe handlers keyed on `s.svc.Hotspot() != nil`; setup URL const `apSetupURL = "http://192.168.4.1/setup"`). **Step 4: Green.** **Step 5: Commit** — `feat(web): captive-portal probe endpoints + AP-mode absolute setup redirect`

---

### Task 4: STA config re-read (handoff prerequisite)

**Files:**
- Modify: `cmd/trainboard/main.go:151-152, 252-257`, `cmd/trainboard/connectivity.go`
- Test: `cmd/trainboard/connectivity_test.go` (append)

**Interfaces:**
- Produces: `staFromDisk(cfgPath string) func() netconn.STAConfig` in connectivity.go — reads `config.LoadRaw(cfgPath)` on EVERY call, returning `STAConfig{SSID: raw.Wifi.SSID, PSK: raw.Wifi.PSK}` (zero STAConfig on read error). Both boot paths use it (the E04 path's hardcoded empty-STA closure is replaced — a portal-saved config must be joinable without a process restart, which is the entire credential-handoff flow: portal saves → RetryNow → manager calls STA() → fresh creds).

Why LoadRaw: the portal saves a connectivity-valid (not board-valid) config; `config.Load` would reject it. LoadRaw's doc comment already scopes it to connectivity wiring — extend that comment to name this second caller.

- [ ] **Step 1: Failing test** — write a config file with wifi creds via `config.SaveConnectivity` (password hash set), assert `staFromDisk(path)()` returns them; overwrite the file with new creds, assert the SAME closure returns the new ones (the re-read property); missing file → zero STAConfig.
- [ ] **Step 2: Red.** **Step 3: Implement + rewire both call sites.** **Step 4: Green** — `go test -race ./cmd/trainboard/` + `make check`. **Step 5: Commit** — `feat(cmd): STA credentials re-read from disk per attempt — portal saves apply without restart`

---

### Task 5: AP-mode partial /setup + credential handoff flow

**Files:**
- Modify: `internal/web/server.go` (setup handlers), `internal/web/templates/setup.html`, `internal/web/service.go` (`SetupConnectivity` method), `internal/web/templates.go` (if a new template block is cleaner — implementer's call, follow existing patterns)
- Create: `internal/web/templates/setup_wifi_done.html`
- Test: `internal/web/server_test.go` or `handlers_config_test.go` area (follow where setup tests live — grep `TestSetup`), `internal/web/e2e_test.go` if any route shape changes (none expected — same /setup paths)

**Interfaces:**
- Consumes: Task 1 seams, `config.SaveConnectivity` (via a new Service method), `Service.WifiRetryNow`, `Service.LastSTAError`, `Service.Hotspot`.
- Produces: `func (s *Service) SetupConnectivity(pw, ssid, psk string) error` — mirrors `SetInitialPassword`'s shape (load-tolerant, refuse when a password already exists, hash, set `Wifi.SSID/PSK`, `ValidateConnectivity`, `SaveConnectivity`). Wifi fields required in this path (that's its purpose): reject blank SSID with "wifi network name is required".

Handler/template behavior (`handleSetupGet`/`handleSetupPost`):
- `setupPageData` gains `APMode bool`, `LastError string` (from `svc.LastSTAError()` — shows the previous failed join to the reconnecting user, per spec).
- GET: `APMode = svc.Hotspot() != nil`. Template: `{{if .APMode}}` → WiFi SSID (text), WiFi password (password, 8-63), admin password + confirm, submit copy "Save & join WiFi — the hotspot will drop for ~20 seconds while the board tries your network. If it fails, rejoin the hotspot and this page will show the error." `{{else}}` → existing three-field form unchanged.
- POST with APMode: validate confirm match → `SetupConnectivity` → on error re-render with message → on success render `setup_wifi_done.html` ("Joining your WiFi… the hotspot is about to drop. If the board doesn't appear on your network in a minute, reconnect to {{.SSID}} and revisit this page.") **then call `svc.WifiRetryNow()` AFTER the response is written** (mirror `scheduleApply`'s `time.AfterFunc(applyDelay, ...)` pattern — same constant) so the phone gets the page before the AP drops. NO process restart in this path (the manager + Task 4's disk re-read make the new creds live).
- POST without APMode: existing behavior byte-identical.
- setupGate: unchanged (password-presence). After a successful partial setup the device HAS a password → gate lifts → post-join, the LAN user logs in and finishes CRS/token at /config (E04 screen already shows the URL — verify the E04 scene includes the device URL; if it doesn't, that's the errorScene's existing E04 message + status-page IPs, acceptable — do NOT scope-creep the scene here).

- [ ] **Step 1: Failing tests** — SetupConnectivity: happy path writes connectivity-valid config (LoadRaw it back: hash set, wifi set); blank SSID rejected; existing-password rejected; short PSK rejected (via validateWifi). Handler: AP-mode GET renders wifi fields + last-error when fake set; AP-mode POST success → done page + retry called after applyDelay (drain a fake channel or count with the harness after sleeping past applyDelay — follow how existing applied-page tests handle the AfterFunc timing); LAN-mode GET/POST regression: existing setup tests must pass UNCHANGED.
- [ ] **Step 2: Red.** **Step 3: Implement.** **Step 4: Green** full package. **Step 5: Commit** — `feat(web): AP-mode partial setup (WiFi + admin password) with credential handoff`

---

### Task 6: E05/E06 on-glass

**Files:**
- Modify: `internal/net/manager.go` (Status field), `internal/runtime/composite.go`, `internal/board/snapshot.go`, `internal/board/scenes.go` (errorScene), `cmd/trainboard/main.go` (composite wiring gains the fault source)
- Test: each package's existing test file

**Interfaces:**
- `net.Status` gains `RadioBlocked bool` — set true when `toSTA`'s Prereqs call fails (and cleared on the next transition attempt); the failure still routes to AP fallback as today.
- `board.Snapshot` gains `FaultDetail string` (rendered, not part of equality-sensitive logic — grep for Snapshot comparisons first; the composite compares pointers and Hotspot values only).
- `errorScene(fault, detail string...)`: when `detail != ""` add `centered(f.Regular, detail, 36)` — read the current errorScene layout first and pick the y that doesn't collide (message at 24, fault corner at 52).
- `runtime.HotspotSnapshotSource` gains a third parameter: `conn func() (stage string, radioBlocked bool)` (nil = feature off). Injection rule, applied ONLY when `hs == nil` (AP scene outranks faults) AND the base snapshot's State is `StateInitialising` or `StateError` (never mask live departures/stale grace): radioBlocked → clone with `Fault: obs.FaultRadioBlocked`, `State: StateError`; else stage != "" → clone with `Fault: obs.FaultConnectivity`, `FaultDetail: stage`, `State: StateError`. Pointer-stability contract extends: cache key now includes the (stage, radioBlocked) pair.
- cmd wiring: `func() (string, bool)` adapter over `mgr.Status()` (Stage string + RadioBlocked), passed in both boot paths; nil when --manage-network off.

- [ ] **Step 1: Failing tests** — manager: Prereqs error → Status.RadioBlocked true, cleared after a subsequent successful transition attempt (extend an existing toSTA test). composite: fault injected only in the allowed base states (table: initialising→E06+detail; error/E04→E06 overrides fault but... DECISION: E04 (config error) is more actionable than E06 — do NOT override an existing non-empty base Fault; only inject when base Fault is empty or E01. Pin that with a test row); pointer stable across identical (base, hs, stage) triples; new pointer on stage change; hs non-nil wins over stage. board: errorScene golden/substring with detail line. Write fully.
- [ ] **Step 2: Red.** **Step 3: Implement.** **Step 4: Green** across `./internal/net ./internal/runtime ./internal/board ./cmd/...` + goldens rule: only hotspot/error-scene goldens may change; anything else = STOP and report.
- [ ] **Step 5: Commit** — `feat(net,runtime,board): E05/E06 connectivity faults rendered on-glass with stage detail`

---

### Task 7: Deferred M3a quality items

**Files:**
- Modify: `internal/config/config.go` (+validate), `internal/net/prereq.go`, `internal/net/driver_mode2.go`, `internal/net/driver_hostapd.go`, `internal/net/manager.go` (bounded online recheck), `cmd/trainboard/connectivity.go` (threading)
- Test: respective test files; `internal/runtime/composite_test.go` (stress); `internal/net/driver_mode2_test.go` (stateful stub)

Four items, one commit each:
1. **Regulatory country from config**: `config.WifiConfig` gains `Country string` (json `country`), default `"GB"` in `Default()`, validated as exactly 2 uppercase ASCII letters when set (empty → treated as GB at the consumers); threaded: `CheckPrereqs` gains a `country string` param (replaces the GB literal in `iw reg set`), both drivers' conf templates take it (mode2 `country=`, hostapd `country_code=`), `startConnectivityManager` passes `cfg.Wifi.Country` (defaulting "GB" when empty). Update every affected existing test's expected conf/argv strings mechanically.
2. **Bound the online recheck**: in `runOnlineWait`, wrap `m.d.Check.Evaluate` in `context.WithTimeout(ctx, staAttemptBound)` exactly as `toSTA` does (same parent-vs-child cancel discipline; the d23a7d7/ac67591 tests must stay green). Test: blocking probe that returns on ctx death → recheck classified as degradation (proceeds to reattempt), not cancellation.
3. **Composite stress test**: `-race` test with one goroutine calling the source at high frequency while another flips the hotspot value and a third flips stage; assert no race (the run itself) and that every returned pointer is internally consistent (Hotspot xor fault rules hold on each returned snapshot).
4. **mode2 ensureDaemon branch**: add a small stateful runner stub IN THE TEST FILE (sequence-aware: returns error on the FIRST `wpa_cli -i wlan0 status` call, scripted success after) proving the daemon-start branch issues `wpa_supplicant -B -i wlan0 -c /run/trainboard-wpa.conf` exactly once, then proceeds.

- [ ] Steps: per item — failing test → red → implement → green → commit (`feat(config,net): regulatory country from config`, `fix(net): bound online recheck like STA attempts`, `test(runtime): composite concurrent stress`, `test(net): cover mode2 daemon-start branch`).

---

### Task 8: Bench protocol artifacts + docs

**Files:**
- Create: `deploy/bench/eval-mode2.sh`, `deploy/bench/destructive-matrix.md`
- Modify: `docs/deploy.md` (§8), `docs/benchmarks/README.md` (pointer)

No Go code; gate is shellcheck-clean bash (run `shellcheck deploy/bench/eval-mode2.sh` if available, else `bash -n`) + `make check` still green.

`eval-mode2.sh` (runs ON the Pi as root, Jess-as-rescue mode): takes `--minutes N` (default 20) dead-man budget. Structure:
1. Preflight: verify `wpa_supplicant`/`dnsmasq` present (install dnsmasq via apt if missing, prompt first), snapshot current network state (`ip addr`, `wpa_cli status` if running, copies of /etc/network/interfaces + any wpa conf) into `/root/bench-backup-$(date +%s)/`.
2. Arm dead-man: `systemd-run --on-active=${MINUTES}m --unit=bench-deadman.service sh -c 'cp -r <backup>/* <original locations>; reboot'` — VERIFY scheduled before any wlan0 mutation; abort if not.
3. Experiment 1: write the M3a mode2 conf (both network blocks; AP `Trainboard-BENCH`, password from `--ap-pass`, GB), start wpa_supplicant with it, `select_network 1`, poll status 10×; report `wpa_state`/`mode`; start dnsmasq with the M3a conf; PROMPT the operator: "associate a phone, confirm lease + http://192.168.4.1 loads [y/n]".
4. Experiment 2: 10× AP↔STA toggle via `select_network 0`/`1`, timing each transition to COMPLETED, logging failures; abort the loop (not the script) after 2 consecutive wedges.
5. Experiment 3: bad-PSK STA attempt (deliberately wrong PSK in network 0) → assert AP restore (select 1, status mode=AP, dnsmasq alive).
6. Teardown: restore from backup, disarm dead-man (`systemctl stop bench-deadman.timer 2>/dev/null; systemctl reset-failed`), print verdict table (per-experiment PASS/FAIL + timings) for pasting into issue #7 and the ADR 0003 addendum.

`destructive-matrix.md`: the #13 checklist as a table — bad PSK, missing SSID, DHCP timeout (dnsmasq killed), daemon crash (`pkill -9 wpa_supplicant` mid-AP), reboot mid-transition, client associated during retry — each with: setup commands, expected board behavior (which scene/fault), expected recovery, observed column to fill at the bench.

`docs/deploy.md` §8 gains: dnsmasq install step, wlan0 ifupdown-migration steps (comment out wlan0 in `/etc/network/interfaces`, disable `ifup@wlan0.service`, THEN enable `--manage-network` — with the boot-time note that WiFi is manager-owned from then on), pointer to `deploy/bench/`. Statement that the mode2-vs-hostapd verdict lands as an ADR 0003 addendum after the bench session (not before).

- [ ] Steps: write both artifacts + doc edits → `bash -n deploy/bench/eval-mode2.sh` clean → `make check` → commit `docs(bench): mode2 evaluation script w/ dead-man switch + destructive matrix + migration guide`.

---

## Post-plan (not tasks)

- Final whole-branch review (opus) → PR → merge.
- Bench session with Jess: run eval-mode2.sh + matrix → driver verdict → ADR 0003 addendum → close #6/#7/#11/#12/#13.

## Self-review notes

- Spec coverage: portal probes #11 ✅ T3; handoff #12 ✅ T4+T5 (bounded attempt shipped in M3a); partial setup ✅ T5; suppression input ✅ T2; retry-now ✅ T2; E05/E06 on-glass ✅ T6; deferred M3a items ✅ T7 (sd_notify logging judged note-only by final review — omitted deliberately; virgin-churn resolved BY T5: partial setup sets the password, enabling persistence); bench #13/#6-remainder/#7-remainder ✅ T8. M4/M5 untouched.
- Type consistency: `Service.Hotspot()/LastSTAError()/WifiRetryNow()/MarkProvisioning()` (T1) used in T2/T3/T5; `staFromDisk` (T4) used nowhere else (cmd-only); composite third param signature `func() (string, bool)` consistent T6/cmd.
- Placeholders: none; T3's originCheck item and T5's E04-scene item are verify-don't-assume instructions with explicit no-scope-creep bounds.
