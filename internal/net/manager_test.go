package net

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// boolErr is one scripted (bool, error) return for fakeDriver.APActive.
type boolErr struct {
	active bool
	err    error
}

// fakeDriver is a scriptable apDriver test double: it records every call
// and returns per-method scripted errors (and, for APActive, a sequence of
// (bool, error) results so tests can exercise the toAP retry path).
type fakeDriver struct {
	mu    sync.Mutex
	calls []string

	startAPErr    error
	stopAPErr     error
	attemptSTAErr error

	// apActiveSeq is consumed front-to-back, one result per APActive call;
	// once exhausted, apActiveDefault/apActiveErr apply to further calls.
	apActiveSeq     []boolErr
	apActiveDefault bool
	apActiveErr     error
}

func (f *fakeDriver) StartAP(context.Context, APConfig) error {
	f.record("StartAP")
	return f.startAPErr
}

func (f *fakeDriver) StopAP(context.Context) error {
	f.record("StopAP")
	return f.stopAPErr
}

func (f *fakeDriver) AttemptSTA(context.Context, STAConfig) error {
	f.record("AttemptSTA")
	return f.attemptSTAErr
}

func (f *fakeDriver) APActive(context.Context) (bool, error) {
	f.record("APActive")
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.apActiveSeq) > 0 {
		be := f.apActiveSeq[0]
		f.apActiveSeq = f.apActiveSeq[1:]
		return be.active, be.err
	}
	return f.apActiveDefault, f.apActiveErr
}

func (f *fakeDriver) record(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *fakeDriver) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// newTestDnsmasq returns a *Dnsmasq wired to a FakeRunner that always
// succeeds (Start, Stop, and Alive all report healthy), plus the underlying
// FakeRunner so tests can override individual scripts.
func newTestDnsmasq() (*Dnsmasq, *FakeRunner) {
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=", "", nil)
	r.Script("pkill -F", "", nil)
	r.Script("pkill -0 -F", "", nil) // present (no error) => Alive() true
	d := NewDnsmasq(r, func(string, []byte) error { return nil })
	return d, r
}

// passingCheck returns a *Check whose every probe passes (StageOK).
func passingCheck() *Check {
	return NewCheckWithProbes(Probes{
		Assoc:   func(context.Context) error { return nil },
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})
}

func TestManagerToSTAHappyPathPublishesOnlineAndCallsOnOnline(t *testing.T) {
	driver := &fakeDriver{}
	dnsmasq, _ := newTestDnsmasq()
	onOnlineCalls := 0

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		Prereqs: func(context.Context) error { return nil },
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		OnOnline: func() {
			onOnlineCalls++
		},
	})

	if err := m.toSTA(context.Background()); err != nil {
		t.Fatalf("toSTA() err = %v, want nil", err)
	}

	got := m.Status()
	if got.State != ManagerOnline {
		t.Fatalf("State = %v, want ManagerOnline", got.State)
	}
	if got.Stage != StageOK {
		t.Fatalf("Stage = %q, want StageOK", got.Stage)
	}
	if onOnlineCalls != 1 {
		t.Fatalf("OnOnline called %d times, want 1", onOnlineCalls)
	}
	wantCalls := []string{"AttemptSTA"}
	if gotCalls := driver.Calls(); len(gotCalls) != len(wantCalls) || gotCalls[0] != wantCalls[0] {
		t.Fatalf("driver.Calls() = %v, want %v", gotCalls, wantCalls)
	}
}

func TestManagerToSTADHCPFailurePublishesStageDHCP(t *testing.T) {
	driver := &fakeDriver{}
	dnsmasq, _ := newTestDnsmasq()
	wantErr := errors.New("no lease")

	check := NewCheckWithProbes(Probes{
		Assoc:   func(context.Context) error { return nil },
		DHCP:    func(context.Context) error { return wantErr },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		Prereqs: func(context.Context) error { return nil },
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
	})

	err := m.toSTA(context.Background())
	if !errors.Is(err, wantErr) {
		t.Fatalf("toSTA() err = %v, want %v", err, wantErr)
	}

	got := m.Status()
	if got.State != ManagerSTAConnecting {
		t.Fatalf("State = %v, want ManagerSTAConnecting", got.State)
	}
	if got.Stage != StageDHCP {
		t.Fatalf("Stage = %q, want StageDHCP", got.Stage)
	}
	if got.LastSTAErr == "" {
		t.Fatal("LastSTAErr should be set")
	}
}

