# M3a: Connectivity Core (host-buildable) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `internal/net` connectivity core — Runner seam, layered check, mode2/hostapd drivers, Manager state machine with AP-restore invariant — plus fault codes, config validation tiers, hotspot scene/snapshot plumbing, watchdog aggregation, and poller poke. Everything host-testable; no captive portal or web surface (M3b).

**Architecture:** One Manager goroutine owns wlan0, driving all OS side effects through a `Runner` command seam and an `apDriver` interface (mode2 = wpa_supplicant native AP via `wpa_cli select_network`; hostapd = daemon handoff). Layered check evaluates association→DHCP→DNS→captive-trap; Darwin reachability is delegated to the poller. Spec: `docs/superpowers/specs/2026-07-07-m3-connectivity-ap-design.md`; ADR 0003.

**Tech Stack:** Go stdlib only (os/exec, net, atomic). No new Go dependencies.

## Global Constraints

- Branch `feat/m3a-connectivity` off `feat/m3-design`. TDD red→green per task; `make check` green before any push.
- Every OS side effect goes through `net.Runner` — **no `os/exec` outside `internal/net/runner.go`**, no file writes outside the paths named here.
- Device binary inventory (verified on the bench Pi, DietPi Bookworm): `wpa_supplicant`/`wpa_cli`/`iw`/`ip`/`dhclient` present at /usr/sbin; **`hostapd`, `dnsmasq`, `rfkill` NOT installed**. STA DHCP client is therefore exactly `dhclient`; rfkill state is read/written via sysfs (`/sys/class/rfkill/*/soft`), never the missing binary; dnsmasq/hostapd installation is an M3b deploy step — M3a code may reference their argv/confs but nothing in M3a runs on-device.
- AP identity: SSID `Trainboard-XXXX` (last 4 uppercase hex of wlan0 MAC), WPA2-PSK password from `config.Provisioning.APPassword`, AP address `192.168.4.1/24`.
- Retry loop: every **5 minutes**; bounded STA attempt ≤45s total across layers; suppression = DHCP lease AND HTTP activity both within **90s**.
- Fault codes: `E05` = rfkill/regulatory blocked; `E06` = connectivity stage failure with stage name. AP mode is a scene (Snapshot.Hotspot), never a fault.
- Clocks and randomness injected everywhere (`now func() time.Time` seams, matching Poller/Soak style). No `time.Now()`/`time.Sleep` in `internal/net` logic paths — waits go through injected timers or the Manager's tick channel as specified below.
- Package doc comments follow the existing style (see internal/runtime/classify.go header).

---

### Task 1: `net.Runner` — command seam + scripted fake

**Files:**
- Create: `internal/net/net.go` (package doc), `internal/net/runner.go`
- Test: `internal/net/runner_test.go`

**Interfaces:**
- Produces (every later task consumes):

```go
// Runner executes one external command and returns its combined output.
type Runner interface {
	Run(ctx context.Context, argv ...string) (string, error)
}

// ExecRunner is the production Runner (os/exec).
func NewExecRunner() Runner

// FakeRunner (in runner_test.go? NO — export it: internal/net/fakerunner.go,
// it is consumed by every other package's tests) scripts responses keyed by
// the joined argv and records calls in order.
type FakeRunner struct{ ... }
func NewFakeRunner() *FakeRunner
func (f *FakeRunner) Script(argvPrefix string, out string, err error) // longest-prefix match wins
func (f *FakeRunner) Calls() []string                                // joined argv, in order
func (f *FakeRunner) Run(ctx context.Context, argv ...string) (string, error)
```

- [ ] **Step 1: Write the failing tests** (`internal/net/runner_test.go`)

```go
package net

import (
	"context"
	"testing"
)

func TestExecRunnerRunsCommand(t *testing.T) {
	r := NewExecRunner()
	out, err := r.Run(context.Background(), "echo", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello\n" {
		t.Fatalf("out = %q, want %q", out, "hello\n")
	}
}

func TestExecRunnerReturnsErrorWithOutput(t *testing.T) {
	r := NewExecRunner()
	out, err := r.Run(context.Background(), "sh", "-c", "echo oops >&2; exit 3")
	if err == nil {
		t.Fatal("want error for exit 3")
	}
	if out != "oops\n" {
		t.Fatalf("combined output = %q, want stderr captured", out)
	}
}

func TestFakeRunnerScriptsByLongestPrefix(t *testing.T) {
	f := NewFakeRunner()
	f.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	f.Script("wpa_cli", "OK\n", nil)

	out, err := f.Run(context.Background(), "wpa_cli", "-i", "wlan0", "status")
	if err != nil || out != "wpa_state=COMPLETED\n" {
		t.Fatalf("longest prefix should win: %q %v", out, err)
	}
	out, _ = f.Run(context.Background(), "wpa_cli", "-i", "wlan0", "select_network", "1")
	if out != "OK\n" {
		t.Fatalf("fallback prefix: %q", out)
	}
	if calls := f.Calls(); len(calls) != 2 || calls[1] != "wpa_cli -i wlan0 select_network 1" {
		t.Fatalf("calls recorded wrong: %v", calls)
	}
}

func TestFakeRunnerUnscriptedIsError(t *testing.T) {
	f := NewFakeRunner()
	if _, err := f.Run(context.Background(), "rm", "-rf", "/"); err == nil {
		t.Fatal("unscripted command must error, not silently succeed")
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/net/` → FAIL (package does not exist / undefined).
- [ ] **Step 3: Implement.** `net.go`:

