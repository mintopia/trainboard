package board

import (
	"strings"
	"testing"
)

func TestWordwrapRespectsWidth(t *testing.T) {
	f := mustFonts(t)
	msg := fixtureBoard().Messages[0]
	lines := wordwrap(f.Regular, W, msg)
	if len(lines) < 2 {
		t.Fatalf("expected multiple lines, got %d", len(lines))
	}
	for _, ln := range lines {
		if w, _ := f.Regular.Measure(ln); w > W {
			t.Errorf("line %q measures %dpx > %d", ln, w, W)
		}
	}
	if joined := strings.Join(lines, " "); joined != msg {
		t.Errorf("wrap lost content:\n got %q\nwant %q", joined, msg)
	}
}

func TestWordwrapShortTextSingleLine(t *testing.T) {
	f := mustFonts(t)
	lines := wordwrap(f.Regular, W, "Short")
	if len(lines) != 1 || lines[0] != "Short" {
		t.Fatalf("got %q", lines)
	}
}

func TestWordwrapEmpty(t *testing.T) {
	f := mustFonts(t)
	if lines := wordwrap(f.Regular, W, ""); len(lines) != 0 {
		t.Fatalf("empty text must yield no lines, got %q", lines)
	}
}
