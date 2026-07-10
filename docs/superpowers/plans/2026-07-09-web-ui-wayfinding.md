# Web UI "Wayfinding" Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the Pi-served admin UI in the Wayfinding design (navy totem / yellow notices / Rail Alphabet, phone-first), replace the 1fps PNG preview with a client-rendered JSON board, and restructure config into a settings list with scoped saves.

**Architecture:** Server-rendered Go templates + htmx stays (no SPA). A new design system lands first (fonts + CSS + chrome), then a JSON board endpoint replaces `/preview.png`, then each page is rebuilt on the new system, ending with a legacy-CSS cleanup. Every task leaves the app shippable.

**Tech Stack:** Go 1.26 stdlib (`http.ServeMux` method patterns, `go:embed`), html/template, htmx 2.0.10 (vendored, unchanged), hand-written CSS + tiny vanilla JS, woff2 fonts.

**Design authority:** `docs/design/2026-07-09-web-ui-wayfinding-brief.md` and `/PRODUCT.md`. Mockups: claude.ai artifact `49ba031e` iteration 3.

## Global Constraints

- Go `1.26`, module `github.com/mintopia/trainboard`. **No new Go dependencies.**
- Gate before every commit: `make check` (= `go vet ./...` + `golangci-lint run` + `go test -race ./...`) must pass.
- Palette (exact): navy `#002f63`, yellow `#ffd41f`, signal green `#00733b`, alert red `#d4351c`, red text-on-white `#942514`, ink `#17222e`, muted `#57646f` (AA on white), hairline `#e3e7eb`, input border `#b7c0c9`, board amber `#ffb000` / dim amber `#c98a00` on `#000`.
- Type: `"Rail Alphabet Light"` on navy only; `"Rail Alphabet"` (dark cut) for headings/nav/buttons on white; body/forms `"Helvetica Neue", Helvetica, Arial, system-ui, sans-serif`; mono (`ui-monospace, "SF Mono", Menlo, monospace`) only inside the board and event timestamps.
- WCAG AA: body contrast ≥ 4.5:1, touch targets ≥ 44px, `:focus-visible` = `3px solid #ffd41f` outline, every animation has a `prefers-reduced-motion: reduce` alternative, no color-only state (dot always paired with words).
- Copy: plain, consequence-first ("Full power cycle — the board is dark for about a minute"). No raw jargon: "Only trains towards" not "Destination CRS"; every wait states expected duration + what to do if it fails. No `confirm()` anywhere.
- Page weight: any page ≤ 150KB over the wire including fonts (fonts are subset woff2, cached).
- Brand: plain "Trainboard" wordmark. No National-Rail-style double arrow.
- Commits: conventional commits, one per task step block as shown; branch `feat/web-ui-wayfinding` off `main`.

---

### Task 0: Branch

- [ ] **Step 1: Create branch**

```bash
cd /Users/mintopia/Projects/trainboard
git checkout main && git pull && git checkout -b feat/web-ui-wayfinding
```

---

### Task 1: Fonts + design tokens + page chrome

Land the Wayfinding design system: subset woff2 fonts, a rewritten `style.css` that ALSO keeps the legacy rules alive (old pages keep working until each is rebuilt; Task 11 deletes legacy), and the new layout chrome (totem → yellow band → tabs).

**Files:**
- Create: `reference/fonts/britrdn.ttf`, `reference/fonts/britrln.ttf` (source copies), `reference/fonts/README.md`
- Create: `internal/web/static/fonts/rail-alphabet-dark.woff2`, `internal/web/static/fonts/rail-alphabet-light.woff2`
- Modify: `internal/web/static/style.css` (prepend new system, keep legacy rules below a marker)
- Modify: `internal/web/templates/layout.html`
- Test: `internal/web/server_test.go` (add static-font test), existing tests must stay green

**Interfaces:**
- Consumes: `staticFS()` embed serving at `GET /static/` (`internal/web/templates.go:42`, `server.go:104`) — fonts land under `static/fonts/` and are embedded by the existing `//go:embed templates/* static/*` directive automatically.
- Produces: CSS component classes used verbatim by ALL later tasks: `.totem`, `.brand`, `.yellowband`, `.tabs`, `.statebar`, `.dot` (+ `.dot.amber`, `.dot.red`), `.board`, `.board .row`, `.board .dest`, `.board .marquee-clip`, `.board .marquee`, `.board .clockline`, `.caption`, `.notice` (+ `.notice.calm`), `.rows`, `.r`, `.k`, `.v`, `.btn` (+ `.btn.ghost`, `.btn.danger`), `.f` (form label), `.hint`, `.crs`, `.check`, `.route`, `.stop` (+ `.done`, `.now`), `.setlist`, `.setrow`, `.chev`, `.backrow`, `.savebar`, `.consequence` (+ `.consequence.ok`), `.act`, `.pip`, `table.events`. Template block name `"tabs"` param: pages set `Active` field ("status"/"config"/"actions").

- [ ] **Step 1: Copy font sources into the repo with provenance note**

```bash
mkdir -p reference/fonts
cp ~/Downloads/British-Rail-Fonts-Rail-Alphabet/britrdn_.ttf reference/fonts/britrdn.ttf
cp ~/Downloads/British-Rail-Fonts-Rail-Alphabet/britrln_.ttf reference/fonts/britrln.ttf
cat > reference/fonts/README.md <<'EOF'
# Rail Alphabet clone fonts

`britrdn.ttf` (BritishRailDarkNormal) and `britrln.ttf` (BritishRailLightNormal) are
freeware Rail Alphabet clones (Fontographer, 2001-07-19) with no license metadata in the
files. Shipping them in the web UI was accepted by the project owner on 2026-07-09
(docs/design/2026-07-09-web-ui-wayfinding-brief.md §8). If a rights issue ever surfaces,
delete `internal/web/static/fonts/` and the `@font-face` block in style.css — the UI
falls back to the metrically-close Helvetica stack with no other change.

Subset woff2 files in internal/web/static/fonts/ are generated from these with:
  pyftsubset <src>.ttf --unicodes="U+0020-007E,U+00A3,U+2013,U+2014,U+2018,U+2019,U+201C,U+201D,U+2026" --flavor=woff2 --output-file=<dst>.woff2
EOF
```

- [ ] **Step 2: Generate subset woff2 files**

```bash
python3 -m venv /tmp/fontsub && /tmp/fontsub/bin/pip install --quiet "fonttools[woff]"
mkdir -p internal/web/static/fonts
/tmp/fontsub/bin/pyftsubset reference/fonts/britrdn.ttf \
  --unicodes="U+0020-007E,U+00A3,U+2013,U+2014,U+2018,U+2019,U+201C,U+201D,U+2026" \
  --flavor=woff2 --output-file=internal/web/static/fonts/rail-alphabet-dark.woff2
/tmp/fontsub/bin/pyftsubset reference/fonts/britrln.ttf \
  --unicodes="U+0020-007E,U+00A3,U+2013,U+2014,U+2018,U+2019,U+201C,U+201D,U+2026" \
  --flavor=woff2 --output-file=internal/web/static/fonts/rail-alphabet-light.woff2
ls -la internal/web/static/fonts/
```
Expected: two `.woff2` files, each roughly 8–20KB. If `pyftsubset` fails on these 2001-era TTFs (bad tables), retry with `--no-hinting --desubroutinize`; if still failing, fall back to whole-file conversion: `/tmp/fontsub/bin/python -c "from fontTools.ttLib import TTFont; f=TTFont('reference/fonts/britrdn.ttf'); f.flavor='woff2'; f.save('internal/web/static/fonts/rail-alphabet-dark.woff2')"` (and same for light) — bigger (~25KB) but within budget.

- [ ] **Step 3: Write the failing test for font serving**

Append to `internal/web/server_test.go`:

```go
func TestStaticFontsServed(t *testing.T) {
	h, _ := newTestServer(t)
	for _, path := range []string{
		"/static/fonts/rail-alphabet-dark.woff2",
		"/static/fonts/rail-alphabet-light.woff2",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: want 200, got %d", path, rec.Code)
		}
		if rec.Body.Len() < 1000 {
			t.Errorf("GET %s: suspiciously small body (%d bytes)", path, rec.Body.Len())
		}
	}
}
```

(Use the same `newTestServer` helper signature as the existing tests in that file — if it returns more values, adapt the assignment, e.g. `h, _, _ :=`.)

- [ ] **Step 4: Run it — should PASS already** (embed directive `templates/* static/*` picks the files up). If it fails, the embed pattern didn't match: confirm files are under `internal/web/static/fonts/` and re-run.

Run: `go test ./internal/web/ -run TestStaticFontsServed -v`
Expected: PASS

- [ ] **Step 5: Rewrite `internal/web/static/style.css`** — new system on top, legacy preserved below. Replace the whole file with:

