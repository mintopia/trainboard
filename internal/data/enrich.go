package data

import (
	"context"
	"log/slog"
	"strings"
)

// Fetcher is the board-fetch seam (mirrors runtime.Fetcher, redeclared here
// so this package doesn't import runtime).
type Fetcher interface {
	Fetch(ctx context.Context, r Request) (*Board, error)
}

// HeadcodeEnricher decorates a base fetcher, filling Departure.Headcode from
// an RTT station lineup. RTT is strictly best-effort: any lineup failure is
// logged and the Darwin board passes through with blank headcodes — the
// panel must never degrade because RTT is down.
type HeadcodeEnricher struct {
	Base Fetcher
	RTT  *RTTClient
	Log  *slog.Logger
}

// Fetch fetches the Darwin board, then annotates headcodes.
func (e *HeadcodeEnricher) Fetch(ctx context.Context, r Request) (*Board, error) {
	b, err := e.Base.Fetch(ctx, r)
	if err != nil {
		return b, err
	}
	lineup, rerr := e.RTT.Lineup(ctx, r.OriginCRS)
	if rerr != nil {
		e.Log.Warn("rtt lineup failed; headcodes left blank", "err", rerr.Error())
		return b, nil
	}
	MatchHeadcodes(b, lineup)
	return b, nil
}

// MatchHeadcodes fills Headcode for each departure with an unambiguous RTT
// counterpart: same booked departure time (Darwin "HH:MM" vs RTT "HHMM"),
// ties broken by case-insensitive destination name. A departure that stays
// ambiguous keeps a blank headcode — a wrong headcode is worse than none.
func MatchHeadcodes(b *Board, lineup []RTTService) {
	for i := range b.Departures {
		d := &b.Departures[i]
		key := strings.ReplaceAll(d.ScheduledTime, ":", "")
		var cands []RTTService
		for _, s := range lineup {
			if s.BookedDeparture == key {
				cands = append(cands, s)
			}
		}
		if len(cands) > 1 {
			var narrowed []RTTService
			for _, s := range cands {
				if strings.EqualFold(s.DestinationName, d.Destination.Name) {
					narrowed = append(narrowed, s)
				}
			}
			cands = narrowed
		}
		if len(cands) == 1 {
			d.Headcode = cands[0].Headcode
		}
	}
}
