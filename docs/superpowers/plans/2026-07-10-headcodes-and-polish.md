# Headcodes + Polish Round Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the web preview clock's seconds offset, add an optional RTT-enriched headcode column to the departure board, skip the config index page on desktop, and give the update buttons proper feedback.

**Architecture:** Headcodes come from a new RealTimeTrains JSON client in `internal/data`, applied by a `HeadcodeEnricher` decorator around the existing Darwin fetcher (RTT failure is non-fatal). The panel row gains a conditional headcode column at the reserved geometry (`ColHeadcodeX=45/W=27`, platform +27, destination at 91), mirrored by `board.js`. Everything else is small web-layer work: template/JS changes plus one redirect-target change.

**Tech Stack:** Go 1.x (stdlib only — no new dependencies), html/template, vanilla JS. Spec: `docs/superpowers/specs/2026-07-10-headcodes-and-polish-design.md`.

## Global Constraints

- `layout.headcodes` defaults to **false**; zero value must be the default (migration-free, like `UpdateConfig.DisableChecks`)
- RTT password is a secret: write-only in all UIs/APIs (blank = keep stored), masked by `Redacted()` and `String()`/`GoString()` — same discipline as `darwin.token`
- RTT failures must never take the board down: log + blank headcodes, never an error state
- Column order with headcodes ON: `order | sched | headcode | platform | destination | status` (reference parity)
- With headcodes OFF (default) panel output must be pixel-identical to today (golden tests prove it)
- No new Go module dependencies; no new JS libraries
- All commits go on a feature branch `feat/headcodes-and-polish` off `main`
- Gate before push: `make check` (vet + lint + test) green

## Dev-run recipe (used by visual tasks 8, 9, 11)

```bash
S=/private/tmp/claude-501/-Users-mintopia-Projects-trainboard/9ea79a14-df27-4410-8c40-cc7ff9cef06d/scratchpad/devrun
mkdir -p $S/preview $S/slots
go run ./cmd/trainboard -config $S/config.json -fixture $S/fixture.json \
  -preview-dir $S/preview -http 127.0.0.1:8899 -mdns=false \
  -slots $S/slots -update-state $S/update-state.json
```

`config.json` needs an argon2id `web.passwordHash` — generate with a throwaway `cmd/` helper calling `internal/web.HashPassword("password123")`, then delete the helper. `fixture.json` uses `data.Board` **field names** (no json tags on that struct). Screenshots: `playwright-core` npm package with the cached `chromium_headless_shell` in `~/Library/Caches/ms-playwright` (recipe proven in the M7/desktop rounds).

---

### Task 1: Config schema — `layout.headcodes` + `rtt.*` credentials

**Files:**
- Modify: `internal/config/config.go` (LayoutConfig, Config, Default)
- Modify: `internal/config/redact.go` (mask rtt.password)
- Modify: `internal/web/service.go:178-230` (ConfigUpdate + UpdateConfig)
- Modify: `internal/web/handlers_api.go:168-192` (configUpdateJSON)
- Test: `internal/config/redact_test.go`, `internal/web/service_test.go`

**Interfaces:**
- Produces: `config.RTTConfig{Username, Password string}` at `Config.RTT` (json `rtt`); `LayoutConfig.Headcodes bool` (json `headcodes`); `web.ConfigUpdate.NewRTTPassword string`; UpdateConfig persists `Cfg.RTT.Username` and applies non-empty `NewRTTPassword`.

- [ ] **Step 1: Write the failing tests**

