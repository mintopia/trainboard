package board

import (
	"strconv"

	"github.com/mintopia/trainboard/internal/data"
	"github.com/mintopia/trainboard/internal/render"
)

// Ordinal renders 1 → "1st", 2 → "2nd" etc, as shown in the board's order
// column.
func Ordinal(n int) string {
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

// rowElements builds the departure row at vertical offset y. With headcodes
// on, the optional headcode column (reference layout) sits between the
// scheduled time and the platform, shifting platform and destination right
// by ColHeadcodeW; off, the geometry is the original six-column row.
// Headcode and platform draw only when present; their boxes stay dark gaps
// otherwise, keeping column positions independent of per-row data.
func rowElements(d data.Departure, order, y int, f *Fonts, headcodes bool) []render.Element {
	els := []render.Element{
		&render.StaticText{Font: f.Regular, Text: Ordinal(order), X: ColOrderX, Y: y, W: ColSchedX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: d.ScheduledTime, X: ColSchedX, Y: y, W: ColSchedW, H: RowH, Align: render.AlignCenter, Level: 15},
	}
	platX, destX := ColPlatformX, ColDestX
	if headcodes {
		if d.Headcode != "" {
			els = append(els, &render.StaticText{Font: f.Regular, Text: d.Headcode, X: ColHeadcodeX, Y: y, W: ColHeadcodeW, H: RowH, Align: render.AlignCenter, Level: 15})
		}
		platX += ColHeadcodeW
		destX += ColHeadcodeW
	}
	if d.Platform != "" {
		els = append(els, &render.StaticText{Font: f.Regular, Text: d.Platform, X: platX, Y: y, W: ColPlatformW, H: RowH, Align: render.AlignCenter, Level: 15})
	}
	els = append(els,
		&render.StaticText{Font: f.Regular, Text: d.Destination.Name, X: destX, Y: y, W: ColStatusX - destX, H: RowH, Align: render.AlignLeft, Level: 15},
		&render.StaticText{Font: f.Regular, Text: string(d.Status), X: ColStatusX, Y: y, W: ColStatusW, H: RowH, Align: render.AlignRight, Level: 15},
	)
	return els
}
