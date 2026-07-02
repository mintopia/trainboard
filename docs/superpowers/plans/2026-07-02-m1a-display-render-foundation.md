# M1 Plan A — Display + Render Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Scope note:** M1 (`PLAN.md`) is split into three sequenced, independently-executable plans:
> - **Plan A (this doc)** — Display driver + render engine foundation. Produces: compose a scene → render to a 4-bit framebuffer → flush to the SSD1322 (fake in tests, real panel on hardware), plus the fps benchmark that gates the render architecture.
> - **Plan B** — Darwin Lite data client + config store (issues: data ×3, config).
> - **Plan C** — Board scenes + runtime integration + boot SLA + observability wiring (CI is bootstrapped here in Plan A Task 1).
>
> Plans B and C are written when reached. Plan A has no dependency on B/C; C consumes A's `render`/`display` packages and B's `data`/`config`.

**Goal:** A native Go SSD1322 driver and greyscale render engine that can rasterize the reference board's fonts/scenes into a 256×64 4-bit framebuffer and flush it to the panel at ≥25fps, with the flush path proven by an on-hardware benchmark.

**Architecture:** Two deep packages behind narrow interfaces. `display` owns the SSD1322 SPI protocol (command/data framing, init sequence, windowed + chunked RAM writes) and knows nothing about pixels-as-content — it flushes an opaque packed byte slice. `render` owns the 4-bit framebuffer, sfnt font rasterization, and the scene/element engine, and produces the packed bytes via `Framebuffer.Pack()`. The two only meet at `[]byte`. A `SPI`/`Transport` interface makes the driver testable with an in-memory fake (golden-byte tests); golden-image tests pin render output.

**Tech Stack:** Go (latest stable — `go 1.26` at time of writing; bump freely), `periph.io/x/conn/v3` + `periph.io/x/host/v3` (SPI/GPIO transport only), `golang.org/x/image/font/opentype` (sfnt — **not** `freetype/truetype`), `golang.org/x/image/font`, `golang.org/x/image/math/fixed`. Standard `testing` with golden files. Lint: `golangci-lint`.

## Global Constraints

- **Module path:** `github.com/mintopia/trainboard` (confirmed — owner `mintopia`).
- **Go version:** latest stable; `go.mod` sets `go 1.26`. CI pins the same.
- **Panel:** SSD1322, **256×64**, **4-bit greyscale** (levels 0–15). Two horizontally-adjacent pixels pack into one byte: **high nibble = left pixel, low nibble = right pixel**. Row = 128 bytes; full frame = **8192 bytes**.
- **SSD1322 addressing:** column address unit = **4 pixels / 2 bytes**; a 256-wide panel sits at **column offset `0x1C`** in the 480-wide (120-column) RAM (`(120-64)/2 = 28 = 0x1C`). Column window for full width: start `0x1C`, end `0x5B`. Row window full height: `0x00`–`0x3F`. Any partial write must be **4-pixel-aligned** in x and width.
- **SPI chunking:** `spidev` default `bufsiz` is **4096 B**; a full frame is 8192 B → RAM data writes are split into chunks of **≤4096 B**.
- **Fonts (from reference project `reference/src/fonts/`):** `Dot Matrix Regular.ttf`, `Dot Matrix Bold.ttf`, `Dot Matrix Bold Tall.ttf`. Sizes: regular/bold/boldtall at **10px**, boldlarge = Bold at **20px**. Pixel size = point size at **DPI 72**. **HintingNone** (sfnt does no hinting).
- **Geometry (reference parity):** text rows are **256×12**; clock row is **256×14**. Amber-on-black → amber = level **15**, black = **0**.
- **Frame rate:** target **25–30fps** (reference ran 25fps at 0.04s interval). 60fps only if the Task 14 benchmark supports it.
- **Layout constants are derived from captured reference frames + golden-image tests — never by porting PIL/FreeType measurement calls** (Go sfnt metrics differ from PIL).
- **Scrolling is integer-pixel** (no sub-pixel motion). Greyscale is for glyph *edge* anti-aliasing only.
- **TDD throughout:** red → green → refactor. Every task ends green with `go test ./...`, `go vet ./...`, `golangci-lint run` all passing. Commit at the end of each task.

---

### Task 1: Repo scaffolding + CI gate

Bootstraps the module, lint/vet/test tooling, and the GitHub Actions gate so every later task's tests are enforced.

**Files:**
- Create: `go.mod`
- Create: `.golangci.yml`
- Create: `.github/workflows/ci.yml`
- Create: `Makefile`
- Create: `internal/buildinfo/buildinfo.go`
- Test: `internal/buildinfo/buildinfo_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: a compiling module rooted at `github.com/mintopia/trainboard`; `make check` runs vet + lint + test; CI green on push/PR.

- [ ] **Step 1: Write the failing test**

`internal/buildinfo/buildinfo_test.go`:
```go
package buildinfo

import "testing"

