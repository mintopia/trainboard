# M1 Plan B — Darwin Lite Data Client + Config Store Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Scope note:** M1 (`PLAN.md`) is split into three sequenced, independently-executable plans:
> - **Plan A (done, merged `9f4c399`)** — `display` + `render` foundation.
> - **Plan B (this doc)** — Darwin Lite (OpenLDBWS) `data` client + `config` store. Consumes issues **#24, #25, #26** (data ×3) and **#27** (config). Produces: a `data.Board` snapshot fetched from live Darwin and mapped to a source-agnostic internal model, plus a transactional local `config` store. Has **no dependency on Plan A** (`data`/`config` don't import `display`/`render`).
> - **Plan C** — `board` scenes + runtime poller/atomic snapshot + boot SLA + observability. Consumes A's `render`/`display` **and** B's `data`/`config`.

**Goal:** A native Go client that fetches `GetDepBoardWithDetails` from Darwin Lite (OpenLDBWS), maps the SOAP/XML response to a source-agnostic `Board` model (departures, calling points, sanitized NRCC messages, cross-midnight-correct absolute times), applies configured filters, plus a transactional JSON config store holding the (secret) Darwin token, board settings, and the powersaving brightness schedule.

**Architecture:** Two deep packages behind narrow interfaces, both source/transport-agnostic at their edges. `data` owns the SOAP envelope, HTTP transport (behind an `httpDoer` interface for fake-in-tests), namespace-tolerant XML parsing, XML→model mapping, HTML sanitization, client-side filtering, and time reconstruction — it exposes only the internal `Board`/`Departure`/`CallingPoint`/`Location` model. `config` owns a versioned JSON document with defaults, validation, transactional (temp+fsync+rename) writes at mode `0600`, token redaction, and powersaving-schedule evaluation. The two meet only in Plan C's runtime, which passes config values into `data.Fetch`.

**Tech Stack:** Go (latest stable — `go 1.26`; matches Plan A's `go.mod`). Standard library only: `encoding/xml`, `net/http`, `html`, `time`, `os`, `encoding/json`. No SOAP library (hand-rolled envelope per ADR 0001). No new third-party dependencies. Lint: `golangci-lint` (config already in repo from Plan A Task 1).

## Global Constraints

- **Module path:** `github.com/mintopia/trainboard` (established in Plan A).
- **Go version:** `go 1.26` (already set in `go.mod`).
- **No new dependencies.** `data` and `config` use only the Go standard library. Do **not** add a SOAP or XML-SOAP package; do **not** re-add `bs4`/HTML-parser equivalents — NRCC sanitization is stdlib `html` + a small tag stripper.
- **Source-agnostic model.** `board`/`render` (Plan A/C) must never see SOAP/XML. All Darwin types stay unexported inside `data`; only `Board`, `Departure`, `CallingPoint`, `Location`, `Status`, and `Fetch`/`Client`/`Request` are exported.
- **Schema-first, NOT ported.** The XML→model mapping is derived from the LDBWS WSDL + a captured live response (Task 8 gate), **not** by porting the reference project's `reference/src/trains/api.py` (that consumed a different a51.li push-port JSON feed). Specifically, per ADR 0001: LDBWS has **no `departed`/`arrived` flags** (departed services are dropped server-side; status derives purely from `etd`), **no `ssd`/origin-time** (cross-midnight is reconstructed from `std` vs the board's `generatedAt`), **no headcode** (`rsid` is a retail ID, not a headcode — do not map it to a headcode field). The reference Python is a valid guide **only** for config keys and client-side filter semantics, never for LDBWS field names.
- **Darwin request pins (`PLAN.md` item 5):**
  - Operation: **`GetDepBoardWithDetails`**. `numRows` **caps at 10** for WithDetails — always request 10 and trim client-side (so the cap + client filtering can't yield a false NoServices).
  - Token header element: `<AccessToken><TokenValue>…</TokenValue></AccessToken>` in the **Token namespace `http://thalesgroup.com/RTTI/2013-11-28/Token/types`** (authoritative — a wrong namespace yields "unauthorized"). This is the one namespace pinned by the spec; do not change it.
  - Body uses the **ldb12 request namespace `http://thalesgroup.com/RTTI/2021-11-01/ldb/`** (starting pin — the version-dated ldb namespace is the #1 failure suspect; Task 8's live probe is the gate that confirms/corrects it).
  - Destination filtering is **server-side**: request `filterCrs` + `filterType=to` (Darwin evaluates calls-at). Do this in the request, not client-side.
- **Status derives from `etd` only** (`PLAN.md` item 6): `etd` ∈ {`"On time"`, `"Cancelled"`, `"Delayed"`, or an expected time `"HH:MM"`}. No arrived/departed status.
- **Cross-midnight/DST (`PLAN.md` item 7):** reconstruct each service's absolute `time.Time` from `std` vs the board `generatedAt` using the **Europe/London** location (not a fixed offset) so DST is correct; a `std` more than ~6h before `generatedAt` rolls to the next day.
- **Config (`PLAN.md` item 8):** JSON at **`/var/lib/trainboard/config.json`**, mode **`0600`**, a **`version`** field, validation + sane defaults, **transactional writes** (write-temp + `fsync` + atomic rename). Holds the Darwin token (secret, **plaintext at rest, redacted in all logs**), the **powersaving brightness schedule** (start/end/brightness, cross-midnight window), and the **calling-point-times toggle** (`layout.times`).
- **TDD throughout:** red → green → refactor. Every task ends green with `go test ./...`, `go vet ./...`, `golangci-lint run` all passing. Commit at the end of each task. `make check` (from Plan A) runs all three.
- **Secrets:** never commit or print the token. `.env` holds `DARWIN_LITE_API_KEY` (live) — it is gitignored; read it only in the env-gated live probe (Task 8), never bake it into a fixture.

---

### Task 1: Internal domain model + Status derivation

The source-agnostic model every later task maps into and Plan C consumes. Pure data + one derivation function; no I/O.

**Files:**
- Create: `internal/data/model.go`
- Test: `internal/data/model_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Location struct { Name, CRS string }`
  - `type Status string` with `const (StatusOnTime Status = "On time"; StatusCancelled Status = "Cancelled"; StatusDelayed Status = "Delayed")`.
  - `type CallingPoint struct { Location Location; ScheduledTime, ExpectedTime, ActualTime string }` (`st`/`et`/`at`, raw `"HH:MM"` strings).
  - `type Departure struct { ScheduledTime, ExpectedTime string; Status Status; Platform, Operator, OperatorCode, ServiceType string; Length int; Origin, Destination Location; CallingPoints []CallingPoint; IsCancelled bool; CancelReason, DelayReason string; When time.Time }` (`When` filled by Task 7).
  - `type Board struct { GeneratedAt time.Time; LocationName, CRS string; Departures []Departure; Messages []string }`
  - `func DeriveStatus(etd string) Status` — maps a raw `etd` to a display status.

- [ ] **Step 1: Write the failing test**

`internal/data/model_test.go`:
```go
package data

import "testing"

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		etd  string
		want Status
	}{
		{"On time", StatusOnTime},
		{"Cancelled", StatusCancelled},
		{"Delayed", StatusDelayed},
		{"12:45", "Exp 12:45"},
		{"", StatusOnTime}, // missing etd ⇒ treat as on time
	}
	for _, c := range cases {
		if got := DeriveStatus(c.etd); got != c.want {
			t.Errorf("DeriveStatus(%q) = %q, want %q", c.etd, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/data/`
Expected: FAIL — undefined `Status`, `DeriveStatus`, etc.

- [ ] **Step 3: Write minimal implementation**

`internal/data/model.go`:
```go
// Package data fetches live departures from Darwin Lite (OpenLDBWS) and maps
// the SOAP/XML response to a source-agnostic board model. SOAP/XML details are
// contained here; callers see only Board and its constituent types.
package data

import "time"

// Location is a named station with its CRS code.
type Location struct {
	Name string
	CRS  string
}

// Status is the human-readable state shown on the right of a departure row.
type Status string

const (
	StatusOnTime    Status = "On time"
	StatusCancelled Status = "Cancelled"
	StatusDelayed   Status = "Delayed"
)

// CallingPoint is an intermediate/final stop with its scheduled, expected, and
// actual times as raw "HH:MM" strings (Darwin's st/et/at).
type CallingPoint struct {
	Location      Location
	ScheduledTime string
	ExpectedTime  string
	ActualTime    string
}

// Departure is a single train service leaving the origin station.
type Departure struct {
	ScheduledTime string // std, "HH:MM"
	ExpectedTime  string // etd, raw
	Status        Status
	Platform      string
	Operator      string
	OperatorCode  string
	ServiceType   string // "train", "bus", "ferry"
	Length        int
	Origin        Location
	Destination   Location
	CallingPoints []CallingPoint
	IsCancelled   bool
	CancelReason  string
	DelayReason   string
	When          time.Time // absolute departure time (reconstructed)
}

// Board is the origin station's departure board at a point in time.
type Board struct {
	GeneratedAt  time.Time
	LocationName string
	CRS          string
	Departures   []Departure
	Messages     []string // sanitized NRCC messages
}

// DeriveStatus maps a raw Darwin etd to a display status. etd is one of
// "On time", "Cancelled", "Delayed", an expected "HH:MM", or empty.
func DeriveStatus(etd string) Status {
	switch etd {
	case "", "On time":
		return StatusOnTime
	case "Cancelled":
		return StatusCancelled
	case "Delayed":
		return StatusDelayed
	default:
		return Status("Exp " + etd)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/data/ -run TestDeriveStatus -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/data/model.go internal/data/model_test.go
git commit -m "feat(data): source-agnostic board model + status derivation"
```

---

### Task 2: SOAP request envelope (golden request-bytes)

Builds the exact `GetDepBoardWithDetails` request bytes. Pinned by a golden test so a namespace/element regression is caught immediately. The request namespace is the highest-risk pin — Task 8's live probe is the confirming gate.

**Files:**
- Create: `internal/data/soap.go`
- Test: `internal/data/soap_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Request struct { OriginCRS, DestinationCRS string; NumRows, TimeWindowMinutes int }`
  - `func buildEnvelope(token string, r Request) ([]byte, error)` (unexported) — the SOAP request body bytes.
  - Constants: `endpointURL`, `soapAction`, `ldbNamespace`, `tokenNamespace`.

- [ ] **Step 1: Write the failing test**

`internal/data/soap_test.go`:
```go
package data

import (
	"strings"
	"testing"
)

func TestBuildEnvelopeWithDestination(t *testing.T) {
	got, err := buildEnvelope("TOKEN-GUID", Request{
		OriginCRS: "PAD", DestinationCRS: "RDG", NumRows: 10, TimeWindowMinutes: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	for _, want := range []string{
		`xmlns:typ="http://thalesgroup.com/RTTI/2013-11-28/Token/types"`,
		`xmlns:ldb="http://thalesgroup.com/RTTI/2021-11-01/ldb/"`,
		`<typ:AccessToken><typ:TokenValue>TOKEN-GUID</typ:TokenValue></typ:AccessToken>`,
		`<ldb:GetDepBoardWithDetailsRequest>`,
		`<ldb:numRows>10</ldb:numRows>`,
		`<ldb:crs>PAD</ldb:crs>`,
		`<ldb:filterCrs>RDG</ldb:filterCrs>`,
		`<ldb:filterType>to</ldb:filterType>`,
		`<ldb:timeWindow>120</ldb:timeWindow>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("envelope missing %q\n---\n%s", want, s)
		}
	}
}