```css
/* trainboard admin UI — "Wayfinding" (docs/design/2026-07-09-web-ui-wayfinding-brief.md)
   UK station signage: navy totem, yellow notices, Rail Alphabet. Phone-first. */

@font-face {
  font-family: "Rail Alphabet";
  src: url(/static/fonts/rail-alphabet-dark.woff2) format("woff2");
  font-weight: 400; font-style: normal; font-display: swap;
}
@font-face {
  font-family: "Rail Alphabet Light";
  src: url(/static/fonts/rail-alphabet-light.woff2) format("woff2");
  font-weight: 400; font-style: normal; font-display: swap;
}

:root {
  --navy: #002f63;
  --yellow: #ffd41f;
  --green: #00733b;
  --red: #d4351c;
  --red-text: #942514;
  --ink: #17222e;
  --muted: #57646f;
  --hairline: #e3e7eb;
  --input-border: #b7c0c9;
  --amber: #ffb000;
  --amber-dim: #c98a00;
  --ra: "Rail Alphabet", "Helvetica Neue", Helvetica, Arial, system-ui, sans-serif;
  --ra-light: "Rail Alphabet Light", "Helvetica Neue", Helvetica, Arial, system-ui, sans-serif;
  --body: "Helvetica Neue", Helvetica, Arial, system-ui, sans-serif;
  --mono: ui-monospace, "SF Mono", Menlo, monospace;
}

* { box-sizing: border-box; }
html, body { background: #fff; }
body {
  margin: 0 auto; max-width: 42rem; padding: 0 0 2rem;
  font-family: var(--body); font-size: 16px; line-height: 1.5; color: var(--ink);
}
main { display: block; padding: 0 1rem; }
a { color: var(--navy); }
:focus-visible { outline: 3px solid var(--yellow); outline-offset: 1px; }

/* Chrome: totem, yellow band, tabs */
.totem { background: var(--navy); color: #fff; padding: .8rem 1rem .7rem; }
.totem .brand { font-family: var(--ra-light); font-size: 1.15rem; letter-spacing: .01em; color: #fff; text-decoration: none; }
.yellowband { height: 6px; background: var(--yellow); }
.tabs { display: flex; align-items: center; gap: 1.3rem; padding: .65rem 1rem 0; border-bottom: 1px solid #d9dee4; font-size: .85rem; }
.tabs a { font-family: var(--ra); color: #405264; text-decoration: none; padding: .2rem 0 .55rem; min-height: 0; }
.tabs a.on { color: var(--navy); box-shadow: 0 2px 0 var(--navy); }
.tabs form.inline { display: inline-flex; margin: 0 0 0 auto; }
.tabs form.inline button {
  font-family: var(--ra); font-size: .85rem; color: #405264; background: none; border: 0;
  padding: .2rem 0 .55rem; cursor: pointer; min-height: 0; min-width: 0;
}

/* Headings */
h2 { font-family: var(--ra); font-size: 1.05rem; font-weight: 400; margin: 1.1rem 0 .6rem; }
h3 { font-family: var(--ra); font-size: .92rem; font-weight: 400; margin: 1.1rem 0 .5rem; }

/* State line */
.statebar { margin: 1rem 0 .9rem; display: flex; align-items: center; gap: .6rem; font-family: var(--ra); font-size: 1rem; }
.statebar .since { margin-left: auto; font-family: var(--body); font-size: .78rem; color: var(--muted); }
.dot { width: .8rem; height: .8rem; border-radius: 50%; background: var(--green); flex-shrink: 0; }
.dot.amber { background: #b25e00; }
.dot.red { background: var(--red); }

/* Live board */
.board {
  border-radius: 4px; background: #000; aspect-ratio: 4 / 1; padding: 2.5% 3.5%;
  display: flex; flex-direction: column; justify-content: space-between;
  font-family: var(--mono); color: var(--amber);
  font-size: clamp(8px, 2.7cqw, 13px); line-height: 1.5;
  white-space: nowrap; overflow: hidden; container-type: inline-size;
}
.board .row { display: flex; gap: 1.1em; }
.board .dest { flex: 1; overflow: hidden; text-overflow: ellipsis; }
.board .center { justify-content: center; }
.board .marquee-clip { overflow: hidden; color: var(--amber-dim); }
.board .marquee { display: inline-block; padding-left: 100%; animation: marquee 16s linear infinite; }
@keyframes marquee { to { transform: translateX(-100%); } }
.board .clockline { display: flex; justify-content: center; letter-spacing: .12em; font-variant-numeric: tabular-nums; }
.board.stale { filter: grayscale(1) brightness(.8); }
.caption { margin: .4rem 0 1.1rem; font-size: .74rem; color: var(--muted); }
@media (prefers-reduced-motion: reduce) {
  .board .marquee { animation: none; padding-left: 0; }
}

/* Notices */
.notice { background: var(--yellow); color: var(--ink); margin: 0 0 .8rem; padding: .7rem .85rem; border-radius: 4px; font-size: .85rem; }
.notice.calm { background: #eef1f4; }
.notice.fault { background: #fbe9e5; }
.notice strong { font-weight: 700; }

/* Fact rows */
.rows { border-top: 1px solid var(--hairline); }
.r { display: flex; justify-content: space-between; gap: 1rem; padding: .5rem 0; border-bottom: 1px solid var(--hairline); font-size: .85rem; }
.r .k { color: var(--muted); }
.r .v { font-variant-numeric: tabular-nums; text-align: right; overflow-wrap: anywhere; }

/* Buttons */
.btn, button.btn {
  font-family: var(--ra); font-size: .9rem; cursor: pointer; min-height: 44px; min-width: 44px;
  background: var(--navy); color: #fff; border: 0; border-radius: 4px; padding: .5rem 1.1rem;
  display: inline-flex; align-items: center; justify-content: center; text-decoration: none;
}
.btn.ghost { background: #fff; color: var(--navy); border: 1.5px solid var(--navy); }
.btn.danger { background: #fff; color: var(--red-text); border: 1.5px solid var(--red); }
.btn:disabled { opacity: .45; cursor: not-allowed; }
.btnrow { display: flex; gap: .6rem; flex-wrap: wrap; }

/* Forms */
label.f { display: block; margin: 0 0 .95rem; font-size: .82rem; color: #405264; }
label.f .hint { font-size: .74rem; color: var(--muted); margin-top: .15rem; }
input[type="text"], input[type="password"], input[type="number"], select, textarea {
  display: block; width: 100%; min-height: 44px; margin-top: .3rem;
  font: inherit; font-size: .95rem; color: var(--ink);
  background: #fff; border: 1.5px solid var(--input-border); border-radius: 4px; padding: .5rem .6rem;
}
textarea { min-height: 4.5rem; }
input.crs { font-family: var(--ra); text-transform: uppercase; letter-spacing: .12em; font-size: 1.1rem; max-width: 8rem; }
label.check { display: flex; align-items: center; gap: .6rem; min-height: 44px; font-size: .85rem; color: var(--ink); margin: 0; }
label.check input { width: 1.35rem; height: 1.35rem; accent-color: var(--navy); }
.field-error { color: var(--red-text); font-size: .78rem; margin-top: .2rem; }

/* Route-line progress */
.route { display: flex; margin: 1.1rem 0 1.3rem; }
.route .stop { flex: 1; position: relative; text-align: center; font-size: .72rem; color: var(--muted); padding-top: 1.3rem; }
.route .stop::before {
  content: ""; position: absolute; top: .32rem; left: 50%; transform: translateX(-50%);
  width: .85rem; height: .85rem; border-radius: 50%;
  background: #fff; border: 3px solid var(--input-border); z-index: 1;
}
.route .stop::after { content: ""; position: absolute; top: .62rem; left: -50%; width: 100%; height: 4px; background: var(--input-border); }
.route .stop:first-child::after { display: none; }
.route .stop.done { color: var(--ink); }
.route .stop.done::before { background: var(--navy); border-color: var(--navy); }
.route .stop.done::after { background: var(--navy); }
.route .stop.now { color: var(--navy); font-weight: 700; }
.route .stop.now::before { border-color: var(--navy); background: var(--yellow); }
.route .stop.now::after { background: var(--navy); }

/* Settings list */
.setlist { margin: .3rem 0 1rem; }
.setlist a.setrow { display: flex; align-items: center; gap: .8rem; padding: .8rem 0; border-bottom: 1px solid var(--hairline); text-decoration: none; color: inherit; }
.setrow .t { font-family: var(--ra); font-size: .92rem; color: var(--ink); }
.setrow .s { font-size: .76rem; color: var(--muted); margin-top: .1rem; }
.setrow .chev { margin-left: auto; color: #8b99a5; font-size: 1.15rem; flex-shrink: 0; }
.backrow { display: flex; align-items: center; gap: .5rem; padding: .7rem 0 .2rem; }
.backrow a { font-family: var(--ra); font-size: .82rem; color: var(--navy); text-decoration: none; }

/* Sticky save bar */
.savebar {
  position: sticky; bottom: 0; background: #fff; border-top: 1px solid #d9dee4;
  margin: 0 -1rem; padding: .7rem 1rem .8rem; display: flex; align-items: center; gap: .8rem;
}
.savebar .consequence { font-size: .72rem; color: var(--muted); flex: 1; }
.savebar .consequence.ok { color: var(--green); }

/* Action rows */
.act { display: flex; align-items: center; gap: 1rem; padding: .85rem 0; border-bottom: 1px solid var(--hairline); }
.act .body { flex: 1; }
.act .body .t { font-family: var(--ra); font-size: .92rem; }
.act .body .d { font-size: .76rem; color: var(--muted); margin-top: .1rem; }
.act .btn { flex-shrink: 0; }

/* Events */
table.events { width: 100%; border-collapse: collapse; font-size: .8rem; }
table.events td { padding: .42rem 0; border-bottom: 1px solid #edf0f2; vertical-align: top; }
table.events td.t { color: var(--muted); white-space: nowrap; padding-right: .9rem; font-variant-numeric: tabular-nums; font-family: var(--mono); font-size: .74rem; }
table.events td.lvl { padding-right: .7rem; }
.pip { display: inline-block; width: .55rem; height: .55rem; border-radius: 50%; background: #8b99a5; }
tr.warn .pip { background: var(--red); }
tr.warn td:last-child { color: var(--red-text); }

/* ============================================================
   LEGACY (amber-on-black) — kept so not-yet-rebuilt pages still
   function during the transition. DELETE in the cleanup task.
   ============================================================ */
.legacy-page { background: #000; color: #ffb000; }
.legacy-page h2, .legacy-page h3, .legacy-page legend { font-family: var(--mono); letter-spacing: .08em; text-transform: uppercase; }
.legacy-page fieldset { border: 0; margin: 1.5rem 0 0; padding: .75rem 0 0; border-top: 1px solid #3a2c00; }
.legacy-page label { display: block; margin: 0 0 1rem; font-size: .9rem; color: #8a6200; }
.legacy-page input, .legacy-page select, .legacy-page textarea { background: #0a0a0a; border: 1px solid #3a2c00; color: #ffb000; }
.legacy-page button { background: #000; color: #ffb000; border: 1px solid #ffb000; border-radius: 2px; min-height: 44px; padding: .5rem 1.25rem; cursor: pointer; }
.legacy-page button.primary { background: #ffb000; color: #000; font-weight: 600; }
.legacy-page .error { color: #ff4d3d; border-left: 3px solid #ff4d3d; padding: .5rem .75rem; margin: 0 0 1rem; background: #180a08; }
.legacy-page .flash { background: #ffb000; color: #000; font-weight: 600; padding: .75rem 1rem; margin: 0 0 1rem; }
.legacy-page dl.facts { display: grid; grid-template-columns: max-content 1fr; gap: .4rem 1rem; margin: 0 0 1.5rem; }
.legacy-page dl.facts dt { color: #8a6200; }
.legacy-page dl.facts dd { margin: 0; min-width: 0; overflow-wrap: anywhere; }
.legacy-page .panel-preview img { display: block; width: 100%; max-width: 512px; aspect-ratio: 4 / 1; background: #000; border: 1px solid #ffb000; image-rendering: pixelated; }
.legacy-page table.events td { border-bottom: 1px solid #1a1400; color: #ffb000; }
.legacy-page table.events tr[data-level="WARN"] td, .legacy-page table.events tr[data-level="ERROR"] td { color: #ff4d3d; }

/* Error paragraph (new pages) */
p.error { color: var(--red-text); background: #fbe9e5; border-radius: 4px; padding: .6rem .8rem; font-size: .85rem; }
```

Note the legacy block is scoped under `.legacy-page` — Step 6 adds that class hook.

- [ ] **Step 6: Rewrite `internal/web/templates/layout.html`**

```html
{{define "layout"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{block "title" .}}Trainboard{{end}}</title>
<link rel="stylesheet" href="/static/style.css">
<script src="/static/htmx.min.js" defer></script>
</head>
<body class="{{block "bodyclass" .}}{{end}}">
<header>
  <div class="totem"><a class="brand" href="/">Trainboard</a></div>
  <div class="yellowband"></div>
  {{block "tabs" .}}{{if .LoggedIn}}<nav class="tabs">
    <a href="/"{{if eq .Active "status"}} class="on"{{end}}>Status</a>
    <a href="/config"{{if eq .Active "config"}} class="on"{{end}}>Configuration</a>
    <a href="/actions"{{if eq .Active "actions"}} class="on"{{end}}>Actions</a>
    <form method="post" action="/logout" class="inline"><input type="hidden" name="csrf" value="{{.CSRF}}"><button type="submit">Log out</button></form>
  </nav>{{end}}{{end}}
</header>
<main>{{block "content" .}}{{end}}</main>
</body>
</html>{{end}}
```

- [ ] **Step 7: Add `Active` to `basePage`** in `internal/web/server.go:29`:

```go
type basePage struct {
	LoggedIn bool
	CSRF     string
	Active   string // which tab: "status" | "config" | "actions" | ""
}
```

Then set `Active` where the three page-data structs are built: in `handlers_status.go` (`Active: "status"`), `handlers_config.go` (`Active: "config"`), `handlers_actions.go` (`Active: "actions"`). Search: `grep -rn "basePage{" internal/web/` and add the field at each status/config/actions construction site. Add `{{define "bodyclass"}}legacy-page{{end}}` to each not-yet-rebuilt page template (`status.html`, `config.html`, `actions.html`, `setup.html`, `login.html`, `applied.html`, `rebooting.html`, `setup_done.html`, `setup_wifi_done.html`, `setup_wifi_status.html`) so they keep the amber look until their own task rebuilds them.

- [ ] **Step 8: Run the full gate; fix any template/test fallout**

