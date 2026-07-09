package web

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/data"
)

// serviceView is one departure row's pre-rasterised text, exactly as the
// panel would show it.
type serviceView struct {
	Order       int    `json:"order"`
	Scheduled   string `json:"scheduled"`
	Platform    string `json:"platform,omitempty"`
	Destination string `json:"destination"`
	Status      string `json:"status"`
	CallingAt   string `json:"callingAt,omitempty"`
	ServiceInfo string `json:"serviceInfo,omitempty"`
}

// hotspotView carries AP-mode identity for the web preview, mirroring
// board.Hotspot.
type hotspotView struct {
	SSID string `json:"ssid"`
	Addr string `json:"addr"`
}

// boardView is GET /api/board's JSON body: the board's current scene,
// described the same way the panel would render it.
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

// handleAPIBoard serves the pre-rasterised board content as JSON, reusing
// the exact text the OLED shows (board.CallingAtText/ServiceInfoText) so the
// web preview never drifts from the panel.
func (s *Server) handleAPIBoard(w http.ResponseWriter, _ *http.Request) {
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
