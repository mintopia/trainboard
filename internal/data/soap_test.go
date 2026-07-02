package data

import (
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
	s := string(got)
	for _, want := range []string{
		`xmlns:typ="http://thalesgroup.com/RTTI/2013-11-28/Token/types"`,
		`xmlns:ldb="http://thalesgroup.com/RTTI/2021-11-01/ldb/"`,
		`<typ:AccessToken><typ:TokenValue>TOKEN-GUID</typ:TokenValue></typ:AccessToken>`,
		`<ldb:GetDepBoardWithDetailsRequest>`,
		`<ldb:numRows>10</ldb:numRows>`,
		`<ldb:crs>PAD</ldb:crs>`,
		`<ldb:filterCrs>RDG</ldb:filterCrs>`,
		`<ldb:filterType>to</ldb:filterType>`,
		`<ldb:timeWindow>120</ldb:timeWindow>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("envelope missing %q\n---\n%s", want, s)
		}
	}
}

func TestBuildEnvelopeOmitsFilterWhenNoDestination(t *testing.T) {
	got, _ := buildEnvelope("T", Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if strings.Contains(string(got), "filterCrs") || strings.Contains(string(got), "filterType") {
		t.Fatalf("filter elements must be omitted when no destination:\n%s", got)
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
