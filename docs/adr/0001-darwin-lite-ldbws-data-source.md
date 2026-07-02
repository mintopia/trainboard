# 1. Use Darwin Lite (OpenLDBWS) as the sole train-data source

Date: 2026-07-02
Status: Accepted

## Context

The rewrite must feed the Departure Board directly from a public train-data API,
removing the old `a51.li` middleman. Two candidates were considered:

- **RealTimeTrains (RTT) pull API** — clean REST/JSON, easy to consume in Go, Bearer
  (JWT) auth. But the departure-board response omits intermediate calling points
  (requiring an extra per-service call), and provides no NRCC disruption messages and
  no train length/formation.
- **Darwin Lite (OpenLDBWS)** — National Rail's SOAP/XML web service. `GetDepBoardWithDetails`
  returns, in a single call: calling points (`subsequentCallingPoints`), NRCC messages
  (`nrccMessages`), cancellation/delay reasons, train length, platform, operator, and
  scheduled/expected times. GUID access-token auth in the SOAP header.

The existing board was designed around Darwin-derived data; every on-screen element
(calling-at scroll, message carousel, reason text, coach count) maps directly to
OpenLDBWS fields.

## Decision

Use **Darwin Lite (OpenLDBWS) `GetDepBoardWithDetails`** as the single data source.
Drop RTT entirely (the credential remains on file but unused). SOAP/XML handling is
contained within the `data` package behind the internal `Departure/Stop/Location/State`
model, so `board` and `render` remain source-agnostic.

## Consequences

- **Positive:** Reproduces the current board's content 1:1 with no feature drops and no
  N+1 per-service calls. One API call per refresh. Talks directly to National Rail.
- **Negative:** SOAP/XML rather than REST/JSON — we hand-roll a small SOAP envelope and
  parse XML with `encoding/xml` (no stdlib SOAP client). Contained to one package.
- **Negative:** Ties us to OpenLDBWS Lite rate/usage limits; acceptable for a single
  board polling on the order of once per minute.
- If Darwin proves insufficient, revisiting RTT (or the Darwin push port) is possible
  behind the same `data` interface, but would be a new decision superseding this one.
- **LDBWS ≠ the old push-port feed** (Codex/Fable review). `GetDepBoardWithDetails`
  differs from the reference's a51.li push-port JSON: it caps at **10 rows**, has **no
  `departed`/`arrived` flags** (departed services dropped server-side; status is derived
  from `etd`), **no `ssd`/origin-time** (cross-midnight derived from `std` vs
  `generatedAt`), and **no headcode** (`rsid` is a retail ID). The mapping doc is written
  schema-first from the WSDL + a captured live response, not by porting the old feed.
  Destination "calls-at" filtering is pushed **server-side** (`filterCrs`+`filterType=to`)
  so the 10-row cap can't cause a false NoServices. Token header uses the **Token
  namespace** `http://thalesgroup.com/RTTI/2013-11-28/Token/types`.
