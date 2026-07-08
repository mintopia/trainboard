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
	start, err1 := time.Parse("15:04", c.Powersaving.Start)
	end, err2 := time.Parse("15:04", c.Powersaving.End)
	if err1 != nil || err2 != nil {
		return NormalBrightness
	}
	local := t.In(tz.Location())
	nowMin := local.Hour()*60 + local.Minute()
	startMin := start.Hour()*60 + start.Minute()
	endMin := end.Hour()*60 + end.Minute()

	var inside bool
	if startMin <= endMin {
		inside = nowMin >= startMin && nowMin < endMin // same-day window
	} else {
		inside = nowMin >= startMin || nowMin < endMin // cross-midnight window
	}
	if inside {
		return c.Powersaving.Brightness
	}
	return NormalBrightness
}
