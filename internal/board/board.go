// Package board maps the data model to render scenes. It owns all
// exact-pixel geometry (derived from the reference implementation's
// render_departure and scene layouts — see the M1C design doc), builds rows
// and scenes from render primitives plus its own animated elements, and
// selects the active scene by priority. Pure: no I/O, no goroutines, no
// wall-clock reads.
package board

// Panel and layout geometry (pixels). Sources: design doc §DepartureBoard
// geometry; reference render_departure column table.
const (
	W    = 256
	H    = 64
	RowH = 12

	ColOrderX    = 0
	ColSchedX    = 17
	ColSchedW    = 28
	ColHeadcodeX = 45 // optional column between sched and platform (layout.headcodes)
	ColHeadcodeW = 27
	ColPlatformX = 45
	ColPlatformW = 19
	ColDestX     = 64
	ColStatusW   = 40
	ColStatusX   = W - ColStatusW // 216

	CallingLabelW = 42
	CallingListX  = 42
	CallingListW  = 214
	ServiceInfoY  = 24
	RemainingY    = 36
	ClockY        = 50
	ClockH        = 14
)
