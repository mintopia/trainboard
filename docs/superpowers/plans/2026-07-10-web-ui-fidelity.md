# M7 Web UI Fidelity Fast-Follow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the web board preview a faithful emulation of the OLED panel (fonts, scene structure, motion), make CRS/TOC fields searchable by name, adapt the layout for desktop, and fix status-page polish items — issues #61–#65, milestone M7.

**Architecture:** The board preview becomes a scaled 256×64 "stage" whose geometry and tick timing are copied verbatim from `internal/board` (`board.go` constants, `elements.go` animation math, `element_scrollingtext.go` scroll cycle), rendered by a rewritten `board.js` using transform-only animation. Search is two new offline lookups in `internal/stations` (station search over the existing 2,606-row CSV; a new ~31-row TOC table) exposed as public JSON endpoints, consumed by a new dependency-free `suggest.js` WAI-ARIA combobox. Desktop is a wide-breakpoint pass over the existing single-column CSS.

**Tech Stack:** Go 1.x stdlib + html/template + htmx (existing), vanilla ES5 JS (matches existing board.js style), one CSS file, pyftsubset (fonttools) for font subsetting at build-doc time only (no runtime dep).

## Global Constraints

- Page weight: any page ≤ 150KB over the wire including fonts (brief §4).
- WCAG AA: ≥4.5:1 body contrast, 44px touch targets, visible focus, reduced-motion alternatives, no color-only state (brief §4).
- No new Go module dependencies; no new JS libraries (htmx + vanilla only).
- All JS `textContent`-only for remote data — never innerHTML (existing board.js rule).
- Red/Green TDD; `make check` (vet + lint + test) green before every push (AGENTS.md).
- Branch: `feat/web-ui-fidelity` off `main`. Commits reference the issue: `(#61)` … `(#65)`.
- Panel geometry/timing constants in JS must cite their Go source file in a comment — they are duplicated, and the comment is the drift tripwire.
- `/api/stations` and `/api/tocs` follow `/api/station`'s posture exactly: public (no auth), exempt from setupGate, same rate-limit treatment (none — offline table lookups).

## Panel Reference Card (copy into JS comments; sources cited)

From `internal/board/board.go`:

```
W=256 H=64 RowH=12
ColOrderX=0 ColSchedX=17 ColSchedW=28 ColPlatformX=45 ColPlatformW=19
ColDestX=64 ColStatusW=40 ColStatusX=216
CallingLabelW=42 CallingListX=42 CallingListW=214
ServiceInfoY=24 RemainingY=36 ClockY=50 ClockH=14
```

From `internal/board/elements.go` (1 tick = 40ms):

```
nsStep=2 (next-service slide-in px/tick)
rsStep=2 rsPauseTicks=125 rsMoveTicks=RowH/rsStep=6 rsSegTicks=131
```

From `internal/render/element_scrollingtext.go`:

```
defaultPauseTicks=60; scroll 1px/tick; cycle = pause + textWidth + pause
offset: 0 while t<pause; t-pause while <textWidth; textWidth (blank hold) after
Static (offset 0 always) when text fits its box.
```

From `internal/board/fonts.go` + `element_clock.go`: rows use Dot Matrix Regular @10px;
clock is Dot Matrix Bold @20px "15:04" + Dot Matrix Bold Tall @10px ":05", seconds
baseline dropped 5px, the pair centered in W.

Scene rows (`scene_departures.go`): y=0 first service (slides up 2px/tick);
y=12 fixed-box "Calling at:" label (w=42, itself a ScrollingText) + calling list
scrolling in x=42 w=214; y=24 service-info ScrollingText full width; y=36 remaining
services band (vertical roll, see Task 3); y=50 clock.

Remaining-services strip (`elements.go:90-128`): strip rows are
[blank, svc2, svc3, …, svcN, dup-of-svc2], each 12px. Phase 1 (tick<6): slide in,
visible strip range [0,b) at band bottom, b=2(tick+1). Phase 2: t=tick-6, s=t/131,
w=t%131, step=min(2(w+1),12), u=12s+step, top = u≤12 ? u : 12+(u-12)%(12·n);
viewport shows strip [top, top+12).

Departure row columns (`row.go:rowElements`): ordinal left @x0 w17; scheduled
centered @x17 w28; platform (only if present) centered @x45 w19; destination left
@x64 w152; status right @x216 w40. Headcode never drawn.

---

### Task 1: Dot Matrix woff2 subsets, @font-face, served-font test

**Files:**
- Create: `internal/web/static/fonts/dotmatrix-regular.woff2`
- Create: `internal/web/static/fonts/dotmatrix-bold.woff2`
- Create: `internal/web/static/fonts/dotmatrix-bold-tall.woff2`
- Modify: `internal/web/static/style.css` (top @font-face block)
- Modify: `docs/design/fonts/README.md`
- Test: `internal/web/server_test.go` (extend the existing woff2 test at ~line 395)

**Interfaces:**
- Produces: CSS font families `"Dot Matrix"`, `"Dot Matrix Bold"`, `"Dot Matrix Bold Tall"` used by Task 3.

- [ ] **Step 1: Extend the failing test**

In `internal/web/server_test.go`, find the existing test (~line 395) that asserts `/static/fonts/rail-alphabet-dark.woff2` and `/static/fonts/rail-alphabet-light.woff2` are served, and add the three new paths to its list:

```go
		"/static/fonts/dotmatrix-regular.woff2",
		"/static/fonts/dotmatrix-bold.woff2",
		"/static/fonts/dotmatrix-bold-tall.woff2",
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run 'Static|Font' -v`
Expected: FAIL (404 for the three dotmatrix paths).

- [ ] **Step 3: Generate the subset woff2 files**

```bash
python3 -m venv /tmp/fontsub && /tmp/fontsub/bin/pip -q install fonttools brotli
cd /Users/mintopia/Projects/trainboard
/tmp/fontsub/bin/pyftsubset "internal/render/fonts/Dot Matrix Regular.ttf" \
  --unicodes="U+0020-007E,U+00A3,U+00B7" --flavor=woff2 \
  --output-file=internal/web/static/fonts/dotmatrix-regular.woff2
/tmp/fontsub/bin/pyftsubset "internal/render/fonts/Dot Matrix Bold.ttf" \
  --unicodes="U+0020-007E,U+00A3,U+00B7" --flavor=woff2 \
  --output-file=internal/web/static/fonts/dotmatrix-bold.woff2
/tmp/fontsub/bin/pyftsubset "internal/render/fonts/Dot Matrix Bold Tall.ttf" \
  --unicodes="U+0020-007E,U+00A3,U+00B7" --flavor=woff2 \
  --output-file=internal/web/static/fonts/dotmatrix-bold-tall.woff2
ls -la internal/web/static/fonts/
```

Expected: three new woff2 files, each well under 30KB (sources are 58–308KB TTFs; ASCII subset shrinks them hard). If any file exceeds 40KB, stop and flag it — the 150KB page budget is at risk.

- [ ] **Step 4: Add @font-face declarations**

At the top of `internal/web/static/style.css`, after the two existing Rail Alphabet `@font-face` blocks:

```css
@font-face {
  font-family: "Dot Matrix";
  src: url(/static/fonts/dotmatrix-regular.woff2) format("woff2");
  font-weight: 400; font-style: normal; font-display: swap;
}
@font-face {
  font-family: "Dot Matrix Bold";
  src: url(/static/fonts/dotmatrix-bold.woff2) format("woff2");
  font-weight: 700; font-style: normal; font-display: swap;
}
@font-face {
  font-family: "Dot Matrix Bold Tall";
  src: url(/static/fonts/dotmatrix-bold-tall.woff2) format("woff2");
  font-weight: 700; font-style: normal; font-display: swap;
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/web/ -run 'Static|Font' -v`
Expected: PASS.

- [ ] **Step 6: Document provenance**

Append to `docs/design/fonts/README.md`:

