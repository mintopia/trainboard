# LDBWS → internal model mapping

Derived schema-first from the OpenLDBWS `GetDepBoardWithDetails` WSDL
(ldb12, namespace `http://thalesgroup.com/RTTI/2021-11-01/ldb/`) and a captured
live response (Task 8 probe). **Not** ported from the old a51.li push-port feed.

## Request
- Endpoint `https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx`.
- Operation `GetDepBoardWithDetails`; `numRows` capped at 10 (always request 10,
  trim client-side; the client clamps out-of-range values to 10).
- Request body namespace (`ldb`): `http://thalesgroup.com/RTTI/2021-11-01/ldb/`
  (the current WSDL targetNamespace). **But the `SOAPAction` HTTP header retains the
  `2015-05-14` ldb version** — `http://thalesgroup.com/RTTI/2015-05-14/ldb/GetDepBoardWithDetails`
  (that's when the *WithDetails operations were introduced). A `2021-11-01` SOAPAction is
  rejected with "did not recognize the value of HTTP Header SOAPAction". Confirmed against
  the live WSDL binding + the live probe (2026-07-02).
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
