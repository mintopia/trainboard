package board

import (
	"strings"

	"github.com/mintopia/trainboard/internal/render"
)

// wordwrap greedily wraps text on spaces so each line measures at most
// width px. A single word wider than width gets its own (overflowing) line.
func wordwrap(f *render.Font, width int, text string) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		candidate := cur + " " + w
		if cw, _ := f.Measure(candidate); cw <= width {
			cur = candidate
			continue
		}
		lines = append(lines, cur)
		cur = w
	}
	return append(lines, cur)
}

// splitPages groups wrapped lines into 3-line carousel pages (reference
// NoServices.update_messages).
func splitPages(lines []string) [][]string {
	var pages [][]string
	for len(lines) > 3 {
		pages = append(pages, lines[:3])
		lines = lines[3:]
	}
	if len(lines) > 0 {
		pages = append(pages, lines)
	}
	return pages
}