In `internal/config/redact_test.go` (append, matching the file's existing style):

```go
func TestRedactedMasksRTTPassword(t *testing.T) {
	c := Default()
	c.RTT = RTTConfig{Username: "jess", Password: "hunter22"}
	r := c.Redacted()
	if r.RTT.Password != "***REDACTED***" {
		t.Fatalf("rtt.password = %q, want masked", r.RTT.Password)
	}
	if r.RTT.Username != "jess" {
		t.Fatalf("rtt.username = %q, want passthrough (not a secret)", r.RTT.Username)
	}
	c.RTT.Password = ""
	if c.Redacted().RTT.Password != "" {
		t.Fatal("empty rtt.password must stay empty")
	}
}

func TestRTTConfigStringMasksPassword(t *testing.T) {
	r := RTTConfig{Username: "jess", Password: "hunter22"}
	for _, s := range []string{fmt.Sprintf("%v", r), fmt.Sprintf("%s", r), fmt.Sprintf("%#v", r)} {
		if strings.Contains(s, "hunter22") {
			t.Fatalf("RTTConfig leaked password: %s", s)
		}
	}
}
```

(Add `"fmt"`/`"strings"` imports if the file lacks them.)

In `internal/web/service_test.go` (append; follow the file's existing `newTestService`-style harness — reuse whatever helper existing UpdateConfig tests use to build a Service over a temp config file):

```go
func TestUpdateConfigPersistsRTT(t *testing.T) {
	svc, cfgPath := newServiceWithValidConfig(t) // reuse/adapt the file's existing helper

	cfg, err := svc.ConfigRedacted()
	if err != nil {
		t.Fatal(err)
	}
	cfg.RTT.Username = "jess"
	cfg.Layout.Headcodes = true
	if err := svc.UpdateConfig(ConfigUpdate{Cfg: cfg, NewRTTPassword: "hunter22"}); err != nil {
		t.Fatal(err)
	}

	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.RTT.Username != "jess" || onDisk.RTT.Password != "hunter22" || !onDisk.Layout.Headcodes {
		t.Fatalf("stored = %+v", onDisk.RTT)
	}

	// Write-only round trip: a redacted re-save with blank NewRTTPassword
	// must keep the stored secret.
	cfg2, _ := svc.ConfigRedacted()
	if cfg2.RTT.Password != "***REDACTED***" {
		t.Fatalf("redacted rtt.password = %q", cfg2.RTT.Password)
	}
	if err := svc.UpdateConfig(ConfigUpdate{Cfg: cfg2}); err != nil {
		t.Fatal(err)
	}
	onDisk2, _ := config.Load(cfgPath)
	if onDisk2.RTT.Password != "hunter22" {
		t.Fatalf("blank NewRTTPassword clobbered the stored secret: %q", onDisk2.RTT.Password)
	}
}
```

**Critical:** `UpdateConfig` must copy `next.RTT.Username = u.Cfg.RTT.Username` but must NOT copy `u.Cfg.RTT.Password` (it's `***REDACTED***` after a round trip) — mirror the Darwin token pattern exactly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ ./internal/web/ -run 'RTT' -v`
Expected: compile errors (`RTTConfig` undefined, `NewRTTPassword` unknown field).

- [ ] **Step 3: Implement**

`internal/config/config.go` — add to `Config` (after `Darwin`):

```go
	RTT         RTTConfig         `json:"rtt"`
```

Add after `DarwinConfig`:

```go
// RTTConfig holds RealTime Trains API credentials (password is secret).
// Both empty (the default) disables headcode enrichment; a missing "rtt"
// key in configs predating this section unmarshals to exactly that.
type RTTConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
```

Extend `LayoutConfig`:

```go
// LayoutConfig holds display layout toggles.
type LayoutConfig struct {
	Times bool `json:"times"` // show calling-point times
	// Headcodes shows the train headcode column (reference layout parity).
	// Off by default — the zero value keeps configs predating this field
	// unchanged, and the column needs RTT credentials to have data anyway.
	Headcodes bool `json:"headcodes"`
}
```

`Default()` needs no change (zero value is the default). Validate needs no change (both fields optional).

`internal/config/redact.go` — in `Redacted()` add:

```go
	if c.RTT.Password != "" {
		c.RTT.Password = redacted
	}
```

And add the leak guards (mirroring `DarwinConfig`):

```go
// String masks the password so RTTConfig can't leak it via %s/%v; the
// username is not a secret and passes through.
func (r RTTConfig) String() string {
	if r.Password == "" {
		return fmt.Sprintf("RTTConfig{username:%q password:unset}", r.Username)
	}
	return fmt.Sprintf("RTTConfig{username:%q password:%s}", r.Username, redacted)
}

// GoString masks the password so %#v can't leak it.
func (r RTTConfig) GoString() string { return r.String() }
```

`internal/web/service.go` — `ConfigUpdate` gains:

```go
	NewRTTPassword string
```

`UpdateConfig`, next to the existing merges (after `next.Update = u.Cfg.Update`):

```go
	next.RTT.Username = u.Cfg.RTT.Username
```

and next to the `NewToken` block:

```go
	if u.NewRTTPassword != "" {
		next.RTT.Password = u.NewRTTPassword
	}
```

`internal/web/handlers_api.go` — `configUpdateJSON` gains:

```go
	NewRTTPassword string `json:"newRttPassword"`
```

and `handleAPIConfigPut`'s `ConfigUpdate` literal gains `NewRTTPassword: body.NewRTTPassword,`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ ./internal/web/ -v -run 'RTT|Redact'`
Expected: PASS. Also run `go test ./internal/config/ ./internal/web/` (full packages) — no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/config internal/web
git commit -m "feat(config): layout.headcodes toggle + write-only RTT credentials"
```

---

### Task 2: RTT lineup client

**Files:**
- Create: `internal/data/rtt.go`
- Create: `internal/data/testdata/rtt_search.json`
- Test: `internal/data/rtt_test.go`

**Interfaces:**
- Consumes: `httpDoer` (existing seam in `internal/data/client.go:17`), test helpers `doerFunc`/`resp` from `client_test.go`.
- Produces: `data.RTTService{Headcode, BookedDeparture, DestinationName string}`; `data.NewRTTClient(user, pass string) *RTTClient`; `(*RTTClient).Lineup(ctx context.Context, crs string) ([]RTTService, error)`.

- [ ] **Step 1: Write the fixture**

`internal/data/testdata/rtt_search.json`:

```json
{
  "location": {"name": "Twyford", "crs": "TWY", "tiploc": "TWYFORD"},
  "filter": null,
  "services": [
    {
      "locationDetail": {
        "realtimeActivated": true,
        "tiploc": "TWYFORD",
        "crs": "TWY",
        "description": "Twyford",
        "gbttBookedDeparture": "1913",
        "origin": [{"tiploc": "RDNGSTN", "description": "Reading", "workingTime": "190500", "publicTime": "1905"}],
        "destination": [{"tiploc": "PADTON", "description": "London Paddington", "workingTime": "203700", "publicTime": "2037"}],
        "isCall": true,
        "isPublicCall": true,
        "platform": "10"
      },
      "serviceUid": "P70091",
      "runDate": "2026-07-10",
      "trainIdentity": "1A23",
      "runningIdentity": "1A23",
      "atocCode": "GW",
      "atocName": "Great Western Railway",
      "serviceType": "train",
      "isPassenger": true
    },
    {
      "locationDetail": {
        "gbttBookedDeparture": "1919",
        "destination": [{"tiploc": "PADTON", "description": "London Paddington", "publicTime": "2009"}]
      },
      "serviceUid": "P70113",
      "runDate": "2026-07-10",
      "trainIdentity": "2C31",
      "atocCode": "GW",
      "serviceType": "train",
      "isPassenger": true
    }
  ]
}
```

- [ ] **Step 2: Write the failing tests**

`internal/data/rtt_test.go`:

```go
package data

import (
	"context"
	"net/http"
	"testing"
)

func TestRTTLineupParsesServices(t *testing.T) {
	body := string(readFixture(t, "rtt_search.json"))
	var gotURL, gotAuth string
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		return resp(200, body), nil
	})}
	svcs, err := c.Lineup(context.Background(), "TWY")
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.rtt.io/api/v1/json/search/TWY" {
		t.Fatalf("url = %q", gotURL)
	}
	if gotAuth == "" {
		t.Fatal("no basic-auth header sent")
	}
	want := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "2C31", BookedDeparture: "1919", DestinationName: "London Paddington"},
	}
	if len(svcs) != len(want) {
		t.Fatalf("got %d services, want %d: %+v", len(svcs), len(want), svcs)
	}
	for i := range want {
		if svcs[i] != want[i] {
			t.Errorf("service[%d] = %+v, want %+v", i, svcs[i], want[i])
		}
	}
}

func TestRTTLineupErrors(t *testing.T) {
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(401, `{"error":"auth"}`), nil
	})}
	if _, err := c.Lineup(context.Background(), "TWY"); err == nil {
		t.Fatal("expected non-200 to error")
	}
}

