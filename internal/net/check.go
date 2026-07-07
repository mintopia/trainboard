package net

import (
	"context"
	"fmt"
	stdnet "net"
	"strings"
)

// Stage identifies which layer of the connectivity check failed. StageOK
// (the empty string) means every layer passed.
type Stage string

const (
	// StageAssoc is wpa_supplicant association to the configured SSID.
	StageAssoc Stage = "ASSOC"
	// StageDHCP is acquiring an IPv4 address on the interface.
	StageDHCP Stage = "DHCP"
	// StageDNS is resolving the configured probe hostname.
	StageDNS Stage = "DNS"
	// StageCaptive is detecting a captive portal intercepting HTTP.
	StageCaptive Stage = "CAPTIVE"
	// StageOK (the empty string) means every layer passed.
	StageOK Stage = ""
)

// Probes are the individually injectable layer checks; production wiring
// lives in NewCheck, fakes are supplied directly in tests via
// NewCheckWithProbes. Each returns nil on pass.
type Probes struct {
	Assoc   func(ctx context.Context) error
	DHCP    func(ctx context.Context) error
	DNS     func(ctx context.Context) error
	Captive func(ctx context.Context) error
}

// Check runs the layered connectivity probes in order.
type Check struct{ p Probes }

// NewCheckWithProbes builds a Check from caller-supplied probes; this is the
// test seam.
func NewCheckWithProbes(p Probes) *Check { return &Check{p: p} }

// NewCheck builds the production Check. Assoc and DHCP are checked via r
// (the Runner command seam); DNS resolves dnsHost with the standard
// resolver; Captive fetches captiveURL via the injected httpGet.
func NewCheck(r Runner, iface, dnsHost, captiveURL string, httpGet func(ctx context.Context, url string) (status int, body string, err error)) *Check {
	return &Check{p: Probes{
		Assoc:   assocProbe(r, iface),
		DHCP:    dhcpProbe(r, iface),
		DNS:     dnsProbe(dnsHost),
		Captive: captiveProbe(captiveURL, httpGet),
	}}
}

func assocProbe(r Runner, iface string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		out, err := r.Run(ctx, "wpa_cli", "-i", iface, "status")
		if err != nil {
			return fmt.Errorf("net: wpa_cli status: %w", err)
		}
		state := parseWpaStatus(out)["wpa_state"]
		if state != "COMPLETED" {
			return fmt.Errorf("net: wpa_state=%s, want COMPLETED", state)
		}
		return nil
	}
}

func dhcpProbe(r Runner, iface string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		out, err := r.Run(ctx, "ip", "-4", "addr", "show", "dev", iface)
		if err != nil {
			return fmt.Errorf("net: ip addr show dev %s: %w", iface, err)
		}
		if !strings.Contains(out, " inet ") {
			return fmt.Errorf("net: no IPv4 address assigned on %s", iface)
		}
		return nil
	}
}

func dnsProbe(dnsHost string) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		_, err := stdnet.DefaultResolver.LookupHost(ctx, dnsHost)
		return err
	}
}

func captiveProbe(captiveURL string, httpGet func(ctx context.Context, url string) (status int, body string, err error)) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		status, _, err := httpGet(ctx, captiveURL)
		if err != nil {
			return fmt.Errorf("net: captive check request: %w", err)
		}
		if status != 204 {
			return fmt.Errorf("net: captive portal detected (status %d)", status)
		}
		return nil
	}
}

// parseWpaStatus parses `wpa_cli status` KEY=VALUE output into a map.
func parseWpaStatus(out string) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		kv[k] = v
	}
	return kv
}

// Evaluate runs layers in order, returning the first failing Stage and its
// error, or (StageOK, nil) once every layer passes.
func (c *Check) Evaluate(ctx context.Context) (Stage, error) {
	if err := c.p.Assoc(ctx); err != nil {
		return StageAssoc, err
	}
	if err := c.p.DHCP(ctx); err != nil {
		return StageDHCP, err
	}
	if err := c.p.DNS(ctx); err != nil {
		return StageDNS, err
	}
	if err := c.p.Captive(ctx); err != nil {
		return StageCaptive, err
	}
	return StageOK, nil
}