Run: `make check`
Expected: PASS. Likely fallout: tests asserting the old `<h1>trainboard</h1>` header or nav markup — update those assertions to the new chrome (brand link `Trainboard`, nav labels `Status`/`Configuration`/`Actions`).

- [ ] **Step 9: Commit**

```bash
git add reference/fonts internal/web/static internal/web/templates/layout.html internal/web
git commit -m "feat(web): Wayfinding design system — fonts, tokens, page chrome (#5)"
```

---

### Task 2: Station-name lookup (`internal/stations` + `GET /api/station`)

**Files:**
- Create: `internal/stations/stations.go`, `internal/stations/stations_test.go`, `internal/stations/data/stations.csv`
- Modify: `internal/web/server.go` (route + setupGate exemption), `internal/web/handlers_api.go` (handler)
- Test: `internal/web/handlers_api_test.go`

**Interfaces:**
- Produces: `stations.Name(crs string) (string, bool)` — case-insensitive 3-letter CRS → station name. `GET /api/station?crs=THA` → `200 {"crs":"THA","name":"Thatcham"}` or `404 {"error":"unknown station code"}`; **no auth required** and exempt from setupGate (used on pre-auth setup pages).

- [ ] **Step 1: Fetch and commit the station table**

```bash
mkdir -p internal/stations/data
curl -fsSL https://raw.githubusercontent.com/davwheat/uk-railway-stations/main/stations.json \
  | python3 -c "
import json,sys,csv
rows = json.load(sys.stdin)
w = csv.writer(sys.stdout)
for r in sorted(rows, key=lambda r: r['crsCode']):
    w.writerow([r['crsCode'].upper(), r['stationName']])
" > internal/stations/data/stations.csv
wc -l internal/stations/data/stations.csv && grep '^THA,' internal/stations/data/stations.csv
```
Expected: ~2,580 lines; `THA,Thatcham`. (If the JSON keys differ, inspect with `python3 -c "import json,sys; print(json.load(sys.stdin)[0])"` and adjust — target CSV format is exactly `CRS,Name` per line, no header.)

- [ ] **Step 2: Write the failing test** — `internal/stations/stations_test.go`:

```go
package stations

import "testing"

func TestName(t *testing.T) {
	cases := []struct {
		crs      string
		want     string
		wantOK   bool
	}{
		{"THA", "Thatcham", true},
		{"tha", "Thatcham", true},   // case-insensitive
		{"PAD", "London Paddington", true},
		{"XXX", "", false},
		{"", "", false},
		{"THAM", "", false},
	}
	for _, c := range cases {
		got, ok := Name(c.crs)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Name(%q) = %q,%v; want %q,%v", c.crs, got, ok, c.want, c.wantOK)
		}
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/stations/ -v`
Expected: FAIL — `undefined: Name` (package won't compile yet — create `stations.go` with just `package stations` first if you want a cleaner failure).

- [ ] **Step 4: Implement** — `internal/stations/stations.go`:

```go
// Package stations provides an offline CRS-code → station-name lookup, backed
// by a bundled snapshot of the UK railway station list. Used by the web UI to
// resolve codes as the user types ("THA · Thatcham").
package stations

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed data/stations.csv
var rawCSV string

var (
	once  sync.Once
	table map[string]string
)

func load() {
	table = make(map[string]string, 2700)
	for _, line := range strings.Split(rawCSV, "\n") {
		crs, name, ok := strings.Cut(strings.TrimRight(line, "\r"), ",")
		if !ok || len(crs) != 3 {
			continue
		}
		table[strings.ToUpper(crs)] = strings.Trim(name, `"`)
	}
}

// Name returns the station name for a 3-letter CRS code (case-insensitive).
func Name(crs string) (string, bool) {
	if len(crs) != 3 {
		return "", false
	}
	once.Do(load)
	name, ok := table[strings.ToUpper(crs)]
	return name, ok
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/stations/ -v`
Expected: PASS

- [ ] **Step 6: Write failing handler test** — append to `internal/web/handlers_api_test.go`:

```go
func TestAPIStationLookup(t *testing.T) {
	h, _ := newTestServer(t)
	// No session cookie on purpose: endpoint is public (pre-auth setup uses it).
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/station?crs=tha", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got struct{ CRS, Name string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if got.CRS != "THA" || got.Name != "Thatcham" {
		t.Errorf("got %+v", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/station?crs=XXX", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown CRS: want 404, got %d", rec.Code)
	}
}

func TestAPIStationLookupBypassesSetupGate(t *testing.T) {
	h, _ := newTestServerVirgin(t) // unprovisioned: setupGate active
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/station?crs=PAD", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("setup gate must not redirect /api/station: want 200, got %d", rec.Code)
	}
}
```
(Match the actual helper names/signatures in `server_test.go:25-79`; `newTestServerVirgin` is the unprovisioned variant.)

- [ ] **Step 7: Run to verify failure** — `go test ./internal/web/ -run TestAPIStation -v` → FAIL (404 from mux).

- [ ] **Step 8: Implement handler + route + gate exemption**

In `internal/web/handlers_api.go` add (match the file's existing JSON-writing conventions — reuse its helper if one exists):

```go
func (s *Server) handleAPIStation(w http.ResponseWriter, r *http.Request) {
	crs := r.URL.Query().Get("crs")
	name, ok := stations.Name(crs)
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown station code"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"crs": strings.ToUpper(crs), "name": name})
}
```

In `server.go` route block: `mux.Handle("GET /api/station", http.HandlerFunc(s.handleAPIStation))` — deliberately **no** requireAuth (public data, needed pre-auth). In `setupGate` (`server.go:174-208`), add `/api/station` to the exempt paths alongside `/static/` and the portal probes.

- [ ] **Step 9: Run tests, then the full gate**

Run: `go test ./internal/web/ -run TestAPIStation -v && make check`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/stations internal/web
git commit -m "feat(web): offline CRS→station-name lookup + public /api/station (#5)"
```

---

### Task 3: Board view model (`GET /api/board`)

Expose the pre-rasterization board content as JSON, reusing the exact text the OLED shows.

**Files:**
- Modify: `internal/board/scene_departures.go` (export text builders), `internal/board/row.go` (export ordinal)
- Create: `internal/web/handlers_board.go`, `internal/web/handlers_board_test.go`
- Modify: `internal/web/server.go` (route)

**Interfaces:**
- Consumes: `Sources.Snapshot func() *board.Snapshot` (`service.go:19`); `board.Snapshot{Board *data.Board; State board.State; Fault obs.FaultCode; FaultDetail string; FetchedAt time.Time; Hotspot *board.Hotspot}` (`snapshot.go:50`); `data.Departure` fields (`data/model.go:36`); existing unexported `callingAtText(d, times bool)` / `serviceInfoText(d)` (`scene_departures.go:14,32`) and `ordinal(n)` (`row.go`); `config.LayoutConfig.Times` via `svc.ConfigRedacted()`.
- Produces:
  - `board.CallingAtText(d data.Departure, times bool) string`, `board.ServiceInfoText(d data.Departure) string`, `board.Ordinal(n int) string` (exported renames; internal callers updated).
  - `GET /api/board` (auth'd JSON) returning `boardView`:

```go
type serviceView struct {
	Order       int    `json:"order"`
	Scheduled   string `json:"scheduled"`
	Platform    string `json:"platform,omitempty"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	CallingAt   string `json:"callingAt,omitempty"`
	ServiceInfo string `json:"serviceInfo,omitempty"`
}
type boardView struct {
	State     string        `json:"state"`               // board.State.String() values
	Location  string        `json:"location,omitempty"`  // Board.LocationName
	FetchedAt time.Time     `json:"fetchedAt,omitempty"`
	Message   string        `json:"message,omitempty"`   // error / clock / initialising text
	First     *serviceView  `json:"first,omitempty"`
	Remaining []serviceView `json:"remaining,omitempty"`
	Messages  []string      `json:"messages,omitempty"`  // NRCC messages (no-services scene)
	Hotspot   *struct {
		SSID string `json:"ssid"`
		Addr string `json:"addr"`
	} `json:"hotspot,omitempty"`
}
```

- [ ] **Step 1: Export the text builders (mechanical rename, tests first)**

In `internal/board`, rename `callingAtText` → `CallingAtText`, `serviceInfoText` → `ServiceInfoText`, `ordinal` → `Ordinal` (use gopls/LSP rename or sed + build). Update all internal call sites (`scene_departures.go`, `row.go`, their tests). Add doc comments (exported now):

```go
// CallingAtText is the exact calling-points string the panel scrolls:
// "A, B and C", each name suffixed " (HH:MM)" when times is true.
func CallingAtText(d data.Departure, times bool) string { ... existing body ... }

// ServiceInfoText is the panel's service line: "<Operator> service formed of N coaches".
func ServiceInfoText(d data.Departure) string { ... existing body ... }

// Ordinal renders 1 → "1st", 2 → "2nd" etc, as shown in the board's order column.
func Ordinal(n int) string { ... existing body ... }
```

Run: `go test ./internal/board/ -v`
Expected: PASS (pure rename).

Commit:
```bash
git add internal/board
git commit -m "refactor(board): export CallingAtText/ServiceInfoText/Ordinal for web reuse (#5)"
```

- [ ] **Step 2: Write failing handler tests** — `internal/web/handlers_board_test.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
)

func fetchBoardView(t *testing.T, h http.Handler, cookies ...*http.Cookie) (int, boardView) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var v boardView
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
			t.Fatalf("bad JSON: %v: %s", err, rec.Body.String())
		}
	}
	return rec.Code, v
}

func TestAPIBoardRequiresAuth(t *testing.T) {
	h, _ := newTestServer(t)
	code, _ := fetchBoardView(t, h) // no cookie
	if code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", code)
	}
}

func TestAPIBoardDepartures(t *testing.T) {
	dep := data.Departure{
		ScheduledTime: "21:47",
		Platform:      "2",
		Status:        data.StatusOnTime,
		Operator:      "Great Western Railway",
		Length:        5,
		Destination:   data.Location{Name: "London Paddington", CRS: "PAD"},
		CallingPoints: []data.CallingPoint{
			{Location: data.Location{Name: "Southall"}, ScheduledTime: "22:05"},
		},
	}
	rest := data.Departure{
		ScheduledTime: "21:58",
		Status:        data.Status("Exp 22:04"),
		Destination:   data.Location{Name: "Reading", CRS: "RDG"},
	}
	snap := &board.Snapshot{
		State:     board.StateDepartures,
		FetchedAt: time.Now(),
		Board: &data.Board{
			LocationName: "Thatcham",
			CRS:          "THA",
			Departures:   []data.Departure{dep, rest},
		},
	}
	h, cookie := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return snap }})
	code, v := fetchBoardView(t, h, cookie)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if v.State != "departures" || v.Location != "Thatcham" {
		t.Errorf("state/location: %+v", v)
	}
	if v.First == nil || v.First.Destination != "London Paddington" || v.First.Order != 1 ||
		v.First.Scheduled != "21:47" || v.First.Platform != "2" || v.First.Status != "On time" {
		t.Errorf("first: %+v", v.First)
	}
	if v.First.CallingAt == "" || v.First.ServiceInfo == "" {
		t.Errorf("first text lines empty: %+v", v.First)
	}
	if len(v.Remaining) != 1 || v.Remaining[0].Destination != "Reading" || v.Remaining[0].Order != 2 {
		t.Errorf("remaining: %+v", v.Remaining)
	}
}

func TestAPIBoardNilSnapshot(t *testing.T) {
	h, cookie := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return nil }})
	code, v := fetchBoardView(t, h, cookie)
	if code != http.StatusOK || v.State != "initialising" {
		t.Errorf("nil snapshot: code %d view %+v", code, v)
	}
}