func TestManagerToSTANoWifiConfiguredReturnsSentinelWithoutDriver(t *testing.T) {
	driver := &fakeDriver{}
	dnsmasq, _ := newTestDnsmasq()
	prereqsCalled := false

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		Prereqs: func(context.Context) error {
			prereqsCalled = true
			return nil
		},
		STA: func() STAConfig { return STAConfig{} },
	})

	err := m.toSTA(context.Background())
	if !errors.Is(err, errNoWifiConfigured) {
		t.Fatalf("toSTA() err = %v, want errNoWifiConfigured", err)
	}
	if prereqsCalled {
		t.Fatal("Prereqs should not be called when no wifi is configured")
	}
	if calls := driver.Calls(); len(calls) != 0 {
		t.Fatalf("driver.Calls() = %v, want none", calls)
	}
}

func TestManagerToAPHappyPathPublishesAPFallback(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq, _ := newTestDnsmasq()

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Dnsmasq: dnsmasq,
		AP:      APConfig{SSID: "Trainboard-ABCD", Password: "hunter22", Addr: "192.168.4.1/24"},
	})

	if err := m.toAP(context.Background()); err != nil {
		t.Fatalf("toAP() err = %v, want nil", err)
	}

	got := m.Status()
	if got.State != ManagerAPFallback {
		t.Fatalf("State = %v, want ManagerAPFallback", got.State)
	}
	if got.Hotspot == nil {
		t.Fatal("Hotspot should be set")
	}
	if got.Hotspot.SSID != "Trainboard-ABCD" {
		t.Fatalf("Hotspot.SSID = %q, want Trainboard-ABCD", got.Hotspot.SSID)
	}
	if got.Hotspot.Addr != "192.168.4.1" {
		t.Fatalf("Hotspot.Addr = %q, want 192.168.4.1", got.Hotspot.Addr)
	}
}

func TestManagerToAPRetriesFullCycleWhenVerificationFirstFails(t *testing.T) {
	driver := &fakeDriver{
		apActiveSeq: []boolErr{
			{active: false},
			{active: true},
		},
	}
	dnsmasq, _ := newTestDnsmasq()

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Dnsmasq: dnsmasq,
		AP:      APConfig{SSID: "Trainboard-ABCD", Addr: "192.168.4.1/24"},
	})

	if err := m.toAP(context.Background()); err != nil {
		t.Fatalf("toAP() err = %v, want nil", err)
	}

	got := m.Status()
	if got.State != ManagerAPFallback {
		t.Fatalf("State = %v, want ManagerAPFallback", got.State)
	}
	if got.Hotspot == nil || got.Hotspot.SSID != "Trainboard-ABCD" {
		t.Fatalf("Hotspot = %+v, want SSID Trainboard-ABCD", got.Hotspot)
	}

	wantCalls := []string{"StartAP", "APActive", "StopAP", "StartAP", "APActive"}
	gotCalls := driver.Calls()
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("driver.Calls() = %v, want %v", gotCalls, wantCalls)
	}
	for i, c := range wantCalls {
		if gotCalls[i] != c {
			t.Fatalf("driver.Calls()[%d] = %q, want %q (full: %v)", i, gotCalls[i], c, gotCalls)
		}
	}
}

func TestManagerToAPFailsOutAfterTwoVerificationFailures(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: false}
	dnsmasq, _ := newTestDnsmasq()

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Dnsmasq: dnsmasq,
		AP:      APConfig{SSID: "Trainboard-ABCD", Addr: "192.168.4.1/24"},
	})

	err := m.toAP(context.Background())
	if err == nil {
		t.Fatal("toAP() err = nil, want error")
	}

	got := m.Status()
	if got.State == ManagerAPFallback {
		t.Fatal("State should not be ManagerAPFallback after two verification failures")
	}
	if got.Hotspot != nil {
		t.Fatalf("Hotspot = %+v, want nil", got.Hotspot)
	}

	wantCalls := []string{"StartAP", "APActive", "StopAP", "StartAP", "APActive"}
	gotCalls := driver.Calls()
	if len(gotCalls) != len(wantCalls) {
		t.Fatalf("driver.Calls() = %v, want %v", gotCalls, wantCalls)
	}
}
