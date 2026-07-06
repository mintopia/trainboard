package main

import (
	"log/slog"

	"github.com/mintopia/trainboard/internal/runtime"
)

// teeFlusher fans a single render loop out to two Flushers: the real panel
// (a) and the in-memory/on-disk preview sink (b). Both are always called —
// the preview must keep working even if the panel errors, and vice versa —
// but only a's error is ever returned to the loop; b's is logged and
// swallowed so a preview hiccup can never stall or fault the panel.
type teeFlusher struct {
	a, b runtime.Flusher
}

// newTeeFlusher wires the panel (a) and preview sink (b) together.
func newTeeFlusher(a, b runtime.Flusher) *teeFlusher {
	return &teeFlusher{a: a, b: b}
}

// Flush writes packed to both flushers unconditionally, returning a's error
// (the panel is the flusher the render loop cares about failing on) and
// logging-and-swallowing b's.
func (t *teeFlusher) Flush(packed []byte) error {
	aErr := t.a.Flush(packed)
	if bErr := t.b.Flush(packed); bErr != nil {
		slog.Default().Error("preview flush failed", "error", bErr.Error())
	}
	return aErr
}

// SetContrast sets contrast on both flushers unconditionally, with the same
// a-wins/b-swallowed error contract as Flush.
func (t *teeFlusher) SetContrast(level byte) error {
	aErr := t.a.SetContrast(level)
	if bErr := t.b.SetContrast(level); bErr != nil {
		slog.Default().Error("preview set contrast failed", "error", bErr.Error())
	}
	return aErr
}
