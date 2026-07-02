package data

import "testing"

func TestSanitizeMessageStripsTagsAndDecodes(t *testing.T) {
	in := `Delays between <A href="x">Slough</A> &amp; Reading of up to 30 mins.`
	got := sanitizeMessage(in)
	want := "Delays between Slough & Reading of up to 30 mins."
	if got != want {
		t.Fatalf("sanitize = %q, want %q", got, want)
	}
}

func TestSanitizeMessageCollapsesWhitespace(t *testing.T) {
	if got := sanitizeMessage("a\n\n  b\t c"); got != "a b c" {
		t.Fatalf("whitespace = %q", got)
	}
}

func TestSanitizeMessageCapsLength(t *testing.T) {
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	if got := sanitizeMessage(string(long)); len([]rune(got)) != 500 {
		t.Fatalf("length = %d, want 500", len([]rune(got)))
	}
}
