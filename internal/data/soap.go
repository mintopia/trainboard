package data

import (
	"bytes"
	"encoding/xml"
	"fmt"
)

// Darwin Lite (OpenLDBWS) endpoint and namespaces.
//
// tokenNamespace is authoritative (PLAN.md item 5) — a wrong Token namespace
// yields "unauthorized". ldbNamespace is the ldb12 (2021-11-01) request
// namespace; it is the #1 failure suspect and is CONFIRMED by the Task 8 live
// probe. If the probe faults on schema/namespace, correct ldbNamespace,
// soapAction, and the golden test in the same commit.
const (
	endpointURL    = "https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx"
	soapAction     = "http://thalesgroup.com/RTTI/2021-11-01/ldb/GetDepBoardWithDetails"
	ldbNamespace   = "http://thalesgroup.com/RTTI/2021-11-01/ldb/"
	tokenNamespace = "http://thalesgroup.com/RTTI/2013-11-28/Token/types"
)

// Request is the parameters for a GetDepBoardWithDetails call.
type Request struct {
	OriginCRS         string
	DestinationCRS    string // optional; server-side filterCrs (filterType=to)
	NumRows           int
	TimeWindowMinutes int
}

// buildEnvelope renders the SOAP request bytes. Values are XML-escaped. The
// element order and namespaces are pinned by TestBuildEnvelope*.
func buildEnvelope(token string, r Request) ([]byte, error) {
	if r.NumRows <= 0 || r.NumRows > 10 {
		r.NumRows = 10 // LDBWS WithDetails caps at 10; always request the max and trim client-side
	}
	var filter string
	if r.DestinationCRS != "" {
		filter = fmt.Sprintf(
			"<ldb:filterCrs>%s</ldb:filterCrs><ldb:filterType>to</ldb:filterType>",
			esc(r.DestinationCRS))
	}
	body := fmt.Sprintf(
		`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" `+
			`xmlns:typ="%s" xmlns:ldb="%s">`+
			`<soap:Header><typ:AccessToken><typ:TokenValue>%s</typ:TokenValue>`+
			`</typ:AccessToken></soap:Header>`+
			`<soap:Body><ldb:GetDepBoardWithDetailsRequest>`+
			`<ldb:numRows>%d</ldb:numRows>`+
			`<ldb:crs>%s</ldb:crs>`+
			`%s`+
			`<ldb:timeOffset>0</ldb:timeOffset>`+
			`<ldb:timeWindow>%d</ldb:timeWindow>`+
			`</ldb:GetDepBoardWithDetailsRequest></soap:Body></soap:Envelope>`,
		tokenNamespace, ldbNamespace,
		esc(token), r.NumRows, esc(r.OriginCRS), filter, r.TimeWindowMinutes)
	return []byte(body), nil
}

// esc XML-escapes a value for inclusion in element text.
func esc(s string) string {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return ""
	}
	return b.String()
}