func TestName(t *testing.T) {
	if got := Name(); got != "trainboard" {
		t.Fatalf("Name() = %q, want %q", got, "trainboard")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/buildinfo/`
Expected: FAIL — `go.mod` missing / package `buildinfo` has no `Name`.

- [ ] **Step 3: Create module + minimal implementation**

`go.mod`:
```
module github.com/mintopia/trainboard

go 1.26
```

`internal/buildinfo/buildinfo.go`:
```go
// Package buildinfo exposes static identifiers for the binary.
package buildinfo

// Name is the canonical short name of the application.
func Name() string { return "trainboard" }
```

`.golangci.yml`:
```yaml
version: "2"
linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - misspell
    - revive
run:
  timeout: 5m
```

`Makefile`:
```makefile
.PHONY: test vet lint check build

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run

check: vet lint test

build:
	go build ./...
```

`.github/workflows/ci.yml`:
```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  build:
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
        run: go test ./... -count=1
      - uses: golangci/golangci-lint-action@v6
        with:
          version: latest
```

- [ ] **Step 4: Run tests + gates to verify they pass**

Run: `go test ./... && go vet ./...`
Expected: PASS (buildinfo test green; vet clean). `golangci-lint run` clean if installed locally.

- [ ] **Step 5: Commit**

```bash
git add go.mod .golangci.yml .github/workflows/ci.yml Makefile internal/buildinfo/
git commit -m "chore: scaffold Go module, lint/vet/test tooling, CI gate"
```

---

### Task 2: Transport interface + FakeTransport + command constants

The seam between the driver and the wire. `FakeTransport` records every operation so later golden-byte tests can assert exact command/data sequences.

**Files:**
- Create: `internal/display/transport.go`
- Create: `internal/display/commands.go`
- Create: `internal/display/fake.go`
- Test: `internal/display/fake_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Transport interface { Command(cmd byte, args ...byte) error; Data(p []byte) error; Reset() error; Close() error }`
  - `type Op struct { Kind OpKind; Bytes []byte }` with `OpKind` one of `OpReset`, `OpCommand`, `OpData`.
  - `type FakeTransport struct { Ops []Op }` implementing `Transport`; `func NewFake() *FakeTransport`.
  - Command constants (`cmdSetColumnAddr = 0x15`, `cmdWriteRAM = 0x5C`, `cmdSetRowAddr = 0x75`, `cmdSetContrast = 0xC1`, `cmdDisplayOn = 0xAF`, etc.).

- [ ] **Step 1: Write the failing test**

`internal/display/fake_test.go`:
```go
package display

import (
	"reflect"
	"testing"
)

func TestFakeTransportRecordsOps(t *testing.T) {
	f := NewFake()
	if err := f.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := f.Command(0x15, 0x1C, 0x5B); err != nil {
		t.Fatal(err)
	}
	if err := f.Data([]byte{0x0A, 0x0B}); err != nil {
		t.Fatal(err)
	}
	want := []Op{
		{Kind: OpReset},
		{Kind: OpCommand, Bytes: []byte{0x15, 0x1C, 0x5B}},
		{Kind: OpData, Bytes: []byte{0x0A, 0x0B}},
	}
	if !reflect.DeepEqual(f.Ops, want) {
		t.Fatalf("Ops = %#v, want %#v", f.Ops, want)
	}
}

func TestFakeTransportCopiesData(t *testing.T) {
	f := NewFake()
	buf := []byte{1, 2, 3}
	if err := f.Data(buf); err != nil {
		t.Fatal(err)
	}
	buf[0] = 99 // mutate caller's slice after the call
	if f.Ops[0].Bytes[0] != 1 {
		t.Fatalf("FakeTransport did not copy data; got %d", f.Ops[0].Bytes[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/display/`
Expected: FAIL — undefined `NewFake`, `Op`, `OpReset`, etc.

- [ ] **Step 3: Write minimal implementation**

`internal/display/transport.go`:
```go
// Package display drives an SSD1322 256x64 4-bit greyscale OLED over SPI.
package display

// Transport carries SSD1322 command and data bytes to the panel. Command
// bytes are framed with D/C low; args and Data payloads with D/C high.
type Transport interface {
	Command(cmd byte, args ...byte) error
	Data(p []byte) error
	Reset() error
	Close() error
}
```

`internal/display/commands.go`:
```go
package display

// SSD1322 command opcodes (datasheet §9). Values referenced by the init
// sequence and flush path; pinned by golden-byte tests.
const (
	cmdEnableGrayTable  = 0x00
	cmdSetColumnAddr    = 0x15
	cmdWriteRAM         = 0x5C
	cmdSetRowAddr       = 0x75
	cmdSetRemap         = 0xA0
	cmdSetStartLine     = 0xA1
	cmdSetDisplayOffset = 0xA2
	cmdSetDisplayNormal = 0xA6
	cmdExitPartial      = 0xA9
	cmdFunctionSelect   = 0xAB
	cmdDisplayOff       = 0xAE
	cmdDisplayOn        = 0xAF
	cmdSetPhaseLength   = 0xB1
	cmdSetClockDivider  = 0xB3
	cmdDisplayEnhanceA  = 0xB4
	cmdSetGPIO          = 0xB5
	cmdSecondPrecharge  = 0xB6
	cmdSetContrast      = 0xC1
	cmdMasterContrast   = 0xC7
	cmdSetMuxRatio      = 0xCA
	cmdDisplayEnhanceB  = 0xD1
	cmdSetCommandLock   = 0xFD
	cmdSetPrechargeVolt = 0xBB
	cmdSetVCOMH         = 0xBE

	// Column window for a 256px panel in the 480px RAM.
	colStart = 0x1C
	colEnd   = 0x5B
	rowStart = 0x00
	rowEnd   = 0x3F
)
```

`internal/display/fake.go`:
```go
package display

// OpKind identifies a recorded transport operation.
type OpKind int

const (
	OpReset OpKind = iota
	OpCommand
	OpData
)

// Op is a single recorded transport operation.
type Op struct {
	Kind  OpKind
	Bytes []byte
}

// FakeTransport is an in-memory Transport that records every operation for
// golden-byte assertions in tests.
type FakeTransport struct {
	Ops    []Op
	Closed bool
}

// NewFake returns an empty FakeTransport.
func NewFake() *FakeTransport { return &FakeTransport{} }

// Command records an opcode plus its argument bytes.
func (f *FakeTransport) Command(cmd byte, args ...byte) error {
	b := append([]byte{cmd}, args...)
	f.Ops = append(f.Ops, Op{Kind: OpCommand, Bytes: b})
	return nil
}

// Data records a copy of the payload (callers may reuse their buffer).
func (f *FakeTransport) Data(p []byte) error {
	b := make([]byte, len(p))
	copy(b, p)
	f.Ops = append(f.Ops, Op{Kind: OpData, Bytes: b})
	return nil
}

// Reset records a reset pulse.
func (f *FakeTransport) Reset() error {
	f.Ops = append(f.Ops, Op{Kind: OpReset})
	return nil
}

// Close marks the transport closed.
func (f *FakeTransport) Close() error { f.Closed = true; return nil }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/display/ -run TestFakeTransport -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/display/
git commit -m "feat(display): transport interface, command constants, fake transport"
```

---

### Task 3: SSD1322 init sequence (golden-byte)

**Files:**
- Create: `internal/display/ssd1322.go`
- Test: `internal/display/ssd1322_test.go`

**Interfaces:**
- Consumes: `Transport`, command constants (Task 2).
- Produces: `type SSD1322 struct{...}`; `func New(t Transport) *SSD1322`; `func (d *SSD1322) Init() error`.

> The exact init bytes below are derived from the SSD1322 datasheet §9 and the reference project's `luma.oled` SSD1322 device. The golden-byte test **pins** the sequence; the on-hardware benchmark (Task 14) is where the sequence is confirmed to actually light the panel. If a byte must change to work on real hardware, change it here and update the golden expectation in the same commit.

- [ ] **Step 1: Write the failing test**

`internal/display/ssd1322_test.go`:
```go
package display

import (
	"reflect"
	"testing"
)

func cmds(ops []Op) [][]byte {
	var out [][]byte
	for _, op := range ops {
		if op.Kind == OpCommand {
			out = append(out, op.Bytes)
		}
	}
	return out
}

func TestInitSequence(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.Init(); err != nil {
		t.Fatal(err)
	}
	if f.Ops[0].Kind != OpReset {
		t.Fatalf("Init must begin with a reset, got %v", f.Ops[0].Kind)
	}
	want := [][]byte{
		{0xFD, 0x12},
		{0xAE},
		{0xB3, 0x91},
		{0xCA, 0x3F},
		{0xA2, 0x00},
		{0xA1, 0x00},
		{0xA0, 0x14, 0x11},
		{0xB5, 0x00},
		{0xAB, 0x01},
		{0xB4, 0xA0, 0xFD},
		{0xC1, 0x9F},
		{0xC7, 0x0F},
		{0xB1, 0xE2},
		{0xD1, 0xA2, 0x20},
		{0xBB, 0x1F},
		{0xB6, 0x08},
		{0xBE, 0x07},
		{0xA6},
		{0xA9},
		{0xAF},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("init commands mismatch\n got=%#v\nwant=%#v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/display/ -run TestInitSequence`
Expected: FAIL — undefined `New` / `Init`.

- [ ] **Step 3: Write minimal implementation**

`internal/display/ssd1322.go`:
```go
package display

// SSD1322 is a 256x64 4-bit greyscale OLED controller driven over a Transport.
type SSD1322 struct {
	t Transport
}

// New wraps a Transport in an SSD1322 driver.
func New(t Transport) *SSD1322 { return &SSD1322{t: t} }

// Init resets the panel and issues the power-on configuration sequence,
// leaving the display on with normal (non-inverted) greyscale output.
func (d *SSD1322) Init() error {
	if err := d.t.Reset(); err != nil {
		return err
	}
	seq := []struct {
		cmd  byte
		args []byte
	}{
		{cmdSetCommandLock, []byte{0x12}},   // unlock commands
		{cmdDisplayOff, nil},                // sleep during config
		{cmdSetClockDivider, []byte{0x91}},  // osc freq / divider
		{cmdSetMuxRatio, []byte{0x3F}},      // 64 MUX
		{cmdSetDisplayOffset, []byte{0x00}}, //
		{cmdSetStartLine, []byte{0x00}},     //
		{cmdSetRemap, []byte{0x14, 0x11}},   // horiz addr incr + dual COM
		{cmdSetGPIO, []byte{0x00}},          //
		{cmdFunctionSelect, []byte{0x01}},   // internal VDD regulator
		{cmdDisplayEnhanceA, []byte{0xA0, 0xFD}},
		{cmdSetContrast, []byte{0x9F}},
		{cmdMasterContrast, []byte{0x0F}},
		{cmdSetPhaseLength, []byte{0xE2}},
		{cmdDisplayEnhanceB, []byte{0xA2, 0x20}},
		{cmdSetPrechargeVolt, []byte{0x1F}},
		{cmdSecondPrecharge, []byte{0x08}},
		{cmdSetVCOMH, []byte{0x07}},
		{cmdSetDisplayNormal, nil},
		{cmdExitPartial, nil},
		{cmdDisplayOn, nil},
	}
	for _, s := range seq {
		if err := d.t.Command(s.cmd, s.args...); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/display/ -run TestInitSequence -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/display/ssd1322.go internal/display/ssd1322_test.go
git commit -m "feat(display): SSD1322 init sequence with golden-byte test"
```

---

### Task 4: Contrast-based brightness

**Files:**
- Modify: `internal/display/ssd1322.go`
- Test: `internal/display/ssd1322_test.go`

**Interfaces:**
- Consumes: `SSD1322`, `cmdSetContrast`.
- Produces: `func (d *SSD1322) SetContrast(level byte) error`.

- [ ] **Step 1: Write the failing test**

Append to `internal/display/ssd1322_test.go`:
```go
func TestSetContrast(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.SetContrast(0x7F); err != nil {
		t.Fatal(err)
	}
	got := cmds(f.Ops)
	want := [][]byte{{0xC1, 0x7F}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SetContrast cmds = %#v, want %#v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/display/ -run TestSetContrast`
Expected: FAIL — undefined `SetContrast`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/display/ssd1322.go`:
```go
// SetContrast sets the panel contrast current (0x00–0xFF), the brightness knob.
func (d *SSD1322) SetContrast(level byte) error {
	return d.t.Command(cmdSetContrast, level)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/display/ -run TestSetContrast -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/display/ssd1322.go internal/display/ssd1322_test.go
git commit -m "feat(display): SetContrast brightness control"
```

---

### Task 5: 4-bit framebuffer + Pack (golden-byte)

**Files:**
- Create: `internal/render/framebuffer.go`
- Test: `internal/render/framebuffer_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Framebuffer struct { W, H int; Pix []byte }` — `Pix` length `W*H`, one byte per pixel, values 0–15.
  - `func New(w, h int) *Framebuffer`
  - `func (fb *Framebuffer) Clear()`
  - `func (fb *Framebuffer) SetPixel(x, y int, level byte)` — clips out-of-bounds; clamps level to 0–15.
  - `func (fb *Framebuffer) At(x, y int) byte`
  - `func (fb *Framebuffer) Pack() []byte` — SSD1322 4-bit format, length `W*H/2`, high nibble = left pixel.

- [ ] **Step 1: Write the failing test**

`internal/render/framebuffer_test.go`:
```go
package render

import (
	"bytes"
	"testing"
)

func TestPackNibbleOrder(t *testing.T) {
	fb := New(4, 1)
	fb.SetPixel(0, 0, 0x0A) // left pixel of byte 0
	fb.SetPixel(1, 0, 0x0B) // right pixel of byte 0
	fb.SetPixel(2, 0, 0x0C)
	fb.SetPixel(3, 0, 0x0D)
	got := fb.Pack()
	want := []byte{0xAB, 0xCD}
	if !bytes.Equal(got, want) {
		t.Fatalf("Pack() = % X, want % X", got, want)
	}
}

func TestPackFullFrameLength(t *testing.T) {
	fb := New(256, 64)
	if got := len(fb.Pack()); got != 8192 {
		t.Fatalf("full-frame Pack len = %d, want 8192", got)
	}
}

func TestSetPixelClampsAndClips(t *testing.T) {
	fb := New(2, 1)
	fb.SetPixel(0, 0, 0xFF) // clamp to 0x0F
	fb.SetPixel(99, 0, 5)   // out of bounds: no panic, ignored
	if fb.At(0, 0) != 0x0F {
		t.Fatalf("level not clamped: got %#x", fb.At(0, 0))
	}
}

func TestClear(t *testing.T) {
	fb := New(2, 2)
	fb.SetPixel(0, 0, 9)
	fb.Clear()
	if fb.At(0, 0) != 0 {
		t.Fatalf("Clear did not zero pixel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/`
Expected: FAIL — undefined `New`, `Framebuffer`, etc.

- [ ] **Step 3: Write minimal implementation**

`internal/render/framebuffer.go`:
```go
// Package render provides a 4-bit greyscale framebuffer, sfnt font
// rasterization, and a scene/element engine for the departure board.
package render

// Framebuffer is a W×H grid of 4-bit greyscale pixels (levels 0–15), one
// byte per pixel for cheap drawing. Pack() converts to SSD1322 wire format.
type Framebuffer struct {
	W, H int
	Pix  []byte
}

// New returns a cleared W×H framebuffer.
func New(w, h int) *Framebuffer {
	return &Framebuffer{W: w, H: h, Pix: make([]byte, w*h)}
}

// Clear resets every pixel to 0 (black).
func (fb *Framebuffer) Clear() {
	for i := range fb.Pix {
		fb.Pix[i] = 0
	}
}

// SetPixel writes level (clamped 0–15) at (x,y); out-of-bounds is ignored.
func (fb *Framebuffer) SetPixel(x, y int, level byte) {
	if x < 0 || y < 0 || x >= fb.W || y >= fb.H {
		return
	}
	if level > 0x0F {
		level = 0x0F
	}
	fb.Pix[y*fb.W+x] = level
}

// At returns the level at (x,y), or 0 if out of bounds.
func (fb *Framebuffer) At(x, y int) byte {
	if x < 0 || y < 0 || x >= fb.W || y >= fb.H {
		return 0
	}
	return fb.Pix[y*fb.W+x]
}

// Pack encodes the framebuffer into SSD1322 4-bit format: two horizontally
// adjacent pixels per byte, high nibble = left pixel. Length is W*H/2.
func (fb *Framebuffer) Pack() []byte {
	out := make([]byte, fb.W*fb.H/2)
	oi := 0
	for y := 0; y < fb.H; y++ {
		row := y * fb.W
		for x := 0; x < fb.W; x += 2 {
			hi := fb.Pix[row+x] & 0x0F
			lo := fb.Pix[row+x+1] & 0x0F
			out[oi] = hi<<4 | lo
			oi++
		}
	}
	return out
}
```

> Note: `Pack` assumes `W` is even (256 is). If a future partial buffer has odd width, add a guard — YAGNI for now.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/render/ -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/render/framebuffer.go internal/render/framebuffer_test.go
git commit -m "feat(render): 4-bit framebuffer with SSD1322 Pack()"
```

---

### Task 6: Full-frame flush — windowing + chunking (golden-byte)

**Files:**
- Modify: `internal/display/ssd1322.go`
- Create: `internal/display/chunk.go`
- Test: `internal/display/ssd1322_test.go`
- Test: `internal/display/chunk_test.go`

**Interfaces:**
- Consumes: `SSD1322`, command/window constants.
- Produces:
  - `func chunk(p []byte, max int) [][]byte` (pure, unexported).
  - `func (d *SSD1322) Flush(packed []byte) error` — sets full window, issues Write-RAM, streams `packed` in ≤`maxChunk` byte data writes. `maxChunk = 4096`. Errors if `len(packed) != 8192`.
  - `const maxChunk = 4096`.

- [ ] **Step 1: Write the failing tests**

`internal/display/chunk_test.go`:
```go
package display

import "testing"

func TestChunkSplitsExact(t *testing.T) {
	got := chunk(make([]byte, 8192), 4096)
	if len(got) != 2 || len(got[0]) != 4096 || len(got[1]) != 4096 {
		t.Fatalf("expected two 4096-byte chunks, got %d chunks", len(got))
	}
}

func TestChunkRemainder(t *testing.T) {
	got := chunk(make([]byte, 5000), 4096)
	if len(got) != 2 || len(got[1]) != 904 {
		t.Fatalf("remainder chunk wrong: %d chunks, last=%d", len(got), len(got[len(got)-1]))
	}
}

func TestChunkEmpty(t *testing.T) {
	if got := chunk(nil, 4096); len(got) != 0 {
		t.Fatalf("chunk(nil) = %d chunks, want 0", len(got))
	}
}
```

Append to `internal/display/ssd1322_test.go`:
```go
func TestFlushWindowAndChunks(t *testing.T) {
	f := NewFake()
	d := New(f)
	if err := d.Flush(make([]byte, 8192)); err != nil {
		t.Fatal(err)
	}
	// First three ops set column window, row window, and Write-RAM.
	wantCmds := [][]byte{
		{0x15, 0x1C, 0x5B},
		{0x75, 0x00, 0x3F},
		{0x5C},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, wantCmds) {
		t.Fatalf("flush cmds = %#v, want %#v", got, wantCmds)
	}
	// Data must arrive as two 4096-byte chunks after the commands.
	var dataLens []int
	for _, op := range f.Ops {
		if op.Kind == OpData {
			dataLens = append(dataLens, len(op.Bytes))
		}
	}
	if len(dataLens) != 2 || dataLens[0] != 4096 || dataLens[1] != 4096 {
		t.Fatalf("data chunks = %v, want [4096 4096]", dataLens)
	}
}

func TestFlushRejectsWrongSize(t *testing.T) {
	d := New(NewFake())
	if err := d.Flush(make([]byte, 100)); err == nil {
		t.Fatal("expected error for wrong-size frame")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/display/ -run 'TestChunk|TestFlush'`
Expected: FAIL — undefined `chunk` / `Flush`.

- [ ] **Step 3: Write minimal implementation**

`internal/display/chunk.go`:
```go
package display

// maxChunk is the largest SPI data write; spidev's default bufsiz is 4096 B
// while a full frame is 8192 B, so RAM writes must be split.
const maxChunk = 4096

// chunk splits p into slices of at most max bytes (views into p, not copies).
func chunk(p []byte, max int) [][]byte {
	if len(p) == 0 {
		return nil
	}
	var out [][]byte
	for len(p) > 0 {
		n := max
		if n > len(p) {
			n = len(p)
		}
		out = append(out, p[:n])
		p = p[n:]
	}
	return out
}
```

Append to `internal/display/ssd1322.go`:
```go
import "fmt"

// frameBytes is the packed size of a full 256x64 4-bit frame.
const frameBytes = 256 * 64 / 2

// Flush writes a full packed frame (8192 bytes) to the panel: it sets the
// full column/row window, issues Write-RAM, then streams the data in
// spidev-safe chunks.
func (d *SSD1322) Flush(packed []byte) error {
	if len(packed) != frameBytes {
		return fmt.Errorf("display: frame is %d bytes, want %d", len(packed), frameBytes)
	}
	if err := d.t.Command(cmdSetColumnAddr, colStart, colEnd); err != nil {
		return err
	}
	if err := d.t.Command(cmdSetRowAddr, rowStart, rowEnd); err != nil {
		return err
	}
	if err := d.t.Command(cmdWriteRAM); err != nil {
		return err
	}
	for _, c := range chunk(packed, maxChunk) {
		if err := d.t.Data(c); err != nil {
			return err
		}
	}
	return nil
}
```

> If Task 3 already added an `import` block, merge `"fmt"` into it rather than adding a second import statement.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/display/ -run 'TestChunk|TestFlush' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/display/chunk.go internal/display/chunk_test.go internal/display/ssd1322.go internal/display/ssd1322_test.go
git commit -m "feat(display): full-frame flush with windowed, chunked RAM writes"
```

---

### Task 7: Partial-region flush — 4-pixel alignment (golden-byte)

Supports the benchmark's 256×12 / 256×24 partial-flush timings and future dirty-region rendering (only if the benchmark justifies it).

**Files:**
- Modify: `internal/display/ssd1322.go`
- Test: `internal/display/ssd1322_test.go`

**Interfaces:**
- Consumes: `SSD1322`, window constants.
- Produces: `func (d *SSD1322) FlushRegion(rowData []byte, x, y, w, h int) error` where `rowData` is `h*(w/2)` packed bytes (row-major, `w/2` bytes per row). Requires `x%4==0` and `w%4==0`; requires the region inside 256×64.

- [ ] **Step 1: Write the failing test**

Append to `internal/display/ssd1322_test.go`:
```go
func TestFlushRegionWindowMath(t *testing.T) {
	f := NewFake()
	d := New(f)
	// A 12-row-tall band at y=20, full width.
	rowData := make([]byte, 12*(256/2))
	if err := d.FlushRegion(rowData, 0, 20, 256, 12); err != nil {
		t.Fatal(err)
	}
	wantCmds := [][]byte{
		{0x15, 0x1C, 0x5B},       // full-width columns
		{0x75, byte(20), byte(31)}, // rows 20..31
		{0x5C},
	}
	if got := cmds(f.Ops); !reflect.DeepEqual(got, wantCmds) {
		t.Fatalf("region cmds = %#v, want %#v", got, wantCmds)
	}
}

func TestFlushRegionOffsetColumns(t *testing.T) {
	f := NewFake()
	d := New(f)
	// x=8 (2 columns in), w=8 (2 columns wide): col start 0x1C+2, end +3.
	if err := d.FlushRegion(make([]byte, 4*(8/2)), 8, 0, 8, 4); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x15, 0x1C + 2, 0x1C + 3}
	if got := cmds(f.Ops)[0]; !reflect.DeepEqual(got, want) {
		t.Fatalf("col window = % X, want % X", got, want)
	}
}

func TestFlushRegionAlignment(t *testing.T) {
	d := New(NewFake())
	if err := d.FlushRegion(make([]byte, 10), 1, 0, 8, 1); err == nil {
		t.Fatal("expected error for x not 4-aligned")
	}
	if err := d.FlushRegion(make([]byte, 10), 0, 0, 6, 1); err == nil {
		t.Fatal("expected error for w not 4-aligned")
	}
}

func TestFlushRegionDataLenCheck(t *testing.T) {
	d := New(NewFake())
	if err := d.FlushRegion(make([]byte, 5), 0, 0, 8, 2); err == nil {
		t.Fatal("expected error for wrong rowData length")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/display/ -run TestFlushRegion`
Expected: FAIL — undefined `FlushRegion`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/display/ssd1322.go`:
```go
// FlushRegion writes a 4-pixel-aligned sub-rectangle. rowData is row-major,
// w/2 packed bytes per row, h rows. x and w must be multiples of 4.
func (d *SSD1322) FlushRegion(rowData []byte, x, y, w, h int) error {
	if x%4 != 0 || w%4 != 0 {
		return fmt.Errorf("display: region x=%d w=%d must be 4-pixel aligned", x, w)
	}
	if x < 0 || y < 0 || x+w > 256 || y+h > 64 || w <= 0 || h <= 0 {
		return fmt.Errorf("display: region %d,%d %dx%d out of 256x64", x, y, w, h)
	}
	if want := h * (w / 2); len(rowData) != want {
		return fmt.Errorf("display: rowData is %d bytes, want %d", len(rowData), want)
	}
	cs := byte(colStart + x/4)
	ce := byte(colStart + (x+w)/4 - 1)
	if err := d.t.Command(cmdSetColumnAddr, cs, ce); err != nil {
		return err
	}
	if err := d.t.Command(cmdSetRowAddr, byte(y), byte(y+h-1)); err != nil {
		return err
	}
	if err := d.t.Command(cmdWriteRAM); err != nil {
		return err
	}
	for _, c := range chunk(rowData, maxChunk) {
		if err := d.t.Data(c); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/display/ -run TestFlushRegion -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Commit**

```bash
git add internal/display/ssd1322.go internal/display/ssd1322_test.go
git commit -m "feat(display): 4-pixel-aligned partial-region flush"
```

---

### Task 8: Font loading + text rasterization (golden-image harness)

Loads the Dot Matrix TTFs via sfnt and rasterizes anti-aliased text to an alpha bitmap. Establishes the golden-image test harness used by all render tasks.

**Files:**
- Copy: the three TTFs into `internal/render/fonts/` (from `reference/src/fonts/`)
- Create: `internal/render/font.go`
- Create: `internal/render/golden.go` (test helper, `//go:build ignore`? no — normal file, used only by tests)
- Test: `internal/render/font_test.go`
- Create (generated): `internal/render/testdata/*.png`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Font struct { ... }`
  - `func LoadFont(ttf []byte, pxSize float64) (*Font, error)` — DPI 72, HintingNone.
  - Embedded fonts: `var (RegularTTF, BoldTTF, BoldTallTTF []byte)` via `go:embed`.
  - `func (f *Font) Measure(s string) (w, h int)` — pixel bounding box (advance width, ascent+descent).
  - `func (f *Font) RenderText(s string) *image.Alpha` — tight-left alpha bitmap, baseline at ascent.
  - Test helper `assertGolden(t *testing.T, name string, fb *Framebuffer)` + `-update` flag.

- [ ] **Step 1: Copy fonts + write the failing test**

```bash
mkdir -p internal/render/fonts internal/render/testdata
cp "reference/src/fonts/Dot Matrix Regular.ttf" internal/render/fonts/
cp "reference/src/fonts/Dot Matrix Bold.ttf" internal/render/fonts/
cp "reference/src/fonts/Dot Matrix Bold Tall.ttf" internal/render/fonts/
```

`internal/render/font_test.go`:
```go
package render

import "testing"

func TestLoadFontAndMeasure(t *testing.T) {
	f, err := LoadFont(RegularTTF, 10)
	if err != nil {
		t.Fatal(err)
	}
	w, h := f.Measure("12:34 London Paddington")
	if w <= 0 || h <= 0 {
		t.Fatalf("Measure returned non-positive size: %dx%d", w, h)
	}
	if h > 14 {
		t.Fatalf("10px font row height %d unexpectedly tall", h)
	}
}

func TestRenderTextProducesInk(t *testing.T) {
	f, err := LoadFont(BoldTTF, 10)
	if err != nil {
		t.Fatal(err)
	}
	img := f.RenderText("Platform 1")
	ink := 0
	for _, a := range img.Pix {
		if a > 0 {
			ink++
		}
	}
	if ink == 0 {
		t.Fatal("RenderText produced a blank bitmap")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run 'TestLoadFont|TestRenderText'`
Expected: FAIL — undefined `LoadFont`, `RegularTTF`, `BoldTTF`.

- [ ] **Step 3: Write minimal implementation**

`internal/render/font.go`:
```go
package render

import (
	_ "embed"
	"image"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed "fonts/Dot Matrix Regular.ttf"
var RegularTTF []byte

//go:embed "fonts/Dot Matrix Bold.ttf"
var BoldTTF []byte

//go:embed "fonts/Dot Matrix Bold Tall.ttf"
var BoldTallTTF []byte

// Font is a sfnt face rasterized at a fixed pixel size with no hinting.
type Font struct {
	face    font.Face
	ascent  int
	descent int
}

// LoadFont parses a TTF and builds a face at pxSize pixels (DPI 72 ⇒ px==pt).
func LoadFont(ttf []byte, pxSize float64) (*Font, error) {
	f, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{
		Size:    pxSize,
		DPI:     72,
		Hinting: font.HintingNone,
	})
	if err != nil {
		return nil, err
	}
	m := face.Metrics()
	return &Font{
		face:    face,
		ascent:  m.Ascent.Ceil(),
		descent: m.Descent.Ceil(),
	}, nil
}

// Measure returns the pixel advance width and the line height (ascent+descent).
func (f *Font) Measure(s string) (w, h int) {
	adv := font.MeasureString(f.face, s)
	return adv.Ceil(), f.ascent + f.descent
}

// RenderText rasterizes s into a left-aligned alpha bitmap (0=transparent,
// 255=full ink), with the baseline placed at ascent so glyphs fit top-down.
func (f *Font) RenderText(s string) *image.Alpha {
	w, h := f.Measure(s)
	if w == 0 {
		w = 1
	}
	dst := image.NewAlpha(image.Rect(0, 0, w, h))
	d := font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(image.White), // white ⇒ alpha=255 ink
		Face: f.face,
		Dot:  fixed.P(0, f.ascent),
	}
	d.DrawString(s)
	return dst
}
```

> `image.NewUniform(image.White)` drawn onto an `*image.Alpha` yields alpha = coverage (anti-aliased edges 0–255). This is the AA ink we later scale by brightness level.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/render/ -run 'TestLoadFont|TestRenderText' -v`
Expected: PASS.

- [ ] **Step 5: Add the golden-image harness + a first golden**

`internal/render/golden.go`:
```go
package render

import (
	"bytes"
	"flag"
	"image"
	"image/png"
	"os"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden images")

// toGray renders the framebuffer as an 8-bit grey image (level*17 ⇒ 0..255).
func toGray(fb *Framebuffer) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, fb.W, fb.H))
	for i, lvl := range fb.Pix {
		g.Pix[i] = lvl * 17
	}
	return g
}

// assertGolden compares fb against testdata/<name>.png, or regenerates it
// under -update.
func assertGolden(t *testing.T, name string, fb *Framebuffer) {
	t.Helper()
	path := "testdata/" + name + ".png"
	g := toGray(fb)
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

Add a smoke test that exercises the harness (this becomes the pattern for later tasks):
```go
func TestGoldenHarness(t *testing.T) {
	f, err := LoadFont(BoldTTF, 20)
	if err != nil {
		t.Fatal(err)
	}
	fb := New(256, 64)
	fb.BlitAlpha(f.RenderText("12:34"), 4, 4, 15) // BlitAlpha lands in Task 9
	assertGolden(t, "harness_smoke", fb)
}
```

> This test **will not compile until Task 9 adds `BlitAlpha`.** Write it now but keep it commented out, or move it to Task 9 Step 1. Recommended: defer `TestGoldenHarness` to Task 9. For this task, verify the harness compiles by generating no golden yet.

- [ ] **Step 6: Commit**

```bash
git add internal/render/font.go internal/render/golden.go internal/render/font_test.go internal/render/fonts/
git commit -m "feat(render): sfnt font loading, text rasterization, golden-image harness"
```

---

### Task 9: BlitAlpha — composite text into the framebuffer (golden-image)

**Files:**
- Modify: `internal/render/framebuffer.go`
- Test: `internal/render/framebuffer_test.go`
- Test: `internal/render/font_test.go` (enable `TestGoldenHarness`)
- Create (generated): `internal/render/testdata/harness_smoke.png`

**Interfaces:**
- Consumes: `Framebuffer`, `image.Alpha` from `RenderText`.
- Produces: `func (fb *Framebuffer) BlitAlpha(src *image.Alpha, x, y int, level byte)` — for each src pixel, `dst = round(alpha * level / 255)`, overwriting the src rectangle (matches the reference's `image.paste` semantics), clipped to bounds.

- [ ] **Step 1: Write the failing test**

Append to `internal/render/framebuffer_test.go`:
```go
import "image"

func TestBlitAlphaScalesByLevel(t *testing.T) {
	fb := New(2, 1)
	src := image.NewAlpha(image.Rect(0, 0, 2, 1))
	src.Pix[0] = 255 // full ink
	src.Pix[1] = 0   // transparent
	fb.BlitAlpha(src, 0, 0, 15)
	if fb.At(0, 0) != 15 {
		t.Fatalf("full ink at level 15 = %d, want 15", fb.At(0, 0))
	}
	if fb.At(1, 0) != 0 {
		t.Fatalf("transparent px = %d, want 0", fb.At(1, 0))
	}
}

func TestBlitAlphaMidLevel(t *testing.T) {
	fb := New(1, 1)
	src := image.NewAlpha(image.Rect(0, 0, 1, 1))
	src.Pix[0] = 128 // ~half
	fb.BlitAlpha(src, 0, 0, 15)
	// round(128*15/255) = round(7.53) = 8
	if fb.At(0, 0) != 8 {
		t.Fatalf("mid ink = %d, want 8", fb.At(0, 0))
	}
}

func TestBlitAlphaClips(t *testing.T) {
	fb := New(2, 2)
	src := image.NewAlpha(image.Rect(0, 0, 2, 2))
	for i := range src.Pix {
		src.Pix[i] = 255
	}
	fb.BlitAlpha(src, 1, 1, 15) // only (1,1) lands
	if fb.At(1, 1) != 15 || fb.At(0, 0) != 0 {
		t.Fatalf("clip failed: (1,1)=%d (0,0)=%d", fb.At(1, 1), fb.At(0, 0))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestBlitAlpha`
Expected: FAIL — undefined `BlitAlpha`.

- [ ] **Step 3: Write minimal implementation**

Append to `internal/render/framebuffer.go`:
```go
import "image"

// BlitAlpha composites an alpha bitmap at (x,y): each source coverage value
// (0–255) becomes level round(alpha*level/255), overwriting the source
// rectangle. Clipped to the framebuffer bounds.
func (fb *Framebuffer) BlitAlpha(src *image.Alpha, x, y int, level byte) {
	b := src.Bounds()
	for sy := b.Min.Y; sy < b.Max.Y; sy++ {
		for sx := b.Min.X; sx < b.Max.X; sx++ {
			a := int(src.AlphaAt(sx, sy).A)
			v := (a*int(level) + 127) / 255 // round to nearest
			fb.SetPixel(x+(sx-b.Min.X), y+(sy-b.Min.Y), byte(v))
		}
	}
}
```

> If `framebuffer.go` already imports a block, merge `"image"` in. `SetPixel` clamps/clips so no bounds math here.

- [ ] **Step 4: Enable the golden harness test + generate the golden**

Move `TestGoldenHarness` from Task 8 into `font_test.go` (uncommented). Then:

Run: `go test ./internal/render/ -run 'TestBlitAlpha|TestGoldenHarness' -update`
Then inspect `internal/render/testdata/harness_smoke.png` by eye (should read "12:34" in large font). Then re-run **without** `-update`:
Run: `go test ./internal/render/ -run 'TestBlitAlpha|TestGoldenHarness' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/render/framebuffer.go internal/render/framebuffer_test.go internal/render/font_test.go internal/render/testdata/harness_smoke.png
git commit -m "feat(render): BlitAlpha composites AA text into the framebuffer"
```

---

### Task 10: Scene/element engine + StaticText (golden-image)

**Files:**
- Create: `internal/render/scene.go`
- Create: `internal/render/element_statictext.go`
- Test: `internal/render/element_statictext_test.go`
- Create (generated): `internal/render/testdata/statictext_*.png`

**Interfaces:**
- Consumes: `Framebuffer`, `Font`, `BlitAlpha`.
- Produces:
  - `type Align int` with `AlignLeft`, `AlignCenter`, `AlignRight`.
  - `type Element interface { Render(fb *Framebuffer, tick int, now time.Time) }`
  - `type Scene struct { Elements []Element }` + `func (s *Scene) Render(fb *Framebuffer, tick int, now time.Time)` (renders in order).
  - `type StaticText struct { Font *Font; Text string; X, Y, W, H int; Align Align; Level byte }` implementing `Element`. Horizontal align within `W`; vertically top-aligned within `H` (matches reference default `vertical_align="top"`).

- [ ] **Step 1: Write the failing test**

`internal/render/element_statictext_test.go`:
```go
package render

import (
	"testing"
	"time"
)

func mustFont(t *testing.T, ttf []byte, px float64) *Font {
	t.Helper()
	f, err := LoadFont(ttf, px)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStaticTextLeftGolden(t *testing.T) {
	fb := New(256, 12)
	el := &StaticText{Font: mustFont(t, RegularTTF, 10), Text: "London Paddington",
		X: 0, Y: 0, W: 256, H: 12, Align: AlignLeft, Level: 15}
	el.Render(fb, 0, time.Time{})
	assertGolden(t, "statictext_left", fb)
}

func TestStaticTextRightAlignsToRightEdge(t *testing.T) {
	fbL := New(256, 12)
	fbR := New(256, 12)
	txt := "1"
	(&StaticText{Font: mustFont(t, BoldTTF, 10), Text: txt, X: 0, Y: 0, W: 256, H: 12, Align: AlignLeft, Level: 15}).Render(fbL, 0, time.Time{})
	(&StaticText{Font: mustFont(t, BoldTTF, 10), Text: txt, X: 0, Y: 0, W: 256, H: 12, Align: AlignRight, Level: 15}).Render(fbR, 0, time.Time{})
	// Right-aligned ink must sit further right than left-aligned ink.
	leftmostInk := func(fb *Framebuffer) int {
		for x := 0; x < fb.W; x++ {
			for y := 0; y < fb.H; y++ {
				if fb.At(x, y) > 0 {
					return x
				}
			}
		}
		return fb.W
	}
	if leftmostInk(fbR) <= leftmostInk(fbL) {
		t.Fatalf("right-align did not shift ink right: L=%d R=%d", leftmostInk(fbL), leftmostInk(fbR))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestStaticText`
Expected: FAIL — undefined `StaticText`, `AlignLeft`, etc.

- [ ] **Step 3: Write minimal implementation**

`internal/render/scene.go`:
```go
package render

import "time"

// Align controls horizontal placement of text within an element's width.
type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Element draws itself into the framebuffer for a given frame tick and time.
type Element interface {
	Render(fb *Framebuffer, tick int, now time.Time)
}

// Scene is an ordered list of elements composited back-to-front.
type Scene struct {
	Elements []Element
}

// Render draws every element in order.
func (s *Scene) Render(fb *Framebuffer, tick int, now time.Time) {
	for _, e := range s.Elements {
		e.Render(fb, tick, now)
	}
}

// alignX returns the x offset within a box of width w for content of width cw.
func alignX(a Align, w, cw int) int {
	switch a {
	case AlignRight:
		return w - cw
	case AlignCenter:
		return (w - cw) / 2
	default:
		return 0
	}
}
```

`internal/render/element_statictext.go`:
```go
package render

import "time"

// StaticText renders a single line of text within a fixed box, horizontally
// aligned and top-anchored.
type StaticText struct {
	Font       *Font
	Text       string
	X, Y, W, H int
	Align      Align
	Level      byte
}

// Render composites the text into fb (tick/now unused — static content).
func (s *StaticText) Render(fb *Framebuffer, _ int, _ time.Time) {
	if s.Text == "" {
		return
	}
	cw, _ := s.Font.Measure(s.Text)
	dx := alignX(s.Align, s.W, cw)
	fb.BlitAlpha(s.Font.RenderText(s.Text), s.X+dx, s.Y, s.Level)
}
```

- [ ] **Step 4: Generate goldens + verify pass**

Run: `go test ./internal/render/ -run TestStaticText -update`
Inspect `testdata/statictext_left.png`. Re-run without `-update`:
Run: `go test ./internal/render/ -run TestStaticText -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/render/scene.go internal/render/element_statictext.go internal/render/element_statictext_test.go internal/render/testdata/statictext_left.png
git commit -m "feat(render): scene/element engine + StaticText with alignment"
```

---

### Task 11: Clock element (golden-image, injected time)

Reproduces the reference clock: `HH:MM` in boldlarge (20px) with `:SS` in boldtall (10px) offset, centered, top of a 14px row.

**Files:**
- Create: `internal/render/element_clock.go`
- Test: `internal/render/element_clock_test.go`
- Create (generated): `internal/render/testdata/clock_*.png`

**Interfaces:**
- Consumes: `Font`, `BlitAlpha`, `Measure`, `alignX`.
- Produces: `type Clock struct { Large, Tall *Font; W int; Level byte }` implementing `Element`. Renders using the `now` passed to `Render` (so tests inject a fixed time). Layout mirrors reference: `HH:MM` and `:SS` laid side by side and centered as a unit; seconds baseline dropped 5px (reference used y=5 for the tall seconds vs y=0 for hourmin).

- [ ] **Step 1: Write the failing test**

`internal/render/element_clock_test.go`:
```go
package render

import (
	"testing"
	"time"
)

func TestClockGolden(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, time.UTC))
	assertGolden(t, "clock_123456", fb)
}

func TestClockIsCentered(t *testing.T) {
	fb := New(256, 14)
	c := &Clock{Large: mustFont(t, BoldTTF, 20), Tall: mustFont(t, BoldTallTTF, 10), W: 256, Level: 15}
	c.Render(fb, 0, time.Date(2026, 7, 2, 12, 34, 56, 0, time.UTC))
	// Ink should be roughly centered: left margin ≈ right margin (±8px).
	var minX, maxX = fb.W, 0
	for x := 0; x < fb.W; x++ {
		for y := 0; y < fb.H; y++ {
			if fb.At(x, y) > 0 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	left := minX
	right := fb.W - 1 - maxX
	if diff := left - right; diff > 8 || diff < -8 {
		t.Fatalf("clock not centered: leftMargin=%d rightMargin=%d", left, right)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestClock`
Expected: FAIL — undefined `Clock`.

- [ ] **Step 3: Write minimal implementation**

`internal/render/element_clock.go`:
```go
package render

import (
	"time"
)

// Clock renders HH:MM in a large font with :SS in a tall font beside it,
// centered horizontally. Mirrors the reference board's clock layout.
type Clock struct {
	Large *Font // HH:MM, ~20px
	Tall  *Font // :SS, ~10px
	W     int
	Level byte
}

const clockSecondsDrop = 5 // reference offset: seconds sit 5px lower

// Render draws the clock for the given time.
func (c *Clock) Render(fb *Framebuffer, _ int, now time.Time) {
	hourmin := now.Format("15:04")
	seconds := now.Format(":05")
	w1, _ := c.Large.Measure(hourmin)
	w2, _ := c.Tall.Measure(seconds)
	margin := alignX(AlignCenter, c.W, w1+w2)
	fb.BlitAlpha(c.Large.RenderText(hourmin), margin, 0, c.Level)
	fb.BlitAlpha(c.Tall.RenderText(seconds), margin+w1, clockSecondsDrop, c.Level)
}
```

- [ ] **Step 4: Generate goldens + verify pass**

Run: `go test ./internal/render/ -run TestClock -update`
Inspect `testdata/clock_123456.png` (should read "12:34:56"). Re-run:
Run: `go test ./internal/render/ -run TestClock -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/render/element_clock.go internal/render/element_clock_test.go internal/render/testdata/clock_123456.png
git commit -m "feat(render): Clock element (HH:MM + :SS), injected time"
```

---

### Task 12: ScrollingText element — integer-pixel scroll (golden-image over ticks)

Reproduces the reference scrolling row: text wider than the box scrolls right-to-left one pixel per tick, with a pause at the start; text that fits is aligned and static.

**Files:**
- Create: `internal/render/element_scrollingtext.go`
- Test: `internal/render/element_scrollingtext_test.go`
- Create (generated): `internal/render/testdata/scroll_*.png`

**Interfaces:**
- Consumes: `Font`, `BlitAlpha`, `Measure`, `RenderText`, `alignX`.
- Produces: `type ScrollingText struct { Font *Font; Text string; X, Y, W, H int; Level byte; PauseTicks int }` implementing `Element`. Deterministic in `tick`: offset is a pure function of `tick`, so golden frames at fixed ticks are reproducible. `PauseTicks` defaults (0 ⇒ 60, matching the reference's 60-frame pause) via a helper.

- [ ] **Step 1: Write the failing test**

`internal/render/element_scrollingtext_test.go`:
```go
package render

import (
	"testing"
	"time"
)

func TestScrollingShortTextStaticGolden(t *testing.T) {
	fb := New(256, 12)
	el := &ScrollingText{Font: mustFont(t, RegularTTF, 10), Text: "On time",
		X: 0, Y: 0, W: 256, H: 12, Level: 15}
	el.Render(fb, 0, time.Time{})
	assertGolden(t, "scroll_short_t0", fb)
}

func TestScrollingLongTextMovesLeft(t *testing.T) {
	long := "This train is formed of 8 coaches. Please mind the gap between the train and the platform edge."
	f := mustFont(t, RegularTTF, 10)
	pause := 5
	// During pause, offset is fixed; after pause it advances one px/tick.
	off0 := scrollOffset(f, long, 256, pause, pause)     // first moving frame
	off1 := scrollOffset(f, long, 256, pause, pause+1)   // next moving frame
	if off1 != off0+1 {
		t.Fatalf("expected 1px/tick after pause: off0=%d off1=%d", off0, off1)
	}
	if p := scrollOffset(f, long, 256, pause, 0); p != 0 {
		t.Fatalf("expected 0 offset during pause, got %d", p)
	}
}

func TestScrollingLongTextGoldenMidScroll(t *testing.T) {
	fb := New(256, 12)
	el := &ScrollingText{Font: mustFont(t, RegularTTF, 10),
		Text: "This train is formed of 8 coaches. Mind the gap.",
		X: 0, Y: 0, W: 256, H: 12, Level: 15, PauseTicks: 5}
	el.Render(fb, 40, time.Time{})
	assertGolden(t, "scroll_long_t40", fb)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/render/ -run TestScrolling`
Expected: FAIL — undefined `ScrollingText`, `scrollOffset`.

- [ ] **Step 3: Write minimal implementation**

`internal/render/element_scrollingtext.go`:
```go
package render

import "time"

const defaultPauseTicks = 60 // reference paused 60 frames before scrolling

// ScrollingText renders one line inside a box. If the text is wider than the
// box it scrolls right-to-left at one pixel per tick after an initial pause;
// otherwise it is left-aligned and static.
type ScrollingText struct {
	Font       *Font
	Text       string
	X, Y, W, H int
	Level      byte
	PauseTicks int // 0 ⇒ defaultPauseTicks
}

// scrollOffset is the pure integer-pixel scroll offset for a given tick.
// Returns 0 while the text fits the box. During [0,pause) the offset is 0;
// after that it advances one pixel per tick, wrapping so the scroll loops.
func scrollOffset(f *Font, text string, boxW, pause, tick int) int {
	tw, _ := f.Measure(text)
	if tw <= boxW {
		return 0
	}
	if pause <= 0 {
		pause = defaultPauseTicks
	}
	if tick < pause {
		return 0
	}
	// Total travel: text scrolls until it has fully passed, then repeats.
	travel := tw + boxW
	return (tick - pause) % travel
}

// Render composites the (possibly scrolled) text into fb.
func (s *ScrollingText) Render(fb *Framebuffer, tick int, _ time.Time) {
	if s.Text == "" {
		return
	}
	img := s.Font.RenderText(s.Text)
	tw := img.Bounds().Dx()
	if tw <= s.W {
		fb.BlitAlpha(img, s.X, s.Y, s.Level) // left-aligned, static
		return
	}
	off := scrollOffset(s.Font, s.Text, s.W, s.PauseTicks, tick)
	// Draw so the text enters from the right and exits left: start at box
	// right edge, move left by off.
	fb.BlitAlpha(img, s.X+s.W-off, s.Y, s.Level)
}
```

> `BlitAlpha` clips to the framebuffer, so the parts of the scrolling text outside the box are simply not drawn. Because a `ScrollingText` in Plan C will own its own 256×12 framebuffer band (composited by the scene), clipping to `s.W` is exact there; when rendered directly into a full-width fb as in tests, ensure the box spans the full width (`W=256`).

- [ ] **Step 4: Generate goldens + verify pass**

Run: `go test ./internal/render/ -run TestScrolling -update`
Inspect the three PNGs. Re-run:
Run: `go test ./internal/render/ -run TestScrolling -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/render/element_scrollingtext.go internal/render/element_scrollingtext_test.go internal/render/testdata/scroll_*.png
git commit -m "feat(render): ScrollingText with integer-pixel scroll + pause"
```

---

### Task 13: periph.io SPI transport (hardware wiring)

The real `Transport` for the Pi. Pure protocol logic is already unit-tested via the fake; this task wires periph.io's SPI + GPIO. It builds and cross-compiles for arm64 but is only exercised on hardware (via Task 14).

**Files:**
- Create: `internal/display/periph.go`
- Test: `internal/display/periph_test.go`

**Interfaces:**
- Consumes: `Transport` interface, `maxChunk`.
- Produces:
  - `type PeriphConfig struct { SPIPort string; DCPin string; ResetPin string; MaxHz int64 }`
  - `func OpenPeriph(cfg PeriphConfig) (*PeriphTransport, error)`
  - `type PeriphTransport` implementing `Transport` (command byte with D/C low; args + data with D/C high; data chunked at `maxChunk`; `Reset()` pulses the reset pin).

- [ ] **Step 1: Write the failing test**

`internal/display/periph_test.go`:
```go
package display

import "testing"

// PeriphTransport must satisfy the Transport interface at compile time.
var _ Transport = (*PeriphTransport)(nil)

func TestOpenPeriphMissingDeviceErrors(t *testing.T) {
	// No SPI hardware in CI: opening a bogus port must error, not panic.
	_, err := OpenPeriph(PeriphConfig{SPIPort: "SPI9.9", DCPin: "GPIO25", ResetPin: "GPIO27", MaxHz: 16_000_000})
	if err == nil {
		t.Fatal("expected error opening nonexistent SPI port")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/display/ -run TestOpenPeriph`
Expected: FAIL — undefined `PeriphTransport`, `OpenPeriph`, `PeriphConfig`.

- [ ] **Step 3: Add dependency + write minimal implementation**

```bash
go get periph.io/x/conn/v3@latest
go get periph.io/x/host/v3@latest
```

`internal/display/periph.go`:
```go
package display

import (
	"fmt"
	"time"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

// PeriphConfig locates the SPI port and control GPIOs for the panel.
type PeriphConfig struct {
	SPIPort  string // e.g. "SPI0.0"
	DCPin    string // data/command select, e.g. "GPIO25"
	ResetPin string // reset, e.g. "GPIO27"
	MaxHz    int64  // SPI clock, e.g. 16_000_000
}

// PeriphTransport drives the SSD1322 over periph.io SPI + GPIO.
type PeriphTransport struct {
	port spi.PortCloser
	conn spi.Conn
	dc   gpio.PinIO
	rst  gpio.PinIO
}

// OpenPeriph initializes the host, opens the SPI port and control pins.
func OpenPeriph(cfg PeriphConfig) (*PeriphTransport, error) {
	if _, err := host.Init(); err != nil {
		return nil, fmt.Errorf("periph host init: %w", err)
	}
	port, err := spireg.Open(cfg.SPIPort)
	if err != nil {
		return nil, fmt.Errorf("open spi %q: %w", cfg.SPIPort, err)
	}
	conn, err := port.Connect(physic.Frequency(cfg.MaxHz)*physic.Hertz, spi.Mode0, 8)
	if err != nil {
		port.Close()
		return nil, fmt.Errorf("spi connect: %w", err)
	}
	dc := gpioreg.ByName(cfg.DCPin)
	rst := gpioreg.ByName(cfg.ResetPin)
	if dc == nil || rst == nil {
		port.Close()
		return nil, fmt.Errorf("gpio pin not found (dc=%q rst=%q)", cfg.DCPin, cfg.ResetPin)
	}
	return &PeriphTransport{port: port, conn: conn, dc: dc, rst: rst}, nil
}

// Command sends an opcode (D/C low) followed by any args (D/C high).
func (p *PeriphTransport) Command(cmd byte, args ...byte) error {
	if err := p.dc.Out(gpio.Low); err != nil {
		return err
	}
	if err := p.conn.Tx([]byte{cmd}, nil); err != nil {
		return err
	}
	if len(args) > 0 {
		return p.Data(args)
	}
	return nil
}

// Data sends payload bytes (D/C high) in spidev-safe chunks.
func (p *PeriphTransport) Data(b []byte) error {
	if err := p.dc.Out(gpio.High); err != nil {
		return err
	}
	for _, c := range chunk(b, maxChunk) {
		if err := p.conn.Tx(c, nil); err != nil {
			return err
		}
	}
	return nil
}

// Reset pulses the reset line low then high.
func (p *PeriphTransport) Reset() error {
	if err := p.rst.Out(gpio.Low); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	if err := p.rst.Out(gpio.High); err != nil {
		return err
	}
	time.Sleep(100 * time.Millisecond)
	return nil
}

// Close releases the SPI port.
func (p *PeriphTransport) Close() error { return p.port.Close() }
```

- [ ] **Step 4: Run test + build to verify**

Run: `go test ./internal/display/ -run TestOpenPeriph -v`
Expected: PASS (error path).
Run: `GOOS=linux GOARCH=arm64 go build ./...`
Expected: builds clean (cross-compiles for the Pi).

- [ ] **Step 5: Commit**

```bash
git add internal/display/periph.go internal/display/periph_test.go go.mod go.sum
git commit -m "feat(display): periph.io SPI/GPIO transport for SSD1322"
```

---

### Task 14: fps benchmark binary (hardware gate)

Times full-frame and partial flushes on the real panel to decide the render architecture: **full-frame flush every frame is the baseline; dirty-region tracking is built only if this benchmark shows full-frame can't clear 25–30fps.** Records the result so the decision is documented, not assumed.

**Files:**
- Create: `cmd/bench/main.go`
- Create: `docs/benchmarks/README.md` (results template)

**Interfaces:**
- Consumes: `display.OpenPeriph`, `display.SSD1322`, `render.Framebuffer`.
- Produces: a runnable arm64 binary printing per-op timings; a committed results doc.

- [ ] **Step 1: Write the benchmark binary**

`cmd/bench/main.go`:
```go
// Command bench times SSD1322 flush paths on real hardware to decide the
// render architecture (full-frame vs dirty-region). Runs on the Pi only.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mintopia/trainboard/internal/display"
	"github.com/mintopia/trainboard/internal/render"
)

func main() {
	frames := flag.Int("frames", 300, "frames per measurement")
	hz := flag.Int64("hz", 16_000_000, "SPI clock in Hz")
	spiPort := flag.String("spi", "SPI0.0", "SPI port name")
	dc := flag.String("dc", "GPIO25", "D/C GPIO pin")
	rst := flag.String("rst", "GPIO27", "reset GPIO pin")
	flag.Parse()

	tr, err := display.OpenPeriph(display.PeriphConfig{SPIPort: *spiPort, DCPin: *dc, ResetPin: *rst, MaxHz: *hz})
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tr.Close()
	d := display.New(tr)
	if err := d.Init(); err != nil {
		fmt.Fprintln(os.Stderr, "init:", err)
		os.Exit(1)
	}

	fb := render.New(256, 64)
	for i := range fb.Pix {
		fb.Pix[i] = byte(i % 16) // non-trivial pattern
	}
	full := fb.Pack()

	measure := func(name string, n int, fn func() error) {
		start := time.Now()
		for i := 0; i < n; i++ {
			if err := fn(); err != nil {
				fmt.Fprintln(os.Stderr, name, "err:", err)
				os.Exit(1)
			}
		}
		el := time.Since(start)
		per := el / time.Duration(n)
		fmt.Printf("%-16s %d frames  %8.3f ms/frame  %6.1f fps\n",
			name, n, float64(per.Microseconds())/1000, float64(time.Second)/float64(per))
	}

	// Partial-flush row bands (4-pixel-aligned full width).
	band12 := make([]byte, 12*(256/2))
	band24 := make([]byte, 24*(256/2))

	measure("full-frame", *frames, func() error { return d.Flush(full) })
	measure("region-256x12", *frames, func() error { return d.FlushRegion(band12, 0, 0, 256, 12) })
	measure("region-256x24", *frames, func() error { return d.FlushRegion(band24, 0, 0, 256, 24) })
}
```

- [ ] **Step 2: Verify it builds (both host and arm64)**

Run: `go build ./cmd/bench/ && GOOS=linux GOARCH=arm64 go build ./cmd/bench/`
Expected: builds clean on both. (No unit test — hardware-only.)

- [ ] **Step 3: Write the results doc template**

`docs/benchmarks/README.md`:
```markdown
# SSD1322 flush benchmarks

Run on the target Pi Zero 2 W + SSD1322 to gate the render architecture.

## How to run

    GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
    scp bench pi@trainboard:/tmp/ && ssh pi@trainboard /tmp/bench --frames 300 --hz 16000000

## Results (fill in)

| SPI Hz | full-frame ms | full-frame fps | 256x12 ms | 256x24 ms | date | notes |
|--------|---------------|----------------|-----------|-----------|------|-------|
|        |               |                |           |           |      |       |

## Decision

- [ ] Full-frame flush clears ≥25fps ⇒ **keep full-frame baseline, do NOT build dirty-region tracking** (delete the complexity from the render loop plan).
- [ ] Full-frame flush is below target ⇒ record the ceiling and design dirty-region flush in Plan C using `FlushRegion`.

Update ADR 0002 with the measured numbers and the decision.
```

- [ ] **Step 4: Commit**

```bash
git add cmd/bench/main.go docs/benchmarks/README.md
git commit -m "feat(bench): SSD1322 flush benchmark to gate render architecture"
```

- [ ] **Step 5: (Hardware, out-of-band) run + record**

On the Pi: build, copy, run `bench`; paste numbers into `docs/benchmarks/README.md`; tick the decision box; update ADR 0002. This is the gate that closes the "fps unknown" risk in `PLAN.md`.

---

## Self-Review

**1. Spec coverage (against `PLAN.md` M1 items 1–3 + CI):**
- M1.1 native SSD1322 driver, chunked writes, 4-px/2-byte column units, 0x1C offset, SPI behind interface + fake → Tasks 2,3,6,7,13. ✓
- M1.2 full-frame flush baseline + hardware benchmark gating dirty-region → Tasks 6,14. ✓
- M1.3 sfnt (not freetype) rasterization, cached glyph output, AA edges, integer-pixel scroll, layout from reference frames, Clock/StaticText/ScrollingText, golden-image tests → Tasks 8–12. ✓ (NextService/RemainingServices are board-row composites deferred to Plan C, which composes StaticText/ScrollingText — noted.)
- CI hard gate (tests+lint+vet) → Task 1. ✓
- Deferred to Plan B: `data`, `config`. Deferred to Plan C: `board` scene contract + exact-pixel row geometry, runtime poller/atomic snapshot, clock-not-synced, boot SLA, observability. Explicitly scoped out at top. ✓

**2. Placeholder scan:** No "TBD"/"handle appropriately". The only intentional out-of-band steps are hardware runs (Task 14 Step 5, Task 13 hardware validation) — these are real actions with instructions, not placeholders. Init-sequence bytes are concrete with a validation note.

**3. Type consistency:** `Framebuffer`/`New`/`Pack`/`SetPixel`/`At`/`BlitAlpha` consistent across Tasks 5,9,10–12. `Transport`/`Command`/`Data`/`Reset`/`Close` consistent Tasks 2,3,6,7,13. `SSD1322`/`New`/`Init`/`SetContrast`/`Flush`/`FlushRegion` consistent. `Element.Render(fb, tick, now)` signature identical across StaticText/Clock/ScrollingText and `Scene.Render`. `Align`/`alignX` shared. `scrollOffset` signature matches its test. `chunk`/`maxChunk` shared Tasks 6,7,13. ✓

**Known follow-ups for Plan C:** derive exact per-row Y offsets and the NextService/RemainingServices composite rows from captured reference frames; wire the render loop (full-frame flush vs dirty-region per the Task 14 result); powersaving contrast schedule drives `SetContrast`.
