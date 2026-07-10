package data

import (
	"context"
	"net/http"
	"testing"
)

func TestRTTLineupParsesServices(t *testing.T) {
	body := string(readFixture(t, "rtt_search.json"))
	var gotURL, gotAuth string
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		return resp(200, body), nil
	})}
	svcs, err := c.Lineup(context.Background(), "TWY")
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != "https://api.rtt.io/api/v1/json/search/TWY" {
		t.Fatalf("url = %q", gotURL)
	}
	if gotAuth == "" {
		t.Fatal("no basic-auth header sent")
	}
	want := []RTTService{
		{Headcode: "1A23", BookedDeparture: "1913", DestinationName: "London Paddington"},
		{Headcode: "2C31", BookedDeparture: "1919", DestinationName: "London Paddington"},
	}
	if len(svcs) != len(want) {
		t.Fatalf("got %d services, want %d: %+v", len(svcs), len(want), svcs)
	}
	for i := range want {
		if svcs[i] != want[i] {
			t.Errorf("service[%d] = %+v, want %+v", i, svcs[i], want[i])
		}
	}
}

func TestRTTLineupErrors(t *testing.T) {
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(401, `{"error":"auth"}`), nil
	})}
	if _, err := c.Lineup(context.Background(), "TWY"); err == nil {
		t.Fatal("expected non-200 to error")
	}
}

func TestRTTLineupEmptyServices(t *testing.T) {
	c := &RTTClient{user: "u", pass: "p", base: "https://api.rtt.io", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(200, `{"location":{"crs":"TWY"},"services":null}`), nil
	})}
	svcs, err := c.Lineup(context.Background(), "TWY")
	if err != nil || len(svcs) != 0 {
		t.Fatalf("want empty lineup, got %v, %v", svcs, err)
	}
}
