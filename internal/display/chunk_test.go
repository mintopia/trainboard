package display

import "testing"

func TestChunkSplitsExact(t *testing.T) {
	got := chunk(make([]byte, 8192), 4096)
	if len(got) != 2 || len(got[0]) != 4096 || len(got[1]) != 4096 {
		t.Fatalf("expected two 4096-byte chunks, got %d chunks", len(got))
	}
}

func TestChunkRemainder(t *testing.T) {
	got := chunk(make([]byte, 5000), 4096)
	if len(got) != 2 || len(got[1]) != 904 {
		t.Fatalf("remainder chunk wrong: %d chunks, last=%d", len(got), len(got[len(got)-1]))
	}
}

func TestChunkEmpty(t *testing.T) {
	if got := chunk(nil, 4096); len(got) != 0 {
		t.Fatalf("chunk(nil) = %d chunks, want 0", len(got))
	}
}