func TestRTTLineupEmptyServices(t *testing.T) {
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, `{"location":{"crs":"TWY"},"services":null}`), nil
	})}
	svcs, err := c.Lineup(context.Background(), "TWY")
	if err != nil || len(svcs) != 0 {
		t.Fatalf("want empty lineup, got %v, %v", svcs, err)
	}
}
```

(If `readFixture`/`doerFunc`/`resp` have different signatures than assumed, adapt the test to the existing helpers in `client_test.go`/`parse_test.go` — do not duplicate helpers.)

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/data/ -run RTT -v`
Expected: compile error (`RTTClient` undefined).

- [ ] **Step 4: Implement `internal/data/rtt.go`**

```go
package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// rttBaseURL is the RealTime Trains public API root.
const rttBaseURL = "https://api.rtt.io"

// RTTService is one row of an RTT station lineup: only the fields headcode
// enrichment needs.
type RTTService struct {
	Headcode        string // trainIdentity, e.g. "1A23"
	BookedDeparture string // gbttBookedDeparture, "HHMM"
	DestinationName string // first destination's description
}

// RTTClient talks to the RealTime Trains JSON API with basic auth. It exists
// solely to enrich Darwin departures with headcodes (which public LDBWS does
// not carry) — it is NOT a second board source.
type RTTClient struct {
	user, pass string
	base       string
	http       httpDoer
}

// NewRTTClient returns an RTTClient with a 15s HTTP timeout.
func NewRTTClient(user, pass string) *RTTClient {
	return &RTTClient{user: user, pass: pass, base: rttBaseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

// rttSearch mirrors the /json/search/{crs} response, local names only, just
// the fields Lineup projects.
type rttSearch struct {
	Services []struct {
		TrainIdentity  string `json:"trainIdentity"`
		LocationDetail struct {
			GBTTBookedDeparture string `json:"gbttBookedDeparture"`
			Destination         []struct {
				Description string `json:"description"`
			} `json:"destination"`
		} `json:"locationDetail"`
	} `json:"services"`
}

// Lineup fetches the station's current departure lineup. A "services": null
// response (no trains) is an empty, non-error lineup.
func (c *RTTClient) Lineup(ctx context.Context, crs string) ([]RTTService, error) {
	url := fmt.Sprintf("%s/api/v1/json/search/%s", c.base, crs)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("data: rtt request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("data: reading rtt response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("data: rtt returned HTTP %d", res.StatusCode)
	}
	var sr rttSearch
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("data: decoding rtt lineup: %w", err)
	}
	out := make([]RTTService, 0, len(sr.Services))
	for _, s := range sr.Services {
		svc := RTTService{Headcode: s.TrainIdentity, BookedDeparture: s.LocationDetail.GBTTBookedDeparture}
		if len(s.LocationDetail.Destination) > 0 {
			svc.DestinationName = s.LocationDetail.Destination[0].Description
		}
		out = append(out, svc)
	}
	return out, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/data/ -run RTT -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/data/rtt.go internal/data/rtt_test.go internal/data/testdata/rtt_search.json
git commit -m "feat(data): RealTime Trains lineup client for headcode enrichment"
```

---

### Task 3: `Departure.Headcode` + matcher + enricher

**Files:**
- Modify: `internal/data/model.go:36-52` (Departure)
- Create: `internal/data/enrich.go`
- Test: `internal/data/enrich_test.go`

**Interfaces:**
- Consumes: `RTTService`, `(*RTTClient).Lineup` (Task 2); `Board`/`Departure`/`Request` (existing).
- Produces: `data.Departure.Headcode string`; `data.Fetcher` interface (same signature as `runtime.Fetcher`); `data.HeadcodeEnricher{Base Fetcher; RTT *RTTClient; Log *slog.Logger}` implementing `Fetcher`; `data.MatchHeadcodes(b *Board, lineup []RTTService)`.

- [ ] **Step 1: Write the failing tests**

`internal/data/enrich_test.go`:

```go
package data

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
)

func dep(sched, destName string) Departure {
	return Departure{ScheduledTime: sched, Destination: Location{Name: destName}}
}

func TestMatchHeadcodesUniqueTime(t *testing.T) {
	b := &Board{Departures: []Departure{dep("19:13", "London Paddington")}}
	MatchHeadcodes(b, []RTTService{{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"}})
	if b.Departures[0].Headcode != "1A23" {
		t.Fatalf("headcode = %q", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesTieBrokenByDestination(t *testing.T) {
	b := &Board{Departures: []Departure{dep("19:13", "Didcot Parkway")}}
	lineup := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "2N40", BookedDeparture: "1913", DestinationName: "Didcot Parkway"},
	}
	MatchHeadcodes(b, lineup)
	if b.Departures[0].Headcode != "2N40" {
		t.Fatalf("headcode = %q, want tie broken by destination", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesAmbiguousLeavesBlank(t *testing.T) {
	b := &Board{Departures: []Departure{dep("19:13", "London Paddington")}}
	lineup := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "1A25", BookedDeparture: "1913", DestinationName: "London Paddington"},
	}
	MatchHeadcodes(b, lineup)
	if b.Departures[0].Headcode != "" {
		t.Fatalf("ambiguous match must stay blank, got %q", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesNoMatchLeavesBlank(t *testing.T) {
	b := &Board{Departures: []Departure{dep("19:13", "London Paddington")}}
	MatchHeadcodes(b, []RTTService{{Headcode: "1A23", BookedDeparture: "0700", DestinationName: "London Paddington"}})
	if b.Departures[0].Headcode != "" {
		t.Fatalf("headcode = %q, want blank", b.Departures[0].Headcode)
	}
}

// fetcherFunc adapts a func to Fetcher for tests.
type fetcherFunc func(ctx context.Context, r Request) (*Board, error)

func (f fetcherFunc) Fetch(ctx context.Context, r Request) (*Board, error) { return f(ctx, r) }

func TestEnricherFillsHeadcodes(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return &Board{Departures: []Departure{dep("19:13", "London Paddington")}}, nil
	})
	rtt := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, string(readFixture(t, "rtt_search.json"))), nil
	})}
	e := &HeadcodeEnricher{Base: base, RTT: rtt, Log: slog.Default()}
	b, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Departures[0].Headcode != "1A23" {
		t.Fatalf("headcode = %q", b.Departures[0].Headcode)
	}
}

func TestEnricherRTTFailureIsNonFatal(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return &Board{Departures: []Departure{dep("19:13", "London Paddington")}}, nil
	})
	rtt := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(401, "denied"), nil
	})}
	e := &HeadcodeEnricher{Base: base, RTT: rtt, Log: slog.Default()}
	b, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"})
	if err != nil {
		t.Fatalf("rtt failure must be non-fatal, got %v", err)
	}
	if b.Departures[0].Headcode != "" {
		t.Fatalf("headcode = %q, want blank on rtt failure", b.Departures[0].Headcode)
	}
}

func TestEnricherPropagatesBaseError(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return nil, errors.New("darwin down")
	})
	e := &HeadcodeEnricher{Base: base, RTT: NewRTTClient("u", "p"), Log: slog.Default()}
	if _, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"}); err == nil {
		t.Fatal("base error must propagate")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/data/ -run 'Match|Enricher' -v`