func TestAPIBoardErrorState(t *testing.T) {
	snap := &board.Snapshot{State: board.StateError, FaultDetail: "darwin unreachable"}
	h, cookie := newTestServerWithSources(t, Sources{Snapshot: func() *board.Snapshot { return snap }})
	code, v := fetchBoardView(t, h, cookie)
	if code != http.StatusOK || v.State != "error" || v.Message == "" {
		t.Errorf("error state: code %d view %+v", code, v)
	}
}
```
(`newTestServerWithSources` exists at `handlers_status_test.go:28` — reuse it; adapt the return values to its real signature, it must yield an authenticated cookie or pair with `loginAs` from `server_test.go:108`.)

- [ ] **Step 3: Run to verify failure** — `go test ./internal/web/ -run TestAPIBoard -v` → FAIL (undefined boardView / 404).

- [ ] **Step 4: Implement** — `internal/web/handlers_board.go`:

```go
package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
)

type serviceView struct {
	Order       int    `json:"order"`
	Scheduled   string `json:"scheduled"`
	Platform    string `json:"platform,omitempty"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	CallingAt   string `json:"callingAt,omitempty"`
	ServiceInfo string `json:"serviceInfo,omitempty"`
}

type hotspotView struct {
	SSID string `json:"ssid"`
	Addr string `json:"addr"`
}

type boardView struct {
	State     string        `json:"state"`
	Location  string        `json:"location,omitempty"`
	FetchedAt time.Time     `json:"fetchedAt,omitempty"`
	Message   string        `json:"message,omitempty"`
	First     *serviceView  `json:"first,omitempty"`
	Remaining []serviceView `json:"remaining,omitempty"`
	Messages  []string      `json:"messages,omitempty"`
	Hotspot   *hotspotView  `json:"hotspot,omitempty"`
}

// buildBoardView mirrors board.BuildScene's priority order
// (Hotspot > Error > ClockNotSynced > NoServices > Departures > Initialising)
// so the web preview always matches what the panel shows.
func buildBoardView(snap *board.Snapshot, times bool) boardView {
	if snap == nil {
		return boardView{State: board.StateInitialising.String(), Message: "Starting up…"}
	}
	v := boardView{State: snap.State.String(), FetchedAt: snap.FetchedAt}
	if snap.Board != nil {
		v.Location = snap.Board.LocationName
		v.Messages = snap.Board.Messages
	}
	if snap.Hotspot != nil {
		v.State = "hotspot"
		v.Hotspot = &hotspotView{SSID: snap.Hotspot.SSID, Addr: snap.Hotspot.Addr}
		return v
	}
	switch snap.State {
	case board.StateError:
		v.Message = snap.FaultDetail
		if v.Message == "" {
			v.Message = "Something went wrong — see recent events"
		}
	case board.StateClockNotSynced:
		v.Message = "Waiting for the clock to sync"
	case board.StateDepartures:
		if snap.Board != nil && len(snap.Board.Departures) > 0 {
			deps := snap.Board.Departures
			first := toServiceView(1, deps[0])
			first.CallingAt = board.CallingAtText(deps[0], times)
			first.ServiceInfo = board.ServiceInfoText(deps[0])
			v.First = &first
			for i, d := range deps[1:] {
				v.Remaining = append(v.Remaining, toServiceView(i+2, d))
			}
		}
	}
	return v
}

func toServiceView(order int, d data.Departure) serviceView {
	return serviceView{
		Order:       order,
		Scheduled:   d.ScheduledTime,
		Platform:    d.Platform,
		Destination: d.Destination.Name,
		Status:      string(d.Status),
	}
}

func (s *Server) handleAPIBoard(w http.ResponseWriter, r *http.Request) {
	times := false
	if cfg, err := s.svc.ConfigRedacted(); err == nil {
		times = cfg.Layout.Times
	}
	var snap *board.Snapshot
	if s.svc.src.Snapshot != nil {
		snap = s.svc.src.Snapshot()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(buildBoardView(snap, times))
}
```

Route in `server.go` next to the other API routes, same wrapping: `chain(http.HandlerFunc(s.handleAPIBoard), apiJSONErrors(...), requireAuth(s.sessions, true))` — copy the exact wrapper composition used by `GET /api/status` (`server.go:134-155`).

- [ ] **Step 5: Run tests + gate**

Run: `go test ./internal/web/ -run TestAPIBoard -v && make check`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): GET /api/board JSON row model mirroring panel scenes (#5)"
```

---

### Task 4: Client board renderer; delete `/preview.png`

**Files:**
- Create: `internal/web/static/board.js`
- Modify: `internal/web/templates/status.html` (board container replaces `<img>`; keep the rest of the page legacy for now)
- Modify: `internal/web/server.go` (remove `/preview.png` route), `internal/web/handlers_status.go` (remove `handlePreviewPNG`), `internal/web/service.go` (remove `Sources.PreviewPNG`)
- Modify: `cmd/trainboard/main.go` (remove wiring), delete `cmd/trainboard/preview.go` + its test if the sink has no other consumer (verify first: `grep -rn "previewSink\|PreviewPNG\|previewLatest" cmd/ internal/`)
- Test: update `internal/web/handlers_status_test.go`, `internal/web/e2e_test.go` (drop PreviewPNG source), add board.js serving test

**Interfaces:**
- Consumes: `GET /api/board` JSON (Task 3 `boardView` schema, exact field names).
- Produces: `<div class="board" id="board" data-endpoint="/api/board"></div>` + `<p class="caption" id="board-caption"></p>` markup contract; `/static/board.js` renders into it. `Sources` struct no longer has `PreviewPNG` (later tasks and `main.go` must not reference it).

- [ ] **Step 1: Write `internal/web/static/board.js`**

```js
// Live board renderer: polls /api/board and renders the panel content as DOM.
// Replaces the old 1fps /preview.png poll (PNG encode on the Pi per second).
// All content set via textContent — never innerHTML — the data is remote text.
(function () {
  "use strict";
  var root = document.getElementById("board");
  if (!root) return;
  var caption = document.getElementById("board-caption");
  var endpoint = root.dataset.endpoint || "/api/board";
  var last = "";

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text) e.textContent = text;
    return e;
  }

  function row(parts) { // parts: array of [cls, text]
    var r = el("div", "row");
    parts.forEach(function (p) { r.appendChild(el("span", p[0], p[1])); });
    return r;
  }

  function render(v) {
    var key = JSON.stringify(v);
    if (key === last) return; // don't restart the marquee for identical data
    last = key;
    root.textContent = "";

    if (v.state === "departures" && v.first) {
      root.appendChild(row([["", v.first.order + " " + v.first.scheduled + " "],
        ["dest", v.first.destination],
        ["", (v.first.platform ? "Plat " + v.first.platform + "  " : "") + v.first.status]]));
      var clip = el("div", "marquee-clip");
      clip.appendChild(el("span", "marquee",
        "Calling at: " + (v.first.callingAt || "") +
        (v.first.serviceInfo ? "   ·   " + v.first.serviceInfo : "")));
      root.appendChild(clip);
      (v.remaining || []).slice(0, 1).forEach(function (s) {
        root.appendChild(row([["", s.order + " " + s.scheduled + " "], ["dest", s.destination], ["", s.status]]));
      });
    } else if (v.state === "hotspot" && v.hotspot) {
      root.appendChild(row([["dest center", "Setup mode"]]));
      root.appendChild(row([["dest center", "Join hotspot: " + v.hotspot.ssid]]));
      root.appendChild(row([["dest center", "Then open http://" + v.hotspot.addr]]));
    } else if (v.state === "no-services") {
      root.appendChild(row([["dest center", v.location || ""]]));
      root.appendChild(row([["dest center", (v.messages && v.messages[0]) || "No services to show"]]));
    } else {
      root.appendChild(row([["dest center", v.message || v.state]]));
    }
    var clock = el("div", "clockline", "");
    clock.id = "board-clock";
    root.appendChild(clock);
    tickClock();
  }

  function tickClock() {
    var c = document.getElementById("board-clock");
    if (!c) return;
    c.textContent = new Date().toLocaleTimeString("en-GB", { timeZone: "Europe/London", hour12: false });
  }

  function staleness(v) {
    if (!caption) return;
    if (!v.fetchedAt) { caption.textContent = "Live panel · rendered in your browser"; return; }
    var age = Math.max(0, (Date.now() - Date.parse(v.fetchedAt)) / 1000);
    if (age > 300) {
      root.classList.add("stale");
      caption.textContent = "Live panel · data " + Math.round(age / 60) + " min old";
    } else {
      root.classList.remove("stale");
      caption.textContent = "Live panel · data " + Math.round(age) + "s old";
    }
  }

  function poll() {
    fetch(endpoint, { headers: { Accept: "application/json" } })
      .then(function (r) {
        if (r.status === 401) { window.location.href = "/login"; throw new Error("unauthenticated"); }
        return r.json();
      })
      .then(function (v) { render(v); staleness(v); })
      .catch(function () { /* transient; next poll retries */ });
  }

  poll();
  setInterval(poll, 5000);
  setInterval(tickClock, 1000);
})();
```

- [ ] **Step 2: Swap the preview block in `templates/status.html`** — replace lines 4-11 (the `panel-preview` section + script) with:

```html
<section>
  <div class="board" id="board" data-endpoint="/api/board" role="img" aria-label="live departure board preview"></div>
  <p class="caption" id="board-caption">Live panel · rendered in your browser</p>
</section>
<script src="/static/board.js" defer></script>
```

- [ ] **Step 3: Delete the PNG path.** Verify consumers first:

```bash
grep -rn "PreviewPNG\|previewSink\|previewLatest\|preview.png" cmd/ internal/ --include="*.go" --include="*.html"
```

Then: remove `GET /preview.png` route (`server.go`), `handlePreviewPNG` (`handlers_status.go:77-86`), `Sources.PreviewPNG` field (`service.go:22`), its wiring in `cmd/trainboard/main.go:243`, and — if the grep shows no remaining consumer of the gray-image/PNG encode — `cmd/trainboard/preview.go` and its test wholesale (if the sink also feeds a host-mode window or anything else, keep the sink and delete only the PNG-encode + `Latest`). Remove `PreviewPNG` from every test fixture (`e2e_test.go:30` sources literal, `handlers_status_test.go` fixtures).

- [ ] **Step 4: Add regression tests** — append to `internal/web/handlers_status_test.go`:

```go
func TestPreviewPNGGone(t *testing.T) {
	h, cookie := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/preview.png", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("preview.png must be gone: want 404, got %d", rec.Code)
	}
}

func TestStatusPageHasBoardContainer(t *testing.T) {
	h, cookie := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{`id="board"`, `data-endpoint="/api/board"`, `/static/board.js`} {
		if !strings.Contains(body, want) {
			t.Errorf("status page missing %q", want)
		}
	}
}
```

- [ ] **Step 5: Full gate**

Run: `make check`
Expected: PASS — compiler will point at every stale `PreviewPNG` reference; fix them all.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(web): client-rendered live board; delete /preview.png and PNG sink (#5)"
```

---

### Task 5: Status page redesign

**Files:**
- Modify: `internal/web/templates/status.html` (full rewrite), `internal/web/handlers_status.go` (view data)
- Test: `internal/web/handlers_status_test.go`

**Interfaces:**
- Consumes: `StatusData` (`service.go:84-97`), `update.Status` (`checker.go:26`), CSS classes from Task 1, board container contract from Task 4.
- Produces: `statusPageData` gains `StateLabel, StateClass, StateDetail string` and `HotspotActive bool`; template defines `"eventlist"` partial (same name as today — `/events` handler keeps working unchanged).

- [ ] **Step 1: Write failing tests for the state mapping** — append to `handlers_status_test.go`:

