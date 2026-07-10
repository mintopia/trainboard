package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// rttBaseURL is the RealTime Trains public API root.
const rttBaseURL = "https://api.rtt.io"

// RTTService is one row of an RTT station lineup: only the fields headcode
// enrichment needs.
type RTTService struct {
	Headcode        string // trainIdentity, e.g. "1A23"
	BookedDeparture string // gbttBookedDeparture, "HHMM"
	DestinationName string // first destination's description
}

// RTTClient talks to the RealTime Trains JSON API with basic auth. It exists
// solely to enrich Darwin departures with headcodes (which public LDBWS does
// not carry) — it is NOT a second board source.
type RTTClient struct {
	user, pass string
	base       string
	http       httpDoer
}

// NewRTTClient returns an RTTClient with a 15s HTTP timeout.
func NewRTTClient(user, pass string) *RTTClient {
	return &RTTClient{user: user, pass: pass, base: rttBaseURL, http: &http.Client{Timeout: 15 * time.Second}}
}

// rttSearch mirrors the /json/search/{crs} response, local names only, just
// the fields Lineup projects.
type rttSearch struct {
	Services []struct {
		TrainIdentity  string `json:"trainIdentity"`
		LocationDetail struct {
			GBTTBookedDeparture string `json:"gbttBookedDeparture"`
			Destination         []struct {
				Description string `json:"description"`
			} `json:"destination"`
		} `json:"locationDetail"`
	} `json:"services"`
}

// Lineup fetches the station's current departure lineup. A "services": null
// response (no trains) is an empty, non-error lineup.
func (c *RTTClient) Lineup(ctx context.Context, crs string) ([]RTTService, error) {
	url := fmt.Sprintf("%s/api/v1/json/search/%s", c.base, crs)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("data: rtt request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("data: reading rtt response: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("data: rtt returned HTTP %d", res.StatusCode)
	}
	var sr rttSearch
	if err := json.Unmarshal(body, &sr); err != nil {
		return nil, fmt.Errorf("data: decoding rtt lineup: %w", err)
	}
	out := make([]RTTService, 0, len(sr.Services))
	for _, s := range sr.Services {
		svc := RTTService{Headcode: s.TrainIdentity, BookedDeparture: s.LocationDetail.GBTTBookedDeparture}
		if len(s.LocationDetail.Destination) > 0 {
			svc.DestinationName = s.LocationDetail.Destination[0].Description
		}
		out = append(out, svc)
	}
	return out, nil
}