```go
// Package net owns wlan0: the Connectivity Manager state machine, the
// layered connectivity check, and the AP drivers (wpa_supplicant mode=2,
// hostapd fallback), all driving OS side effects through the Runner seam
// (ADR 0003; M3 design spec). Pure logic is host-testable against
// FakeRunner; nothing here executes commands except ExecRunner.
package net
```

`runner.go`:

```go
package net

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// Runner executes one external command, returning combined stdout+stderr.
// Every OS side effect in this package goes through a Runner: production
// uses ExecRunner, tests use FakeRunner.
type Runner interface {
	Run(ctx context.Context, argv ...string) (string, error)
}

// ExecRunner runs commands via os/exec. The only place in the codebase that
// may exec.
type ExecRunner struct{}

// NewExecRunner returns the production Runner.
func NewExecRunner() Runner { return ExecRunner{} }

// Run executes argv[0] with argv[1:], combined output.
func (ExecRunner) Run(ctx context.Context, argv ...string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("net: empty argv")
	}
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	return string(out), err
}

// FakeRunner scripts command responses for tests. Keys are joined argv
// prefixes; the longest matching prefix wins. Unscripted commands error —
// a test must declare every side effect it expects.
type FakeRunner struct {
	mu      sync.Mutex
	scripts []fakeScript // insertion order; matched by longest prefix
	calls   []string
}

type fakeScript struct {
	prefix string
	out    string
	err    error
}

// NewFakeRunner returns an empty scripted runner.
func NewFakeRunner() *FakeRunner { return &FakeRunner{} }

// Script registers a response for any command whose joined argv starts with
// argvPrefix. Longest prefix wins; later registrations of an equal prefix
// replace earlier ones.
func (f *FakeRunner) Script(argvPrefix, out string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.scripts {
		if s.prefix == argvPrefix {
			f.scripts[i] = fakeScript{argvPrefix, out, err}
			return
		}
	}
	f.scripts = append(f.scripts, fakeScript{argvPrefix, out, err})
}

// Calls returns every executed command as its joined argv, in order.
func (f *FakeRunner) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// Run records the call and returns the longest-prefix scripted response.
func (f *FakeRunner) Run(_ context.Context, argv ...string) (string, error) {
	joined := strings.Join(argv, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, joined)
	best := -1
	for i, s := range f.scripts {
		if strings.HasPrefix(joined, s.prefix) && (best < 0 || len(s.prefix) > len(f.scripts[best].prefix)) {
			best = i
		}
	}
	if best < 0 {
		return "", fmt.Errorf("net: unscripted command %q", joined)
	}
	return f.scripts[best].out, f.scripts[best].err
}
```

Put `FakeRunner` in `internal/net/fakerunner.go` (exported, used by tests across tasks; document that it is a test double kept in the main tree deliberately, like display/fake.go).

- [ ] **Step 4: Verify green** — `go test -race ./internal/net/ -v` → PASS (4 tests).
- [ ] **Step 5: Commit** — `git add internal/net && git commit -m "feat(net): Runner command seam — exec impl + scripted fake"`

---

### Task 2: Fault codes E05/E06

**Files:**
- Modify: `internal/obs/faults.go`
- Test: `internal/obs/faults_test.go` (exists — extend; if absent, create)

**Interfaces:**
- Produces: `obs.FaultRadioBlocked FaultCode = "E05"`, `obs.FaultConnectivity FaultCode = "E06"`; `Message()` returns "WiFi radio blocked" and "Network connectivity" respectively. Stage detail ("E06 DHCP") is composed by the SCENE from Snapshot data, not encoded in the FaultCode.

- [ ] **Step 1: Failing test**

```go
func TestM3FaultCodes(t *testing.T) {
	if FaultRadioBlocked != "E05" || FaultRadioBlocked.Message() != "WiFi radio blocked" {
		t.Fatalf("E05 wrong: %q %q", FaultRadioBlocked, FaultRadioBlocked.Message())
	}
	if FaultConnectivity != "E06" || FaultConnectivity.Message() != "Network connectivity" {
		t.Fatalf("E06 wrong: %q %q", FaultConnectivity, FaultConnectivity.Message())
	}
}
```

- [ ] **Step 2: Red** — `go test ./internal/obs/ -run TestM3Fault` → FAIL undefined.
- [ ] **Step 3: Implement** — add to the const block:

```go
	// FaultRadioBlocked: wlan0 is rfkill-soft-blocked or the regulatory
	// domain is unset — AP mode would be dead-on-arrival (M3 spec, issue #6).
	FaultRadioBlocked FaultCode = "E05"
	// FaultConnectivity: a layered connectivity stage failed (association /
	// DHCP / DNS / captive); the failing stage is carried on the Snapshot.
	FaultConnectivity FaultCode = "E06"
```

and the two Message() cases: `"WiFi radio blocked"`, `"Network connectivity"`.

