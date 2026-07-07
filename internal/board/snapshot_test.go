package board

import (
	"testing"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/obs"
	"github.com/mintopia/trainboard/internal/render"
)

func build(t *testing.T, s *Snapshot) *render.Framebuffer {
	t.Helper()
	scene := BuildScene(s, config.Default().Layout, "v1", mustFonts(t))
	fb := render.New(W, H)
	scene.Render(fb, 0, fixedNow)
	return fb
}

func pixEq(a, b *render.Framebuffer) bool { return string(a.Pix) == string(b.Pix) }

func TestBuildSceneNilIsInitialising(t *testing.T) {
	want := render.New(W, H)
	initialisingScene("v1", mustFonts(t)).Render(want, 0, fixedNow)
	if !pixEq(build(t, nil), want) {
		t.Fatal("nil snapshot must render the initialising scene")
	}
}

func TestBuildScenePerState(t *testing.T) {
	f := mustFonts(t)
	b := fixtureBoard()
	cases := []struct {
		name string
		snap *Snapshot
		want *render.Scene
	}{
		{"departures", &Snapshot{State: StateDepartures, Board: b}, departureBoardScene(b, config.Default().Layout, f)},
		{"noservices", &Snapshot{State: StateNoServices, Board: emptyBoard()}, noServicesScene(emptyBoard(), f)},
		{"error", &Snapshot{State: StateError, Fault: obs.FaultAuthRejected}, errorScene(obs.FaultAuthRejected, f)},
		{"clock", &Snapshot{State: StateClockNotSynced, Fault: obs.FaultClockNotSynced}, clockNotSyncedScene(f)},
		{"initialising", &Snapshot{State: StateInitialising}, initialisingScene("v1", f)},
	}
	for _, tc := range cases {
		want := render.New(W, H)
		tc.want.Render(want, 0, fixedNow)
		if !pixEq(build(t, tc.snap), want) {
			t.Errorf("%s: BuildScene rendered the wrong scene", tc.name)
		}
	}
}

func TestHotspotOverridesEverything(t *testing.T) {
	f := mustFonts(t)
	hs := &Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	want := render.New(W, H)
	hotspotInfoScene(hs.SSID, hs.Password, hs.Addr, f).Render(want, 0, fixedNow)
	for _, st := range []State{StateDepartures, StateError, StateClockNotSynced, StateNoServices} {
		snap := &Snapshot{State: st, Board: fixtureBoard(), Hotspot: hs, Fault: obs.FaultDarwinUnreachable}
		if !pixEq(build(t, snap), want) {
			t.Errorf("state %v: hotspot must take priority", st)
		}
	}
}

func TestDeparturesWithEmptyBoardFallsBackSafely(t *testing.T) {
	// Must not panic; renders the error scene defensively.
	f := mustFonts(t)
	want := render.New(W, H)
	errorScene(obs.FaultDarwinUnreachable, f).Render(want, 0, fixedNow)
	if !pixEq(build(t, &Snapshot{State: StateDepartures, Board: emptyBoard()}), want) {
		t.Fatal("empty departures board must fall back to error scene")
	}
}

func TestStateString(t *testing.T) {
	cases := map[State]string{StateInitialising: "initialising", StateDepartures: "departures", StateNoServices: "no-services", StateError: "error", StateClockNotSynced: "clock-not-synced"}
	for st, want := range cases {
		if st.String() != want {
			t.Errorf("%d.String() = %q, want %q", st, st.String(), want)
		}
	}
}
