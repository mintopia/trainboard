# M1 Plan C — Board Scenes + Runtime + Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Scope note:** M1 (`PLAN.md`) is split into three sequenced plans. **Plan A (merged)** — `display` + `render`. **Plan B (merged)** — `data` + `config`. **Plan C (this doc)** — `internal/board` + `internal/runtime` + `internal/obs` + `cmd/trainboard`, per the approved design `docs/superpowers/specs/2026-07-02-m1c-board-runtime-design.md`. Closes **#23, #28, #31**. Hardware issues **#21/#29 are out of scope** (separate hardware-bench session).

**Goal:** Compose the merged render/display foundation and Darwin data client into a runnable `cmd/trainboard` that fetches live departures and drives the SSD1322 through a complete six-scene set, entirely buildable/testable/previewable on host.

**Architecture:** Three new packages + one command. `board` is pure model→pixels: owns ALL exact-pixel geometry constants, builds scenes from `render` primitives plus its own board-local animated elements (`render.Element` implementations), deterministic, golden-tested. `runtime` owns time and concurrency: poller goroutine → immutable `board.Snapshot` via `atomic.Pointer`, lock-free 25fps render loop, fetch-result→state classification, brightness-on-change. `obs` is stdlib-only: bounded event ring + slog tee + fault codes. `cmd/trainboard` wires config→data→runtime→transport with a PNG preview transport for host.

**Tech Stack:** Go 1.26, stdlib + existing deps only (`golang.org/x/image` via `render`, `periph.io` via `display`). No new dependencies.

## Global Constraints

- **Module:** `github.com/mintopia/trainboard`; Go `1.26`; **no new dependencies**.
- **`render` and `display` interfaces are frozen.** Do not add fields/methods to `render` or `display` types. New element behaviour lives in `internal/board` as new `render.Element` implementations. (Exception: Task 3 adds the test-only sub-package `internal/render/rendertest`, and Task 12 adds a `Version` to `internal/buildinfo` — nothing else.)
- **Geometry from the spec, not PIL.** All pixel constants are the named constants in Task 4, taken from the approved design (derived from `reference/src/trains/elements.py::render_departure` + `scenes.py`). Never call/port PIL measurement.
- **Headcode is never drawn** (`rsid` is a retail ID, not a headcode). The 27px constant is retained but unfed; platform stays at x=45.
- **Fonts:** `regular` = Dot Matrix Regular @10px, `bold` = Bold @10px, `boldtall` = Bold Tall @10px, `boldlarge` = Bold @20px (reference `board.py::load_fonts`). Loaded via `render.LoadFont` from the embedded TTFs (`render.RegularTTF`, `render.BoldTTF`, `render.BoldTallTTF`).
- **Full-frame flush every tick** (ADR 0002 baseline). No dirty-region tracking anywhere in this plan.
- **Tick = 0.04s (25fps).** All animation constants are in ticks. Reference parity: scroll step 1px/tick horizontal (`render.ScrollingText`, exists), 2px/tick vertical (board elements, this plan).
- **Snapshot immutability:** a published `*board.Snapshot` and everything reachable from it is never mutated after publish. The poller builds a fresh one per fetch; the render loop only reads.
- **State classification table (spec §Fetch-result classification)** is authoritative, including: 5-minute stale grace (`ADR 0003`), x509 time-validity → `ClockNotSynced` (never `Error`, never AP-fallback-triggering), never-succeeded fetch error → `Error`.
- **Scene priority:** `HotspotInfo > Error > ClockNotSynced > NoServices/DepartureBoard`; `Initialising` pre-first-data. `HotspotInfo` is defined but **never selected in M1** (no code path sets it; M3 will).
- **`config.Board.Services` → `data.Filter.MaxServices`. `data.Request.NumRows` is always 10.** (M1B carry-over; prevents false NoServices.)
- **Secrets:** never log the token; always log `cfg.Redacted()` / rely on config's `String()`. `.env` is gitignored and only read by live probes/dev runs.
- **TDD:** red → green per task; every task ends with `make check` green (vet + golangci-lint + `go test -race ./...` from Task 11 onward; plain `go test ./...` before that) and a commit.
- **Golden tests:** exact-byte PNG comparison via the Task 3 `rendertest` harness with `-update` regeneration — never loosen a comparison. Golden scenes/frames use fixed `time.Time`s and fixed ticks; nothing reads the wall clock.

---

### Task 1: obs — bounded event ring

Fixed-capacity, thread-safe, oldest-evicted event ring. Stdlib only. This is the M2 `/status` page's future data source; in M1C the slog tee (Task 2) and runtime write into it.

**Files:**
- Create: `internal/obs/ring.go`
- Test: `internal/obs/ring_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `type Event struct { Time time.Time; Level slog.Level; Msg string; Attrs map[string]string }`
  - `func NewRing(capacity int) *Ring` — capacity ≤ 0 ⇒ panic (programmer error).
  - `func (r *Ring) Add(e Event)`
  - `func (r *Ring) Events() []Event` — copy, oldest → newest.
  - `func (r *Ring) Len() int`
  - `const DefaultRingCapacity = 256`

- [ ] **Step 1: Write the failing test**

`internal/obs/ring_test.go`:
```go
package obs

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func evt(i int) Event {
	return Event{Time: time.Unix(int64(i), 0), Msg: fmt.Sprintf("e%d", i)}
}

func TestRingKeepsInsertionOrder(t *testing.T) {
	r := NewRing(4)
	for i := 0; i < 3; i++ {
		r.Add(evt(i))
	}
	got := r.Events()
	if len(got) != 3 || r.Len() != 3 {
		t.Fatalf("len = %d/%d, want 3", len(got), r.Len())
	}
	for i, e := range got {
		if e.Msg != fmt.Sprintf("e%d", i) {
			t.Fatalf("got[%d].Msg = %q", i, e.Msg)
		}
	}
}

func TestRingEvictsOldest(t *testing.T) {
	r := NewRing(4)
	for i := 0; i < 10; i++ {
		r.Add(evt(i))
	}
	got := r.Events()
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	for i, e := range got {
		if want := fmt.Sprintf("e%d", i+6); e.Msg != want {
			t.Fatalf("got[%d].Msg = %q, want %q", i, e.Msg, want)
		}
	}
}

func TestRingEventsReturnsCopy(t *testing.T) {
	r := NewRing(2)
	r.Add(evt(1))
	got := r.Events()
	got[0].Msg = "mutated"
	if r.Events()[0].Msg != "e1" {
		t.Fatal("Events() must return a copy")
	}
}

func TestRingConcurrentAddAndRead(t *testing.T) {
	r := NewRing(DefaultRingCapacity)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.Add(evt(w*1000 + i))
				_ = r.Events()
			}
		}(w)
	}
	wg.Wait()
	if r.Len() != DefaultRingCapacity {
		t.Fatalf("Len = %d, want %d", r.Len(), DefaultRingCapacity)
	}
}

func TestRingZeroCapacityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRing(0) must panic")
		}
	}()
	NewRing(0)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/obs/ -v`
Expected: FAIL — `undefined: NewRing` (package won't compile until ring.go exists).

- [ ] **Step 3: Write minimal implementation**

`internal/obs/ring.go`:
```go
// Package obs provides observability primitives for the board: a bounded
// in-memory event ring, a slog handler that tees into it, and the on-screen
// fault-code registry. Stdlib only.
package obs

import (
	"log/slog"
	"sync"
	"time"
)

// DefaultRingCapacity is the ring size used by the application.
const DefaultRingCapacity = 256

// Event is one recorded observation: a log record, state transition, or
// timing sample.
type Event struct {
	Time  time.Time
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// Ring is a fixed-capacity, thread-safe event buffer that evicts the oldest
// entry when full.
type Ring struct {
	mu    sync.Mutex
	buf   []Event
	start int // index of oldest
	n     int // count
}

// NewRing returns a ring holding at most capacity events. capacity must be
// positive.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("obs: ring capacity must be positive")
	}
	return &Ring{buf: make([]Event, capacity)}
}

// Add appends an event, evicting the oldest if the ring is full.
func (r *Ring) Add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
		return
	}
	r.buf[r.start] = e
	r.start = (r.start + 1) % len(r.buf)
}

// Events returns a copy of the buffered events, oldest first.
func (r *Ring) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, r.n)
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// Len reports how many events are buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/obs/ -race -v`
Expected: PASS (all 5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/obs/ring.go internal/obs/ring_test.go
git commit -m "feat(obs): bounded thread-safe event ring"
```

---

### Task 2: obs — fault codes + journald-friendly slog logger with ring tee

The fault-code registry surfaced on-screen during Error/ClockNotSynced, and the process logger: text to a writer (journald adds its own timestamps; we drop slog's) with every record ≥ Info mirrored into the ring.

**Files:**
- Create: `internal/obs/faults.go`, `internal/obs/log.go`
- Test: `internal/obs/faults_test.go`, `internal/obs/log_test.go`

**Interfaces:**
- Consumes: `Ring`, `Event` (Task 1).
- Produces:
  - `type FaultCode string`
  - `const (FaultNone FaultCode = ""; FaultDarwinUnreachable FaultCode = "E01"; FaultAuthRejected FaultCode = "E02"; FaultClockNotSynced FaultCode = "E03"; FaultConfigError FaultCode = "E04")`
  - `func (f FaultCode) Message() string` — short operator-facing text; empty for `FaultNone`.
  - `func NewLogger(w io.Writer, ring *Ring, level slog.Level) *slog.Logger` — text handler on `w` with the `time` attr removed (journald-friendly), teeing every record at ≥ `level` into `ring` (nil ring ⇒ no tee).

- [ ] **Step 1: Write the failing tests**

`internal/obs/faults_test.go`:
```go
package obs

import "testing"

func TestFaultMessages(t *testing.T) {
	cases := map[FaultCode]string{
		FaultNone:              "",
		FaultDarwinUnreachable: "Darwin unreachable",
		FaultAuthRejected:      "Darwin token rejected",
		FaultClockNotSynced:    "Waiting for time sync",
		FaultConfigError:       "Configuration error",
	}
	for code, want := range cases {
		if got := code.Message(); got != want {
			t.Errorf("%q.Message() = %q, want %q", code, got, want)
		}
	}
}
```

`internal/obs/log_test.go`:
```go
package obs

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerWritesTextWithoutTime(t *testing.T) {
	var sb strings.Builder
	log := NewLogger(&sb, nil, slog.LevelInfo)
	log.Info("hello", "k", "v")
	out := sb.String()
	if !strings.Contains(out, "msg=hello") || !strings.Contains(out, "k=v") {
		t.Fatalf("missing msg/attr in %q", out)
	}
	if strings.Contains(out, "time=") {
		t.Fatalf("time attr must be dropped for journald, got %q", out)
	}
}

func TestLoggerTeesIntoRing(t *testing.T) {
	ring := NewRing(8)
	log := NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	log.Info("fetched", "departures", "5")
	log.Warn("fetch failed", "err", "boom")
	events := ring.Events()
	if len(events) != 2 {
		t.Fatalf("ring has %d events, want 2", len(events))
	}
	if events[0].Msg != "fetched" || events[0].Level != slog.LevelInfo {
		t.Fatalf("event 0 = %+v", events[0])
	}
	if events[1].Attrs["err"] != "boom" {
		t.Fatalf("event 1 attrs = %+v", events[1].Attrs)
	}
}

func TestLoggerLevelGate(t *testing.T) {
	ring := NewRing(8)
	var sb strings.Builder
	log := NewLogger(&sb, ring, slog.LevelInfo)
	log.Debug("noisy")
	if ring.Len() != 0 || sb.Len() != 0 {
		t.Fatal("debug record must be dropped at Info level")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/obs/ -v`
Expected: FAIL — `undefined: FaultCode`, `undefined: NewLogger`.

- [ ] **Step 3: Write minimal implementation**

`internal/obs/faults.go`:
```go
package obs

// FaultCode is a short diagnostic code surfaced in a corner of the screen
// during Error / ClockNotSynced scenes for field diagnosis.
type FaultCode string

// The M1 fault-code registry (spec §Observability).
const (
	FaultNone              FaultCode = ""
	FaultDarwinUnreachable FaultCode = "E01"
	FaultAuthRejected      FaultCode = "E02"
	FaultClockNotSynced    FaultCode = "E03"
	FaultConfigError       FaultCode = "E04"
)

// Message returns the short operator-facing description of the fault.
func (f FaultCode) Message() string {
	switch f {
	case FaultDarwinUnreachable:
		return "Darwin unreachable"
	case FaultAuthRejected:
		return "Darwin token rejected"
	case FaultClockNotSynced:
		return "Waiting for time sync"
	case FaultConfigError:
		return "Configuration error"
	default:
		return ""
	}
}
```

`internal/obs/log.go`:
```go
package obs

import (
	"context"
	"io"
	"log/slog"
)

// NewLogger returns a slog.Logger that writes logfmt-style text to w with
// the time attribute removed (journald stamps its own), and tees every
// record at or above level into ring (nil ring disables the tee).
func NewLogger(w io.Writer, ring *Ring, level slog.Level) *slog.Logger {
	text := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(&teeHandler{text: text, ring: ring, level: level})
}

// teeHandler forwards records to the text handler and records them in the ring.
type teeHandler struct {
	text  slog.Handler
	ring  *Ring
	level slog.Level
}

func (h *teeHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	if h.ring != nil {
		attrs := make(map[string]string, rec.NumAttrs())
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		h.ring.Add(Event{Time: rec.Time, Level: rec.Level, Msg: rec.Message, Attrs: attrs})
	}
	return h.text.Handle(ctx, rec)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{text: h.text.WithAttrs(attrs), ring: h.ring, level: h.level}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{text: h.text.WithGroup(name), ring: h.ring, level: h.level}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/obs/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/obs/faults.go internal/obs/faults_test.go internal/obs/log.go internal/obs/log_test.go
git commit -m "feat(obs): fault-code registry + journald slog logger teeing into ring"
```

---

### Task 3: rendertest — golden-image harness for downstream packages

`board` needs golden-image tests, but the harness (`assertGolden`/`toGray`) is private to `render`'s in-package tests. **It cannot be extracted and re-imported by those tests: `render`'s test files are `package render`, and an in-package test file importing `rendertest` (which imports `render`) is an import cycle.** So: create `rendertest` as a standalone mirror of the private harness for `board` (and later packages) to use, and leave `render`'s tests completely untouched — which also honours the "render is frozen" constraint. The duplication is ~50 lines of stable test helper; each file carries a comment pointing at the other.

**Files:**
- Create: `internal/render/rendertest/rendertest.go`
- Modify: `internal/render/golden_test.go` — comment only (see Step 3); no behaviour change.

**Interfaces:**
- Consumes: `render.Framebuffer`.
- Produces:
  - `func ToGray(fb *render.Framebuffer) *image.Gray` — levels 0–15 scaled ×17 to 0–255 (must byte-match `golden_test.go`'s private `toGray`; **copy its scaling expression exactly**).
  - `func AssertGolden(t *testing.T, dir, name string, fb *render.Framebuffer)` — exact-byte PNG compare against `dir/name.png`; regenerates when the test binary runs with `-update`.

- [ ] **Step 1: Read the existing harness**

Read `internal/render/golden_test.go` fully. The new package must reproduce `toGray`'s exact pixel scaling and `assertGolden`'s exact compare/update semantics (including the "missing golden" error hint).

- [ ] **Step 2: Create the package**

`internal/render/rendertest/rendertest.go`:
```go
// Package rendertest provides the golden-image test harness for packages
// downstream of render (board, …). It intentionally mirrors the private
// harness in render's own golden_test.go, which cannot import this package
// (render's tests are in-package; importing rendertest would be an import
// cycle). Keep the two in lockstep. Import from _test files only.
package rendertest

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
)

var update = flag.Bool("update", false, "regenerate golden images")

// ToGray converts the 4-bit framebuffer to an 8-bit grayscale image
// (level × 17, so 15 → 255) for PNG golden storage.
func ToGray(fb *render.Framebuffer) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, fb.W, fb.H))
	for i, lv := range fb.Pix {
		g.Pix[i] = lv * 17
	}
	return g
}