- [ ] **Step 4: Green** — full package. **Step 5: Commit** `feat(obs): E05/E06 fault codes for M3 connectivity`.

---

### Task 3: Config validation tiers

**Files:**
- Modify: `internal/config/config.go` (Validate area)
- Test: `internal/config/config_test.go` (extend)

**Interfaces:**
- Produces: `func (c Config) ValidateConnectivity() error` — passes when `Web.PasswordHash != ""` and Wifi is either empty (both fields) or complete (SSID 1-32 bytes; PSK 8-63 chars). Existing `Validate()` (board-valid) is UNCHANGED and additionally implies connectivity-valid: implement `Validate()` as `ValidateConnectivity()` + the existing board checks, so the tiers nest. Read the existing Validate first and keep every current rule.

- [ ] **Step 1: Failing tests**

```go
func TestValidateConnectivityTier(t *testing.T) {
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("password-only config should be connectivity-valid: %v", err)
	}
	if err := c.Validate(); err == nil {
		t.Fatal("connectivity-valid config with no origin/token must NOT be board-valid")
	}

	c.Wifi.SSID = "HomeNet" // ssid without psk = incomplete
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("SSID without PSK must fail connectivity validation")
	}
	c.Wifi.PSK = "short" // < 8
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("PSK under 8 chars must fail")
	}
	c.Wifi.PSK = "longenough"
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("complete wifi should pass: %v", err)
	}

	c.Web.PasswordHash = ""
	if err := c.ValidateConnectivity(); err == nil {
		t.Fatal("no admin password must fail connectivity validation")
	}
}

func TestValidateImpliesConnectivity(t *testing.T) {
	// Any config passing Validate must pass ValidateConnectivity.
	c := Default()
	c.Web.PasswordHash = "$argon2id$fake"
	c.Board.Origin = "PAD"
	c.Darwin.Token = "tok"
	if err := c.Validate(); err != nil {
		t.Fatalf("fixture should be board-valid: %v", err)
	}
	if err := c.ValidateConnectivity(); err != nil {
		t.Fatalf("board-valid must imply connectivity-valid: %v", err)
	}
}
```

NOTE for implementer: if the existing `Validate()` does not currently require `Web.PasswordHash`, DO NOT add that requirement to `Validate()`'s existing callers' expectations blindly — check `config_test.go`'s current Validate fixtures. If Validate today passes with an empty PasswordHash (M1-era config), keep `Validate()` = existing rules + wifi-shape check only, and have `ValidateConnectivity()` = password + wifi shape as a SEPARATE tier (no nesting of the password rule). Adjust `TestValidateImpliesConnectivity` accordingly (drop it if nesting is impossible without breaking existing fixtures) and record which shape you chose in your report.

- [ ] **Step 2: Red.** **Step 3: Implement** per the note. Wifi shape check shared by both tiers:

```go
// validateWifi: empty (both fields blank) or complete with sane lengths.
func (c Config) validateWifi() error {
	if c.Wifi.SSID == "" && c.Wifi.PSK == "" {
		return nil
	}
	if c.Wifi.SSID == "" || len(c.Wifi.SSID) > 32 {
		return errors.New("config: wifi.ssid must be 1-32 bytes when wifi is configured")
	}
	if l := len(c.Wifi.PSK); l < 8 || l > 63 {
		return errors.New("config: wifi.psk must be 8-63 characters")
	}
	return nil
}
```

- [ ] **Step 4: Green** full config package. **Step 5: Commit** `feat(config): connectivity-valid tier for AP-mode partial setup`.

---

### Task 4: Layered check — probes + staging

**Files:**
- Create: `internal/net/check.go`
- Test: `internal/net/check_test.go`

**Interfaces:**
- Produces:

```go
type Stage string
const (
	StageAssoc   Stage = "ASSOC"
	StageDHCP    Stage = "DHCP"
	StageDNS     Stage = "DNS"
	StageCaptive Stage = "CAPTIVE"
	StageOK      Stage = "" // all layers passed
)

// Probes are the individually injectable layer checks; production wiring
// in NewCheck, fakes in tests. Each returns nil on pass.
type Probes struct {
	Assoc   func(ctx context.Context) error
	DHCP    func(ctx context.Context) error
	DNS     func(ctx context.Context) error
	Captive func(ctx context.Context) error
}

type Check struct{ p Probes }
func NewCheck(r Runner, iface, dnsHost, captiveURL string, httpGet func(ctx context.Context, url string) (status int, body string, err error)) *Check
func NewCheckWithProbes(p Probes) *Check // test seam
// Evaluate runs layers in order, returning the first failing Stage and its
// error, or (StageOK, nil).
func (c *Check) Evaluate(ctx context.Context) (Stage, error)
```

Production probes (all via Runner except DNS/HTTP which use injected funcs):
- Assoc: `wpa_cli -i wlan0 status` output contains line `wpa_state=COMPLETED` (parse KEY=VALUE lines; any other wpa_state = failure with that state in the error).
- DHCP: `ip -4 addr show dev wlan0` output contains ` inet ` (a v4 address assigned).
- DNS: injected resolver func — production `func(ctx) error { _, err := net.DefaultResolver.LookupHost(ctx, dnsHost); return err }` with dnsHost = `lite.realtime.nationalrail.co.uk` (wire in cmd, not hardcoded in the package).
- Captive: httpGet(captiveURL) with captiveURL = `http://connectivitycheck.gstatic.com/generate_204`: status 204 = pass; any other status = captive trap (error names the status); transport error = fail.

