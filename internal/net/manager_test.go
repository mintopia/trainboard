package net

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

	// stopAPBlock, if non-nil, is received from inside StopAP before it
	// returns — lets Task 9's run-loop tests pause mid-retry to
	// deterministically observe the transient ManagerSTARetry status.
	stopAPBlock chan struct{}
}

func (f *fakeDriver) StartAP(context.Context, APConfig) error {
	f.record("StartAP")
	return f.startAPErr
}

func (f *fakeDriver) StopAP(context.Context) error {
	f.record("StopAP")
	if f.stopAPBlock != nil {
		<-f.stopAPBlock
	}
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
	if got.Hotspot.Password != "hunter22" {
		t.Fatalf("Hotspot.Password = %q, want hunter22", got.Hotspot.Password)
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

// --- Task 9: Run loop test infrastructure -----------------------------------

// fakeAfter is the ManagerDeps.After test double: each call records a fresh
// unbuffered channel so a test can wait for Run to reach a particular wait
// point (nth), then hand-fire it (fire). A send on an unbuffered channel
// blocks until Run's select receives it, so firing is itself a
// synchronization point — no extra sleeps are needed once it returns.
type fakeAfter struct {
	mu    sync.Mutex
	calls []chan time.Time
}

func (f *fakeAfter) After(time.Duration) <-chan time.Time {
	ch := make(chan time.Time)
	f.mu.Lock()
	f.calls = append(f.calls, ch)
	f.mu.Unlock()
	return ch
}

// nth polls until the n-th (1-based) After() call has been made, then
// returns its channel.
func (f *fakeAfter) nth(t *testing.T, n int) chan time.Time {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		ready := len(f.calls) >= n
		var ch chan time.Time
		if ready {
			ch = f.calls[n-1]
		}
		f.mu.Unlock()
		if ready {
			return ch
		}
		if time.Now().After(deadline) {
			t.Fatalf("fakeAfter: timed out waiting for After() call #%d", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// fire sends a value into ch (a fakeAfter channel), timing out the test if
// Run's select never receives it.
func fire(t *testing.T, ch chan time.Time) {
	t.Helper()
	select {
	case ch <- time.Time{}:
	case <-time.After(2 * time.Second):
		t.Fatal("fire: timed out sending to After() channel")
	}
}

// fakeClock is the ManagerDeps.Now test double: a settable, mutex-guarded
// point in time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{now: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// runnerRecordingInto wraps a Runner and additionally records recognisable
// Dnsmasq step names into driver's call log, so run-loop tests can assert a
// single unified ordering across the Driver and Dnsmasq fakes (e.g.
// "Dnsmasq.Stop before the retry's AttemptSTA").
type runnerRecordingInto struct {
	Runner
	driver *fakeDriver
}

func (r runnerRecordingInto) Run(ctx context.Context, argv ...string) (string, error) {
	out, err := r.Runner.Run(ctx, argv...)
	if len(argv) > 0 {
		switch {
		case argv[0] == "dnsmasq":
			r.driver.record("Dnsmasq.Start")
		case argv[0] == "pkill" && len(argv) > 1 && argv[1] == "-0":
			r.driver.record("Dnsmasq.Alive")
		case argv[0] == "pkill":
			r.driver.record("Dnsmasq.Stop")
		}
	}
	return out, err
}

// newTestDnsmasqRecordingInto is newTestDnsmasq, but Dnsmasq's own runner
// calls also land in driver.Calls() for unified cross-fake ordering
// assertions.
func newTestDnsmasqRecordingInto(driver *fakeDriver) *Dnsmasq {
	r := NewFakeRunner()
	r.Script("dnsmasq --conf-file=", "", nil)
	r.Script("pkill -F", "", nil)
	r.Script("pkill -0 -F", "", nil)
	return NewDnsmasq(runnerRecordingInto{Runner: r, driver: driver}, func(string, []byte) error { return nil })
}

// waitForState polls m.Status() (a lock-free read) until pred is true or the
// deadline passes.
func waitForState(t *testing.T, m *Manager, pred func(Status) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if pred(m.Status()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for status; last = %+v", m.Status())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForCond polls cond (which must do its own synchronized reads, e.g. via
// sync/atomic) until it reports true or the deadline passes.
func waitForCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(time.Millisecond)
	}
}

// waitForCall polls until driver.Calls() contains name at least once.
func waitForCall(t *testing.T, driver *fakeDriver, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, c := range driver.Calls() {
			if c == name {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for driver call %q; got %v", name, driver.Calls())
		}
		time.Sleep(time.Millisecond)
	}
}

// runManagerAsync starts m.Run(ctx) in a goroutine, returning a channel that
// receives its result.
func runManagerAsync(ctx context.Context, m *Manager) <-chan error {
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	return done
}

// waitRunDone waits for Run's goroutine to return, failing the test if it
// hangs.
func waitRunDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
		return nil
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func lastIndexOf(s []string, v string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == v {
			return i
		}
	}
	return -1
}

// --- Task 9: Run loop scenarios ---------------------------------------------

// (a) boot with no wifi configured falls back to a verified AP; Status ends
// ManagerAPFallback.
func TestManagerRunBootNoWifiFallsBackToAP(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{} }, // no wifi configured
		AP:      APConfig{SSID: "Trainboard-A", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })
	if got := m.Status(); got.Hotspot == nil || got.Hotspot.SSID != "Trainboard-A" {
		t.Fatalf("Hotspot = %+v, want SSID Trainboard-A", got.Hotspot)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (b) a full fallback-retry-success cycle: APFallback -> fire the 5m timer
// -> ManagerSTARetry observed (Hotspot nil during the attempt) -> probes
// pass -> Online, OnOnline called, dnsmasq stopped before the retry attempt.
func TestManagerRunFullFallbackRetrySuccessCycle(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true, stopAPBlock: make(chan struct{})}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))

	var assocCalls int
	check := NewCheckWithProbes(Probes{
		Assoc: func(context.Context) error {
			assocCalls++
			if assocCalls == 1 {
				return errors.New("boot: not associated yet")
			}
			return nil
		},
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	var onOnlineCalls atomic.Int32
	m := NewManager(ManagerDeps{
		Driver:   driver,
		Check:    check,
		Dnsmasq:  dnsmasq,
		STA:      func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:       APConfig{SSID: "Trainboard-B", Addr: "192.168.4.1/24"},
		OnOnline: func() { onOnlineCalls.Add(1) },
		Now:      clock.Now,
		After:    after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	timer := after.nth(t, 1)
	fire(t, timer)

	// StopAP blocks (stopAPBlock), so by the time it's recorded the
	// preceding ManagerSTARetry publish is guaranteed visible.
	waitForCall(t, driver, "StopAP")
	if got := m.Status(); got.State != ManagerSTARetry {
		t.Fatalf("State = %v, want ManagerSTARetry", got.State)
	} else if got.Hotspot != nil {
		t.Fatalf("Hotspot = %+v, want nil during retry", got.Hotspot)
	}

	close(driver.stopAPBlock)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerOnline })
	waitForCond(t, func() bool { return onOnlineCalls.Load() == 1 })

	calls := driver.Calls()
	idxStop, idxStopAP, idxAttempt := indexOf(calls, "Dnsmasq.Stop"), indexOf(calls, "StopAP"), lastIndexOf(calls, "AttemptSTA")
	if idxStop < 0 || idxStopAP < 0 || idxAttempt < 0 || idxStop >= idxStopAP || idxStopAP >= idxAttempt {
		t.Fatalf("want Dnsmasq.Stop < StopAP < AttemptSTA(retry) order, got %v", calls)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (c) a retry failure restores the AP: probes never pass, so the retry
// attempt fails and toAP is called again, restoring ManagerAPFallback with
// LastSTAErr set from the STA failure.
func TestManagerRunRetryFailureRestoresAP(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))

	check := NewCheckWithProbes(Probes{
		Assoc:   func(context.Context) error { return errors.New("never associates") },
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-C", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	timer := after.nth(t, 1)
	fire(t, timer)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback && s.LastSTAErr != "" })
	if got := m.Status(); got.Hotspot == nil || got.Hotspot.SSID != "Trainboard-C" {
		t.Fatalf("Hotspot = %+v, want SSID Trainboard-C restored", got.Hotspot)
	}

	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	default:
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (d) suppression: NoteProvisioning just before the timer fires skips the
// retry cycle entirely — no StopAP call, and Run loops back to wait again.
func TestManagerRunSuppressesRetryDuringActiveProvisioning(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(1_700_000_000, 0))

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{} }, // no wifi -> straight to AP
		AP:      APConfig{SSID: "Trainboard-D", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	timer := after.nth(t, 1)
	m.NoteProvisioning(clock.Now())
	fire(t, timer)

	// Suppressed: Run loops back and re-waits rather than tearing the AP
	// down, i.e. it makes a second After() call.
	after.nth(t, 2)

	for _, c := range driver.Calls() {
		if c == "StopAP" {
			t.Fatalf("StopAP called despite suppression: %v", driver.Calls())
		}
	}
	if got := m.Status(); got.State != ManagerAPFallback || got.Hotspot == nil {
		t.Fatalf("Status = %+v, want unchanged ManagerAPFallback", got)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (e) RetryNow bypasses both the 5m wait and the suppression window.
func TestManagerRunRetryNowBypassesWaitAndSuppression(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(1_700_000_000, 0))

	var assocCalls int
	check := NewCheckWithProbes(Probes{
		Assoc: func(context.Context) error {
			assocCalls++
			if assocCalls == 1 {
				return errors.New("boot: not associated yet")
			}
			return nil
		},
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-E", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	after.nth(t, 1)                 // Run is now blocked selecting on the 5m timer / retry chan.
	m.NoteProvisioning(clock.Now()) // would suppress a scheduled wake...
	m.RetryNow()                    // ...but RetryNow bypasses it.

	waitForState(t, m, func(s Status) bool { return s.State == ManagerOnline })

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (f) online watch degradation: Online -> a probe failure on the 30s
// re-check (and the ensuing toSTA reattempt) ends in ManagerAPFallback.
func TestManagerRunOnlineWatchDegradesToAPFallback(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))

	var assocCalls int
	check := NewCheckWithProbes(Probes{
		Assoc: func(context.Context) error {
			assocCalls++
			if assocCalls <= 1 {
				return nil
			}
			return errors.New("association lost")
		},
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   check,
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-F", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerOnline })

	timer := after.nth(t, 1)
	fire(t, timer)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })
	if got := m.Status(); got.Hotspot == nil || got.Hotspot.SSID != "Trainboard-F" {
		t.Fatalf("Hotspot = %+v, want SSID Trainboard-F", got.Hotspot)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// (g) escalation: AP verification permanently fails (both toAP attempts), so
// Run returns a non-nil error instead of publishing APFallback.
func TestManagerRunEscalatesWhenAPVerificationPermanentlyFails(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: false}
	dnsmasq := newTestDnsmasqRecordingInto(driver)

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{} }, // no wifi -> straight to AP
		AP:      APConfig{SSID: "Trainboard-G", Addr: "192.168.4.1/24"},
		After:   (&fakeAfter{}).After,
		Now:     time.Now,
	})

	err := m.Run(context.Background())
	if err == nil {
		t.Fatal("Run() err = nil, want non-nil (escalation)")
	}
	if got := m.Status(); got.State == ManagerAPFallback {
		t.Fatalf("State = %v, want not APFallback after escalation", got.State)
	}
}

// (h) Beat is called at least once per loop iteration.
func TestManagerRunCallsBeatEveryLoopIteration(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))

	var beatMu sync.Mutex
	beatCalls := 0

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-H", Addr: "192.168.4.1/24"},
		Beat: func() {
			beatMu.Lock()
			beatCalls++
			beatMu.Unlock()
		},
		Now:   clock.Now,
		After: after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerOnline })

	timer1 := after.nth(t, 1)
	fire(t, timer1)

	after.nth(t, 2) // Run has looped back and is waiting again.

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}

	beatMu.Lock()
	got := beatCalls
	beatMu.Unlock()
	if got < 3 {
		t.Fatalf("Beat called %d times, want at least 3 (boot + two online-wait iterations)", got)
	}
}
