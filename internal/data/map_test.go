package data

import "testing"

func TestMapBoardBasic(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_basic.xml"))
	b, err := mapBoard(wb)
	if err != nil {
		t.Fatal(err)
	}
	if b.CRS != "PAD" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	d := b.Departures[0]
	if d.Status != StatusOnTime || d.Platform != "9" || d.OperatorCode != "GW" {
		t.Fatalf("departure = %+v", d)
	}
	if d.Destination.CRS != "BRI" || len(d.CallingPoints) != 2 {
		t.Fatalf("dep dest/calling wrong: %+v", d)
	}
	if b.GeneratedAt.IsZero() {
		t.Fatal("GeneratedAt not parsed")
	}
	if b.Messages[0] != "Engineering work between Slough and Reading." {
		t.Fatalf("message = %q", b.Messages[0])
	}
}

func TestMapBoardExpectedStatus(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_cancelled.xml"))
	b, _ := mapBoard(wb)
	if b.Departures[0].Status != StatusCancelled || !b.Departures[0].IsCancelled {
		t.Fatalf("status = %q", b.Departures[0].Status)
	}
}

func TestMapBoardMergesBusServices(t *testing.T) {
	wb, _ := parseBoard(readFixture(t, "board_bus.xml"))
	b, _ := mapBoard(wb)
	var buses int
	for _, d := range b.Departures {
		if d.ServiceType == "bus" {
			buses++
		}
	}
	if buses != 1 {
		t.Fatalf("bus departures = %d, want 1", buses)
	}
}