// AssertGolden compares fb against dir/name.png byte-exactly, regenerating
// the file when -update is set.
func AssertGolden(t *testing.T, dir, name string, fb *render.Framebuffer) {
	t.Helper()
	path := filepath.Join(dir, name+".png")
	g := ToGray(fb)
	if *update {
		var buf bytes.Buffer
		if err := png.Encode(&buf, g); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run: go test -run %s -update): %v", path, t.Name(), err)
	}
	want, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	wg, ok := want.(*image.Gray)
	if !ok || wg.Rect != g.Rect || !bytes.Equal(wg.Pix, g.Pix) {
		t.Fatalf("framebuffer differs from golden %s", path)
	}
}
```
**IMPORTANT:** before committing, diff this against the private originals in `golden_test.go` — if the original `toGray` scales differently (e.g. not `lv * 17`), copy the original's exact expression. Byte-identical output is the requirement; the code above is the expected shape, the file on disk is the authority.

- [ ] **Step 3: Write a self-test + cross-link the two harnesses**

`internal/render/rendertest/rendertest_test.go` (external test package — no cycle):
```go
package rendertest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestToGrayScalesLevels(t *testing.T) {
	fb := render.New(2, 1)
	fb.SetPixel(0, 0, 15)
	fb.SetPixel(1, 0, 8)
	g := rendertest.ToGray(fb)
	if g.Pix[0] != 255 || g.Pix[1] != 8*17 {
		t.Fatalf("pix = %v, want [255 136]", g.Pix[:2])
	}
}

func TestAssertGoldenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	fb := render.New(4, 4)
	fb.SetPixel(1, 2, 9)
	// Write the golden by hand, then assert against it.
	writeGolden(t, dir, "rt", fb)
	rendertest.AssertGolden(t, dir, "rt", fb)
}

// writeGolden encodes exactly what AssertGolden -update would write.
func writeGolden(t *testing.T, dir, name string, fb *render.Framebuffer) {
	t.Helper()
	g := rendertest.ToGray(fb)
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := encodePNG(f, g); err != nil {
		t.Fatal(err)
	}
}
```
(Implement `encodePNG` as a one-line `png.Encode` wrapper in the test file, with the `image/png` import.)

Then add one comment line above `toGray` in `internal/render/golden_test.go`:
```go
// Kept in lockstep with internal/render/rendertest (which downstream
// packages use; it can't be imported from these in-package tests).
```

- [ ] **Step 4: Verify nothing in render changed behaviour**

Run: `go test ./internal/render/... -count=1`
Expected: PASS; `git status --porcelain internal/render/testdata` prints nothing (no golden touched).

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: vet + lint + tests all green.

- [ ] **Step 6: Commit**

```bash
git add internal/render/rendertest/ internal/render/golden_test.go
git commit -m "test(render): rendertest golden harness for downstream packages"
```

---

### Task 4: board — fonts, geometry constants, departure row builder

The foundation of `internal/board`: the loaded font set, every exact-pixel constant, `ordinal()`, and the pure row builder that turns one `data.Departure` into positioned `render` elements. Golden-tested.

**Files:**
- Create: `internal/board/board.go` (package doc + geometry constants), `internal/board/fonts.go`, `internal/board/row.go`
- Create: `internal/board/fixtures_test.go` (shared test fixtures used by Tasks 4–8)
- Test: `internal/board/row_test.go`
- Create dir: `internal/board/testdata/`

**Interfaces:**
- Consumes: `render.LoadFont`, `render.RegularTTF/BoldTTF/BoldTallTTF`, `render.StaticText`, `render.Element`, `data.Departure`.
- Produces:
  - Constants: `W = 256`, `H = 64`, `RowH = 12`, `ColOrderX = 0`, `ColSchedX = 17`, `ColSchedW = 28`, `ColHeadcodeX = 45`, `ColHeadcodeW = 27` (retained, never fed), `ColPlatformX = 45`, `ColPlatformW = 19`, `ColDestX = 64`, `ColStatusW = 40`, `ColStatusX = 216` (= W − ColStatusW), `CallingLabelW = 42`, `CallingListX = 42`, `CallingListW = 214`, `ServiceInfoY = 24`, `RemainingY = 36`, `ClockY = 50`, `ClockH = 14`.
  - `type Fonts struct { Regular, Bold, BoldTall, BoldLarge *render.Font }`
  - `func LoadFonts() (*Fonts, error)` — Regular@10, Bold@10, BoldTall@10, Bold@20.
  - `func ordinal(n int) string` — `1→"1st"`, `2→"2nd"`, `3→"3rd"`, `4→"4th"`, `11..13→"11th".."13th"`, `21→"21st"`.
  - `func rowElements(d data.Departure, order, y int, f *Fonts) []render.Element` — the six-column row at vertical offset y: order (left, x=0), scheduled (centered in 28px at x=17), platform (centered in 19px at x=45, **only if non-empty**), destination (left, x=64), status (right-aligned in 40px at x=216, text = `string(d.Status)`). All Regular font, `Level: 15`, `H: RowH`. Headcode column never drawn.

- [ ] **Step 1: Write the shared fixtures + failing test**

`internal/board/fixtures_test.go`:
```go
package board

import (
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

// fixedNow is the wall-clock instant every golden test renders at.
var fixedNow = time.Date(2026, 7, 6, 10, 30, 45, 0, time.UTC)

func mustFonts(t *testing.T) *Fonts {
	t.Helper()
	f, err := LoadFonts()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func dep(sched, plat, dest string, status data.Status, calling ...string) data.Departure {
	cps := make([]data.CallingPoint, len(calling))
	for i, name := range calling {
		cps[i] = data.CallingPoint{Location: data.Location{Name: name}, ScheduledTime: "11:0" + string(rune('0'+i))}
	}
	return data.Departure{
		ScheduledTime: sched,
		Status:        status,
		Platform:      plat,
		Operator:      "Great Western Railway",
		Destination:   data.Location{Name: dest},
		CallingPoints: cps,
		Length:        5,
	}
}

// fixtureBoard covers the spec's fixture matrix: on-time, delayed (Exp hh:mm),
// cancelled, missing platform, long destination.
func fixtureBoard() *data.Board {
	return &data.Board{
		GeneratedAt:  fixedNow,
		LocationName: "Paddington",
		CRS:          "PAD",
		Departures: []data.Departure{
			dep("10:32", "9", "Bristol Temple Meads", data.StatusOnTime, "Reading", "Didcot Parkway", "Swindon"),
			dep("10:41", "12", "Oxford", data.Status("Exp 10:44"), "Slough", "Reading"),
			dep("10:45", "", "Plymouth", data.StatusDelayed, "Reading", "Taunton"),
			dep("10:52", "5", "Weston-super-Mare via Bristol Temple Meads", data.StatusOnTime, "Reading"),
			dep("11:02", "8", "Cardiff Central", data.StatusCancelled),
		},
		Messages: []string{"Major disruption between Reading and London Paddington is expected until the end of the day. Please check before you travel."},
	}
}

func singleDepBoard() *data.Board {
	b := fixtureBoard()
	b.Departures = b.Departures[:1]
	return b
}

func emptyBoard() *data.Board {
	b := fixtureBoard()
	b.Departures = nil
	return b
}
```

`internal/board/row_test.go`:
```go
package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestOrdinal(t *testing.T) {
	cases := map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 22: "22nd", 103: "103rd"}
	for n, want := range cases {
		if got := ordinal(n); got != want {
			t.Errorf("ordinal(%d) = %q, want %q", n, got, want)
		}
	}
}

func renderRow(t *testing.T, d data.Departure, order int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, RowH)
	scene := &render.Scene{Elements: rowElements(d, order, 0, mustFonts(t))}
	scene.Render(fb, 0, fixedNow)
	return fb
}

func TestRowGoldenOnTime(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_ontime", renderRow(t, fixtureBoard().Departures[0], 1))
}

func TestRowGoldenExpected(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_expected", renderRow(t, fixtureBoard().Departures[1], 2))
}

func TestRowGoldenMissingPlatform(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_noplatform", renderRow(t, fixtureBoard().Departures[2], 3))
}

func TestRowGoldenCancelled(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "row_cancelled", renderRow(t, fixtureBoard().Departures[4], 5))
}