```go
func TestStateLine(t *testing.T) {
	cases := []struct {
		name      string
		st        StatusData
		wantLabel string
		wantClass string
	}{
		{"departures", StatusData{State: "departures", LastFetch: time.Now()}, "Running normally", "ok"},
		{"no services", StatusData{State: "no-services", LastFetch: time.Now()}, "Running — no services to show", "ok"},
		{"initialising", StatusData{State: "initialising"}, "Starting up", "warn"},
		{"clock", StatusData{State: "clock-not-synced"}, "Waiting for clock sync", "warn"},
		{"fault", StatusData{State: "error", Fault: "E02"}, "Fault E02", "bad"},
		{"stale", StatusData{State: "departures", LastFetch: time.Now().Add(-10 * time.Minute)}, "Running — data is stale", "warn"},
	}
	for _, c := range cases {
		label, class, _ := stateLine(c.st, time.Now())
		if label != c.wantLabel || class != c.wantClass {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", c.name, label, class, c.wantLabel, c.wantClass)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/web/ -run TestStateLine -v` → FAIL (`undefined: stateLine`).

- [ ] **Step 3: Implement `stateLine`** in `handlers_status.go`:

```go
// staleAfter is how old the last successful fetch may be before the status
// page calls the data stale (2× the max refresh anyone sane configures).
const staleAfter = 5 * time.Minute

// stateLine maps runtime state to the status page's headline: label, css
// class ("ok"|"warn"|"bad"), and a short detail sentence (may be empty).
func stateLine(st StatusData, now time.Time) (label, class, detail string) {
	switch st.State {
	case "departures", "no-services":
		if !st.LastFetch.IsZero() && now.Sub(st.LastFetch) > staleAfter {
			return "Running — data is stale", "warn",
				"Last successful fetch " + st.LastFetch.Format("15:04:05") + ". Check recent events below."
		}
		if st.State == "no-services" {
			return "Running — no services to show", "ok", ""
		}
		return "Running normally", "ok", ""
	case "initialising":
		return "Starting up", "warn", "The board is connecting and fetching first departures."
	case "clock-not-synced":
		return "Waiting for clock sync", "warn", "Departure times need an accurate clock; this resolves itself within a minute or two of network access."
	case "error":
		f := st.Fault
		if f == "" {
			f = "unknown"
		}
		return "Fault " + f, "bad", "The panel shows details. Recent events below usually name the cause."
	default:
		return st.State, "warn", ""
	}
}
```

Extend `statusPageData` (`handlers_status.go:13`) with `StateLabel, StateClass, StateDetail string` and `HotspotActive bool`; populate in `handleIndex` via `stateLine(st, time.Now())` and `s.svc.Hotspot() != nil`. Set `Active: "status"` (Task 1 field).

- [ ] **Step 4: Rewrite `templates/status.html`**

```html
{{define "title"}}Status — Trainboard{{end}}
{{define "content"}}
<div class="statebar">
  <span class="dot{{if eq .StateClass "warn"}} amber{{else if eq .StateClass "bad"}} red{{end}}"></span>
  {{.StateLabel}}
  <span class="since">up {{.UptimeText}}</span>
</div>
{{if .StateDetail}}<p class="caption" style="margin-top:-.4rem">{{.StateDetail}}</p>{{end}}

<section>
  <div class="board" id="board" data-endpoint="/api/board" role="img" aria-label="live departure board preview"></div>
  <p class="caption" id="board-caption">Live panel · rendered in your browser</p>
</section>

{{if .HotspotActive}}
<div class="notice"><strong>Hotspot mode.</strong> The board couldn't join your WiFi and is running its own hotspot. <a href="/actions">Retry from Actions</a> once your network is back.</div>
{{end}}

{{with .Status.Update}}
{{if .RolledBackFrom}}
<div class="notice fault"><strong>Rolled back.</strong> {{.RolledBackFrom}} failed to boot repeatedly — running {{.Running}} instead.
  <form method="post" action="/actions/update/dismiss" class="btnrow" style="margin-top:.6rem"><input type="hidden" name="csrf" value="{{$.CSRF}}">
    <button class="btn ghost" type="submit">Dismiss</button></form>
</div>
{{end}}
{{if .Available}}
<div class="notice"><strong>Update available.</strong> {{.Available}} is ready to install — the board restarts and is back in about a minute.{{if .NotesURL}} <a href="{{.NotesURL}}" target="_blank" rel="noopener">Release notes</a>.{{end}}</div>
<div class="btnrow" style="margin-bottom:1rem">
  <form method="post" action="/actions/update/apply"><input type="hidden" name="csrf" value="{{$.CSRF}}">
    <button class="btn" type="submit">Install {{.Available}}</button></form>
  <form method="post" action="/actions/update/check"><input type="hidden" name="csrf" value="{{$.CSRF}}">
    <button class="btn ghost" type="submit">Check again</button></form>
</div>
{{else if .Enabled}}
<form method="post" action="/actions/update/check" style="margin:0 0 1rem"><input type="hidden" name="csrf" value="{{$.CSRF}}">
  <button class="btn ghost" type="submit">Check for updates</button></form>
{{end}}
{{if .LastError}}<p class="error">Update check failed: {{.LastError}}</p>{{end}}
{{end}}

{{if .SoakRemainingText}}
<div class="notice calm">Burn-in soak running — {{.SoakRemainingText}} remaining. The panel cycles white/black; departures resume when it ends. <a href="/actions">Cancel from Actions</a>.</div>
{{end}}

<h3>This board</h3>
<div class="rows">
  <div class="r"><span class="k">Software</span><span class="v">{{.Status.Update.Running}}{{if not .Status.Update.Running}}{{.Status.Version}}{{end}}</span></div>
  <div class="r"><span class="k">Address</span><span class="v">{{range $i, $ip := .Status.IPs}}{{if $i}} · {{end}}{{$ip}}{{end}}{{if .MDNSState}} · trainboard.local{{end}}</span></div>
  <div class="r"><span class="k">Last data fetch</span><span class="v">{{if .Status.LastFetch.IsZero}}never{{else}}{{.Status.LastFetch.Format "15:04:05"}}{{end}}</span></div>
</div>

<h3>Recent events</h3>
<div id="events" hx-get="/events" hx-trigger="every 5s" hx-swap="innerHTML">
  {{template "eventlist" .Status.Events}}
</div>
<script src="/static/board.js" defer></script>
{{end}}
{{define "eventlist"}}
<table class="events"><tbody>
{{range .}}<tr{{if or (eq .Level "WARN") (eq .Level "ERROR")}} class="warn"{{end}}><td class="t">{{.Time.Format "15:04:05"}}</td><td class="lvl"><span class="pip"></span></td><td>{{.Msg}}</td></tr>{{end}}
</tbody></table>
{{end}}
```

Remove the `legacy-page` bodyclass define from this template (it is now a Wayfinding page). Note: `obs.Event.Level` comparison — check the actual type (`obs.Event` fields) with `grep -n "type Event" internal/obs/*.go`; if `Level` is a typed string, compare via `printf "%s" .Level` or add a template func — match what the current template does with `data-level="{{.Level}}"`.

- [ ] **Step 5: Update markup assertions in existing status tests, run gate**

Run: `make check`
Expected: PASS (fix any test asserting old `dl.facts` markup).

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): Wayfinding status page — state line, notices, fact rows (#5)"
```

---

### Task 6: Configuration list + Departures & Display sub-pages

**Files:**
- Create: `internal/web/templates/config_list.html`, `internal/web/templates/config_departures.html`, `internal/web/templates/config_display.html`
- Modify: `internal/web/handlers_config.go`, `internal/web/templates.go`, `internal/web/server.go` (routes)
- Test: `internal/web/handlers_config_test.go`

**Interfaces:**
- Consumes: `config.Config` sections (`config.go:9-18`), `Service.ConfigRedacted()`, `Service.UpdateConfig(ConfigUpdate)` (`service.go:142,171`), parse helpers `parseIntField`/`formHasKey`/`splitCSV`/`joinCSV`/`parseReplacements`/`formatReplacements` (`handlers_config.go:160-238`), `stations.Name` (Task 2), `scheduleApply` (`handlers_actions.go:44`).
- Produces: routes `GET /config` (list), `GET+POST /config/departures`, `GET+POST /config/display`; summary builders `summarizeDepartures(cfg) string`, `summarizeDisplay(cfg) string` etc. (one per section, all in `handlers_config.go`); POST pattern for Task 7 to copy: parse only own fields onto a fresh `ConfigRedacted()` copy → `UpdateConfig` → on success 303 to `/restarting` (Task 8 adds that page — until then 303 to `/config`); template names registered as `"configList"`, `"configDepartures"`, `"configDisplay"` in the `render` switch.

- [ ] **Step 1: Write failing tests** — replace the old single-form tests progressively; add to `handlers_config_test.go`:

```go
func TestConfigListShowsSummaries(t *testing.T) {
	h, cookie := newTestServer(t) // provisioned server with validCfg()
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"/config/departures", "/config/display", "/config/network", "/config/updates", "/config/admin"} {
		if !strings.Contains(body, want) {
			t.Errorf("config list missing link %q", want)
		}
	}
}

func TestConfigDepartures(t *testing.T) {
	h, cookie, csrf, applyCh := newTestServerWithApply(t)
	// GET renders the form with current values
	req := httptest.NewRequest(http.MethodGet, "/config/departures", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `name="board.origin"`) {
		t.Fatalf("GET form: code %d", rec.Code)
	}
	// POST with changed origin saves and schedules apply
	form := url.Values{
		"csrf":                    {csrf},
		"board.origin":            {"PAD"},
		"board.destination":       {""},
		"board.platforms":         {""},
		"board.tocs":              {""},
		"board.services":          {"3"},
		"board.cutoffHours":       {"3"},
		"board.refreshSeconds":    {"30"},
		"board.timeWindowMinutes": {"120"},
		"board.replacements":      {""},
	}
	rec = postForm(t, h, "/config/departures", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST: want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh)
}