Expected: compile errors (`Headcode` field, `MatchHeadcodes`, `HeadcodeEnricher` undefined).

- [ ] **Step 3: Implement**

`internal/data/model.go` — add to `Departure` after `Platform`:

```go
	Headcode      string // RTT trainIdentity, e.g. "1A23"; "" when enrichment is off or unmatched
```

Create `internal/data/enrich.go`:

```go
package data

import (
	"context"
	"log/slog"
	"strings"
)

// Fetcher is the board-fetch seam (mirrors runtime.Fetcher, redeclared here
// so this package doesn't import runtime).
type Fetcher interface {
	Fetch(ctx context.Context, r Request) (*Board, error)
}

// HeadcodeEnricher decorates a base fetcher, filling Departure.Headcode from
// an RTT station lineup. RTT is strictly best-effort: any lineup failure is
// logged and the Darwin board passes through with blank headcodes — the
// panel must never degrade because RTT is down.
type HeadcodeEnricher struct {
	Base Fetcher
	RTT  *RTTClient
	Log  *slog.Logger
}

// Fetch fetches the Darwin board, then annotates headcodes.
func (e *HeadcodeEnricher) Fetch(ctx context.Context, r Request) (*Board, error) {
	b, err := e.Base.Fetch(ctx, r)
	if err != nil {
		return b, err
	}
	lineup, rerr := e.RTT.Lineup(ctx, r.OriginCRS)
	if rerr != nil {
		e.Log.Warn("rtt lineup failed; headcodes left blank", "err", rerr.Error())
		return b, nil
	}
	MatchHeadcodes(b, lineup)
	return b, nil
}

// MatchHeadcodes fills Headcode for each departure with an unambiguous RTT
// counterpart: same booked departure time (Darwin "HH:MM" vs RTT "HHMM"),
// ties broken by case-insensitive destination name. A departure that stays
// ambiguous keeps a blank headcode — a wrong headcode is worse than none.
func MatchHeadcodes(b *Board, lineup []RTTService) {
	for i := range b.Departures {
		d := &b.Departures[i]
		key := strings.ReplaceAll(d.ScheduledTime, ":", "")
		var cands []RTTService
		for _, s := range lineup {
			if s.BookedDeparture == key {
				cands = append(cands, s)
			}
		}
		if len(cands) > 1 {
			var narrowed []RTTService
			for _, s := range cands {
				if strings.EqualFold(s.DestinationName, d.Destination.Name) {
					narrowed = append(narrowed, s)
				}
			}
			cands = narrowed
		}
		if len(cands) == 1 {
			d.Headcode = cands[0].Headcode
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/data/ -v`
Expected: all PASS (including pre-existing tests).

- [ ] **Step 5: Commit**

```bash
git add internal/data/model.go internal/data/enrich.go internal/data/enrich_test.go
git commit -m "feat(data): headcode matching + non-fatal RTT enrichment decorator"
```

---

### Task 4: Panel row geometry with headcodes + goldens

**Files:**
- Modify: `internal/board/row.go` (rowElements signature + column shift)
- Modify: `internal/board/elements.go:69-71,90-103` (newNextServiceRow / newRemainingServices)
- Modify: `internal/board/scene_departures.go:45-60` (pass layout.Headcodes)
- Test: `internal/board/row_test.go` (+ new goldens in `internal/board/testdata/`)

**Interfaces:**
- Consumes: `data.Departure.Headcode` (Task 3); `config.LayoutConfig.Headcodes` (Task 1).
- Produces: `rowElements(d data.Departure, order, y int, f *Fonts, headcodes bool) []render.Element`; `newNextServiceRow(d data.Departure, f *Fonts, headcodes bool)`; `newRemainingServices(deps []data.Departure, f *Fonts, headcodes bool)`.

- [ ] **Step 1: Write the failing tests**

In `internal/board/row_test.go`, update `renderRow` and add cases:

```go
func renderRow(t *testing.T, d data.Departure, order int) *render.Framebuffer {
	t.Helper()
	return renderRowHC(t, d, order, false)
}

func renderRowHC(t *testing.T, d data.Departure, order int, headcodes bool) *render.Framebuffer {
	t.Helper()
	fb := render.New(W, RowH)
	scene := &render.Scene{Elements: rowElements(d, order, 0, mustFonts(t), headcodes)}
	scene.Render(fb, 0, fixedNow)
	return fb
}

func TestRowGoldenHeadcode(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = "1A23"
	rendertest.AssertGolden(t, "testdata", "row_headcode", renderRowHC(t, d, 1, true))
}

// Headcodes ON but this service unmatched: the column stays a gap and the
// platform/destination still shift — column positions must not depend on
// per-row data.
func TestRowGoldenHeadcodeBlank(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = ""
	rendertest.AssertGolden(t, "testdata", "row_headcode_blank", renderRowHC(t, d, 1, true))
}

// Flag off: pixel-identical to the pre-feature renderer even when the data
// carries a headcode (existing goldens already lock the geometry; this locks
// the "ignores the field" contract).
func TestRowHeadcodeOffIgnoresField(t *testing.T) {
	d := fixtureBoard().Departures[0]
	plain := renderRowHC(t, d, 1, false)
	d.Headcode = "1A23"
	withField := renderRowHC(t, d, 1, false)
	if string(plain.Pix) != string(withField.Pix) {
		t.Fatal("headcodes-off row must ignore the Headcode field")
	}
}

// With headcodes on, the headcode box [45,72) and shifted platform box
// [72,91) must hold the right content: blank headcode leaves [45,72) dark.
func TestRowHeadcodeBlankLeavesGap(t *testing.T) {
	d := fixtureBoard().Departures[0]
	d.Headcode = ""
	fb := renderRowHC(t, d, 1, true)
	for x := ColHeadcodeX; x < ColHeadcodeX+ColHeadcodeW; x++ {
		for y := 0; y < RowH; y++ {
			if fb.At(x, y) != 0 {
				t.Fatalf("headcode box pixel (%d,%d) = %d, want 0", x, y, fb.At(x, y))
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/board/ -run Row -v`
Expected: compile error (rowElements takes 4 args).

