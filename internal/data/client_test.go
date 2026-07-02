package data

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// doerFunc adapts a func to httpDoer.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func resp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestFetchRawPostsEnvelopeAndReturnsBody(t *testing.T) {
	var gotURL, gotAction, gotBody string
	c := &Client{token: "TOK", http: doerFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.String()
		gotAction = r.Header.Get("SOAPAction")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		return resp(200, "<ok/>"), nil
	})}
	body, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "<ok/>" {
		t.Fatalf("body = %q", body)
	}
	if gotURL != endpointURL {
		t.Errorf("url = %q, want %q", gotURL, endpointURL)
	}
	if !strings.Contains(gotAction, "GetDepBoardWithDetails") {
		t.Errorf("SOAPAction = %q", gotAction)
	}
	if !strings.Contains(gotBody, "<ldb:crs>PAD</ldb:crs>") {
		t.Errorf("posted body missing crs:\n%s", gotBody)
	}
}

func TestFetchRawErrorsOnFault(t *testing.T) {
	fault := `<soap:Envelope><soap:Body><soap:Fault>` +
		`<faultstring>Unauthorized</faultstring></soap:Fault></soap:Body></soap:Envelope>`
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(500, fault), nil
	})}
	_, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120})
	if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("expected fault error mentioning Unauthorized, got %v", err)
	}
}

func TestFetchRawErrorsOnNon200WithoutFault(t *testing.T) {
	c := &Client{token: "TOK", http: doerFunc(func(*http.Request) (*http.Response, error) {
		return resp(503, "service unavailable"), nil
	})}
	if _, err := c.fetchRaw(context.Background(), Request{OriginCRS: "PAD", NumRows: 10, TimeWindowMinutes: 120}); err == nil {
		t.Fatal("expected error on 503")
	}
}
