package net

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFileWrite records a single writeFile invocation.
type fakeFileWrite struct {
	path string
	data []byte
}

// fakeFileWriter is the injected writeFile test double: it records every
// call instead of touching the filesystem.
type fakeFileWriter struct {
	mu    sync.Mutex
	calls []fakeFileWrite
}

func (f *fakeFileWriter) write(path string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeFileWrite{path, append([]byte(nil), data...)})
	return nil
}

func (f *fakeFileWriter) writes() []fakeFileWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeFileWrite(nil), f.calls...)
}

// fakeSleeper is the injected sleep test double: it records call count
// instead of actually blocking.
type fakeSleeper struct {
	mu    sync.Mutex
	count int
}

func (s *fakeSleeper) sleep(time.Duration) {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
}

func (s *fakeSleeper) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func newTestMode2Driver(r Runner) (*mode2Driver, *fakeFileWriter, *fakeSleeper) {
	fw := &fakeFileWriter{}
	sl := &fakeSleeper{}
	d := newMode2Driver(r, "wlan0", "GB", fw.write, sl.sleep)
	return d, fw, sl
}

// (a) StartAP happy path issues exactly the expected argv sequence in order.
func TestMode2DriverStartAPHappyPathIssuesExactSequence(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\nmode=AP\n", nil)
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 1", "", nil)
	r.Script("ip addr flush dev wlan0", "", nil)
	r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

	d, _, _ := newTestMode2Driver(r)

	err := d.StartAP(context.Background(), APConfig{
		SSID:     "Trainboard-1234",
		Password: "testpass1",
		Addr:     "192.168.4.1/24",
	})
	if err != nil {
		t.Fatalf("StartAP() = %v, want nil", err)
	}

	want := []string{
		"wpa_cli -i wlan0 status",              // daemon check (already running)
		"wpa_cli -i wlan0 reconfigure",         // conf changed, reload
		"wpa_cli -i wlan0 select_network 1",    // AP is network id 1
		"wpa_cli -i wlan0 status",              // poll: satisfied first try
		"ip addr flush dev wlan0",              // clear existing addr
		"ip addr add 192.168.4.1/24 dev wlan0", // assign AP static addr
	}
	got := r.Calls()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() =\n%v\nwant\n%v", got, want)
	}
}

// (b) StartAP fails when status never reaches mode=AP after 10 polls.
func TestMode2DriverStartAPFailsWhenModeNeverAP(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 status", "wpa_state=SCANNING\n", nil)
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 1", "", nil)

	d, _, sl := newTestMode2Driver(r)

	err := d.StartAP(context.Background(), APConfig{
		SSID:     "Trainboard-1234",
		Password: "testpass1",
		Addr:     "192.168.4.1/24",
	})
	if err == nil {
		t.Fatal("StartAP() = nil, want error")
	}
	if !strings.Contains(err.Error(), "AP not active") {
		t.Fatalf("err = %v, want containing %q", err, "AP not active")
	}

	// daemon check (1) + reconfigure (1) + select_network (1) + 10 polls = 13
	// calls; the poll loop sleeps between attempts but not after the last.
	wantCalls := 13
	if got := len(r.Calls()); got != wantCalls {
		t.Fatalf("len(Calls()) = %d, want %d (calls: %v)", got, wantCalls, r.Calls())
	}
	if got := sl.calls(); got != pollAttempts-1 {
		t.Fatalf("sleep called %d times, want %d", got, pollAttempts-1)
	}
	for _, c := range r.Calls() {
		if strings.HasPrefix(c, "ip ") {
			t.Fatalf("Calls() contains %q, want no ip commands after AP never active", c)
		}
	}
}

// (c) AttemptSTA happy path ends with `dhclient -1 -v wlan0`.
func TestMode2DriverAttemptSTAHappyPathEndsWithDHClient(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -1 -v wlan0", "bound to 192.168.3.181\n", nil)

	d, fw, _ := newTestMode2Driver(r)

	err := d.AttemptSTA(context.Background(), STAConfig{SSID: "HomeWifi", PSK: "supersecretpsk"})
	if err != nil {
		t.Fatalf("AttemptSTA() = %v, want nil", err)
	}

	calls := r.Calls()
	if len(calls) == 0 || calls[len(calls)-1] != "dhclient -1 -v wlan0" {
		t.Fatalf("last call = %q, want %q (calls: %v)", calls[len(calls)-1], "dhclient -1 -v wlan0", calls)
	}
	writes := fw.writes()
	if len(writes) != 1 {
		t.Fatalf("writeFile called %d times, want 1", len(writes))
	}

	// The mode2 conf is a single file holding both the STA and AP network
	// blocks (switched between via select_network) — see mode2Driver's doc
	// comment. A live reconfigure during AttemptSTA must not drop the AP
	// block, or a subsequent StartAP would have nothing to select.
	conf := string(writes[0].data)
	for _, want := range []string{`id_str="sta"`, `id_str="ap"`, "mode=2"} {
		if !strings.Contains(conf, want) {
			t.Errorf("AttemptSTA conf missing %q; conf must retain the AP block:\n%s", want, conf)
		}
	}
}