// A row with no platform must leave the platform box pixels untouched.
func TestRowNoPlatformLeavesGap(t *testing.T) {
	fb := renderRow(t, fixtureBoard().Departures[2], 3)
	for x := ColPlatformX; x < ColPlatformX+ColPlatformW; x++ {
		for y := 0; y < RowH; y++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("platform box pixel (%d,%d) = %d, want 0", x, y, fb.At(x, y))
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/board/ -v`
Expected: FAIL — package doesn't compile (`undefined: LoadFonts`, `rowElements`, constants).

- [ ] **Step 3: Write the implementation**

`internal/board/board.go`:
```go
// Package board maps the data model to render scenes. It owns all
// exact-pixel geometry (derived from the reference implementation's
// render_departure and scene layouts — see the M1C design doc), builds rows
// and scenes from render primitives plus its own animated elements, and
// selects the active scene by priority. Pure: no I/O, no goroutines, no
// wall-clock reads.
package board

// Panel and layout geometry (pixels). Sources: design doc §DepartureBoard
// geometry; reference render_departure column table.
const (
	W    = 256
	H    = 64
	RowH = 12

	ColOrderX    = 0
	ColSchedX    = 17
	ColSchedW    = 28
	ColHeadcodeX = 45 // retained for a future headcode source; never drawn in M1
	ColHeadcodeW = 27
	ColPlatformX = 45
	ColPlatformW = 19
	ColDestX     = 64
	ColStatusW   = 40
	ColStatusX   = W - ColStatusW // 216

	CallingLabelW = 42
	CallingListX  = 42
	CallingListW  = 214
	ServiceInfoY  = 24
	RemainingY    = 36
	ClockY        = 50
	ClockH        = 14
)
```

`internal/board/fonts.go`:
```go
package board

import "github.com/mintopia/trainboard/internal/render"

// Fonts is the loaded font set used by all scenes, mirroring the reference
// board: regular/bold/boldtall at 10px, boldlarge at 20px.
type Fonts struct {
	Regular, Bold, BoldTall, BoldLarge *render.Font
}

// LoadFonts rasterizes the embedded Dot Matrix faces at reference sizes.
func LoadFonts() (*Fonts, error) {
	reg, err := render.LoadFont(render.RegularTTF, 10)
	if err != nil {
		return nil, err
	}
	bold, err := render.LoadFont(render.BoldTTF, 10)
	if err != nil {
		return nil, err
	}
	tall, err := render.LoadFont(render.BoldTallTTF, 10)
	if err != nil {
		return nil, err
	}
	large, err := render.LoadFont(render.BoldTTF, 20)
	if err != nil {
		return nil, err
	}
	return &Fonts{Regular: reg, Bold: bold, BoldTall: tall, BoldLarge: large}, nil
}
```

`internal/board/row.go`:
```go
package board

import (
	"strconv"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// ordinal formats 1 → "1st", 2 → "2nd", 11 → "11th", matching the reference.
func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// rowElements builds the six-column departure row at vertical offset y.
// Headcode is never drawn (no data source); platform only when present.
func rowElements(d data.Departure, order, y int, f *Fonts) []render.Element {
	els := []render.Element{
		&render.StaticText{Font: f.Regular, Text: ordinal(order), X: ColOrderX, Y: y, W: ColSchedX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: d.ScheduledTime, X: ColSchedX, Y: y, W: ColSchedW, H: RowH, Align: render.AlignCenter, Level: 15},
	}
	if d.Platform != "" {
		els = append(els, &render.StaticText{Font: f.Regular, Text: d.Platform, X: ColPlatformX, Y: y, W: ColPlatformW, H: RowH, Align: render.AlignCenter, Level: 15})
	}
	els = append(els,
		&render.StaticText{Font: f.Regular, Text: d.Destination.Name, X: ColDestX, Y: y, W: ColStatusX - ColDestX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: string(d.Status), X: ColStatusX, Y: y, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15},
	)
	return els
}
```
**Note:** check `render.StaticText`'s actual field for right alignment — the Align constants are `AlignLeft`, `AlignCenter`, `AlignRight` (verify with `go doc ./internal/render Align` and read `scene.go::alignX`). If long destination text overflows its box, that is the existing StaticText behaviour — do not clip in this task.

- [ ] **Step 4: Generate goldens, eyeball, verify**

```bash
go test ./internal/board/ -run 'Golden' -update
go test ./internal/board/ -count=1 -v
```
Expected: PASS. Then convert one golden to a viewable size and *look at it* (e.g. `open internal/board/testdata/row_ontime.png`): order/time/platform/destination/status must sit in their columns like the reference board.

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/board/
git commit -m "feat(board): geometry constants, fonts, golden-tested departure row"
```

---

### Task 5: board — animated elements (offset, next-service scroll-in, remaining-services carousel)

The three board-local `render.Element` implementations that give the board its motion, all **deterministic pure functions of `tick`** so goldens can pin exact frames. Semantics from reference `elements.py` (`NextService`, `RemainingServices`) at 2px/tick.

**Files:**
- Create: `internal/board/elements.go`
- Test: `internal/board/elements_test.go`

**Interfaces:**
- Consumes: `rowElements`, geometry constants, `Fonts` (Task 4); `render.Framebuffer/Scene/Element/Clock`.
- Produces:
  - `func offsetElement(el render.Element, dx, dy, w, h int) render.Element` — renders `el` into a w×h scratch framebuffer, then copies every scratch pixel to `(dx, dy)` (overwrite, matching BlitAlpha semantics). Used to place `render.Clock` (which draws at y=0) at `(0, ClockY)`.
  - `func newNextServiceRow(d data.Departure, f *Fonts) render.Element` — row 1 pre-rendered to a 256×12 scratch; on tick t shows the top `b = min(RowH, 2*(t+1))` scratch rows at y = `RowH − b` (slide up from bottom edge; fully in at t ≥ 5, static thereafter).
  - `func newRemainingServices(deps []data.Departure, f *Fonts) render.Element` — nil/empty deps ⇒ renders nothing. Otherwise a scratch strip of height `(n+2)*RowH`: rows `i = 1..n` at `y = i*RowH` with ordinal `i+1`, plus a duplicate of `deps[0]` (ordinal 2) at `y = (n+1)*RowH` for seamless wrap; row 0 of the strip is blank. Per tick t the visible 12px window is:
    - `t < 6` (scroll-in): show strip rows `[0, 2*(t+1))` at panel y = `RemainingY + RowH − 2*(t+1)`.
    - `t ≥ 6`: let `t' = t−6`, `seg = rsPauseTicks + RowH/rsStep = 125+6 = 131`, `r = (t'/seg) mod n`, `w = t' mod seg`; window top in strip = `(r+1)*RowH + max(0, (w−rsPauseTicks+1)*2)` capped so the final move step lands exactly on the next row boundary; blit strip `[top, top+RowH)` at panel y = `RemainingY`.
  - Constants: `rsStep = 2` (px/tick), `rsPauseTicks = 125` (≈5s at 25fps), `nsStep = 2`.

- [ ] **Step 1: Write the failing tests**

`internal/board/elements_test.go`:
```go
package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func fbFor(t *testing.T, el render.Element, tick int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, H)
	el.Render(fb, tick, fixedNow)
	return fb
}

func TestOffsetElementPlacesClock(t *testing.T) {
	f := mustFonts(t)
	clock := offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH)
	rendertest.AssertGolden(t, "testdata", "el_clock_at_50", fbFor(t, clock, 0))
}

func TestNextServiceScrollIn(t *testing.T) {
	f := mustFonts(t)
	el := newNextServiceRow(fixtureBoard().Departures[0], f)
	// Mid scroll-in: t=2 → 6 rows visible at y=6.
	rendertest.AssertGolden(t, "testdata", "el_next_t2", fbFor(t, el, 2))
	// Fully in: identical frames at t=5 and t=500.
	at5 := fbFor(t, el, 5)
	at500 := fbFor(t, el, 500)
	if string(at5.Pix) != string(at500.Pix) {
		t.Fatal("next-service row must be static once fully scrolled in")
	}
	rendertest.AssertGolden(t, "testdata", "el_next_full", at5)
}

func TestNextServiceMidScrollShowsTopSliceAtBottom(t *testing.T) {
	f := mustFonts(t)
	el := newNextServiceRow(fixtureBoard().Departures[0], f)
	fb := fbFor(t, el, 0) // b=2 → scratch rows 0..1 at y=10..11
	for y := 0; y < 10; y++ {
		for x := 0; x < W; x++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("pixel (%d,%d) lit during first scroll tick", x, y)
			}
		}
	}
}

func TestRemainingServicesEmptyRendersNothing(t *testing.T) {
	f := mustFonts(t)
	fb := fbFor(t, newRemainingServices(nil, f), 100)
	for i, p := range fb.Pix {
		if p != 0 {
			t.Fatalf("pixel %d lit for empty remaining services", i)
		}
	}
}

func TestRemainingServicesHoldsRowDuringPause(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // ordinals 2..5
	el := newRemainingServices(deps, f)
	// After scroll-in (t=6) the window holds row 1 (first remaining, ordinal
	// "2nd") for rsPauseTicks. Frames at t=6 and t=6+124 must be identical.
	a := fbFor(t, el, 6)
	b := fbFor(t, el, 6+rsPauseTicks-1)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel must hold the row for the whole pause")
	}
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold2nd", a)
}

func TestRemainingServicesAdvancesToNextRow(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:]
	el := newRemainingServices(deps, f)
	// One full segment after the first hold: window shows ordinal "3rd".
	rendertest.AssertGolden(t, "testdata", "el_remaining_hold3rd", fbFor(t, el, 6+131))
}

func TestRemainingServicesWrapsSeamlessly(t *testing.T) {
	f := mustFonts(t)
	deps := fixtureBoard().Departures[1:] // n = 4
	el := newRemainingServices(deps, f)
	// Hold frame of cycle 0 row 0 must equal hold frame of cycle 1 row 0.
	a := fbFor(t, el, 6)
	b := fbFor(t, el, 6+4*131)
	if string(a.Pix) != string(b.Pix) {
		t.Fatal("carousel wrap must be seamless")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/board/ -v`
Expected: FAIL — `undefined: offsetElement`, `newNextServiceRow`, `newRemainingServices`, `rsPauseTicks`.

- [ ] **Step 3: Write the implementation**

`internal/board/elements.go`:
```go
package board

import (
	"time"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// Animation constants (ticks are 0.04s frames; reference parity).
const (
	nsStep       = 2   // next-service scroll-in px/tick
	rsStep       = 2   // remaining-services scroll px/tick
	rsPauseTicks = 125 // hold each row ~5s
	rsMoveTicks  = RowH / rsStep
	rsSegTicks   = rsPauseTicks + rsMoveTicks
)

// offset renders a child element into a scratch framebuffer and copies it to
// a fixed panel position. It lets position-less elements (render.Clock) and
// pre-rendered strips participate in absolute layout without touching render.
type offset struct {
	el      render.Element
	dx, dy  int
	scratch *render.Framebuffer
}

func offsetElement(el render.Element, dx, dy, w, h int) render.Element {
	return &offset{el: el, dx: dx, dy: dy, scratch: render.New(w, h)}
}

func (o *offset) Render(fb *render.Framebuffer, tick int, now time.Time) {
	o.scratch.Clear()
	o.el.Render(o.scratch, tick, now)
	copyRect(fb, o.scratch, 0, o.scratch.H, o.dx, o.dy)
}

// copyRect overwrites dst at (dx,dy) with src rows [srcY0, srcY1).
func copyRect(dst, src *render.Framebuffer, srcY0, srcY1, dx, dy int) {
	for y := srcY0; y < srcY1; y++ {
		ty := dy + y - srcY0
		if ty < 0 || ty >= dst.H {
			continue
		}
		for x := 0; x < src.W; x++ {
			tx := dx + x
			if tx < 0 || tx >= dst.W {
				continue
			}
			dst.SetPixel(tx, ty, src.At(x, y))
		}
	}
}

// prerender draws elements once into a fresh w×h framebuffer.
func prerender(els []render.Element, w, h int) *render.Framebuffer {
	fb := render.New(w, h)
	s := &render.Scene{Elements: els}
	s.Render(fb, 0, time.Time{})
	return fb
}

// nextServiceRow slides departure row 1 up from the bottom edge of its band
// (2px/tick, reference NextService), then holds it.
type nextServiceRow struct {
	strip *render.Framebuffer // 256×12 pre-rendered row
}

func newNextServiceRow(d data.Departure, f *Fonts) render.Element {
	return &nextServiceRow{strip: prerender(rowElements(d, 1, 0, f), W, RowH)}
}

func (n *nextServiceRow) Render(fb *render.Framebuffer, tick int, _ time.Time) {
	b := nsStep * (tick + 1)
	if b > RowH {
		b = RowH
	}
	copyRect(fb, n.strip, 0, b, 0, RowH-b)
}

// remainingServices vertically cycles rows 2..n (reference RemainingServices):
// scroll in, hold each row rsPauseTicks, scroll 12px to the next in
// rsMoveTicks, wrapping seamlessly via a duplicated first row.
type remainingServices struct {
	strip *render.Framebuffer
	n     int
}

func newRemainingServices(deps []data.Departure, f *Fonts) render.Element {
	if len(deps) == 0 {
		return &remainingServices{}
	}
	n := len(deps)
	var els []render.Element
	for i, d := range deps {
		els = append(els, rowElements(d, i+2, (i+1)*RowH, f)...)
	}
	els = append(els, rowElements(deps[0], 2, (n+1)*RowH, f)...)
	return &remainingServices{strip: prerender(els, W, (n+2)*RowH), n: n}
}

func (r *remainingServices) Render(fb *render.Framebuffer, tick int, _ time.Time) {
	if r.strip == nil {
		return
	}
	if tick < rsMoveTicks {
		// Scroll-in: strip rows [0,b) (blank row 0) at the band's bottom.
		b := rsStep * (tick + 1)
		copyRect(fb, r.strip, 0, b, 0, RemainingY+RowH-b)
		return
	}
	t := tick - rsMoveTicks
	seg := t / rsSegTicks
	w := t % rsSegTicks
	row := 1 + seg%r.n
	top := row * RowH
	if w >= rsPauseTicks {
		top += rsStep * (w - rsPauseTicks + 1)
	}
	copyRect(fb, r.strip, top, top+RowH, 0, RemainingY)
}
```

- [ ] **Step 4: Generate goldens, eyeball, verify**

```bash
go test ./internal/board/ -run 'ScrollIn|Remaining|Offset' -update
go test ./internal/board/ -count=1 -v
open internal/board/testdata/el_remaining_hold2nd.png
```
Expected: PASS; the hold frame shows the "2nd" departure row in the y=36 band; the clock golden shows HH:MM:SS at the bottom.

**Verify the wrap math once by hand before trusting the goldens:** with n=4, at `t = 6 + 4*131` the element must show the same pixels as `t = 6` (the test asserts it). If it doesn't, the duplicate-row/`seg mod n` logic is off — fix the code, not the test.

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/board/elements.go internal/board/elements_test.go internal/board/testdata/
git commit -m "feat(board): deterministic scroll-in row, remaining-services carousel, offset element"
```

---

### Task 6: board — DepartureBoard scene assembly

The primary live scene: next-service row + calling-at label/list + service-info line + remaining-services carousel + clock, with the text builders (`callingAtText`, `serviceInfoText`).

**Files:**
- Create: `internal/board/scene_departures.go`
- Test: `internal/board/scene_departures_test.go`

**Interfaces:**
- Consumes: Tasks 4–5 (`rowElements`, animated elements, `Fonts`, constants), `render.ScrollingText`, `render.Clock`, `data.Board`, `config.LayoutConfig`.
- Produces:
  - `func callingAtText(d data.Departure, showTimes bool) string` — `"A, B and C"` / `"A"` / `""`; with `showTimes`, each stop becomes `"Name (HH:MM)"` using the calling point's `ScheduledTime`.
  - `func serviceInfoText(d data.Departure) string` — `"{Operator} service"` + `" formed of N coaches"` (singular `"coach"` when N==1) when `Length > 0`.
  - `func departureBoardScene(b *data.Board, layout config.LayoutConfig, f *Fonts) *render.Scene` — elements in z-order: nextServiceRow(deps[0]); `ScrollingText` "Calling at:" at (0,12) w=CallingLabelW; `ScrollingText` calling-at list at (CallingListX,12) w=CallingListW; `ScrollingText` service info at (0,ServiceInfoY) w=W; remainingServices(deps[1:]) at RemainingY; clock via `offsetElement` at (0,ClockY) sized W×ClockH. All Level 15, Regular font (clock: BoldLarge+BoldTall).

- [ ] **Step 1: Write the failing tests**

`internal/board/scene_departures_test.go`:
```go
package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func TestCallingAtText(t *testing.T) {
	d := fixtureBoard().Departures[0] // Reading, Didcot Parkway, Swindon
	if got, want := callingAtText(d, false), "Reading, Didcot Parkway and Swindon"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := callingAtText(d, true), "Reading (11:00), Didcot Parkway (11:01) and Swindon (11:02)"; got != want {
		t.Errorf("with times: got %q, want %q", got, want)
	}
	one := fixtureBoard().Departures[3] // single stop: Reading
	if got, want := callingAtText(one, false), "Reading"; got != want {
		t.Errorf("single: got %q, want %q", got, want)
	}
	none := fixtureBoard().Departures[4] // no calling points
	if got := callingAtText(none, false); got != "" {
		t.Errorf("empty: got %q, want \"\"", got)
	}
}

func TestServiceInfoText(t *testing.T) {
	d := fixtureBoard().Departures[0]
	if got, want := serviceInfoText(d), "Great Western Railway service formed of 5 coaches"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	d.Length = 1
	if got, want := serviceInfoText(d), "Great Western Railway service formed of 1 coach"; got != want {
		t.Errorf("singular: got %q, want %q", got, want)
	}
	d.Length = 0
	if got, want := serviceInfoText(d), "Great Western Railway service"; got != want {
		t.Errorf("no length: got %q, want %q", got, want)
	}
}

func sceneFrame(t *testing.T, tick int) *render.Framebuffer {
	t.Helper()
	s := departureBoardScene(fixtureBoard(), config.Default().Layout, mustFonts(t))
	fb := render.New(W, H)
	// Render every tick up to the target so stateless elements are exercised
	// exactly as the runtime loop would at that tick.
	fb.Clear()
	s.Render(fb, tick, fixedNow)
	return fb
}

func TestDepartureBoardGoldenSettled(t *testing.T) {
	// t=200: next service fully in, carousel holding "2nd", scrolls mid-cycle.
	rendertest.AssertGolden(t, "testdata", "scene_departures_t200", sceneFrame(t, 200))
}

func TestDepartureBoardGoldenFirstTick(t *testing.T) {
	rendertest.AssertGolden(t, "testdata", "scene_departures_t0", sceneFrame(t, 0))
}

func TestDepartureBoardSingleServiceLeavesCarouselBlank(t *testing.T) {
	s := departureBoardScene(singleDepBoard(), config.Default().Layout, mustFonts(t))
	fb := render.New(W, H)
	s.Render(fb, 200, fixedNow)
	for y := RemainingY; y < RemainingY+RowH; y++ {
		for x := 0; x < W; x++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("carousel band pixel (%d,%d) lit with no remaining services", x, y)
			}
		}
	}
}
```

**Check before running:** `config.LayoutConfig`'s field for the times toggle — run `go doc ./internal/config LayoutConfig` and use its actual field name (M1B named it after the reference `settings.layout.times`; expect something like `Times bool`). Adjust `departureBoardScene`'s use accordingly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/board/ -v -run 'CallingAt|ServiceInfo|DepartureBoard'`
Expected: FAIL — `undefined: callingAtText` etc.

- [ ] **Step 3: Write the implementation**

`internal/board/scene_departures.go`:
```go
package board

import (
	"fmt"
	"strings"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// callingAtText joins calling points as "A, B and C", optionally suffixing
// each with its scheduled time (layout.times).
func callingAtText(d data.Departure, showTimes bool) string {
	if len(d.CallingPoints) == 0 {
		return ""
	}
	names := make([]string, len(d.CallingPoints))
	for i, cp := range d.CallingPoints {
		names[i] = cp.Location.Name
		if showTimes {
			names[i] += " (" + cp.ScheduledTime + ")"
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// serviceInfoText is the operator/coaches info line for the next service.
func serviceInfoText(d data.Departure) string {
	info := d.Operator + " service"
	if d.Length > 0 {
		plural := "es"
		if d.Length == 1 {
			plural = ""
		}
		info += fmt.Sprintf(" formed of %d coach%s", d.Length, plural)
	}
	return info
}

// departureBoardScene composes the primary live scene from a non-empty board.
func departureBoardScene(b *data.Board, layout config.LayoutConfig, f *Fonts) *render.Scene {
	first := b.Departures[0]
	els := []render.Element{
		newNextServiceRow(first, f),
		&render.ScrollingText{Font: f.Regular, Text: "Calling at:", X: 0, Y: RowH, W: CallingLabelW, H: RowH, Level: 15},
		&render.ScrollingText{Font: f.Regular, Text: callingAtText(first, layout.Times), X: CallingListX, Y: RowH, W: CallingListW, H: RowH, Level: 15},
		&render.ScrollingText{Font: f.Regular, Text: serviceInfoText(first), X: 0, Y: ServiceInfoY, W: W, H: RowH, Level: 15},
		newRemainingServices(b.Departures[1:], f),
		offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH),
	}
	return &render.Scene{Elements: els}
}
```

- [ ] **Step 4: Generate goldens, eyeball, verify**

```bash
go test ./internal/board/ -run 'DepartureBoardGolden' -update
go test ./internal/board/ -count=1
open internal/board/testdata/scene_departures_t200.png
```
Expected: PASS; the settled frame must read like the real board: row 1 at top, "Calling at:" line, info line, "2nd …" row at y=36, clock at the bottom. Compare against a photo of the reference board if in doubt.

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/board/scene_departures.go internal/board/scene_departures_test.go internal/board/testdata/
git commit -m "feat(board): departure-board scene with calling-at and service info lines"
```

---

### Task 7: board — auxiliary scenes (Initialising, NoServices, Error, ClockNotSynced, HotspotInfo)

The five non-departure scenes, plus the word-wrap helper and the NoServices message carousel (3-line pages, tick-driven). All golden-tested.

**Files:**
- Create: `internal/board/wordwrap.go`, `internal/board/scenes.go`
- Test: `internal/board/wordwrap_test.go`, `internal/board/scenes_test.go`

**Interfaces:**
- Consumes: Tasks 4–5; `obs.FaultCode` (Task 2); `render.StaticText`, `render.Clock`.
- Produces:
  - `func wordwrap(f *render.Font, width int, text string) []string` — greedy wrap on spaces using `f.Measure`; a single word wider than width gets its own line (never split mid-word).
  - `func initialisingScene(version string, f *Fonts) *render.Scene` — bold centered "Departure board is initialising" at (0,0); regular centered "Version: {version}" at (0,16); regular centered "Connecting..." at (0,28).
  - `func noServicesScene(b *data.Board, f *Fonts) *render.Scene` — bold centered title = `b.LocationName` at (0,0); body = tick-driven carousel element (see below); clock at ClockY.
  - Carousel semantics (`noServicesBody`, from reference `NoServices.update_message_carousel`): pages are each NRCC message word-wrapped to W, split into 3-line pages; the rotation is `default text (nsDefaultTicks=250) → page₀ (nsPageTicks=125) → page₁ (125) → … → default …`, pure function of tick. Default text: `"No services available at this time."`. Page lines are centered `StaticText`s at y offsets 12, 24, 36. No messages ⇒ default text always.
  - `func errorScene(fault obs.FaultCode, f *Fonts) *render.Scene` — bold centered "Unable to fetch departures" at (0,8); regular centered `fault.Message()` at (0,24); fault code bottom-right corner (regular, right-aligned in 40px at (ColStatusX, 52)); clock at ClockY **omitted** (layout collision with corner code — spec only requires fault message + code).
  - `func clockNotSyncedScene(f *Fonts) *render.Scene` — bold centered "Waiting for time sync..." at (0,20); fault code `E03` corner as above; **no clock** (clock is wrong by definition).
  - `func hotspotInfoScene(ssid, addr string, f *Fonts) *render.Scene` — bold centered "Setup mode" at (0,0); regular centered `"Join hotspot: " + ssid` at (0,16); regular centered `"Then open http://" + addr` at (0,28). Defined now, selected only by M3.
  - Constants: `nsDefaultTicks = 250`, `nsPageTicks = 125`.

- [ ] **Step 1: Write the failing tests**

`internal/board/wordwrap_test.go`:
```go
package board

import (
	"strings"
	"testing"
)

func TestWordwrapRespectsWidth(t *testing.T) {
	f := mustFonts(t)
	msg := fixtureBoard().Messages[0]
	lines := wordwrap(f.Regular, W, msg)
	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
	for _, ln := range lines {
		if w, _ := f.Regular.Measure(ln); w > W {
			t.Errorf("line %q measures %dpx > %d", ln, w, W)
		}
	}
	if joined := strings.Join(lines, " "); joined != msg {
		t.Errorf("wrap lost content:\n got %q\nwant %q", joined, msg)
	}
}

func TestWordwrapShortTextSingleLine(t *testing.T) {
	f := mustFonts(t)
	lines := wordwrap(f.Regular, W, "Short")
	if len(lines) != 1 || lines[0] != "Short" {
		t.Fatalf("got %q", lines)
	}
}

func TestWordwrapEmpty(t *testing.T) {
	f := mustFonts(t)
	if lines := wordwrap(f.Regular, W, ""); len(lines) != 0 {
		t.Fatalf("empty text must yield no lines, got %q", lines)
	}
}
```

`internal/board/scenes_test.go`:
```go
package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
	"github.com/mintopia/trainboard/internal/render/rendertest"
)