```markdown

## Dot Matrix (board preview)

The web board preview uses the same Dot Matrix Regular/Bold/Bold Tall TTFs the panel
renders with (embedded at `internal/render/fonts/`), subset to woff2 with:

  pyftsubset "internal/render/fonts/Dot Matrix <Weight>.ttf" \
    --unicodes="U+0020-007E,U+00A3,U+00B7" --flavor=woff2 \
    --output-file=internal/web/static/fonts/dotmatrix-<weight>.woff2

Same shipping posture as the panel itself: the fonts are already distributed inside
every release binary; the web subsets add no new rights exposure.
```

- [ ] **Step 7: Commit**

```bash
git add internal/web/static/fonts/ internal/web/static/style.css internal/web/server_test.go docs/design/fonts/README.md
git commit -m "feat(web): Dot Matrix woff2 subsets for the board preview (#61)"
```

---

### Task 2: `/api/board` reports board time

**Files:**
- Modify: `internal/web/handlers_board.go`
- Test: `internal/web/handlers_board_test.go`

**Interfaces:**
- Produces: `boardView.Time string` (json `"time"`, RFC3339, server wall-clock at response time). Task 3's clock and drift caption consume it.

- [ ] **Step 1: Write the failing test**

Add to `internal/web/handlers_board_test.go` (follow the file's existing handler-test style — it exercises `handleAPIBoard` via the served route; copy the setup from the nearest existing `/api/board` test):

```go
// The board clock on the web preview must show the BOARD's time, not the
// browser's — drift between them is diagnostic signal (#65). /api/board
// therefore carries the server's wall clock.
func TestAPIBoardIncludesServerTime(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/api/board", loginCookie(t, srv))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var v struct {
		Time string `json:"time"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, err := time.Parse(time.RFC3339, v.Time)
	if err != nil {
		t.Fatalf("time %q not RFC3339: %v", v.Time, err)
	}
	if d := time.Since(got); d < -5*time.Second || d > 5*time.Second {
		t.Fatalf("time %v not near now (delta %v)", got, d)
	}
}
```

(If the existing `/api/board` tests authenticate differently — e.g. a helper other than `loginCookie` — use that file's established helper verbatim.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestAPIBoardIncludesServerTime -v`
Expected: FAIL — `time ""` not RFC3339.

- [ ] **Step 3: Implement**

In `internal/web/handlers_board.go`:

Add the field to `boardView` (after `State`):

```go
	// Time is the server's wall clock when the view was built. The web
	// preview drives its clock from this (not the browser's clock) and
	// flags drift — clock disagreement is diagnostic signal (#65).
	Time string `json:"time"`
```

Set it in `handleAPIBoard` (NOT in `buildBoardView`, which stays pure for its unit tests), before encoding:

```go
	view := buildBoardView(snap, times)
	view.Time = time.Now().Format(rfc3339)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(view)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -run 'TestAPIBoard|BoardView' -v`
Expected: PASS (existing buildBoardView tests untouched — the field is zero there).

- [ ] **Step 5: Commit**

```bash
git add internal/web/handlers_board.go internal/web/handlers_board_test.go
git commit -m "feat(web): /api/board carries server time for the preview clock (#65)"
```

---

### Task 3: Faithful board renderer (board.js rewrite + board CSS)

**Files:**
- Rewrite: `internal/web/static/board.js`
- Modify: `internal/web/static/style.css` (replace the `/* Live board */` block)
- Modify: `internal/web/templates/status.html` (board container markup)
- Test: `internal/web/handlers_status_test.go` (markup assertions), attended visual acceptance

**Interfaces:**
- Consumes: `GET /api/board` JSON (`boardView` incl. Task 2's `time`), full `remaining` array (server already sends all rows — current JS was slicing to 1).
- Produces: `#board` wrapper with `.board-stage` child; caption element `#board-caption`.

This task makes the web preview structurally and temporally identical to the panel: same geometry, same fonts, same tick math. All constants cite their Go source. No fidelity shortcuts — where the panel does something odd (e.g. the "Calling at:" label is itself a ScrollingText in a 42px box), the JS does the same odd thing.

- [ ] **Step 1: Write the failing markup test**

In `internal/web/handlers_status_test.go` add:

```go
// The board preview is a fixed 256×64 stage scaled to its wrapper (#61).
// The wrapper keeps the role/aria of the old .board element.
func TestStatusBoardStageMarkup(t *testing.T) {
	srv, _ := newTestServer(t)
	body := getPath(t, srv.Handler(), "/", loginCookie(t, srv)).Body.String()
	for _, want := range []string{
		`class="boardwrap" id="board"`,
		`data-endpoint="/api/board"`,
		`role="img"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
	if strings.Contains(body, `class="board"`) {
		t.Errorf("old .board container still present")
	}
}
```

(Adapt helper names to the file's existing style, as in Task 2.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestStatusBoardStageMarkup -v`
Expected: FAIL — old `class="board"` markup.

- [ ] **Step 3: Update status.html board section**

In `internal/web/templates/status.html` replace:

```html
<section>
  <div class="board" id="board" data-endpoint="/api/board" role="img" aria-label="live departure board preview"></div>
  <p class="caption" id="board-caption">Live panel · rendered in your browser</p>
</section>
```

with:

```html
<section>
  <div class="boardwrap" id="board" data-endpoint="/api/board" role="img" aria-label="live departure board preview"></div>
  <p class="caption" id="board-caption">Live panel · rendered in your browser</p>
</section>
```

- [ ] **Step 4: Replace the board CSS**

In `internal/web/static/style.css`, delete the entire `/* Live board */` block (`.board`, `.board .row`, `.board .dest`, `.board .center`, `.board .marquee-clip`, `.board .marquee`, `@keyframes marquee`, `.board .clockline`, `.board.stale`, and the `@media (prefers-reduced-motion…)` marquee override — keep `.caption`). Replace with:

```css
/* Live board — 256×64 panel emulation, scaled to fit (#61).
   Geometry mirrors internal/board/board.go; board.js owns all layout. */
.boardwrap {
  aspect-ratio: 4 / 1; background: #000; border-radius: 4px;
  position: relative; overflow: hidden;
}
.boardwrap.stale { filter: grayscale(1) brightness(.8); }
.board-stage {
  position: absolute; top: 0; left: 0; width: 256px; height: 64px;
  transform-origin: 0 0;
  font-family: "Dot Matrix", var(--mono); color: var(--amber);
  font-size: 10px; line-height: 12px;
}
.board-stage .clip { position: absolute; overflow: hidden; }
.board-stage .t { position: absolute; white-space: pre; will-change: transform; }
```

- [ ] **Step 5: Rewrite board.js**

Replace `internal/web/static/board.js` entirely with:

