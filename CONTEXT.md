# Context & Glossary — Train Departure Display

The ubiquitous language for the trainboard rewrite. Glossary only — no
implementation details. Keep terms precise; if code or conversation drifts from
these definitions, fix one or the other.

## Terms

- **Board (Departure Board)** — the physical 256×64 SSD1322 OLED and, by extension,
  the on-screen representation of departures for one origin station.
- **Departure** — a single train service leaving the origin station: its scheduled
  time, expected time, platform, operator, destination, calling points, and status.
- **Calling Point** — an intermediate or final station a Departure stops at, with a
  station name and a time. Sourced from Darwin's `subsequentCallingPoints`.
- **Origin Station** — the station the Board is configured to show departures from
  (identified by CRS code).
- **CRS** — three-letter Computer Reservation System station code (e.g. `PAD`).
  The public station identifier used by Darwin.
- **Operator (TOC)** — the Train Operating Company running a Departure
  (e.g. "South Western Railway"). From Darwin's `operator`/`operatorCode`.
- **Status** — the human-readable state of a Departure shown on the right of its row:
  "On time", "Exp HH:MM", "Cancelled", "Arrived", etc. Derived from scheduled vs
  expected time and cancellation flags.
- **NRCC Message** — a National Rail Communication Centre disruption/notice message
  attached to a station board (`nrccMessages`). Shown in the message carousel when
  present.
- **Scene** — a full-screen mode of the Board. One drives the screen at a time:
  Initialising, Departure Board, No Services, Error, Hotspot Info, or Clock.
- **Error Scene** — shown when the network is up but fresh data cannot be obtained
  (Darwin failing / data stale) and the last-known board has been held for its grace
  period (5 minutes). States that live data is unavailable.
- **Headcode** — the train reporting number (e.g. `1A23`); sourced from the RealTime
  Trains API's `trainIdentity` field (Darwin's public LDBWS does not carry it). Shown
  as an optional column between scheduled time and platform when `layout.headcodes` is
  enabled; display geometry defined in `internal/board/board.go` (ColHeadcodeX=45,
  W=27). Default off.
- **Hotspot Info Scene** — shown while the device is in AP Mode: displays the hotspot
  SSID, its password, and the AP IP address so a user can connect and reconfigure.
- **Provisioning** — first-run configuration of a device over AP mode + web UI:
  entering wifi credentials and the Darwin access token.
- **AP Mode (Access Point Mode)** — the device hosting its own wifi network + captive
  portal serving the web UI. Entered both on first run (no wifi configured) and as a
  **fallback** when configured wifi cannot be joined. While in fallback AP mode the
  device periodically re-attempts the configured wifi and returns to normal operation
  if it succeeds.
- **Connectivity Manager** — the component that owns wifi state: attempts the
  configured network, decides when to enter/leave AP Mode, and drives the retry loop.

## External services

- **Darwin Lite (OpenLDBWS)** — National Rail's Live Departure Boards Web Service, a
  SOAP/XML API at `lite.realtime.nationalrail.co.uk/OpenLDBWS/`. The sole source of
  live train data. Authenticated with a GUID access token in the SOAP header. See
  ADR 0001.
- **RTT enrichment** — optional decoration of Departures with Headcodes. Implemented
  as `data.HeadcodeEnricher`, a decorator around the Darwin fetcher that matches
  departures by booked departure time (ties broken by destination name); ambiguous
  matches render with blank headcode. Non-fatal: RTT fetch/auth failures log a warning
  and leave the board unaffected. Credentials stored in config as `rtt.username`
  (plaintext) and `rtt.password` (write-only secret).