func frame(t *testing.T, s *render.Scene, tick int) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, H)
	s.Render(fb, tick, fixedNow)
	return fb
}

func TestInitialisingGolden(t *testing.T) {
	s := initialisingScene("v1.2.3", mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_initialising", frame(t, s, 0))
}

func TestNoServicesGoldenDefaultText(t *testing.T) {
	s := noServicesScene(emptyBoard(), mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_noservices_default", frame(t, s, 0))
}

func TestNoServicesCarouselShowsMessagePage(t *testing.T) {
	f := mustFonts(t)
	s := noServicesScene(emptyBoard(), f)
	// During the first page window (tick just past nsDefaultTicks).
	rendertest.AssertGolden(t, "testdata", "scene_noservices_page0", frame(t, s, nsDefaultTicks+1))
	// Default and page frames must differ.
	if string(frame(t, s, 0).Pix) == string(frame(t, s, nsDefaultTicks+1).Pix) {
		t.Fatal("carousel page must differ from default text")
	}
}

func TestNoServicesCarouselCyclesBackToDefault(t *testing.T) {
	f := mustFonts(t)
	b := emptyBoard() // one message => pages = wordwrap pages of that message
	s := noServicesScene(b, f)
	pages := len(splitPages(wordwrap(f.Regular, W, b.Messages[0])))
	cycle := nsDefaultTicks + pages*nsPageTicks
	if string(frame(t, s, 0).Pix) != string(frame(t, s, cycle).Pix) {
		t.Fatal("carousel must return to default text after all pages")
	}
}

func TestNoServicesNoMessagesAlwaysDefault(t *testing.T) {
	b := emptyBoard()
	b.Messages = nil
	s := noServicesScene(b, mustFonts(t))
	if string(frame(t, s, 0).Pix) != string(frame(t, s, 5000).Pix) {
		t.Fatal("without messages the body must be static")
	}
}

func TestErrorSceneGolden(t *testing.T) {
	s := errorScene(obs.FaultDarwinUnreachable, mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_error_e01", frame(t, s, 0))
}

func TestClockNotSyncedGolden(t *testing.T) {
	s := clockNotSyncedScene(mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_clocknotsynced", frame(t, s, 0))
}

func TestHotspotInfoGolden(t *testing.T) {
	s := hotspotInfoScene("trainboard-setup", "192.168.4.1", mustFonts(t))
	rendertest.AssertGolden(t, "testdata", "scene_hotspot", frame(t, s, 0))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/board/ -v -run 'Wordwrap|Initialising|NoServices|ErrorScene|ClockNotSynced|Hotspot'`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write the implementation**

`internal/board/wordwrap.go`:
```go
package board

import (
	"strings"

	"github.com/mintopia/trainboard/internal/render"
)

// wordwrap greedily wraps text on spaces so each line measures at most
// width px. A single word wider than width gets its own (overflowing) line.
func wordwrap(f *render.Font, width int, text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		candidate := cur + " " + w
		if cw, _ := f.Measure(candidate); cw <= width {
			cur = candidate
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	return append(lines, cur)
}

// splitPages groups wrapped lines into 3-line carousel pages (reference
// NoServices.update_messages).
func splitPages(lines []string) [][]string {
	var pages [][]string
	for len(lines) > 3 {
		pages = append(pages, lines[:3])
		lines = lines[3:]
	}
	if len(lines) > 0 {
		pages = append(pages, lines)
	}
	return pages
}
```

`internal/board/scenes.go`:
```go
package board

import (
	"time"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
)

// NoServices carousel timing (ticks): the default text holds ~10s, each
// message page ~5s (reference settings.messages.{frequency,interval}).
const (
	nsDefaultTicks = 250
	nsPageTicks    = 125
)

const noServicesText = "No services available at this time."

func centered(f *render.Font, text string, y int) render.Element {
	return &render.StaticText{Font: f, Text: text, X: 0, Y: y, W: W, H: RowH, Align: render.AlignCenter, Level: 15}
}

// initialisingScene is the pre-first-data boot screen.
func initialisingScene(version string, f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Departure board is initialising", 0),
		centered(f.Regular, "Version: "+version, 16),
		centered(f.Regular, "Connecting...", 28),
	}}
}

// noServicesBody carousels the default text and 3-line NRCC message pages.
type noServicesBody struct {
	font  *render.Font
	pages [][]string
}

func (nb *noServicesBody) Render(fb *render.Framebuffer, tick int, now time.Time) {
	lines := []string{noServicesText}
	if len(nb.pages) > 0 {
		cycle := nsDefaultTicks + len(nb.pages)*nsPageTicks
		phase := tick % cycle
		if phase >= nsDefaultTicks {
			lines = nb.pages[(phase-nsDefaultTicks)/nsPageTicks]
		}
	}
	for i, ln := range lines {
		centered(nb.font, ln, RowH*(i+1)).Render(fb, tick, now)
	}
}

// noServicesScene shows the station title plus the NRCC message carousel.
func noServicesScene(b *data.Board, f *Fonts) *render.Scene {
	var pages [][]string
	for _, msg := range b.Messages {
		pages = append(pages, splitPages(wordwrap(f.Regular, W, msg))...)
	}
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, b.LocationName, 0),
		&noServicesBody{font: f.Regular, pages: pages},
		offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH),
	}}
}