- [ ] **Step 3: Implement**

`internal/board/row.go` — replace `rowElements`:

```go
// rowElements builds the departure row at vertical offset y. With headcodes
// on, the optional headcode column (reference layout) sits between the
// scheduled time and the platform, shifting platform and destination right
// by ColHeadcodeW; off, the geometry is the original six-column row.
// Headcode and platform draw only when present; their boxes stay dark gaps
// otherwise, keeping column positions independent of per-row data.
func rowElements(d data.Departure, order, y int, f *Fonts, headcodes bool) []render.Element {
	els := []render.Element{
		&render.StaticText{Font: f.Regular, Text: Ordinal(order), X: ColOrderX, Y: y, W: ColSchedX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: d.ScheduledTime, X: ColSchedX, Y: y, W: ColSchedW, H: RowH, Align: render.AlignCenter, Level: 15},
	}
	platX, destX := ColPlatformX, ColDestX
	if headcodes {
		if d.Headcode != "" {
			els = append(els, &render.StaticText{Font: f.Regular, Text: d.Headcode, X: ColHeadcodeX, Y: y, W: ColHeadcodeW, H: RowH, Align: render.AlignCenter, Level: 15})
		}
		platX += ColHeadcodeW
		destX += ColHeadcodeW
	}
	if d.Platform != "" {
		els = append(els, &render.StaticText{Font: f.Regular, Text: d.Platform, X: platX, Y: y, W: ColPlatformW, H: RowH, Align: render.AlignCenter, Level: 15})
	}
	els = append(els,
		&render.StaticText{Font: f.Regular, Text: d.Destination.Name, X: destX, Y: y, W: ColStatusX - destX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: string(d.Status), X: ColStatusX, Y: y, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15},
	)
	return els
}
```

Update `internal/board/board.go:19` comment: `ColHeadcodeX = 45 // optional column between sched and platform (layout.headcodes)`.

`internal/board/elements.go` — thread the flag:

```go
func newNextServiceRow(d data.Departure, f *Fonts, headcodes bool) render.Element {
	return &nextServiceRow{strip: prerender(rowElements(d, 1, 0, f, headcodes), W, RowH)}
}
```

```go
func newRemainingServices(deps []data.Departure, f *Fonts, headcodes bool) render.Element {
```

with both internal `rowElements(...)` calls gaining the trailing `headcodes` argument.

`internal/board/scene_departures.go` — in `departureBoardScene`:

```go
		newNextServiceRow(first, f, layout.Headcodes),
		...
		newRemainingServices(b.Departures[1:], f, layout.Headcodes),
```

Fix every other caller the compiler reports (element tests etc.) by passing `false`.

- [ ] **Step 4: Generate goldens + run tests**

Golden tests self-record on first run if that's `rendertest.AssertGolden`'s convention — check `internal/render/rendertest` for the update mechanism (an `-update` flag or auto-write-when-missing). Generate `row_headcode.png` / `row_headcode_blank.png` accordingly, then **eyeball both PNGs**: headcode centered in x∈[45,72), platform at x∈[72,91), destination from 91.

Run: `go test ./internal/board/ -v`
Expected: all PASS, including untouched pre-existing goldens (proves off-flag pixel parity).

- [ ] **Step 5: Commit**

```bash
git add internal/board
git commit -m "feat(board): optional headcode column at reference geometry"
```

---

### Task 5: Wire the enricher in main

**Files:**
- Modify: `cmd/trainboard/main.go:146-155`

**Interfaces:**
- Consumes: `data.HeadcodeEnricher`, `data.NewRTTClient` (Tasks 2-3), `cfg.Layout.Headcodes`/`cfg.RTT` (Task 1).

- [ ] **Step 1: Implement**

Replace the fetcher wiring's else-branch:

```go
	} else {
		fetcher = data.NewClient(cfg.Darwin.Token)
		if cfg.Layout.Headcodes && cfg.RTT.Username != "" {
			fetcher = &data.HeadcodeEnricher{Base: fetcher, RTT: data.NewRTTClient(cfg.RTT.Username, cfg.RTT.Password), Log: log}
			log.Info("headcode enrichment enabled", "source", "rtt")
		}
	}
```