```js
// Live board renderer: polls /api/board and emulates the SSD1322 panel.
// Fidelity contract (#61): geometry, fonts, and tick timing are copied from
// the panel renderer — constants cite their Go sources and MUST track them:
//   internal/board/board.go        (pixel geometry)
//   internal/board/elements.go     (slide-in + remaining-services roll)
//   internal/render/element_scrollingtext.go (scroll cycle)
//   internal/board/fonts.go, internal/render/element_clock.go (fonts, clock)
// All content set via textContent — never innerHTML — the data is remote text.
(function () {
  "use strict";
  var root = document.getElementById("board");
  if (!root) return;
  var caption = document.getElementById("board-caption");
  var endpoint = root.dataset.endpoint || "/api/board";
  var reduced = window.matchMedia("(prefers-reduced-motion: reduce)");

  // --- Panel geometry (internal/board/board.go) ---
  var W = 256, H = 64, ROW_H = 12;
  var COL_ORDER_X = 0, COL_SCHED_X = 17, COL_SCHED_W = 28;
  var COL_PLAT_X = 45, COL_PLAT_W = 19, COL_DEST_X = 64;
  var COL_STATUS_X = 216, COL_STATUS_W = 40;
  var CALLING_LABEL_W = 42, CALLING_X = 42, CALLING_W = 214;
  var SERVICE_Y = 24, REMAINING_Y = 36, CLOCK_Y = 50;
  // --- Timing (internal/board/elements.go; 1 tick = 40ms) ---
  var TICK_MS = 40;
  var NS_STEP = 2;                       // next-service slide px/tick
  var RS_STEP = 2, RS_PAUSE = 125;       // remaining-services roll
  var RS_MOVE = ROW_H / RS_STEP;         // 6 ticks
  var RS_SEG = RS_PAUSE + RS_MOVE;       // 131 ticks
  var SCROLL_PAUSE = 60;                 // element_scrollingtext.go

  var FONT_REG = '10px "Dot Matrix", monospace';
  var FONT_CLOCK = '20px "Dot Matrix Bold", monospace';
  var FONT_CLOCK_SEC = '10px "Dot Matrix Bold Tall", monospace';

  var measureCtx = document.createElement("canvas").getContext("2d");
  function textW(text, font) {
    measureCtx.font = font || FONT_REG;
    return Math.ceil(measureCtx.measureText(text).width);
  }

  // --- Stage & scaling ---
  var stage = document.createElement("div");
  stage.className = "board-stage";
  root.appendChild(stage);
  function rescale() {
    stage.style.transform = "scale(" + root.clientWidth / W + ")";
  }
  if (window.ResizeObserver) new ResizeObserver(rescale).observe(root);
  window.addEventListener("resize", rescale);
  rescale();

  function el(tag, cls) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    return e;
  }
  function place(e, x, y, w, h) {
    e.style.left = x + "px"; e.style.top = y + "px";
    e.style.width = w + "px"; e.style.height = h + "px";
    return e;
  }

  // staticText mirrors render.StaticText: text drawn once inside a box with
  // left/center/right alignment, clipped.
  function staticText(parent, text, x, y, w, align, font) {
    var clip = place(el("div", "clip"), x, y, w, ROW_H);
    var t = el("span", "t");
    t.textContent = text;
    if (font) t.style.font = font;
    var tw = textW(text, font);
    var dx = align === "center" ? Math.max(0, (w - tw) >> 1)
           : align === "right" ? Math.max(0, w - tw) : 0;
    t.style.left = dx + "px"; t.style.top = "0";
    clip.appendChild(t);
    parent.appendChild(clip);
  }

  // scrollOffset mirrors render.scrollOffset exactly: static while the text
  // fits; else hold SCROLL_PAUSE, travel 1px/tick until fully out (tw),
  // hold blank SCROLL_PAUSE, wrap.
  function scrollOffset(tw, boxW, tick) {
    if (tw <= boxW) return 0;
    var cycle = SCROLL_PAUSE + tw + SCROLL_PAUSE;
    var t = tick % cycle;
    if (t < SCROLL_PAUSE) return 0;
    var off = t - SCROLL_PAUSE;
    return off < tw ? off : tw;
  }

  // Animated elements register an update(tick) here; one rAF loop drives all.
  var animated = [];
  // Epochs preserve each element's animation phase across identical redraws:
  // key -> {text, t0}. A changed text restarts only that element (#61).
  var epochs = {};
  function epoch(key, text, now) {
    var e = epochs[key];
    if (!e || e.text !== text) e = epochs[key] = { text: text, t0: now };
    return e.t0;
  }

  function scrollingText(parent, key, text, x, y, w, now) {
    var clip = place(el("div", "clip"), x, y, w, ROW_H);
    var t = el("span", "t");
    t.textContent = text;
    t.style.left = "0"; t.style.top = "0";
    clip.appendChild(t);
    parent.appendChild(clip);
    var tw = textW(text);
    if (tw <= w || reduced.matches) return; // static (panel truncates only by clip)
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      t.style.transform = "translateX(" + -scrollOffset(tw, w, tick) + "px)";
    });
  }

  // rowInto mirrors board.rowElements: six-column departure row.
  function rowInto(parent, s, y) {
    staticText(parent, s.order + ordSuffix(s.order), COL_ORDER_X, y, COL_SCHED_X, "left");
    staticText(parent, s.scheduled, COL_SCHED_X, y, COL_SCHED_W, "center");
    if (s.platform) staticText(parent, s.platform, COL_PLAT_X, y, COL_PLAT_W, "center");
    staticText(parent, s.destination, COL_DEST_X, y, COL_STATUS_X - COL_DEST_X, "left");
    staticText(parent, s.status, COL_STATUS_X, y, COL_STATUS_W, "right");
  }
  // Server sends order as an int; suffix mirrors board.Ordinal.
  function ordSuffix(n) {
    var m = n % 100;
    if (m >= 11 && m <= 13) return "th";
    switch (n % 10) { case 1: return "st"; case 2: return "nd"; case 3: return "rd"; }
    return "th";
  }

  // nextServiceRow mirrors board.nextServiceRow: the first row slides up
  // from the bottom of its 12px band at 2px/tick, then holds.
  function nextServiceRow(parent, s, now) {
    var clip = place(el("div", "clip"), 0, 0, W, ROW_H);
    var strip = place(el("div", "t"), 0, 0, W, ROW_H);
    strip.style.position = "absolute";
    rowInto(strip, s, 0);
    clip.appendChild(strip);
    parent.appendChild(clip);
    if (reduced.matches) return;
    var key = "first";
    var text = JSON.stringify([s.order, s.scheduled, s.destination, s.platform, s.status]);
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      var b = Math.min(NS_STEP * (tick + 1), ROW_H);
      strip.style.transform = "translateY(" + (ROW_H - b) + "px)";
    });
  }

  // remainingBand mirrors board.remainingServices: a strip of
  // [blank, svc2..svcN, dup-svc2] rows rolling vertically in a 12px window —
  // slide in (6 ticks), then per segment move 12px over 6 ticks and hold 5s,
  // wrapping seamlessly via the duplicated row.
  function remainingBand(parent, deps, now) {
    if (!deps.length) return;
    var n = deps.length;
    var clip = place(el("div", "clip"), 0, REMAINING_Y, W, ROW_H);
    var strip = el("div", "t");
    strip.style.position = "absolute"; strip.style.left = "0"; strip.style.top = "0";
    strip.style.width = W + "px"; strip.style.height = (n + 2) * ROW_H + "px";
    deps.forEach(function (s, i) { rowInto(strip, s, (i + 1) * ROW_H); });
    rowInto(strip, deps[0], (n + 1) * ROW_H); // dup covers mid-move wrap
    clip.appendChild(strip);
    parent.appendChild(clip);
    if (reduced.matches) {              // static: show the 2nd service, no roll
      strip.style.transform = "translateY(" + -ROW_H + "px)";
      return;
    }
    var key = "remaining";
    var text = JSON.stringify(deps);
    var t0 = epoch(key, text, now);
    animated.push(function (nowMs) {
      var tick = Math.floor((nowMs - t0) / TICK_MS);
      var ty;
      if (tick < RS_MOVE) {             // slide-in: strip top at band bottom
        ty = ROW_H - RS_STEP * (tick + 1);
      } else {                          // move-then-hold cycle
        var t = tick - RS_MOVE, s = Math.floor(t / RS_SEG), w = t % RS_SEG;
        var step = Math.min(RS_STEP * (w + 1), ROW_H);
        var u = ROW_H * s + step;
        var top = u > ROW_H ? ROW_H + (u - ROW_H) % (ROW_H * n) : u;
        ty = -top;
      }
      strip.style.transform = "translateY(" + ty + "px)";
    });
  }

  // clock mirrors render.Clock: Bold 20px HH:MM + Bold Tall 10px :SS at a
  // 5px drop, the pair centered. Driven from BOARD time (Task 2), not the
  // browser clock — drift between them is surfaced in the caption (#65).
  var clockBase = null, clockAt = 0; // server epoch ms, and Date.now() at fetch
  function clockText() {
    var d = clockBase === null ? new Date() : new Date(clockBase + (Date.now() - clockAt));
    var s = d.toLocaleTimeString("en-GB", { timeZone: "Europe/London", hour12: false });
    return [s.slice(0, 5), s.slice(5, 8)]; // ["HH:MM", ":SS"]
  }
  var clockHM = null, clockSS = null;
  function clockInto(parent) {
    var parts = clockText();
    var w1 = textW(parts[0], FONT_CLOCK), w2 = textW(parts[1], FONT_CLOCK_SEC);
    var margin = Math.max(0, (W - (w1 + w2)) >> 1);
    clockHM = el("span", "t"); clockHM.style.font = FONT_CLOCK;
    clockHM.style.left = margin + "px"; clockHM.style.top = CLOCK_Y + "px";
    clockHM.style.lineHeight = "14px";
    clockSS = el("span", "t"); clockSS.style.font = FONT_CLOCK_SEC;
    clockSS.style.left = (margin + w1) + "px";
    clockSS.style.top = (CLOCK_Y + 5) + "px"; // clockSecondsDrop=5
    parent.appendChild(clockHM); parent.appendChild(clockSS);
    tickClock();
  }
  function tickClock() {
    if (!clockHM) return;
    var parts = clockText();
    clockHM.textContent = parts[0];
    clockSS.textContent = parts[1];
  }

  function centeredLine(parent, text, y) {
    staticText(parent, text, 0, y, W, "center");
  }

  var last = "";
  function render(v) {
    var key = JSON.stringify(v, function (k, val) { return k === "time" ? undefined : val; });
    if (key === last) return; // identical scene: leave animations untouched
    last = key;
    animated = [];
    stage.textContent = "";
    var now = performance.now();

    if (v.state === "departures" && v.first) {
      nextServiceRow(stage, v.first, now);
      // "Calling at:" label is itself a ScrollingText in a 42px box on the
      // panel (scene_departures.go:50) — mirrored verbatim, quirks and all.
      scrollingText(stage, "calling-label", "Calling at:", 0, ROW_H, CALLING_LABEL_W, now);
      scrollingText(stage, "calling-list", v.first.callingAt || "", CALLING_X, ROW_H, CALLING_W, now);
      scrollingText(stage, "service-info", v.first.serviceInfo || "", 0, SERVICE_Y, W, now);
      remainingBand(stage, v.remaining || [], now);
    } else if (v.state === "hotspot" && v.hotspot) {
      centeredLine(stage, "Setup mode", 0);
      centeredLine(stage, "Join hotspot: " + v.hotspot.ssid, ROW_H);
      centeredLine(stage, "Then open http://" + v.hotspot.addr, SERVICE_Y);
    } else if (v.state === "no-services") {
      centeredLine(stage, v.location || "", 0);
      centeredLine(stage, (v.messages && v.messages[0]) || "No services to show", ROW_H);
    } else {
      centeredLine(stage, v.message || v.state, ROW_H);
    }
    clockInto(stage);
  }

  function staleness(v) {
    if (!caption) return;
    var text;
    if (!v.fetchedAt) {
      text = "Live panel · rendered in your browser";
    } else {
      var age = Math.max(0, (Date.now() - Date.parse(v.fetchedAt)) / 1000);
      if (age > 300) {
        root.classList.add("stale");
        text = "Live panel · data " + Math.round(age / 60) + " min old";
      } else {
        root.classList.remove("stale");
        text = "Live panel · data " + Math.round(age) + "s old";
      }
    }
    if (clockBase !== null) {
      var drift = Math.round(Math.abs(Date.now() - (clockBase + (Date.now() - clockAt))) / 1000);
      if (drift > 30) text += " · board clock differs from this device by " + drift + "s";
    }
    caption.textContent = text;
  }

  function poll() {
    fetch(endpoint, { headers: { Accept: "application/json" } })
      .then(function (r) {
        if (r.status === 401) { window.location.href = "/login"; throw new Error("unauthenticated"); }
        return r.json();
      })
      .then(function (v) {
        if (v.time) { clockBase = Date.parse(v.time); clockAt = Date.now(); }
        render(v); staleness(v);
      })
      .catch(function () { /* transient; next poll retries */ });
  }

  function frame(nowMs) {
    if (!document.hidden) {
      for (var i = 0; i < animated.length; i++) animated[i](nowMs);
    }
    requestAnimationFrame(frame);
  }

  // Fonts affect measurement (scroll distances, clock centering): render
  // once immediately, then re-render after the faces load.
  poll();
  setInterval(poll, 5000);
  setInterval(tickClock, 1000);
  if (!reduced.matches) requestAnimationFrame(frame);
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(function () { last = ""; poll(); });
  }
})();
```

