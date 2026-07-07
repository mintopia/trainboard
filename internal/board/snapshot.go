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
	SSID, Password, Addr string
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
			return hotspotInfoScene(s.Hotspot.SSID, s.Hotspot.Password, s.Hotspot.Addr, f)
		}
		return initialisingScene(version, f)
	}
	if s.Hotspot != nil {
		return hotspotInfoScene(s.Hotspot.SSID, s.Hotspot.Password, s.Hotspot.Addr, f)
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
