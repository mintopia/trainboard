package data

import (
	"html"
	"strings"
)

// sanitizeMessage turns an NRCC HTML message into plain text: strip tags,
// decode entities, collapse whitespace, cap at 500 runes.
func sanitizeMessage(raw string) string {
	// Strip tags: drop everything between '<' and the next '>'.
	var b strings.Builder
	depth := 0
	for _, r := range raw {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	text := html.UnescapeString(b.String())
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > 500 {
		text = string(runes[:500])
	}
	return text
}
