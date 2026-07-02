package data

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBuildEnvelopeWithDestination(t *testing.T) {
	got, err := buildEnvelope("TOKEN-GUID", Request{
		OriginCRS: "PAD", DestinationCRS: "RDG", NumRows: 10, TimeWindowMinutes: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:typ="http://thalesgroup.com/RTTI/2013-11-28/Token/types" xmlns:ldb="http://thalesgroup.com/RTTI/2021-11-01/ldb/"><soap:Header><typ:AccessToken><typ:TokenValue>TOKEN-GUID</typ:TokenValue></typ:AccessToken></soap:Header><soap:Body><ldb:GetDepBoardWithDetailsRequest><ldb:numRows>10</ldb:numRows><ldb:crs>PAD</ldb:crs><ldb:filterCrs>RDG</ldb:filterCrs><ldb:filterType>to</ldb:filterType><ldb:timeOffset>0</ldb:timeOffset><ldb:timeWindow>120</ldb:timeWindow></ldb:GetDepBoardWithDetailsRequest></soap:Body></soap:Envelope>`
	if string(got) != want {
		t.Fatalf("envelope mismatch:\ngot:\n%s\nwant:\n%s", string(got), want)
	}
	var v any
	if err := xml.Unmarshal(got, &v); err != nil {
		t.Fatalf("envelope not well-formed XML: %v", err)
	}
}

func TestBuildEnvelopeOmitsFilterWhenNoDestination(t *testing.T) {
	got, err := buildEnvelope("T", Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	want := `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:typ="http://thalesgroup.com/RTTI/2013-11-28/Token/types" xmlns:ldb="http://thalesgroup.com/RTTI/2021-11-01/ldb/"><soap:Header><typ:AccessToken><typ:TokenValue>T</typ:TokenValue></typ:AccessToken></soap:Header><soap:Body><ldb:GetDepBoardWithDetailsRequest><ldb:numRows>10</ldb:numRows><ldb:crs>PAD</ldb:crs><ldb:timeOffset>0</ldb:timeOffset><ldb:timeWindow>120</ldb:timeWindow></ldb:GetDepBoardWithDetailsRequest></soap:Body></soap:Envelope>`
	if string(got) != want {
		t.Fatalf("envelope mismatch:\ngot:\n%s\nwant:\n%s", string(got), want)
	}
	var v any
	if err := xml.Unmarshal(got, &v); err != nil {
		t.Fatalf("envelope not well-formed XML: %v", err)
	}
}

func TestBuildEnvelopeEscapesToken(t *testing.T) {
	got, _ := buildEnvelope(`a&b<c`, Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if strings.Contains(string(got), "a&b<c") {
		t.Fatalf("token not XML-escaped:\n%s", got)
	}
	if !strings.Contains(string(got), "a&amp;b&lt;c") {
		t.Fatalf("token escaping wrong:\n%s", got)
	}
}

func TestBuildEnvelopeClampsNumRows(t *testing.T) {
	for _, n := range []int{0, -1, 11, 100} {
		got, err := buildEnvelope("T", Request{OriginCRS: "PAD", NumRows: n, TimeWindowMinutes: 120})
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !strings.Contains(string(got), "<ldb:numRows>10</ldb:numRows>") {
			t.Fatalf("NumRows %d not clamped to 10:\n%s", n, got)
		}
	}
}
