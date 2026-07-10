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
