package net

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// fakeExitError satisfies this package's exitCoder interface (the same
// structural shape the standard library exec package's *ExitError type has
// via its promoted ExitCode method), so Alive()'s error-type discrimination
// can be tested without this test file importing that package itself —
// every OS side effect goes through the Runner seam (ADR 0003); only
// runner.go may exec.
type fakeExitError struct{ code int }

func (e fakeExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e fakeExitError) ExitCode() int { return e.code }

func TestDnsmasqStart(t *testing.T) {
	// Start: write conf → dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)

	var writePath string
	var writeData []byte
	writeFile := func(path string, data []byte) error {
		writePath = path
		writeData = data
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	err := d.Start(context.Background())
	if err != nil {
		t.Fatalf("Start() = %v, want nil", err)
	}

	// Verify conf was written to correct path
	if writePath != "/run/trainboard-dnsmasq.conf" {
		t.Fatalf("conf written to %q, want /run/trainboard-dnsmasq.conf", writePath)
	}

	// Verify conf content
	expectedConf := `interface=wlan0
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,10m
dhcp-option=option:router,192.168.4.1
address=/#/192.168.4.1
no-resolv
`
	if string(writeData) != expectedConf {
		t.Fatalf("conf content:\ngot:\n%s\nwant:\n%s", string(writeData), expectedConf)
	}

	// Verify dnsmasq command was called
	calls := r.Calls()
	if len(calls) == 0 {
		t.Fatal("dnsmasq command not executed")
	}
	if !strings.Contains(calls[0], "dnsmasq") {
		t.Fatalf("command %q doesn't contain 'dnsmasq'", calls[0])
	}
}

func TestDnsmasqStartCommandArgv(t *testing.T) {
	// Verify argv order and structure
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	calls := r.Calls()
	if len(calls) == 0 {
		t.Fatal("no commands executed")
	}

	// Check command has correct structure
	cmd := calls[0]
	if !strings.HasPrefix(cmd, "dnsmasq ") {
		t.Fatalf("command %q doesn't start with 'dnsmasq '", cmd)
	}
	if !strings.Contains(cmd, "--conf-file=/run/trainboard-dnsmasq.conf") {
		t.Fatalf("command %q missing --conf-file arg", cmd)
	}
	if !strings.Contains(cmd, "--pid-file=/run/trainboard-dnsmasq.pid") {
		t.Fatalf("command %q missing --pid-file arg", cmd)
	}
}

func TestDnsmasqStop(t *testing.T) {
	// Stop: pkill -F /run/trainboard-dnsmasq.pid (tolerate failure)
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)
	r.Script("pkill -F /run/trainboard-dnsmasq.pid", "", nil)

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	err := d.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() = %v, want nil", err)
	}

	calls := r.Calls()
	found := false
	for _, call := range calls {
		if strings.HasPrefix(call, "pkill -F /run/trainboard-dnsmasq.pid") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("pkill not called; calls: %v", calls)
	}
}

func TestDnsmasqStopTolerateFail(t *testing.T) {
	// Stop: tolerate failure from pkill
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)
	// pkill fails (process not running)
	r.Script("pkill -F /run/trainboard-dnsmasq.pid", "", fmt.Errorf("pkill: no matching processes"))

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	err := d.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() should tolerate pkill failure; got %v", err)
	}
}

func TestDnsmasqAliveTrue(t *testing.T) {
	// Alive: pkill -0 -F exit 0 → true
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)
	r.Script("pkill -0 -F /run/trainboard-dnsmasq.pid", "", nil)

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	alive, err := d.Alive(context.Background())
	if err != nil {
		t.Fatalf("Alive() = _, %v, want _, nil", err)
	}
	if !alive {
		t.Fatal("Alive() = false, want true when pkill -0 succeeds")
	}
}

func TestDnsmasqAliveFalse(t *testing.T) {
	// Alive: pkill -0 -F exits non-zero (an exitCoder error, meaning "no
	// process matched") → false, nil.
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)
	r.Script("pkill -0 -F /run/trainboard-dnsmasq.pid", "", fakeExitError{code: 1})

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	alive, err := d.Alive(context.Background())
	if err != nil {
		t.Fatalf("Alive() = _, %v, want _, nil", err)
	}
	if alive {
		t.Fatal("Alive() = true, want false when pkill -0 exits non-zero")
	}
}

func TestDnsmasqAliveCheckError(t *testing.T) {
	// Alive: pkill -0 -F fails for a reason other than "no such process"
	// (e.g. pkill binary missing, permission denied) → the caller must be
	// told the check itself failed, not that dnsmasq is down.
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=/run/trainboard-dnsmasq.conf --pid-file=/run/trainboard-dnsmasq.pid", "", nil)
	r.Script("pkill -0 -F /run/trainboard-dnsmasq.pid", "", fmt.Errorf("exec: \"pkill\": executable file not found in $PATH"))

	writeFile := func(_ string, _ []byte) error {
		return nil
	}

	d := NewDnsmasq(r, writeFile)
	_ = d.Start(context.Background())

	alive, err := d.Alive(context.Background())
	if err == nil {
		t.Fatal("Alive() = _, nil, want a non-nil error when the liveness check itself fails")
	}
	if alive {
		t.Fatal("Alive() = true, want false alongside the error")
	}
}
