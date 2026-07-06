package obs

import (
	"context"
	"io"
	"log/slog"
)

// NewLogger returns a slog.Logger that writes logfmt-style text to w with
// the time attribute removed (journald stamps its own), and tees every
// record at or above level into ring (nil ring disables the tee).
func NewLogger(w io.Writer, ring *Ring, level slog.Level) *slog.Logger {
	text := slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(&teeHandler{text: text, ring: ring, level: level})
}

// teeHandler forwards records to the text handler and records them in the ring.
type teeHandler struct {
	text  slog.Handler
	ring  *Ring
	level slog.Level
}

func (h *teeHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	if h.ring != nil {
		attrs := make(map[string]string, rec.NumAttrs())
		rec.Attrs(func(a slog.Attr) bool {
			attrs[a.Key] = a.Value.String()
			return true
		})
		h.ring.Add(Event{Time: rec.Time, Level: rec.Level, Msg: rec.Message, Attrs: attrs})
	}
	return h.text.Handle(ctx, rec)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{text: h.text.WithAttrs(attrs), ring: h.ring, level: h.level}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{text: h.text.WithGroup(name), ring: h.ring, level: h.level}
}
