package data

import (
	"os"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseBoardBasic(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_basic.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if wb.LocationName != "London Paddington" || wb.CRS != "PAD" {
		t.Fatalf("station = %q/%q", wb.LocationName, wb.CRS)
	}
	if wb.GeneratedAt == "" {
		t.Fatal("generatedAt empty")
	}
	if len(wb.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(wb.Services))
	}
	s := wb.Services[0]
	if s.STD != "12:45" || s.ETD != "On time" || s.Platform != "9" {
		t.Fatalf("service fields wrong: %+v", s)
	}
	if s.OperatorCode != "GW" || s.Length != 8 {
		t.Fatalf("operator/length wrong: %+v", s)
	}
	if s.Destination.CRS != "BRI" {
		t.Fatalf("destination = %q", s.Destination.CRS)
	}
	if len(s.CallingPoints) != 2 || s.CallingPoints[0].CRS != "RDG" {
		t.Fatalf("calling points wrong: %+v", s.CallingPoints)
	}
	if len(wb.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(wb.Messages))
	}
}

func TestParseBoardEmptyHasNoServices(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_empty.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Services) != 0 {
		t.Fatalf("expected no services, got %d", len(wb.Services))
	}
}

func TestParseBoardCancelled(t *testing.T) {
	wb, err := parseBoard(readFixture(t, "board_cancelled.xml"))
	if err != nil {
		t.Fatal(err)
	}
	s := wb.Services[0]
	if s.ETD != "Cancelled" || !s.IsCancelled || s.CancelReason == "" {
		t.Fatalf("cancelled fields wrong: %+v", s)
	}
}

func TestParseBoardRejectsNonBoard(t *testing.T) {
	if _, err := parseBoard(readFixture(t, "fault.xml")); err == nil {
		t.Fatal("expected error parsing a fault as a board")
	}
}