- [ ] **Step 1: Failing tests** — with `NewCheckWithProbes`, table-test: all pass → `(StageOK, nil)`; first failure short-circuits (later probes not called — verify with call-flag booleans); each stage returns its own Stage constant. Plus production-probe tests via FakeRunner: assoc pass/fail on scripted `wpa_cli -i wlan0 status` outputs (`wpa_state=COMPLETED\n` vs `wpa_state=SCANNING\n` — error text must contain "SCANNING"), DHCP pass/fail on scripted `ip -4 addr show dev wlan0` (with/without ` inet 192.168.3.181/24`), captive 204 vs 302 vs transport error via injected httpGet. Write the table code explicitly in the test file.
- [ ] **Step 2: Red.** **Step 3: Implement** exactly the interfaces above; parsing helpers unexported (`parseWpaStatus(out string) map[string]string`).
- [ ] **Step 4: Green + `go vet`.** **Step 5: Commit** `feat(net): layered connectivity check — assoc/DHCP/DNS/captive probes`.

---

### Task 5: mode2 driver (wpa_supplicant native AP)

**Files:**
- Create: `internal/net/driver.go` (interface + shared types), `internal/net/driver_mode2.go`
- Test: `internal/net/driver_mode2_test.go`

**Interfaces:**
- Produces:

```go
// APConfig is the AP identity handed to a driver.
type APConfig struct {
	SSID     string // Trainboard-XXXX
	Password string // WPA2-PSK, 8-63 chars
	Addr     string // "192.168.4.1/24"
}

// STAConfig is the target client network.
type STAConfig struct{ SSID, PSK string }

// apDriver abstracts "make the AP exist / attempt the STA network".
// Implementations: mode2 (single wpa_supplicant), hostapd (Task 6).
type apDriver interface {
	// StartAP brings the AP up (and assigns APConfig.Addr to the iface).
	StartAP(ctx context.Context, ap APConfig) error
	// StopAP tears the AP down (does NOT start STA).
	StopAP(ctx context.Context) error
	// AttemptSTA switches to the client network and runs dhclient; it does
	// NOT evaluate connectivity beyond association+DHCP client exit — the
	// layered Check owns that.
	AttemptSTA(ctx context.Context, sta STAConfig) error
	// APActive reports whether the AP is currently beaconing (used by the
	// AP-restore invariant).
	APActive(ctx context.Context) (bool, error)
}
```

mode2 mechanics (all through Runner, iface parameterised, default wlan0):
- The driver OWNS `/run/trainboard-wpa.conf`, written via injected `writeFile func(path string, data []byte) error` (production os.WriteFile; fake records) — conf template with BOTH networks:

```
ctrl_interface=/run/wpa_supplicant
country=GB
network={
    id_str="sta"
    ssid="<sta ssid>"
    psk="<sta psk>"
    disabled=1
}
network={
    id_str="ap"
    ssid="<ap ssid>"
    mode=2
    frequency=2437
    key_mgmt=WPA-PSK
    psk="<ap password>"
    disabled=1
}
```

- Daemon lifecycle: `wpa_supplicant -B -i wlan0 -c /run/trainboard-wpa.conf` started if `wpa_cli -i wlan0 status` errors (not running); conf changes → rewrite + `wpa_cli -i wlan0 reconfigure`.
- StartAP: write conf → ensure daemon → `wpa_cli -i wlan0 select_network 1` (AP is network id 1, STA id 0 — ids follow conf order) → poll `wpa_cli -i wlan0 status` until `wpa_state=COMPLETED` AND `mode=AP` (bounded: 10 polls via injected `sleep func(time.Duration)`, 500ms apart — production time.Sleep, fake no-op recording) → `ip addr flush dev wlan0` → `ip addr add 192.168.4.1/24 dev wlan0`.
- StopAP: `wpa_cli -i wlan0 disable_network 1` → `ip addr flush dev wlan0`.
- AttemptSTA: write conf (fresh creds) → reconfigure → `select_network 0` → poll status for `wpa_state=COMPLETED` (mode absent/station) → `dhclient -1 -v wlan0` (`-1` = one shot, exits nonzero on no lease).
- APActive: status parse → `wpa_state=COMPLETED` && `mode=AP`.
- Escalation NOT here: a failed step returns an error naming the step; the Manager owns retries.

- [ ] **Step 1: Failing tests** — FakeRunner-scripted: (a) StartAP happy path issues exactly the expected argv sequence in order (assert via Calls() — daemon check, select_network 1, status polls, ip flush, ip add); (b) StartAP fails when status never reaches mode=AP after 10 polls (script status as `wpa_state=SCANNING`), error mentions "AP not active"; (c) AttemptSTA happy path ends with `dhclient -1 -v wlan0`; (d) AttemptSTA surfaces dhclient failure; (e) conf file written with both network blocks and correct ssid/psk substitution (assert via fake writeFile capture; include a PSK containing `"` — the driver must reject it: wpa conf has no escaping, error not injection). Write all five tests fully.
- [ ] **Step 2: Red.** **Step 3: Implement** per mechanics above. **Step 4: Green + vet + lint.** **Step 5: Commit** `feat(net): wpa_supplicant mode=2 AP driver`.