func TestBuildEnvelopeOmitsFilterWhenNoDestination(t *testing.T) {
	got, _ := buildEnvelope("T", Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if strings.Contains(string(got), "filterCrs") || strings.Contains(string(got), "filterType") {
		t.Fatalf("filter elements must be omitted when no destination:\n%s", got)
	}
}

func TestBuildEnvelopeEscapesToken(t *testing.T) {
	got, _ := buildEnvelope(`a&b<c`, Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if strings.Contains(string(got), "a&b<c") {
		t.Fatalf("token not XML-escaped:\n%s", got)
	}
	if !strings.Contains(string(got), "a&amp;b&lt;c") {
		t.Fatalf("token escaping wrong:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/data/ -run TestBuildEnvelope`
Expected: FAIL — undefined `buildEnvelope`, `Request`.

- [ ] **Step 3: Write minimal implementation**

`internal/data/soap.go`:
```go
package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// Darwin Lite (OpenLDBWS) endpoint and namespaces.
//
// tokenNamespace is authoritative (PLAN.md item 5) — a wrong Token namespace
// yields "unauthorized". ldbNamespace is the ldb12 (2021-11-01) request
// namespace; it is the #1 failure suspect and is CONFIRMED by the Task 8 live
// probe. If the probe faults on schema/namespace, correct ldbNamespace,
// soapAction, and the golden test in the same commit.
const (
	endpointURL    = "https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx"
	soapAction     = "http://thalesgroup.com/RTTI/2021-11-01/ldb/GetDepBoardWithDetails"
	ldbNamespace   = "http://thalesgroup.com/RTTI/2021-11-01/ldb/"
	tokenNamespace = "http://thalesgroup.com/RTTI/2013-11-28/Token/types"
)

// Request is the parameters for a GetDepBoardWithDetails call.
type Request struct {
	OriginCRS         string
	DestinationCRS    string // optional; server-side filterCrs (filterType=to)
	NumRows           int
	TimeWindowMinutes int
}

// buildEnvelope renders the SOAP request bytes. Values are XML-escaped. The
// element order and namespaces are pinned by TestBuildEnvelope*.
func buildEnvelope(token string, r Request) ([]byte, error) {
	var filter string
	if r.DestinationCRS != "" {
		filter = fmt.Sprintf(
			"<ldb:filterCrs>%s</ldb:filterCrs><ldb:filterType>to</ldb:filterType>",
			esc(r.DestinationCRS))
	}
	body := fmt.Sprintf(
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" `+
			`xmlns:typ="%s" xmlns:ldb="%s">`+
			`<soap:Header><typ:AccessToken><typ:TokenValue>%s</typ:TokenValue>`+
			`</typ:AccessToken></soap:Header>`+
			`<soap:Body><ldb:GetDepBoardWithDetailsRequest>`+
			`<ldb:numRows>%d</ldb:numRows>`+
			`<ldb:crs>%s</ldb:crs>`+
			`%s`+
			`<ldb:timeOffset>0</ldb:timeOffset>`+
			`<ldb:timeWindow>%d</ldb:timeWindow>`+
			`</ldb:GetDepBoardWithDetailsRequest></soap:Body></soap:Envelope>`,
		tokenNamespace, ldbNamespace,
		esc(token), r.NumRows, esc(r.OriginCRS), filter, r.TimeWindowMinutes)
	return []byte(body), nil
}

// esc XML-escapes a value for inclusion in element text.
func esc(s string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/data/ -run TestBuildEnvelope -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/data/soap.go internal/data/soap_test.go
git commit -m "feat(data): GetDepBoardWithDetails SOAP envelope with pinned namespaces"
```

---

### Task 3: HTTP client + SOAP-fault detection

Wraps the envelope in an HTTP POST behind an injectable `httpDoer` so tests use a fake round-tripper (no network). Detects SOAP faults and non-200s as errors.

**Files:**
- Create: `internal/data/client.go`
- Test: `internal/data/client_test.go`

**Interfaces:**
- Consumes: `buildEnvelope`, `Request`, `endpointURL`, `soapAction` (Task 2).
- Produces:
  - `type httpDoer interface { Do(*http.Request) (*http.Response, error) }` (unexported).
  - `type Client struct { token string; http httpDoer }`
  - `func NewClient(token string) *Client` — uses a `*http.Client` with a 15s timeout.
  - `func (c *Client) fetchRaw(ctx context.Context, r Request) ([]byte, error)` (unexported) — POSTs, returns the raw response body, errors on non-200 or SOAP fault.
  - `func extractFault(body []byte) string` (unexported) — returns the `<faultstring>` text if the body is a SOAP fault, else "".

- [ ] **Step 1: Write the failing test**

`internal/data/client_test.go`:
```go
package data

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// doerFunc adapts a func to httpDoer.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func resp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestFetchRawPostsEnvelopeAndReturnsBody(t *testing.T) {
	var gotURL, gotAction, gotBody string
	c := &Client{token: "TOK", http: doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAction = r.Header.Get("SOAPAction")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return resp(200, "<ok/>"), nil
	})}
	body, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<ok/>" {
		t.Fatalf("body = %q", body)
	}
	if gotURL != endpointURL {
		t.Errorf("url = %q, want %q", gotURL, endpointURL)
	}
	if !strings.Contains(gotAction, "GetDepBoardWithDetails") {
		t.Errorf("SOAPAction = %q", gotAction)
	}
	if !strings.Contains(gotBody, "<ldb:crs>PAD</ldb:crs>") {
		t.Errorf("posted body missing crs:\n%s", gotBody)
	}
}

func TestFetchRawErrorsOnFault(t *testing.T) {
	fault := `<soap:Envelope><soap:Body><soap:Fault>` +
		`<faultstring>Unauthorized</faultstring></soap:Fault></soap:Body></soap:Envelope>`
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(500, fault), nil
	})}
	_, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("expected fault error mentioning Unauthorized, got %v", err)
	}
}

