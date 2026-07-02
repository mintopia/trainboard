package data

import (
	"encoding/xml"
	"fmt"
)

// Wire structs mirror the LDBWS GetDepBoardWithDetails response. All xml tags
// use LOCAL NAMES ONLY (no namespace) so the response's version-dated ldb/lt
// namespaces cannot break decoding.

type wireLocation struct {
	LocationName string `xml:"location>locationName"`
	CRS          string `xml:"location>crs"`
}

type wireCallingPoint struct {
	LocationName string `xml:"locationName"`
	CRS          string `xml:"crs"`
	ST           string `xml:"st"`
	ET           string `xml:"et"`
	AT           string `xml:"at"`
}

type wireService struct {
	STD          string       `xml:"std"`
	ETD          string       `xml:"etd"`
	Platform     string       `xml:"platform"`
	Operator     string       `xml:"operator"`
	OperatorCode string       `xml:"operatorCode"`
	ServiceType  string       `xml:"serviceType"`
	Length       int          `xml:"length"`
	IsCancelled  bool         `xml:"isCancelled"`
	CancelReason string       `xml:"cancelReason"`
	DelayReason  string       `xml:"delayReason"`
	Origin       wireLocation `xml:"origin"`
	Destination  wireLocation `xml:"destination"`
	// subsequentCallingPoints > callingPointList > callingPoint (first list is
	// the through route; nested paths flatten the first list).
	CallingPoints []wireCallingPoint `xml:"subsequentCallingPoints>callingPointList>callingPoint"`
}

type wireBoard struct {
	GeneratedAt  string        `xml:"generatedAt"`
	LocationName string        `xml:"locationName"`
	CRS          string        `xml:"crs"`
	Messages     []string      `xml:"nrccMessages>message"`
	Services     []wireService `xml:"trainServices>service"`
	BusServices  []wireService `xml:"busServices>service"`
}

type wireEnvelope struct {
	Board *wireBoard `xml:"Body>GetDepBoardWithDetailsResponse>GetStationBoardResult"`
}

// parseBoard decodes a GetDepBoardWithDetails response body into a wireBoard.
func parseBoard(body []byte) (*wireBoard, error) {
	var env wireEnvelope
	if err := xml.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("data: decoding board: %w", err)
	}
	if env.Board == nil {
		return nil, fmt.Errorf("data: response has no GetStationBoardResult")
	}
	return env.Board, nil
}
