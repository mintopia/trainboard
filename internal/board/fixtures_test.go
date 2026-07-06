package board

import (
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/data"
)

// fixedNow is the wall-clock instant every golden test renders at.
var fixedNow = time.Date(2026, 7, 6, 10, 30, 45, 0, time.UTC)

func mustFonts(t *testing.T) *Fonts {
	t.Helper()
	f, err := LoadFonts()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func dep(sched, plat, dest string, status data.Status, calling ...string) data.Departure {
	cps := make([]data.CallingPoint, len(calling))
	for i, name := range calling {
		cps[i] = data.CallingPoint{Location: data.Location{Name: name}, ScheduledTime: "11:0" + string(rune('0'+i))}
	}
	return data.Departure{
		ScheduledTime: sched,
		Status:        status,
		Platform:      plat,
		Operator:      "Great Western Railway",
		Destination:   data.Location{Name: dest},
		CallingPoints: cps,
		Length:        5,
	}
}

// fixtureBoard covers the spec's fixture matrix: on-time, delayed (Exp hh:mm),
// cancelled, missing platform, long destination.
func fixtureBoard() *data.Board {
	return &data.Board{
		GeneratedAt:  fixedNow,
		LocationName: "Paddington",
		CRS:          "PAD",
		Departures: []data.Departure{
			dep("10:32", "9", "Bristol Temple Meads", data.StatusOnTime, "Reading", "Didcot Parkway", "Swindon"),
			dep("10:41", "12", "Oxford", data.Status("Exp 10:44"), "Slough", "Reading"),
			dep("10:45", "", "Plymouth", data.StatusDelayed, "Reading", "Taunton"),
			dep("10:52", "5", "Weston-super-Mare via Bristol Temple Meads", data.StatusOnTime, "Reading"),
			dep("11:02", "8", "Cardiff Central", data.StatusCancelled),
		},
		Messages: []string{"Major disruption between Reading and London Paddington is expected until the end of the day. Please check before you travel."},
	}
}

func singleDepBoard() *data.Board {
	b := fixtureBoard()
	b.Departures = b.Departures[:1]
	return b
}

//nolint:unused // shared fixture consumed by later M1C tasks (scenes/runtime)
func emptyBoard() *data.Board {
	b := fixtureBoard()
	b.Departures = nil
	return b
}
