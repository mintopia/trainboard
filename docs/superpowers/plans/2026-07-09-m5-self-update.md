# M5 Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The device updates itself from GitHub releases: minisign-signed manifest, A/B slots, external launcher with boot-counter rollback, web-UI trigger with opt-in auto-apply (issues #16–#19).

**Architecture:** A new `internal/update` package holds all updater logic (state file, slot selection, keyring verification, manifest checks, GitHub release discovery, apply pipeline, periodic checker). A tiny new `cmd/trainboard-launcher` binary reads the state file and `exec()`s the active slot. The payload promotes itself to known-good after a health check. A new `release.yml` workflow builds, signs, and publishes releases on tag push.

**Tech Stack:** Go 1.26, `github.com/jedisct1/go-minisign` (signature verification), `golang.org/x/mod/semver` (version ordering), minisign CLI in CI, GitHub Actions + `gh` CLI.

**Spec:** `docs/superpowers/specs/2026-07-09-m5-self-update-design.md` — read it before starting any task.

## Global Constraints

- Module path: `github.com/mintopia/trainboard`; Go `1.26`.
- Every commit must pass: `go vet ./...`, `go test -race ./... -count=1`, `golangci-lint run` (v2.10.1; linters: errcheck, govet, ineffassign, staticcheck, unused, misspell, revive).
- Red/green TDD: write the failing test first, watch it fail, then implement.
- Commit style (from git history): `feat(update): …`, `fix(net): …`, `docs(deploy): …` — conventional commits with package scopes.
- All times shown/compared are Europe/London (`internal/tz.Location()`), never host TZ.
- Comment density: this codebase carries rationale-heavy doc comments (see `internal/config/store.go`); match it.
- Production paths: slots `/opt/trainboard/slots/{a,b}/trainboard`, launcher `/opt/trainboard/launcher`, state `/var/lib/trainboard/updater/state.json`.
- Versions are semver tags with a leading `v` (`v0.1.0`). A non-semver running version (e.g. `"dev"`) never blocks an upgrade.
- The known-good slot is NEVER written by the apply pipeline (double-fault guarantee, #18).
- No wall-clock trust checks anywhere in verification (#17 — the Pi has no RTC).

---

### Task 1: Updater state file (`internal/update/state.go`)

The state document shared by launcher and payload: atomic writes (same temp+fsync+rename pattern as `internal/config/store.go:112`), tolerant load for the launcher, strict load for the payload.

**Files:**
- Create: `internal/update/state.go`
- Test: `internal/update/state_test.go`

**Interfaces:**
- Produces (later tasks rely on these exact names):
  - `type State struct { Active, ActiveVersion, KnownGood, KnownGoodVersion string; BootAttempts int; VersionFloor, RolledBackFrom string }` (JSON tags: `active`, `active_version`, `known_good`, `known_good_version`, `boot_attempts`, `version_floor`, `rolled_back_from`)
  - `func DefaultState() State` — `{Active: "a", KnownGood: "a"}`
  - `func LoadState(path string) (State, error)` — missing file returns an error wrapping `fs.ErrNotExist` (callers distinguish "not a slot install"); corrupt JSON or invalid slot names return other errors.
  - `func LoadStateOrDefault(path string) State` — any error ⇒ `DefaultState()` (launcher's degrade-never-refuse rule).
  - `func SaveState(path string, s State) error` — `MkdirAll` parent, temp file, fsync, rename.
  - `const DefaultSlotsDir = "/opt/trainboard/slots"`, `const DefaultStatePath = "/var/lib/trainboard/updater/state.json"`

- [ ] **Step 1: Write the failing test**

```go
// internal/update/state_test.go
package update

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updater", "state.json")
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 2, VersionFloor: "v0.1.0", RolledBackFrom: "v0.1.9",
	}
	if err := SaveState(path, want); err != nil {
		t.Fatalf("SaveState: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got != want {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
}

func TestLoadStateMissingIsErrNotExist(t *testing.T) {
	_, err := LoadState(filepath.Join(t.TempDir(), "nope.json"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing state: got %v, want fs.ErrNotExist", err)
	}
}

func TestLoadStateRejectsBadSlotNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"active":"c","known_good":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Error("bad active slot name accepted")
	}
}

func TestLoadStateOrDefaultDegrades(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"missing": filepath.Join(dir, "missing.json"),
		"corrupt": corrupt,
	} {
		if got := LoadStateOrDefault(path); got != DefaultState() {
			t.Errorf("%s: got %+v, want DefaultState", name, got)
		}
	}
}

func TestDefaultState(t *testing.T) {
	d := DefaultState()
	if d.Active != "a" || d.KnownGood != "a" || d.BootAttempts != 0 {
		t.Errorf("DefaultState = %+v", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run TestState -v` (and `TestDefaultState`, `TestLoadState`)
Expected: FAIL — package does not exist / undefined: `State`, `SaveState`, etc.

- [ ] **Step 3: Write the implementation**

```go
// Package update implements M5 self-update: the A/B slot state file, slot
// selection, signed-manifest verification, GitHub release discovery, the
// download/verify/install pipeline, and the periodic update checker
// (docs/superpowers/specs/2026-07-09-m5-self-update-design.md).
package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Production filesystem layout (spec §2). The launcher and payload both
// default to these; tests and dev builds override via flags/env.
const (
	DefaultSlotsDir  = "/opt/trainboard/slots"
	DefaultStatePath = "/var/lib/trainboard/updater/state.json"
)

// State is the updater state document shared by the launcher (increments
// BootAttempts, performs rollback flips) and the payload (applies updates,
// promotes known-good). Writes are atomic (SaveState); the two writers never
// run concurrently — the launcher writes strictly before exec()ing the
// payload, and the payload is the only process alive after that.
type State struct {
	// Active is the slot the launcher execs: "a" or "b".
	Active string `json:"active"`
	// ActiveVersion is the version installed in Active, recorded at apply
	// time so the launcher never has to interrogate a binary for its
	// version (it may not even exec).
	ActiveVersion string `json:"active_version"`
	// KnownGood is the last slot that passed the payload health check. The
	// apply pipeline never writes this slot (double-fault guarantee).
	KnownGood        string `json:"known_good"`
	KnownGoodVersion string `json:"known_good_version"`
	// BootAttempts counts launcher execs of Active since the last healthy
	// start. The launcher increments it BEFORE exec; the payload's
	// health-check promotion resets it to 0.
	BootAttempts int `json:"boot_attempts"`
	// VersionFloor is the high-water mark of every accepted manifest's
	// min_version: replayed old manifests below it are rejected (spec §1
	// anti-rollback). Empty until the first release sets one.
	VersionFloor string `json:"version_floor"`
	// RolledBackFrom is set by the launcher when a rollback flip happens,
	// surfaced in the web UI, and cleared by an operator dismiss or the
	// next successful update promotion.
	RolledBackFrom string `json:"rolled_back_from"`
}

// DefaultState is the fresh-install (and corrupt-state fallback) state:
// slot a active and known-good, no attempts burned.
func DefaultState() State { return State{Active: "a", KnownGood: "a"} }

// validSlot reports whether s names one of the two slots.
func validSlot(s string) bool { return s == "a" || s == "b" }

// LoadState reads and validates the state at path. A missing file returns an
// error wrapping fs.ErrNotExist so callers can distinguish "not a slot
// install" (dev mode, pre-migration device) from a corrupt document.
func LoadState(path string) (State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return State{}, fmt.Errorf("update: reading state: %w", err)
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, fmt.Errorf("update: parsing state %s: %w", path, err)
	}
	if !validSlot(s.Active) {
		return State{}, fmt.Errorf("update: state %s: invalid active slot %q", path, s.Active)
	}
	if !validSlot(s.KnownGood) {
		return State{}, fmt.Errorf("update: state %s: invalid known_good slot %q", path, s.KnownGood)
	}
	return s, nil
}

// LoadStateOrDefault is the launcher's tolerant read: ANY failure (missing,
// unreadable, corrupt, invalid slots) degrades to DefaultState rather than
// refusing to boot (spec §2: degrade, never refuse).
func LoadStateOrDefault(path string) State {
	s, err := LoadState(path)
	if err != nil {
		return DefaultState()
	}
	return s
}

// SaveState writes s atomically: temp file in the same directory, fsync,
// rename over path — the same pattern as config.saveRaw. The parent
// directory is created if missing (first boot after migration).
func SaveState(path string, s State) error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("update: encoding state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("update: creating state dir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("update: creating temp state: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: chmod temp state: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: writing temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: closing temp state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("update: renaming state into place: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add internal/update/state.go internal/update/state_test.go
git commit -m "feat(update): A/B updater state file with atomic writes (#16)"
```

---

### Task 2: Slot selection, promotion, rollback dismiss (`internal/update/slots.go`)

The pure decision logic at the heart of #18: boot-attempt counting, rollback flip, double-fault → recovery, health promotion, marker lifecycle.

**Files:**
- Create: `internal/update/slots.go`
- Test: `internal/update/slots_test.go`

**Interfaces:**
- Consumes: `State`, `LoadState`, `SaveState` (Task 1).
- Produces:
  - `const MaxBootAttempts = 3`
  - `type BootDecision struct { Slot string; Recovery, RolledBack bool; State State }`
  - `func Decide(s State) BootDecision` — pure; `.State` is what the launcher persists before exec.
  - `func Promote(path, version string) error` — payload's health-check promotion; missing state file is a silent no-op (dev mode).
  - `func DismissRollback(path string) error` — clears `RolledBackFrom`; missing state file is a no-op.

**Semantics (spec §2, encode exactly):**
- `Decide` increments `BootAttempts` first (a slot that segfaults instantly still burned an attempt).
- attempts ≤ 3 after increment → boot `Active`.
- attempts > 3 and `Active ≠ KnownGood` → **rollback**: `RolledBackFrom = ActiveVersion`, `Active = KnownGood`, `ActiveVersion = KnownGoodVersion`, `BootAttempts = 1` (this boot is the known-good slot's first attempt), `RolledBack: true`.
- attempts > 3 and `Active == KnownGood` → **double fault**: `Recovery: true`, boot `KnownGood` with `BootAttempts = 0` (so an operator-triggered restart from recovery retries a normal boot).
- `Promote`: `BootAttempts = 0`; `wasUpdate := KnownGood != Active`; set `ActiveVersion`, `KnownGoodVersion` to `version` and `KnownGood = Active`; clear `RolledBackFrom` only when `wasUpdate` (a routine healthy boot must not silently clear an unacknowledged rollback marker).

- [ ] **Step 1: Write the failing test**

```go
// internal/update/slots_test.go
package update

import (
	"path/filepath"
	"testing"
)

func TestDecide(t *testing.T) {
	tests := []struct {
		name string
		in   State
		want BootDecision
	}{
		{
			name: "fresh boot, attempts increment",
			in:   State{Active: "a", KnownGood: "a"},
			want: BootDecision{Slot: "a", State: State{Active: "a", KnownGood: "a", BootAttempts: 1}},
		},
		{
			name: "new slot within attempt budget",
			in:   State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 2},
			want: BootDecision{Slot: "b", State: State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3}},
		},
		{
			name: "new slot exhausted attempts: rollback to known-good",
			in:   State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3, VersionFloor: "v0.1.0"},
			want: BootDecision{Slot: "a", RolledBack: true, State: State{
				Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0",
				BootAttempts: 1, VersionFloor: "v0.1.0", RolledBackFrom: "v0.2.0",
			}},
		},
		{
			name: "known-good itself exhausted attempts: double fault, recovery",
			in:   State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3},
			want: BootDecision{Slot: "a", Recovery: true, State: State{
				Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 0,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(tt.in); got != tt.want {
				t.Errorf("Decide(%+v)\n got %+v\nwant %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestPromote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	// Pending update in slot b, one attempt burned, rollback marker from an
	// earlier bad release still showing.
	seed := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 1, RolledBackFrom: "v0.1.9",
	}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	if err := Promote(path, "v0.2.0"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "b", KnownGoodVersion: "v0.2.0",
		BootAttempts: 0, RolledBackFrom: "",
	}
	if got != want {
		t.Errorf("after update promote:\n got %+v\nwant %+v", got, want)
	}
}

func TestPromoteRoutineBootKeepsRollbackMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	seed := State{
		Active: "a", ActiveVersion: "v0.1.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 1, RolledBackFrom: "v0.2.0",
	}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	if err := Promote(path, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(path)
	if got.RolledBackFrom != "v0.2.0" {
		t.Errorf("routine healthy boot cleared RolledBackFrom (= %q); only an update promote or dismiss may", got.RolledBackFrom)
	}
	if got.BootAttempts != 0 {
		t.Errorf("BootAttempts = %d, want 0", got.BootAttempts)
	}
}

func TestPromoteMissingStateIsNoop(t *testing.T) {
	if err := Promote(filepath.Join(t.TempDir(), "absent.json"), "v0.1.0"); err != nil {
		t.Errorf("Promote without a state file (dev mode) must be a no-op, got %v", err)
	}
}

func TestDismissRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(path, State{Active: "a", KnownGood: "a", RolledBackFrom: "v0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := DismissRollback(path); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadState(path)
	if got.RolledBackFrom != "" {
		t.Errorf("RolledBackFrom = %q after dismiss, want empty", got.RolledBackFrom)
	}
	// Missing state file: no-op, no error.
	if err := DismissRollback(filepath.Join(t.TempDir(), "absent.json")); err != nil {
		t.Errorf("dismiss without state file: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestDecide|TestPromote|TestDismiss' -v`
Expected: FAIL — undefined: `Decide`, `BootDecision`, `Promote`, `DismissRollback`

- [ ] **Step 3: Write the implementation**

```go
// internal/update/slots.go
package update

import (
	"errors"
	"io/fs"
)

// MaxBootAttempts is how many launcher execs a slot gets before the
// launcher gives up on it (spec §2: "3 strikes").
const MaxBootAttempts = 3

// BootDecision is Decide's verdict: which slot to exec, whether to pass
// --recovery, and the state the launcher must persist BEFORE exec (so a
// slot that segfaults instantly still burned its attempt).
type BootDecision struct {
	Slot string
	// Recovery selects the payload's --recovery mode (double fault: the
	// known-good slot itself never came healthy).
	Recovery bool
	// RolledBack reports that THIS decision performed the rollback flip,
	// for the launcher's log line.
	RolledBack bool
	State      State
}

// Decide implements the launcher's slot selection (spec §2). It is pure:
// the caller persists .State and execs .Slot.
func Decide(s State) BootDecision {
	s.BootAttempts++
	if s.BootAttempts <= MaxBootAttempts {
		return BootDecision{Slot: s.Active, State: s}
	}
	if s.Active != s.KnownGood {
		// Rollback: the pending slot never came healthy. Flip back to
		// known-good and record what we abandoned for the web UI.
		s.RolledBackFrom = s.ActiveVersion
		s.Active = s.KnownGood
		s.ActiveVersion = s.KnownGoodVersion
		s.BootAttempts = 1 // this boot is known-good's first attempt
		return BootDecision{Slot: s.Active, RolledBack: true, State: s}
	}
	// Double fault: known-good itself is failing. Enter recovery mode with
	// the counter reset so an operator-triggered restart from the recovery
	// web UI retries a normal boot.
	s.BootAttempts = 0
	return BootDecision{Slot: s.KnownGood, Recovery: true, State: s}
}

// Promote marks the running slot healthy: the payload calls this once its
// health check passes (render loop up + web self-probe OK, spec §2). A
// missing state file means this is not a slot install (dev mode,
// pre-migration device) and is a silent no-op.
//
// RolledBackFrom is cleared only on an UPDATE promote (KnownGood was
// pointing elsewhere): a routine healthy boot must not silently clear an
// unacknowledged rollback marker the operator hasn't seen yet.
func Promote(path, version string) error {
	s, err := LoadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	wasUpdate := s.KnownGood != s.Active
	s.BootAttempts = 0
	s.ActiveVersion = version
	s.KnownGood = s.Active
	s.KnownGoodVersion = version
	if wasUpdate {
		s.RolledBackFrom = ""
	}
	return SaveState(path, s)
}

// DismissRollback clears the rollback marker (operator acknowledged it in
// the web UI). Missing state file is a no-op, same as Promote.
func DismissRollback(path string) error {
	s, err := LoadState(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	s.RolledBackFrom = ""
	return SaveState(path, s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add internal/update/slots.go internal/update/slots_test.go
git commit -m "feat(update): slot selection, rollback, promotion, dismiss (#18)"
```

---

### Task 3: Trusted keyring + minisign verification (`internal/update/keyring.go`)

Two embedded minisign public keys (CI + offline recovery); a manifest verifies if ANY keyring key signed it. No wall-clock checks (#17). Adds the `go-minisign` dependency.

**Files:**
- Create: `internal/update/keyring.go`
- Test: `internal/update/keyring_test.go`

**Interfaces:**
- Produces:
  - `func Keyring() ([]minisign.PublicKey, error)` — parses the embedded key list; errors if empty ("keyring empty — key ceremony not run").
  - `func ParsePublicKeys(lines []string) ([]minisign.PublicKey, error)`
  - `func VerifyManifest(keys []minisign.PublicKey, message, sigFile []byte) error` — nil error ⇔ some key verifies.
  - `var embeddedKeys []string` — base64 key lines; EMPTY until Task 17's key ceremony fills it (Keyring() errors until then; the checker surfaces that as "updates unavailable", it never crashes the board).

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/jedisct1/go-minisign
go mod tidy
```

- [ ] **Step 2: Write the failing test**

The test helper generates a real ed25519 keypair and emits minisign's *file formats* (public-key base64 line; 4-line `.minisig` signature file) so the production parse+verify path is exercised end-to-end without the minisign CLI. It uses the legacy `Ed` (non-prehashed) algorithm — go-minisign verifies both `Ed` and `ED`, and production signatures from the CLI (prehashed `ED`) take the identical code path through `VerifyManifest`.

> **Note for the implementer:** minisign's global signature covers `signature_bytes || trusted_comment_text`. If `TestVerifyManifest` fails only on the global-signature check, read go-minisign's `Verify` source (`~/go/pkg/mod/github.com/jedisct1/go-minisign*/minisign.go`) and match exactly what it concatenates (with or without the `trusted comment: ` prefix) in `signSigFile` below. That is the single permitted adjustment.

```go
// internal/update/keyring_test.go
package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// testKeypair returns a minisign-format public key line and a signer over
// raw messages, built from a fresh ed25519 key.
func testKeypair(t *testing.T, keyID string) (pubLine string, sign func(msg []byte) []byte) {
	t.Helper()
	if len(keyID) != 8 {
		t.Fatalf("keyID must be 8 bytes, got %q", keyID)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blob := append([]byte("Ed"), []byte(keyID)...)
	blob = append(blob, pub...)
	return base64.StdEncoding.EncodeToString(blob), func(msg []byte) []byte {
		return signSigFile(priv, keyID, msg)
	}
}

// signSigFile builds a complete .minisig file for msg: untrusted comment,
// base64(alg || key_id || sig), trusted comment, base64(global sig). The
// global signature covers sig_bytes || trusted_comment_text (minisign
// format spec).
func signSigFile(priv ed25519.PrivateKey, keyID string, msg []byte) []byte {
	sig := ed25519.Sign(priv, msg)
	blob := append([]byte("Ed"), []byte(keyID)...)
	blob = append(blob, sig...)
	trusted := "timestamp:0"
	global := ed25519.Sign(priv, append(append([]byte{}, sig...), []byte(trusted)...))
	out := "untrusted comment: test signature\n" +
		base64.StdEncoding.EncodeToString(blob) + "\n" +
		"trusted comment: " + trusted + "\n" +
		base64.StdEncoding.EncodeToString(global) + "\n"
	return []byte(out)
}

func TestParsePublicKeys(t *testing.T) {
	pubLine, _ := testKeypair(t, "AAAAAAAA")
	keys, err := ParsePublicKeys([]string{pubLine})
	if err != nil {
		t.Fatalf("ParsePublicKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if _, err := ParsePublicKeys([]string{"not base64!!"}); err == nil {
		t.Error("garbage key line accepted")
	}
}

func TestVerifyManifest(t *testing.T) {
	msg := []byte(`{"version":"v0.2.0"}`)
	ciPub, ciSign := testKeypair(t, "AAAAAAAA")
	recPub, _ := testKeypair(t, "BBBBBBBB")
	strangerPub, strangerSign := testKeypair(t, "CCCCCCCC")
	_ = strangerPub

	keyring, err := ParsePublicKeys([]string{ciPub, recPub})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("good signature from a keyring key verifies", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, ciSign(msg)); err != nil {
			t.Errorf("VerifyManifest: %v", err)
		}
	})
	t.Run("signature from an unknown key is rejected", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, strangerSign(msg)); err == nil {
			t.Error("unknown key's signature accepted")
		}
	})
	t.Run("tampered message is rejected", func(t *testing.T) {
		sig := ciSign(msg)
		if err := VerifyManifest(keyring, []byte(`{"version":"v9.9.9"}`), sig); err == nil {
			t.Error("tampered message accepted")
		}
	})
	t.Run("garbage signature file is rejected", func(t *testing.T) {
		if err := VerifyManifest(keyring, msg, []byte("garbage")); err == nil {
			t.Error("garbage sig file accepted")
		}
	})
	t.Run("empty keyring is rejected", func(t *testing.T) {
		if err := VerifyManifest(nil, msg, ciSign(msg)); err == nil {
			t.Error("empty keyring accepted a signature")
		}
	})
}

func TestKeyringEmptyUntilCeremony(t *testing.T) {
	// embeddedKeys ships empty until the attended key ceremony (Task 17)
	// pastes the real public keys in. Keyring() must error, not panic.
	if len(embeddedKeys) == 0 {
		if _, err := Keyring(); err == nil {
			t.Error("Keyring() with no embedded keys must error")
		}
	} else {
		if _, err := Keyring(); err != nil {
			t.Errorf("Keyring() with embedded keys: %v", err)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestParsePublicKeys|TestVerifyManifest|TestKeyring' -v`
Expected: FAIL — undefined: `ParsePublicKeys`, `VerifyManifest`, `Keyring`, `embeddedKeys`

- [ ] **Step 4: Write the implementation**

```go
// internal/update/keyring.go
package update

import (
	"errors"
	"fmt"

	minisign "github.com/jedisct1/go-minisign"
)

// embeddedKeys is the device's trusted keyring: minisign public keys, one
// base64 line each (the second line of a .pub file). Two keys by design
// (spec §1): the CI signing key (GitHub Actions secret) and the offline
// recovery key (operator's password manager). A manifest is trusted if ANY
// key here signed it — that overlap is what makes key rotation shippable
// as a normal signed update.
//
// EMPTY until the key ceremony (deploy.md §Self-update key ceremony) runs;
// until then Keyring() errors and the updater reports itself unavailable.
var embeddedKeys = []string{}

// Keyring parses the embedded trusted keys.
func Keyring() ([]minisign.PublicKey, error) {
	if len(embeddedKeys) == 0 {
		return nil, errors.New("update: keyring is empty (key ceremony not run)")
	}
	return ParsePublicKeys(embeddedKeys)
}

// ParsePublicKeys parses base64 minisign public-key lines.
func ParsePublicKeys(lines []string) ([]minisign.PublicKey, error) {
	keys := make([]minisign.PublicKey, 0, len(lines))
	for i, l := range lines {
		k, err := minisign.NewPublicKey(l)
		if err != nil {
			return nil, fmt.Errorf("update: keyring entry %d: %w", i, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// VerifyManifest checks that sigFile is a valid minisign signature over
// message by ANY key in keys. There are deliberately no time-based checks
// here (#17): trust is bounded by the signed version floor, not wall-clock
// expiry, because a headless Pi without RTC must verify updates before NTP.
func VerifyManifest(keys []minisign.PublicKey, message, sigFile []byte) error {
	if len(keys) == 0 {
		return errors.New("update: keyring is empty")
	}
	sig, err := minisign.DecodeSignature(string(sigFile))
	if err != nil {
		return fmt.Errorf("update: parsing manifest signature: %w", err)
	}
	for i := range keys {
		ok, err := keys[i].Verify(message, sig)
		if ok && err == nil {
			return nil
		}
	}
	return errors.New("update: manifest signature not made by any trusted key")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS. If only the global-signature sub-check fails, apply the single permitted adjustment from Step 2's note.

- [ ] **Step 6: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add go.mod go.sum internal/update/keyring.go internal/update/keyring_test.go
git commit -m "feat(update): trusted minisign keyring, any-key manifest verification (#17)"
```

---

### Task 4: Manifest schema + installability checks (`internal/update/manifest.go`)

The signed manifest binds version/commit/arch/asset/sha256/min_version; installability = arch match + strictly-newer + at-or-above floor (spec §1). Adds the `golang.org/x/mod` dependency.

**Files:**
- Create: `internal/update/manifest.go`
- Test: `internal/update/manifest_test.go`

**Interfaces:**
- Produces:
  - `type Manifest struct { Version, Channel, Commit, Arch, Asset, SHA256, MinVersion string }` (JSON tags: `version`, `channel`, `commit`, `arch`, `asset`, `sha256`, `min_version`)
  - `const RequiredArch = "linux/arm64"`
  - `func ParseManifest(raw []byte) (Manifest, error)`
  - `func (m Manifest) CheckInstallable(running, floor string) error`
  - `func maxVersion(a, b string) string` — semver max; an invalid side loses.

- [ ] **Step 1: Add the dependency**

```bash
go get golang.org/x/mod
go mod tidy
```

- [ ] **Step 2: Write the failing test**

```go
// internal/update/manifest_test.go
package update

import (
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234",
		Arch: "linux/arm64", Asset: "trainboard_v0.2.0_linux_arm64.gz",
		SHA256: strings.Repeat("ab", 32), MinVersion: "v0.1.0",
	}
}

func TestParseManifest(t *testing.T) {
	raw := []byte(`{"version":"v0.2.0","channel":"stable","commit":"abc1234",` +
		`"arch":"linux/arm64","asset":"trainboard_v0.2.0_linux_arm64.gz",` +
		`"sha256":"` + strings.Repeat("ab", 32) + `","min_version":"v0.1.0"}`)
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if m != validManifest() {
		t.Errorf("got %+v, want %+v", m, validManifest())
	}
	if _, err := ParseManifest([]byte("{nope")); err == nil {
		t.Error("garbage JSON accepted")
	}
}

func TestCheckInstallable(t *testing.T) {
	mod := func(f func(*Manifest)) Manifest { m := validManifest(); f(&m); return m }
	tests := []struct {
		name           string
		m              Manifest
		running, floor string
		wantErr        string // substring; "" = installable
	}{
		{name: "upgrade from older release", m: validManifest(), running: "v0.1.0", floor: "v0.1.0"},
		{name: "dev build always upgradeable", m: validManifest(), running: "dev", floor: ""},
		{name: "empty floor ok", m: validManifest(), running: "v0.1.0", floor: ""},
		{name: "same version rejected", m: validManifest(), running: "v0.2.0", floor: "", wantErr: "not newer"},
		{name: "downgrade rejected", m: validManifest(), running: "v0.3.0", floor: "", wantErr: "not newer"},
		{name: "replayed manifest below floor rejected",
			m: validManifest(), running: "dev", floor: "v0.5.0", wantErr: "version floor"},
		{name: "wrong arch rejected",
			m: mod(func(m *Manifest) { m.Arch = "linux/amd64" }), running: "v0.1.0", wantErr: "arch"},
		{name: "invalid semver version rejected",
			m: mod(func(m *Manifest) { m.Version = "banana" }), running: "v0.1.0", wantErr: "semver"},
		{name: "invalid min_version rejected",
			m: mod(func(m *Manifest) { m.MinVersion = "banana" }), running: "v0.1.0", wantErr: "min_version"},
		{name: "missing asset rejected",
			m: mod(func(m *Manifest) { m.Asset = "" }), running: "v0.1.0", wantErr: "asset"},
		{name: "missing sha256 rejected",
			m: mod(func(m *Manifest) { m.SHA256 = "" }), running: "v0.1.0", wantErr: "sha256"},
		{name: "prerelease ordering: v0.2.0-rc1 not newer than v0.2.0",
			m: mod(func(m *Manifest) { m.Version = "v0.2.0-rc1" }), running: "v0.2.0", wantErr: "not newer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.m.CheckInstallable(tt.running, tt.floor)
			if tt.wantErr == "" && err != nil {
				t.Errorf("CheckInstallable: %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("CheckInstallable = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestMaxVersion(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{"v0.1.0", "v0.2.0", "v0.2.0"},
		{"v0.2.0", "v0.1.0", "v0.2.0"},
		{"", "v0.1.0", "v0.1.0"},
		{"v0.1.0", "", "v0.1.0"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := maxVersion(tt.a, tt.b); got != tt.want {
			t.Errorf("maxVersion(%q,%q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestParseManifest|TestCheckInstallable|TestMaxVersion' -v`
Expected: FAIL — undefined: `Manifest`, `ParseManifest`, etc.

- [ ] **Step 4: Write the implementation**

```go
// internal/update/manifest.go
package update

import (
	"encoding/json"
	"fmt"

	"golang.org/x/mod/semver"
)

// RequiredArch is the only architecture this device installs. It is checked
// against the SIGNED manifest, so a mismatched-arch asset can't be swapped
// in even with a valid release signature (spec §1).
const RequiredArch = "linux/arm64"

// Manifest is the signed release descriptor — the ONLY signed object; it
// binds everything else (spec §1). sha256 is of the DECOMPRESSED binary.
type Manifest struct {
	Version    string `json:"version"`
	Channel    string `json:"channel"`
	Commit     string `json:"commit"`
	Arch       string `json:"arch"`
	Asset      string `json:"asset"`
	SHA256     string `json:"sha256"`
	MinVersion string `json:"min_version"`
}

// ParseManifest decodes a manifest document. Verify the signature FIRST
// (VerifyManifest) — parsing is not validation.
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("update: parsing manifest: %w", err)
	}
	return m, nil
}

// CheckInstallable runs the three anti-rollback/replay checks from spec §1
// against a signature-verified manifest:
//
//  1. arch must match this device;
//  2. version must be strictly newer than running — except that a
//     non-semver running version ("dev" builds, a just-migrated device)
//     never blocks an upgrade;
//  3. version must be at or above the device's persisted version floor, so
//     a validly-signed OLD manifest can't be replayed to downgrade.
//
// There are deliberately no time-based checks (#17).
func (m Manifest) CheckInstallable(running, floor string) error {
	if m.Arch != RequiredArch {
		return fmt.Errorf("update: manifest arch %q does not match device arch %q", m.Arch, RequiredArch)
	}
	if !semver.IsValid(m.Version) {
		return fmt.Errorf("update: manifest version %q is not valid semver", m.Version)
	}
	if m.MinVersion != "" && !semver.IsValid(m.MinVersion) {
		return fmt.Errorf("update: manifest min_version %q is not valid semver", m.MinVersion)
	}
	if m.Asset == "" {
		return fmt.Errorf("update: manifest has no asset name")
	}
	if m.SHA256 == "" {
		return fmt.Errorf("update: manifest has no sha256")
	}
	if semver.IsValid(running) && semver.Compare(m.Version, running) <= 0 {
		return fmt.Errorf("update: %s is not newer than running %s", m.Version, running)
	}
	if semver.IsValid(floor) && semver.Compare(m.Version, floor) < 0 {
		return fmt.Errorf("update: %s is below the version floor %s (rollback protection)", m.Version, floor)
	}
	return nil
}

// maxVersion returns the semver-greater of a and b; an invalid (or empty)
// side loses. Used to ratchet the persisted version floor monotonically.
func maxVersion(a, b string) string {
	switch {
	case !semver.IsValid(a):
		return b
	case !semver.IsValid(b):
		return a
	case semver.Compare(a, b) >= 0:
		return a
	default:
		return b
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS

- [ ] **Step 6: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add go.mod go.sum internal/update/manifest.go internal/update/manifest_test.go
git commit -m "feat(update): signed manifest schema + anti-rollback installability checks (#16, #17)"
```

---

### Task 5: Config `update` section + unattended-update window (`internal/config`)

New config section. **Field-naming decision (deviation from the spec's table, same behaviour):** the spec's `checkEnabled` default-true cannot survive JSON round-trips of existing configs (a missing field unmarshals to `false`). Invert it: `DisableChecks bool` — the zero value of every field is the desired default (`Channel: ""` ⇒ stable, `AutoApply: false`, `DisableChecks: false` ⇒ checks on), so existing on-disk configs need no migration.

**Files:**
- Modify: `internal/config/config.go` (add `UpdateConfig`, wire into `Config` + `Default()`)
- Modify: `internal/config/validate.go` (channel validation)
- Modify: `internal/config/powersaving.go` (extract `inHHMMWindow`, add `InUpdateWindow`)
- Test: `internal/config/update_test.go` (new), existing tests must stay green

**Interfaces:**
- Produces:
  - `type UpdateConfig struct { Channel string; AutoApply bool; DisableChecks bool }` (JSON tags: `channel`, `autoApply`, `disableChecks`)
  - `Config.Update UpdateConfig` field (JSON key `update`)
  - `func (u UpdateConfig) EffectiveChannel() string` — `""` ⇒ `"stable"`
  - `func (c Config) InUpdateWindow(t time.Time) bool` — powersave window when enabled, else 03:00–05:00 Europe/London.

- [ ] **Step 1: Write the failing test**

```go
// internal/config/update_test.go
package config

import (
	"encoding/json"
	"testing"
	"time"
)

func TestUpdateConfigZeroValueDefaults(t *testing.T) {
	// A pre-M5 config document has no "update" key at all; its zero value
	// must mean: stable channel, no auto-apply, checks enabled.
	var c Config
	if err := json.Unmarshal([]byte(`{"version":1}`), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Update.EffectiveChannel(); got != "stable" {
		t.Errorf("EffectiveChannel = %q, want stable", got)
	}
	if c.Update.AutoApply || c.Update.DisableChecks {
		t.Errorf("zero value: AutoApply=%v DisableChecks=%v, want false/false",
			c.Update.AutoApply, c.Update.DisableChecks)
	}
}

func TestUpdateChannelValidation(t *testing.T) {
	base := Default()
	base.Board.Origin = "KGX"
	base.Darwin.Token = "tok"
	for _, ch := range []string{"", "stable", "prerelease"} {
		c := base
		c.Update.Channel = ch
		if err := c.Validate(); err != nil {
			t.Errorf("channel %q rejected: %v", ch, err)
		}
	}
	c := base
	c.Update.Channel = "nightly"
	if err := c.Validate(); err == nil {
		t.Error("channel \"nightly\" accepted")
	}
}

func TestInUpdateWindow(t *testing.T) {
	// Times below are Europe/London wall-clock (project TZ rule).
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	at := func(hhmm string) time.Time {
		tt, err := time.ParseInLocation("2006-01-02 15:04", "2026-07-09 "+hhmm, london)
		if err != nil {
			t.Fatal(err)
		}
		return tt
	}

	var c Config // powersaving disabled ⇒ fallback window 03:00–05:00
	if !c.InUpdateWindow(at("04:00")) {
		t.Error("04:00 not in default window")
	}
	if c.InUpdateWindow(at("12:00")) || c.InUpdateWindow(at("05:00")) {
		t.Error("12:00 or 05:00 wrongly inside default window (end is exclusive)")
	}

	c.Powersaving = PowersavingConfig{Enabled: true, Start: "23:00", End: "07:00", Brightness: 32}
	if !c.InUpdateWindow(at("23:30")) || !c.InUpdateWindow(at("06:00")) {
		t.Error("cross-midnight powersave window not honoured")
	}
	if c.InUpdateWindow(at("12:00")) {
		t.Error("midday wrongly inside powersave window")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestUpdate|TestInUpdateWindow' -v`
Expected: FAIL — `c.Update` undefined, `InUpdateWindow` undefined

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add to the `Config` struct (after `Wifi`):

```go
	Update      UpdateConfig      `json:"update"`
```

and below `WifiConfig`:

```go
// UpdateConfig controls M5 self-update behaviour. Field polarity is chosen
// so the ZERO VALUE is the desired default for configs written before this
// section existed (a missing JSON key unmarshals to the zero value):
// Channel "" means stable, DisableChecks false means periodic checks run.
// That is why this is DisableChecks and not the spec table's checkEnabled —
// same behaviour, migration-free encoding.
type UpdateConfig struct {
	// Channel is "stable" (or "", its equivalent) or "prerelease".
	Channel string `json:"channel"`
	// AutoApply applies available updates unattended during
	// Config.InUpdateWindow. Off by default: manual apply from the web UI.
	AutoApply bool `json:"autoApply"`
	// DisableChecks turns the periodic GitHub release check off entirely.
	DisableChecks bool `json:"disableChecks"`
}

// EffectiveChannel maps the empty channel to "stable".
func (u UpdateConfig) EffectiveChannel() string {
	if u.Channel == "" {
		return "stable"
	}
	return u.Channel
}
```

`Default()` needs no change (zero values are the defaults), but add `Update: UpdateConfig{Channel: "stable"}` anyway so freshly-written documents are explicit.

In `internal/config/validate.go`, add to `Validate()` before the `validateWifi` call:

```go
	if c.Update.Channel != "" && c.Update.Channel != "stable" && c.Update.Channel != "prerelease" {
		return fmt.Errorf("config: update.channel %q must be stable or prerelease", c.Update.Channel)
	}
```

In `internal/config/powersaving.go`, extract the window math from `BrightnessAt` and add the update window:

```go
// inHHMMWindow reports whether t (converted to Europe/London) falls inside
// the [start, end) wall-clock window; start > end means the window crosses
// midnight. Malformed HH:MM strings report false.
func inHHMMWindow(t time.Time, startHHMM, endHHMM string) bool {
	start, err1 := time.Parse("15:04", startHHMM)
	end, err2 := time.Parse("15:04", endHHMM)
	if err1 != nil || err2 != nil {
		return false
	}
	local := t.In(tz.Location())
	nowMin := local.Hour()*60 + local.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin // same-day window
	}
	return nowMin >= startMin || nowMin < endMin // cross-midnight window
}

// InUpdateWindow reports whether t is inside the unattended-update window
// (spec §3): the powersaving window when one is configured (display already
// dark — never mid-evening), else 03:00–05:00 Europe/London.
func (c Config) InUpdateWindow(t time.Time) bool {
	if c.Powersaving.Enabled && isHHMM(c.Powersaving.Start) && isHHMM(c.Powersaving.End) {
		return inHHMMWindow(t, c.Powersaving.Start, c.Powersaving.End)
	}
	return inHHMMWindow(t, "03:00", "05:00")
}
```

Rewrite `BrightnessAt`'s body to use the helper (behaviour unchanged; its existing tests are the regression net):

```go
func (c Config) BrightnessAt(t time.Time) int {
	if !c.Powersaving.Enabled {
		return NormalBrightness
	}
	if !isHHMM(c.Powersaving.Start) || !isHHMM(c.Powersaving.End) {
		return NormalBrightness
	}
	if inHHMMWindow(t, c.Powersaving.Start, c.Powersaving.End) {
		return c.Powersaving.Brightness
	}
	return NormalBrightness
}
```

- [ ] **Step 4: Run tests to verify they pass (including the existing powersaving suite)**

Run: `go test ./internal/config/ -v`
Expected: PASS — new tests AND all pre-existing `TestBrightnessAt`/store/validate tests.

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/config/ -count=1
git add internal/config/
git commit -m "feat(config): update section (channel/autoApply/disableChecks) + unattended window (#19)"
```

---

### Task 6: GitHub release discovery (`internal/update/github.go`)

Finds the newest release matching the channel via the GitHub releases API (unauthenticated; one device is far inside rate limits).

**Files:**
- Create: `internal/update/github.go`
- Test: `internal/update/github_test.go`

**Interfaces:**
- Produces:
  - `type Asset struct { Name, URL string }`
  - `type Release struct { Version string; Prerelease bool; NotesURL string; Assets []Asset }`
  - `type Client struct { ReleasesURL string; HTTP *http.Client }`
  - `func NewClient() *Client` — production URL + 30s timeout.
  - `func (c *Client) LatestRelease(ctx context.Context, channel string) (*Release, error)` — nil, nil when no release matches.
  - `func (r *Release) AssetURL(name string) (string, bool)`

- [ ] **Step 1: Write the failing test**

```go
// internal/update/github_test.go
package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const releasesJSON = `[
  {"tag_name":"v0.3.0-rc1","prerelease":true,"draft":false,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v0.3.0-rc1",
   "assets":[{"name":"manifest.json","browser_download_url":"https://dl/rc1/manifest.json"}]},
  {"tag_name":"v0.2.0","prerelease":false,"draft":false,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v0.2.0",
   "assets":[
     {"name":"manifest.json","browser_download_url":"https://dl/v020/manifest.json"},
     {"name":"manifest.json.minisig","browser_download_url":"https://dl/v020/manifest.json.minisig"},
     {"name":"trainboard_v0.2.0_linux_arm64.gz","browser_download_url":"https://dl/v020/bin.gz"}]},
  {"tag_name":"v9.9.9","prerelease":false,"draft":true,
   "html_url":"https://github.com/mintopia/trainboard/releases/tag/v9.9.9","assets":[]}
]`

func newTestClient(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := NewClient()
	c.ReleasesURL = srv.URL
	return c
}

func TestLatestReleaseStableSkipsPrereleaseAndDraft(t *testing.T) {
	c := newTestClient(t, http.StatusOK, releasesJSON)
	rel, err := c.LatestRelease(context.Background(), "stable")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if rel == nil || rel.Version != "v0.2.0" || rel.Prerelease {
		t.Fatalf("got %+v, want v0.2.0 stable", rel)
	}
	if url, ok := rel.AssetURL("manifest.json.minisig"); !ok || url != "https://dl/v020/manifest.json.minisig" {
		t.Errorf("AssetURL = %q,%v", url, ok)
	}
	if _, ok := rel.AssetURL("absent"); ok {
		t.Error("AssetURL found an absent asset")
	}
	if rel.NotesURL == "" {
		t.Error("NotesURL empty")
	}
}

func TestLatestReleasePrereleaseChannelTakesNewest(t *testing.T) {
	c := newTestClient(t, http.StatusOK, releasesJSON)
	rel, err := c.LatestRelease(context.Background(), "prerelease")
	if err != nil {
		t.Fatal(err)
	}
	if rel == nil || rel.Version != "v0.3.0-rc1" {
		t.Fatalf("got %+v, want v0.3.0-rc1", rel)
	}
}

func TestLatestReleaseNoneMatching(t *testing.T) {
	c := newTestClient(t, http.StatusOK, `[]`)
	rel, err := c.LatestRelease(context.Background(), "stable")
	if err != nil || rel != nil {
		t.Errorf("got %+v, %v; want nil, nil", rel, err)
	}
}

func TestLatestReleaseHTTPError(t *testing.T) {
	c := newTestClient(t, http.StatusForbidden, `{"message":"rate limited"}`)
	if _, err := c.LatestRelease(context.Background(), "stable"); err == nil {
		t.Error("HTTP 403 not surfaced as error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run TestLatestRelease -v`
Expected: FAIL — undefined: `Client`, `NewClient`, `Release`

- [ ] **Step 3: Implement**

```go
// internal/update/github.go
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// defaultReleasesURL is this repository's releases feed. Unauthenticated:
// one device checking every 6h is far inside GitHub's anonymous rate limit.
const defaultReleasesURL = "https://api.github.com/repos/mintopia/trainboard/releases"

// maxAPIResponse bounds how much of the releases feed we read (defensive;
// the feed for this repo is a few KB).
const maxAPIResponse = 1 << 20 // 1 MiB

// Asset is a downloadable release artifact.
type Asset struct {
	Name string
	URL  string
}

// Release is one GitHub release, reduced to what the updater needs.
type Release struct {
	Version    string // tag name, e.g. "v0.2.0"
	Prerelease bool
	NotesURL   string // release page, linked from the web UI
	Assets     []Asset
}

// AssetURL finds a release asset's download URL by exact name.
func (r *Release) AssetURL(name string) (string, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL, true
		}
	}
	return "", false
}

// Client queries the GitHub releases API.
type Client struct {
	ReleasesURL string
	HTTP        *http.Client
}

// NewClient returns a production client (30s timeout: WiFi Pi, small JSON).
func NewClient() *Client {
	return &Client{ReleasesURL: defaultReleasesURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// ghRelease/ghAsset mirror the fields we read from the API document.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Prerelease bool      `json:"prerelease"`
	Draft      bool      `json:"draft"`
	HTMLURL    string    `json:"html_url"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// LatestRelease returns the newest (API order: most recent first) non-draft
// release matching channel: "stable" skips prereleases, "prerelease"
// accepts anything. (nil, nil) when no release matches — a fresh repo, or a
// prerelease-only history on the stable channel.
func (c *Client) LatestRelease(ctx context.Context, channel string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ReleasesURL+"?per_page=10", nil)
	if err != nil {
		return nil, fmt.Errorf("update: building releases request: %w", err)
	}
	// GitHub's API requires a User-Agent.
	req.Header.Set("User-Agent", "trainboard-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetching releases: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: releases API returned %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return nil, fmt.Errorf("update: reading releases response: %w", err)
	}
	var rels []ghRelease
	if err := json.Unmarshal(raw, &rels); err != nil {
		return nil, fmt.Errorf("update: parsing releases response: %w", err)
	}
	for _, r := range rels {
		if r.Draft {
			continue
		}
		if channel != "prerelease" && r.Prerelease {
			continue
		}
		out := &Release{Version: r.TagName, Prerelease: r.Prerelease, NotesURL: r.HTMLURL}
		for _, a := range r.Assets {
			out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.URL})
		}
		return out, nil
	}
	return nil, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add internal/update/github.go internal/update/github_test.go
git commit -m "feat(update): GitHub release discovery with channel filter (#19)"
```

---

### Task 7: Apply pipeline (`internal/update/apply.go`)

Download manifest + signature → verify → check installable → stream the gzipped asset into the inactive slot (hashing as it goes) → fsync + rename → flip state. The known-good slot is never written: the target is always `otherSlot(KnownGood)`.

**Files:**
- Create: `internal/update/apply.go`
- Test: `internal/update/apply_test.go`

**Interfaces:**
- Consumes: `VerifyManifest`, `ParseManifest`, `Manifest.CheckInstallable`, `maxVersion` (Tasks 3–4), `LoadState`/`SaveState` (Task 1), `Release.AssetURL` (Task 6).
- Produces:
  - `type Applier struct { SlotsDir, StatePath, Running string; Keys []minisign.PublicKey; HTTP *http.Client; Log *slog.Logger }`
  - `func (a *Applier) Apply(ctx context.Context, rel *Release) error`
  - `func otherSlot(s string) string`

- [ ] **Step 1: Write the failing test**

The test builds a fake release server serving a real gzipped "binary", a manifest signed with the Task 3 test helper, and drives `Apply` end-to-end into a temp slots dir.

```go
// internal/update/apply_test.go
package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// applyFixture wires a complete fake release: binary, signed manifest,
// download server, seeded state, and a ready Applier.
type applyFixture struct {
	applier *Applier
	release *Release
	binary  []byte
	state   string // state path
}

func newApplyFixture(t *testing.T, mutate func(*Manifest), seed State) *applyFixture {
	t.Helper()
	binary := []byte("#!/fake-arm64-binary v0.2.0\n" + strings.Repeat("x", 4096))
	sum := sha256.Sum256(binary)

	m := Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234", Arch: RequiredArch,
		Asset: "trainboard_v0.2.0_linux_arm64.gz", SHA256: hex.EncodeToString(sum[:]),
		MinVersion: "v0.1.0",
	}
	if mutate != nil {
		mutate(&m)
	}
	manRaw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	pubLine, sign := testKeypair(t, "AAAAAAAA")
	keys, err := ParsePublicKeys([]string{pubLine})
	if err != nil {
		t.Fatal(err)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(manRaw) })
	mux.HandleFunc("/manifest.json.minisig", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(sign(manRaw)) })
	mux.HandleFunc("/bin.gz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(gz.Bytes()) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := SaveState(statePath, seed); err != nil {
		t.Fatal(err)
	}

	return &applyFixture{
		applier: &Applier{
			SlotsDir: filepath.Join(dir, "slots"), StatePath: statePath,
			Running: "v0.1.0", Keys: keys, HTTP: srv.Client(), Log: testLogger(),
		},
		release: &Release{Version: "v0.2.0", Assets: []Asset{
			{Name: "manifest.json", URL: srv.URL + "/manifest.json"},
			{Name: "manifest.json.minisig", URL: srv.URL + "/manifest.json.minisig"},
			{Name: "trainboard_v0.2.0_linux_arm64.gz", URL: srv.URL + "/bin.gz"},
		}},
		binary: binary,
		state:  statePath,
	}
}

func goodSeed() State {
	return State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", VersionFloor: "v0.1.0"}
}

func TestApplyHappyPath(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	if err := f.applier.Apply(context.Background(), f.release); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Binary landed in the INACTIVE slot (b), decompressed, executable.
	got, err := os.ReadFile(filepath.Join(f.applier.SlotsDir, "b", "trainboard"))
	if err != nil {
		t.Fatalf("installed binary: %v", err)
	}
	if !bytes.Equal(got, f.binary) {
		t.Error("installed binary differs from source")
	}
	info, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed binary not executable: %v", info.Mode())
	}
	// State flipped: b pending, a still known-good, attempts reset,
	// floor ratcheted.
	st, err := LoadState(f.state)
	if err != nil {
		t.Fatal(err)
	}
	want := State{
		Active: "b", ActiveVersion: "v0.2.0",
		KnownGood: "a", KnownGoodVersion: "v0.1.0",
		BootAttempts: 0, VersionFloor: "v0.1.0",
	}
	if st != want {
		t.Errorf("state after apply:\n got %+v\nwant %+v", st, want)
	}
}

func TestApplyNeverWritesKnownGoodSlot(t *testing.T) {
	// Pending unpromoted update already in b (Active=b, KnownGood=a):
	// applying again must overwrite the PENDING slot b, never a.
	seed := State{Active: "b", ActiveVersion: "v0.1.5", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	f := newApplyFixture(t, nil, seed)
	knownGoodBin := filepath.Join(f.applier.SlotsDir, "a", "trainboard")
	if err := os.MkdirAll(filepath.Dir(knownGoodBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knownGoodBin, []byte("known-good, do not touch"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.applier.Apply(context.Background(), f.release); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := os.ReadFile(knownGoodBin)
	if err != nil || string(got) != "known-good, do not touch" {
		t.Fatalf("KNOWN-GOOD SLOT WAS WRITTEN (double-fault guarantee violated): %q, %v", got, err)
	}
	st, _ := LoadState(f.state)
	if st.Active != "b" || st.ActiveVersion != "v0.2.0" || st.KnownGood != "a" {
		t.Errorf("state after re-apply: %+v", st)
	}
}

func TestApplyRejectsBadSHA(t *testing.T) {
	f := newApplyFixture(t, func(m *Manifest) { m.SHA256 = strings.Repeat("00", 32) }, goodSeed())
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("bad sha256: got %v", err)
	}
	// Nothing installed, state untouched.
	if _, err := os.Stat(filepath.Join(f.applier.SlotsDir, "b", "trainboard")); err == nil {
		t.Error("binary installed despite sha mismatch")
	}
	st, _ := LoadState(f.state)
	if st != goodSeed() {
		t.Errorf("state mutated on failed apply: %+v", st)
	}
}

func TestApplyRejectsUnsignedManifest(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	// Replace the signature with one from an untrusted key.
	_, strangerSign := testKeypair(t, "ZZZZZZZZ")
	manURL, _ := f.release.AssetURL("manifest.json")
	resp, err := f.applier.HTTP.Get(manURL)
	if err != nil {
		t.Fatal(err)
	}
	manRaw := make([]byte, 0)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		manRaw = append(manRaw, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	_ = resp.Body.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(strangerSign(manRaw))
	}))
	t.Cleanup(srv.Close)
	for i, a := range f.release.Assets {
		if a.Name == "manifest.json.minisig" {
			f.release.Assets[i].URL = srv.URL
		}
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "trusted") {
		t.Errorf("untrusted signature: got %v", err)
	}
}

func TestApplyRejectsDowngradeAndFloor(t *testing.T) {
	// Running version already newer than the release.
	f := newApplyFixture(t, nil, goodSeed())
	f.applier.Running = "v0.3.0"
	if err := f.applier.Apply(context.Background(), f.release); err == nil || !strings.Contains(err.Error(), "not newer") {
		t.Errorf("downgrade: got %v", err)
	}
	// Replay below the persisted floor.
	seed := goodSeed()
	seed.VersionFloor = "v0.5.0"
	f2 := newApplyFixture(t, nil, seed)
	f2.applier.Running = "dev"
	if err := f2.applier.Apply(context.Background(), f2.release); err == nil || !strings.Contains(err.Error(), "floor") {
		t.Errorf("floor replay: got %v", err)
	}
}

func TestApplyRequiresStateFile(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	if err := os.Remove(f.state); err != nil {
		t.Fatal(err)
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil {
		t.Error("apply without updater state (not a slot install) must fail")
	}
}

func TestApplyTruncatedDownload(t *testing.T) {
	f := newApplyFixture(t, nil, goodSeed())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "999999")
		_, _ = w.Write([]byte("\x1f\x8b\x08\x00trunc")) // gzip magic then garbage/EOF
	}))
	t.Cleanup(srv.Close)
	for i, a := range f.release.Assets {
		if a.Name == "trainboard_v0.2.0_linux_arm64.gz" {
			f.release.Assets[i].URL = srv.URL
		}
	}
	if err := f.applier.Apply(context.Background(), f.release); err == nil {
		t.Error("truncated download accepted")
	}
	st, _ := LoadState(f.state)
	if st != goodSeed() {
		t.Errorf("state mutated on failed apply: %+v", st)
	}
}

func TestOtherSlot(t *testing.T) {
	if otherSlot("a") != "b" || otherSlot("b") != "a" {
		t.Error("otherSlot broken")
	}
}
```

Add a shared test logger helper (used above and by later tasks) in a new file `internal/update/update_test.go`:

```go
// internal/update/update_test.go
package update

import (
	"io"
	"log/slog"
)

// testLogger discards logs; updater tests assert behaviour, not log lines.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestApply|TestOtherSlot' -v`
Expected: FAIL — undefined: `Applier`, `otherSlot`

- [ ] **Step 3: Implement**

```go
// internal/update/apply.go
package update

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	minisign "github.com/jedisct1/go-minisign"
)

// maxBinarySize caps the decompressed payload (decompression-bomb guard;
// the real binary is ~15 MB).
const maxBinarySize = 100 << 20 // 100 MiB

// Applier downloads, verifies, and installs a release into the inactive
// slot (spec §2 apply flow). It never writes the known-good slot.
type Applier struct {
	SlotsDir  string // e.g. /opt/trainboard/slots
	StatePath string
	Running   string // buildinfo.Version() of this process
	Keys      []minisign.PublicKey
	HTTP      *http.Client
	Log       *slog.Logger
}

// otherSlot maps a↔b.
func otherSlot(s string) string {
	if s == "a" {
		return "b"
	}
	return "a"
}

// Apply runs the full pipeline for rel:
//
//  1. fetch manifest.json + manifest.json.minisig; verify the signature
//     against the keyring BEFORE parsing anything;
//  2. CheckInstallable (arch / strictly-newer / floor — spec §1);
//  3. stream the gzipped asset into a temp file in the TARGET slot's own
//     directory (same filesystem ⇒ atomic rename), hashing the
//     decompressed bytes as they land;
//  4. compare sha256, chmod 0755, fsync file + directory, rename;
//  5. flip state: Active=target, attempts reset, floor ratcheted;
//     KnownGood untouched.
//
// The target is always otherSlot(KnownGood) — NOT otherSlot(Active) — so a
// re-apply while an unpromoted update is already pending overwrites the
// pending slot rather than the known-good one (double-fault guarantee).
//
// Every failure path leaves the state document and the known-good slot
// exactly as they were: update failures are non-fatal by design (spec §3).
func (a *Applier) Apply(ctx context.Context, rel *Release) error {
	manURL, ok := rel.AssetURL("manifest.json")
	if !ok {
		return fmt.Errorf("update: release %s has no manifest.json asset", rel.Version)
	}
	sigURL, ok := rel.AssetURL("manifest.json.minisig")
	if !ok {
		return fmt.Errorf("update: release %s has no manifest.json.minisig asset", rel.Version)
	}
	manRaw, err := a.fetchSmall(ctx, manURL)
	if err != nil {
		return err
	}
	sigRaw, err := a.fetchSmall(ctx, sigURL)
	if err != nil {
		return err
	}
	if err := VerifyManifest(a.Keys, manRaw, sigRaw); err != nil {
		return err
	}
	m, err := ParseManifest(manRaw)
	if err != nil {
		return err
	}

	st, err := LoadState(a.StatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("update: no updater state — this is not a slot install (run the migration first)")
	}
	if err != nil {
		return err
	}
	if err := m.CheckInstallable(a.Running, st.VersionFloor); err != nil {
		return err
	}

	target := otherSlot(st.KnownGood)
	assetURL, ok := rel.AssetURL(m.Asset)
	if !ok {
		return fmt.Errorf("update: release %s has no asset %q named by its manifest", rel.Version, m.Asset)
	}
	a.Log.Info("applying update", "version", m.Version, "slot", target, "asset", m.Asset)
	if err := a.installBinary(ctx, assetURL, m.SHA256, target); err != nil {
		return err
	}

	st.Active = target
	st.ActiveVersion = m.Version
	st.BootAttempts = 0
	st.VersionFloor = maxVersion(st.VersionFloor, m.MinVersion)
	if err := SaveState(a.StatePath, st); err != nil {
		return err
	}
	a.Log.Info("update staged; restart to boot it", "version", m.Version, "slot", target)
	return nil
}

// installBinary streams the gzipped asset at url into
// SlotsDir/<slot>/trainboard via a same-directory temp file, verifying the
// decompressed sha256 before the rename makes it visible.
func (a *Applier) installBinary(ctx context.Context, url, wantSHA, slot string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("update: building download request: %w", err)
	}
	req.Header.Set("User-Agent", "trainboard-updater")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("update: downloading %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: downloading asset: %s", resp.Status)
	}

	slotDir := filepath.Join(a.SlotsDir, slot)
	if err := os.MkdirAll(slotDir, 0o755); err != nil {
		return fmt.Errorf("update: creating slot dir: %w", err)
	}
	tmp, err := os.CreateTemp(slotDir, ".trainboard-*.tmp")
	if err != nil {
		return fmt.Errorf("update: creating temp binary: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: asset is not gzip: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(gz, maxBinarySize+1))
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: decompressing asset: %w", err)
	}
	if n > maxBinarySize {
		_ = tmp.Close()
		return fmt.Errorf("update: decompressed asset exceeds %d bytes", maxBinarySize)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != strings.ToLower(wantSHA) {
		_ = tmp.Close()
		return fmt.Errorf("update: sha256 mismatch: manifest %s, downloaded %s", wantSHA, got)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: chmod binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("update: fsync binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("update: closing binary: %w", err)
	}
	final := filepath.Join(slotDir, "trainboard")
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("update: renaming binary into place: %w", err)
	}
	// fsync the directory so the rename itself survives power loss.
	if d, err := os.Open(slotDir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// fetchSmall GETs a small artifact (manifest / signature) with a 1 MiB cap.
func (a *Applier) fetchSmall(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("update: building request for %s: %w", url, err)
	}
	req.Header.Set("User-Agent", "trainboard-updater")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: fetching %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: fetching %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponse))
	if err != nil {
		return nil, fmt.Errorf("update: reading %s: %w", url, err)
	}
	return raw, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS — pay special attention to `TestApplyNeverWritesKnownGoodSlot`.

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add internal/update/apply.go internal/update/apply_test.go internal/update/update_test.go
git commit -m "feat(update): download/verify/install pipeline into inactive slot (#16)"
```

---

### Task 8: Periodic checker + status (`internal/update/checker.go`)

Background check every 6h (evaluated on a 10-minute cadence so opt-in auto-apply can catch the update window promptly), on-demand check/apply for the web UI, and a `Status` snapshot for rendering.

**Files:**
- Create: `internal/update/checker.go`
- Test: `internal/update/checker_test.go`

**Interfaces:**
- Consumes: `Client.LatestRelease` (Task 6), `Applier.Apply` (Task 7), `LoadStateOrDefault` (Task 1), `config.Config.InUpdateWindow` / `Update.EffectiveChannel` / `Update.AutoApply` / `Update.DisableChecks` (Task 5), `semver`.
- Produces (the seams Tasks 13–14 wire into web/main):
  - `type Status struct { Enabled bool; Running, Available, NotesURL string; LastCheck time.Time; LastError, RolledBackFrom string }` (JSON tags: `enabled`, `running`, `available`, `notesUrl`, `lastCheck`, `lastError`, `rolledBackFrom`; string fields `omitempty` except `running`)
  - `func NewChecker(client *Client, applier *Applier, cfg config.Config, enabled bool, log *slog.Logger) *Checker`
  - `func (c *Checker) Run(ctx context.Context, restart func())` — periodic loop; returns immediately when disabled. `restart` fires only on a successful AUTO apply.
  - `func (c *Checker) CheckNow(ctx context.Context) error`
  - `func (c *Checker) ApplyNow(ctx context.Context) error` — does NOT restart (the web handler schedules that itself).
  - `func (c *Checker) Status() Status`

- [ ] **Step 1: Write the failing test**

```go
// internal/update/checker_test.go
package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
)

// checkerFixture: a releases feed server + seeded state + checker.
func newCheckerFixture(t *testing.T, releasesBody string, cfg config.Config, running string) *Checker {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releasesBody))
	}))
	t.Cleanup(srv.Close)
	client := NewClient()
	client.ReleasesURL = srv.URL

	statePath := filepath.Join(t.TempDir(), "state.json")
	seed := DefaultState()
	seed.RolledBackFrom = "v0.1.9"
	if err := SaveState(statePath, seed); err != nil {
		t.Fatal(err)
	}
	applier := &Applier{StatePath: statePath, Running: running, HTTP: srv.Client(), Log: testLogger()}
	return NewChecker(client, applier, cfg, true, testLogger())
}

func TestCheckNowFindsNewerRelease(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "v0.1.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	st := c.Status()
	if st.Available != "v0.2.0" {
		t.Errorf("Available = %q, want v0.2.0", st.Available)
	}
	if st.NotesURL == "" || st.LastCheck.IsZero() || st.LastError != "" {
		t.Errorf("Status = %+v", st)
	}
	if st.RolledBackFrom != "v0.1.9" {
		t.Errorf("RolledBackFrom = %q (must be read live from state)", st.RolledBackFrom)
	}
}

func TestCheckNowAlreadyCurrent(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "v0.2.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "" {
		t.Errorf("Available = %q, want empty (running is current)", got)
	}
}

func TestCheckNowDevBuildSeesUpdate(t *testing.T) {
	c := newCheckerFixture(t, releasesJSON, config.Config{}, "dev")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "v0.2.0" {
		t.Errorf("Available = %q, want v0.2.0 (non-semver running never blocks)", got)
	}
}

func TestCheckNowPrereleaseChannel(t *testing.T) {
	cfg := config.Config{}
	cfg.Update.Channel = "prerelease"
	c := newCheckerFixture(t, releasesJSON, cfg, "v0.2.0")
	if err := c.CheckNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.Status().Available; got != "v0.3.0-rc1" {
		t.Errorf("Available = %q, want v0.3.0-rc1", got)
	}
}

func TestCheckNowRecordsError(t *testing.T) {
	c := newCheckerFixture(t, `{"message":"boom"`, config.Config{}, "v0.1.0")
	if err := c.CheckNow(context.Background()); err == nil {
		t.Fatal("bad feed accepted")
	}
	if st := c.Status(); st.LastError == "" {
		t.Error("LastError not recorded")
	}
}

func TestApplyNowWithoutUpdateErrors(t *testing.T) {
	c := newCheckerFixture(t, `[]`, config.Config{}, "v0.1.0")
	if err := c.ApplyNow(context.Background()); err == nil {
		t.Error("ApplyNow with nothing available must error")
	}
}

func TestDisabledCheckerStatus(t *testing.T) {
	client := NewClient()
	applier := &Applier{StatePath: filepath.Join(t.TempDir(), "absent.json"), Running: "dev", Log: testLogger()}
	c := NewChecker(client, applier, config.Config{}, false, testLogger())
	st := c.Status()
	if st.Enabled {
		t.Error("disabled checker reports Enabled")
	}
	if st.Running != "dev" {
		t.Errorf("Running = %q", st.Running)
	}
	// Run must return immediately for a disabled checker.
	done := make(chan struct{})
	go func() { c.Run(context.Background(), func() {}); close(done) }()
	<-done
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestCheckNow|TestApplyNow|TestDisabled' -v`
Expected: FAIL — undefined: `NewChecker`, `Checker`, `Status`

- [ ] **Step 3: Implement**

```go
// internal/update/checker.go
package update

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"golang.org/x/mod/semver"

	"github.com/mintopia/trainboard/internal/config"
)

// Checker cadences (spec §3): a full release-feed check every checkEvery;
// the loop wakes every tickEvery so an opt-in auto-apply can catch the
// update window without waiting for the next 6h check.
const (
	checkEvery   = 6 * time.Hour
	tickEvery    = 10 * time.Minute
	initialDelay = 2 * time.Minute // let connectivity settle after boot
)

// Status is the updater's render-ready snapshot for the web UI / JSON API.
type Status struct {
	// Enabled reports whether the updater is usable at all on this device
	// (slot install present + keyring non-empty). The web UI hides the
	// whole Software section's controls when false.
	Enabled bool   `json:"enabled"`
	Running string `json:"running"`
	// Available is the newer release's version ("" = none known).
	Available string    `json:"available,omitempty"`
	NotesURL  string    `json:"notesUrl,omitempty"`
	LastCheck time.Time `json:"lastCheck"`
	LastError string    `json:"lastError,omitempty"`
	// RolledBackFrom surfaces the launcher's rollback marker (spec §3),
	// read live from the state file.
	RolledBackFrom string `json:"rolledBackFrom,omitempty"`
}

// Checker periodically discovers releases and (opt-in) auto-applies them
// inside the unattended-update window.
type Checker struct {
	client  *Client
	applier *Applier
	cfg     config.Config
	enabled bool
	log     *slog.Logger

	mu        sync.Mutex
	available *Release
	lastCheck time.Time
	lastErr   string
}

// NewChecker wires the checker. enabled=false (no slot install, empty
// keyring, or recovery mode) makes Run a no-op and Status report
// Enabled=false, but the struct is always safe to call.
func NewChecker(client *Client, applier *Applier, cfg config.Config, enabled bool, log *slog.Logger) *Checker {
	return &Checker{client: client, applier: applier, cfg: cfg, enabled: enabled, log: log}
}

// Run is the periodic loop. It exits when ctx is cancelled, immediately if
// the checker is disabled or the operator disabled checks. restart is
// invoked only after a successful unattended apply (auto-apply on, inside
// the window) — production wires the same clean-exit used by config apply.
func (c *Checker) Run(ctx context.Context, restart func()) {
	if !c.enabled || c.cfg.Update.DisableChecks {
		return
	}
	// Jittered initial delay: don't stampede the API at whole-fleet boot,
	// and let STA connectivity come up first.
	first := time.NewTimer(initialDelay + time.Duration(rand.Int63n(int64(time.Minute)))) //nolint:gosec // jitter, not crypto
	defer first.Stop()
	select {
	case <-ctx.Done():
		return
	case <-first.C:
	}
	c.tick(ctx, restart)

	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.tick(ctx, restart)
		}
	}
}

// tick runs a feed check when one is due, then evaluates auto-apply.
func (c *Checker) tick(ctx context.Context, restart func()) {
	c.mu.Lock()
	due := time.Since(c.lastCheck) >= checkEvery
	c.mu.Unlock()
	if due {
		if err := c.CheckNow(ctx); err != nil {
			c.log.Warn("update check failed", "error", err.Error())
		}
	}

	c.mu.Lock()
	avail := c.available
	c.mu.Unlock()
	if avail == nil || !c.cfg.Update.AutoApply || !c.cfg.InUpdateWindow(time.Now()) {
		return
	}
	c.log.Info("auto-applying update", "version", avail.Version)
	if err := c.ApplyNow(ctx); err != nil {
		c.log.Error("auto-apply failed", "error", err.Error())
		return
	}
	restart()
}

// CheckNow queries the release feed once and records the outcome. An
// "available" release is one that is strictly newer than the running
// version — except a non-semver running version ("dev") which any valid
// release beats.
func (c *Checker) CheckNow(ctx context.Context) error {
	rel, err := c.client.LatestRelease(ctx, c.cfg.Update.EffectiveChannel())

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastCheck = time.Now()
	if err != nil {
		c.lastErr = err.Error()
		c.available = nil
		return err
	}
	c.lastErr = ""
	c.available = nil
	if rel == nil {
		return nil
	}
	running := c.applier.Running
	if !semver.IsValid(running) || semver.Compare(rel.Version, running) > 0 {
		c.available = rel
	}
	return nil
}

// ApplyNow applies the last-found release (checking first if none is
// cached). It does NOT restart the process: the web handler renders its
// response and schedules the restart itself, exactly like config apply;
// only Run's unattended path restarts directly.
func (c *Checker) ApplyNow(ctx context.Context) error {
	c.mu.Lock()
	rel := c.available
	c.mu.Unlock()
	if rel == nil {
		if err := c.CheckNow(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		rel = c.available
		c.mu.Unlock()
	}
	if rel == nil {
		return errors.New("update: no update available")
	}
	if err := c.applier.Apply(ctx, rel); err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
		return err
	}
	c.mu.Lock()
	c.available = nil
	c.lastErr = ""
	c.mu.Unlock()
	return nil
}

// Status assembles the render-ready snapshot. RolledBackFrom is read live
// from the state file on every call (cheap: one small file) so the web UI
// sees a rollback the moment the launcher records it.
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{
		Enabled:   c.enabled,
		Running:   c.applier.Running,
		LastCheck: c.lastCheck,
		LastError: c.lastErr,
	}
	if c.available != nil {
		st.Available = c.available.Version
		st.NotesURL = c.available.NotesURL
	}
	if s, err := LoadState(c.applier.StatePath); err == nil {
		st.RolledBackFrom = s.RolledBackFrom
	}
	return st
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/update/ -count=1
git add internal/update/checker.go internal/update/checker_test.go
git commit -m "feat(update): periodic checker, on-demand check/apply, status snapshot (#19)"
```

---

### Task 9: Launcher binary (`cmd/trainboard-launcher`)

The stable shim (spec §2): tolerant state read → `Decide` → persist BEFORE exec → `exec()` the slot. Exec failure of a non-known-good slot falls back to known-good directly. Never sits in an exit/restart loop of its own.

**Files:**
- Create: `cmd/trainboard-launcher/main.go`
- Test: `cmd/trainboard-launcher/main_test.go`

**Test-approach note (deviation from the spec's wording):** the spec suggests compiling tiny fake payload binaries. A real `exec()` replaces the test process, so an exec-and-crash cycle cannot be observed in-process anyway; instead the tests inject a fake `execFn` and simulate each "crashed boot" by running `launch` again (state on disk carries the story between boots). Same rollback story, fully hermetic, no compiled fixtures.

**Interfaces:**
- Consumes: `update.LoadStateOrDefault`, `update.Decide`, `update.SaveState`, `update.DefaultSlotsDir`, `update.DefaultStatePath` (Tasks 1–2).
- Produces: the frozen launcher contract — env vars `TRAINBOARD_SLOTS` / `TRAINBOARD_STATE` override paths; ALL argv passed through to the payload verbatim; `--recovery` appended on double fault. Task 16's unit file and migration depend on exactly this.

- [ ] **Step 1: Write the failing test**

```go
// cmd/trainboard-launcher/main_test.go
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mintopia/trainboard/internal/update"
)

// fakeExec records what would have been exec()d and optionally fails for
// specific binaries (simulating a missing/corrupt slot binary — a crash
// AFTER exec is simulated by just running the launcher again, since a real
// exec never returns).
type fakeExec struct {
	calls   []string   // binaries exec'd, in order
	argvs   [][]string // full argv per call
	failFor []string   // substrings of paths whose exec fails
}

func (f *fakeExec) exec(bin string, argv, _ []string) error {
	f.calls = append(f.calls, bin)
	f.argvs = append(f.argvs, argv)
	for _, s := range f.failFor {
		if strings.Contains(bin, s) {
			return errors.New("exec format error")
		}
	}
	return nil
}

// launcherEnv points the launcher at a temp slots/state layout.
func launcherEnv(t *testing.T, seed *update.State) (slots, statePath string) {
	t.Helper()
	dir := t.TempDir()
	slots = filepath.Join(dir, "slots")
	statePath = filepath.Join(dir, "state.json")
	if seed != nil {
		if err := update.SaveState(statePath, *seed); err != nil {
			t.Fatal(err)
		}
	}
	return slots, statePath
}

func TestLaunchActiveSlotAndBurnAttempt(t *testing.T) {
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	wantBin := filepath.Join(slots, "b", "trainboard")
	if len(fe.calls) != 1 || fe.calls[0] != wantBin {
		t.Fatalf("exec'd %v, want [%s]", fe.calls, wantBin)
	}
	// argv[0] = binary, then passthrough args, no --recovery.
	wantArgv := []string{wantBin, "--production"}
	if len(fe.argvs[0]) != 2 || fe.argvs[0][0] != wantArgv[0] || fe.argvs[0][1] != wantArgv[1] {
		t.Errorf("argv = %v, want %v", fe.argvs[0], wantArgv)
	}
	// Attempt burned BEFORE exec.
	st, err := update.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.BootAttempts != 1 {
		t.Errorf("BootAttempts = %d, want 1", st.BootAttempts)
	}
}

func TestFullRollbackStory(t *testing.T) {
	// A freshly-applied update in slot b that crashes on every boot: three
	// launches burn attempts 1..3 in slot b, the fourth flips back to a.
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	for boot := 1; boot <= 3; boot++ {
		fe := &fakeExec{}
		if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
			t.Fatalf("boot %d: %v", boot, err)
		}
		if want := filepath.Join(slots, "b", "trainboard"); fe.calls[0] != want {
			t.Fatalf("boot %d exec'd %s, want %s", boot, fe.calls[0], want)
		}
	}
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("boot 4: %v", err)
	}
	if want := filepath.Join(slots, "a", "trainboard"); fe.calls[0] != want {
		t.Fatalf("boot 4 exec'd %s, want rollback to %s", fe.calls[0], want)
	}
	st, _ := update.LoadState(statePath)
	if st.Active != "a" || st.RolledBackFrom != "v0.2.0" || st.BootAttempts != 1 {
		t.Errorf("state after rollback: %+v", st)
	}
	// No --recovery on a plain rollback.
	for _, a := range fe.argvs[0] {
		if a == "--recovery" {
			t.Error("rollback boot wrongly passed --recovery")
		}
	}
}

func TestDoubleFaultEntersRecovery(t *testing.T) {
	seed := update.State{Active: "a", ActiveVersion: "v0.1.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 3}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	argv := fe.argvs[0]
	if argv[len(argv)-1] != "--recovery" {
		t.Errorf("double fault argv = %v, want trailing --recovery", argv)
	}
	st, _ := update.LoadState(statePath)
	if st.BootAttempts != 0 {
		t.Errorf("BootAttempts = %d, want 0 (operator restart retries a normal boot)", st.BootAttempts)
	}
}

func TestExecFailureFallsBackToKnownGood(t *testing.T) {
	// Slot b's binary is missing/corrupt (exec itself fails): fall back to
	// known-good a in the SAME launch, no --recovery.
	seed := update.State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{failFor: []string{filepath.Join("slots", "b")}}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if len(fe.calls) != 2 || !strings.Contains(fe.calls[1], filepath.Join("a", "trainboard")) {
		t.Fatalf("calls = %v, want [b-bin, a-bin]", fe.calls)
	}
}

func TestExecFailureOfKnownGoodErrors(t *testing.T) {
	seed := update.State{Active: "a", KnownGood: "a"}
	slots, statePath := launcherEnv(t, &seed)
	fe := &fakeExec{failFor: []string{"trainboard"}}
	if err := launch(slots, statePath, []string{}, fe.exec); err == nil {
		t.Error("known-good exec failure must return an error (systemd Restart=always retries)")
	}
}

func TestMissingStateBootsSlotA(t *testing.T) {
	slots, statePath := launcherEnv(t, nil) // no state file at all
	fe := &fakeExec{}
	if err := launch(slots, statePath, []string{"--production"}, fe.exec); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if want := filepath.Join(slots, "a", "trainboard"); fe.calls[0] != want {
		t.Errorf("exec'd %s, want default slot a", fe.calls[0])
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file not created on first boot: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/trainboard-launcher/ -v`
Expected: FAIL — package does not exist / undefined: `launch`

- [ ] **Step 3: Implement**

```go
// Command trainboard-launcher is the stable boot shim (spec §2, issue #18):
// it reads the updater state, burns a boot attempt, selects a slot
// (rolling back or entering recovery per update.Decide), and exec()s the
// payload — process replacement, so systemd's WatchdogSec/NotifyAccess
// apply to the payload unchanged.
//
// This binary is deliberately tiny and is NEVER updated by A/B (deploy.md
// §Self-update). Its contract is frozen: TRAINBOARD_SLOTS /
// TRAINBOARD_STATE env overrides, argv passed through verbatim,
// --recovery appended on double fault.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mintopia/trainboard/internal/update"
)

func main() {
	slots := envOr("TRAINBOARD_SLOTS", update.DefaultSlotsDir)
	statePath := envOr("TRAINBOARD_STATE", update.DefaultStatePath)
	if err := launch(slots, statePath, os.Args[1:], realExec); err != nil {
		// Exec of every candidate failed. Exit nonzero and let systemd's
		// Restart=always retry — the state file has already burned this
		// attempt, so persistent failure still converges via Decide.
		fmt.Fprintln(os.Stderr, "trainboard-launcher:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// realExec replaces this process with the payload.
func realExec(bin string, argv, env []string) error {
	return syscall.Exec(bin, argv, env)
}

// launch is main's testable body. execFn never returns on success in
// production (process replacement); tests substitute a recorder.
func launch(slots, statePath string, passthrough []string, execFn func(bin string, argv, env []string) error) error {
	st := update.LoadStateOrDefault(statePath)
	dec := update.Decide(st)
	if dec.RolledBack {
		fmt.Fprintf(os.Stderr, "trainboard-launcher: rolling back to slot %s (%s failed %d boots)\n",
			dec.Slot, dec.State.RolledBackFrom, update.MaxBootAttempts)
	}
	if dec.Recovery {
		fmt.Fprintf(os.Stderr, "trainboard-launcher: double fault — entering recovery mode on slot %s\n", dec.Slot)
	}
	// Persist BEFORE exec: a payload that segfaults instantly must still
	// have burned its attempt. A failed write is a warning, not fatal —
	// booting something beats refusing to boot (spec §2).
	if err := update.SaveState(statePath, dec.State); err != nil {
		fmt.Fprintln(os.Stderr, "trainboard-launcher: warning: persisting state:", err)
	}

	args := append([]string{}, passthrough...)
	if dec.Recovery {
		args = append(args, "--recovery")
	}
	bin := filepath.Join(slots, dec.Slot, "trainboard")
	err := execFn(bin, append([]string{bin}, args...), os.Environ())
	if err == nil {
		return nil // tests only; a real exec never returns on success
	}
	// Exec itself failed (missing/corrupt binary — e.g. an interrupted
	// install). If we weren't already on known-good, fall straight back to
	// it now rather than waiting three boots.
	if dec.Slot != dec.State.KnownGood {
		fb := filepath.Join(slots, dec.State.KnownGood, "trainboard")
		fmt.Fprintf(os.Stderr, "trainboard-launcher: exec %s: %v; falling back to %s\n", bin, err, fb)
		if err2 := execFn(fb, append([]string{fb}, passthrough...), os.Environ()); err2 != nil {
			return fmt.Errorf("exec %s: %v; fallback exec %s: %v", bin, err, fb, err2)
		}
		return nil
	}
	return fmt.Errorf("exec %s: %w", bin, err)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/trainboard-launcher/ -v`
Expected: PASS — `TestFullRollbackStory` is the one that matters most.

- [ ] **Step 5: Verify it cross-compiles for the Pi**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /dev/null ./cmd/trainboard-launcher`
Expected: exit 0

- [ ] **Step 6: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add cmd/trainboard-launcher/
git commit -m "feat(launcher): stable boot shim — attempt counter, rollback, recovery exec (#18)"
```

---

### Task 10: E07 fault code + recovery boot path (`internal/obs`, `cmd/trainboard`)

`--recovery` renders a dedicated fault scene and keeps ONLY the web server (+ connectivity manager + mDNS) alive — no poller, no periodic checker. Reuses the E04 error-loop machinery by generalising it.

**Files:**
- Modify: `internal/obs/faults.go` (add E07)
- Modify: `internal/obs/faults_test.go` (extend the message table test)
- Modify: `cmd/trainboard/main.go` (add `--recovery` flag; generalise `runConfigErrorLoop` → `runFaultLoop`)
- Test: `cmd/trainboard/main_test.go` (extend if it covers flag parsing; otherwise obs test only — the loop refactor is covered by existing E04 tests staying green)

**Interfaces:**
- Consumes: existing `runConfigErrorLoop` machinery in `cmd/trainboard/main.go:274`.
- Produces:
  - `obs.FaultUpdateRecovery FaultCode = "E07"`, message `"Update recovery mode"`.
  - `runFaultLoop(ctx, fl, fonts, log, path, httpAddr, ring, previewLatest, startedAt, soak, wd, manageNetwork, mdnsEnabled bool, fault obs.FaultCode, cause error) error` — the generalised loop; `runConfigErrorLoop` becomes a thin wrapper passing `obs.FaultConfigError`, and the recovery path calls it with `obs.FaultUpdateRecovery`.
  - Task 14 wires the actual `--recovery` branch (it owns main's updater wiring); THIS task only adds the fault code, the flag, and the generalised loop so main compiles with `--recovery` falling into `runFaultLoop`.

- [ ] **Step 1: Write the failing test**

Add to `internal/obs/faults_test.go` (append a case to the existing message-table test; if it's a map/table, add the entry — shown here as a standalone test to be safe):

```go
func TestFaultUpdateRecovery(t *testing.T) {
	if FaultUpdateRecovery != "E07" {
		t.Errorf("FaultUpdateRecovery = %q, want E07", FaultUpdateRecovery)
	}
	if got := FaultUpdateRecovery.Message(); got != "Update recovery mode" {
		t.Errorf("Message() = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/obs/ -run TestFaultUpdateRecovery -v`
Expected: FAIL — undefined: `FaultUpdateRecovery`

- [ ] **Step 3: Implement the fault code**

In `internal/obs/faults.go`, extend the const block and `Message`:

```go
	// FaultUpdateRecovery: the launcher hit a double fault (known-good slot
	// itself failing) and exec'd the payload in --recovery mode — web UI +
	// AP only, no departures (M5 spec §2, issue #18).
	FaultUpdateRecovery FaultCode = "E07"
```

and in `Message()`:

```go
	case FaultUpdateRecovery:
		return "Update recovery mode"
```

- [ ] **Step 4: Generalise the error loop and add the flag**

In `cmd/trainboard/main.go`:

1. Add the flag next to the others in `run()`:

```go
	recovery := flag.Bool("recovery", false, "recovery mode: web UI + AP only (set by the launcher on double fault)")
```

2. Immediately after the config-load block's success path decision point — i.e. right after `fl`/`previewLatest` are set up and BEFORE `loadConfig` — branch:

```go
	if *recovery {
		// Double-fault recovery (launcher appended --recovery): the most
		// conservative useful posture. No poller, no periodic update
		// checks — just the fault scene, the web UI (config fixes +
		// manual update apply, wired in the M5 main-wiring task), the AP
		// fallback, and mDNS.
		return runFaultLoop(ctx, fl, fonts, log, *cfgPath, *httpAddr, ring, previewLatest,
			startedAt, soak, wd, *manageNetwork, *mdnsEnabled,
			obs.FaultUpdateRecovery, errors.New("launcher double fault"))
	}
```

(add `"errors"` to imports)

3. Rename `runConfigErrorLoop` to `runFaultLoop`, parameterising the fault:

- Change the signature's trailing `err error` to `fault obs.FaultCode, cause error`.
- Change `log.Error("config error", ...)` to `log.Error("fault loop", "fault", string(fault), "err", cause.Error(), "path", path)`.
- Change `snap := &board.Snapshot{State: board.StateError, Fault: obs.FaultConfigError}` to use `fault`.
- Keep EVERYTHING else identical (connectivity, mDNS, web server, render loop).
- Re-create `runConfigErrorLoop` as a thin wrapper so the existing call site and its doc comment stay meaningful:

```go
// runConfigErrorLoop renders the E04 fault scene and idles; see
// runFaultLoop for the shared machinery (M5 generalised it so --recovery
// can render E07 through the identical path).
func runConfigErrorLoop(ctx context.Context, fl runtime.Flusher, fonts *board.Fonts, log *slog.Logger, path, httpAddr string, ring *obs.Ring, previewLatest func() []byte, startedAt time.Time, soak *runtime.Soak, wd *obs.Watchdog, manageNetwork, mdnsEnabled bool, err error) error {
	return runFaultLoop(ctx, fl, fonts, log, path, httpAddr, ring, previewLatest, startedAt, soak, wd, manageNetwork, mdnsEnabled, obs.FaultConfigError, err)
}
```

Move `runConfigErrorLoop`'s existing long doc comment onto `runFaultLoop` (it describes the machinery, which now lives there).

- [ ] **Step 5: Run tests to verify everything passes**

Run: `go test ./internal/obs/ ./cmd/trainboard/ -v`
Expected: PASS — new E07 test AND all existing main/E04 tests (the refactor must not change E04 behaviour).

- [ ] **Step 6: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add internal/obs/ cmd/trainboard/main.go
git commit -m "feat(obs,runtime): E07 update-recovery fault + generalised fault boot loop (#18)"
```

---

### Task 11: On-screen update hint (`internal/runtime/loop.go`)

The "subtle on-screen hint" (#19): a dim 2×2 pixel dot in the bottom-left corner, drawn as a loop-level overlay after the scene renders. Bottom-left is empty in every scene (the clock is centred, rows are left-aligned text starting above it) and the overlay approach means no `BuildScene` signature change, no snapshot field, and no cache interaction — it can't defeat ADR 0002's scene caching because it touches the framebuffer, not the scene. Soak frames deliberately skip it (any static element during soak would defeat the burn-in treatment — see `soakStep`).

**Files:**
- Modify: `internal/runtime/loop.go`
- Test: `internal/runtime/loop_test.go` (append; reuse the file's existing fake flusher/snapshot helpers — read the file first and follow its established test setup pattern)

**Interfaces:**
- Consumes: `Loop.step` internals (`l.fb`, `l.fl`).
- Produces: `func (l *Loop) SetUpdateHint(f func() bool)` — nil (default) disables the feature. Task 14 wires it to the checker.

- [ ] **Step 1: Write the failing test**

Append to `internal/runtime/loop_test.go` (adapt constructor/fake names to the file's existing helpers — it already builds Loops with fake Flushers and snapshot sources; follow that pattern exactly):

```go
func TestUpdateHintOverlay(t *testing.T) {
	// Build a loop exactly as the file's other tests do (fake flusher, any
	// snapshot source, config.Default(), test fonts), then:
	fonts := mustFonts(t) // or however existing tests obtain *board.Fonts
	fl := &fakeFlusher{}  // the file's existing fake
	snap := &board.Snapshot{State: board.StateInitialising}
	l := NewLoop(func() *board.Snapshot { return snap }, fl, config.Default(), fonts, "vtest", testLog())

	hint := false
	l.SetUpdateHint(func() bool { return hint })

	// One frame without the hint: bottom-left pixels stay 0.
	if err := l.step(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := l.fb.At(0, l.fb.H-1); got != 0 {
		t.Fatalf("pixel (0,H-1) = %d before hint, want 0", got)
	}

	// Enable the hint: the 2x2 bottom-left block lights at level 6.
	hint = true
	if err := l.step(time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, p := range [][2]int{{0, l.fb.H - 1}, {1, l.fb.H - 1}, {0, l.fb.H - 2}, {1, l.fb.H - 2}} {
		if got := l.fb.At(p[0], p[1]); got != updateHintLevel {
			t.Errorf("pixel (%d,%d) = %d with hint, want %d", p[0], p[1], got, updateHintLevel)
		}
	}
}
```

If the existing file has no `mustFonts`/`testLog` helpers under those names, use whatever it actually uses — the assertions above are the contract; the setup mirrors neighbours.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -run TestUpdateHintOverlay -v`
Expected: FAIL — undefined: `SetUpdateHint`, `updateHintLevel`

- [ ] **Step 3: Implement**

In `internal/runtime/loop.go`:

1. Add to the `Loop` struct fields: `updateHint func() bool`.
2. Add the setter beside `SetBeat`:

```go
// SetUpdateHint installs the "update available" probe. When it reports
// true, step overlays a dim 2x2 dot in the bottom-left corner after the
// scene renders — the subtle on-screen hint from the M5 spec (#19). It is
// an overlay on the framebuffer, not a scene element, so it cannot
// interact with scene caching (ADR 0002); soak frames never draw it. nil
// (the default) disables the feature.
func (l *Loop) SetUpdateHint(f func() bool) { l.updateHint = f }
```

3. Add the level constant near `TickInterval`:

```go
// updateHintLevel is the greyscale level (0-15) of the update-available
// dot: visible if you look, invisible if you don't.
const updateHintLevel = 6
```

4. In `step`, between `l.scene.Render(l.fb, l.tick, now)` and `packed := l.fb.Pack()`:

```go
	if l.updateHint != nil && l.updateHint() {
		drawUpdateHint(l.fb)
	}
```

5. Add the draw helper at the bottom of the file:

```go
// drawUpdateHint lights the 2x2 bottom-left block. Bottom-left is unused
// by every scene (the clock is centred, text rows sit above it), so the
// dot never collides with content.
func drawUpdateHint(fb *render.Framebuffer) {
	for _, p := range [][2]int{{0, fb.H - 1}, {1, fb.H - 1}, {0, fb.H - 2}, {1, fb.H - 2}} {
		fb.SetPixel(p[0], p[1], updateHintLevel)
	}
}
```

(If `render.Framebuffer`'s setter is named differently — check `internal/render` — use its actual name; `internal/board/elements.go:copyRect` calls `dst.SetPixel(tx, ty, ...)`, so `SetPixel` is correct.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runtime/ -v`
Expected: PASS — all existing loop/soak tests stay green.

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./internal/runtime/ -count=1
git add internal/runtime/loop.go internal/runtime/loop_test.go
git commit -m "feat(render): subtle update-available hint dot as loop overlay (#19)"
```

---

### Task 12: Health check → known-good promotion (`internal/runtime/loop.go` hook + `internal/update/health.go`)

The payload promotes its slot once (a) the render loop has flushed its first frame AND (b) the web server answers a self-probe — both within 60s of start (spec §2).

**Files:**
- Modify: `internal/runtime/loop.go` (first-frame hook)
- Create: `internal/update/health.go`
- Test: `internal/runtime/loop_test.go` (hook), `internal/update/health_test.go`

**Interfaces:**
- Consumes: `Promote` (Task 2), `Loop.step`'s existing `l.flushed` first-frame branch.
- Produces:
  - `func (l *Loop) SetOnFirstFrame(f func())` — invoked exactly once, on the first successful flush.
  - `type Health struct { FirstFrame <-chan struct{}; Probe func(ctx context.Context) error; Deadline time.Duration; StatePath, Version string; Log *slog.Logger }`
  - `func (h Health) Run(ctx context.Context)` — waits, probes (2s interval), promotes; gives up silently at Deadline (the launcher's counter then does its job).

- [ ] **Step 1: Write the failing tests**

Append to `internal/runtime/loop_test.go`:

```go
func TestOnFirstFrameFiresOnce(t *testing.T) {
	// Same setup pattern as TestUpdateHintOverlay.
	fonts := mustFonts(t)
	fl := &fakeFlusher{}
	snap := &board.Snapshot{State: board.StateInitialising}
	l := NewLoop(func() *board.Snapshot { return snap }, fl, config.Default(), fonts, "vtest", testLog())

	fired := 0
	l.SetOnFirstFrame(func() { fired++ })
	for i := 0; i < 3; i++ {
		if err := l.step(time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if fired != 1 {
		t.Errorf("OnFirstFrame fired %d times, want exactly 1", fired)
	}
}
```

Create `internal/update/health_test.go`:

```go
// internal/update/health_test.go
package update

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func healthState(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	seed := State{Active: "b", ActiveVersion: "v0.2.0", KnownGood: "a", KnownGoodVersion: "v0.1.0", BootAttempts: 1}
	if err := SaveState(path, seed); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHealthPromotesWhenBothSignalsArrive(t *testing.T) {
	path := healthState(t)
	ff := make(chan struct{})
	close(ff) // first frame already flushed
	h := Health{
		FirstFrame: ff,
		Probe:      func(ctx context.Context) error { return nil },
		Deadline:   2 * time.Second,
		StatePath:  path, Version: "v0.2.0", Log: testLogger(),
	}
	h.Run(context.Background())
	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.KnownGood != "b" || st.KnownGoodVersion != "v0.2.0" || st.BootAttempts != 0 {
		t.Errorf("not promoted: %+v", st)
	}
}

func TestHealthGivesUpAtDeadline(t *testing.T) {
	path := healthState(t)
	h := Health{
		FirstFrame: make(chan struct{}), // never fires
		Probe:      func(ctx context.Context) error { return errors.New("down") },
		Deadline:   150 * time.Millisecond,
		StatePath:  path, Version: "v0.2.0", Log: testLogger(),
	}
	done := make(chan struct{})
	go func() { h.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not give up at deadline")
	}
	st, _ := LoadState(path)
	if st.KnownGood != "a" || st.BootAttempts != 1 {
		t.Errorf("promoted despite failed health check: %+v", st)
	}
}

func TestHealthRetriesProbe(t *testing.T) {
	path := healthState(t)
	ff := make(chan struct{})
	close(ff)
	calls := 0
	h := Health{
		FirstFrame: ff,
		Probe: func(ctx context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not yet")
			}
			return nil
		},
		Deadline:  5 * time.Second,
		StatePath: path, Version: "v0.2.0", Log: testLogger(),
	}
	h.probeEvery = 10 * time.Millisecond // test seam; defaults to 2s
	h.Run(context.Background())
	st, _ := LoadState(path)
	if st.KnownGood != "b" {
		t.Errorf("not promoted after probe retries: %+v", st)
	}
	if calls < 3 {
		t.Errorf("probe called %d times", calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runtime/ -run TestOnFirstFrame -v && go test ./internal/update/ -run TestHealth -v`
Expected: FAIL — undefined: `SetOnFirstFrame`, `Health`

- [ ] **Step 3: Implement the loop hook**

In `internal/runtime/loop.go`: add struct field `onFirstFrame func()`, setter:

```go
// SetOnFirstFrame installs a callback invoked exactly once, after the
// first successful flush — the render half of M5's health check (the
// other half is the web self-probe; update.Health joins them). nil (the
// default) disables the hook.
func (l *Loop) SetOnFirstFrame(f func()) { l.onFirstFrame = f }
```

and in `step`'s existing first-flush branch:

```go
	if !l.flushed {
		l.flushed = true
		l.log.Info("first frame flushed", "render_us", renderDur.Microseconds(), "flush_us", flushDur.Microseconds())
		if l.onFirstFrame != nil {
			l.onFirstFrame()
		}
	}
```

- [ ] **Step 4: Implement Health**

```go
// internal/update/health.go
package update

import (
	"context"
	"log/slog"
	"time"
)

// Health joins the two post-start health signals (spec §2) and promotes
// the running slot to known-good when both arrive within Deadline:
//
//   - FirstFrame: closed by the render loop's SetOnFirstFrame hook;
//   - Probe: a loopback HTTP request to the embedded web server.
//
// The health check runs INSIDE the payload — the launcher never judges
// health (#18); it only counts boots. If the deadline passes, Run returns
// without promoting and the launcher's attempt counter converges on a
// rollback.
type Health struct {
	FirstFrame <-chan struct{}
	Probe      func(ctx context.Context) error
	Deadline   time.Duration
	StatePath  string
	Version    string
	Log        *slog.Logger

	// probeEvery overrides the probe retry interval (tests); 0 = 2s.
	probeEvery time.Duration
}

// Run blocks until promotion, deadline, or ctx cancellation. Run it in a
// goroutine.
func (h Health) Run(ctx context.Context) {
	deadline := time.NewTimer(h.Deadline)
	defer deadline.Stop()

	select {
	case <-ctx.Done():
		return
	case <-deadline.C:
		h.Log.Warn("health check: no first frame before deadline; not promoting")
		return
	case <-h.FirstFrame:
	}

	every := h.probeEvery
	if every == 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := h.Probe(ctx); err == nil {
			if err := Promote(h.StatePath, h.Version); err != nil {
				h.Log.Error("health check: promote failed", "error", err.Error())
				return
			}
			h.Log.Info("health check passed: slot promoted to known-good", "version", h.Version)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			h.Log.Warn("health check: web probe never succeeded before deadline; not promoting")
			return
		case <-t.C:
		}
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/runtime/ ./internal/update/ -v`
Expected: PASS

- [ ] **Step 6: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add internal/runtime/loop.go internal/runtime/loop_test.go internal/update/health.go internal/update/health_test.go
git commit -m "feat(update,render): post-start health check promotes slot to known-good (#18)"
```

---

### Task 13: Web UI — Software section, update actions, JSON API (`internal/web`)

Status page gains a "Software" section (running/available versions, notes link, check/apply/dismiss forms, rollback banner, last-error line); `/api/status` gains the update status; three new POST actions on both surfaces. All behind the existing auth/CSRF/rate-limit middleware, mirroring `/actions/restart` exactly.

**Files:**
- Modify: `internal/web/service.go` (Sources/Actions seams + Service methods + StatusData)
- Modify: `internal/web/handlers_actions.go` (three new handlers)
- Modify: `internal/web/server.go` (routes)
- Modify: `internal/web/handlers_api.go` (API mirrors — READ THE FILE FIRST and copy its exact response/error conventions)
- Modify: `internal/web/templates/status.html` (Software section)
- Modify: `internal/web/templates/actions.html` (remove the disabled placeholder "Update firmware" button — the real controls live on the status page)
- Modify: `internal/web/handlers_config.go` + `templates/config.html` + `service.go:UpdateConfig` (Update fieldset on the config form)
- Test: `internal/web/handlers_update_test.go` (new)

**Naming hazard:** `handlers_config.go:85` has a local variable `update := ConfigUpdate{...}` that would shadow the imported `update` package. Rename that local to `upd` as part of this task.

**Interfaces:**
- Consumes: `update.Status` (Task 8), `config.UpdateConfig` (Task 5).
- Produces (main wires these in Task 14):
  - `web.Sources.UpdateStatus func() update.Status` — nil ⇒ zero `update.Status` (Enabled=false hides the controls).
  - `web.Actions.UpdateCheck func(ctx context.Context) error`
  - `web.Actions.UpdateApply func(ctx context.Context) error` — applies WITHOUT restarting; handlers schedule the restart.
  - `web.Actions.UpdateDismiss func() error`
  - Routes: `POST /actions/update/check`, `POST /actions/update/apply`, `POST /actions/update/dismiss`, `POST /api/actions/update/check`, `POST /api/actions/update/apply`.
  - `Service.UpdateStatus() update.Status`, `Service.CheckForUpdate(ctx) error`, `Service.ApplyUpdate(ctx) error`, `Service.DismissRollback() error` — all nil-safe (nil action ⇒ "updates are not available on this device" error).
  - `StatusData.Update update.Status` field.

- [ ] **Step 1: Write the failing test**

Model the fixture on the existing `handlers_actions_test.go` (read it first; it already builds an authed test server — reuse its helpers for session/CSRF):

```go
// internal/web/handlers_update_test.go
package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/update"
)

// newUpdateTestServer builds an authed server whose update seams record
// calls. Reuse the file-local helpers the actions tests use for login/CSRF
// (adapt names to what handlers_actions_test.go actually provides).
func newUpdateTestServer(t *testing.T, st update.Status, applyErr error) (*Server, *struct{ checks, applies, dismisses int }) {
	t.Helper()
	calls := &struct{ checks, applies, dismisses int }{}
	src := Sources{
		Snapshot:     func() *board.Snapshot { return nil },
		Ring:         obs.NewRing(obs.DefaultRingCapacity),
		Version:      "v0.1.0",
		StartedAt:    time.Now(),
		UpdateStatus: func() update.Status { return st },
	}
	act := Actions{
		Apply:         func() {},
		UpdateCheck:   func(ctx context.Context) error { calls.checks++; return nil },
		UpdateApply:   func(ctx context.Context) error { calls.applies++; return applyErr },
		UpdateDismiss: func() error { calls.dismisses++; return nil },
	}
	// ...build Service/Server + authed session exactly as the actions
	// tests do...
	_ = src
	_ = act
	panic("adapt to the existing test fixture pattern before running")
}

func TestStatusPageShowsSoftwareSection(t *testing.T) {
	st := update.Status{Enabled: true, Running: "v0.1.0", Available: "v0.2.0",
		NotesURL: "https://github.com/mintopia/trainboard/releases/tag/v0.2.0",
		RolledBackFrom: "v0.1.9", LastError: "boom"}
	// GET / (authed) must contain: running version, available version,
	// notes link, rollback banner, last error, and the three forms.
	// Assert on body substrings:
	//   "v0.2.0", "rolled back from v0.1.9", "boom",
	//   `action="/actions/update/check"`, `action="/actions/update/apply"`,
	//   `action="/actions/update/dismiss"`.
	_ = st
}

func TestStatusPageHidesControlsWhenDisabled(t *testing.T) {
	// Zero update.Status (Enabled=false): body must NOT contain
	// "/actions/update/apply".
}

func TestUpdateCheckActionCallsSeamAndRedirects(t *testing.T) {
	// POST /actions/update/check with CSRF → 302 to "/", calls.checks==1.
}

func TestUpdateApplySuccessRendersAppliedAndSchedulesRestart(t *testing.T) {
	// POST /actions/update/apply → 200 containing the applied/restart copy,
	// calls.applies==1. (Restart scheduling is scheduleApply; assert the
	// same way the config-save test asserts it, or skip the timing assert.)
}

func TestUpdateApplyFailureRedirectsWithoutRestart(t *testing.T) {
	// applyErr != nil: POST /actions/update/apply → 302 to "/" (the status
	// page shows Status.LastError), Actions.Apply never scheduled.
}

func TestUpdateDismissActionRedirects(t *testing.T) {
	// POST /actions/update/dismiss → 302 "/", calls.dismisses==1.
}

func TestAPIStatusIncludesUpdate(t *testing.T) {
	// GET /api/status → JSON containing "update":{"enabled":true,...}.
}

func TestAPIUpdateActions(t *testing.T) {
	// POST /api/actions/update/check and /apply → the API's standard
	// success shape; failure → its standard {"error": ...} shape.
}

func TestUpdateActionsNilSafe(t *testing.T) {
	// Server built with zero Actions (no update seams): POST
	// /actions/update/apply must not panic; expect a graceful error
	// (redirect or 4xx/5xx per Service's "not available" error).
}
```

Flesh each stub out against the real fixture helpers in `handlers_actions_test.go` — the assertions in the comments are the contract; the plumbing must match the house style. Every test must fail before Step 3 (compile failure counts for the seam tests; behavioural tests must fail behaviourally once the fixture compiles).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'TestStatusPage|TestUpdate|TestAPIStatus|TestAPIUpdate' -v`
Expected: FAIL — undefined: `Sources.UpdateStatus`, `Actions.UpdateCheck`, etc.

- [ ] **Step 3: Implement the Service layer**

In `internal/web/service.go`:

1. Add to `Sources`:

```go
	// UpdateStatus reports the M5 updater's state (available release,
	// rollback marker, last error). nil = updater not wired (dev mode);
	// reads as the zero Status, whose Enabled=false hides the controls.
	UpdateStatus func() update.Status
```

2. Add to `Actions`:

```go
	// UpdateCheck / UpdateApply / UpdateDismiss drive M5 self-update.
	// UpdateApply stages the update WITHOUT restarting — the handler
	// renders its response then schedules the restart via Actions.Apply,
	// the same shape as config save. All three are nil when the updater
	// is not wired.
	UpdateCheck   func(ctx context.Context) error
	UpdateApply   func(ctx context.Context) error
	UpdateDismiss func() error
```

3. Add to `StatusData`: `Update update.Status` and populate in `Status()`: `st.Update = s.UpdateStatus()`.

4. Add Service methods:

```go
// UpdateStatus reports the updater's render-ready state. Nil-safe: an
// unwired seam reads as the zero Status (Enabled=false).
func (s *Service) UpdateStatus() update.Status {
	if s.src.UpdateStatus == nil {
		return update.Status{}
	}
	return s.src.UpdateStatus()
}

// CheckForUpdate runs an on-demand release check. Nil-safe.
func (s *Service) CheckForUpdate(ctx context.Context) error {
	if s.act.UpdateCheck == nil {
		return errors.New("updates are not available on this device")
	}
	return s.act.UpdateCheck(ctx)
}

// ApplyUpdate stages the available update into the inactive slot; the
// caller schedules the restart. Nil-safe.
func (s *Service) ApplyUpdate(ctx context.Context) error {
	if s.act.UpdateApply == nil {
		return errors.New("updates are not available on this device")
	}
	return s.act.UpdateApply(ctx)
}

// DismissRollback clears the rollback banner. Nil-safe.
func (s *Service) DismissRollback() error {
	if s.act.UpdateDismiss == nil {
		return nil
	}
	return s.act.UpdateDismiss()
}
```

(add imports `context`, `github.com/mintopia/trainboard/internal/update`)

- [ ] **Step 4: Implement handlers + routes**

In `internal/web/handlers_actions.go` (rename the `update :=` local in `handlers_config.go` to `upd` first):

```go
// handleUpdateCheck runs an on-demand release check and returns to the
// status page (PRG); the outcome lands in Status.Update.LastError /
// .Available, which the page renders.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.CheckForUpdate(r.Context()); err != nil {
		s.log.Warn("update check failed", "error", err.Error())
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleUpdateApply stages the available update, then — success only —
// renders the applied page and schedules the same clean-exit restart as
// config save (the launcher boots the new slot). Failure redirects to the
// status page, whose Software section shows Status.LastError; the current
// binary keeps running (update failures are non-fatal by design).
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.ApplyUpdate(r.Context()); err != nil {
		s.log.Error("update apply failed", "error", err.Error())
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	s.render(w, "applied", basePage{LoggedIn: true, CSRF: csrfFrom(r)})
	s.scheduleApply()
}

// handleUpdateDismiss clears the rollback banner (PRG).
func (s *Server) handleUpdateDismiss(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DismissRollback(); err != nil {
		s.log.Warn("rollback dismiss failed", "error", err.Error())
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
```

In `internal/web/server.go`, next to the other `/actions/*` routes:

```go
	s.mux.Handle("POST /actions/update/check", chain(http.HandlerFunc(s.handleUpdateCheck),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/update/apply", chain(http.HandlerFunc(s.handleUpdateApply),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
	s.mux.Handle("POST /actions/update/dismiss", chain(http.HandlerFunc(s.handleUpdateDismiss),
		rateLimit(s.actionLimit, log), requireAuth(s.sessions, false), csrfProtect(log)))
```

and the API mirrors next to the other `/api/actions/*` routes (same middleware chain shape with `apiJSONErrors`):

```go
	s.mux.Handle("POST /api/actions/update/check", chain(http.HandlerFunc(s.handleAPIUpdateCheck),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
	s.mux.Handle("POST /api/actions/update/apply", chain(http.HandlerFunc(s.handleAPIUpdateApply),
		apiJSONErrors, rateLimit(s.actionLimit, log), requireAuth(s.sessions, true), csrfProtect(log)))
```

In `internal/web/handlers_api.go`: read the file, then (a) add `Update update.Status` (JSON key `update`) to the API status payload struct alongside its existing fields, populated from `StatusData.Update`; (b) add `handleAPIUpdateCheck` / `handleAPIUpdateApply` following the exact request/response conventions of `handleAPIActionsRestart` (success shape, error shape, restart scheduling for apply's success path mirroring the HTML handler: apply → on success schedule restart and return the API's standard success body).

- [ ] **Step 5: Templates**

In `internal/web/templates/status.html`, after the `<dl class="facts">` block:

```html
<h3>Software</h3>
{{with .Status.Update}}
{{if .RolledBackFrom}}
<p class="error">Rolled back from {{.RolledBackFrom}} after repeated failed boots — running {{.Running}} instead.
  <form method="post" action="/actions/update/dismiss" style="display:inline">
    <input type="hidden" name="csrf" value="{{$.CSRF}}"><button type="submit">Dismiss</button></form></p>
{{end}}
<dl class="facts">
  <dt>Running</dt><dd>{{.Running}}</dd>
  {{if .Available}}<dt>Available</dt><dd>{{.Available}}{{if .NotesURL}} — <a href="{{.NotesURL}}" target="_blank" rel="noopener">release notes</a>{{end}}</dd>{{end}}
  {{if not .LastCheck.IsZero}}<dt>Last check</dt><dd>{{.LastCheck.Format "15:04:05"}}</dd>{{end}}
  {{if .LastError}}<dt>Update error</dt><dd class="error">{{.LastError}}</dd>{{end}}
</dl>
{{if .Enabled}}
<form method="post" action="/actions/update/check"><input type="hidden" name="csrf" value="{{$.CSRF}}">
  <button type="submit">Check for updates now</button></form>
{{if .Available}}
<form method="post" action="/actions/update/apply" onsubmit="return confirm('Download and install {{.Available}}? The board restarts into the new version.')">
  <input type="hidden" name="csrf" value="{{$.CSRF}}">
  <button type="submit" class="primary">Install {{.Available}}</button></form>
{{end}}
{{else}}
<p>Self-update is not available on this device (not a slot install, or no trusted keys).</p>
{{end}}
{{end}}
```

In `internal/web/templates/actions.html`, delete the line:

```html
<form><button type="button" disabled title="coming in a later release">Update firmware</button></form>
```

In `internal/web/templates/config.html`, add before the Admin fieldset:

```html
<fieldset><legend>Software updates</legend>
  <label>Channel
    <select name="update.channel">
      <option value="stable" {{if ne .Cfg.Update.Channel "prerelease"}}selected{{end}}>stable</option>
      <option value="prerelease" {{if eq .Cfg.Update.Channel "prerelease"}}selected{{end}}>prerelease</option>
    </select>
  </label>
  <label><input type="checkbox" name="update.autoApply" {{if .Cfg.Update.AutoApply}}checked{{end}}> Install updates automatically (overnight / powersave window)</label>
  <label><input type="checkbox" name="update.checks" {{if not .Cfg.Update.DisableChecks}}checked{{end}}> Check for updates periodically</label>
</fieldset>
```

In `internal/web/handlers_config.go:parseConfigForm`, after the `cfg.Wifi.SSID` line:

```go
	cfg.Update.Channel = r.PostFormValue("update.channel")
	cfg.Update.AutoApply = formHasKey(r, "update.autoApply")
	cfg.Update.DisableChecks = !formHasKey(r, "update.checks") // checkbox is "checks ON"; storage is inverted (see config.UpdateConfig)
```

In `internal/web/service.go:UpdateConfig`, after `next.Wifi.SSID = u.Cfg.Wifi.SSID`:

```go
	next.Update = u.Cfg.Update
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS — new tests AND the whole existing web suite (e2e, config, actions, api).

- [ ] **Step 7: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add internal/web/
git commit -m "feat(web): Software section — update status, check/apply/dismiss, config fields, API (#19)"
```

---

### Task 14: Main wiring (`cmd/trainboard`)

Everything converges: build the updater seams when the device is a slot install, start the checker + health promotion in the normal boot path, wire the web seams in BOTH boot paths (normal and fault loop — recovery must be able to manually apply), pass the update hint to the render loop.

**Files:**
- Create: `cmd/trainboard/update.go`
- Modify: `cmd/trainboard/main.go` (flags, wiring in `run()` and `runFaultLoop`)
- Test: `cmd/trainboard/update_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1–13.
- Produces: `buildUpdater(cfg config.Config, cfgValid bool, slotsDir, statePath string, log *slog.Logger) *updaterSeams` where

```go
// updaterSeams bundles what main wires into web/runtime/checker.
type updaterSeams struct {
	checker  *update.Checker
	enabled  bool
	statePath string
}
```

- [ ] **Step 1: Write the failing test**

```go
// cmd/trainboard/update_test.go
package main

import (
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/update"
)

func TestBuildUpdaterDisabledWithoutStateFile(t *testing.T) {
	// Dev machine / pre-migration Pi: no state file ⇒ updater disabled but
	// non-nil (web shows "not available", nothing crashes).
	u := buildUpdater(config.Default(), true, t.TempDir(), filepath.Join(t.TempDir(), "absent.json"), testLogSink(t))
	if u == nil || u.enabled {
		t.Fatalf("updater = %+v, want non-nil disabled", u)
	}
	if st := u.checker.Status(); st.Enabled {
		t.Error("disabled updater reports Enabled status")
	}
}

func TestBuildUpdaterDisabledWithEmptyKeyring(t *testing.T) {
	// Slot install present but the key ceremony hasn't run: disabled.
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := update.SaveState(statePath, update.DefaultState()); err != nil {
		t.Fatal(err)
	}
	u := buildUpdater(config.Default(), true, t.TempDir(), statePath, testLogSink(t))
	// With embeddedKeys still empty this must be disabled; once the key
	// ceremony fills the keyring this branch flips — assert consistency
	// with update.Keyring() rather than a fixed expectation.
	if _, err := update.Keyring(); err != nil {
		if u.enabled {
			t.Error("updater enabled despite empty keyring")
		}
	} else if !u.enabled {
		t.Error("updater disabled despite state file + keyring")
	}
}

func TestProbeURLFromHTTPAddr(t *testing.T) {
	tests := []struct{ addr, want string }{
		{":80", "http://127.0.0.1:80/login"},
		{":8080", "http://127.0.0.1:8080/login"},
		{"0.0.0.0:8080", "http://127.0.0.1:8080/login"},
		{"192.168.0.5:80", "http://192.168.0.5:80/login"},
	}
	for _, tt := range tests {
		if got := probeURL(tt.addr); got != tt.want {
			t.Errorf("probeURL(%q) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}
```

Add `testLogSink` if `cmd/trainboard`'s tests don't already have a discard-logger helper (check `main_test.go` first; reuse if present):

```go
func testLogSink(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/trainboard/ -run 'TestBuildUpdater|TestProbeURL' -v`
Expected: FAIL — undefined: `buildUpdater`, `probeURL`

- [ ] **Step 3: Implement `cmd/trainboard/update.go`**

```go
// M5 self-update wiring: builds the checker/applier seams from the device
// state, decides whether the updater is usable, and derives the health
// probe. Kept out of main.go to keep run() readable.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/mintopia/trainboard/internal/buildinfo"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/update"
	"github.com/mintopia/trainboard/internal/web"
)

// healthDeadline is how long after start the payload has to come healthy
// (first frame + web probe) before promotion is abandoned and the
// launcher's boot counter is left to converge (spec §2).
const healthDeadline = 60 * time.Second

// updaterSeams bundles the wired updater for main to hand to web/runtime.
type updaterSeams struct {
	checker   *update.Checker
	enabled   bool
	statePath string
}

// buildUpdater assembles the checker + applier. The updater is enabled
// only when this is a slot install (state file present) AND the keyring is
// non-empty (key ceremony done) AND the loaded config was valid (cfgValid
// false = E04 loop: no live config to read a channel from — a disabled
// checker still renders status so the operator sees why). A disabled
// updater is still non-nil: every seam stays callable and reports
// "unavailable" gracefully.
func buildUpdater(cfg config.Config, cfgValid bool, slotsDir, statePath string, log *slog.Logger) *updaterSeams {
	keys, keyErr := update.Keyring()
	_, stateErr := update.LoadState(statePath)
	enabled := cfgValid && keyErr == nil && stateErr == nil
	if keyErr != nil {
		log.Info("self-update unavailable: keyring", "reason", keyErr.Error())
	}
	if stateErr != nil {
		log.Info("self-update unavailable: not a slot install", "reason", stateErr.Error())
	}
	applier := &update.Applier{
		SlotsDir:  slotsDir,
		StatePath: statePath,
		Running:   buildinfo.Version(),
		Keys:      keys,
		HTTP:      &http.Client{Timeout: 5 * time.Minute}, // binary download on Pi WiFi
		Log:       log,
	}
	checker := update.NewChecker(update.NewClient(), applier, cfg, enabled, log)
	return &updaterSeams{checker: checker, enabled: enabled, statePath: statePath}
}

// webSeams returns the Sources/Actions fragments main merges into the web
// service wiring.
func (u *updaterSeams) webSources() func() update.Status { return u.checker.Status }

func (u *updaterSeams) webActions() (check, apply func(ctx context.Context) error, dismiss func() error) {
	return u.checker.CheckNow, u.checker.ApplyNow, func() error { return update.DismissRollback(u.statePath) }
}

// updateAvailable is the render loop's hint probe.
func (u *updaterSeams) updateAvailable() bool { return u.checker.Status().Available != "" }

// probeURL derives the loopback health-probe URL from the web listen
// address: an empty or wildcard host becomes 127.0.0.1. /login is the
// cheapest always-reachable authed-or-not route.
func probeURL(httpAddr string) string {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		host, port = "127.0.0.1", "80"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s/login", net.JoinHostPort(host, port))
}

// webProbe GETs probeURL and reports healthy for any HTTP response with a
// non-5xx status (the server is up and routing; auth state is irrelevant).
func webProbe(url string) func(ctx context.Context) error {
	client := &http.Client{Timeout: 5 * time.Second}
	return func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 500 {
			return fmt.Errorf("web probe: %s", resp.Status)
		}
		return nil
	}
}

// mergeUpdateSeams copies the updater's seams onto the web Sources/Actions
// structs (used by both boot paths).
func mergeUpdateSeams(src *web.Sources, act *web.Actions, u *updaterSeams) {
	src.UpdateStatus = u.webSources()
	check, apply, dismiss := u.webActions()
	act.UpdateCheck = check
	act.UpdateApply = apply
	act.UpdateDismiss = dismiss
}
```

(Resolve the import list properly once the file is final — only what is actually used.)

- [ ] **Step 4: Wire into `run()` and `runFaultLoop`**

In `cmd/trainboard/main.go` `run()`:

1. Add flags next to the others:

```go
	slotsDir := flag.String("slots", update.DefaultSlotsDir, "A/B slot directory (self-update)")
	statePath := flag.String("update-state", update.DefaultStatePath, "updater state file (self-update)")
```

2. Normal boot path, after `cfg` loads successfully and BEFORE `startWebServer`:

```go
	upd := buildUpdater(cfg, true, *slotsDir, *statePath, log)
```

3. `newWebService`/`startWebServer` currently build `web.Sources`/`web.Actions` inside `newWebService` — extend `newWebService` and `startWebServer` signatures with `upd *updaterSeams` (nil-tolerated: skip the merge when nil) and call `mergeUpdateSeams(&sources, &actions, upd)` before `web.NewService`. Update BOTH call sites (`run` passes `upd`; `runFaultLoop` passes its own — see below).

4. After `loop := runtime.NewLoop(...)` in `run()`:

```go
	loop.SetUpdateHint(upd.updateAvailable)
	firstFrame := make(chan struct{})
	loop.SetOnFirstFrame(func() { close(firstFrame) })
	go update.Health{
		FirstFrame: firstFrame,
		Probe:      webProbe(probeURL(*httpAddr)),
		Deadline:   healthDeadline,
		StatePath:  *statePath,
		Version:    buildinfo.Version(),
		Log:        log,
	}.Run(ctx)
	go upd.checker.Run(ctx, func() {
		log.Info("auto-applied update: exiting for restart into new slot")
		os.Exit(0)
	})
```

5. In `runFaultLoop` (both E04 and E07 paths): build a **manual-only** updater so the operator can apply a known-working release from recovery (spec §2):

```go
	// Recovery/E04 web UI can still manually check/apply updates (spec §2:
	// "from the recovery web UI the operator can … manually apply a
	// known-working update"), but nothing runs unattended here: the
	// checker's Run loop is never started in a fault loop.
	upd := buildUpdater(config.Default(), true, slotsDir, statePath, log)
```

`runFaultLoop` needs `slotsDir`/`statePath` parameters threaded from `run()`'s flags — add them to its signature (and `runConfigErrorLoop`'s wrapper). Note `cfgValid` is passed `true` here deliberately: manual apply from recovery must work; the channel falls back to `config.Default()`'s stable. No `Health` promotion and no `checker.Run` in fault loops.

6. `--recovery` branch (Task 10) now passes the real seams through `runFaultLoop` unchanged.

- [ ] **Step 5: Run the full suite**

Run: `go test -race ./... -count=1`
Expected: PASS — including `cmd/trainboard`'s existing wiring tests (their `newWebService` call sites need the new nil/`upd` argument).

- [ ] **Step 6: Manual smoke test (dev mode)**

```bash
go run ./cmd/trainboard --http :8080 --config /tmp/m5-smoke-config.json
```
Expected: preview mode starts (E04 config-error scene is fine — no config exists at that path); log line `self-update unavailable: not a slot install`; after completing /setup, the `/` status page's Software section shows "Self-update is not available on this device". Ctrl-C to stop.

- [ ] **Step 7: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add cmd/trainboard/
git commit -m "feat(runtime): wire updater — checker, health promotion, hint, recovery seams (#16-#19)"
```

---

### Task 15: `cmd/mkmanifest` — release manifest generator

A tiny CI tool: computes the decompressed binary's sha256 and emits the manifest JSON. Reuses `update.Manifest` so CI and device can never disagree on the schema.

**Files:**
- Create: `cmd/mkmanifest/main.go`
- Test: `cmd/mkmanifest/main_test.go`

**Interfaces:**
- Consumes: `update.Manifest`, `update.RequiredArch` (Task 4).
- Produces: CLI contract used by Task 16's workflows:
  `go run ./cmd/mkmanifest -version vX.Y.Z -channel stable|prerelease -commit <sha> -asset <name.gz> -binary <path> -min-version vA.B.C` → manifest JSON on stdout.

- [ ] **Step 1: Write the failing test**

```go
// cmd/mkmanifest/main_test.go
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/update"
)

func TestMkManifest(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "trainboard")
	payload := []byte("fake binary contents")
	if err := os.WriteFile(bin, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := mkmanifest(&out, args{
		version: "v0.2.0", channel: "stable", commit: "abc1234",
		asset: "trainboard_v0.2.0_linux_arm64.gz", binary: bin, minVersion: "v0.1.0",
	})
	if err != nil {
		t.Fatalf("mkmanifest: %v", err)
	}
	var m update.Manifest
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output not valid manifest JSON: %v", err)
	}
	sum := sha256.Sum256(payload)
	want := update.Manifest{
		Version: "v0.2.0", Channel: "stable", Commit: "abc1234", Arch: update.RequiredArch,
		Asset: "trainboard_v0.2.0_linux_arm64.gz", SHA256: hex.EncodeToString(sum[:]), MinVersion: "v0.1.0",
	}
	if m != want {
		t.Errorf("manifest:\n got %+v\nwant %+v", m, want)
	}
}

func TestMkManifestValidation(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "trainboard")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := args{version: "v0.2.0", channel: "stable", commit: "abc1234",
		asset: "a.gz", binary: bin, minVersion: "v0.1.0"}

	bad := base
	bad.version = "0.2.0" // missing v prefix
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("invalid semver accepted")
	}
	bad = base
	bad.channel = "nightly"
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("invalid channel accepted")
	}
	bad = base
	bad.binary = filepath.Join(t.TempDir(), "missing")
	if err := mkmanifest(&bytes.Buffer{}, bad); err == nil {
		t.Error("missing binary accepted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/mkmanifest/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement**

```go
// Command mkmanifest emits the signed-release manifest JSON (M5 spec §1)
// for a built binary. CI runs it between build and minisign; it reuses
// update.Manifest so the generator and the on-device verifier can never
// drift on schema.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"golang.org/x/mod/semver"

	"github.com/mintopia/trainboard/internal/update"
)

type args struct {
	version, channel, commit, asset, binary, minVersion string
}

func main() {
	var a args
	flag.StringVar(&a.version, "version", "", "release version (vX.Y.Z)")
	flag.StringVar(&a.channel, "channel", "stable", "stable or prerelease")
	flag.StringVar(&a.commit, "commit", "", "source commit (short sha)")
	flag.StringVar(&a.asset, "asset", "", "gzipped binary asset filename")
	flag.StringVar(&a.binary, "binary", "", "path to the DECOMPRESSED binary to hash")
	flag.StringVar(&a.minVersion, "min-version", "", "minimum-rollback version floor")
	flag.Parse()
	if err := mkmanifest(os.Stdout, a); err != nil {
		fmt.Fprintln(os.Stderr, "mkmanifest:", err)
		os.Exit(1)
	}
}

func mkmanifest(w io.Writer, a args) error {
	if !semver.IsValid(a.version) {
		return fmt.Errorf("version %q is not valid semver (vX.Y.Z)", a.version)
	}
	if a.channel != "stable" && a.channel != "prerelease" {
		return fmt.Errorf("channel %q must be stable or prerelease", a.channel)
	}
	if a.minVersion != "" && !semver.IsValid(a.minVersion) {
		return fmt.Errorf("min-version %q is not valid semver", a.minVersion)
	}
	if a.asset == "" || a.commit == "" {
		return fmt.Errorf("asset and commit are required")
	}
	f, err := os.Open(a.binary)
	if err != nil {
		return fmt.Errorf("opening binary: %w", err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hashing binary: %w", err)
	}
	m := update.Manifest{
		Version: a.version, Channel: a.channel, Commit: a.commit,
		Arch: update.RequiredArch, Asset: a.asset,
		SHA256: hex.EncodeToString(h.Sum(nil)), MinVersion: a.minVersion,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/mkmanifest/ -v`
Expected: PASS

- [ ] **Step 5: Lint and commit**

```bash
go vet ./... && golangci-lint run && go test -race ./... -count=1
git add cmd/mkmanifest/
git commit -m "feat(release): mkmanifest — manifest generator sharing the device schema (#16)"
```

---

### Task 16: Release workflow + version floor file + CI dry-run

Tag push → gate → build arm64 → manifest → minisign → GitHub release. A PR-triggered dry-run job proves the pipeline with a throwaway key before the first real tag.

**Files:**
- Create: `.github/workflows/release.yml`
- Create: `deploy/release/MIN_VERSION` (content: `v0.1.0` + newline)
- Modify: `.github/workflows/ci.yml` (append the `release-dryrun` job)

**Interfaces:**
- Consumes: `cmd/mkmanifest` (Task 15), `deploy/release/MIN_VERSION`.
- Produces: release assets exactly as the device expects (Task 6/7): `trainboard_<tag>_linux_arm64.gz`, `trainboard-launcher_<tag>_linux_arm64.gz`, `manifest.json`, `manifest.json.minisig`. Requires repo secret `MINISIGN_SECRET_KEY` (Task 17).

- [ ] **Step 1: Write `deploy/release/MIN_VERSION`**

```
v0.1.0
```

(Bump this file in a normal PR whenever a release must become the new rollback floor — e.g. after a key rotation or a security fix.)

- [ ] **Step 2: Write `.github/workflows/release.yml`**

```yaml
# Release pipeline (M5 spec §1): tag push → test gate → arm64 build →
# manifest → minisign → GitHub release. The MINISIGN_SECRET_KEY repo secret
# holds the CI signing key (unencrypted minisign secret key; the offline
# recovery key never touches CI — see docs/deploy.md §Self-update).
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
          cache: true
      - name: vet
        run: go vet ./...
      - name: test
        run: go test -race ./... -count=1
      - uses: golangci/golangci-lint-action@v8
        with:
          version: v2.10.1
      - name: build arm64 binaries
        env:
          VERSION: ${{ github.ref_name }}
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
            -ldflags "-X github.com/mintopia/trainboard/internal/buildinfo.version=${VERSION}" \
            -o trainboard ./cmd/trainboard
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
            -ldflags "-X github.com/mintopia/trainboard/internal/buildinfo.version=${VERSION}" \
            -o trainboard-launcher ./cmd/trainboard-launcher
      - name: manifest and assets
        env:
          VERSION: ${{ github.ref_name }}
        run: |
          # A semver prerelease suffix (v0.3.0-rc1) IS the prerelease channel.
          CHANNEL=stable
          case "$VERSION" in *-*) CHANNEL=prerelease;; esac
          echo "CHANNEL=$CHANNEL" >> "$GITHUB_ENV"
          go run ./cmd/mkmanifest \
            -version "$VERSION" -channel "$CHANNEL" -commit "${GITHUB_SHA:0:7}" \
            -asset "trainboard_${VERSION}_linux_arm64.gz" -binary trainboard \
            -min-version "$(tr -d '[:space:]' < deploy/release/MIN_VERSION)" > manifest.json
          gzip -9 -c trainboard > "trainboard_${VERSION}_linux_arm64.gz"
          gzip -9 -c trainboard-launcher > "trainboard-launcher_${VERSION}_linux_arm64.gz"
      - name: sign manifest
        env:
          MINISIGN_SECRET_KEY: ${{ secrets.MINISIGN_SECRET_KEY }}
        run: |
          sudo apt-get update -qq && sudo apt-get install -y -qq minisign
          printf '%s\n' "$MINISIGN_SECRET_KEY" > minisign.key
          minisign -S -s minisign.key -m manifest.json -x manifest.json.minisig \
            -t "trainboard ${{ github.ref_name }}"
          rm -f minisign.key
      - name: create GitHub release
        env:
          GH_TOKEN: ${{ github.token }}
          VERSION: ${{ github.ref_name }}
        run: |
          FLAGS="--generate-notes"
          [ "$CHANNEL" = "prerelease" ] && FLAGS="$FLAGS --prerelease"
          gh release create "$VERSION" $FLAGS \
            "trainboard_${VERSION}_linux_arm64.gz" \
            "trainboard-launcher_${VERSION}_linux_arm64.gz" \
            manifest.json manifest.json.minisig
```

- [ ] **Step 3: Append the dry-run job to `.github/workflows/ci.yml`**

```yaml
  release-dryrun:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
          cache: true
      - name: build arm64 binaries
        run: |
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o trainboard ./cmd/trainboard
          CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o trainboard-launcher ./cmd/trainboard-launcher
      - name: manifest, throwaway sign, verify round-trip
        run: |
          sudo apt-get update -qq && sudo apt-get install -y -qq minisign
          minisign -G -W -f -p dry.pub -s dry.key
          go run ./cmd/mkmanifest \
            -version v9.9.9 -channel stable -commit dryrun0 \
            -asset trainboard_v9.9.9_linux_arm64.gz -binary trainboard \
            -min-version "$(tr -d '[:space:]' < deploy/release/MIN_VERSION)" > manifest.json
          minisign -S -s dry.key -m manifest.json -x manifest.json.minisig -t "dry run"
          minisign -V -p dry.pub -m manifest.json -x manifest.json.minisig
```

- [ ] **Step 4: Verify locally what can be verified**

```bash
# YAML sanity + the exact mkmanifest invocation the workflow uses:
go build -o /tmp/tb-dryrun ./cmd/trainboard
go run ./cmd/mkmanifest -version v9.9.9 -channel stable -commit dryrun0 \
  -asset trainboard_v9.9.9_linux_arm64.gz -binary /tmp/tb-dryrun \
  -min-version "$(tr -d '[:space:]' < deploy/release/MIN_VERSION)"
```
Expected: valid manifest JSON on stdout. Full workflow proof comes from the dry-run job on this task's PR.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/release.yml .github/workflows/ci.yml deploy/release/MIN_VERSION
git commit -m "ci(release): tag-push release pipeline with minisign + PR dry-run (#16, #19)"
```

---

### Task 17: Migration script, systemd unit, deploy docs, key ceremony instructions

Everything the operator (Jess) needs: migrate the deployed Pi to slots, install the launcher, run the key ceremony, and the on-hardware test checklist. **The key ceremony itself is an attended step for Jess** — the code change it produces (filling `embeddedKeys`) is a two-line diff she commits after generating keys.

**Files:**
- Create: `deploy/migrate-to-slots.sh`
- Modify: `deploy/trainboard.service` (`ExecStart` → launcher)
- Modify: `docs/deploy.md` (Self-update section: layout, migration, fault code E07, key ceremony, hardware checklist)

**Interfaces:**
- Consumes: the launcher contract (Task 9), state schema (Task 1), release assets (Task 16).

- [ ] **Step 1: Write `deploy/migrate-to-slots.sh`**

```sh
#!/bin/sh
# One-time migration of a pre-M5 device to the A/B slot layout
# (docs/superpowers/specs/2026-07-09-m5-self-update-design.md §3).
# Run as root ON the Pi, AFTER copying the launcher into place:
#   scp trainboard-launcher root@trainboard.local:/opt/trainboard/launcher
#   ssh root@trainboard.local 'sh -s' < deploy/migrate-to-slots.sh
set -eu

SLOTS=/opt/trainboard/slots
STATE_DIR=/var/lib/trainboard/updater
LAUNCHER=/opt/trainboard/launcher
OLD_BIN=/usr/local/bin/trainboard

[ -x "$LAUNCHER" ] || { echo "ERROR: launcher not installed at $LAUNCHER — scp it first"; exit 1; }

mkdir -p "$SLOTS/a" "$SLOTS/b" "$STATE_DIR"

if [ ! -x "$SLOTS/a/trainboard" ]; then
  [ -x "$OLD_BIN" ] || { echo "ERROR: no existing binary at $OLD_BIN to migrate"; exit 1; }
  cp "$OLD_BIN" "$SLOTS/a/trainboard"
  chmod 0755 "$SLOTS/a/trainboard"
  echo "migrated $OLD_BIN -> $SLOTS/a/trainboard"
fi

if [ ! -f "$STATE_DIR/state.json" ]; then
  # Seed state: slot a is active AND known-good. A "dev" version is fine —
  # a non-semver running version never blocks the first real update.
  VERSION=$("$SLOTS/a/trainboard" --version | awk '{print $2}')
  cat > "$STATE_DIR/state.json" <<EOF
{
  "active": "a",
  "active_version": "$VERSION",
  "known_good": "a",
  "known_good_version": "$VERSION",
  "boot_attempts": 0,
  "version_floor": "",
  "rolled_back_from": ""
}
EOF
  echo "seeded $STATE_DIR/state.json (version $VERSION)"
fi

cat <<'DONE'
Slot layout ready. Finish by installing the updated unit and restarting:
  scp deploy/trainboard.service root@trainboard.local:/etc/systemd/system/trainboard.service
  # IMPORTANT: if your current unit's ExecStart carries extra flags
  # (--manage-network), re-add them to the new ExecStart line first.
  ssh root@trainboard.local 'systemctl daemon-reload && systemctl restart trainboard'
Then delete the old binary once the board is confirmed up:
  ssh root@trainboard.local rm /usr/local/bin/trainboard
DONE
```

Make it executable: `chmod +x deploy/migrate-to-slots.sh`.

- [ ] **Step 2: Update `deploy/trainboard.service`**

Change the `ExecStart` line and its comment block:

```ini
# M5 self-update: ExecStart runs the stable launcher, which selects the
# active A/B slot and exec()s /opt/trainboard/slots/{a,b}/trainboard —
# process replacement, so WatchdogSec/NotifyAccess below still govern the
# payload. Add --manage-network here (after the wlan0 migration, see
# docs/deploy.md) exactly as before; the launcher passes all flags through.
ExecStart=/opt/trainboard/launcher --production
```

Everything else in the unit stays byte-identical.

- [ ] **Step 3: Update `docs/deploy.md`**

Add a `## Self-update (M5)` section covering, in this order (write real prose, not bullets-of-bullets — match the doc's existing voice):

1. **How it works** (three sentences: A/B slots, signed manifest, launcher rollback; link the spec).
2. **Where things live** — extend the existing paths table:

| What | Path |
|---|---|
| Launcher (stable shim) | `/opt/trainboard/launcher` |
| Slot binaries | `/opt/trainboard/slots/{a,b}/trainboard` |
| Updater state | `/var/lib/trainboard/updater/state.json` |
| Version floor (repo) | `deploy/release/MIN_VERSION` |

3. **Migrating an existing device** — the `scp` launcher + `migrate-to-slots.sh` + unit-swap steps from the script's output, verbatim.
4. **Key ceremony** (once, on the workstation):

```
brew install minisign

# CI signing key (unencrypted — it lives in a GitHub Actions secret):
minisign -G -W -f -p ci.pub -s ci.key -c "trainboard CI signing key"
gh secret set MINISIGN_SECRET_KEY < ci.key

# Offline recovery key (password-protected; NEVER uploaded anywhere):
minisign -G -f -p recovery.pub -s recovery.key -c "trainboard recovery key"
# → store recovery.key's CONTENT and its password in the password manager.

# Embed both PUBLIC keys in the device keyring: copy the second line of
# ci.pub and recovery.pub into internal/update/keyring.go's embeddedKeys
# slice, then commit. Finally: rm ci.key recovery.key ci.pub recovery.pub
```

5. **Cutting a release**: `git tag v0.1.0 && git push origin v0.1.0` (a `-rc1` suffix ⇒ prerelease channel). Note the MIN_VERSION bump procedure and when to use it (key rotation, security floor).
6. **Fault code table**: add the row `| E07 | Update recovery mode | The launcher hit a double fault (both slots failing); the board serves only the web UI + AP. Fix config or apply a known-good release from the web UI, or reflash. |`
7. **On-hardware test checklist** (attended, after first release exists):
   - [ ] Migrate the Pi (`migrate-to-slots.sh`), confirm board boots via launcher (`systemctl status trainboard` shows launcher → payload PID).
   - [ ] Cut `v0.1.0`, then `v0.1.1`; web UI shows "Available v0.1.1"; Apply; board restarts into v0.1.1; status shows promoted (`known_good_version: v0.1.1` in state.json).
   - [ ] Pull power mid-download; on reboot the board still runs the old version and a re-apply succeeds.
   - [ ] Break slot b deliberately (`ssh: echo garbage > /opt/trainboard/slots/b/trainboard` after staging an update, before restart); observe 3 failed boots then automatic rollback + web UI banner.
   - [ ] Force a double fault (corrupt BOTH slot binaries with the unit stopped, then start); observe E07 on-glass + web UI reachable; restore by re-applying a release from the recovery web UI.

- [ ] **Step 4: Verify the script parses and the docs build**

```bash
sh -n deploy/migrate-to-slots.sh
```
Expected: exit 0 (syntax OK).

- [ ] **Step 5: Commit**

```bash
git add deploy/migrate-to-slots.sh deploy/trainboard.service docs/deploy.md
git commit -m "docs(deploy),feat(deploy): slot migration script, launcher unit, self-update guide (#16-#19)"
```

---

### Task 18: Milestone close-out (attended — Jess + agent together)

No code. Sequence:

1. **Key ceremony** (Jess, per deploy.md §Self-update key ceremony) → commit filling `internal/update/keyring.go:embeddedKeys` with the two public-key lines: `feat(update): embed production keyring (CI + recovery keys) (#17)`. CI must be green — `TestKeyringEmptyUntilCeremony` flips to its populated branch automatically.
2. **PR + review**: single PR for the milestone branch (repo convention), Codex review per AGENTS.md.
3. **First release**: tag `v0.1.0` after merge; confirm the release workflow publishes all four assets and the dry-run job stays green.
4. **Hardware**: run the deploy.md on-hardware checklist (migration was deliberately left until a release exists to update TO). This also completes the still-pending "deploy main to the Pi" step from M3.5.
5. Close #16, #17, #18, #19 with links to the PR + first release.

---

## Execution notes

- Work on a feature branch (e.g. `feat/m5-self-update`) per repo convention; single PR at the end.
- Tasks 1–8 are pure `internal/update` work with no cross-package edits — safe to execute strictly in order, each independently committable.
- Tasks 9–15 touch neighbouring packages; Task 14 (main wiring) MUST come after 8, 10, 11, 12, 13.
- Task 16 is independent of 9–15 except for `cmd/mkmanifest` (Task 15).
- If `jedisct1/go-minisign`'s API differs from the calls written here (`NewPublicKey`, `DecodeSignature`, `PublicKey.Verify`), check the module's source under `$(go env GOMODCACHE)` and adapt the three call sites in `keyring.go` — the tests define the behavioural contract.