func faultCorner(fault obs.FaultCode, f *Fonts) render.Element {
	return &render.StaticText{Font: f.Regular, Text: string(fault), X: ColStatusX, Y: 52, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15}
}

// errorScene is shown on hard fetch failure after the stale grace expires.
func errorScene(fault obs.FaultCode, f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Unable to fetch departures", 8),
		centered(f.Regular, fault.Message(), 24),
		faultCorner(fault, f),
	}}
}

// clockNotSyncedScene is the pre-NTP transient; deliberately clockless.
func clockNotSyncedScene(f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Waiting for time sync...", 20),
		faultCorner(obs.FaultClockNotSynced, f),
	}}
}

// hotspotInfoScene is defined for the scene contract; M3 drives it.
func hotspotInfoScene(ssid, addr string, f *Fonts) *render.Scene {
	return &render.Scene{Elements: []render.Element{
		centered(f.Bold, "Setup mode", 0),
		centered(f.Regular, "Join hotspot: "+ssid, 16),
		centered(f.Regular, "Then open http://"+addr, 28),
	}}
}
```

- [ ] **Step 4: Generate goldens, eyeball, verify**

```bash
go test ./internal/board/ -run 'Golden|CarouselShows' -update
go test ./internal/board/ -count=1
open internal/board/testdata/scene_noservices_page0.png internal/board/testdata/scene_error_e01.png
```
Expected: PASS; the page frame shows 3 wrapped message lines under the "Paddington" title; the error frame shows the message with `E01` bottom-right.

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/board/wordwrap.go internal/board/wordwrap_test.go internal/board/scenes.go internal/board/scenes_test.go internal/board/testdata/
git commit -m "feat(board): initialising/noservices/error/clocknotsynced/hotspot scenes + NRCC carousel"
```

---

### Task 8: board — Snapshot, State, and priority scene selection

The immutable snapshot type the runtime publishes, and the single entry point that turns a snapshot into the scene to draw, enforcing the spec's priority order.

**Files:**
- Create: `internal/board/snapshot.go`
- Test: `internal/board/snapshot_test.go`