func TestConfigDeparturesValidationError(t *testing.T) {
	h, cookie, csrf, _ := newTestServerWithApply(t)
	form := url.Values{"csrf": {csrf}, "board.origin": {"XX"}, "board.services": {"3"},
		"board.cutoffHours": {"3"}, "board.refreshSeconds": {"30"}, "board.timeWindowMinutes": {"120"}}
	rec := postForm(t, h, "/config/departures", form, cookie)
	if rec.Code != http.StatusOK { // re-rendered form with error, not a redirect
		t.Fatalf("want 200 re-render, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "station code") {
		t.Errorf("expected a validation message naming the station code")
	}
}
```
(Adapt helper signatures to the real `newTestServerWithApply` at `server_test.go:25-79` / `awaitApply` at `handlers_config_test.go:169`.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/web/ -run 'TestConfigList|TestConfigDepartures' -v` → FAIL.

- [ ] **Step 3: Implement handlers** in `handlers_config.go`. Shape (Departures shown; Display is the same pattern over `powersaving.*` + `layout.times`):

```go
type configListPageData struct {
	basePage
	Sections []configSectionSummary
}
type configSectionSummary struct {
	Slug, Title, Summary string
}

func (s *Server) handleConfigList(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.render(w, "configList", configListPageData{
		basePage: s.pageBase(r, "config"),
		Sections: []configSectionSummary{
			{"departures", "Departures", summarizeDepartures(cfg)},
			{"display", "Display", summarizeDisplay(cfg)},
			{"network", "Network", summarizeNetwork(cfg)},
			{"updates", "Updates", summarizeUpdates(cfg)},
			{"admin", "Admin", "Password"},
		},
	})
}

func summarizeDepartures(cfg config.Config) string {
	origin := cfg.Board.Origin
	if name, ok := stations.Name(origin); ok {
		origin = name
	}
	sum := origin
	if cfg.Board.Destination != "" {
		dest := cfg.Board.Destination
		if name, ok := stations.Name(dest); ok {
			dest = name
		}
		sum += " → " + dest
	}
	return fmt.Sprintf("%s · %d services", sum, cfg.Board.Services)
}

func summarizeDisplay(cfg config.Config) string {
	if !cfg.Powersaving.Enabled {
		return "Full brightness all day"
	}
	return fmt.Sprintf("Dim %s–%s · brightness %d", cfg.Powersaving.Start, cfg.Powersaving.End, cfg.Powersaving.Brightness)
}

func summarizeNetwork(cfg config.Config) string {
	ssid := cfg.Wifi.SSID
	if ssid == "" {
		ssid = "WiFi not set"
	}
	token := "Darwin token not set"
	if cfg.Darwin.Token != "" { // ConfigRedacted redacts but leaves presence detectable — verify; else expose a HasToken bool from the service
		token = "Darwin token set"
	}
	return ssid + " · " + token
}

func summarizeUpdates(cfg config.Config) string {
	sum := cfg.Update.EffectiveChannel()
	if cfg.Update.AutoApply {
		sum += " · automatic overnight"
	}
	if cfg.Update.DisableChecks {
		sum += " · checks off"
	}
	return sum
}
```

Add a small helper on Server (used everywhere from now on):

```go
// pageBase fills the fields every page shares. active is the highlighted tab.
func (s *Server) pageBase(r *http.Request, active string) basePage {
	return basePage{LoggedIn: true, CSRF: csrfFrom(r), Active: active}
}
```

Departures GET/POST:

```go
type configDeparturesPageData struct {
	basePage
	Cfg              config.Config
	Error            string
	PlatformsCSV     string
	TOCsCSV          string
	ReplacementsText string
	OriginName       string // resolved station name or ""
	DestinationName  string
}

func (s *Server) handleConfigDeparturesGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	s.renderConfigDepartures(w, r, cfg, "")
}

func (s *Server) renderConfigDepartures(w http.ResponseWriter, r *http.Request, cfg config.Config, errMsg string) {
	d := configDeparturesPageData{
		basePage:         s.pageBase(r, "config"),
		Cfg:              cfg,
		Error:            errMsg,
		PlatformsCSV:     joinCSV(cfg.Board.Platforms),
		TOCsCSV:          joinCSV(cfg.Board.TOCs),
		ReplacementsText: formatReplacements(cfg.Board.Replacements),
	}
	d.OriginName, _ = stations.Name(cfg.Board.Origin)
	d.DestinationName, _ = stations.Name(cfg.Board.Destination)
	s.render(w, "configDepartures", d)
}

func (s *Server) handleConfigDeparturesPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	cfg, err := s.svc.ConfigRedacted()
	if err != nil {
		http.Error(w, "config unreadable", http.StatusInternalServerError)
		return
	}
	// Parse ONLY this section's fields onto the loaded config.
	var perr error
	cfg.Board.Origin = strings.ToUpper(strings.TrimSpace(r.PostFormValue("board.origin")))
	cfg.Board.Destination = strings.ToUpper(strings.TrimSpace(r.PostFormValue("board.destination")))
	cfg.Board.Platforms = splitCSV(r.PostFormValue("board.platforms"))
	cfg.Board.TOCs = splitCSV(r.PostFormValue("board.tocs"))
	if cfg.Board.Services, perr = parseIntField(r, "board.services", perr); perr != nil { /* collected below */ }
	if cfg.Board.CutoffHours, perr = parseIntField(r, "board.cutoffHours", perr); perr != nil {
	}
	if cfg.Board.RefreshSeconds, perr = parseIntField(r, "board.refreshSeconds", perr); perr != nil {
	}
	if cfg.Board.TimeWindowMinutes, perr = parseIntField(r, "board.timeWindowMinutes", perr); perr != nil {
	}
	cfg.Board.Replacements, perr = parseReplacements(r.PostFormValue("board.replacements"), perr)
	if _, ok := stations.Name(cfg.Board.Origin); !ok && perr == nil {
		perr = fmt.Errorf("%q is not a station code we recognise — 3 letters, e.g. PAD", cfg.Board.Origin)
	}
	if perr == nil {
		perr = s.svc.UpdateConfig(ConfigUpdate{Cfg: cfg})
	}
	if perr != nil {
		s.renderConfigDepartures(w, r, cfg, perr.Error())
		return
	}
	s.scheduleApply()
	http.Redirect(w, r, "/restarting", http.StatusSeeOther)
}
```

**Adapt the `parseIntField`/`parseReplacements` calls to their real signatures** (`handlers_config.go:160-238`) — the code above assumes `(value, err)` accumulation; mirror exactly how the existing `parseConfigForm` uses them. Until Task 8 creates `/restarting`, redirect to `/config` instead and leave a `// TODO(task-8)` comment — Task 8's checklist includes flipping this redirect.

Routes in `server.go` (replacing old config routes; POST /config HTML route is deleted, `PUT /api/config` untouched):

```go
mux.Handle("GET /config", chain(http.HandlerFunc(s.handleConfigList), requireAuth(s.sessions, false)))
mux.Handle("GET /config/departures", chain(http.HandlerFunc(s.handleConfigDeparturesGet), requireAuth(s.sessions, false)))
mux.Handle("POST /config/departures", chain(http.HandlerFunc(s.handleConfigDeparturesPost), rateLimit(s.actionLimit, s.log), requireAuth(s.sessions, false), csrfProtect(s.log)))
// same trio pattern for /config/display
```
(Copy the exact middleware composition order from the old `POST /config` registration.)

- [ ] **Step 4: Templates.** `templates/config_list.html`:

```html
{{define "title"}}Configuration — Trainboard{{end}}
{{define "content"}}
<h2>Configuration</h2>
<div class="setlist">
{{range .Sections}}
  <a class="setrow" href="/config/{{.Slug}}"><span><span class="t">{{.Title}}</span><div class="s">{{.Summary}}</div></span><span class="chev">›</span></a>
{{end}}
</div>
{{end}}
```

`templates/config_departures.html`:

```html
{{define "title"}}Departures — Trainboard{{end}}
{{define "content"}}
<div class="backrow"><a href="/config">‹ Configuration</a></div>
<h3>Departures</h3>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post" action="/config/departures">
<input type="hidden" name="csrf" value="{{.CSRF}}">
<label class="f">Station
  <input class="crs" type="text" name="board.origin" value="{{.Cfg.Board.Origin}}" required maxlength="3" pattern="[A-Za-z]{3}"
         hx-get="/api/station" hx-trigger="keyup changed delay:400ms" hx-target="#origin-name" hx-swap="none"
         hx-on::after-request="document.getElementById('origin-name').textContent = event.detail.successful ? JSON.parse(event.detail.xhr.responseText).name : 'unknown station code'">
  <div class="hint">3-letter station code{{if .OriginName}} · {{end}}<span id="origin-name">{{.OriginName}}</span></div>
</label>
<label class="f">Only trains towards <span style="color:#8b99a5">(optional)</span>
  <input class="crs" type="text" name="board.destination" value="{{.Cfg.Board.Destination}}" maxlength="3"
         hx-get="/api/station" hx-trigger="keyup changed delay:400ms" hx-target="#dest-name" hx-swap="none"
         hx-on::after-request="document.getElementById('dest-name').textContent = event.detail.successful ? JSON.parse(event.detail.xhr.responseText).name : ''">
  <div class="hint"><span id="dest-name">{{.DestinationName}}</span></div>
</label>
<label class="f">Platforms
  <input type="text" name="board.platforms" value="{{.PlatformsCSV}}" placeholder="All platforms">
  <div class="hint">Comma-separated, e.g. 1, 2 — leave empty for all</div>
</label>
<label class="f">Operators
  <input type="text" name="board.tocs" value="{{.TOCsCSV}}" placeholder="All operators">
  <div class="hint">Comma-separated TOC codes, e.g. GW, XR — leave empty for all</div>
</label>
<label class="f">Services shown
  <input type="number" name="board.services" value="{{.Cfg.Board.Services}}" min="1" max="10" style="max-width:8rem">
</label>
<label class="f">Ignore departures more than
  <input type="number" name="board.cutoffHours" value="{{.Cfg.Board.CutoffHours}}" min="1" style="max-width:8rem">
  <div class="hint">hours away</div>
</label>
<label class="f">Refresh every
  <input type="number" name="board.refreshSeconds" value="{{.Cfg.Board.RefreshSeconds}}" min="15" style="max-width:8rem">
  <div class="hint">seconds</div>
</label>
<label class="f">Look ahead
  <input type="number" name="board.timeWindowMinutes" value="{{.Cfg.Board.TimeWindowMinutes}}" min="1" style="max-width:8rem">
  <div class="hint">minutes</div>
</label>
<label class="f">Station name replacements
  <textarea name="board.replacements" rows="3">{{.ReplacementsText}}</textarea>
  <div class="hint">One per line, From=To — e.g. London Paddington=Paddington</div>
</label>
<div class="savebar">
  <span class="consequence">Restarts the board — departures pause ~15 seconds.</span>
  <button class="btn" type="submit">Save</button>
</div>
</form>
{{end}}
```

Note the htmx hint pattern: `hx-swap="none"` + `hx-on::after-request` writing `textContent` (not innerHTML) — copy it verbatim for every CRS field in later tasks. `templates/config_display.html` follows the same shape with the four `powersaving.*` fields (Enabled checkbox, Start, End HH:MM text inputs with `pattern="[0-2][0-9]:[0-5][0-9]"`, Brightness number 0-255) plus the `layout.times` checkbox ("Show calling-point times on the panel"), same savebar/consequence.

Register both in `templates.go` (clone pattern) + `render` switch cases `"configList"`, `"configDepartures"`, `"configDisplay"`. Delete `templates/config.html` and its `configTemplate` var **in Task 7** (network/updates/admin fields still live there until then — keep `GET /config-legacy` unrouted; just leave the file registered but unreachable if the compiler needs it, simplest is to leave the old template + var untouched this task).

- [ ] **Step 5: Run gate; migrate the old config tests that posted to `/config`** — `baseConfigForm` (`handlers_config_test.go:136`) callers split across `/config/departures` + `/config/display` forms; e2e journey step posting `/config` updates in Task 7 when all sections exist (until then point it at `/config/departures`).

Run: `make check`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): settings-list config + departures & display sections (#5)"
```

---

### Task 7: Network, Updates & Admin sub-pages; retire the monolith form

**Files:**
- Create: `internal/web/templates/config_network.html`, `internal/web/templates/config_updates.html`, `internal/web/templates/config_admin.html`
- Delete: `internal/web/templates/config.html`
- Modify: `internal/web/handlers_config.go`, `internal/web/templates.go`, `internal/web/server.go`
- Test: `internal/web/handlers_config_test.go`, `internal/web/e2e_test.go`

**Interfaces:**
- Consumes: everything Task 6 produced (pattern, `pageBase`, summaries), `ConfigUpdate{Cfg, NewToken, NewWifiPSK, NewPassword}` (`service.go:152`), fact from investigation: **`VerifyLogin` reads the hash from disk per attempt (`service.go:322-337`) → Admin save needs NO restart; the update Checker snapshots config at construction (`checker.go:69`) → Updates save DOES restart.**
- Produces: routes `GET+POST /config/{network,updates,admin}`; old HTML `POST /config` and `configTemplate` removed.

- [ ] **Step 1: Failing tests** (same shape as Task 6's — one per section; key assertions):

```go
func TestConfigNetworkSavesSecretsWriteOnly(t *testing.T) {
	h, cookie, csrf, applyCh := newTestServerWithApply(t)
	form := url.Values{"csrf": {csrf}, "wifi.ssid": {"NewNet"}, "wifi.psk": {"newpassword1"}, "darwin.token": {""}}
	rec := postForm(t, h, "/config/network", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d: %s", rec.Code, rec.Body.String())
	}
	awaitApply(t, applyCh) // network changes restart
	// empty darwin.token means "unchanged" — verify by reloading config through the service
}

func TestConfigUpdatesRestarts(t *testing.T) {
	h, cookie, csrf, applyCh := newTestServerWithApply(t)
	form := url.Values{"csrf": {csrf}, "update.channel": {"prerelease"}, "update.checks": {"on"}}
	rec := postForm(t, h, "/config/updates", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", rec.Code)
	}
	awaitApply(t, applyCh) // checker snapshots config at construction: restart required
}

func TestConfigAdminNoRestart(t *testing.T) {
	h, cookie, csrf, applyCh := newTestServerWithApply(t)
	form := url.Values{"csrf": {csrf}, "web.password": {"newpassword1"}, "web.password.confirm": {"newpassword1"}}
	rec := postForm(t, h, "/config/admin", form, cookie)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("want 303, got %d", rec.Code)
	}
	select {
	case <-applyCh:
		t.Fatal("admin save must NOT restart the board (VerifyLogin reads from disk)")
	case <-time.After(2 * applyDelay):
	}
	// old session still valid; new password verifies
}

