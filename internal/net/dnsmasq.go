package net

import (
	"context"
	"fmt"
	"strings"
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

// Alive checks if dnsmasq is currently running.
func (d *Dnsmasq) Alive(ctx context.Context) (bool, error) {
	_, err := d.r.Run(ctx, "pkill", "-0", "-F", "/run/trainboard-dnsmasq.pid")
	if err != nil {
		// If pkill -0 fails, the process is not running
		// Check if it's an actual error or just a "not found" exit code
		if strings.Contains(err.Error(), "exit status") {
			// This is the expected case when process is not running
			return false, nil
		}
		// Some other error occurred
		return false, nil
	}
	// If pkill -0 succeeds, the process is running
	return true, nil
}
