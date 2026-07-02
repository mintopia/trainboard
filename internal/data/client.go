package data

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"time"
)

// httpDoer is the subset of *http.Client the data client needs (injectable
// for tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client talks to Darwin Lite with a fixed access token.
type Client struct {
	token string
	http  httpDoer
}

// NewClient returns a Client with a 15s HTTP timeout.
func NewClient(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// fetchRaw POSTs the SOAP envelope and returns the raw response body. It errors
// on transport failure, non-200 status, or a SOAP fault in the body.
func (c *Client) fetchRaw(ctx context.Context, r Request) ([]byte, error) {
	env, err := buildEnvelope(c.token, r)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(env))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", soapAction)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("data: darwin request failed: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("data: reading darwin response: %w", err)
	}
	if fault := extractFault(body); fault != "" {
		return nil, fmt.Errorf("data: darwin SOAP fault: %s", fault)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("data: darwin returned HTTP %d", res.StatusCode)
	}
	return body, nil
}

// extractFault returns the faultstring if body is a SOAP fault, else "".
func extractFault(body []byte) string {
	var env struct {
		Fault struct {
			String string `xml:"faultstring"`
		} `xml:"Body>Fault"`
	}
	if err := xml.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Fault.String
}