---

### Task 6: hostapd driver (fallback)

**Files:**
- Create: `internal/net/driver_hostapd.go`
- Test: `internal/net/driver_hostapd_test.go`

**Interfaces:** same `apDriver`; construction `newHostapdDriver(r Runner, iface string, writeFile func(string, []byte) error, sleep func(time.Duration))`.

Mechanics: owns `/run/trainboard-hostapd.conf`:

```
interface=wlan0
driver=nl80211
ssid=<ap ssid>
country_code=GB
hw_mode=g
channel=6
wpa=2
wpa_key_mgmt=WPA-PSK
rsn_pairwise=CCMP
wpa_passphrase=<ap password>
```

- StartAP: stop wpa_supplicant's control of the iface (`wpa_cli -i wlan0 disable_network 0` tolerated-if-error) → write conf → `hostapd -B /run/trainboard-hostapd.conf` → `ip addr flush` + `ip addr add` (same as mode2).
- StopAP: `pkill -x hostapd` (tolerate exit 1 = none running) → `ip addr flush dev wlan0`.
- AttemptSTA: StopAP first, then identical wpa_cli/dhclient flow as mode2 (share the helper: extract `staAttempt(ctx, r, iface, sta, writeFile, sleep) error` used by both drivers — put it in driver.go).
- APActive: `pgrep -x hostapd` exit 0.
- Passphrase with newline/`"` rejected same as mode2.

- [ ] **Step 1: Failing tests** — mirror Task 5's shape: happy-path argv order for StartAP/StopAP/AttemptSTA, pkill-tolerance, conf content capture, APActive true/false via scripted pgrep. Write fully.
- [ ] **Steps 2-5:** red → implement → green → commit `feat(net): hostapd fallback AP driver`.

---

### Task 7: iface prerequisites + dnsmasq control

**Files:**
- Create: `internal/net/prereq.go`, `internal/net/dnsmasq.go`
- Test: `internal/net/prereq_test.go`, `internal/net/dnsmasq_test.go`

**Interfaces:**
- Produces:

```go
// CheckPrereqs verifies first-boot radio prerequisites (issue #6): rfkill
// soft-block state via sysfs (rfkill binary is not installed on DietPi) and
// regulatory country via `iw reg get`. Returns nil or an error suitable for
// FaultRadioBlocked. It FIXES what it safely can (writes "0" to the sysfs
// soft file; `iw reg set GB` when country is 00/unset) and re-verifies.
func CheckPrereqs(ctx context.Context, r Runner, readFile func(string) ([]byte, error), writeFile func(string, []byte) error, glob func(string) ([]string, error)) error

// Dnsmasq controls the AP-side DHCP + wildcard DNS (production requires the
// dnsmasq package — installed by M3b's deploy step; M3a never runs it).
type Dnsmasq struct{ ... }
func NewDnsmasq(r Runner, writeFile func(string, []byte) error) *Dnsmasq
func (d *Dnsmasq) Start(ctx context.Context) error // write conf, start
func (d *Dnsmasq) Stop(ctx context.Context) error
func (d *Dnsmasq) Alive(ctx context.Context) (bool, error) // pgrep
```

- Prereq mechanics: glob `/sys/class/rfkill/rfkill*/type`, find entries whose type file reads `wlan`; for each, read sibling `soft`: `1` → write `0`, re-read, still `1` → error "rfkill soft-block persists". `iw reg get` output containing `country 00` → `iw reg set GB` → re-check; still 00 → error. Both errors mention what the operator can do.
- dnsmasq conf `/run/trainboard-dnsmasq.conf`:

```
interface=wlan0
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,10m
dhcp-option=option:router,192.168.4.1
address=/#/192.168.4.1
no-resolv
```

Start: write conf → `dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid`. Stop: `pkill -F /run/trainboard-dnsmasq.pid` (tolerate failure). Alive: `pgrep -F` equivalent: `pkill -0 -F /run/trainboard-dnsmasq.pid` exit 0.

- [ ] **Step 1: Failing tests** — prereq: fake fs (map-backed readFile/writeFile/glob) tables: unblocked+GB → nil, no calls beyond reads; soft-blocked once → writes "0" and passes on re-read; persistent block → E05-able error; country 00 → `iw reg set GB` issued. dnsmasq: conf content capture; argv order; Alive both ways. Write fully.
- [ ] **Steps 2-5:** red → implement → green → commit `feat(net): radio prerequisites (sysfs rfkill, iw reg) + dnsmasq control`.

---

### Task 8: Manager — states and single transitions

**Files:**
- Create: `internal/net/manager.go`
- Test: `internal/net/manager_test.go`

**Interfaces:**
- Produces:

