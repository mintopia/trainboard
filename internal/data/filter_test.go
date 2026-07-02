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
	got := Filter{MaxServices: 3}.Apply(b)
	if len(got.Departures) != 3 {
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

func TestFilterReturnsIndependentCallingPoints(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{GeneratedAt: gen, Departures: []Departure{dep("1", "GW", gen, "Oxford")}}
	out := Filter{}.Apply(b) // no replacements configured
	out.Departures[0].CallingPoints[0].Location.Name = "MUTATED"
	if b.Departures[0].CallingPoints[0].Location.Name != "Oxford" {
		t.Fatalf("mutating returned copy changed input calling point: %q", b.Departures[0].CallingPoints[0].Location.Name)
	}
}

func TestFilterCutoffBoundaryExcluded(t *testing.T) {
	gen := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	b := &Board{
		GeneratedAt: gen,
		Departures: []Departure{
			dep("1", "GW", gen.Add(8*time.Hour), "AtCutoff"),         // exactly at cutoff → excluded
			dep("1", "GW", gen.Add(8*time.Hour-time.Minute), "Just"), // just inside → kept
		},
	}
	out := Filter{CutoffHours: 8}.Apply(b)
	if len(out.Departures) != 1 || out.Departures[0].Destination.Name != "Just" {
		t.Fatalf("cutoff boundary handling wrong: %+v", out.Departures)
	}
}
