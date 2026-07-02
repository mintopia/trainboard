package data

import "testing"

func TestDeriveStatus(t *testing.T) {
	cases := []struct {
		etd  string
		want Status
	}{
		{"On time", StatusOnTime},
		{"Cancelled", StatusCancelled},
		{"Delayed", StatusDelayed},
		{"12:45", "Exp 12:45"},
		{"", StatusOnTime}, // missing etd ⇒ treat as on time
	}
	for _, c := range cases {
		if got := DeriveStatus(c.etd); got != c.want {
			t.Errorf("DeriveStatus(%q) = %q, want %q", c.etd, got, c.want)
		}
	}
}