```go
type ManagerState int
const (
	ManagerBoot ManagerState = iota
	ManagerSTAConnecting
	ManagerOnline
	ManagerAPFallback
	ManagerSTARetry // mid tear-down-retry: AP is DOWN
)
func (s ManagerState) String() string // "boot","sta-connecting","online","ap-fallback","sta-retry"

// Status is the Manager's published state (atomic snapshot, immutable).
type Status struct {
	State       ManagerState
	Stage       Stage         // failing layer while STAConnecting/APFallback (E06 detail)
	Hotspot     *board.Hotspot // non-nil while the AP should be shown on-screen
	LastSTAErr  string        // preserved across AP restore for the portal (M3b)
}

type ManagerDeps struct {
	Driver   apDriver
	Check    *Check
	Dnsmasq  *Dnsmasq
	Prereqs  func(ctx context.Context) error
	AP       APConfig
	STA      func() STAConfig       // reads config (empty SSID = none configured)
	OnOnline func()                 // poller poke (Task 11)
	Beat     func()                 // watchdog heartbeat (Task 11); called every loop iteration
	Log      *slog.Logger
	Now      func() time.Time
	// Timers injected for tests: returns a channel that fires after d.
	After func(d time.Duration) <-chan time.Time
}

func NewManager(d ManagerDeps) *Manager
func (m *Manager) Status() Status            // atomic load
func (m *Manager) RetryNow()                 // non-blocking nudge (buffered ch)
func (m *Manager) NoteProvisioning(now time.Time) // web/dnsmasq activity callback (suppression input)
func (m *Manager) Run(ctx context.Context) error
```

This task implements: state publication (atomic.Pointer[Status]), and the two single transitions as unexported, directly-tested methods —
- `toSTA(ctx) error`: Prereqs → Driver.AttemptSTA(STA()) → Check.Evaluate; publishes STAConnecting with each failing Stage; on StageOK publishes Online, calls OnOnline. Any layer failure returns error (caller decides fallback).
- `toAP(ctx) error`: Driver.StartAP(AP) → Dnsmasq.Start → **verify**: Driver.APActive true AND Dnsmasq.Alive true → publish APFallback with `Hotspot{SSID: AP.SSID, Addr: "192.168.4.1"}`. Verification failure → one full retry (StopAP+Stop, then again); second failure returns the error (Task 9 escalates).

- [ ] **Step 1: Failing tests** — fakes: fakeDriver (records calls, scriptable errors per method), Check via NewCheckWithProbes, fake Dnsmasq via FakeRunner, injected After that returns immediately-fired channels. Tests: (a) toSTA happy → Online published, OnOnline called once; (b) toSTA with DHCP probe failing → error, Status.Stage == StageDHCP, State STAConnecting; (c) toSTA with no wifi configured (STA() returns empty SSID) → returns errNoWifiConfigured sentinel without calling the driver; (d) toAP happy → APFallback + Hotspot set with SSID/addr; (e) toAP where APActive first reports false → full second attempt happens (StopAP called between) and succeeds; (f) toAP failing verification twice → error out, Hotspot nil, state NOT APFallback. Write all fully.
- [ ] **Steps 2-5:** red → implement → green (`-race`) → commit `feat(net): Manager state publication + STA/AP transitions with verified AP restore`.

---

### Task 9: Manager — run loop, retry, suppression, escalation

**Files:**
- Modify: `internal/net/manager.go`
- Test: `internal/net/manager_test.go` (extend)

**Interfaces:**
- Consumes Task 8 exactly. Produces the finished `Run(ctx)`:

Loop semantics (all waits via `deps.After`, every iteration calls `deps.Beat()`):
1. Boot: `toSTA`; success → Online watch. errNoWifiConfigured or layered failure → `toAP`; if `toAP` errors (post-retry) → **escalation**: stop calling Beat (return a sentinel error from Run; cmd treats Manager exit as fatal-to-heartbeat, watchdog reboots — document in code).
2. Online watch: every 30s re-run `Check.Evaluate` (cheap probes); failure → full `toSTA` re-attempt once; still failing → `toAP`.
3. APFallback: wait 5m (or RetryNow nudge), THEN check suppression: `lastProvisioning` (set by NoteProvisioning) within 90s → skip this cycle (log, re-wait). Otherwise: publish ManagerSTARetry (Hotspot nil — scene drops to underlying state), Dnsmasq.Stop, Driver.StopAP, `toSTA`; success → Online; failure → `toAP` again (which re-verifies; its post-retry failure = same escalation), `Status.LastSTAErr` set from the toSTA error.
4. RetryNow: buffered-1 channel select alongside the 5m timer; drained on fire.
5. ctx cancel anywhere → clean return nil (best-effort Driver.StopAP + Dnsmasq.Stop when currently in AP mode — device is shutting down/restarting).

- [ ] **Step 1: Failing tests** (drive with manual After channels — the test holds the chan and fires it):
  (a) boot with no wifi → toAP path, Status ends APFallback;
  (b) full fallback-retry-success cycle: APFallback → fire 5m timer → STARetry observed (Hotspot nil during attempt) → probes pass → Online, OnOnline called, dnsmasq stopped before attempt;
  (c) retry-failure restores AP: probes fail → APFallback again, Hotspot back, LastSTAErr non-empty;
  (d) suppression: NoteProvisioning(now) just before timer fires → no StopAP call this cycle;
  (e) RetryNow bypasses the 5m wait AND suppression;
  (f) online watch degradation: Online → probe failure on the 30s re-check → ends APFallback;
  (g) escalation: toAP verification permanently failing → Run returns non-nil;
  (h) Beat called at least once per loop iteration (count via closure).
  Write all fully; use `-race`.