(Fixture mode stays unwrapped — it's offline by definition. `runtime.Fetcher` and `data.Fetcher` have identical method sets, so the assignments are legal both ways.)

- [ ] **Step 2: Verify it builds and behaves**

Run: `go build ./... && go vet ./cmd/trainboard/`
Expected: clean. There is no unit seam in main; the covering tests are Tasks 2-4's.

- [ ] **Step 3: Commit**

```bash
git add cmd/trainboard/main.go
git commit -m "feat: wire RTT headcode enrichment behind layout.headcodes + creds"
```

---

### Task 6: Web forms — Display checkbox + Network RTT fields

**Files:**
- Modify: `internal/web/templates/config_display.html:24` (add checkbox)
- Modify: `internal/web/templates/config_network.html:23-26` (add RTT fields)
- Modify: `internal/web/handlers_config.go:282-343` (display) and `:429-468` (network)
- Test: `internal/web/handlers_config_test.go`

**Interfaces:**
- Consumes: config fields + `ConfigUpdate.NewRTTPassword` (Task 1).
- Produces: form fields `layout.headcodes` (checkbox), `rtt.username` (text, pre-filled), `rtt.password` (password, write-only); `configDisplayPageData.RTTSet bool`.

- [ ] **Step 1: Write the failing tests**

In `internal/web/handlers_config_test.go`, following the file's existing POST-handler test harness (reuse its server/cookie/form helpers):

```go
func TestConfigDisplayPostSavesHeadcodes(t *testing.T) {
	// Arrange a logged-in server over a valid temp config (existing helper),
	// POST /config/display with the base display form + "layout.headcodes"="on",
	// expect 303 → /restarting and the stored config's Layout.Headcodes true.
	// Then POST again WITHOUT the key and expect it back to false.
}

func TestConfigNetworkPostSavesRTTCreds(t *testing.T) {
	// POST /config/network with the base network form + rtt.username=jess,
	// rtt.password=hunter22 → 303; stored config has RTT.Username "jess" and
	// RTT.Password "hunter22".
	// Second POST with rtt.username=jess and BLANK rtt.password → stored
	// password still "hunter22" (write-only semantics).
}
```

Write these as real tests against the existing harness — the comments above describe assertions, not placeholders; the mechanics (session cookie, csrf token, form encoding) must copy the adjacent tests in the same file.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'ConfigDisplayPostSavesHeadcodes|ConfigNetworkPostSavesRTT' -v`
Expected: FAIL (fields not parsed, stored config unchanged).

- [ ] **Step 3: Implement**

`config_display.html` — after the `layout.times` checkbox (line 24):

```html
<label class="check"><input type="checkbox" name="layout.headcodes" {{if .Cfg.Layout.Headcodes}}checked{{end}}> Show train headcodes</label>
{{if not .RTTSet}}<div class="hint">Headcodes need RealTime Trains credentials &mdash; set them on the Network page.</div>{{end}}
```

`handlers_config.go` — `configDisplayPageData` gains `RTTSet bool`; `renderConfigDisplay` sets it:

```go
	s.render(w, "configDisplay", configDisplayPageData{
		basePage: s.pageBase(r, "config"),
		Cfg:      cfg,
		RTTSet:   cfg.RTT.Username != "",
		Error:    errMsg,
	})
```

`handleConfigDisplayPost` — next to the `layout.times` line:

```go
	cfg.Layout.Headcodes = formHasKey(r, "layout.headcodes")
```

`config_network.html` — after the Darwin token label (line 26):

```html
<label class="f">RealTime Trains username
  <input type="text" name="rtt.username" value="{{.Cfg.RTT.Username}}" autocomplete="off">
  <div class="hint">Optional &mdash; needed for train headcodes. <a href="https://api.rtt.io/" target="_blank" rel="noopener noreferrer">Register for a free RTT API account</a></div>
</label>
<label class="f">RealTime Trains password
  <input type="password" name="rtt.password" placeholder="unchanged" autocomplete="off">
</label>
```

(`rtt.username` is not a secret — pre-filling it is deliberate and safe under `ConfigRedacted`; the password input must never carry a value attribute, same rule as `darwin.token`.)

`handleConfigNetworkPost` — with the other field parses:

```go
	cfg.RTT.Username = strings.TrimSpace(r.PostFormValue("rtt.username"))
```

and the `ConfigUpdate` literal gains:

```go
		NewRTTPassword: r.PostFormValue("rtt.password"),
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: all PASS including the e2e route table (forms it posts lack the new keys → both parse as absent/blank, which is valid).

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): headcodes toggle on Display, RTT credentials on Network"
```

---

### Task 7: Web preview — headcode in /api/board + board.js column shift

**Files:**
- Modify: `internal/web/handlers_board.go`
- Modify: `internal/web/static/board.js:17-21,118-125,144-145`
- Test: `internal/web/handlers_board_test.go`

**Interfaces:**
- Consumes: `Departure.Headcode` (Task 3), `cfg.Layout.Headcodes` (Task 1).
- Produces: JSON — `serviceView.headcode` (omitempty) per service, top-level `boardView.headcodes` flag; `buildBoardView(snap *board.Snapshot, layout config.LayoutConfig) boardView` (signature change from `times bool`).

- [ ] **Step 1: Write the failing test**

In `internal/web/handlers_board_test.go`, following the file's existing buildBoardView tests:

```go
func TestBoardViewCarriesHeadcodes(t *testing.T) {
	snap := departuresSnapshot(t) // reuse/adapt the file's existing snapshot fixture helper
	snap.Board.Departures[0].Headcode = "1A23"
	v := buildBoardView(snap, config.LayoutConfig{Times: true, Headcodes: true})
	if !v.Headcodes {
		t.Fatal("view must carry the layout flag")
	}
	if v.First.Headcode != "1A23" {
		t.Fatalf("first.headcode = %q", v.First.Headcode)
	}
	off := buildBoardView(snap, config.LayoutConfig{Times: true})
	if off.Headcodes {
		t.Fatal("flag off must not set headcodes")
	}
}
```

Update existing `buildBoardView(snap, true)`-style calls to `buildBoardView(snap, config.LayoutConfig{Times: true})`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run BoardView -v`
Expected: compile errors (signature, fields).

- [ ] **Step 3: Implement Go side**

`handlers_board.go`:
- `serviceView` gains `Headcode string \`json:"headcode,omitempty"\`` (after `Platform`).
- `boardView` gains `Headcodes bool \`json:"headcodes,omitempty"\``.
- `buildBoardView(snap *board.Snapshot, layout config.LayoutConfig)` — replace the `times bool` param; body uses `layout.Times` where `times` was, and sets `v.Headcodes = layout.Headcodes` in the departures case.
- `toServiceView` sets `Headcode: d.Headcode`.
- `handleAPIBoard`:

```go
	var layout config.LayoutConfig
	if cfg, err := s.svc.ConfigRedacted(); err == nil {
		layout = cfg.Layout
	}
	...
	view := buildBoardView(snap, layout)
```

(add the `config` import).

- [ ] **Step 4: Implement board.js side**

Constants block (`board.js:19-21`) gains:

```js
  var COL_HC_X = 45, COL_HC_W = 27;    // board.go ColHeadcodeX/W (layout.headcodes)
```

Add a module-level flag near `var last = "";`:

```js
  var hcOn = false; // layout.headcodes, from the last /api/board payload
```

`render(v)` sets it before building (immediately after `var now = performance.now();`):

```js
    hcOn = !!v.headcodes;
```

`rowInto` mirrors `board.rowElements`' shift:

```js
  // rowInto mirrors board.rowElements: departure row, with the optional
  // headcode column (layout.headcodes) between sched and platform.
  function rowInto(parent, s, y) {
    staticText(parent, s.order + ordSuffix(s.order), COL_ORDER_X, y, COL_SCHED_X, "left");
    staticText(parent, s.scheduled, COL_SCHED_X, y, COL_SCHED_W, "center");
    var shift = hcOn ? COL_HC_W : 0;
    if (hcOn && s.headcode) staticText(parent, s.headcode, COL_HC_X, y, COL_HC_W, "center");
    if (s.platform) staticText(parent, s.platform, COL_PLAT_X + shift, y, COL_PLAT_W, "center");
    staticText(parent, s.destination, COL_DEST_X + shift, y, COL_STATUS_X - COL_DEST_X - shift, "left");
    staticText(parent, s.status, COL_STATUS_X, y, COL_STATUS_W, "right");
  }
```