- [ ] **Step 6: Run the Go tests**

Run: `go test ./internal/web/ -v -run 'Status|Board'`
Expected: PASS including the new markup test.

- [ ] **Step 7: Visual smoke locally**

Run the dev server (`go run ./cmd/trainboard` with a dev config, or the e2e harness if that's quicker) and eyeball on phone-width and desktop-width viewports:
- first row slides up once on load; "Calling at:" label sits in its 42px box with the list scrolling beside it; service line scrolls independently; remaining band holds 5s then rolls 12px in ~240ms; clock is big HH:MM with smaller dropped :SS, centered.
- OS reduced-motion on: everything static, second service visible, no scroll.
- Data refresh with identical content must NOT restart any animation; a changed calling-at restarts only that scroll.

- [ ] **Step 8: Commit**

```bash
git add internal/web/static/board.js internal/web/static/style.css internal/web/templates/status.html internal/web/handlers_status_test.go
git commit -m "feat(web): faithful panel emulation — Dot Matrix, real scene structure and motion (#61)"
```

---

### Task 4: `stations.Search` + `GET /api/stations`

**Files:**
- Modify: `internal/stations/stations.go`
- Modify: `internal/web/handlers_api.go`
- Modify: `internal/web/server.go` (route + setupGate exemption)
- Test: `internal/stations/stations_test.go`, `internal/web/handlers_api_test.go`

**Interfaces:**
- Produces: `stations.Station{CRS, Name string}`; `stations.Search(q string, limit int) []Station`; `GET /api/stations?q=<text>` → `200 [{"crs":"SHF","name":"Sheffield"}, …]` (≤8, ranked; `[]` never `null`). Public + setupGate-exempt. Tasks 6 consumes.

- [ ] **Step 1: Write the failing package tests**

Add to `internal/stations/stations_test.go`:

```go
func TestSearchByNamePrefix(t *testing.T) {
	got := stations.Search("sheff", 8)
	if len(got) == 0 || got[0].CRS != "SHF" {
		t.Fatalf("Search(sheff) = %+v, want Sheffield (SHF) first", got)
	}
}

func TestSearchByExactCodeRanksFirst(t *testing.T) {
	got := stations.Search("pad", 8)
	if len(got) == 0 || got[0].CRS != "PAD" {
		t.Fatalf("Search(pad) = %+v, want exact code PAD first", got)
	}
}

func TestSearchSubstring(t *testing.T) {
	got := stations.Search("paddington", 8)
	found := false
	for _, s := range got {
		if s.CRS == "PAD" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Search(paddington) = %+v, want PAD present", got)
	}
}

func TestSearchLimit(t *testing.T) {
	if got := stations.Search("st", 5); len(got) > 5 {
		t.Fatalf("Search limit ignored: %d results", len(got))
	}
}

func TestSearchShortQueryEmpty(t *testing.T) {
	if got := stations.Search("s", 8); len(got) != 0 {
		t.Fatalf("Search(single char) = %+v, want empty", got)
	}
	if got := stations.Search("", 8); len(got) != 0 {
		t.Fatalf("Search(empty) = %+v, want empty", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/stations/ -v`
Expected: FAIL — `Search` and `Station` undefined.

- [ ] **Step 3: Implement Search**

In `internal/stations/stations.go`, extend `load` to also build an ordered slice, and add the search:

```go
// Station is one row of the bundled UK station list.
type Station struct {
	CRS  string
	Name string
}

var list []Station // name-sorted at load (CSV is CRS-sorted; names track closely)

func load() {
	table = make(map[string]string, 2700)
	for _, line := range strings.Split(rawCSV, "\n") {
		crs, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(crs) != 3 {
			continue
		}
		crs = strings.ToUpper(crs)
		name = strings.Trim(name, `"`)
		table[crs] = name
		list = append(list, Station{CRS: crs, Name: name})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
}