func TestOldMonolithConfigPostGone(t *testing.T) {
	h, cookie, csrf, _ := newTestServerWithApply(t)
	rec := postForm(t, h, "/config", url.Values{"csrf": {csrf}}, cookie)
	if rec.Code == http.StatusOK {
		t.Fatalf("POST /config (HTML monolith) should be gone; got 200")
	}
}
```

- [ ] **Step 2: Run to verify failure**, then implement the three handler pairs following Task 6's exact pattern:
  - **Network**: fields `wifi.ssid` (text, maxlength 32), `wifi.psk` (password, placeholder "unchanged"), `darwin.token` (password, placeholder "unchanged", hint links `https://realtime.nationalrail.co.uk/OpenLDBWSRegistration/` "Register for a free Darwin token"). POST → `ConfigUpdate{Cfg: cfg, NewWifiPSK: r.PostFormValue("wifi.psk"), NewToken: r.PostFormValue("darwin.token")}` → `scheduleApply()` → 303 `/restarting`. Consequence copy: "Restarts the board. If the WiFi details are wrong it falls back to its setup hotspot."
  - **Updates**: `update.channel` select (stable/prerelease), `update.autoApply` + `update.checks` checkboxes (remember `checks` inverts into `DisableChecks`, `handlers_config.go:155`) → `scheduleApply()` → 303. Consequence: "Restarts the board — departures pause ~15 seconds."
  - **Admin**: `web.password` + `web.password.confirm` (autocomplete="new-password"), match-check → `ConfigUpdate{Cfg: cfg, NewPassword: pw}` → **no scheduleApply** → 303 `/config`. Savebar consequence: `<span class="consequence ok">Applies immediately — no restart.</span>`.

- [ ] **Step 3: Delete the monolith**: remove `POST /config` HTML route, `handleConfigPost`, `parseConfigForm`, `configPageData`, `templates/config.html`, `configTemplate` var + render case. Keep every still-used helper (`parseIntField` etc. moved next to their new callers). Migrate `e2e_test.go`'s config step to `/config/departures` + `/config/admin` posts covering the same journey.

- [ ] **Step 4: Gate + commit**

Run: `make check`
Expected: PASS

```bash
git add -A
git commit -m "feat(web): network/updates/admin config sections; retire monolith form (#5)"
```

---

### Task 8: Actions page + wait interstitials

**Files:**
- Create: `internal/web/templates/restarting.html`, `internal/web/static/reconnect.js`
- Modify: `internal/web/templates/actions.html` (rewrite), `internal/web/templates/rebooting.html` (rewrite), delete `internal/web/templates/applied.html`
- Modify: `internal/web/handlers_actions.go`, `internal/web/handlers_config.go` (flip Task 6/7 redirects to `/restarting`), `internal/web/templates.go`, `internal/web/server.go`
- Test: `internal/web/handlers_actions_test.go`

**Interfaces:**
- Consumes: action endpoints + `scheduleApply` (`handlers_actions.go`), `actionsPageData` (`:10`), soak keys `1h/4h/8h` (`service.go:340`).
- Produces: `GET /restarting` (public info page, auth-exempt like static: it must render while the server is coming back), `reconnect.js` contract: `<body data-reconnect-delay="MS">` → after delay, poll `/` HEAD every 2s, redirect on success. Old "applied" render name replaced by "restarting" everywhere (`grep -rn '"applied"' internal/web/`).

- [ ] **Step 1: `internal/web/static/reconnect.js`**

```js
// Wait-page reconnect: after an initial delay, poll until the server answers,
// then navigate home. Keeps written instructions visible the whole time.
(function () {
  "use strict";
  var delay = parseInt(document.body.dataset.reconnectDelay || "5000", 10);
  function poll() {
    fetch("/", { method: "HEAD", cache: "no-store" })
      .then(function (r) { if (r.ok || r.status === 401 || r.status === 302) { window.location.href = "/"; } })
      .catch(function () { /* still down; keep polling */ })
      .finally(function () { setTimeout(poll, 2000); });
  }
  setTimeout(poll, delay);
})();
```

- [ ] **Step 2: `templates/restarting.html`** (replaces applied.html):

```html
{{define "title"}}Restarting — Trainboard{{end}}
{{define "bodyattrs"}} data-reconnect-delay="5000"{{end}}
{{define "content"}}
<h2>Restarting the board</h2>
<p>Departures pause for about <strong>15 seconds</strong> while it comes back with your changes.</p>
<div class="notice calm">This page returns to Status by itself. If it doesn't within a minute, reload — and if the board stays dark, check <a href="/actions">Actions</a> once it's back.</div>
<script src="/static/reconnect.js" defer></script>
{{end}}
```

`templates/rebooting.html` — same shape, `data-reconnect-delay="30000"`, copy: "Full power cycle — the board is dark for **about a minute**." Add `{{block "bodyattrs" .}}{{end}}` inside layout.html's `<body class="...">` tag (one-line layout change: `<body class="{{block "bodyclass" .}}{{end}}"{{block "bodyattrs" .}}{{end}}>`).

- [ ] **Step 3: Rewrite `templates/actions.html`** — consequence-first rows, `<details>` inline confirm (no JS):

```html
{{define "title"}}Actions — Trainboard{{end}}
{{define "content"}}
<h2>Actions</h2>
{{if .SoakError}}<p class="error">{{.SoakError}}</p>{{end}}
<div class="act">
  <div class="body"><div class="t">Restart board software</div>
    <div class="d">Departures pause for ~15 seconds. Settings are kept.</div></div>
  <form method="post" action="/actions/restart"><input type="hidden" name="csrf" value="{{.CSRF}}">
    <button class="btn ghost" type="submit">Restart</button></form>
</div>
<details class="act-confirm">
  <summary class="act" style="cursor:pointer">
    <span class="body"><span class="t">Reboot device</span>
      <span class="d" style="display:block">Full power cycle — the board is dark for about a minute.</span></span>
    <span class="btn danger" role="button">Reboot…</span>
  </summary>
  <div class="notice"><strong>Confirm reboot?</strong> The board goes dark for about a minute and this page reconnects by itself.
    <form method="post" action="/actions/reboot" class="btnrow" style="margin-top:.6rem">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button class="btn" type="submit">Reboot now</button>
    </form>
  </div>
</details>
{{if .HotspotActive}}
<div class="act">
  <div class="body"><div class="t">Retry WiFi</div>
    <div class="d">The board is in hotspot mode. Retrying drops this hotspot for ~20 seconds while it tries your network again.</div></div>
  <form method="post" action="/actions/wifi-retry"><input type="hidden" name="csrf" value="{{.CSRF}}">
    <button class="btn ghost" type="submit">Retry now</button></form>
</div>
{{end}}
{{if .SoakRemaining}}
<div class="act">
  <div class="body"><div class="t">Burn-in soak — running</div>
    <div class="d">{{.SoakRemaining}} remaining. The panel cycles white/black; departures resume when it ends.</div></div>
  <form method="post" action="/actions/soak/cancel"><input type="hidden" name="csrf" value="{{.CSRF}}">
    <button class="btn ghost" type="submit">Cancel</button></form>
</div>
{{else}}
<details class="act-confirm">
  <summary class="act" style="cursor:pointer">
    <span class="body"><span class="t">Burn-in soak</span>
      <span class="d" style="display:block">Cycles the panel white/black to even out wear. Departures resume after.</span></span>
    <span class="btn ghost" role="button">Start…</span>
  </summary>
  <div class="notice calm">
    <form method="post" action="/actions/soak">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <label class="f">Duration
        <select name="duration"><option value="1h">1 hour</option><option value="4h">4 hours</option><option value="8h" selected>8 hours</option></select>
      </label>
      <button class="btn" type="submit">Start soak</button>
    </form>
  </div>
</details>
{{end}}
{{end}}
```

Add CSS for `details.act-confirm > summary { list-style: none; } details.act-confirm > summary::-webkit-details-marker { display: none; }` to style.css.

- [ ] **Step 4: Handler changes.** Register `restartingTemplate`; `render` case `"restarting"`; route `GET /restarting` with **no** auth middleware (page must render for a just-restarted, session-lost browser). Change `handleActionsRestart` + `handleUpdateApply` + LAN-setup post-apply renders from `"applied"` to a 303 redirect to `/restarting`; flip Task 6/7 config redirects from `/config` to `/restarting`; `handleActionsReboot` renders `"rebooting"` as today. Delete `applied.html` + var + case. Update every test asserting "applied" (`grep -rn applied internal/web/*_test.go`).

- [ ] **Step 5: Tests** — assert: `GET /restarting` 200 without a session; restart action 303 → `/restarting`; actions page contains the four `.act` blocks and no `onsubmit="return confirm`.