`nextServiceRow`'s epoch key (`board.js:145`) gains the headcode so a changed headcode restarts that row's slide:

```js
    var text = JSON.stringify([s.order, s.scheduled, s.destination, s.platform, s.headcode, s.status]);
```

- [ ] **Step 5: Run tests + visual check**

Run: `go test ./internal/web/ -v` → PASS.
Dev-run (recipe above) with a fixture whose departures carry `Headcode` values and a config with `"layout":{"times":true,"headcodes":true}`; screenshot `/` and confirm the preview row reads `1st 19:13 1A23 10 London Paddington … Exp 20:08`.

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "feat(web): headcode column in /api/board and the live preview"
```

---

### Task 8: Preview clock — fix the seconds' vertical offset

**Files:**
- Modify: `internal/web/static/board.js:202-214` (clockInto)
- Reference (ground truth, do not modify): `internal/board/testdata/el_clock_at_50.png`

**Interfaces:** none (self-contained JS fix).

- [ ] **Step 1: Establish ground truth**

Open `internal/board/testdata/el_clock_at_50.png` (the panel's golden clock render) and note the exact pixel rows where the HH:MM glyphs and the :SS glyphs start and end. This is the target.

- [ ] **Step 2: Reproduce**

Dev-run + screenshot the status page preview. Confirm the defect: the seconds sit visibly lower relative to HH:MM than in the golden.

**Diagnosis (from the design spec):** the HH:MM span sets `line-height:14px` under a 20px font (negative half-leading raises glyphs ≈3px above `top`); the :SS span inherits the stage's `line-height:12px` under a 10px font (positive half-leading lowers glyphs ≈1px). The panel blits bitmap tops at exactly y=50 and y=55 (`clockSecondsDrop=5`, `element_clock.go:20`); the browser's effective drop is ≈4px larger.

- [ ] **Step 3: Fix**

In `clockInto`, give the seconds span an explicit line-height and correct its top so the *rendered glyph* tops match the panel's 50/55 relationship. Starting point:

```js
    clockSS = el("span", "t"); clockSS.style.font = FONT_CLOCK_SEC;
    clockSS.style.left = (margin + w1) + "px";
    clockSS.style.lineHeight = "10px";
    // clockSecondsDrop=5 (element_clock.go), minus the half-leading skew
    // between the two spans' line boxes — tuned against the panel golden
    // el_clock_at_50.png, not derived: webfont metrics are not the bitmap's.
    clockSS.style.top = (CLOCK_Y + 1) + "px";
```

Iterate the `+ 1` (and, only if needed, the HM span's line-height) against screenshots until the browser matches the golden. Whatever constant lands, the comment must cite the golden as the source of truth.

- [ ] **Step 4: Verify**

Screenshot the preview next to `el_clock_at_50.png` at matching scale — seconds glyphs must sit at the same offset relative to HH:MM in both. Also check `prefers-reduced-motion` and a font-still-loading first paint don't regress (the fix touches only static positioning, but confirm no exception in the console).

- [ ] **Step 5: Commit**

```bash
git add internal/web/static/board.js
git commit -m "fix(web): align preview clock seconds with the panel (half-leading skew)"
```

---

### Task 9: Desktop — skip the config index

**Files:**
- Modify: `internal/web/templates/config_list.html`
- Test: `internal/web/handlers_config_test.go`

**Interfaces:** none new. `/config` route and mobile behaviour unchanged.

- [ ] **Step 1: Write the failing test**

In `internal/web/handlers_config_test.go` (or wherever `GET /config` is already asserted — extend that test):

```go
func TestConfigListDesktopRedirectScript(t *testing.T) {
	// GET /config (authed, existing harness) and assert the body contains
	// both `min-width: 64rem` and `location.replace("/config/departures")`
	// — the desktop skip is client-side, so the contract is the script's
	// presence, not a server redirect.
}
```

(Real test against the existing harness; assert with `strings.Contains` on the recorded body.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run ConfigListDesktop -v`
Expected: FAIL (script absent).

- [ ] **Step 3: Implement**

`config_list.html` — first line inside `{{define "content"}}`:

```html
<script>if (window.matchMedia && matchMedia("(min-width: 64rem)").matches) location.replace("/config/departures");</script>
```

Rationale comment goes in the template right above it:

```html
<!-- Desktop (>=64rem) has the cfgnav master-detail rail, so the section list
     is redundant there: hop straight to the first section. location.replace
     keeps Back working (the index never enters history). Mobile keeps this
     page as its section navigation. -->
```

- [ ] **Step 4: Run tests + visual check**

Run: `go test ./internal/web/ -run Config -v` → PASS.
Dev-run: at a ≥64rem viewport, clicking "Configuration" lands on `/config/departures` with the rail; at a phone viewport `/config` still shows the section list. Browser Back from departures (desktop) does not bounce through `/config`.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): desktop skips the config index straight to Departures"
```

---

### Task 10: Update check — "you're up to date" feedback

**Files:**
- Modify: `internal/web/handlers_actions.go:131-136` (redirect target)
- Modify: `internal/web/handlers_status.go` (statusPageData + handleIndex)
- Modify: `internal/web/templates/status.html:34-37`
- Test: `internal/web/handlers_update_test.go` (redirect), `internal/web/handlers_status_test.go` (notice rendering)

**Interfaces:**
- Produces: `statusPageData.CheckedNow bool`; `POST /actions/update/check` redirects to `/?checked=1`.

- [ ] **Step 1: Write the failing tests**

In `internal/web/handlers_update_test.go`, find the existing check-handler test and change/extend the redirect assertion:

```go
	// handleUpdateCheck must land back on the status page with the
	// checked flag, so the page can confirm "no update" affirmatively.
	if loc := rec.Header().Get("Location"); loc != "/?checked=1" {
		t.Fatalf("Location = %q, want /?checked=1", loc)
	}
```

In `internal/web/handlers_status_test.go` (following its existing GET / render tests):

```go
func TestStatusShowsUpToDateAfterCheck(t *testing.T) {
	// Harness with an update.Status{Enabled: true, Running: "v0.4.1"} —
	// no Available, no LastError (reuse the file's status-page fixture).
	// GET /?checked=1 → body contains "You're up to date".
	// GET /           → body does NOT contain "You're up to date".
	// GET /?checked=1 with LastError set → body does NOT contain it
	// (the error line renders instead).
}
```

(Real assertions via the file's existing recorder helpers.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/web/ -run 'UpdateCheck|UpToDate' -v`
Expected: FAIL (Location is `/`; notice absent).

