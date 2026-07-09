package board

import (
	"fmt"
	"strings"

	"github.com/mintopia/trainboard/internal/config"
	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// CallingAtText is the exact calling-points string the panel scrolls:
// "A, B and C", each name suffixed " (HH:MM)" when times is true.
func CallingAtText(d data.Departure, showTimes bool) string {
	if len(d.CallingPoints) == 0 {
		return ""
	}
	names := make([]string, len(d.CallingPoints))
	for i, cp := range d.CallingPoints {
		names[i] = cp.Location.Name
		if showTimes {
			names[i] += " (" + cp.ScheduledTime + ")"
		}
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// ServiceInfoText is the panel's service line: "<Operator> service formed of
// N coaches".
func ServiceInfoText(d data.Departure) string {
	info := d.Operator + " service"
	if d.Length > 0 {
		plural := "es"
		if d.Length == 1 {
			plural = ""
		}
		info += fmt.Sprintf(" formed of %d coach%s", d.Length, plural)
	}
	return info
}

// departureBoardScene composes the primary live scene from a non-empty board.
func departureBoardScene(b *data.Board, layout config.LayoutConfig, f *Fonts) *render.Scene {
	first := b.Departures[0]
	els := []render.Element{
		newNextServiceRow(first, f),
		&render.ScrollingText{Font: f.Regular, Text: "Calling at:", X: 0, Y: RowH, W: CallingLabelW, H: RowH, Level: 15},
		&render.ScrollingText{Font: f.Regular, Text: CallingAtText(first, layout.Times), X: CallingListX, Y: RowH, W: CallingListW, H: RowH, Level: 15},
		&render.ScrollingText{Font: f.Regular, Text: ServiceInfoText(first), X: 0, Y: ServiceInfoY, W: W, H: RowH, Level: 15},
		newRemainingServices(b.Departures[1:], f),
		offsetElement(&render.Clock{Large: f.BoldLarge, Tall: f.BoldTall, W: W, Level: 15}, 0, ClockY, W, ClockH),
	}
	return &render.Scene{Elements: els}
}