Run: `make check`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat(web): consequence-first actions + reconnecting wait pages (#5)"
```

---

### Task 9: Setup flow redesign

**Files:**
- Modify: `internal/web/templates/setup.html`, `internal/web/templates/setup_done.html`, `internal/web/templates/setup_wifi_done.html`, `internal/web/templates/setup_wifi_status.html`
- Modify: `internal/web/server.go` (setup page data gains route-line state)
- Test: `internal/web/handlers_setup_ap_test.go`, `internal/web/server_test.go`

**Interfaces:**
- Consumes: `handleSetupGet/Post/PostAPMode` (`server.go:311,367,408`), `setupPageData` (`:34`), `scheduleWifiRetry` (`handlers_actions.go:53`), `apSetupURL` (`handlers_portal.go:14`), route-line CSS (Task 1), `/api/station` hint pattern (Task 6, verbatim).
- Produces: no route changes; templates only + page-data fields `Steps []setupStep` where `type setupStep struct { Label string; State string }` (State: "done"|"now"|"").

- [ ] **Step 1: Add the step model** to `server.go` next to `setupPageData`:

```go
// setupStep is one stop on the setup route-line. State is "done", "now" or ""
// (upcoming). The line reads left→right like a line-of-route diagram.
type setupStep struct {
	Label string
	State string
}

func apSetupSteps(current int) []setupStep { // 0=WiFi form, 1=joining
	steps := []setupStep{{Label: "Hotspot joined"}, {Label: "WiFi + password"}, {Label: "Configure station"}, {Label: "Departures live"}}
	steps[0].State = "done"
	for i := range steps {
		if i-1 < current && i != 0 {
			steps[i].State = "done"
		}
	}
	steps[current+1].State = "now"
	return steps
}

func lanSetupSteps(current int) []setupStep { // 0=password+station form
	steps := []setupStep{{Label: "Connected"}, {Label: "Password + station"}, {Label: "Departures live"}}
	steps[0].State = "done"
	steps[current+1].State = "now"
	return steps
}
```

Add `Steps []setupStep` to `setupPageData`, `setupWifiDonePageData`, `setupWifiStatusPageData` and populate at each render site (`handleSetupGet`, `handleSetupPost`, `handleSetupPostAPMode`). Add a shared template partial in `layout.html`:

```html
{{define "routeline"}}{{if .}}<div class="route">{{range .}}<div class="stop{{if .State}} {{.State}}{{end}}">{{.Label}}</div>{{end}}</div>{{end}}{{end}}
```

- [ ] **Step 2: Rewrite `templates/setup.html`** — both variants; AP-mode form first:

```html
{{define "title"}}Set up — Trainboard{{end}}
{{define "content"}}
{{template "routeline" .Steps}}
{{if .APMode}}
<h3>Connect to your WiFi</h3>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
{{if .LastError}}<div class="notice fault"><strong>The last attempt failed:</strong> {{.LastError}}. Check the details and try again.</div>{{end}}
<form method="post" action="/setup">
  <label class="f">WiFi network name
    <input type="text" name="ssid" required maxlength="32" autocomplete="off">
  </label>
  <label class="f">WiFi password
    <input type="password" name="psk" required minlength="8" maxlength="63">
  </label>
  <label class="f">Choose an admin password
    <input type="password" name="password" required minlength="8" autocomplete="new-password">
    <div class="hint">At least 8 characters — you'll use it to sign in to this page from your network</div>
  </label>
  <label class="f">Confirm admin password
    <input type="password" name="confirm" required autocomplete="new-password">
  </label>
  <button class="btn" type="submit" style="width:100%">Join WiFi</button>
  <div class="notice calm" style="margin-top:.8rem">This hotspot switches off for about <strong>20 seconds</strong> while the board tries your network. Stay on this page — your phone should rejoin automatically. If the board can't connect, the hotspot comes back and this page shows what went wrong.</div>
</form>
{{else}}
<h3>Finish setting up</h3>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post" action="/setup">
  <label class="f">Choose an admin password
    <input type="password" name="password" required minlength="8" autocomplete="new-password">
    <div class="hint">At least 8 characters</div>
  </label>
  <label class="f">Confirm admin password
    <input type="password" name="confirm" required autocomplete="new-password">
  </label>
  <label class="f">Station
    <input class="crs" type="text" name="origin" required maxlength="3" pattern="[A-Za-z]{3}"
           hx-get="/api/station" hx-trigger="keyup changed delay:400ms" hx-swap="none"
           hx-on::after-request="document.getElementById('origin-name').textContent = event.detail.successful ? JSON.parse(event.detail.xhr.responseText).name : 'unknown station code'">
    <div class="hint">3-letter station code, e.g. PAD · <span id="origin-name"></span></div>
  </label>
  <label class="f">Darwin API token
    <input type="password" name="token" required autocomplete="off">
    <div class="hint">From your National Rail OpenLDBWS registration email — <a href="https://realtime.nationalrail.co.uk/OpenLDBWSRegistration/" target="_blank" rel="noopener">register free here</a></div>
  </label>
  <button class="btn" type="submit" style="width:100%">Save and start the board</button>
  <div class="notice calm" style="margin-top:.8rem">The board restarts with your settings — departures appear within about a minute.</div>
</form>
{{end}}
{{end}}
```

- [ ] **Step 3: Rewrite the wait/result pages.**

`templates/setup_wifi_done.html` (the AP joining wait — server is about to vanish, so it's static instructions, no reconnect polling):

```html
{{define "title"}}Joining your WiFi — Trainboard{{end}}
{{define "content"}}
{{template "routeline" .Steps}}
<h3>Joining {{.SSID}}</h3>
<p>The hotspot is off while the board connects. This usually takes <strong>about 20 seconds</strong>.</p>
<div class="rows">
  <div class="r"><span class="k">If your WiFi worked</span><span class="v">visit <strong>http://trainboard.local</strong> and sign in</span></div>
  <div class="r"><span class="k">If this page goes stale</span><span class="v">rejoin the <strong>trainboard</strong> hotspot</span></div>
  <div class="r"><span class="k">If it failed</span><span class="v">the hotspot returns and shows the error</span></div>
</div>
{{end}}
```
(Add `SSID string` to `setupWifiDonePageData` if not present — `server.go:64`.)

`templates/setup_done.html` (LAN variant, server restarting): route-line with "Departures live" as `now` + the reconnect pattern from Task 8 (`data-reconnect-delay="10000"`, script include, copy "The board is starting with your settings — this page moves to Status when it's back, usually under a minute.").

`templates/setup_wifi_status.html` (AP fallback after a failed join): route-line with "WiFi + password" as `now`, a `notice fault` showing `{{.LastError}}` and the configured `{{.SSID}}`, and directions: sign in at `http://192.168.4.1/login` then use Actions → Retry WiFi, or resubmit this form with corrected details (link `/setup`).

- [ ] **Step 4: Update setup tests** asserting old copy/markup (`handlers_setup_ap_test.go`, `server_test.go` step assertions like the old giant button text), run gate.

Run: `make check`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): route-line setup flow with honest wait states (#5)"
```

---

### Task 10: Login page

**Files:**
- Modify: `internal/web/templates/login.html`, `internal/web/server.go` (`handleLoginGet/Post` page data)
- Test: `internal/web/server_test.go`

**Interfaces:**
- Consumes: `handleLoginPost` (`server.go:431`), authLimit 5/min (`server.go:81`), rateLimit 429 behavior (`middleware.go:149`).
- Produces: login page data `{ basePage; Error string; RateLimited bool }`.

- [ ] **Step 1: Rewrite `templates/login.html`**

```html
{{define "title"}}Sign in — Trainboard{{end}}
{{define "content"}}
<h2>Sign in</h2>
{{if .RateLimited}}
<div class="notice fault"><strong>Too many attempts.</strong> Sign-in is paused for a minute — try again shortly.</div>
{{else if .Error}}
<p class="error">{{.Error}}</p>
{{end}}
<form method="post" action="/login">
  <label class="f">Admin password
    <input type="password" name="password" required autocomplete="current-password" autofocus>
  </label>
  <button class="btn" type="submit">Sign in</button>
</form>
<p class="caption" style="margin-top:1rem">This is the admin password you chose during setup. Forgotten it? See the recovery notes in the project README.</p>
{{end}}
```

- [ ] **Step 2: Wire page data.** `handleLoginGet`/`handleLoginPost` render with `Error: "That password didn't match — try again."` on verify failure (200 re-render, as today). For the 429 path: the rateLimit middleware currently writes a bare 429 before the handler runs — that's acceptable (the plain 429 page is transient); only if the existing tests assert HTML on 429 should you render the template. Do not weaken the limiter.

- [ ] **Step 3: Update login tests asserting old markup; add one asserting the new error copy on bad password. Run gate.**

Run: `make check`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/web
git commit -m "feat(web): Wayfinding login page (#5)"
```

---

### Task 11: Legacy cleanup + accessibility & weight audit

**Files:**
- Modify: `internal/web/static/style.css` (delete the LEGACY block), all templates (remove any remaining `legacy-page` bodyclass defines)
- Modify: `docs/deploy.md` (note preview.png removal + fonts), `docs/design/2026-07-09-web-ui-wayfinding-brief.md` (mark shipped)
- Test: full suite + manual checks below

**Interfaces:**
- Consumes: everything; this is the closing gate.

- [ ] **Step 1: Delete the legacy CSS block** (marked `LEGACY` in style.css) and every remaining `{{define "bodyclass"}}legacy-page{{end}}`:

```bash
grep -rn "legacy-page" internal/web/ && echo "STILL PRESENT — remove" || echo clean
```

- [ ] **Step 2: Contrast + a11y verification (manual, record results in the commit message):**
  - `#57646f` on `#fff` = 5.9:1 ✓ and `#405264` on `#fff` = 8.2:1 ✓ (labels); `#fff` on `#002f63` = 12.6:1 ✓; `#942514` on `#fbe9e5` ≥ 4.5:1 — verify with a checker; `#17222e` on `#ffd41f` = 10.9:1 ✓.
  - Keyboard-walk every page: every interactive element shows the yellow focus ring.
  - `prefers-reduced-motion: reduce` (macOS: System Settings → Accessibility → Display → Reduce motion) stops the marquee.
  - Every state dot is adjacent to words naming the state (no color-only signalling).

- [ ] **Step 3: Page-weight audit**

```bash
go build ./... && go vet ./...
for f in internal/web/static/style.css internal/web/static/board.js internal/web/static/reconnect.js internal/web/static/htmx.min.js internal/web/static/fonts/*.woff2; do wc -c "$f"; done
```
Expected: total static payload ≤ ~120KB (htmx 51KB + fonts ~2×15KB + css ~12KB + js ~4KB). If a font exceeds 30KB the subset failed — redo Task 1 Step 2.

- [ ] **Step 4: Docs.** In `docs/deploy.md`: note that `/preview.png` is gone (any personal bookmarks/scripts should use `/api/board`), and that the UI serves bundled fonts. In the design brief, add a final line "Shipped in feat/web-ui-wayfinding — <date>."

- [ ] **Step 5: Full gate + commit**

Run: `make check`
Expected: PASS

```bash
git add -A
git commit -m "chore(web): drop legacy amber CSS; a11y + weight audit notes (#5)"
```

---

### Task 12: On-device verification (attended, with Jess)

Not a code task — the acceptance checklist for real hardware before merge:

- [ ] Deploy the branch build to the Pi (slot install or `make deploy` per docs/deploy.md).
- [ ] Phone on LAN: status page — live board renders, marquee scrolls, clock ticks, staleness caption sane.
- [ ] Change station THA→PAD via Configuration → Departures: hint resolves, save → `/restarting` → auto-return, panel shows Paddington departures.
- [ ] Admin password change: applies with no restart; old session stays valid; re-login with the new password works.
- [ ] Actions: restart (reconnect works), soak start/cancel via inline confirm.
- [ ] AP-mode drill (pull WiFi credentials or use a test SSID): captive portal → new setup flow → joining page instructions accurate → failure path returns hotspot with the error shown.
- [ ] OTA smoke: install a real release through the new notice UI.
- [ ] Captive-portal webviews: iPhone CNA renders acceptably (fonts may not load in CNA — page must remain usable on the Helvetica fallback).

---

## Self-Review Notes

- **Spec coverage:** brief §5 settings-list ✓ (T6-7); §7 /api/board + preview deletion ✓ (T3-4); §6 states — status (T5), setup (T9), config validation (T6), actions/in-flight (T8), login (T10), interstitials (T8); §8 fonts ✓ (T1, decision: ship), CRS table ✓ (T2); §4 weight budget ✓ (T11). Route-line ✓ (T9). No-color-only + reduced-motion ✓ (T1/T5/T11).
- **Known judgment calls encoded:** Admin save = no restart (VerifyLogin reads disk, `service.go:322`); Updates save = restart (Checker snapshots cfg, `checker.go:69`); `/api/station` public + setupGate-exempt; `/restarting` auth-exempt; AP joining page static (server vanishes — no reconnect polling), LAN interstitials poll.
- **Helper-signature caveat:** test snippets reference `newTestServer`/`newTestServerWithApply`/`newTestServerWithSources`/`postForm`/`awaitApply` — implementers MUST match the real signatures in `server_test.go:25-108`, `handlers_config_test.go:136-169`, `handlers_status_test.go:28` rather than invent new helpers.
- **`ConfigRedacted` + secrets presence:** T6's `summarizeNetwork` assumes token presence is detectable post-redaction — verify how `ConfigRedacted` redacts (`service.go:142`); if it blanks the token entirely, add `Service.HasDarwinToken() bool` reading via `config.LoadRaw`.
