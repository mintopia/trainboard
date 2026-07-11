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
	// StatusOnTime indicates the train is running as scheduled.
	StatusOnTime Status = "On time"
	// StatusCancelled indicates the train service has been cancelled.
	StatusCancelled Status = "Cancelled"
	// StatusDelayed indicates the train is delayed.
	StatusDelayed Status = "Delayed"
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
	Headcode      string // RTT trainIdentity, e.g. "1A23"; "" when enrichment is off or unmatched
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