- [ ] **Step 3: Implement**

`handlers_actions.go` `handleUpdateCheck` — change the redirect:

```go
	// "/?checked=1": the status page affirms "no update available" only for
	// a landing that immediately follows an explicit check — an unqualified
	// "/" (every other visit) must not imply a check just happened.
	http.Redirect(w, r, "/?checked=1", http.StatusFound)
```

`handlers_status.go` — `statusPageData` gains:

```go
	// CheckedNow is true when this render immediately follows an explicit
	// "Check for updates" (the /?checked=1 PRG landing): the template may
	// affirm "up to date" rather than silently showing no banner.
	CheckedNow bool
```

`handleIndex` sets it with the other fields:

```go
		CheckedNow:    r.URL.Query().Get("checked") == "1",
```

`status.html` — replace lines 34-37's `{{else if .Enabled}}` branch:

```html
{{else if .Enabled}}
{{if and $.CheckedNow (not .LastError)}}<div class="notice calm">You're up to date &mdash; {{.Running}} is the latest release.</div>{{end}}
<form method="post" action="/actions/update/check" style="margin:0 0 1rem"><input type="hidden" name="csrf" value="{{$.CSRF}}">
  <button class="btn ghost" type="submit">Check for updates</button></form>
{{end}}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/web/ -v`
Expected: PASS (the e2e table's `/actions/update/check` row may assert the old Location — update it to `/?checked=1` if so).

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): affirm 'up to date' after an explicit update check"
```

---

### Task 11: Update buttons — in-flight busy states

**Files:**
- Modify: `internal/web/templates/status.html` (data-busy attributes + script include)
- Create: `internal/web/static/busy.js`
- Modify: `internal/web/static/style.css` (spinner + busy notice)
- Test: `internal/web/handlers_status_test.go` (attributes present)

**Interfaces:**
- Produces: `data-busy="<message>"` opt-in attribute on any form; on submit, all submit buttons in `data-busy` forms disable and a `.notice.busy` with a spinner is inserted before the first such form's block.

- [ ] **Step 1: Write the failing test**

In `internal/web/handlers_status_test.go`, extend the update-section render test(s):

```go
	// The install/check forms must opt into the busy-state script.
	if !strings.Contains(body, `data-busy="Installing update`) {
		t.Fatal("apply form lacks data-busy")
	}
	if !strings.Contains(body, `data-busy="Checking for updates`) {
		t.Fatal("check form lacks data-busy")
	}
```

(Attach to a fixture that renders both the Available and Enabled branches — likely two assertions in two existing tests.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/web/ -run Status -v` → FAIL.

- [ ] **Step 3: Implement**

`status.html` — the three update forms gain the attribute:

- apply form (line 29): `<form method="post" action="/actions/update/apply" data-busy="Installing update — downloading and verifying… the board restarts itself when it's done.">`
- both check forms (lines 31, 35): `<form method="post" action="/actions/update/check" data-busy="Checking for updates…" …>`

and the page's script line (next to the board.js include, line 61):

```html
<script src="/static/busy.js" defer></script>
```

Create `internal/web/static/busy.js`:

```js
// Busy states for slow form posts (updates): any form with a data-busy
// attribute disables every update button on submit and shows the message
// with a spinner until the server responds (PRG navigation replaces the
// page, so there is nothing to undo on success; a failed network submit
// re-enables via pageshow when the user navigates back).
(function () {
  "use strict";
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form.hasAttribute || !form.hasAttribute("data-busy")) return;
    var msg = form.getAttribute("data-busy");
    document.querySelectorAll("form[data-busy] button").forEach(function (b) {
      b.disabled = true;
    });
    var n = document.createElement("div");
    n.className = "notice calm busy";
    var spin = document.createElement("span");
    spin.className = "spinner";
    n.appendChild(spin);
    n.appendChild(document.createTextNode(" " + msg));
    form.parentNode.insertBefore(n, form);
  });
  // bfcache restore (user pressed Back mid-update): reset the buttons.
  window.addEventListener("pageshow", function (e) {
    if (!e.persisted) return;
    document.querySelectorAll("form[data-busy] button").forEach(function (b) {
      b.disabled = false;
    });
    document.querySelectorAll(".notice.busy").forEach(function (n) { n.remove(); });
  });
})();
```

`style.css` — with the other notice styles:

```css
.spinner { display: inline-block; width: .85em; height: .85em; border: 2px solid currentColor; border-right-color: transparent; border-radius: 50%; vertical-align: -.1em; animation: spin .8s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spinner { animation: none; opacity: .4; } }
```

- [ ] **Step 4: Run tests + visual check**

Run: `go test ./internal/web/ -v` → PASS.
Dev-run: click "Check for updates" — button disables, spinner notice appears, page then lands on `/?checked=1` with the up-to-date notice (Task 10). (A real apply can't run against the dev updater; the check path exercises the same code.)

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "feat(web): busy spinner + disabled buttons while updates check/install"
```

---

### Task 12: Gates, docs, PR

**Files:**
- Modify: `CONTEXT.md` (domain terms: headcode, RTT enrichment) — follow its existing entry style
- Verify only: whole repo

- [ ] **Step 1: Full gates**

Run: `make check`
Expected: vet, lint, tests all green. Fix anything red (quality-gate rule: even unrelated failures block).

- [ ] **Step 2: Docs**

Add "Headcode" and "RTT enrichment" to CONTEXT.md's terminology (source: RTT `trainIdentity`; column between sched and platform; non-fatal decorator), and note the busy-state/`data-busy` convention wherever the web UI's JS conventions are documented (if CONTEXT.md or DESIGN.md has such a section — otherwise skip, don't invent a doc).

- [ ] **Step 3: Push + PR**

```bash
git push -u origin feat/headcodes-and-polish
gh pr create --title "feat: headcode column (RTT), preview clock fix, desktop config skip, update feedback" --body "..."
```

PR body: summary of the four items, spec + plan links, screenshots from Tasks 7-9/11, note that `layout.headcodes` is default-off and RTT creds are write-only. End with the standard generated-with footer. Then request the milestone-end Codex review per repo rules.
