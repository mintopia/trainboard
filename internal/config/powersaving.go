package config

import (
	"time"

	"github.com/mintopia/trainboard/internal/tz"
)

// NormalBrightness is the panel contrast when powersaving is not active.
const NormalBrightness = 255

// BrightnessAt returns the panel contrast (0-255) for time t: the powersaving
// brightness when enabled and t falls inside the window, else NormalBrightness.
// The window is Europe/London wall-clock (start/end are UK local times) and
// may cross midnight (start > end).
func (c Config) BrightnessAt(t time.Time) int {
	if !c.Powersaving.Enabled {
		return NormalBrightness
	}
	if !isHHMM(c.Powersaving.Start) || !isHHMM(c.Powersaving.End) {
		return NormalBrightness
	}
	if inHHMMWindow(t, c.Powersaving.Start, c.Powersaving.End) {
		return c.Powersaving.Brightness
	}
	return NormalBrightness
}

// inHHMMWindow reports whether t (converted to Europe/London) falls inside
// the [start, end) wall-clock window; start > end means the window crosses
// midnight. Malformed HH:MM strings report false.
func inHHMMWindow(t time.Time, startHHMM, endHHMM string) bool {
	start, err1 := time.Parse("15:04", startHHMM)
	end, err2 := time.Parse("15:04", endHHMM)
	if err1 != nil || err2 != nil {
		return false
	}
	local := t.In(tz.Location())
	nowMin := local.Hour()*60 + local.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()
	if startMin <= endMin {
		return nowMin >= startMin && nowMin < endMin // same-day window
	}
	return nowMin >= startMin || nowMin < endMin // cross-midnight window
}

// InUpdateWindow reports whether t is inside the unattended-update window
// (spec §3): the powersaving window when one is configured (display already
// dark — never mid-evening), else 03:00–05:00 Europe/London.
func (c Config) InUpdateWindow(t time.Time) bool {
	if c.Powersaving.Enabled && isHHMM(c.Powersaving.Start) && isHHMM(c.Powersaving.End) {
		return inHHMMWindow(t, c.Powersaving.Start, c.Powersaving.End)
	}
	return inHHMMWindow(t, "03:00", "05:00")
}
