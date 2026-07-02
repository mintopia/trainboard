package data

import (
	"context"
	"net/http"
	"testing"
)

func TestFetchPipeline(t *testing.T) {
	body := string(readFixture(t, "board_basic.xml"))
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, body), nil
	})}
	b, err := c.Fetch(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	if b.CRS != "PAD" || len(b.Departures) != 1 {
		t.Fatalf("board = %+v", b)
	}
	if b.Departures[0].When.IsZero() {
		t.Fatal("Fetch did not reconstruct When")
	}
}

func TestFetchPropagatesFault(t *testing.T) {
	body := string(readFixture(t, "fault.xml"))
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(500, body), nil
	})}
	if _, err := c.Fetch(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120}); err == nil {
		t.Fatal("expected fault to propagate")
	}
}
