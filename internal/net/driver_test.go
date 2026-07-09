package net

import (
	"context"
	"errors"
	"testing"
	"time"
)

// staTestRender/staTestWrite are the minimal renderConf/writeFile stand-ins
// these staAttempt-level tests need; the driver-specific conf shapes are
// covered by driver_mode2_test.go / driver_hostapd_test.go.
func staTestRender(STAConfig) ([]byte, error) { return []byte("conf"), nil }

func noopSleep(time.Duration) {}

// (a) staAttempt's dhclient invocation is daemon-mode: no `-1`, and carries
// the `-pf dhclientPidfile` flag (issue #46).
func TestStaAttemptDHClientArgvIsDaemonModeWithPidfile(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -v -pf "+dhclientPidfile+" wlan0", "bound to 192.168.3.181\n", nil)

	err := staAttempt(context.Background(), r, "wlan0", STAConfig{SSID: "HomeWifi", PSK: "psk"}, staTestRender, func(string, []byte) error { return nil }, pollAttempts, noopSleep)
	if err != nil {
		t.Fatalf("staAttempt() = %v, want nil", err)
	}

	calls := r.Calls()
	last := calls[len(calls)-1]
	want := "dhclient -v -pf " + dhclientPidfile + " wlan0"
	if last != want {
		t.Fatalf("last call = %q, want %q (calls: %v)", last, want, calls)
	}
	for _, c := range calls {
		if c == "dhclient -1 -v wlan0" {
			t.Fatalf("calls contain the old one-shot invocation %q, want daemon mode only: %v", c, calls)
		}
	}
}

// (b) staAttempt kills any stale dhclient daemon BEFORE it does anything
// else — the conf write, wpa_cli calls, or the new dhclient invocation. A
// fresh attempt must never let a previous attempt's renewer race the new
// lease. writeFile is made to fail here so the attempt aborts immediately
// after the kill, proving the kill happened first and nothing scripted
// after it (reconfigure/select_network/dhclient) was ever reached.
func TestStaAttemptKillsDHClientBeforeConfWrite(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", nil)

	var writeCalled bool
	writeFile := func(_ string, _ []byte) error {
		writeCalled = true
		return errors.New("disk full")
	}

	err := staAttempt(context.Background(), r, "wlan0", STAConfig{SSID: "HomeWifi", PSK: "psk"}, staTestRender, writeFile, pollAttempts, noopSleep)
	if err == nil {
		t.Fatal("staAttempt() = nil, want error (writeFile fails)")
	}
	if !writeCalled {
		t.Fatal("writeFile was never called")
	}

	calls := r.Calls()
	want := []string{"pkill -F " + dhclientPidfile + " dhclient"}
	if len(calls) != len(want) || calls[0] != want[0] {
		t.Fatalf("Calls() = %v, want exactly %v (the kill must run before, and nothing else runs after, the failed conf write)", calls, want)
	}
}

// (c) A failed kill (pkill's exit 1 — "no matching process", the common
// case when no daemon was ever started) must never abort the attempt: the
// rest of staAttempt's sequence still runs to completion.
func TestStaAttemptProceedsWhenDHClientKillFails(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", errors.New("exit status 1: no process found"))
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -v -pf "+dhclientPidfile+" wlan0", "bound to 192.168.3.181\n", nil)

	err := staAttempt(context.Background(), r, "wlan0", STAConfig{SSID: "HomeWifi", PSK: "psk"}, staTestRender, func(string, []byte) error { return nil }, pollAttempts, noopSleep)
	if err != nil {
		t.Fatalf("staAttempt() = %v, want nil (a failed pkill must be tolerated)", err)
	}

	calls := r.Calls()
	if len(calls) == 0 || calls[len(calls)-1] != "dhclient -v -pf "+dhclientPidfile+" wlan0" {
		t.Fatalf("last call = %q, want the dhclient invocation to still run (calls: %v)", calls[len(calls)-1], calls)
	}
}

// (d) killDHClient itself: issues exactly `pkill -F <dhclientPidfile>
// dhclient` — the trailing pattern is a name guard (review finding: -F alone
// is a pure pid selector, so a stale pidfile whose pid was recycled by an
// unrelated process would otherwise be killed) — and never panics or
// propagates a failure; it has no return value precisely so callers cannot
// accidentally treat "nothing to kill" as an error.
func TestKillDHClientIssuesPkillAndToleratesFailure(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", fakeExitError{code: 1})

	killDHClient(context.Background(), r, nil)

	calls := r.Calls()
	want := "pkill -F " + dhclientPidfile + " dhclient"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("Calls() = %v, want exactly [%q]", calls, want)
	}
}

// (e) an unexpected pkill failure (not the plain "no matching process" exit
// pkill's ExitCode reports) must still never propagate or panic — it is
// merely logged (review finding: killDHClient used to swallow every error
// silently, hiding a genuine failure such as a missing pkill binary).
func TestKillDHClientToleratesUnexpectedFailureWithoutPanicking(t *testing.T) {
	r := NewFakeRunner()
	r.Script("pkill -F "+dhclientPidfile+" dhclient", "", errors.New("exec: \"pkill\": executable file not found in $PATH"))

	killDHClient(context.Background(), r, nil) // must not panic

	calls := r.Calls()
	want := "pkill -F " + dhclientPidfile + " dhclient"
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("Calls() = %v, want exactly [%q]", calls, want)
	}
}

func TestDHClientPidfileConst(t *testing.T) {
	if dhclientPidfile != "/run/trainboard-dhclient.pid" {
		t.Fatalf("dhclientPidfile = %q, want %q", dhclientPidfile, "/run/trainboard-dhclient.pid")
	}
}