// Search finds stations whose name or CRS code matches q, best first:
// exact code, then name prefix, then substring. Queries under 2 characters
// return nothing (too noisy to suggest). Case-insensitive.
func Search(q string, limit int) []Station {
	q = strings.TrimSpace(q)
	if len(q) < 2 || limit <= 0 {
		return nil
	}
	once.Do(load)
	uq, lq := strings.ToUpper(q), strings.ToLower(q)

	var exact, prefix, sub []Station
	if name, ok := table[uq]; ok && len(uq) == 3 {
		exact = append(exact, Station{CRS: uq, Name: name})
	}
	for _, s := range list {
		ln := strings.ToLower(s.Name)
		switch {
		case s.CRS == uq:
			// already in exact
		case strings.HasPrefix(ln, lq):
			prefix = append(prefix, s)
		case strings.Contains(ln, lq):
			sub = append(sub, s)
		}
		if len(prefix) >= limit && len(sub) >= limit {
			break
		}
	}
	out := append(append(exact, prefix...), sub...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
```

Add `"sort"` to imports.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/stations/ -v`
Expected: PASS (including the pre-existing Name tests).

- [ ] **Step 5: Write the failing handler test**

Add to `internal/web/handlers_api_test.go` (copy the style of the existing `/api/station` test in that file):

```go
// GET /api/stations?q= is the search companion to /api/station: public
// (pre-auth setup pages use it), JSON array, ≤8 ranked results, [] not null.
func TestAPIStationsSearch(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/api/stations?q=sheff")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []struct {
		CRS  string `json:"crs"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) == 0 || got[0].CRS != "SHF" {
		t.Fatalf("got %+v, want Sheffield (SHF) first", got)
	}
}

func TestAPIStationsSearchEmptyIsArray(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/api/stations?q=zzzzzz")
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Fatalf("empty search body = %q, want []", body)
	}
}
```

Also extend the existing setupGate-exemption test (server_test.go — the one covering `/api/station` pre-setup) to cover `/api/stations`.

- [ ] **Step 6: Run tests to verify they fail**

Run: `go test ./internal/web/ -run TestAPIStations -v`
Expected: FAIL (404 or setup redirect).

- [ ] **Step 7: Implement handler + route**

In `internal/web/handlers_api.go`, after `handleAPIStation`:

```go
// stationJSON is stations.Station's lowerCamel JSON projection.
type stationJSON struct {
	CRS  string `json:"crs"`
	Name string `json:"name"`
}

// handleAPIStations is GET /api/stations?q=<text>: offline station search by
// name or code (internal/stations.Search), best match first, at most 8.
// Public like /api/station — the pre-auth setup pages use it (see
// setupGate's exemption list).
func (s *Server) handleAPIStations(w http.ResponseWriter, r *http.Request) {
	res := stations.Search(r.URL.Query().Get("q"), 8)
	out := make([]stationJSON, 0, len(res))
	for _, st := range res {
		out = append(out, stationJSON{CRS: st.CRS, Name: st.Name})
	}
	writeJSON(w, http.StatusOK, out)
}
```

In `internal/web/server.go`:
- next to the `GET /api/station` route: `s.mux.Handle("GET /api/stations", http.HandlerFunc(s.handleAPIStations))`
- in `setupGate`, extend the exemption: `r.URL.Path == "/api/station" || r.URL.Path == "/api/stations"`
- update the two doc comments that enumerate the public exemptions (`/api/station` mentions at ~lines 204, 226-230, 264-270) to include `/api/stations`.

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./internal/web/ -run 'TestAPIStation|SetupGate' -v && go vet ./...`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/stations/ internal/web/handlers_api.go internal/web/handlers_api_test.go internal/web/server.go internal/web/server_test.go
git commit -m "feat(web): station search by name — stations.Search + GET /api/stations (#62)"
```

---

### Task 5: TOC table + `GET /api/tocs`

**Files:**
- Create: `internal/stations/data/tocs.csv`
- Create: `internal/stations/tocs.go`
- Create: `internal/stations/tocs_test.go`
- Modify: `internal/web/handlers_api.go`, `internal/web/server.go`
- Test: `internal/web/handlers_api_test.go`

**Interfaces:**
- Produces: `stations.TOC{Code, Name string}`; `stations.TOCName(code string) (string, bool)`; `stations.TOCSearch(q string, limit int) []TOC` where **empty q returns the full list** (it's ~31 rows — the client caches it for hint resolution); `GET /api/tocs?q=` → `200 [{"code":"GW","name":"Great Western Railway"}, …]`. Public + setupGate-exempt. Tasks 7 consumes.

- [ ] **Step 1: Write the TOC data file**

Create `internal/stations/data/tocs.csv` (ATOC operator codes as used by Darwin; name forms are the passenger-facing brands):

```csv
AW,Transport for Wales
CC,c2c
CH,Chiltern Railways
CS,Caledonian Sleeper
EM,East Midlands Railway
ES,Eurostar
GC,Grand Central
GN,Great Northern
GR,LNER
GW,Great Western Railway
GX,Gatwick Express
HT,Hull Trains
HX,Heathrow Express
IL,Island Line
LD,Lumo
LE,Greater Anglia
LM,West Midlands Railway
LO,London Overground
LT,London Underground
ME,Merseyrail
NT,Northern
SE,Southeastern
SN,Southern
SR,ScotRail
SW,South Western Railway
TL,Thameslink
TP,TransPennine Express
TW,Tyne and Wear Metro
VT,Avanti West Coast
XC,CrossCountry
XR,Elizabeth line
```

- [ ] **Step 2: Write the failing tests**

Create `internal/stations/tocs_test.go`:

```go
package stations_test

import (
	"testing"

	"github.com/mintopia/trainboard/internal/stations"
)

func TestTOCName(t *testing.T) {
	name, ok := stations.TOCName("gw")
	if !ok || name != "Great Western Railway" {
		t.Fatalf("TOCName(gw) = %q,%v", name, ok)
	}
	if _, ok := stations.TOCName("ZZ"); ok {
		t.Fatalf("TOCName(ZZ) unexpectedly found")
	}
}

func TestTOCSearchByName(t *testing.T) {
	got := stations.TOCSearch("eliza", 8)
	if len(got) != 1 || got[0].Code != "XR" {
		t.Fatalf("TOCSearch(eliza) = %+v, want XR", got)
	}
}

func TestTOCSearchByCode(t *testing.T) {
	got := stations.TOCSearch("XC", 8)
	if len(got) == 0 || got[0].Code != "XC" {
		t.Fatalf("TOCSearch(XC) = %+v, want XC first", got)
	}
}

func TestTOCSearchEmptyReturnsAll(t *testing.T) {
	got := stations.TOCSearch("", 100)
	if len(got) < 30 {
		t.Fatalf("TOCSearch(\"\") = %d rows, want the full table", len(got))
	}
}
```

(Match the existing stations_test.go package form — if it uses `package stations` internal style, follow it.)

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/stations/ -run TOC -v`
Expected: FAIL — undefined.

- [ ] **Step 4: Implement**

Create `internal/stations/tocs.go`:

```go
package stations

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed data/tocs.csv
var rawTOCs string

// TOC is one train operating company: ATOC code → passenger-facing name.
type TOC struct {
	Code string
	Name string
}

var (
	tocOnce  sync.Once
	tocTable map[string]string
	tocList  []TOC
)

func loadTOCs() {
	tocTable = make(map[string]string, 40)
	for _, line := range strings.Split(rawTOCs, "\n") {
		code, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(code) != 2 {
			continue
		}
		code = strings.ToUpper(code)
		tocTable[code] = name
		tocList = append(tocList, TOC{Code: code, Name: name})
	}
}

// TOCName returns the operator name for a 2-letter ATOC code
// (case-insensitive).
func TOCName(code string) (string, bool) {
	if len(code) != 2 {
		return "", false
	}
	tocOnce.Do(loadTOCs)
	name, ok := tocTable[strings.ToUpper(code)]
	return name, ok
}

// TOCSearch finds operators by code or name fragment, exact code first.
// An empty query returns the whole table (it is ~31 rows; the web UI
// caches it client-side for name hints).
func TOCSearch(q string, limit int) []TOC {
	tocOnce.Do(loadTOCs)
	q = strings.TrimSpace(q)
	if limit <= 0 {
		return nil
	}
	if q == "" {
		out := tocList
		if len(out) > limit {
			out = out[:limit]
		}
		return out
	}
	uq, lq := strings.ToUpper(q), strings.ToLower(q)
	var exact, rest []TOC
	for _, tc := range tocList {
		switch {
		case tc.Code == uq:
			exact = append(exact, tc)
		case strings.Contains(strings.ToLower(tc.Name), lq):
			rest = append(rest, tc)
		}
	}
	out := append(exact, rest...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/stations/ -v`