func TestFetchRawErrorsOnNon200WithoutFault(t *testing.T) {
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(503, "service unavailable"), nil
	})}
	if _, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120}); err == nil {
		t.Fatal("expected error on 503")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/data/ -run TestFetchRaw`
Expected: FAIL — undefined `Client`, `fetchRaw`.

- [ ] **Step 3: Write minimal implementation**

`internal/data/client.go`:
```go
package data

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpDoer is the subset of *http.Client the data client needs (injectable
// for tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to Darwin Lite with a fixed access token.
type Client struct {
	token string
	http  httpDoer
}

// NewClient returns a Client with a 15s HTTP timeout.
func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// fetchRaw POSTs the SOAP envelope and returns the raw response body. It errors
// on transport failure, non-200 status, or a SOAP fault in the body.
func (c *Client) fetchRaw(ctx context.Context, r Request) ([]byte, error) {
	env, err := buildEnvelope(c.token, r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(env))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", soapAction)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("data: darwin request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("data: reading darwin response: %w", err)
	}
	if fault := extractFault(body); fault != "" {
		return nil, fmt.Errorf("data: darwin SOAP fault: %s", fault)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("data: darwin returned HTTP %d", res.StatusCode)
	}
	return body, nil
}

// extractFault returns the faultstring if body is a SOAP fault, else "".
func extractFault(body []byte) string {
	var env struct {
		Fault struct {
			String string `xml:"faultstring"`
		} `xml:"Body>Fault"`
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Fault.String
}
```

> `xml:"Body>Fault"` matches on local names regardless of the SOAP namespace prefix, so it tolerates `soap:`/`soapenv:` variation.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/data/ -run TestFetchRaw -v`
Expected: PASS (all three).

- [ ] **Step 5: Commit**

```bash
git add internal/data/client.go internal/data/client_test.go
git commit -m "feat(data): HTTP SOAP client with fault detection behind httpDoer"
```

---

### Task 4: Namespace-tolerant XML response parsing + fixtures

Parses `GetDepBoardWithDetails` responses into internal wire structs using **local-name-only** xml tags (so the response's version-dated namespaces can't break parsing). Establishes the fixture set covering the required edge cases.

**Files:**
- Create: `internal/data/parse.go`
- Test: `internal/data/parse_test.go`
- Create: `internal/data/testdata/board_basic.xml`
- Create: `internal/data/testdata/board_cancelled.xml`
- Create: `internal/data/testdata/board_empty.xml`
- Create: `internal/data/testdata/fault.xml`

**Interfaces:**
- Consumes: nothing (produces raw wire structs for Task 5 to map).
- Produces:
  - Unexported wire structs: `wireEnvelope`, `wireBoard`, `wireService`, `wireCallingPoint`, `wireLocation`.
  - `func parseBoard(body []byte) (*wireBoard, error)` — decodes the response, errors if no `GetStationBoardResult` present.

- [ ] **Step 1: Create the canonical fixture + write the failing test**

`internal/data/testdata/board_basic.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2021-11-01/ldb/">
      <GetStationBoardResult xmlns:lt="http://thalesgroup.com/RTTI/2012-01-13/ldb/types">
        <lt:generatedAt>2026-07-02T12:30:00.0000000+01:00</lt:generatedAt>
        <lt:locationName>London Paddington</lt:locationName>
        <lt:crs>PAD</lt:crs>
        <lt:nrccMessages>
          <lt:message>Engineering work between &lt;A&gt;Slough&lt;/A&gt; and Reading.</lt:message>
        </lt:nrccMessages>
        <lt:trainServices>
          <lt:service>
            <lt:std>12:45</lt:std>
            <lt:etd>On time</lt:etd>
            <lt:platform>9</lt:platform>
            <lt:operator>Great Western Railway</lt:operator>
            <lt:operatorCode>GW</lt:operatorCode>
            <lt:serviceType>train</lt:serviceType>
            <lt:length>8</lt:length>
            <lt:origin><lt:location><lt:locationName>London Paddington</lt:locationName><lt:crs>PAD</lt:crs></lt:location></lt:origin>
            <lt:destination><lt:location><lt:locationName>Bristol Temple Meads</lt:locationName><lt:crs>BRI</lt:crs></lt:location></lt:destination>
            <lt:subsequentCallingPoints>
              <lt:callingPointList>
                <lt:callingPoint><lt:locationName>Reading</lt:locationName><lt:crs>RDG</lt:crs><lt:st>13:02</lt:st><lt:et>On time</lt:et></lt:callingPoint>
                <lt:callingPoint><lt:locationName>Swindon</lt:locationName><lt:crs>SWI</lt:crs><lt:st>13:25</lt:st><lt:et>On time</lt:et></lt:callingPoint>
              </lt:callingPointList>
            </lt:subsequentCallingPoints>
          </lt:service>
        </lt:trainServices>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>
```

`internal/data/parse_test.go`:
```go
package data

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseBoardBasic(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if wb.LocationName != "London Paddington" || wb.CRS != "PAD" {
		t.Fatalf("station = %q/%q", wb.LocationName, wb.CRS)
	}
	if wb.GeneratedAt == "" {
		t.Fatal("generatedAt empty")
	}
	if len(wb.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(wb.Services))
	}
	s := wb.Services[0]
	if s.STD != "12:45" || s.ETD != "On time" || s.Platform != "9" {
		t.Fatalf("service fields wrong: %+v", s)
	}
	if s.OperatorCode != "GW" || s.Length != 8 {
		t.Fatalf("operator/length wrong: %+v", s)
	}
	if s.Destination.CRS != "BRI" {
		t.Fatalf("destination = %q", s.Destination.CRS)
	}
	if len(s.CallingPoints) != 2 || s.CallingPoints[0].CRS != "RDG" {
		t.Fatalf("calling points wrong: %+v", s.CallingPoints)
	}
	if len(wb.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(wb.Messages))
	}
}

func TestParseBoardEmptyHasNoServices(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_empty.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Services) != 0 {
		t.Fatalf("expected no services, got %d", len(wb.Services))
	}
}

func TestParseBoardCancelled(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_cancelled.xml"))
	if err != nil {
		t.Fatal(err)
	}
	s := wb.Services[0]
	if s.ETD != "Cancelled" || !s.IsCancelled || s.CancelReason == "" {
		t.Fatalf("cancelled fields wrong: %+v", s)
	}
}

func TestParseBoardRejectsNonBoard(t *testing.T) {
	if _, err := parseBoard(readFixture(t, "fault.xml")); err == nil {
		t.Fatal("expected error parsing a fault as a board")
	}
}
```

- [ ] **Step 2: Create the remaining fixtures**

`internal/data/testdata/board_empty.xml` — same envelope as basic but with an empty board:
```xml
<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2021-11-01/ldb/">
      <GetStationBoardResult xmlns:lt="http://thalesgroup.com/RTTI/2012-01-13/ldb/types">
        <lt:generatedAt>2026-07-02T02:10:00.0000000+01:00</lt:generatedAt>
        <lt:locationName>London Paddington</lt:locationName>
        <lt:crs>PAD</lt:crs>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>
```

`internal/data/testdata/board_cancelled.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2021-11-01/ldb/">
      <GetStationBoardResult xmlns:lt="http://thalesgroup.com/RTTI/2012-01-13/ldb/types">
        <lt:generatedAt>2026-07-02T23:50:00.0000000+01:00</lt:generatedAt>
        <lt:locationName>London Paddington</lt:locationName>
        <lt:crs>PAD</lt:crs>
        <lt:trainServices>
          <lt:service>
            <lt:std>00:12</lt:std>
            <lt:etd>Cancelled</lt:etd>
            <lt:operator>Great Western Railway</lt:operator>
            <lt:operatorCode>GW</lt:operatorCode>
            <lt:serviceType>train</lt:serviceType>
            <lt:isCancelled>true</lt:isCancelled>
            <lt:cancelReason>This train has been cancelled because of a fault on this train.</lt:cancelReason>
            <lt:origin><lt:location><lt:locationName>London Paddington</lt:locationName><lt:crs>PAD</lt:crs></lt:location></lt:origin>
            <lt:destination><lt:location><lt:locationName>Oxford</lt:locationName><lt:crs>OXF</lt:crs></lt:location></lt:destination>
          </lt:service>
        </lt:trainServices>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>
```

`internal/data/testdata/fault.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <soap:Fault><faultstring>Unauthorized</faultstring></soap:Fault>
  </soap:Body>
</soap:Envelope>
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/data/ -run TestParseBoard`
Expected: FAIL — undefined `parseBoard`, wire structs.

- [ ] **Step 4: Write minimal implementation**

`internal/data/parse.go`:
```go
package data

import (
	"encoding/xml"
	"fmt"
)

// Wire structs mirror the LDBWS GetDepBoardWithDetails response. All xml tags
// use LOCAL NAMES ONLY (no namespace) so the response's version-dated ldb/lt
// namespaces cannot break decoding.

type wireLocation struct {
	LocationName string `xml:"location>locationName"`
	CRS          string `xml:"location>crs"`
}

type wireCallingPoint struct {
	LocationName string `xml:"locationName"`
	CRS          string `xml:"crs"`
	ST           string `xml:"st"`
	ET           string `xml:"et"`
	AT           string `xml:"at"`
}

type wireService struct {
	STD          string `xml:"std"`
	ETD          string `xml:"etd"`
	Platform     string `xml:"platform"`
	Operator     string `xml:"operator"`
	OperatorCode string `xml:"operatorCode"`
	ServiceType  string `xml:"serviceType"`
	Length       int    `xml:"length"`
	IsCancelled  bool   `xml:"isCancelled"`
	CancelReason string `xml:"cancelReason"`
	DelayReason  string `xml:"delayReason"`
	Origin       wireLocation `xml:"origin"`
	Destination  wireLocation `xml:"destination"`
	// subsequentCallingPoints > callingPointList > callingPoint (first list is
	// the through route; nested paths flatten the first list).
	CallingPoints []wireCallingPoint `xml:"subsequentCallingPoints>callingPointList>callingPoint"`
}

type wireBoard struct {
	GeneratedAt  string        `xml:"generatedAt"`
	LocationName string        `xml:"locationName"`
	CRS          string        `xml:"crs"`
	Messages     []string      `xml:"nrccMessages>message"`
	Services     []wireService `xml:"trainServices>service"`
	BusServices  []wireService `xml:"busServices>service"`
}

type wireEnvelope struct {
	Board *wireBoard `xml:"Body>GetDepBoardWithDetailsResponse>GetStationBoardResult"`
}

// parseBoard decodes a GetDepBoardWithDetails response body into a wireBoard.
func parseBoard(body []byte) (*wireBoard, error) {
	var env wireEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("data: decoding board: %w", err)
	}
	if env.Board == nil {
		return nil, fmt.Errorf("data: response has no GetStationBoardResult")
	}
	return env.Board, nil
}
```

> Bus services parse into a separate slice; Task 5 merges `Services`+`BusServices` and marks `ServiceType`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/data/ -run TestParseBoard -v`
Expected: PASS (all four).

- [ ] **Step 6: Commit**

```bash
git add internal/data/parse.go internal/data/parse_test.go internal/data/testdata/
git commit -m "feat(data): namespace-tolerant LDBWS response parsing + fixtures"
```

---

### Task 5: Map wire → model + NRCC HTML sanitization

Turns wire structs into the internal `Board`, deriving status from `etd`, merging bus services, and sanitizing NRCC messages (entity-decode + tag-strip). Time reconstruction (the `When` field) is deliberately left to Task 7.

**Files:**
- Create: `internal/data/sanitize.go`
- Create: `internal/data/map.go`
- Test: `internal/data/sanitize_test.go`
- Test: `internal/data/map_test.go`
- Create: `internal/data/testdata/board_bus.xml`

**Interfaces:**
- Consumes: wire structs + `parseBoard` (Task 4), `DeriveStatus` (Task 1).
- Produces:
  - `func sanitizeMessage(raw string) string` — entity-decode, strip tags, collapse whitespace, cap length at 500 runes.
  - `func mapBoard(wb *wireBoard) (*Board, error)` — wire → `Board`, `GeneratedAt` parsed as RFC3339, services mapped with status; `When` left zero.

- [ ] **Step 1: Write the failing tests**

`internal/data/sanitize_test.go`:
```go
package data

import "testing"

func TestSanitizeMessageStripsTagsAndDecodes(t *testing.T) {
	in := `Delays between <A href="x">Slough</A> &amp; Reading of up to 30 mins.`
	got := sanitizeMessage(in)
	want := "Delays between Slough & Reading of up to 30 mins."
	if got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
}

func TestSanitizeMessageCollapsesWhitespace(t *testing.T) {
	if got := sanitizeMessage("a\n\n  b\t c"); got != "a b c" {
		t.Fatalf("whitespace = %q", got)
	}
}

func TestSanitizeMessageCapsLength(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	if got := sanitizeMessage(string(long)); len([]rune(got)) != 500 {
		t.Fatalf("length = %d, want 500", len([]rune(got)))
	}
}
```

`internal/data/map_test.go`:
```go
package data

import "testing"

func TestMapBoardBasic(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_basic.xml"))
	b, err := mapBoard(wb)
	if err != nil {
		t.Fatal(err)
	}
	if b.CRS != "PAD" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	d := b.Departures[0]
	if d.Status != StatusOnTime || d.Platform != "9" || d.OperatorCode != "GW" {
		t.Fatalf("departure = %+v", d)
	}
	if d.Destination.CRS != "BRI" || len(d.CallingPoints) != 2 {
		t.Fatalf("dep dest/calling wrong: %+v", d)
	}
	if b.GeneratedAt.IsZero() {
		t.Fatal("GeneratedAt not parsed")
	}
	if b.Messages[0] != "Engineering work between Slough and Reading." {
		t.Fatalf("message = %q", b.Messages[0])
	}
}

func TestMapBoardExpectedStatus(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_cancelled.xml"))
	b, _ := mapBoard(wb)
	if b.Departures[0].Status != StatusCancelled || !b.Departures[0].IsCancelled {
		t.Fatalf("status = %q", b.Departures[0].Status)
	}
}

func TestMapBoardMergesBusServices(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_bus.xml"))
	b, _ := mapBoard(wb)
	var buses int
	for _, d := range b.Departures {
		if d.ServiceType == "bus" {
			buses++
		}
	}
	if buses != 1 {
		t.Fatalf("bus departures = %d, want 1", buses)
	}
}
```

`internal/data/testdata/board_bus.xml`:
```xml
<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/">
  <soap:Body>
    <GetDepBoardWithDetailsResponse xmlns="http://thalesgroup.com/RTTI/2021-11-01/ldb/">
      <GetStationBoardResult xmlns:lt="http://thalesgroup.com/RTTI/2012-01-13/ldb/types">
        <lt:generatedAt>2026-07-02T12:30:00.0000000+01:00</lt:generatedAt>
        <lt:locationName>London Paddington</lt:locationName>
        <lt:crs>PAD</lt:crs>
        <lt:busServices>
          <lt:service>
            <lt:std>12:50</lt:std>
            <lt:etd>On time</lt:etd>
            <lt:operator>Great Western Railway</lt:operator>
            <lt:operatorCode>GW</lt:operatorCode>
            <lt:serviceType>bus</lt:serviceType>
            <lt:origin><lt:location><lt:locationName>London Paddington</lt:locationName><lt:crs>PAD</lt:crs></lt:location></lt:origin>
            <lt:destination><lt:location><lt:locationName>Oxford</lt:locationName><lt:crs>OXF</lt:crs></lt:location></lt:destination>
          </lt:service>
        </lt:busServices>
      </GetStationBoardResult>
    </GetDepBoardWithDetailsResponse>
  </soap:Body>
</soap:Envelope>
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/data/ -run 'TestSanitize|TestMapBoard'`
Expected: FAIL — undefined `sanitizeMessage`, `mapBoard`.

- [ ] **Step 3: Write minimal implementation**

`internal/data/sanitize.go`:
```go
package data

import (
	"html"
	"strings"
)

// sanitizeMessage turns an NRCC HTML message into plain text: strip tags,
// decode entities, collapse whitespace, cap at 500 runes.
func sanitizeMessage(raw string) string {
	// Strip tags: drop everything between '<' and the next '>'.
	var b strings.Builder
	depth := 0
	for _, r := range raw {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	text := html.UnescapeString(b.String())
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > 500 {
		text = string(runes[:500])
	}
	return text
}
```

`internal/data/map.go`:
```go
package data

import (
	"fmt"
	"time"
)

// mapBoard converts a parsed wire board into the internal model. GeneratedAt is
// parsed as RFC3339; per-service When is left zero for Task 7 to reconstruct.
func mapBoard(wb *wireBoard) (*Board, error) {
	gen, err := time.Parse(time.RFC3339, wb.GeneratedAt)
	if err != nil {
		return nil, fmt.Errorf("data: parsing generatedAt %q: %w", wb.GeneratedAt, err)
	}
	b := &Board{
		GeneratedAt:  gen,
		LocationName: wb.LocationName,
		CRS:          wb.CRS,
	}
	for _, raw := range wb.Messages {
		if m := sanitizeMessage(raw); m != "" {
			b.Messages = append(b.Messages, m)
		}
	}
	for _, s := range wb.Services {
		b.Departures = append(b.Departures, mapService(s, "train"))
	}
	for _, s := range wb.BusServices {
		b.Departures = append(b.Departures, mapService(s, "bus"))
	}
	return b, nil
}

// mapService maps one wire service to a Departure, defaulting the service type.
func mapService(s wireService, defaultType string) Departure {
	st := s.ServiceType
	if st == "" {
		st = defaultType
	}
	cps := make([]CallingPoint, 0, len(s.CallingPoints))
	for _, cp := range s.CallingPoints {
		cps = append(cps, CallingPoint{
			Location:      Location{Name: cp.LocationName, CRS: cp.CRS},
			ScheduledTime: cp.ST,
			ExpectedTime:  cp.ET,
			ActualTime:    cp.AT,
		})
	}
	return Departure{
		ScheduledTime: s.STD,
		ExpectedTime:  s.ETD,
		Status:        DeriveStatus(s.ETD),
		Platform:      s.Platform,
		Operator:      s.Operator,
		OperatorCode:  s.OperatorCode,
		ServiceType:   st,
		Length:        s.Length,
		Origin:        Location{Name: s.Origin.LocationName, CRS: s.Origin.CRS},
		Destination:   Location{Name: s.Destination.LocationName, CRS: s.Destination.CRS},
		CallingPoints: cps,
		IsCancelled:   s.IsCancelled,
		CancelReason:  s.CancelReason,
		DelayReason:   s.DelayReason,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/data/ -run 'TestSanitize|TestMapBoard' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/data/sanitize.go internal/data/map.go internal/data/sanitize_test.go internal/data/map_test.go internal/data/testdata/board_bus.xml
git commit -m "feat(data): map wire response to model + NRCC HTML sanitization"
```

---

### Task 6: Client-side filtering + trimming

Applies configured platform/TOC filters, service-count trim, cutoff-hours window, and station-name replacements. Destination filtering is **already server-side** (Task 2's `filterCrs`); this task does the remaining client-side filters that LDBWS can't express.

**Files:**
- Create: `internal/data/filter.go`
- Test: `internal/data/filter_test.go`

**Interfaces:**
- Consumes: `Board`, `Departure`, `Location` (Task 1).
- Produces:
  - `type Filter struct { Platforms []string; TOCs []string; MaxServices int; CutoffHours int; Replacements map[string]string }`
  - `func (f Filter) Apply(b *Board) *Board` — returns a filtered copy (does not mutate the input). Order: platform → TOC → cutoff → replacements → trim to MaxServices. `MaxServices <= 0` means no trim; `CutoffHours <= 0` means no cutoff. Replacements are applied to `Location.Name` on the departure origin/destination and all calling points.

- [ ] **Step 1: Write the failing test**

`internal/data/filter_test.go`:
```go
package data

import (
	"testing"
	"time"
)

func dep(plat, toc string, when time.Time, destName string) Departure {
	return Departure{
		Platform: plat, OperatorCode: toc, When: when,
		Destination: Location{Name: destName, CRS: "XXX"},
		CallingPoints: []CallingPoint{{Location: Location{Name: destName}}},
	}
}

func TestFilterPlatformAndTOC(t *testing.T) {
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{Departures: []Departure{
		dep("9", "GW", base, "Bristol"),
		dep("1", "GW", base, "Oxford"),
		dep("9", "XR", base, "Reading"),
	}}
	out := Filter{Platforms: []string{"9"}, TOCs: []string{"GW"}}.Apply(b)
	if len(out.Departures) != 1 || out.Departures[0].Destination.Name != "Bristol" {
		t.Fatalf("platform+toc filter = %+v", out.Departures)
	}
}

func TestFilterCutoff(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{
		GeneratedAt: gen,
		Departures: []Departure{
			dep("1", "GW", gen.Add(1*time.Hour), "Near"),
			dep("1", "GW", gen.Add(9*time.Hour), "Far"),
		},
	}
	out := Filter{CutoffHours: 8}.Apply(b)
	if len(out.Departures) != 1 || out.Departures[0].Destination.Name != "Near" {
		t.Fatalf("cutoff filter = %+v", out.Departures)
	}
}

func TestFilterTrimsToMaxServices(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{GeneratedAt: gen, Departures: []Departure{
		dep("1", "GW", gen, "A"), dep("1", "GW", gen, "B"),
		dep("1", "GW", gen, "C"), dep("1", "GW", gen, "D"),
	}}
	if got := Filter{MaxServices: 3}.Apply(b); len(got.Departures) != 3 {
		t.Fatalf("trim = %d, want 3", len(got.Departures))
	}
}

func TestFilterReplacements(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{GeneratedAt: gen, Departures: []Departure{dep("1", "GW", gen, "London Paddington")}}
	out := Filter{Replacements: map[string]string{"London ": ""}}.Apply(b)
	if out.Departures[0].Destination.Name != "Paddington" {
		t.Fatalf("replacement dest = %q", out.Departures[0].Destination.Name)
	}
	if out.Departures[0].CallingPoints[0].Location.Name != "Paddington" {
		t.Fatalf("replacement calling = %q", out.Departures[0].CallingPoints[0].Location.Name)
	}
}

func TestFilterDoesNotMutateInput(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{GeneratedAt: gen, Departures: []Departure{dep("9", "GW", gen, "X"), dep("1", "GW", gen, "Y")}}
	_ = Filter{Platforms: []string{"9"}}.Apply(b)
	if len(b.Departures) != 2 {
		t.Fatalf("input mutated: len = %d", len(b.Departures))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/data/ -run TestFilter`
Expected: FAIL — undefined `Filter`.

- [ ] **Step 3: Write minimal implementation**

`internal/data/filter.go`:
```go
package data

import (
	"strings"
	"time"
)

// Filter holds the client-side filters LDBWS can't express server-side.
// Destination "calls-at" filtering is handled server-side via the request's
// filterCrs, not here.
type Filter struct {
	Platforms    []string
	TOCs         []string
	MaxServices  int
	CutoffHours  int
	Replacements map[string]string
}

// Apply returns a filtered copy of b. It never mutates the input.
func (f Filter) Apply(b *Board) *Board {
	out := *b
	out.Departures = nil
	cutoff := time.Time{}
	if f.CutoffHours > 0 {
		cutoff = b.GeneratedAt.Add(time.Duration(f.CutoffHours) * time.Hour)
	}
	for _, d := range b.Departures {
		if len(f.Platforms) > 0 && !contains(f.Platforms, d.Platform) {
			continue
		}
		if len(f.TOCs) > 0 && !contains(f.TOCs, d.OperatorCode) {
			continue
		}
		if !cutoff.IsZero() && !d.When.IsZero() && !d.When.Before(cutoff) {
			continue
		}
		out.Departures = append(out.Departures, f.replace(d))
		if f.MaxServices > 0 && len(out.Departures) >= f.MaxServices {
			break
		}
	}
	return &out
}

// replace applies station-name replacements to a departure's locations,
// returning a copy with fresh calling-point storage.
func (f Filter) replace(d Departure) Departure {
	if len(f.Replacements) == 0 {
		return d
	}
	d.Origin.Name = f.applyReplacements(d.Origin.Name)
	d.Destination.Name = f.applyReplacements(d.Destination.Name)
	cps := make([]CallingPoint, len(d.CallingPoints))
	for i, cp := range d.CallingPoints {
		cp.Location.Name = f.applyReplacements(cp.Location.Name)
		cps[i] = cp
	}
	d.CallingPoints = cps
	return d
}

func (f Filter) applyReplacements(name string) string {
	for from, to := range f.Replacements {
		name = strings.ReplaceAll(name, from, to)
	}
	return name
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/data/ -run TestFilter -v`
Expected: PASS (all five).

- [ ] **Step 5: Commit**

```bash
git add internal/data/filter.go internal/data/filter_test.go
git commit -m "feat(data): client-side platform/TOC/cutoff/replacement filtering"
```

---

### Task 7: Cross-midnight / DST time reconstruction + Fetch wiring

Reconstructs each departure's absolute `When` from `std` vs the board `generatedAt`, using Europe/London so DST is correct, then wires the full `Fetch` pipeline (request → transport → parse → map → reconstruct).

**Files:**
- Create: `internal/data/timerecon.go`
- Modify: `internal/data/client.go` (add `Fetch`)
- Test: `internal/data/timerecon_test.go`
- Test: `internal/data/fetch_test.go`

**Interfaces:**
- Consumes: `Board`, `Departure` (Task 1), `mapBoard`/`parseBoard` (Tasks 4–5), `fetchRaw` (Task 3), `Request` (Task 2).
- Produces:
  - `func reconstructTimes(b *Board, loc *time.Location)` — fills each `Departure.When` from `ScheduledTime` relative to `GeneratedAt`.
  - `func londonLocation() (*time.Location, error)` — loads `Europe/London` once.
  - `func (c *Client) Fetch(ctx context.Context, r Request) (*Board, error)` — the full pipeline.

- [ ] **Step 1: Write the failing test**

`internal/data/timerecon_test.go`:
```go
package data

import (
	"testing"
	"time"
)

func TestReconstructTimesSameDay(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 12, 30, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "12:45"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 2, 12, 45, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesRollsPastMidnight(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	// Board generated at 23:50; a 00:12 service is tomorrow.
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 23, 50, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "00:12"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 3, 0, 12, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesRecentPastStaysToday(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	// A std slightly before generatedAt (within 6h) is the same day, not tomorrow.
	b := &Board{
		GeneratedAt: time.Date(2026, 7, 2, 12, 30, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "12:28"}},
	}
	reconstructTimes(b, loc)
	want := time.Date(2026, 7, 2, 12, 28, 0, 0, loc)
	if !b.Departures[0].When.Equal(want) {
		t.Fatalf("When = %v, want %v", b.Departures[0].When, want)
	}
}

func TestReconstructTimesDSTSpringForward(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	// 2026 UK clocks go forward on 29 March. A board late on the 28th with an
	// early-hours service on the 29th must land in BST (offset +1h).
	b := &Board{
		GeneratedAt: time.Date(2026, 3, 28, 23, 40, 0, 0, loc),
		Departures:  []Departure{{ScheduledTime: "02:30"}},
	}
	reconstructTimes(b, loc)
	got := b.Departures[0].When
	_, offset := got.Zone()
	if got.Day() != 29 || offset != 3600 {
		t.Fatalf("DST reconstruction = %v (offset %ds), want 29th at +3600s", got, offset)
	}
}
```

`internal/data/fetch_test.go`:
```go
package data

import (
	"context"
	"net/http"
	"testing"
)

func TestFetchPipeline(t *testing.T) {
	body := string(readFixture(t, "board_basic.xml"))
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, body), nil
	})}
	b, err := c.Fetch(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	if b.CRS != "PAD" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	if b.Departures[0].When.IsZero() {
		t.Fatal("Fetch did not reconstruct When")
	}
}

func TestFetchPropagatesFault(t *testing.T) {
	body := string(readFixture(t, "fault.xml"))
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(500, body), nil
	})}
	if _, err := c.Fetch(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120}); err == nil {
		t.Fatal("expected fault to propagate")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/data/ -run 'TestReconstruct|TestFetch'`
Expected: FAIL — undefined `reconstructTimes`, `Fetch`.

- [ ] **Step 3: Write minimal implementation**

`internal/data/timerecon.go`:
```go
package data

import (
	"sync"
	"time"
)

var (
	londonOnce sync.Once
	londonLoc  *time.Location
	londonErr  error
)

// londonLocation loads Europe/London once (needed for DST-correct times).
func londonLocation() (*time.Location, error) {
	londonOnce.Do(func() { londonLoc, londonErr = time.LoadLocation("Europe/London") })
	return londonLoc, londonErr
}

// reconstructTimes fills each departure's When from its "HH:MM" ScheduledTime,
// anchored to the board's GeneratedAt in loc. LDBWS gives no date, so a std
// more than 6h before generatedAt is treated as the next day (rolled past
// midnight); everything else is the same day.
func reconstructTimes(b *Board, loc *time.Location) {
	gen := b.GeneratedAt.In(loc)
	for i := range b.Departures {
		hhmm := b.Departures[i].ScheduledTime
		if len(hhmm) < 5 {
			continue
		}
		var h, m int
		if _, err := parseHHMM(hhmm, &h, &m); err != nil {
			continue
		}
		cand := time.Date(gen.Year(), gen.Month(), gen.Day(), h, m, 0, 0, loc)
		if cand.Before(gen.Add(-6 * time.Hour)) {
			cand = cand.AddDate(0, 0, 1)
		}
		b.Departures[i].When = cand
	}
}

// parseHHMM parses "HH:MM" (ignoring any trailing seconds) into h and m.
func parseHHMM(s string, h, m *int) (int, error) {
	t, err := time.Parse("15:04", s[:5])
	if err != nil {
		return 0, err
	}
	*h, *m = t.Hour(), t.Minute()
	return 2, nil
}
```

Append `Fetch` to `internal/data/client.go`:
```go
// Fetch runs the full pipeline: build+POST the request, parse the response, map
// it to the model, and reconstruct absolute departure times.
func (c *Client) Fetch(ctx context.Context, r Request) (*Board, error) {
	raw, err := c.fetchRaw(ctx, r)
	if err != nil {
		return nil, err
	}
	wb, err := parseBoard(raw)
	if err != nil {
		return nil, err
	}
	b, err := mapBoard(wb)
	if err != nil {
		return nil, err
	}
	loc, err := londonLocation()
	if err != nil {
		return nil, err
	}
	reconstructTimes(b, loc)
	return b, nil
}
```

> Add `"context"` to `client.go`'s imports if Task 3 didn't already (it did, for `fetchRaw`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/data/ -run 'TestReconstruct|TestFetch' -v`
Expected: PASS (all six).

- [ ] **Step 5: Commit**

```bash
git add internal/data/timerecon.go internal/data/client.go internal/data/timerecon_test.go internal/data/fetch_test.go
git commit -m "feat(data): DST-correct time reconstruction + Fetch pipeline"
```

---

### Task 8: Env-gated live probe + schema-first mapping doc (GATE)

Runs a real `GetDepBoardWithDetails` against Darwin (skipped without a token) to confirm the request envelope actually returns a valid board — the **required gate** for the mapping doc (`PLAN.md` item 5: fixtures alone can lock in a wrong pin). Writes the mapping doc from the WSDL + the captured response.

**Files:**
- Create: `internal/data/live_test.go`
- Create: `docs/data/ldbws-mapping.md`
- Modify: `.gitignore` (ignore live captures)

**Interfaces:**
- Consumes: `NewClient`, `Fetch`, `Request` (Tasks 3, 7).
- Produces: an env-gated test + the mapping doc. No new production code.

- [ ] **Step 1: Write the env-gated live probe**

`internal/data/live_test.go`:
```go
package data

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveProbe hits the real Darwin Lite endpoint. It is skipped unless
// DARWIN_LITE_API_KEY is set, so CI and offline runs stay green. It is the gate
// that confirms the request envelope/namespaces are actually accepted.
func TestLiveProbe(t *testing.T) {
	token := os.Getenv("DARWIN_LITE_API_KEY")
	if token == "" {
		t.Skip("DARWIN_LITE_API_KEY not set; skipping live Darwin probe")
	}
	crs := os.Getenv("DARWIN_PROBE_CRS")
	if crs == "" {
		crs = "PAD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	b, err := NewClient(token).Fetch(ctx, Request{OriginCRS: crs, NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatalf("live probe failed (namespace/token suspect — see soap.go): %v", err)
	}
	if b.CRS == "" || b.LocationName == "" {
		t.Fatalf("live board missing station identity: %+v", b)
	}
	t.Logf("live board: %s (%s), %d departures, %d messages, generatedAt=%s",
		b.LocationName, b.CRS, len(b.Departures), len(b.Messages), b.GeneratedAt)
}
```

- [ ] **Step 2: Verify it skips cleanly without a token**

Run: `go test ./internal/data/ -run TestLiveProbe -v`
Expected: SKIP (`DARWIN_LITE_API_KEY not set`).

- [ ] **Step 3: Run the live probe against real Darwin (controller/human step)**

> This step requires the live token and network — it is run by the operator, not baked into CI.
```bash
set -a; . ./.env; set +a   # loads DARWIN_LITE_API_KEY from the gitignored .env
go test ./internal/data/ -run TestLiveProbe -v
```
Expected: PASS, with a log line reporting a real station board.
- **If it FAILS with a fault/unauthorized:** the ldb request namespace or SOAPAction in `soap.go` is wrong. Fetch the live WSDL (`https://lite.realtime.nationalrail.co.uk/OpenLDBWS/wsdl.aspx?ver=2021-11-01`), correct `ldbNamespace`/`soapAction`, update `TestBuildEnvelope*`, and re-run — all in one commit. The Token namespace is authoritative; do not change it.

- [ ] **Step 4: Ignore live captures + write the mapping doc**

Append to `.gitignore`:
```
# Live Darwin response captures (may contain real data) — never commit
internal/data/testdata/_live_*.xml
```

`docs/data/ldbws-mapping.md`:
```markdown
# LDBWS → internal model mapping

Derived schema-first from the OpenLDBWS `GetDepBoardWithDetails` WSDL
(ldb12, namespace `http://thalesgroup.com/RTTI/2021-11-01/ldb/`) and a captured
live response (Task 8 probe). **Not** ported from the old a51.li push-port feed.

## Request
- Operation `GetDepBoardWithDetails`; `numRows` capped at 10 (always request 10,
  trim client-side).
- Token: `<AccessToken><TokenValue>` in namespace
  `http://thalesgroup.com/RTTI/2013-11-28/Token/types`.
- Destination filter is server-side: `filterCrs` + `filterType=to`.
- `timeOffset=0`, `timeWindow` configurable (default 120 min).

## Response → model
| LDBWS field | model field | notes |
|---|---|---|
| `GetStationBoardResult/generatedAt` | `Board.GeneratedAt` | RFC3339; anchor for time reconstruction |
| `locationName`,`crs` | `Board.LocationName`,`Board.CRS` | |
| `nrccMessages/message` | `Board.Messages[]` | HTML-sanitized to text |
| `trainServices/service` + `busServices/service` | `Board.Departures[]` | merged; `ServiceType` marks bus |
| `service/std` | `Departure.ScheduledTime` | "HH:MM" |
| `service/etd` | `Departure.ExpectedTime` + `Status` | status derived: On time / Cancelled / Delayed / Exp HH:MM |
| `service/platform` | `Departure.Platform` | may be absent |
| `service/operator`,`operatorCode` | `Departure.Operator`,`OperatorCode` | |
| `service/length` | `Departure.Length` | often absent ⇒ 0 |
| `service/isCancelled`,`cancelReason` | `Departure.IsCancelled`,`CancelReason` | |
| `service/delayReason` | `Departure.DelayReason` | |
| `service/origin/location`,`destination/location` | `Departure.Origin`,`Destination` | |
| `subsequentCallingPoints/callingPointList/callingPoint` | `Departure.CallingPoints[]` | first list = through route |
| `callingPoint/st`,`et`,`at` | `CallingPoint.ScheduledTime`,`ExpectedTime`,`ActualTime` | |

## Deliberately NOT mapped
- **No headcode** — `rsid` is a retail service ID (e.g. "GW123400"), not a
  headcode. The board's headcode feature stays data-unavailable (PLAN.md item 4).
- **No `departed`/`arrived`** — LDBWS drops departed services server-side; status
  comes only from `etd`.
- **No `ssd`/origin-time** — absolute times reconstructed from `std` vs
  `generatedAt` (Europe/London, DST-correct; 6h look-back rolls past midnight).
```

- [ ] **Step 5: Verify gates + commit**

Run: `make check`
Expected: PASS (live probe skips without token; everything else green).
```bash
git add internal/data/live_test.go docs/data/ldbws-mapping.md .gitignore
git commit -m "feat(data): env-gated live Darwin probe + schema-first mapping doc"
```

---

### Task 9: Config struct + defaults + JSON round-trip

The versioned config document with sane defaults and stable JSON serialization. No file I/O yet (Task 11).

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Config struct` with `Version int`, nested `Darwin`, `Board`, `Layout`, `Powersaving` structs (JSON-tagged).
  - `const CurrentVersion = 1`.
  - `func Default() Config` — the default document (version set, `Board.Services=3`, `CutoffHours=8`, `RefreshSeconds=60`, `TimeWindowMinutes=120`, `Layout.Times=true`, powersaving disabled).

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:
```go
package config

import (
	"encoding/json"
	"testing"
)

func TestDefaultHasSaneValues(t *testing.T) {
	c := Default()
	if c.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", c.Version, CurrentVersion)
	}
	if c.Board.Services != 3 || c.Board.CutoffHours != 8 || c.Board.RefreshSeconds != 60 {
		t.Errorf("board defaults wrong: %+v", c.Board)
	}
	if c.Board.TimeWindowMinutes != 120 {
		t.Errorf("timeWindow default = %d, want 120", c.Board.TimeWindowMinutes)
	}
	if !c.Layout.Times {
		t.Error("layout.times should default true")
	}
	if c.Powersaving.Enabled {
		t.Error("powersaving should default disabled")
	}
}

func TestConfigRoundTrips(t *testing.T) {
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	c.Board.Replacements = map[string]string{"London ": ""}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Board.Origin != "PAD" || back.Darwin.Token != "GUID" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
	if back.Board.Replacements["London "] != "" {
		t.Fatalf("replacements lost: %+v", back.Board.Replacements)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — undefined `Config`, `Default`, `CurrentVersion`.

- [ ] **Step 3: Write minimal implementation**

`internal/config/config.go`:
```go
// Package config is the versioned local configuration store: a JSON document
// with defaults, validation, transactional writes, and token redaction.
package config

// CurrentVersion is the schema version written by this build.
const CurrentVersion = 1

// Config is the full device configuration document.
type Config struct {
	Version     int             `json:"version"`
	Darwin      DarwinConfig    `json:"darwin"`
	Board       BoardConfig     `json:"board"`
	Layout      LayoutConfig    `json:"layout"`
	Powersaving PowersavingConfig `json:"powersaving"`
}

// DarwinConfig holds the Darwin Lite access token (secret).
type DarwinConfig struct {
	Token string `json:"token"`
}

// BoardConfig holds departure-board content settings.
type BoardConfig struct {
	Origin            string            `json:"origin"`            // CRS
	Destination       string            `json:"destination"`       // optional CRS (server-side filter)
	Platforms         []string          `json:"platforms"`         // client filter
	TOCs              []string          `json:"tocs"`              // client filter (operatorCode)
	Services          int               `json:"services"`          // max rows to show
	CutoffHours       int               `json:"cutoffHours"`       // hide departures beyond this window
	RefreshSeconds    int               `json:"refreshSeconds"`    // poll interval
	TimeWindowMinutes int               `json:"timeWindowMinutes"` // LDBWS timeWindow
	Replacements      map[string]string `json:"replacements"`      // station-name substitutions
}

// LayoutConfig holds display layout toggles.
type LayoutConfig struct {
	Times bool `json:"times"` // show calling-point times
}

// PowersavingConfig dims the panel during a (possibly cross-midnight) window.
type PowersavingConfig struct {
	Enabled    bool   `json:"enabled"`
	Start      string `json:"start"`      // "HH:MM"
	End        string `json:"end"`        // "HH:MM"
	Brightness int    `json:"brightness"` // SSD1322 contrast 0-255 while saving
}

// Default returns a config populated with sane defaults.
func Default() Config {
	return Config{
		Version: CurrentVersion,
		Board: BoardConfig{
			Services:          3,
			CutoffHours:       8,
			RefreshSeconds:    60,
			TimeWindowMinutes: 120,
			Replacements:      map[string]string{},
		},
		Layout: LayoutConfig{Times: true},
		Powersaving: PowersavingConfig{
			Start:      "23:00",
			End:        "07:00",
			Brightness: 32,
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'TestDefault|TestConfigRoundTrips' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): versioned config struct with sane defaults"
```

---

### Task 10: Config validation

Validates a config document, returning descriptive errors. Plan C's runtime uses a validation failure to decide AP mode; this task only produces the checker.

**Files:**
- Create: `internal/config/validate.go`
- Test: `internal/config/validate_test.go`

**Interfaces:**
- Consumes: `Config` + nested types (Task 9).
- Produces: `func (c Config) Validate() error` — checks version, CRS format, ranges, and "HH:MM" fields.

- [ ] **Step 1: Write the failing test**

`internal/config/validate_test.go`:
```go
package config

import (
	"strings"
	"testing"
)

func validConfig() Config {
	c := Default()
	c.Darwin.Token = "some-guid"
	c.Board.Origin = "PAD"
	return c
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		msg    string
	}{
		{"bad version", func(c *Config) { c.Version = 99 }, "version"},
		{"empty origin", func(c *Config) { c.Board.Origin = "" }, "origin"},
		{"bad origin crs", func(c *Config) { c.Board.Origin = "PADX" }, "origin"},
		{"bad destination crs", func(c *Config) { c.Board.Destination = "rd" }, "destination"},
		{"no token", func(c *Config) { c.Darwin.Token = "" }, "token"},
		{"services too low", func(c *Config) { c.Board.Services = 0 }, "services"},
		{"services too high", func(c *Config) { c.Board.Services = 11 }, "services"},
		{"cutoff negative", func(c *Config) { c.Board.CutoffHours = -1 }, "cutoff"},
		{"refresh too low", func(c *Config) { c.Board.RefreshSeconds = 2 }, "refresh"},
		{"bad powersaving time", func(c *Config) { c.Powersaving.Enabled = true; c.Powersaving.Start = "25:00" }, "powersaving"},
		{"bad powersaving brightness", func(c *Config) { c.Powersaving.Enabled = true; c.Powersaving.Brightness = 300 }, "brightness"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.msg) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.msg)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestValidate`
Expected: FAIL — undefined `Validate`.

- [ ] **Step 3: Write minimal implementation**

`internal/config/validate.go`:
```go
package config

import (
	"fmt"
	"time"
)

// Validate checks the config is internally consistent and usable. A non-nil
// error means the runtime should fall back to provisioning (AP mode).
func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return fmt.Errorf("config: unsupported version %d (want %d)", c.Version, CurrentVersion)
	}
	if !isCRS(c.Board.Origin) {
		return fmt.Errorf("config: board.origin %q is not a 3-letter CRS code", c.Board.Origin)
	}
	if c.Board.Destination != "" && !isCRS(c.Board.Destination) {
		return fmt.Errorf("config: board.destination %q is not a 3-letter CRS code", c.Board.Destination)
	}
	if c.Darwin.Token == "" {
		return fmt.Errorf("config: darwin.token is required")
	}
	if c.Board.Services < 1 || c.Board.Services > 10 {
		return fmt.Errorf("config: board.services %d out of range 1-10", c.Board.Services)
	}
	if c.Board.CutoffHours < 0 {
		return fmt.Errorf("config: board.cutoffHours %d must be >= 0", c.Board.CutoffHours)
	}
	if c.Board.RefreshSeconds < 15 {
		return fmt.Errorf("config: board.refreshSeconds %d too low (min 15)", c.Board.RefreshSeconds)
	}
	if c.Board.TimeWindowMinutes < 1 {
		return fmt.Errorf("config: board.timeWindowMinutes %d must be >= 1", c.Board.TimeWindowMinutes)
	}
	if c.Powersaving.Enabled {
		if !isHHMM(c.Powersaving.Start) || !isHHMM(c.Powersaving.End) {
			return fmt.Errorf("config: powersaving start/end must be HH:MM (got %q/%q)", c.Powersaving.Start, c.Powersaving.End)
		}
		if c.Powersaving.Brightness < 0 || c.Powersaving.Brightness > 255 {
			return fmt.Errorf("config: powersaving.brightness %d out of range 0-255", c.Powersaving.Brightness)
		}
	}
	return nil
}

// isCRS reports whether s is a 3-letter uppercase CRS code.
func isCRS(s string) bool {
	if len(s) != 3 {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// isHHMM reports whether s parses as a 24h "HH:MM" time.
func isHHMM(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: PASS (all sub-cases).

- [ ] **Step 5: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "feat(config): validation with descriptive errors"
```

---

### Task 11: Transactional Load/Save store

Reads and writes the config file atomically: `Save` writes a temp file, `fsync`s it, and renames over the target at mode `0600`; `Load` reads + validates, and a missing file yields defaults.

**Files:**
- Create: `internal/config/store.go`
- Test: `internal/config/store_test.go`

**Interfaces:**
- Consumes: `Config`, `Default`, `Validate` (Tasks 9–10).
- Produces:
  - `const DefaultPath = "/var/lib/trainboard/config.json"`.
  - `func Load(path string) (Config, error)` — missing file ⇒ `Default()`, no error; present ⇒ parse + `Validate`.
  - `func Save(path string, c Config) error` — validates, then transactional write at `0600`.

- [ ] **Step 1: Write the failing test**

`internal/config/store_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != CurrentVersion || c.Board.Services != 3 {
		t.Fatalf("missing-file load not default: %+v", c)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Board.Origin != "PAD" || back.Darwin.Token != "GUID" {
		t.Fatalf("round-trip lost data: %+v", back)
	}
}

func TestSaveUsesMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Default() // no token, no origin ⇒ invalid
	if err := Save(path, c); err == nil {
		t.Fatal("expected Save to reject invalid config")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid Save must not create the file")
	}
}

func TestLoadRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"board":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected Load to reject an invalid config file")
	}
}

func TestSaveNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	c := Default()
	c.Darwin.Token = "GUID"
	c.Board.Origin = "PAD"
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("expected only config.json, found %d entries", len(entries))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestLoad|TestSave'`
Expected: FAIL — undefined `Load`, `Save`, `DefaultPath`.

- [ ] **Step 3: Write minimal implementation**

`internal/config/store.go`:
```go
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// DefaultPath is the on-device config location.
const DefaultPath = "/var/lib/trainboard/config.json"

// Load reads and validates the config at path. A missing file returns defaults
// with no error; a present-but-invalid file returns an error.
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("config: reading %s: %w", path, err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("config: parsing %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Save validates c, then writes it atomically at mode 0600: a temp file in the
// same directory is written, fsync'd, and renamed over path.
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("config: encoding: %w", err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: writing temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("config: fsync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("config: closing temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("config: renaming into place: %w", err)
	}
	return nil
}
```

> `defer os.Remove(tmpName)` cleans up the temp file on any error path; after a successful `Rename` the temp name no longer exists, so the deferred remove is a harmless no-op. This is why `TestSaveNoTempLeftBehind` passes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'TestLoad|TestSave' -v`
Expected: PASS (all cases).

- [ ] **Step 5: Commit**

```bash
git add internal/config/store.go internal/config/store_test.go
git commit -m "feat(config): transactional Load/Save at mode 0600"
```

---

### Task 12: Token redaction

Ensures the secret token never appears in logs: a `Redacted` view and a `String`/`GoString` that mask it.

**Files:**
- Create: `internal/config/redact.go`
- Test: `internal/config/redact_test.go`

**Interfaces:**
- Consumes: `Config`, `DarwinConfig` (Task 9).
- Produces:
  - `func (c Config) Redacted() Config` — a copy with a masked token.
  - `func (c Config) String() string` — the redacted config as a single line (so `%s`/`%v` never leak the token).
  - `func (d DarwinConfig) String() string` — masks the token.

- [ ] **Step 1: Write the failing test**

`internal/config/redact_test.go`:
```go
package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRedactedMasksToken(t *testing.T) {
	c := Default()
	c.Darwin.Token = "super-secret-guid"
	c.Board.Origin = "PAD"
	r := c.Redacted()
	if r.Darwin.Token == "super-secret-guid" || r.Darwin.Token == "" {
		t.Fatalf("token not masked: %q", r.Darwin.Token)
	}
	if c.Darwin.Token != "super-secret-guid" {
		t.Fatal("Redacted mutated the original")
	}
}

func TestStringNeverLeaksToken(t *testing.T) {
	c := Default()
	c.Darwin.Token = "super-secret-guid"
	c.Board.Origin = "PAD"
	for _, s := range []string{
		c.String(),
		fmt.Sprintf("%v", c),
		fmt.Sprintf("%s", c),
		fmt.Sprintf("%v", c.Darwin),
	} {
		if strings.Contains(s, "super-secret-guid") {
			t.Fatalf("token leaked in %q", s)
		}
	}
}

func TestRedactedEmptyTokenStaysEmpty(t *testing.T) {
	c := Default()
	if c.Redacted().Darwin.Token != "" {
		t.Fatal("empty token should stay empty when redacted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestRedacted|TestString'`
Expected: FAIL — undefined `Redacted`, `String`.

- [ ] **Step 3: Write minimal implementation**

`internal/config/redact.go`:
```go
package config

import "fmt"

const redacted = "***REDACTED***"

// Redacted returns a copy of c with the Darwin token masked (empty stays empty).
func (c Config) Redacted() Config {
	if c.Darwin.Token != "" {
		c.Darwin.Token = redacted
	}
	return c
}

// String renders the config with the token masked, safe for logs.
func (c Config) String() string {
	r := c.Redacted()
	return fmt.Sprintf("Config{version:%d origin:%q dest:%q services:%d refresh:%ds darwin:%s powersaving:%t}",
		r.Version, r.Board.Origin, r.Board.Destination, r.Board.Services,
		r.Board.RefreshSeconds, r.Darwin, r.Powersaving.Enabled)
}

// String masks the token so DarwinConfig can't leak it via %s/%v.
func (d DarwinConfig) String() string {
	if d.Token == "" {
		return "DarwinConfig{token:unset}"
	}
	return "DarwinConfig{token:" + redacted + "}"
}
```

> Defining `String()` on both `Config` and `DarwinConfig` makes them `fmt.Stringer`, so `%s` and `%v` use the masked form. Serialization (Task 11) uses `json.Marshal`, which ignores `String()`, so the real token is still persisted to disk (plaintext at rest, per spec) — only logs are masked.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'TestRedacted|TestString' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/redact.go internal/config/redact_test.go
git commit -m "feat(config): redact Darwin token in all log output"
```

---

### Task 13: Powersaving schedule evaluation

Computes the active panel contrast for a given time, honouring a cross-midnight powersaving window. Plan C's runtime calls this each tick to drive `SSD1322.SetContrast`.

**Files:**
- Create: `internal/config/powersaving.go`
- Test: `internal/config/powersaving_test.go`

**Interfaces:**
- Consumes: `Config`, `PowersavingConfig` (Task 9).
- Produces:
  - `const NormalBrightness = 255`.
  - `func (c Config) BrightnessAt(t time.Time) int` — returns `Powersaving.Brightness` when enabled and `t` is inside the (possibly cross-midnight) window, else `NormalBrightness`.

- [ ] **Step 1: Write the failing test**

`internal/config/powersaving_test.go`:
```go
package config

import (
	"testing"
	"time"
)

func at(hhmm string) time.Time {
	t, _ := time.Parse("15:04", hhmm)
	return time.Date(2026, 7, 2, t.Hour(), t.Minute(), 0, 0, time.UTC)
}

func TestBrightnessDisabledAlwaysNormal(t *testing.T) {
	c := Default() // powersaving disabled
	if got := c.BrightnessAt(at("02:00")); got != NormalBrightness {
		t.Fatalf("disabled brightness = %d, want %d", got, NormalBrightness)
	}
}

func TestBrightnessCrossMidnightWindow(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Start = "23:00"
	c.Powersaving.End = "07:00"
	c.Powersaving.Brightness = 20
	inside := []string{"23:00", "23:30", "00:00", "03:00", "06:59"}
	outside := []string{"07:00", "07:30", "12:00", "22:59"}
	for _, s := range inside {
		if got := c.BrightnessAt(at(s)); got != 20 {
			t.Errorf("at %s brightness = %d, want 20 (inside)", s, got)
		}
	}
	for _, s := range outside {
		if got := c.BrightnessAt(at(s)); got != NormalBrightness {
			t.Errorf("at %s brightness = %d, want %d (outside)", s, got, NormalBrightness)
		}
	}
}

func TestBrightnessSameDayWindow(t *testing.T) {
	c := Default()
	c.Powersaving.Enabled = true
	c.Powersaving.Start = "01:00"
	c.Powersaving.End = "06:00"
	c.Powersaving.Brightness = 10
	if got := c.BrightnessAt(at("03:00")); got != 10 {
		t.Errorf("inside same-day window = %d, want 10", got)
	}
	if got := c.BrightnessAt(at("12:00")); got != NormalBrightness {
		t.Errorf("outside same-day window = %d, want %d", got, NormalBrightness)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestBrightness`
Expected: FAIL — undefined `BrightnessAt`, `NormalBrightness`.

- [ ] **Step 3: Write minimal implementation**

`internal/config/powersaving.go`:
```go
package config

import "time"

// NormalBrightness is the panel contrast when powersaving is not active.
const NormalBrightness = 255

// BrightnessAt returns the panel contrast (0-255) for time t: the powersaving
// brightness when enabled and t falls inside the window, else NormalBrightness.
// The window may cross midnight (start > end).
func (c Config) BrightnessAt(t time.Time) int {
	if !c.Powersaving.Enabled {
		return NormalBrightness
	}
	start, err1 := time.Parse("15:04", c.Powersaving.Start)
	end, err2 := time.Parse("15:04", c.Powersaving.End)
	if err1 != nil || err2 != nil {
		return NormalBrightness
	}
	nowMin := t.Hour()*60 + t.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()

	var inside bool
	if startMin <= endMin {
		inside = nowMin >= startMin && nowMin < endMin // same-day window
	} else {
		inside = nowMin >= startMin || nowMin < endMin // cross-midnight window
	}
	if inside {
		return c.Powersaving.Brightness
	}
	return NormalBrightness
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestBrightness -v`
Expected: PASS.

- [ ] **Step 5: Run full gates + commit**

Run: `make check`
Expected: PASS (all `data` + `config` tests, vet, lint green; live probe skips).
```bash
git add internal/config/powersaving.go internal/config/powersaving_test.go
git commit -m "feat(config): cross-midnight powersaving brightness schedule"
```

---

## Self-Review

**Spec coverage (`PLAN.md` items 5–8 + issues #24–#27):**
- Item 5 (Darwin SOAP client, schema-first mapping, token namespace, numRows cap, timeWindow, live-probe gate, fixtures) → Tasks 2, 3, 4, 5, 8. Fixtures cover empty, cancelled, bus, SOAP-fault, missing-platform (cancelled fixture omits platform), missing/optional fields (empty board). **Gap folded in:** delayed, multi-destination, circular-route, and "10 fetched all filtered out" fixtures are not individually authored — the delayed path is exercised by `DeriveStatus`'s `"Delayed"` case (Task 1) and the Exp-status path by the `"12:45"` case; the filter-to-empty path is exercised by Task 6's platform/TOC tests reducing to zero. These are behaviourally covered; add dedicated XML fixtures during execution only if the live probe reveals structural surprises.
- Item 6 (server-side filterCrs, client-side platform/TOC/count/cutoff, no departed/arrived, NRCC sanitize) → Tasks 2 (filterCrs), 6 (client filters), 5 (sanitize), 1 (status from etd only).
- Item 7 (cross-midnight/DST from std vs generatedAt) → Task 7, with 23:xx/00:xx and DST-spring tests.
- Item 8 (JSON at path, 0600, version, validation, defaults, transactional, secret token redacted, powersaving schedule, layout.times) → Tasks 9, 10, 11, 12, 13.
- Issues #24 (SOAP client + mapping doc)→Tasks 2–5,8; #25 (filtering + NRCC)→Tasks 5–6; #26 (time reconstruction)→Task 7; #27 (config store)→Tasks 9–13.

**Placeholder scan:** No TODO/TBD/"handle edge cases" — every step has concrete code, fixtures, and commands.

**Type consistency:** `Board`/`Departure`/`CallingPoint`/`Location`/`Status` defined in Task 1 and used unchanged in Tasks 4–7. `Request` (Task 2) used in Tasks 3, 7, 8. `httpDoer`/`Client`/`fetchRaw` (Task 3) used in Task 7's `Fetch`. `Config` and nested types (Task 9) used in Tasks 10–13. `Filter` (Task 6) is standalone (Plan C wires config→Filter). `Config.BoardConfig.TimeWindowMinutes` (Task 9) maps to `data.Request.TimeWindowMinutes` (Task 2) — names align.

**Notes for the executor:**
- `data` and `config` are independent packages with no import edge between them; the two task groups (1–8 data, 9–13 config) can be executed in either order or in parallel.
- Task 8 Step 3 (live probe) is an operator/hardware step; CI stays green because the probe skips without `DARWIN_LITE_API_KEY`. Treat a red live probe as a namespace/token bug per the instructions in `soap.go`.
