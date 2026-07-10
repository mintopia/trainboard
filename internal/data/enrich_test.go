package data

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"testing"
)

func newDeparture(sched, destName string) Departure {
	return Departure{ScheduledTime: sched, Destination: Location{Name: destName}}
}

func TestMatchHeadcodesUniqueTime(t *testing.T) {
	b := &Board{Departures: []Departure{newDeparture("19:13", "London Paddington")}}
	MatchHeadcodes(b, []RTTService{{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"}})
	if b.Departures[0].Headcode != "1A23" {
		t.Fatalf("headcode = %q", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesTieBrokenByDestination(t *testing.T) {
	b := &Board{Departures: []Departure{newDeparture("19:13", "Didcot Parkway")}}
	lineup := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "2N40", BookedDeparture: "1913", DestinationName: "Didcot Parkway"},
	}
	MatchHeadcodes(b, lineup)
	if b.Departures[0].Headcode != "2N40" {
		t.Fatalf("headcode = %q, want tie broken by destination", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesAmbiguousLeavesBlank(t *testing.T) {
	b := &Board{Departures: []Departure{newDeparture("19:13", "London Paddington")}}
	lineup := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "1A25", BookedDeparture: "1913", DestinationName: "London Paddington"},
	}
	MatchHeadcodes(b, lineup)
	if b.Departures[0].Headcode != "" {
		t.Fatalf("ambiguous match must stay blank, got %q", b.Departures[0].Headcode)
	}
}

func TestMatchHeadcodesNoMatchLeavesBlank(t *testing.T) {
	b := &Board{Departures: []Departure{newDeparture("19:13", "London Paddington")}}
	MatchHeadcodes(b, []RTTService{{Headcode: "1A23", BookedDeparture: "0700", DestinationName: "London Paddington"}})
	if b.Departures[0].Headcode != "" {
		t.Fatalf("headcode = %q, want blank", b.Departures[0].Headcode)
	}
}

// fetcherFunc adapts a func to Fetcher for tests.
type fetcherFunc func(ctx context.Context, r Request) (*Board, error)

func (f fetcherFunc) Fetch(ctx context.Context, r Request) (*Board, error) { return f(ctx, r) }

func TestEnricherFillsHeadcodes(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return &Board{Departures: []Departure{newDeparture("19:13", "London Paddington")}}, nil
	})
	rtt := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, string(readFixture(t, "rtt_search.json"))), nil
	})}
	e := &HeadcodeEnricher{Base: base, RTT: rtt, Log: slog.Default()}
	b, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Departures[0].Headcode != "1A23" {
		t.Fatalf("headcode = %q", b.Departures[0].Headcode)
	}
}

func TestEnricherRTTFailureIsNonFatal(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return &Board{Departures: []Departure{newDeparture("19:13", "London Paddington")}}, nil
	})
	rtt := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(401, "denied"), nil
	})}
	e := &HeadcodeEnricher{Base: base, RTT: rtt, Log: slog.Default()}
	b, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"})
	if err != nil {
		t.Fatalf("rtt failure must be non-fatal, got %v", err)
	}
	if b.Departures[0].Headcode != "" {
		t.Fatalf("headcode = %q, want blank on rtt failure", b.Departures[0].Headcode)
	}
}

func TestEnricherPropagatesBaseError(t *testing.T) {
	base := fetcherFunc(func(context.Context, Request) (*Board, error) {
		return nil, errors.New("darwin down")
	})
	e := &HeadcodeEnricher{Base: base, RTT: NewRTTClient("u", "p"), Log: slog.Default()}
	if _, err := e.Fetch(context.Background(), Request{OriginCRS: "TWY"}); err == nil {
		t.Fatal("base error must propagate")
	}
}
