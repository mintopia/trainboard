package main

import (
	"log/slog"

	"github.com/mintopia/trainboard/internal/runtime"
)

// teeFlusher fans a single render loop out to two Flushers: the real panel
// (a) and the in-memory/on-disk preview sink (b). Both are always called —
// the preview must keep working even if the panel errors, and vice versa —
// but only a's error is ever returned to the loop; b's is logged (through
// log, which callers wire to the obs logger so preview failures surface in
// /events, not just stderr) and swallowed so a preview hiccup can never
// stall or fault the panel.
type teeFlusher struct {
	a, b runtime.Flusher
	log  *slog.Logger
}

// newTeeFlusher wires the panel (a) and preview sink (b) together. log
// records b's swallowed errors; pass the obs logger so a struggling preview
// sink is still observable.
func newTeeFlusher(a, b runtime.Flusher, log *slog.Logger) *teeFlusher {
	return &teeFlusher{a: a, b: b, log: log}
}

// Flush writes packed to both flushers unconditionally, returning a's error
// (the panel is the flusher the render loop cares about failing on) and
// logging-and-swallowing b's.
func (t *teeFlusher) Flush(packed []byte) error {
	aErr := t.a.Flush(packed)
	if bErr := t.b.Flush(packed); bErr != nil {
		t.log.Warn("preview flush failed", "err", bErr.Error())
	}
	return aErr
}

// SetContrast sets contrast on both flushers unconditionally, with the same
// a-wins/b-swallowed error contract as Flush.
func (t *teeFlusher) SetContrast(level byte) error {
	aErr := t.a.SetContrast(level)
	if bErr := t.b.SetContrast(level); bErr != nil {
		t.log.Warn("preview set contrast failed", "err", bErr.Error())
	}
	return aErr
}
