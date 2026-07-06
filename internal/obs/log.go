package obs

import (
	"context"
	"io"
	"log/slog"
	"strings"
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

// teeHandler forwards records to the text handler and records them in the
// ring. It mirrors slog's own group/attr semantics so the ring's flat
// map[string]string carries the same information as the text output:
// attrs bound via WithAttrs are remembered (qualified by any groups open at
// the time), and groups opened via WithGroup qualify subsequently bound or
// logged attrs, joined with dots (e.g. group "req", key "n" -> "req.n").
type teeHandler struct {
	text  slog.Handler
	ring  *Ring
	level slog.Level

	groups     []string    // open group names, outermost first
	boundAttrs []slog.Attr // attrs from WithAttrs, keys already group-qualified
}

func (h *teeHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *teeHandler) Handle(ctx context.Context, rec slog.Record) error {
	if h.ring != nil {
		attrs := make(map[string]string, len(h.boundAttrs)+rec.NumAttrs())
		for _, a := range h.boundAttrs {
			attrs[a.Key] = a.Value.String()
		}
		prefix := h.groupPrefix()
		rec.Attrs(func(a slog.Attr) bool {
			attrs[prefix+a.Key] = a.Value.String()
			return true
		})
		h.ring.Add(Event{Time: rec.Time, Level: rec.Level, Msg: rec.Message, Attrs: attrs})
	}
	return h.text.Handle(ctx, rec)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	prefix := h.groupPrefix()
	qualified := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		qualified[i] = slog.Attr{Key: prefix + a.Key, Value: a.Value}
	}
	combined := make([]slog.Attr, 0, len(h.boundAttrs)+len(qualified))
	combined = append(combined, h.boundAttrs...)
	combined = append(combined, qualified...)
	return &teeHandler{
		text:       h.text.WithAttrs(attrs),
		ring:       h.ring,
		level:      h.level,
		groups:     h.groups,
		boundAttrs: combined,
	}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	groups := make([]string, 0, len(h.groups)+1)
	groups = append(groups, h.groups...)
	groups = append(groups, name)
	return &teeHandler{
		text:       h.text.WithGroup(name),
		ring:       h.ring,
		level:      h.level,
		groups:     groups,
		boundAttrs: h.boundAttrs,
	}
}

// groupPrefix returns the currently open group path as a dot-joined prefix
// (including a trailing dot), or "" if no groups are open.
func (h *teeHandler) groupPrefix() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".") + "."
}