// (d) AttemptSTA surfaces dhclient failure.
func TestMode2DriverAttemptSTASurfacesDHClientFailure(t *testing.T) {
	r := NewFakeRunner()
	r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
	r.Script("wpa_cli -i wlan0 select_network 0", "", nil)
	r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\n", nil)
	r.Script("dhclient -1 -v wlan0", "No DHCPOFFERS received.\n", errors.New("exit status 2"))

	d, _, _ := newTestMode2Driver(r)

	err := d.AttemptSTA(context.Background(), STAConfig{SSID: "HomeWifi", PSK: "supersecretpsk"})
	if err == nil {
		t.Fatal("AttemptSTA() = nil, want error")
	}
	if !strings.Contains(err.Error(), "dhclient") {
		t.Fatalf("err = %v, want containing %q", err, "dhclient")
	}
}

// (e) conf file written with both network blocks and correct ssid/psk
// substitution; a PSK containing a `"` is rejected rather than written
// (the wpa conf format has no escaping, so this is an injection guard, not
// just a cosmetic validation).
func TestMode2DriverConfWriteSubstitutionAndQuoteRejection(t *testing.T) {
	t.Run("both network blocks written with correct substitution", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\nmode=AP\n", nil)
		r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
		r.Script("wpa_cli -i wlan0 select_network 1", "", nil)
		r.Script("ip addr flush dev wlan0", "", nil)
		r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

		d, fw, _ := newTestMode2Driver(r)
		// AttemptSTA would normally populate this; set directly (white-box,
		// same package) so this test can assert both blocks in one write
		// without re-scripting the whole AttemptSTA sequence.
		d.sta = STAConfig{SSID: "HomeWifi", PSK: "clientpsk123"}

		err := d.StartAP(context.Background(), APConfig{
			SSID:     "Trainboard-ABCD",
			Password: "appassword1",
			Addr:     "192.168.4.1/24",
		})
		if err != nil {
			t.Fatalf("StartAP() = %v, want nil", err)
		}

		writes := fw.writes()
		if len(writes) != 1 {
			t.Fatalf("writeFile called %d times, want 1", len(writes))
		}
		if writes[0].path != wpaConfPath {
			t.Fatalf("write path = %q, want %q", writes[0].path, wpaConfPath)
		}
		conf := string(writes[0].data)

		for _, want := range []string{
			`id_str="sta"`,
			`ssid="HomeWifi"`,
			`psk="clientpsk123"`,
			`id_str="ap"`,
			`ssid="Trainboard-ABCD"`,
			`mode=2`,
			`frequency=2437`,
			`key_mgmt=WPA-PSK`,
			`psk="appassword1"`,
		} {
			if !strings.Contains(conf, want) {
				t.Errorf("conf missing %q; conf:\n%s", want, conf)
			}
		}
	})

	t.Run("PSK containing a quote is rejected, not written", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\nmode=AP\n", nil)

		d, fw, _ := newTestMode2Driver(r)

		err := d.StartAP(context.Background(), APConfig{
			SSID:     "Trainboard-ABCD",
			Password: `apppass"; disabled=0`,
			Addr:     "192.168.4.1/24",
		})
		if err == nil {
			t.Fatal("StartAP() = nil, want error for quote-containing password")
		}
		if len(fw.writes()) != 0 {
			t.Fatalf("writeFile called %d times, want 0 (quote must be rejected before write)", len(fw.writes()))
		}
	})

	t.Run("configured country is used instead of a hardcoded GB", func(t *testing.T) {
		r := NewFakeRunner()
		r.Script("wpa_cli -i wlan0 status", "wpa_state=COMPLETED\nmode=AP\n", nil)
		r.Script("wpa_cli -i wlan0 reconfigure", "", nil)
		r.Script("wpa_cli -i wlan0 select_network 1", "", nil)
		r.Script("ip addr flush dev wlan0", "", nil)
		r.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

		fw := &fakeFileWriter{}
		sl := &fakeSleeper{}
		d := newMode2Driver(r, "wlan0", "US", fw.write, sl.sleep)

		err := d.StartAP(context.Background(), APConfig{
			SSID:     "Trainboard-1234",
			Password: "testpass1",
			Addr:     "192.168.4.1/24",
		})
		if err != nil {
			t.Fatalf("StartAP() = %v, want nil", err)
		}

		conf := string(fw.writes()[0].data)
		if !strings.Contains(conf, "country=US") {
			t.Fatalf("conf missing %q; conf:\n%s", "country=US", conf)
		}
		if strings.Contains(conf, "country=GB") {
			t.Fatalf("conf hardcodes country=GB instead of the configured country; conf:\n%s", conf)
		}
	})
}