Expected: PASS.

- [ ] **Step 6: Handler + route + gate exemption (test-first)**

Add to `internal/web/handlers_api_test.go`:

```go
// GET /api/tocs?q= mirrors /api/stations for operators; empty q returns the
// full ~31-row table (the client caches it for name hints).
func TestAPITOCs(t *testing.T) {
	srv, _ := newTestServer(t)
	rec := getPath(t, srv.Handler(), "/api/tocs?q=eliza")
	var got []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].Code != "XR" {
		t.Fatalf("got %+v, want XR", got)
	}
	rec = getPath(t, srv.Handler(), "/api/tocs")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal full list: %v", err)
	}
	if len(got) < 30 {
		t.Fatalf("full list = %d rows, want ≥30", len(got))
	}
}
```

Run to verify it fails, then in `internal/web/handlers_api.go`:

```go
// tocJSON is stations.TOC's lowerCamel JSON projection.
type tocJSON struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// handleAPITOCs is GET /api/tocs?q=: offline operator search; empty q
// returns the full table (~31 rows) which the web UI caches for name hints.
// Public + setupGate-exempt like /api/station(s).
func (s *Server) handleAPITOCs(w http.ResponseWriter, r *http.Request) {
	res := stations.TOCSearch(r.URL.Query().Get("q"), 40)
	out := make([]tocJSON, 0, len(res))
	for _, tc := range res {
		out = append(out, tocJSON{Code: tc.Code, Name: tc.Name})
	}
	writeJSON(w, http.StatusOK, out)
}
```

In `internal/web/server.go`: route `s.mux.Handle("GET /api/tocs", http.HandlerFunc(s.handleAPITOCs))` next to the stations routes; extend the setupGate exemption and its doc comments with `/api/tocs`.

- [ ] **Step 7: Run tests + gates**

