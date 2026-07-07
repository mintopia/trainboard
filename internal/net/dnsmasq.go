package net

import (
	"context"
	"errors"
	"fmt"
)

// Dnsmasq controls the AP-side DHCP + wildcard DNS (production requires the
// dnsmasq package — installed by M3b's deploy step; M3a never runs it).
type Dnsmasq struct {
	r         Runner
	writeFile func(string, []byte) error
}

// NewDnsmasq returns a new Dnsmasq controller.
func NewDnsmasq(r Runner, writeFile func(string, []byte) error) *Dnsmasq {
	return &Dnsmasq{
		r:         r,
		writeFile: writeFile,
	}
}

// Start writes the dnsmasq configuration and starts the daemon.
func (d *Dnsmasq) Start(ctx context.Context) error {
	conf := `interface=wlan0
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,10m
dhcp-option=option:router,192.168.4.1
address=/#/192.168.4.1
no-resolv
`

	// Write configuration file
	if err := d.writeFile("/run/trainboard-dnsmasq.conf", []byte(conf)); err != nil {
		return fmt.Errorf("failed to write dnsmasq conf: %w", err)
	}

	// Start dnsmasq
	_, err := d.r.Run(ctx, "dnsmasq", "--conf-file=/run/trainboard-dnsmasq.conf", "--pid-file=/run/trainboard-dnsmasq.pid")
	if err != nil {
		return fmt.Errorf("failed to start dnsmasq: %w", err)
	}

	return nil
}

// Stop terminates the dnsmasq daemon.
func (d *Dnsmasq) Stop(ctx context.Context) error {
	// pkill tolerates failure (process may already be stopped)
	_, _ = d.r.Run(ctx, "pkill", "-F", "/run/trainboard-dnsmasq.pid")
	return nil
}

// exitCoder matches the standard library exec package's *ExitError type via
// its promoted ExitCode method, structurally, without this file importing
// that package directly — every OS side effect here goes through the
// Runner seam (ADR 0003), and Runner.Run's production implementation
// (ExecRunner, runner.go) is the only place that package may be imported.
type exitCoder interface{ ExitCode() int }

// Alive checks if dnsmasq is currently running. pkill -0 exiting non-zero
// because no process matched (an error satisfying exitCoder, i.e. a genuine
// *ExitError from the Runner) is the only "not alive" case; any other
// error (pkill missing, permission denied, ...) means the check itself
// failed and is returned to the caller rather than being silently mapped to
// "not running".
func (d *Dnsmasq) Alive(ctx context.Context) (bool, error) {
	_, err := d.r.Run(ctx, "pkill", "-0", "-F", "/run/trainboard-dnsmasq.pid")
	if err == nil {
		return true, nil
	}
	var ec exitCoder
	if errors.As(err, &ec) {
		return false, nil
	}
	return false, fmt.Errorf("could not check dnsmasq liveness: %w", err)
}