// sequencedStatusRunner is a small stateful Runner test double, distinct from
// FakeRunner (which is deliberately call-order-independent, matching one
// scripted response per command regardless of how many times it's called —
// see FakeRunner's doc comment). ensureDaemon's daemon-start branch needs the
// SAME "wpa_cli -i <iface> status" command to fail once (not running yet,
// taking the branch under test) then succeed (so a later poll can observe
// the daemon come up) — something FakeRunner's order-independent scripting
// cannot express. Every call, matched or not, is recorded in order so the
// test can assert the exact sequence including how many times the daemon
// was actually started.
type sequencedStatusRunner struct {
	Runner
	statusCmd string

	mu         sync.Mutex
	calls      []string
	statusHits int
}

func (s *sequencedStatusRunner) Run(ctx context.Context, argv ...string) (string, error) {
	cmd := strings.Join(argv, " ")

	s.mu.Lock()
	s.calls = append(s.calls, cmd)
	isStatus := cmd == s.statusCmd
	if isStatus {
		s.statusHits++
	}
	hit := s.statusHits
	s.mu.Unlock()

	if isStatus {
		if hit == 1 {
			return "", errors.New("exit status 1: wlan0: No such device")
		}
		return "wpa_state=COMPLETED\nmode=AP\n", nil
	}
	return s.Runner.Run(ctx, argv...)
}

func (s *sequencedStatusRunner) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// TestMode2DriverEnsureDaemonStartsWhenNotRunning covers ensureDaemon's
// daemon-start branch (wpa_cli status errors => start wpa_supplicant), which
// FakeRunner's order-independent scripting can never reach: a single script
// for "wpa_cli -i wlan0 status" applies to every call regardless of order, so
// it can never fail once then succeed on the next call, as this branch (and
// StartAP's subsequent pollStatus) requires. It proves the daemon-start
// command is issued exactly once and StartAP still proceeds to completion.
func TestMode2DriverEnsureDaemonStartsWhenNotRunning(t *testing.T) {
	base := NewFakeRunner()
	base.Script("wpa_supplicant -B -i wlan0 -c /run/trainboard-wpa.conf", "", nil)
	base.Script("wpa_cli -i wlan0 select_network 1", "", nil)
	base.Script("ip addr flush dev wlan0", "", nil)
	base.Script("ip addr add 192.168.4.1/24 dev wlan0", "", nil)

	r := &sequencedStatusRunner{Runner: base, statusCmd: "wpa_cli -i wlan0 status"}

	d, _, _ := newTestMode2Driver(r)

	err := d.StartAP(context.Background(), APConfig{
		SSID:     "Trainboard-1234",
		Password: "testpass1",
		Addr:     "192.168.4.1/24",
	})
	if err != nil {
		t.Fatalf("StartAP() = %v, want nil", err)
	}

	want := []string{
		"wpa_cli -i wlan0 status",                                // ensureDaemon: not running (errors)
		"wpa_supplicant -B -i wlan0 -c /run/trainboard-wpa.conf", // daemon-start branch
		"wpa_cli -i wlan0 select_network 1",
		"wpa_cli -i wlan0 status", // poll: satisfied first try
		"ip addr flush dev wlan0",
		"ip addr add 192.168.4.1/24 dev wlan0",
	}
	if got := r.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Calls() =\n%v\nwant\n%v", got, want)
	}

	starts := 0
	for _, c := range r.Calls() {
		if c == "wpa_supplicant -B -i wlan0 -c /run/trainboard-wpa.conf" {
			starts++
		}
		if strings.HasPrefix(c, "wpa_cli -i wlan0 reconfigure") {
			t.Fatalf("Calls() contains a reconfigure call, want the daemon-start branch (not the already-running branch): %v", r.Calls())
		}
	}
	if starts != 1 {
		t.Fatalf("wpa_supplicant -B started %d times, want exactly 1", starts)
	}
}