**Interfaces:**
- Consumes: all scene constructors (Tasks 6–7), `data.Board`, `config.LayoutConfig`, `obs.FaultCode`.
- Produces:
  - `type State int` with `const (StateInitialising State = iota; StateDepartures; StateNoServices; StateError; StateClockNotSynced)` and `func (s State) String() string` (`"initialising"`, `"departures"`, `"no-services"`, `"error"`, `"clock-not-synced"`).
  - `type Hotspot struct { SSID, Addr string }`
  - `type Snapshot struct { Board *data.Board; State State; Fault obs.FaultCode; FetchedAt time.Time; Hotspot *Hotspot }` — treat as immutable once published.
  - `func BuildScene(s *Snapshot, layout config.LayoutConfig, version string, f *Fonts) *render.Scene` — priority: nil snapshot or `StateInitialising` → initialising; `s.Hotspot != nil` → hotspot (overrides everything; M3's hook); `StateError` → error(s.Fault); `StateClockNotSynced` → clockNotSynced; `StateNoServices` → noServices(s.Board); `StateDepartures` → departureBoard(s.Board). A `StateDepartures` snapshot with a nil/empty board falls back to the error scene with `FaultDarwinUnreachable` (defensive; must not panic).

- [ ] **Step 1: Write the failing test**

`internal/board/snapshot_test.go`:
```go
package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
)

func build(t *testing.T, s *Snapshot) *render.Framebuffer {
	t.Helper()
	scene := BuildScene(s, config.Default().Layout, "v1", mustFonts(t))
	fb := render.New(W, H)
	scene.Render(fb, 0, fixedNow)
	return fb
}

func pixEq(a, b *render.Framebuffer) bool { return string(a.Pix) == string(b.Pix) }

func TestBuildSceneNilIsInitialising(t *testing.T) {
	want := render.New(W, H)
	initialisingScene("v1", mustFonts(t)).Render(want, 0, fixedNow)
	if !pixEq(build(t, nil), want) {
		t.Fatal("nil snapshot must render the initialising scene")
	}
}

func TestBuildScenePerState(t *testing.T) {
	f := mustFonts(t)
	b := fixtureBoard()
	cases := []struct {
		name string
		snap *Snapshot
		want *render.Scene
	}{
		{"departures", &Snapshot{State: StateDepartures, Board: b}, departureBoardScene(b, config.Default().Layout, f)},
		{"noservices", &Snapshot{State: StateNoServices, Board: emptyBoard()}, noServicesScene(emptyBoard(), f)},
		{"error", &Snapshot{State: StateError, Fault: obs.FaultAuthRejected}, errorScene(obs.FaultAuthRejected, f)},
		{"clock", &Snapshot{State: StateClockNotSynced, Fault: obs.FaultClockNotSynced}, clockNotSyncedScene(f)},
		{"initialising", &Snapshot{State: StateInitialising}, initialisingScene("v1", f)},
	}
	for _, tc := range cases {
		want := render.New(W, H)
		tc.want.Render(want, 0, fixedNow)
		if !pixEq(build(t, tc.snap), want) {
			t.Errorf("%s: BuildScene rendered the wrong scene", tc.name)
		}
	}
}

func TestHotspotOverridesEverything(t *testing.T) {
	f := mustFonts(t)
	hs := &Hotspot{SSID: "trainboard-setup", Addr: "192.168.4.1"}
	want := render.New(W, H)
	hotspotInfoScene(hs.SSID, hs.Addr, f).Render(want, 0, fixedNow)
	for _, st := range []State{StateDepartures, StateError, StateClockNotSynced, StateNoServices} {
		snap := &Snapshot{State: st, Board: fixtureBoard(), Hotspot: hs, Fault: obs.FaultDarwinUnreachable}
		if !pixEq(build(t, snap), want) {
			t.Errorf("state %v: hotspot must take priority", st)
		}
	}
}

func TestDeparturesWithEmptyBoardFallsBackSafely(t *testing.T) {
	// Must not panic; renders the error scene defensively.
	f := mustFonts(t)
	want := render.New(W, H)
	errorScene(obs.FaultDarwinUnreachable, f).Render(want, 0, fixedNow)
	if !pixEq(build(t, &Snapshot{State: StateDepartures, Board: emptyBoard()}), want) {
		t.Fatal("empty departures board must fall back to error scene")
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{StateInitialising: "initialising", StateDepartures: "departures", StateNoServices: "no-services", StateError: "error", StateClockNotSynced: "clock-not-synced"}
	for st, want := range cases {
		if st.String() != want {
			t.Errorf("%d.String() = %q, want %q", st, st.String(), want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/board/ -v -run 'BuildScene|Hotspot|StateString|FallsBack'`
Expected: FAIL — `undefined: Snapshot`, `BuildScene`, `State…`.

- [ ] **Step 3: Write the implementation**

`internal/board/snapshot.go`:
```go
package board

import (
	"time"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
)

// State classifies what the board should show (spec §Fetch-result
// classification). It is computed by the runtime, never by board.
type State int

// States in ascending scene priority within the non-hotspot group.
const (
	StateInitialising State = iota
	StateDepartures
	StateNoServices
	StateError
	StateClockNotSynced
)

func (s State) String() string {
	switch s {
	case StateDepartures:
		return "departures"
	case StateNoServices:
		return "no-services"
	case StateError:
		return "error"
	case StateClockNotSynced:
		return "clock-not-synced"
	default:
		return "initialising"
	}
}

// Hotspot carries AP-mode identity; populated only by M3's connectivity
// manager. Non-nil Hotspot outranks every state.
type Hotspot struct {
	SSID, Addr string
}

// Snapshot is the immutable unit the poller publishes and the render loop
// consumes. Never mutate a published snapshot or anything reachable from it.
type Snapshot struct {
	Board     *data.Board
	State     State
	Fault     obs.FaultCode
	FetchedAt time.Time
	Hotspot   *Hotspot
}

// BuildScene maps a snapshot to the scene to draw, enforcing the priority
// HotspotInfo > Error > ClockNotSynced > NoServices/DepartureBoard, with
// Initialising as the pre-first-data default.
func BuildScene(s *Snapshot, layout config.LayoutConfig, version string, f *Fonts) *render.Scene {
	if s == nil || s.State == StateInitialising {
		if s != nil && s.Hotspot != nil {
			return hotspotInfoScene(s.Hotspot.SSID, s.Hotspot.Addr, f)
		}
		return initialisingScene(version, f)
	}
	if s.Hotspot != nil {
		return hotspotInfoScene(s.Hotspot.SSID, s.Hotspot.Addr, f)
	}
	switch s.State {
	case StateError:
		return errorScene(s.Fault, f)
	case StateClockNotSynced:
		return clockNotSyncedScene(f)
	case StateNoServices:
		return noServicesScene(s.Board, f)
	default: // StateDepartures
		if s.Board == nil || len(s.Board.Departures) == 0 {
			return errorScene(obs.FaultDarwinUnreachable, f)
		}
		return departureBoardScene(s.Board, layout, f)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/board/ -count=1 -race`
Expected: PASS (whole package).

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/board/snapshot.go internal/board/snapshot_test.go
git commit -m "feat(board): snapshot type + priority scene selection (hotspot > error > clock > data)"
```

---

### Task 9: runtime — fetch-result classification

The pure function implementing the spec's classification table, including x509 time-validity detection and the 5-minute stale grace. No goroutines yet.

**Files:**
- Create: `internal/runtime/classify.go` (+ package doc)
- Test: `internal/runtime/classify_test.go`

**Interfaces:**
- Consumes: `board.Snapshot/State`, `obs.FaultCode`, `data.Board`.
- Produces:
  - `const StaleGrace = 5 * time.Minute`
  - `func Classify(b *data.Board, fetchErr error, prev *board.Snapshot, now time.Time) *board.Snapshot` — the table:
    | input | result |
    |---|---|
    | `fetchErr == nil`, ≥1 departure | `StateDepartures`, Board=b, FetchedAt=now, FaultNone |
    | `fetchErr == nil`, 0 departures | `StateNoServices`, Board=b, FetchedAt=now, FaultNone |
    | x509 time-validity error | `StateClockNotSynced`, Fault `E03`; carries prev's Board+FetchedAt if any |
    | other error, prev holds good data (`StateDepartures`/`StateNoServices`) fetched < 5 min ago | **returns prev unchanged** (stale grace) |
    | other error, otherwise (incl. never-succeeded, ≥5 min stale, prev==nil) | `StateError`, Fault: `E02` if auth-rejected else `E01`; carries prev's Board+FetchedAt if any |
  - `func isClockError(err error) bool` — true for `x509.CertificateInvalidError` with `Reason == x509.Expired` (covers "not yet valid": Go uses the same reason) anywhere in the chain (`errors.As`), or an error string containing `"x509: certificate has expired or is not yet valid"` (belt for wrapped-by-message cases).
  - `func isAuthError(err error) bool` — error string containing `"token"` or `"unauthor"` (case-insensitive; Darwin's SOAP fault text for a bad token says "Invalid Access Token", surfaced by data's fault detection).

- [ ] **Step 1: Write the failing test**

`internal/runtime/classify_test.go`:
```go
package runtime

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

var t0 = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

func goodBoard(n int) *data.Board {
	b := &data.Board{LocationName: "Paddington", CRS: "PAD"}
	for i := 0; i < n; i++ {
		b.Departures = append(b.Departures, data.Departure{ScheduledTime: fmt.Sprintf("12:%02d", i)})
	}
	return b
}

func TestClassifySuccessWithDepartures(t *testing.T) {
	s := Classify(goodBoard(3), nil, nil, t0)
	if s.State != board.StateDepartures || s.Fault != obs.FaultNone || !s.FetchedAt.Equal(t0) || len(s.Board.Departures) != 3 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifySuccessEmptyIsNoServices(t *testing.T) {
	s := Classify(goodBoard(0), nil, nil, t0)
	if s.State != board.StateNoServices || s.Board == nil {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyNeverSucceededErrorIsError(t *testing.T) {
	s := Classify(nil, errors.New("dial tcp: connection refused"), nil, t0)
	if s.State != board.StateError || s.Fault != obs.FaultDarwinUnreachable {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyAuthErrorIsE02(t *testing.T) {
	s := Classify(nil, errors.New(`soap fault: "Invalid Access Token"`), nil, t0)
	if s.State != board.StateError || s.Fault != obs.FaultAuthRejected {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestClassifyStaleGraceKeepsPrevSnapshot(t *testing.T) {
	prev := Classify(goodBoard(2), nil, nil, t0)
	s := Classify(nil, errors.New("timeout"), prev, t0.Add(StaleGrace-time.Second))
	if s != prev {
		t.Fatalf("inside grace the previous snapshot must be returned unchanged; got %+v", s)
	}
}

func TestClassifyGraceExpiredIsError(t *testing.T) {
	prev := Classify(goodBoard(2), nil, nil, t0)
	s := Classify(nil, errors.New("timeout"), prev, t0.Add(StaleGrace))
	if s.State != board.StateError || s.Fault != obs.FaultDarwinUnreachable {
		t.Fatalf("at the 5-minute edge the state must become Error; got %+v", s)
	}
	if s.Board == nil || len(s.Board.Departures) != 2 {
		t.Fatal("error snapshot should still carry the last good board")
	}
}

func TestClassifyGraceDoesNotApplyAfterErrorState(t *testing.T) {
	prevErr := &board.Snapshot{State: board.StateError, Fault: obs.FaultDarwinUnreachable, FetchedAt: t0}
	s := Classify(nil, errors.New("timeout"), prevErr, t0.Add(time.Second))
	if s.State != board.StateError {
		t.Fatalf("grace applies only to good-data snapshots; got %+v", s)
	}
}

func TestClassifyX509IsClockNotSynced(t *testing.T) {
	certErr := x509.CertificateInvalidError{Reason: x509.Expired}
	wrapped := &url.Error{Op: "Post", URL: "https://lite.realtime.nationalrail.co.uk", Err: fmt.Errorf("tls: %w", certErr)}
	prev := Classify(goodBoard(1), nil, nil, t0)
	s := Classify(nil, wrapped, prev, t0.Add(time.Hour))
	if s.State != board.StateClockNotSynced || s.Fault != obs.FaultClockNotSynced {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Board == nil {
		t.Fatal("clock-not-synced must carry the previous board")
	}
}

func TestClassifyX509ByMessage(t *testing.T) {
	err := errors.New("Post \"https://…\": x509: certificate has expired or is not yet valid: current time 1970-01-01T00:00:10Z is before 2025-01-01")
	s := Classify(nil, err, nil, t0)
	if s.State != board.StateClockNotSynced {
		t.Fatalf("snapshot = %+v", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -v`
Expected: FAIL — `undefined: Classify`, `StaleGrace`.

- [ ] **Step 3: Write the implementation**

`internal/runtime/classify.go`:
```go
// Package runtime owns time and concurrency for the board: the Darwin
// poller publishing immutable snapshots through an atomic pointer, the
// fixed-rate render loop consuming them lock-free, and the classification
// of fetch results into on-screen states. board stays pure; data does the
// fetching; runtime is where they meet.
package runtime

import (
	"crypto/x509"
	"errors"
	"strings"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

// StaleGrace is how long the last good board keeps showing through fetch
// failures before the Error scene takes over (ADR 0003 staleness window).
const StaleGrace = 5 * time.Minute

// Classify maps a fetch result onto the next snapshot per the M1C design's
// classification table. prev is the currently published snapshot (nil before
// the first fetch resolves). Inside the stale grace it returns prev itself,
// so the render loop sees an unchanged frame.
func Classify(b *data.Board, fetchErr error, prev *board.Snapshot, now time.Time) *board.Snapshot {
	if fetchErr == nil {
		st := board.StateDepartures
		if len(b.Departures) == 0 {
			st = board.StateNoServices
		}
		return &board.Snapshot{Board: b, State: st, FetchedAt: now}
	}

	if isClockError(fetchErr) {
		s := &board.Snapshot{State: board.StateClockNotSynced, Fault: obs.FaultClockNotSynced}
		if prev != nil {
			s.Board, s.FetchedAt = prev.Board, prev.FetchedAt
		}
		return s
	}

	if prev != nil &&
		(prev.State == board.StateDepartures || prev.State == board.StateNoServices) &&
		now.Sub(prev.FetchedAt) < StaleGrace {
		return prev
	}

	fault := obs.FaultDarwinUnreachable
	if isAuthError(fetchErr) {
		fault = obs.FaultAuthRejected
	}
	s := &board.Snapshot{State: board.StateError, Fault: fault}
	if prev != nil {
		s.Board, s.FetchedAt = prev.Board, prev.FetchedAt
	}
	return s
}

// isClockError reports whether err is the pre-NTP x509 time-validity
// failure. It must never match generic transport/DNS errors: this state is
// excluded from M3's AP-fallback trigger.
func isClockError(err error) bool {
	var cie x509.CertificateInvalidError
	if errors.As(err, &cie) && cie.Reason == x509.Expired {
		return true
	}
	return strings.Contains(err.Error(), "x509: certificate has expired or is not yet valid")
}

// isAuthError reports whether the fetch failed on credentials rather than
// connectivity (Darwin's fault text for a bad token).
func isAuthError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "token") || strings.Contains(msg, "unauthor")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -count=1 -race -v`
Expected: PASS (all 10 tests).

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/
git commit -m "feat(runtime): fetch-result classification with stale grace + x509 clock detection"
```

---

### Task 10: runtime — poller with atomic snapshot publication

The goroutine that fetches on the config interval, classifies, and publishes via `atomic.Pointer`. Fetcher and clock injected for tests; state transitions logged (which also lands them in the obs ring via the Task 2 tee).

**Files:**
- Create: `internal/runtime/poller.go`
- Test: `internal/runtime/poller_test.go`

**Interfaces:**
- Consumes: `Classify` (Task 9), `data.Client/Request/Filter/Board`, `config.Config`, `board.Snapshot`, `*slog.Logger`.
- Produces:
  - `type Fetcher interface { Fetch(ctx context.Context, r data.Request) (*data.Board, error) }` — satisfied by `*data.Client`.
  - `func NewPoller(f Fetcher, cfg config.Config, log *slog.Logger) *Poller` — derives from cfg: `data.Request{OriginCRS: cfg.Board.Origin, DestinationCRS: cfg.Board.Destination, NumRows: 10, TimeWindowMinutes: cfg.Board.TimeWindowMinutes}` and `data.Filter{Platforms: cfg.Board.Platforms, TOCs: cfg.Board.TOCs, MaxServices: cfg.Board.Services, CutoffHours: cfg.Board.CutoffHours, Replacements: cfg.Board.Replacements}`.
  - `func (p *Poller) Snapshot() *board.Snapshot` — lock-free `atomic.Pointer.Load`; nil before first poll completes.
  - `func (p *Poller) Run(ctx context.Context)` — immediate first poll, then every `cfg.Board.RefreshSeconds` seconds until ctx cancels. Each poll: `fetchTimeout = 30s` child context → Fetch → `Filter.Apply` → `Classify` → publish → log (`Info` success with departure count; `Warn` failure with err + resulting state; `Info` state transitions `from`→`to`).
  - Test seams (unexported fields set by tests): `now func() time.Time`, `pollDone chan<- struct{}` (optional notification after each publish).

- [ ] **Step 1: Write the failing test**

`internal/runtime/poller_test.go`:
```go
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/obs"
)

// scriptFetcher returns each result in sequence, then repeats the last.
type scriptFetcher struct {
	mu      sync.Mutex
	results []fetchResult
	i       int
	lastReq data.Request
}

type fetchResult struct {
	b   *data.Board
	err error
}

func (s *scriptFetcher) Fetch(_ context.Context, r data.Request) (*data.Board, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = r
	res := s.results[s.i]
	if s.i < len(s.results)-1 {
		s.i++
	}
	return res.b, res.err
}

func testCfg() config.Config {
	cfg := config.Default()
	cfg.Board.Origin = "PAD"
	cfg.Board.Services = 3
	cfg.Board.RefreshSeconds = 15 // min valid; tests drive polls manually anyway
	return cfg
}

func newTestPoller(f Fetcher) (*Poller, *obs.Ring) {
	ring := obs.NewRing(64)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	return NewPoller(f, testCfg(), log), ring
}

func TestPollOncePublishesSnapshot(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(5)}}}
	p, _ := newTestPoller(f)
	if p.Snapshot() != nil {
		t.Fatal("snapshot must be nil before first poll")
	}
	p.pollOnce(context.Background())
	s := p.Snapshot()
	if s == nil || s.State != board.StateDepartures {
		t.Fatalf("snapshot = %+v", s)
	}
	// Filter applied: Services=3 caps 5 departures to 3.
	if len(s.Board.Departures) != 3 {
		t.Fatalf("MaxServices filter not applied: %d departures", len(s.Board.Departures))
	}
	// Request derived from config with NumRows pinned to 10.
	if f.lastReq.OriginCRS != "PAD" || f.lastReq.NumRows != 10 {
		t.Fatalf("request = %+v", f.lastReq)
	}
}

func TestPollOnceStateTransitionLogged(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(2)}, {err: errors.New("boom")}}}
	p, ring := newTestPoller(f)
	p.now = func() time.Time { return t0 }
	p.pollOnce(context.Background())
	p.now = func() time.Time { return t0.Add(StaleGrace + time.Minute) }
	p.pollOnce(context.Background())
	if s := p.Snapshot(); s.State != board.StateError {
		t.Fatalf("state = %v, want error", s.State)
	}
	var transition bool
	for _, e := range ring.Events() {
		if e.Msg == "state transition" && e.Attrs["from"] == "departures" && e.Attrs["to"] == "error" {
			transition = true
		}
	}
	if !transition {
		t.Fatalf("missing state-transition event; ring = %+v", ring.Events())
	}
}

func TestPollOnceStaleGraceKeepsSnapshotIdentity(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(2)}, {err: errors.New("boom")}}}
	p, _ := newTestPoller(f)
	p.now = func() time.Time { return t0 }
	p.pollOnce(context.Background())
	first := p.Snapshot()
	p.now = func() time.Time { return t0.Add(time.Minute) }
	p.pollOnce(context.Background())
	if p.Snapshot() != first {
		t.Fatal("inside grace the identical snapshot pointer must stay published")
	}
}

func TestRunPollsUntilCancelled(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(1)}}}
	p, _ := newTestPoller(f)
	done := make(chan struct{}, 8)
	p.pollDone = done
	ctx, cancel := context.WithCancel(context.Background())
	go p.Run(ctx)
	<-done // first immediate poll
	cancel()
	if p.Snapshot() == nil {
		t.Fatal("Run must publish after its immediate first poll")
	}
}
```
(Add `"sync"` to the imports — `scriptFetcher` uses a mutex because `Run` polls from a goroutine.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -v -run Poll`
Expected: FAIL — `undefined: NewPoller`.

- [ ] **Step 3: Write the implementation**

`internal/runtime/poller.go`:
```go
package runtime

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
)

// fetchTimeout bounds one Darwin round-trip; well under the minimum
// 15-second refresh interval so polls never overlap.
const fetchTimeout = 30 * time.Second

// Fetcher is the data-client seam; *data.Client satisfies it.
type Fetcher interface {
	Fetch(ctx context.Context, r data.Request) (*data.Board, error)
}

// Poller fetches departures on the configured interval and publishes
// immutable snapshots through an atomic pointer. It never blocks the render
// loop and the render loop never blocks it.
type Poller struct {
	fetcher  Fetcher
	req      data.Request
	filter   data.Filter
	interval time.Duration
	log      *slog.Logger
	snap     atomic.Pointer[board.Snapshot]

	// test seams
	now      func() time.Time
	pollDone chan<- struct{}
}

// NewPoller derives the Darwin request and client-side filter from cfg.
// NumRows stays pinned at 10 (the LDBWS WithDetails cap): display trimming
// happens in the filter via cfg.Board.Services.
func NewPoller(f Fetcher, cfg config.Config, log *slog.Logger) *Poller {
	return &Poller{
		fetcher: f,
		req: data.Request{
			OriginCRS:         cfg.Board.Origin,
			DestinationCRS:    cfg.Board.Destination,
			NumRows:           10,
			TimeWindowMinutes: cfg.Board.TimeWindowMinutes,
		},
		filter: data.Filter{
			Platforms:    cfg.Board.Platforms,
			TOCs:         cfg.Board.TOCs,
			MaxServices:  cfg.Board.Services,
			CutoffHours:  cfg.Board.CutoffHours,
			Replacements: cfg.Board.Replacements,
		},
		interval: time.Duration(cfg.Board.RefreshSeconds) * time.Second,
		log:      log,
		now:      time.Now,
	}
}

// Snapshot returns the currently published snapshot (nil before the first
// poll completes). Lock-free.
func (p *Poller) Snapshot() *board.Snapshot {
	return p.snap.Load()
}

// Run polls immediately, then on every interval tick until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	p.pollOnce(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Poller) pollOnce(ctx context.Context) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	b, err := p.fetcher.Fetch(fctx, p.req)
	if err == nil {
		b = p.filter.Apply(b)
	}
	prev := p.snap.Load()
	next := Classify(b, err, prev, p.now())
	p.snap.Store(next)

	switch {
	case err != nil:
		p.log.Warn("fetch failed", "err", err.Error(), "state", next.State.String())
	default:
		p.log.Info("fetched", "departures", len(next.Board.Departures), "state", next.State.String())
	}
	if prev != nil && prev.State != next.State {
		p.log.Info("state transition", "from", prev.State.String(), "to", next.State.String())
	} else if prev == nil {
		p.log.Info("state transition", "from", "initialising", "to", next.State.String())
	}
	if p.pollDone != nil {
		select {
		case p.pollDone <- struct{}{}:
		default:
		}
	}
}
```
**Check the actual `data.Filter.Apply` signature** (`go doc ./internal/data Filter`): it is `func (f Filter) Apply(b *Board) *Board`. If the method name differs, follow the code.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runtime/ -count=1 -race -v`
Expected: PASS.

- [ ] **Step 5: Run the full gate**

Run: `make check`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/poller.go internal/runtime/poller_test.go
git commit -m "feat(runtime): interval poller publishing immutable snapshots via atomic pointer"
```

---

### Task 11: runtime — render loop with brightness-on-change; -race becomes the gate

The fixed-25fps loop: lock-free snapshot read → scene (rebuilt only when the snapshot pointer changes, tick reset to 0) → render → full-frame flush; SSD1322 contrast issued only when `BrightnessAt` changes. Also: `make test` and CI gain `-race` permanently.

**Files:**
- Create: `internal/runtime/loop.go`
- Test: `internal/runtime/loop_test.go`
- Modify: `Makefile` (test target → `go test -race ./...`)
- Modify: `.github/workflows/ci.yml` (test step → `go test -race ./... -count=1`)

**Interfaces:**
- Consumes: `board.BuildScene/Snapshot/Fonts/W/H`, `config.Config` (`BrightnessAt`, `Layout`), `render.New/Framebuffer`, Task 10's `Poller.Snapshot` (as a `func() *board.Snapshot`).
- Produces:
  - `type Flusher interface { Flush(packed []byte) error; SetContrast(level byte) error }` — `*display.SSD1322` satisfies it; the preview transport (Task 12) implements it.
  - `const TickInterval = 40 * time.Millisecond`
  - `func NewLoop(src func() *board.Snapshot, fl Flusher, cfg config.Config, fonts *board.Fonts, version string, log *slog.Logger) *Loop`
  - `func (l *Loop) Run(ctx context.Context) error` — ticker at TickInterval; calls `step` each tick; returns the first flush error (a dead panel is fatal; systemd restarts) or nil on ctx cancel.
  - `func (l *Loop) step(now time.Time) error` — exported-for-test via lowercase + same package tests: (1) `src()`; if pointer differs from last, rebuild scene via `board.BuildScene`, reset tick to 0, log the swap at Debug; (2) `b := cfg.BrightnessAt(now)`; if `b != lastBrightness` → `SetContrast(byte(b))`, remember; (3) clear fb, `scene.Render(fb, tick, now)`, `Flush(fb.Pack())`, measuring both durations with `time.Since`; (4) tick++. First-ever flush logs `Info "first frame"` (boot-timing event for #29's future measurement). **Frame-timing observability (spec §Observability):** every `timingEveryTicks = 375` ticks (~15s) log `Info "frame timing"` with `render_us`/`flush_us` of the last frame — the tee lands it in the ring without flooding it (256-cap ring at 25fps would evict everything in ~10s if logged per tick).
  - `const timingEveryTicks = 375`

- [ ] **Step 1: Write the failing test**

`internal/runtime/loop_test.go`:
```go
package runtime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
)

type fakeFlusher struct {
	mu        sync.Mutex
	flushes   int
	lastFrame []byte
	contrasts []byte
}

func (f *fakeFlusher) Flush(p []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
	f.lastFrame = append([]byte(nil), p...)
	return nil
}

func (f *fakeFlusher) SetContrast(l byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contrasts = append(f.contrasts, l)
	return nil
}

func (f *fakeFlusher) stats() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes, len(f.contrasts)
}

func mustBoardFonts(t *testing.T) *board.Fonts {
	t.Helper()
	f, err := board.LoadFonts()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func powersavingCfg() config.Config {
	cfg := testCfg()
	cfg.Powersaving.Enabled = true // 23:00–07:00 @ 32 (defaults)
	return cfg
}

func newTestLoop(t *testing.T, src func() *board.Snapshot, cfg config.Config) (*Loop, *fakeFlusher) {
	t.Helper()
	fl := &fakeFlusher{}
	log := obs.NewLogger(&strings.Builder{}, nil, slog.LevelInfo)
	return NewLoop(src, fl, cfg, mustBoardFonts(t), "v1", log), fl
}

func TestStepFlushesFullFrameEveryTick(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := l.step(day.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	flushes, _ := fl.stats()
	if flushes != 3 {
		t.Fatalf("flushes = %d, want 3", flushes)
	}
	if len(fl.lastFrame) != board.W*board.H/2 {
		t.Fatalf("frame size = %d, want %d (full packed frame)", len(fl.lastFrame), board.W*board.H/2)
	}
}

func TestStepSetsContrastOnlyOnChange(t *testing.T) {
	l, fl := newTestLoop(t, func() *board.Snapshot { return nil }, powersavingCfg())
	day := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)   // normal: 255
	night := time.Date(2026, 7, 6, 23, 30, 0, 0, time.UTC) // saving: 32
	for i := 0; i < 5; i++ {
		_ = l.step(day.Add(time.Duration(i) * TickInterval))
	}
	_, n := fl.stats()
	if n != 1 {
		t.Fatalf("contrast commands after 5 same-brightness ticks = %d, want 1", n)
	}
	_ = l.step(night)
	_ = l.step(night.Add(TickInterval))
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if len(fl.contrasts) != 2 || fl.contrasts[1] != 32 {
		t.Fatalf("contrasts = %v, want [255 32]", fl.contrasts)
	}
}

func TestStepRebuildsSceneOnSnapshotChange(t *testing.T) {
	var mu sync.Mutex
	snap := (*board.Snapshot)(nil)
	src := func() *board.Snapshot { mu.Lock(); defer mu.Unlock(); return snap }
	l, fl := newTestLoop(t, src, testCfg())
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	_ = l.step(now)
	initFrame := append([]byte(nil), fl.lastFrame...)
	mu.Lock()
	snap = &board.Snapshot{State: board.StateDepartures, Board: goodBoard(2), FetchedAt: now}
	mu.Unlock()
	_ = l.step(now.Add(TickInterval))
	if string(initFrame) == string(fl.lastFrame) {
		t.Fatal("frame must change when the snapshot changes")
	}
	if l.tick != 1 {
		t.Fatalf("tick = %d, want 1 (reset to 0 on swap, then incremented)", l.tick)
	}
}

// The concurrency contract under the race detector: poller publishing while
// the loop reads and flushes.
func TestPollerAndLoopConcurrently(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{{b: goodBoard(3)}}}
	p, _ := newTestPoller(f)
	l, fl := newTestLoop(t, p.Snapshot, testCfg())
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.Run(ctx) }()
	go func() {
		defer wg.Done()
		now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 200; i++ {
			_ = l.step(now.Add(time.Duration(i) * TickInterval))
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	wg.Wait()
	if fls, _ := fl.stats(); fls != 200 {
		t.Fatalf("flushes = %d, want 200", fls)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	l, _ := newTestLoop(t, func() *board.Snapshot { return nil }, testCfg())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	time.Sleep(120 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on ctx cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runtime/ -v -run 'Step|Concurrently|RunStops'`
Expected: FAIL — `undefined: NewLoop`, `TickInterval`.

- [ ] **Step 3: Write the implementation**

`internal/runtime/loop.go`:
```go
package runtime

import (
	"context"
	"log/slog"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/render"
)

// TickInterval is the fixed frame period: 0.04s, 25fps, reference parity.
const TickInterval = 40 * time.Millisecond

// Flusher is the panel seam: *display.SSD1322 in production, the PNG
// preview transport on host, a fake in tests.
type Flusher interface {
	Flush(packed []byte) error
	SetContrast(level byte) error
}

// Loop renders the active scene at a fixed rate, full-frame flushing every
// tick (ADR 0002 baseline). It owns the frame tick counter, which restarts
// whenever a new snapshot is published so scene entry animations replay.
type Loop struct {
	src     func() *board.Snapshot
	fl      Flusher
	cfg     config.Config
	fonts   *board.Fonts
	version string
	log     *slog.Logger

	fb         *render.Framebuffer
	scene      *render.Scene
	last       *board.Snapshot
	tick       int
	brightness int  // last applied; -1 = never
	flushed    bool // first-frame logged
	sceneBuilt bool
}

// NewLoop wires a snapshot source (Poller.Snapshot) to a Flusher.
func NewLoop(src func() *board.Snapshot, fl Flusher, cfg config.Config, fonts *board.Fonts, version string, log *slog.Logger) *Loop {
	return &Loop{src: src, fl: fl, cfg: cfg, fonts: fonts, version: version, log: log, fb: render.New(board.W, board.H), brightness: -1}
}

// Run ticks until ctx cancels. A flush error is returned (fatal: the panel
// is unreachable; systemd restarts the unit).
func (l *Loop) Run(ctx context.Context) error {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if err := l.step(now); err != nil {
				return err
			}
		}
	}
}

// step renders and flushes exactly one frame at the given instant.
func (l *Loop) step(now time.Time) error {
	if snap := l.src(); snap != l.last || !l.sceneBuilt {
		l.scene = board.BuildScene(snap, l.cfg.Layout, l.version, l.fonts)
		l.last = snap
		l.tick = 0
		l.sceneBuilt = true
		l.log.Debug("scene swapped")
	}

	if b := l.cfg.BrightnessAt(now); b != l.brightness {
		if err := l.fl.SetContrast(byte(b)); err != nil {
			return err
		}
		l.brightness = b
	}

	l.fb.Clear()
	renderStart := time.Now()
	l.scene.Render(l.fb, l.tick, now)
	packed := l.fb.Pack()
	renderDur := time.Since(renderStart)
	flushStart := time.Now()
	if err := l.fl.Flush(packed); err != nil {
		return err
	}
	flushDur := time.Since(flushStart)
	if !l.flushed {
		l.flushed = true
		l.log.Info("first frame flushed", "render_us", renderDur.Microseconds(), "flush_us", flushDur.Microseconds())
	}
	if l.tick > 0 && l.tick%timingEveryTicks == 0 {
		l.log.Info("frame timing", "render_us", renderDur.Microseconds(), "flush_us", flushDur.Microseconds())
	}
	l.tick++
	return nil
}
```
Add above the Loop type:
```go
// timingEveryTicks spaces "frame timing" ring events ~15s apart so the
// 256-entry ring keeps history instead of being flooded at 25fps.
const timingEveryTicks = 375
```
And add this test to `loop_test.go`:
```go
func TestStepEmitsPeriodicFrameTiming(t *testing.T) {
	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	fl := &fakeFlusher{}
	l := NewLoop(func() *board.Snapshot { return nil }, fl, testCfg(), mustBoardFonts(t), "v1", log)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	for i := 0; i <= timingEveryTicks; i++ {
		if err := l.step(now.Add(time.Duration(i) * TickInterval)); err != nil {
			t.Fatal(err)
		}
	}
	var timings int
	for _, e := range ring.Events() {
		if e.Msg == "frame timing" {
			timings++
		}
	}
	if timings != 1 {
		t.Fatalf("frame-timing events = %d, want exactly 1 after %d ticks", timings, timingEveryTicks+1)
	}
}
```
**Check `config.Config.BrightnessAt`'s return type** (`go doc ./internal/config Config`): it returns `int` (0–255). If it differs, adapt the comparison/cast.

- [ ] **Step 4: Make -race the permanent gate**

`Makefile` — change the test target:
```make
test:
	go test -race ./...
```
`.github/workflows/ci.yml` — change the test step run line to:
```yaml
      - name: test
        run: go test -race ./... -count=1
```

- [ ] **Step 5: Run tests + full gate**

Run: `go test ./internal/runtime/ -count=1 -race -v && make check`
Expected: PASS everywhere; `make check` now runs the race detector across the repo.

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/loop.go internal/runtime/loop_test.go Makefile .github/workflows/ci.yml
git commit -m "feat(runtime): 25fps render loop with brightness-on-change; -race in test gate"
```

---

### Task 12: cmd/trainboard — wiring, PNG preview transport, fixture fetcher, systemd unit, E2E

The runnable binary: config → data client → poller + loop → transport (periph SPI under `--production`, PNG preview otherwise), plus `internal/buildinfo.Version`, a JSON fixture fetcher for offline dev, the systemd unit, and the end-to-end scripted test.

**Files:**
- Modify: `internal/buildinfo/buildinfo.go` — add `var version = "dev"` + `func Version() string` (ldflags-settable: `-X github.com/mintopia/trainboard/internal/buildinfo.version=v1.2.3`).
- Create: `cmd/trainboard/main.go`, `cmd/trainboard/preview.go`, `cmd/trainboard/fixture.go`
- Create: `deploy/trainboard.service`
- Test: `cmd/trainboard/preview_test.go`, `cmd/trainboard/fixture_test.go`, `internal/runtime/e2e_test.go`

**Interfaces:**
- Consumes: everything above; `display.OpenPeriph/PeriphConfig/New`, `config.Load/DefaultPath`, `data.NewClient`.
- Produces:
  - `buildinfo.Version() string`.
  - `previewSink` implementing `runtime.Flusher`: unpacks the SSD1322 wire format (per `render.Framebuffer.Pack`: one byte = two pixels, **high nibble = left pixel**, row-major), scales ×17 to 8-bit gray, writes `frame.png` atomically (tmp + rename) into `--preview-dir`, rate-limited to every 25th flush (1/s); `SetContrast` records the value (exposed for tests).
  - `fixtureFetcher` implementing `runtime.Fetcher`: loads a `data.Board` from a JSON file once, returns it (with `GeneratedAt` bumped to `time.Now()` each fetch so the stale grace doesn't trip) — dev/demo mode.
  - Flags: `--config` (default `config.DefaultPath`), `--production` (real SPI panel), `--preview-dir` (default `./preview`), `--fixture` (path to JSON board; wins over live Darwin), `--version` (print + exit).

- [ ] **Step 1: buildinfo version (red → green)**

Append to `internal/buildinfo/buildinfo_test.go` (create if absent):
```go
package buildinfo

import "testing"

func TestVersionDefaultsToDev(t *testing.T) {
	if Version() != "dev" {
		t.Fatalf("Version() = %q, want \"dev\"", Version())
	}
}
```
Run: `go test ./internal/buildinfo/` → FAIL (`undefined: Version`). Then append to `internal/buildinfo/buildinfo.go`:
```go
// version is stamped at release build time via
// -ldflags "-X github.com/mintopia/trainboard/internal/buildinfo.version=vX.Y.Z".
var version = "dev"

// Version reports the release version of this binary.
func Version() string { return version }
```
Run: `go test ./internal/buildinfo/` → PASS.

- [ ] **Step 2: preview sink (red → green)**

`cmd/trainboard/preview_test.go`:
```go
package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/mintopia/trainboard/internal/render"
)

func TestPreviewSinkWritesDecodablePNG(t *testing.T) {
	dir := t.TempDir()
	s := newPreviewSink(dir, 1) // every flush
	fb := render.New(256, 64)
	fb.SetPixel(0, 0, 15)  // top-left: high nibble of byte 0
	fb.SetPixel(255, 63, 8) // bottom-right: low nibble of last byte
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(dir, "frame.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 256 || img.Bounds().Dy() != 64 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	r, _, _, _ := img.At(0, 0).RGBA()
	if r>>8 != 255 { // level 15 × 17
		t.Fatalf("pixel (0,0) = %d, want 255 — nibble order wrong?", r>>8)
	}
	r, _, _, _ = img.At(255, 63).RGBA()
	if r>>8 != 8*17 {
		t.Fatalf("pixel (255,63) = %d, want %d", r>>8, 8*17)
	}
}

func TestPreviewSinkRateLimits(t *testing.T) {
	dir := t.TempDir()
	s := newPreviewSink(dir, 25)
	fb := render.New(256, 64)
	for i := 0; i < 24; i++ {
		if err := s.Flush(fb.Pack()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "frame.png")); !os.IsNotExist(err) {
		t.Fatal("no PNG expected before the 25th flush")
	}
	if err := s.Flush(fb.Pack()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "frame.png")); err != nil {
		t.Fatal("PNG expected on the 25th flush")
	}
}

func TestPreviewSinkRecordsContrast(t *testing.T) {
	s := newPreviewSink(t.TempDir(), 1)
	if err := s.SetContrast(32); err != nil {
		t.Fatal(err)
	}
	if s.lastContrast != 32 {
		t.Fatalf("lastContrast = %d", s.lastContrast)
	}
}
```
Run to FAIL, then `cmd/trainboard/preview.go`:
```go
package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
)

// previewSink is the host-mode Flusher: it unpacks SSD1322 wire frames and
// writes a rate-limited PNG the operator (and later the M2 status page) can
// watch instead of real glass.
type previewSink struct {
	dir          string
	every        int // write 1 PNG per N flushes
	n            int
	lastContrast byte
}

func newPreviewSink(dir string, every int) *previewSink {
	return &previewSink{dir: dir, every: every}
}

func (p *previewSink) SetContrast(level byte) error {
	p.lastContrast = level
	return nil
}

func (p *previewSink) Flush(packed []byte) error {
	p.n++
	if p.n%p.every != 0 {
		return nil
	}
	const w, h = 256, 64
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i, b := range packed {
		img.Pix[i*2] = (b >> 4) * 17  // high nibble = left pixel
		img.Pix[i*2+1] = (b & 0x0F) * 17
	}
	tmp, err := os.CreateTemp(p.dir, "frame-*.png.tmp")
	if err != nil {
		return err
	}
	if err := png.Encode(tmp, img); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(p.dir, "frame.png"))
}
```
Run: `go test ./cmd/trainboard/ -race` → PASS.

- [ ] **Step 3: fixture fetcher (red → green)**

`cmd/trainboard/fixture_test.go`:
```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

func TestFixtureFetcherLoadsBoardAndFreshensGeneratedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.json")
	body := `{"LocationName":"Paddington","CRS":"PAD","GeneratedAt":"2020-01-01T00:00:00Z","Departures":[{"ScheduledTime":"10:32","Status":"On time","Platform":"9","Destination":{"Name":"Bristol Temple Meads"}}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := newFixtureFetcher(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Fetch(context.Background(), data.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if b.LocationName != "Paddington" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	if time.Since(b.GeneratedAt) > time.Minute {
		t.Fatal("GeneratedAt must be freshened so the stale grace never trips in fixture mode")
	}
	// Each fetch returns an independent copy (published snapshots are immutable).
	b2, _ := f.Fetch(context.Background(), data.Request{})
	b.Departures[0].Platform = "MUTATED"
	if b2.Departures[0].Platform == "MUTATED" || func() bool { b3, _ := f.Fetch(context.Background(), data.Request{}); return b3.Departures[0].Platform == "MUTATED" }() {
		t.Fatal("fixture fetches must not alias each other")
	}
}

func TestFixtureFetcherMissingFile(t *testing.T) {
	if _, err := newFixtureFetcher("/nonexistent.json"); err == nil {
		t.Fatal("expected error for missing fixture")
	}
}
```
Run to FAIL, then `cmd/trainboard/fixture.go`:
```go
package main

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

// fixtureFetcher replays a data.Board from a JSON file: offline dev/demo
// mode. Each Fetch returns a deep-enough copy with a fresh GeneratedAt so
// snapshots stay immutable and the stale grace never trips.
type fixtureFetcher struct {
	raw []byte
}

func newFixtureFetcher(path string) (*fixtureFetcher, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe data.Board
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	return &fixtureFetcher{raw: raw}, nil
}

func (f *fixtureFetcher) Fetch(_ context.Context, _ data.Request) (*data.Board, error) {
	var b data.Board
	if err := json.Unmarshal(f.raw, &b); err != nil {
		return nil, err
	}
	b.GeneratedAt = time.Now()
	return &b, nil
}
```
Run: `go test ./cmd/trainboard/ -race` → PASS.

- [ ] **Step 4: end-to-end scripted test (red only until main exists is fine — it tests runtime wiring, not main)**

`internal/runtime/e2e_test.go`:
```go
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/obs"
)

// TestEndToEndScriptedOutcomes drives config → fetch → classify → scene →
// flush across the spec's fetch-outcome script and asserts the state the
// screen is in after each poll.
func TestEndToEndScriptedOutcomes(t *testing.T) {
	f := &scriptFetcher{results: []fetchResult{
		{b: goodBoard(3)},                    // 1: departures
		{b: goodBoard(0)},                    // 2: no services
		{err: errors.New("dial tcp: refused")}, // 3: error inside grace → keeps NoServices
		{err: errors.New("dial tcp: refused")}, // 4: error past grace → Error
		{b: goodBoard(2)},                    // 5: recovery
	}}
	ring := obs.NewRing(64)
	log := obs.NewLogger(&strings.Builder{}, ring, slog.LevelInfo)
	p := NewPoller(f, testCfg(), log)

	clock := t0
	p.now = func() time.Time { return clock }

	l, fl := newTestLoop(t, p.Snapshot, testCfg())

	steps := []struct {
		advance time.Duration
		want    board.State
	}{
		{0, board.StateDepartures},
		{time.Minute, board.StateNoServices},
		{time.Minute, board.StateNoServices}, // stale grace holds
		{StaleGrace, board.StateError},
		{time.Minute, board.StateDepartures},
	}
	for i, st := range steps {
		clock = clock.Add(st.advance)
		p.pollOnce(context.Background())
		snap := p.Snapshot()
		if snap.State != st.want {
			t.Fatalf("step %d: state = %v, want %v", i+1, snap.State, st.want)
		}
		if err := l.step(clock); err != nil {
			t.Fatal(err)
		}
	}
	if flushes, _ := fl.stats(); flushes != len(steps) {
		t.Fatalf("flushes = %d, want %d", flushes, len(steps))
	}
	// The ring recorded every transition of the journey.
	var transitions []string
	for _, e := range ring.Events() {
		if e.Msg == "state transition" {
			transitions = append(transitions, e.Attrs["from"]+"→"+e.Attrs["to"])
		}
	}
	want := []string{"initialising→departures", "departures→no-services", "no-services→error", "error→departures"}
	if strings.Join(transitions, ",") != strings.Join(want, ",") {
		t.Fatalf("transitions = %v, want %v", transitions, want)
	}
}
```
Run: `go test ./internal/runtime/ -race -run EndToEnd -v` → PASS (everything it uses exists after Tasks 9–11; if it fails, the classification/poller contract is broken — fix the code).

- [ ] **Step 5: main wiring**

`cmd/trainboard/main.go`:
```go
// Command trainboard runs the departure board: config → Darwin poller →
// scene render loop → SSD1322 (or a PNG preview on host).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/buildinfo"
	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/display"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "trainboard:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", config.DefaultPath, "config file path")
	production := flag.Bool("production", false, "drive the real SSD1322 over SPI")
	previewDir := flag.String("preview-dir", "./preview", "PNG preview directory (host mode)")
	fixture := flag.String("fixture", "", "JSON board fixture instead of live Darwin (dev)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Name(), buildinfo.Version())
		return nil
	}

	ring := obs.NewRing(obs.DefaultRingCapacity)
	log := obs.NewLogger(os.Stderr, ring, slog.LevelInfo)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	fonts, err := board.LoadFonts()
	if err != nil {
		return err
	}

	var fl runtime.Flusher
	if *production {
		tr, err := display.OpenPeriph(display.PeriphConfig{SPIPort: "SPI0.0", DCPin: "GPIO25", ResetPin: "GPIO27", MaxHz: 16_000_000})
		if err != nil {
			return err
		}
		defer func() { _ = tr.Close() }()
		panel := display.New(tr)
		if err := panel.Init(); err != nil {
			return err
		}
		fl = panel
	} else {
		if err := os.MkdirAll(*previewDir, 0o755); err != nil {
			return err
		}
		fl = newPreviewSink(*previewDir, 25)
		log.Info("preview mode", "dir", *previewDir)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// Config unusable: show the E04 fault on-screen and idle; the operator
		// fixes the file (M2 will offer a UI). systemd keeps us alive.
		log.Error("config load failed", "err", err.Error(), "path", *cfgPath)
		snap := &board.Snapshot{State: board.StateError, Fault: obs.FaultConfigError}
		loop := runtime.NewLoop(func() *board.Snapshot { return snap }, fl, config.Default(), fonts, buildinfo.Version(), log)
		return loop.Run(ctx)
	}
	log.Info("config loaded", "config", cfg.Redacted().String())

	var fetcher runtime.Fetcher
	if *fixture != "" {
		fetcher, err = newFixtureFetcher(*fixture)
		if err != nil {
			return err
		}
		log.Info("fixture mode", "path", *fixture)
	} else {
		fetcher = data.NewClient(cfg.Darwin.Token)
	}

	poller := runtime.NewPoller(fetcher, cfg, log)
	go poller.Run(ctx)

	loop := runtime.NewLoop(poller.Snapshot, fl, cfg, fonts, buildinfo.Version(), log)
	log.Info("starting render loop", "version", buildinfo.Version())
	return loop.Run(ctx)
}
```
**Check `config.Config.Redacted()`/`String()` actual API** (Task 2 of M1B provided both; use whichever compiles — `cfg.Redacted().String()` or just `cfg.String()` if String already redacts).

- [ ] **Step 6: systemd unit**

`deploy/trainboard.service`:
```ini
# Train departure board. Installed to /etc/systemd/system/trainboard.service.
# Starts early and deliberately does NOT wait for network-online: the board
# renders its Initialising/Error scenes offline and recovers when Darwin
# becomes reachable (M1C design §systemd).
[Unit]
Description=Train departure board
# No After=network-online.target on purpose.

[Service]
ExecStart=/usr/local/bin/trainboard --production
Restart=always
RestartSec=2
User=root
# WatchdogSec placeholder: M3's connectivity manager will own sd_notify
# aggregation. The render loop must NOT pet the watchdog for connectivity.
#WatchdogSec=30

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 7: run everything, then run the binary on host with the preview**

```bash
make check
go build ./... && GOOS=linux GOARCH=arm64 go build ./...
mkdir -p /tmp/tb-preview
cat > /tmp/tb-fixture.json <<'EOF'
{"LocationName":"Paddington","CRS":"PAD","Departures":[
 {"ScheduledTime":"10:32","Status":"On time","Platform":"9","Operator":"Great Western Railway","Length":5,
  "Destination":{"Name":"Bristol Temple Meads"},
  "CallingPoints":[{"Location":{"Name":"Reading"},"ScheduledTime":"11:00"},{"Location":{"Name":"Swindon"},"ScheduledTime":"11:20"}]},
 {"ScheduledTime":"10:41","Status":"Exp 10:44","Platform":"12","Operator":"Great Western Railway","Destination":{"Name":"Oxford"}}
]}
EOF
cat > /tmp/tb-config.json <<'EOF'
{"version":1,"darwin":{"token":"unused-in-fixture-mode"},"board":{"origin":"PAD","services":3,"cutoffHours":8,"refreshSeconds":60,"timeWindowMinutes":120,"replacements":{}},"layout":{"times":true},"powersaving":{"enabled":false,"start":"23:00","end":"07:00","brightness":32}}
EOF
go run ./cmd/trainboard --config /tmp/tb-config.json --fixture /tmp/tb-fixture.json --preview-dir /tmp/tb-preview &
sleep 5 && open /tmp/tb-preview/frame.png && kill %1
```
Expected: `make check` green; both native and linux/arm64 builds succeed; the preview PNG shows the live departure board scene (row 1, calling-at scroll, info line, carousel band, clock).

- [ ] **Step 8: Commit**

```bash
git add internal/buildinfo/ cmd/trainboard/ deploy/ internal/runtime/e2e_test.go
git commit -m "feat(cmd): trainboard binary with PNG preview, fixture mode, systemd unit"
```

---

## Verification (whole-plan gate)

1. `make check` — vet + golangci-lint + `go test -race ./...` all green.
2. `GOOS=linux GOARCH=arm64 go build ./...` — Pi target builds.
3. `git status --porcelain` — clean tree, every task committed.
4. Host preview run (Task 12 Step 7) shows a correct departure board frame.
5. All six scenes have goldens in `internal/board/testdata/` — eyeballed once against the reference board's look.
6. Issues #23, #28, #31 are fully covered; #21/#29 untouched (hardware session).



