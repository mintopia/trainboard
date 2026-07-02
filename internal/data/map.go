package data

import (
	"fmt"
	"time"
)

// mapBoard converts a parsed wire board into the internal model. GeneratedAt is
// parsed as RFC3339; per-service When is left zero for Task 7 to reconstruct.
func mapBoard(wb *wireBoard) (*Board, error) {
	gen, err := time.Parse(time.RFC3339, wb.GeneratedAt)
	if err != nil {
		return nil, fmt.Errorf("data: parsing generatedAt %q: %w", wb.GeneratedAt, err)
	}
	b := &Board{
		GeneratedAt:  gen,
		LocationName: wb.LocationName,
		CRS:          wb.CRS,
	}
	for _, raw := range wb.Messages {
		if m := sanitizeMessage(raw); m != "" {
			b.Messages = append(b.Messages, m)
		}
	}
	for _, s := range wb.Services {
		b.Departures = append(b.Departures, mapService(s, "train"))
	}
	for _, s := range wb.BusServices {
		b.Departures = append(b.Departures, mapService(s, "bus"))
	}
	return b, nil
}

// mapService maps one wire service to a Departure, defaulting the service type.
func mapService(s wireService, defaultType string) Departure {
	st := s.ServiceType
	if st == "" {
		st = defaultType
	}
	cps := make([]CallingPoint, 0, len(s.CallingPoints))
	for _, cp := range s.CallingPoints {
		cps = append(cps, CallingPoint{
			Location:      Location{Name: cp.LocationName, CRS: cp.CRS},
			ScheduledTime: cp.ST,
			ExpectedTime:  cp.ET,
			ActualTime:    cp.AT,
		})
	}
	return Departure{
		ScheduledTime: s.STD,
		ExpectedTime:  s.ETD,
		Status:        DeriveStatus(s.ETD),
		Platform:      s.Platform,
		Operator:      s.Operator,
		OperatorCode:  s.OperatorCode,
		ServiceType:   st,
		Length:        s.Length,
		Origin:        Location{Name: s.Origin.LocationName, CRS: s.Origin.CRS},
		Destination:   Location{Name: s.Destination.LocationName, CRS: s.Destination.CRS},
		CallingPoints: cps,
		IsCancelled:   s.IsCancelled,
		CancelReason:  s.CancelReason,
		DelayReason:   s.DelayReason,
	}
}
