package stations

import "testing"

func TestName(t *testing.T) {
	cases := []struct {
		crs    string
		want   string
		wantOK bool
	}{
		{"THA", "Thatcham", true},
		{"tha", "Thatcham", true}, // case-insensitive
		{"PAD", "London Paddington", true},
		{"XXX", "", false},
		{"", "", false},
		{"THAM", "", false},
	}
	for _, c := range cases {
		got, ok := Name(c.crs)
		if got != c.want || ok != c.wantOK {
			t.Errorf("Name(%q) = %q,%v; want %q,%v", c.crs, got, ok, c.want, c.wantOK)
		}
	}
}

func TestSearchByNamePrefix(t *testing.T) {
	got := Search("sheff", 8)
	if len(got) == 0 || got[0].CRS != "SHF" {
		t.Fatalf("Search(sheff) = %+v, want Sheffield (SHF) first", got)
	}
}

func TestSearchByExactCodeRanksFirst(t *testing.T) {
	got := Search("pad", 8)
	if len(got) == 0 || got[0].CRS != "PAD" {
		t.Fatalf("Search(pad) = %+v, want exact code PAD first", got)
	}
}

func TestSearchSubstring(t *testing.T) {
	got := Search("paddington", 8)
	found := false
	for _, s := range got {
		if s.CRS == "PAD" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Search(paddington) = %+v, want PAD present", got)
	}
}

func TestSearchLimit(t *testing.T) {
	if got := Search("st", 5); len(got) > 5 {
		t.Fatalf("Search limit ignored: %d results", len(got))
	}
}

func TestSearchShortQueryEmpty(t *testing.T) {
	if got := Search("s", 8); len(got) != 0 {
		t.Fatalf("Search(single char) = %+v, want empty", got)
	}
	if got := Search("", 8); len(got) != 0 {
		t.Fatalf("Search(empty) = %+v, want empty", got)
	}
}