- [ ] **Steps 2-5:** red → implement → green → commit `feat(net): Manager run loop — 5min retry, suppression, retry-now, escalation`.

---

### Task 10: Hotspot scene password + snapshot composition

**Files:**
- Modify: `internal/board/scenes.go:87-94` (hotspotInfoScene), `internal/board/snapshot.go:42-44` (Hotspot struct)
- Create: `internal/runtime/composite.go`
- Test: `internal/board/scenes_test.go` (extend), `internal/runtime/composite_test.go`

**Interfaces:**
- `board.Hotspot` gains `Password string`; `hotspotInfoScene(ssid, password, addr string, f *Fonts)` adds line `centered(f.Regular, "Password: "+password, 40)`; BuildScene call sites updated (`s.Hotspot.SSID, s.Hotspot.Password, s.Hotspot.Addr` — two call sites in BuildScene).
- Produces:

```go
// HotspotSnapshotSource decorates a snapshot source with the Manager's
// hotspot state: while hs() returns non-nil, the returned snapshot is the
// base snapshot cloned with Hotspot set. The composed pointer is CACHED and
// only replaced when the base pointer or hotspot value changes — the render
// loop rebuilds its scene on pointer inequality (loop.go step()), so a
// fresh pointer every call would rebuild the scene at 25fps.
func HotspotSnapshotSource(base func() *board.Snapshot, hs func() *board.Hotspot) func() *board.Snapshot
```

Implementation shape: small struct with mutex holding lastBase/lastHS/composed; comparison of hs by VALUE (SSID+Password+Addr), not pointer, so the Manager may return fresh pointers.

- [ ] **Step 1: Failing tests** — board: golden or substring test asserting the scene renders the password line (follow scenes_test.go's existing pattern — check how initialising/error scenes are asserted and mirror it). runtime/composite: (i) hs nil → base pointer returned UNCHANGED (identity — critical for scene-swap semantics); (ii) hs non-nil → composed snapshot has Hotspot, base unmutated (immutability: original snapshot's Hotspot still nil); (iii) two consecutive calls with same base+hs → SAME pointer; (iv) hs value change → new pointer; (v) base change → new pointer; (vi) nil base + hs non-nil → synthetic snapshot (StateInitialising + Hotspot) so first-boot AP mode shows before any poll. Write fully.
- [ ] **Steps 2-5:** red → implement → green (mind golden regeneration rules: only the hotspot scene golden may change; it currently has no golden — check first) → commit `feat(board,runtime): hotspot scene password + pointer-stable snapshot composition`.

---

### Task 11: Watchdog aggregator + sd_notify + Poller.Poke

**Files:**
- Create: `internal/obs/watchdog.go`
- Modify: `internal/runtime/poller.go`, `internal/runtime/loop.go` (Beat hooks), `deploy/trainboard.service`
- Test: `internal/obs/watchdog_test.go`, `internal/runtime/poller_test.go` (extend)

**Interfaces:**
- Produces:

```go
// package obs
// Watchdog aggregates component heartbeats and pets systemd only while
// EVERY registered component has beaten within its deadline (M3 spec §
// Watchdog): a healthy render loop must not mask a deadlocked manager.
type Watchdog struct{ ... }
func NewWatchdog(notify func(state string) error, now func() time.Time) *Watchdog
func (w *Watchdog) Register(name string, deadline time.Duration) func() // returns that component's Beat
func (w *Watchdog) Run(ctx context.Context, interval time.Duration)    // pets at interval when all healthy
func (w *Watchdog) Healthy(now time.Time) (bool, string)               // false + first stale component name

// SdNotify writes state (e.g. "WATCHDOG=1") to $NOTIFY_SOCKET (unixgram);
// returns nil silently when the env var is unset (dev mode).
func SdNotify(state string) error
```

- Poller: add `Poke()` — buffered-1 channel; Run's select gains `case <-p.poke: p.pollOnce(ctx)` (timer NOT reset — acceptable extra poll). Loop/Poller/Manager Beat wiring happens in Task 12; this task only adds `Beat func()` optional fields where needed: Poller gets none (cmd wraps pollDone? NO — simplest: Poller.Run calls `p.beat()` if non-nil at the top of each loop iteration; add unexported `beat func()` + `SetBeat(f func())` setter; same for Loop: `SetBeat`, called each tick).
- Unit file: uncomment/set `WatchdogSec=30` with a comment pointing at the aggregator; add `# sd_notify Type stays default (notify NOT set: watchdog works with Type=simple via WatchdogSec + NotifyAccess)` — set `NotifyAccess=main`.
- Deadlines wired in Task 12: render 5s, poller `2*interval+35s`, manager 90s.