Run: `go test ./internal/web/ ./internal/stations/ -v -run 'TOC|Station' && make check`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/stations/ internal/web/handlers_api.go internal/web/handlers_api_test.go internal/web/server.go internal/web/server_test.go
git commit -m "feat(web): bundled TOC table + GET /api/tocs operator search (#63)"
```

---

### Task 6: suggest.js combobox + CRS field wiring

**Files:**
- Create: `internal/web/static/suggest.js`
- Modify: `internal/web/static/style.css` (suggest list styles)
- Modify: `internal/web/templates/config_departures.html` (origin + destination fields)
- Modify: `internal/web/templates/setup.html` (origin field)
- Test: `internal/web/handlers_config_test.go` / `handlers_setup_ap_test.go` markup assertions, attended acceptance

**Interfaces:**
- Consumes: `GET /api/stations?q=` (Task 4).
- Produces: enhancement contract — any `<input data-suggest="/api/stations" data-hint="<id>">` becomes a WAI-ARIA combobox; selection writes the CRS code into the input and the station name into the hint element. No-JS fallback: the input stays a plain 3-letter code field (server-side validation unchanged).

Design rules baked in:
- Progressive enhancement only — the form posts exactly the same field names; a JS failure leaves today's behaviour.
- The visible input IS the form field (`board.origin` etc.); typing a name is a transient search state, selection snaps the value to the code. The server never sees names.
- Keyboard: ArrowDown/ArrowUp move, Enter selects, Escape closes; `aria-activedescendant` tracks; list options are `role="option"` showing "Name (CRS)".
- The `.crs` monospace/uppercase styling is suspended while searching (class `searching`), restored on selection — typing "sheffield" must not render as cramped uppercase letter-spaced code styling.

- [ ] **Step 1: Write the failing markup tests**

In `internal/web/handlers_config_test.go`, add (following that file's GET-form test style):

```go
// CRS fields are suggest-enhanced comboboxes (#62): data-suggest carries the
// search endpoint; the legacy htmx per-keystroke lookup attributes are gone.
func TestDeparturesFormHasStationSuggest(t *testing.T) {
	srv, _ := newTestServer(t)
	body := getPath(t, srv.Handler(), "/config/departures", loginCookie(t, srv)).Body.String()
	for _, want := range []string{
		`data-suggest="/api/stations"`,
		`data-hint="origin-name"`,
		`data-hint="dest-name"`,
		`src="/static/suggest.js"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("departures form missing %q", want)
		}
	}
	if strings.Contains(body, `hx-get="/api/station"`) {
		t.Errorf("legacy htmx station lookup still present")
	}
}
```

Add the equivalent assertion for the setup page origin field in the setup flow's template test file (find the existing test asserting setup.html's origin field markup and extend it).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'Suggest|Departures' -v`
Expected: FAIL.

- [ ] **Step 3: Write suggest.js**

Create `internal/web/static/suggest.js`:

```js
// suggest.js — dependency-free accessible autosuggest (#62, #63).
// Enhances <input data-suggest="<endpoint>"> into a WAI-ARIA combobox.
//   data-hint="<id>"   element that shows the resolved name(s)
//   data-multi=","     comma-separated multi-token field (operators)
// Endpoints return JSON arrays of {crs|code, name}. Selection writes the
// CODE into the input — the server contract is unchanged, and without JS
// the field remains a plain code input. textContent only, never innerHTML.
(function () {
  "use strict";
  var seq = 0;
  document.querySelectorAll("input[data-suggest]").forEach(enhance);

  function enhance(input) {
    var endpoint = input.dataset.suggest;
    var multi = input.dataset.multi || "";
    var hint = input.dataset.hint ? document.getElementById(input.dataset.hint) : null;
    var cache = null; // full-list cache (TOCs); stations always query

    // Free typing needs room: suspend 3-char constraints; the server still
    // validates codes on submit.
    input.removeAttribute("maxlength");
    input.removeAttribute("pattern");
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-expanded", "false");
    input.autocomplete = "off";

    var box = document.createElement("div");
    box.className = "suggestwrap";
    input.parentNode.insertBefore(box, input);
    box.appendChild(input);
    var list = document.createElement("ul");
    list.className = "suggest";
    list.id = "suggest-" + (++seq);
    list.setAttribute("role", "listbox");
    list.hidden = true;
    box.appendChild(list);
    input.setAttribute("aria-controls", list.id);

    var items = [], active = -1, timer = null;

    function token() {
      if (!multi) return input.value.trim();
      var parts = input.value.split(multi);
      return parts[parts.length - 1].trim();
    }

    function close() {
      list.hidden = true;
      input.setAttribute("aria-expanded", "false");
      input.removeAttribute("aria-activedescendant");
      items = []; active = -1;
      input.classList.remove("searching");
    }

    function show(results) {
      list.textContent = "";
      items = results; active = -1;
      if (!results.length) { close(); return; }
      results.forEach(function (s, i) {
        var li = document.createElement("li");
        li.id = list.id + "-" + i;
        li.setAttribute("role", "option");
        li.textContent = s.name + " (" + (s.crs || s.code) + ")";
        li.addEventListener("mousedown", function (ev) { ev.preventDefault(); pick(i); });
        list.appendChild(li);
      });
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
    }

    function highlight(i) {
      var opts = list.children;
      if (active >= 0 && opts[active]) opts[active].removeAttribute("aria-selected");
      active = i;
      if (i >= 0 && opts[i]) {
        opts[i].setAttribute("aria-selected", "true");
        input.setAttribute("aria-activedescendant", opts[i].id);
      } else {
        input.removeAttribute("aria-activedescendant");
      }
    }

    function pick(i) {
      var s = items[i];
      if (!s) return;
      var code = (s.crs || s.code).toUpperCase();
      if (multi) {
        var parts = input.value.split(multi);
        parts[parts.length - 1] = " " + code;
        input.value = parts.join(multi).replace(/^ /, "");
      } else {
        input.value = code;
        if (hint) hint.textContent = s.name;
      }
      close();
      if (multi && hint) resolveMulti();
      input.dispatchEvent(new Event("change", { bubbles: true }));
      input.focus();
    }

    // Multi hint: resolve every token against the cached full table:
    // "GW, XR" → "Great Western Railway, Elizabeth line".
    function resolveMulti() {
      fullList().then(function (all) {
        var names = [];
        input.value.split(multi).forEach(function (t) {
          t = t.trim().toUpperCase();
          if (!t) return;
          var m = all.filter(function (s) { return (s.code || s.crs) === t; })[0];
          names.push(m ? m.name : t + "?");
        });
        hint.textContent = names.join(", ");
      });
    }

    function fullList() {
      if (cache) return Promise.resolve(cache);
      return fetch(endpoint, { headers: { Accept: "application/json" } })
        .then(function (r) { return r.json(); })
        .then(function (v) { cache = v; return v; });
    }

    function search() {
      var q = token();
      if (q.length < 2) { close(); return; }
      fetch(endpoint + "?q=" + encodeURIComponent(q), { headers: { Accept: "application/json" } })
        .then(function (r) { return r.json(); })
        .then(function (v) {
          if (token() !== q) return; // stale response
          show(v.slice(0, 8));
          // Exact code typed by hand: keep the hint honest without a pick.
          if (!multi && hint) {
            var exact = v.filter(function (s) { return (s.crs || s.code) === q.toUpperCase(); })[0];
            if (exact) hint.textContent = exact.name;
          }
        })
        .catch(close);
    }

    input.addEventListener("input", function () {
      input.classList.add("searching");
      clearTimeout(timer);
      timer = setTimeout(search, 250);
      if (multi && hint) { clearTimeout(input._mt); input._mt = setTimeout(resolveMulti, 600); }
    });

    input.addEventListener("keydown", function (ev) {
      if (list.hidden) return;
      if (ev.key === "ArrowDown") { ev.preventDefault(); highlight(Math.min(active + 1, items.length - 1)); }
      else if (ev.key === "ArrowUp") { ev.preventDefault(); highlight(Math.max(active - 1, 0)); }
      else if (ev.key === "Enter") { if (active >= 0) { ev.preventDefault(); pick(active); } }
      else if (ev.key === "Escape") { close(); }
    });

    input.addEventListener("blur", function () { setTimeout(close, 150); });

    if (multi && hint && input.value.trim()) resolveMulti();
  }
})();
```

- [ ] **Step 4: Add suggest styles**

Append to `internal/web/static/style.css` (after the Forms block):

```css
/* Autosuggest combobox (#62, #63) */
.suggestwrap { position: relative; }
input.crs.searching {
  text-transform: none; letter-spacing: normal; max-width: 100%;
  font-family: var(--body); font-size: .95rem;
}
ul.suggest {
  position: absolute; z-index: 10; left: 0; right: 0; top: 100%;
  margin: 2px 0 0; padding: 0; list-style: none;
  background: #fff; border: 1.5px solid var(--navy); border-radius: 4px;
  box-shadow: 0 4px 10px rgba(0, 47, 99, .15); max-height: 14rem; overflow-y: auto;
}
ul.suggest li { padding: .55rem .7rem; min-height: 44px; display: flex; align-items: center; cursor: pointer; font-size: .9rem; }
ul.suggest li[aria-selected="true"], ul.suggest li:hover { background: var(--yellow); }
```

- [ ] **Step 5: Wire the departures form**

In `internal/web/templates/config_departures.html` replace the Station and "Only trains towards" labels with:

```html
<label class="f">Station
  <input class="crs" type="text" name="board.origin" value="{{.Cfg.Board.Origin}}" required maxlength="3" pattern="[A-Za-z]{3}"
         data-suggest="/api/stations" data-hint="origin-name">
  <div class="hint">Type a station name or 3-letter code &middot; <span id="origin-name">{{.OriginName}}</span></div>
</label>
<label class="f">Only trains towards <span style="color:#8b99a5">(optional)</span>
  <input class="crs" type="text" name="board.destination" value="{{.Cfg.Board.Destination}}" maxlength="3"
         data-suggest="/api/stations" data-hint="dest-name">
  <div class="hint"><span id="dest-name">{{.DestinationName}}</span></div>
</label>
```

At the bottom of the template (before `{{end}}`), add:

```html
<script src="/static/suggest.js" defer></script>
```

- [ ] **Step 6: Wire the setup form**

In `internal/web/templates/setup.html` apply the same transformation to the origin field (~line 37): replace the `hx-get`/`hx-on::after-request` attributes with `data-suggest="/api/stations" data-hint="origin-name"`, keep `required maxlength="3" pattern="[A-Za-z]{3}"`, change the hint copy to `Type a station name or 3-letter code`, and add the `<script src="/static/suggest.js" defer></script>` include.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/web/ -v`
Expected: PASS, including updated markup tests. If existing tests asserted the old `hx-get="/api/station"` markup, update them to the new contract — the no-JS behaviour they guard (plain code input, server-side validation) is unchanged.

- [ ] **Step 8: Attended smoke**

Dev server: type "sheff" in Station → listbox appears; ArrowDown+Enter fills `SHF`, hint shows "Sheffield"; keyboard-only pass works; Escape closes; with JS disabled the field is a plain 3-letter input and the form still saves.

- [ ] **Step 9: Commit**

```bash
git add internal/web/static/suggest.js internal/web/static/style.css internal/web/templates/config_departures.html internal/web/templates/setup.html internal/web/handlers_config_test.go
git commit -m "feat(web): station combobox — search by name, code autofill (#62)"
```

---

### Task 7: Operators field — TOC suggest + name hints

**Files:**
- Modify: `internal/web/templates/config_departures.html` (Operators field)
- Modify: `internal/web/handlers_config.go` (server-rendered TOC names hint)
- Test: `internal/web/handlers_config_test.go`

**Interfaces:**
- Consumes: `GET /api/tocs` (Task 5), suggest.js `data-multi` mode (Task 6), `stations.TOCName` (Task 5).
- Produces: `TOCNames string` field on the departures form's template data.

- [ ] **Step 1: Write the failing test**

In `internal/web/handlers_config_test.go`:

```go
// The Operators field carries TOC suggest + server-rendered name hints
// (#63): "GW, XR" renders "Great Western Railway, Elizabeth line".
func TestDeparturesFormTOCHints(t *testing.T) {
	srv, svc := newTestServer(t)
	setBoardTOCs(t, svc, []string{"GW", "XR"}) // use the file's existing config-mutation helper
	body := getPath(t, srv.Handler(), "/config/departures", loginCookie(t, srv)).Body.String()
	for _, want := range []string{
		`data-suggest="/api/tocs"`,
		`data-multi=","`,
		`data-hint="tocs-names"`,
		`Great Western Railway, Elizabeth line`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("departures form missing %q", want)
		}
	}
}
```

(`setBoardTOCs` stands for however the file's existing tests seed `Cfg.Board.TOCs` — reuse the real helper; if none exists, save via the form POST helper first.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestDeparturesFormTOCHints -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/web/handlers_config.go`, where the departures form's template data is built (the struct that already carries `OriginName`/`DestinationName`/`TOCsCSV`), add a `TOCNames string` field and populate it:

```go
	// TOC name hints (#63): "GW, XR" → "Great Western Railway, Elizabeth
	// line"; unknown codes render as "XX?" so typos are visible, not silent.
	var tocNames []string
	for _, code := range cfg.Board.TOCs {
		if name, ok := stations.TOCName(code); ok {
			tocNames = append(tocNames, name)
		} else {
			tocNames = append(tocNames, code+"?")
		}
	}
	data.TOCNames = strings.Join(tocNames, ", ")
```

In `internal/web/templates/config_departures.html` replace the Operators label:

```html
<label class="f">Operators
  <input type="text" name="board.tocs" value="{{.TOCsCSV}}" placeholder="All operators"
         data-suggest="/api/tocs" data-multi="," data-hint="tocs-names">
  <div class="hint">Type an operator name or code, comma-separated &mdash; leave empty for all &middot; <span id="tocs-names">{{.TOCNames}}</span></div>
</label>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -run 'TOC|Departures' -v`
Expected: PASS.

- [ ] **Step 5: Attended smoke**

Type "avanti" in Operators → suggest shows "Avanti West Coast (VT)"; picking appends `VT`; hint line lists full names; a second token after a comma suggests independently.

- [ ] **Step 6: Commit**

```bash
git add internal/web/templates/config_departures.html internal/web/handlers_config.go internal/web/handlers_config_test.go
git commit -m "feat(web): TOC operator suggest + name hints (#63)"
```

---

### Task 8: Desktop adaptation

**Files:**
- Modify: `internal/web/static/style.css`
- Modify: `internal/web/templates/status.html` (two-column wrapper)
- Test: `internal/web/handlers_status_test.go`

**Interfaces:**
- Consumes: Task 3's `.boardwrap` stage (already width-driven, so it scales up for free).
- Produces: `.cols` grid wrapper on the status page.

Design intent (from the critique): don't invent a desktop dashboard — let the existing hierarchy breathe. Board + state stay the full-width hero; facts and events sit side by side beneath; forms stay at a readable measure.

- [ ] **Step 1: Write the failing test**

In `internal/web/handlers_status_test.go`:

```go
// Desktop layout (#64): facts and events sit in a two-column grid that
// collapses to one column on phones (CSS-only collapse; markup is shared).
func TestStatusTwoColumnWrapper(t *testing.T) {
	srv, _ := newTestServer(t)
	body := getPath(t, srv.Handler(), "/", loginCookie(t, srv)).Body.String()
	if !strings.Contains(body, `class="cols"`) {
		t.Fatalf("status page missing .cols wrapper")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestStatusTwoColumnWrapper -v`
Expected: FAIL.

- [ ] **Step 3: Wrap the status sections**

In `internal/web/templates/status.html`, wrap the "This board" and "Recent events" sections:

```html
<div class="cols">
<section>
<h3>This board</h3>
<div class="rows">
  …(existing fact rows unchanged)…
</div>
</section>
<section>
<h3>Recent events</h3>
<div id="events" hx-get="/events" hx-trigger="every 5s" hx-swap="innerHTML">
  {{template "eventlist" .Status.Events}}
</div>
</section>
</div>
```

- [ ] **Step 4: Add the wide-breakpoint CSS**

Append to `internal/web/static/style.css`:

```css
/* Desktop adaptation (#64) — phone-first unchanged; wide screens let the
   existing hierarchy breathe instead of stretching a phone column. */
.cols { display: block; }
@media (min-width: 64rem) {
  body { max-width: 64rem; }
  .cols { display: grid; grid-template-columns: 1fr 1fr; gap: 0 3rem; align-items: start; }
  /* Forms keep a readable measure rather than stretching to the grid. */
  main form { max-width: 42rem; }
  /* Full-bleed save bar is a phone affordance; inline it on desktop. */
  .savebar { margin: 0; padding-left: 0; padding-right: 0; }
}
```

- [ ] **Step 5: Run tests + visual pass**

Run: `go test ./internal/web/ -v -run Status`
Expected: PASS.

Attended: at ≥1024px-wide viewport, status shows board hero full width with facts | events side by side; the board stage scales crisply (Task 3's ResizeObserver); config sub-pages keep a readable form column; at phone width nothing changed.

- [ ] **Step 6: Commit**

```bash
git add internal/web/static/style.css internal/web/templates/status.html internal/web/handlers_status_test.go
git commit -m "feat(web): desktop adaptation — wide status grid, readable form measure (#64)"
```

---

### Task 9: Addresses one per line

**Files:**
- Modify: `internal/web/templates/status.html` (Address fact row)
- Modify: `internal/web/static/style.css`
- Test: `internal/web/handlers_status_test.go`

- [ ] **Step 1: Write the failing test**

```go
// Each address renders on its own line (#65): usb0 lifeline + LAN IP is a
// real dual-address case and dot-joined text wrapped mid-address.
func TestStatusAddressesOnSeparateLines(t *testing.T) {
	srv, svc := newTestServer(t)
	setStatusIPs(t, svc, []string{"192.168.0.102", "10.55.0.1"}) // use the file's existing status-stub helper
	body := getPath(t, srv.Handler(), "/", loginCookie(t, srv)).Body.String()
	for _, want := range []string{
		`<span class="addr">192.168.0.102</span>`,
		`<span class="addr">10.55.0.1</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
	if strings.Contains(body, `192.168.0.102 · 10.55.0.1`) {
		t.Errorf("addresses still dot-joined on one line")
	}
}
```

(`setStatusIPs` stands for the file's existing mechanism for stubbing `Status.IPs` — reuse it.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run TestStatusAddresses -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

In `internal/web/templates/status.html` replace the Address row:

```html
  <div class="r"><span class="k">Address</span><span class="v addrs">{{range .Status.IPs}}<span class="addr">{{.}}</span>{{end}}{{if .MDNSState}}<span class="addr">trainboard.local</span>{{end}}</span></div>
```

Append to `internal/web/static/style.css` (next to the fact-row rules):

```css
.r .v.addrs { display: flex; flex-direction: column; align-items: flex-end; gap: .15rem; }
.r .v.addrs .addr { white-space: nowrap; }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -run TestStatusAddresses -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/templates/status.html internal/web/static/style.css internal/web/handlers_status_test.go
git commit -m "fix(web): status addresses one per line (#65)"
```

---

### Task 10: Gates, weight audit, docs, PR

**Files:**
- Modify: `docs/deploy.md` (if the board-preview or API description drifted)
- No new code — verification and close-out.

- [ ] **Step 1: Full gates**

Run: `make check`
Expected: vet + lint + all tests PASS.

- [ ] **Step 2: Page-weight audit**

Sum the worst page's transfer: status page HTML + style.css + htmx.min.js + board.js + suggest.js + all five woff2 fonts:

```bash
ls -la internal/web/static/ internal/web/static/fonts/ | awk '{print $5, $9}'
```

Expected: total < 150KB (brief §4). Fonts are cached after first load, but the budget is judged on the cold first view. If over: re-subset the Dot Matrix fonts to digits+upper+lower+punctuation only and re-measure.

- [ ] **Step 3: A11y spot-check list (attended or via the accessibility-tester agent)**

- combobox: keyboard-only station selection works; listbox announced; `aria-activedescendant` tracks.
- reduced-motion: board fully static; no rAF loop running.
- contrast: suggest list yellow highlight on ink text ≥4.5:1 (yellow #ffd41f + ink #17222e passes); board amber-on-black is decorative/`role="img"` with aria-label.
- focus rings visible on the enhanced inputs.

- [ ] **Step 4: Docs touch-up**

In `docs/deploy.md` (~line 115) the preview description mentions `/api/board` — extend the sentence to note the preview is a faithful panel emulation using the panel's own Dot Matrix fonts, and that station/TOC search is served offline from bundled tables (`/api/stations`, `/api/tocs`).

- [ ] **Step 5: Update the plan ledger + push + PR**

```bash
git push -u origin feat/web-ui-fidelity
gh pr create --title "M7: Web UI fidelity fast-follow (#61-#65)" --body "$(cat <<'EOF'
Board preview is now a faithful SSD1322 emulation (Dot Matrix fonts, real scene
structure, panel tick timing); stations and TOCs are searchable by name with
accessible comboboxes; desktop gets a two-column status and readable form
measure; addresses render one per line; the preview clock runs on board time
and flags drift.

Closes #61, closes #62, closes #63, closes #64, closes #65.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 6: Final review**

Request a final code review (superpowers:requesting-code-review) of the full branch diff before merge, per AGENTS.md's end-of-milestone review gate.

---

## Self-Review Notes

- **Spec coverage:** #61 → Tasks 1–3; #62 → Tasks 4, 6; #63 → Tasks 5, 7; #64 → Task 8; #65 → Tasks 2 (time), 3 (drift caption), 9 (addresses). All five issues covered.
- **Type consistency:** `stations.Station{CRS,Name}` / `stations.TOC{Code,Name}` used consistently in Tasks 4–7; JSON keys `crs`/`code`+`name` consistent between handlers and suggest.js (`s.crs || s.code`); `boardView.Time` produced in Task 2 and consumed as `v.time` in Task 3.
- **Known duplication:** panel constants exist in Go and JS. Accepted deliberately (no build step to share them); both sides carry source-citation comments as the drift tripwire.
- **Helper-name caveat:** test snippets use `newTestServer`/`getPath`/`loginCookie` per server_test.go; where a task's file uses different established helpers, the implementer must adapt names, not behaviour.
