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
