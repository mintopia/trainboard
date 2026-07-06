package board

import (
	"strconv"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// ordinal formats 1 → "1st", 2 → "2nd", 11 → "11th", matching the reference.
func ordinal(n int) string {
	suffix := "th"
	switch {
	case n%100 >= 11 && n%100 <= 13:
	case n%10 == 1:
		suffix = "st"
	case n%10 == 2:
		suffix = "nd"
	case n%10 == 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

// rowElements builds the six-column departure row at vertical offset y.
// Headcode is never drawn (no data source); platform only when present.
func rowElements(d data.Departure, order, y int, f *Fonts) []render.Element {
	els := []render.Element{
		&render.StaticText{Font: f.Regular, Text: ordinal(order), X: ColOrderX, Y: y, W: ColSchedX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: d.ScheduledTime, X: ColSchedX, Y: y, W: ColSchedW, H: RowH, Align: render.AlignCenter, Level: 15},
	}
	if d.Platform != "" {
		els = append(els, &render.StaticText{Font: f.Regular, Text: d.Platform, X: ColPlatformX, Y: y, W: ColPlatformW, H: RowH, Align: render.AlignCenter, Level: 15})
	}
	els = append(els,
		&render.StaticText{Font: f.Regular, Text: d.Destination.Name, X: ColDestX, Y: y, W: ColStatusX - ColDestX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: string(d.Status), X: ColStatusX, Y: y, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15},
	)
	return els
}