- [ ] **Step 1: Failing tests** — watchdog: fake notify recorder + manual clock: all-beaten → Run tick sends WATCHDOG=1; one stale component → no pet, Healthy false naming it; re-beat resumes petting; Register after Run started is safe (`-race`). SdNotify: set NOTIFY_SOCKET to a test unixgram socket (t.TempDir path), assert datagram content "WATCHDOG=1"; unset env → nil, no panic. Poller.Poke: failed-state poller with pollDone seam — Poke triggers an immediate pollOnce without waiting the backoff (mirror TestRunRetriesFastAfterError's style). Loop/Poller SetBeat: beat counter increments across steps/polls. Write fully.
- [ ] **Steps 2-5:** red → implement → green → commit `feat(obs,runtime): watchdog heartbeat aggregation + sd_notify + poller poke`.

---

### Task 12: cmd wiring + docs

**Files:**
- Modify: `cmd/trainboard/main.go`, `docs/deploy.md` (§flags + new §connectivity), `deploy/trainboard.service` (if not finished in 11)
- Test: `cmd/trainboard` builds; `make check`; existing tests green.

**Interfaces:** consumes everything. Wiring rules:

- New flag `--manage-network` (default **false**): the Manager only runs when set (the bench Pi's WiFi is ifupdown-managed until the M3b bench session flips it — this flag is the safety interlock; document in deploy.md that enabling it requires the M3b migration steps).
- Construction (production path, after config load, both boot paths):

```go
if *manageNetwork {
	mac := readWlanMAC()             // /sys/class/net/wlan0/address; helper in main
	ap := net.APConfig{SSID: "Trainboard-" + macTail(mac), Password: cfg.Provisioning.APPassword, Addr: "192.168.4.1/24"}
	// runner, drivers (mode2 default pending evaluation), check, dnsmasq,
	// manager with STA-from-config closure, OnOnline: poller.Poke,
	// Beat: wd.Register("manager", 90*time.Second)
	go func() {
		if err := mgr.Run(ctx); err != nil {
			log.Error("connectivity manager exited", "err", err.Error())
			// deliberately NOT re-registered: its watchdog beat goes stale
			// and systemd reboots — the escalation path (spec §Watchdog).
		}
	}()
	snapshotSrc = runtime.HotspotSnapshotSource(snapshotSrc, func() *board.Hotspot { return mgr.Status().Hotspot })
}
```

- Watchdog always constructed (SdNotify no-ops off-systemd): loop.SetBeat(wd.Register("render", 5s)), poller.SetBeat(wd.Register("poller", 2*interval+35s)); `go wd.Run(ctx, 10*time.Second)`.
- E04 boot path: manager runs there too (spec: AP works unconfigured) — STA() closure returns cfg-less empty STAConfig → straight to AP fallback. PasswordHash may be empty there; AP password comes from Provisioning.APPassword: when empty, generate once via the existing web RegenerateAPPassword path? NO — that's web's. Rule: when `Provisioning.APPassword == ""`, the manager-side wiring generates one with the same alphabet (move `apAlphabet` + generation into `config.GenerateAPPassword() (string, error)`, web's RegenerateAPPassword calls it too — small refactor, keep web tests green) and persists via config.Save on the E04-tolerant path (Save validates! → use the connectivity tier… config.Save validates full Validate — check config.Save; if it hard-validates, add `config.SaveConnectivity` variant gated on ValidateConnectivity; document). Record what you found in the report.
- deploy.md: new "Connectivity & AP mode (M3, behind --manage-network)" section: flag table row, fault codes E05/E06 rows in §6, WatchdogSec note, the M3b prerequisite warning (do NOT enable on a device whose WiFi you need until the bench migration).

- [ ] **Step 1:** wire per above; any interface friction discovered → fix in the owning file with its tests updated (report it).
- [ ] **Step 2:** `make check` fully green; `go build ./...`.
- [ ] **Step 3:** grep-gates: `grep -rn "os/exec" internal/ | grep -v internal/net/runner.go` → empty; `grep -rn "time.Sleep" internal/net/ | grep -v _test` → only the injected default in driver constructors.
- [ ] **Step 4: Commit** `feat(cmd): wire connectivity manager behind --manage-network; watchdog live`.

---

## Post-plan (not tasks)

- PR `feat/m3a-connectivity` → main after final whole-branch review.
- Hardware evaluation + M3b plan follow (portal, handoff, partial setup, bench matrix, driver verdict → ADR 0003 addendum).

## Self-review notes

- Spec coverage: Runner/#13-host ✅ T1; E05/E06 ✅ T2; validation tiers ✅ T3; layered check ✅ T4; mode2/hostapd drivers ✅ T5/T6; prereqs #6 ✅ T7; dnsmasq ✅ T7; manager states+invariant #7/#9 ✅ T8/T9; suppression+retry-now ✅ T9; hotspot scene #10 (scene part) ✅ T10; watchdog+sd_notify #9 ✅ T11; poller poke (spec §layered check last line) ✅ T11; wiring ✅ T12. Portal/handoff/partial-setup = M3b by design.
- Type consistency: `apDriver` methods used by Manager in T8/T9 match T5/T6 signatures; `Stage` constants shared T4→T8; `board.Hotspot{SSID,Password,Addr}` T10 matches Manager's Status usage in T8 (Manager constructs `*board.Hotspot` — net imports board; no cycle: board does not import net).
- Placeholders: T12 contains two investigate-and-decide notes (config.Save validation shape; existing Validate/password nesting in T3) — deliberate: they depend on code state the plan writer verified only partially; both instruct the implementer to record the choice, and the reviewer gate covers them.
