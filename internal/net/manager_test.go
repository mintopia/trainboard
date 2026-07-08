package net

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

// fakeDriver is a scriptable Driver test double: it records every call
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

	// attemptSTABlockUntilCtxDone, if true, makes AttemptSTA block on
	// <-ctx.Done() and return ctx.Err() — simulating a real driver whose
	// underlying command is still in flight when ctx is cancelled.
	attemptSTABlockUntilCtxDone bool

	// attemptSTAHook, if non-nil, runs at the start of each AttemptSTA call —
	// lets the fast-retry tests advance a fake clock to model a slow (or
	// instant) STA attempt without a real wall-clock wait.
	attemptSTAHook func()
}

func (f *fakeDriver) StartAP(ctx context.Context, _ APConfig) error {
	f.record("StartAP")
	// Mirrors a real driver (exec.CommandContext): a cancelled ctx fails the
	// call immediately rather than proceeding as if nothing happened.
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.startAPErr
}

func (f *fakeDriver) StopAP(context.Context) error {
	f.record("StopAP")
	if f.stopAPBlock != nil {
		<-f.stopAPBlock
	}
	return f.stopAPErr
}

func (f *fakeDriver) AttemptSTA(ctx context.Context, _ STAConfig) error {
	f.record("AttemptSTA")
	if f.attemptSTAHook != nil {
		f.attemptSTAHook()
	}
	if f.attemptSTABlockUntilCtxDone {
		<-ctx.Done()
		return ctx.Err()
	}
	return f.attemptSTAErr
}

func (f *fakeDriver) APActive(ctx context.Context) (bool, error) {
	f.record("APActive")
	if err := ctx.Err(); err != nil {
		return false, err
	}
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

// TestManagerToSTAPrereqsFailureSetsRadioBlockedThenClears pins the E05
// on-glass contract: a Prereqs failure (rfkill soft-block / missing regulatory
// domain) publishes RadioBlocked=true so the render loop can raise E05, and a
// subsequent successful transition attempt clears it (each publish is a fresh
// immutable Status, so the flag must not survive into Online).
func TestManagerToSTAPrereqsFailureSetsRadioBlockedThenClears(t *testing.T) {
	driver := &fakeDriver{}
	dnsmasq, _ := newTestDnsmasq()
	prereqErr := errors.New("rfkill soft blocked")
	failPrereqs := true

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		Prereqs: func(context.Context) error {
			if failPrereqs {
				return prereqErr
			}
			return nil
		},
		STA:      func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		OnOnline: func() {},
	})

	if err := m.toSTA(context.Background()); !errors.Is(err, prereqErr) {
		t.Fatalf("toSTA() err = %v, want %v", err, prereqErr)
	}
	got := m.Status()
	if got.State != ManagerSTAConnecting {
		t.Fatalf("State = %v, want ManagerSTAConnecting", got.State)
	}
	if !got.RadioBlocked {
		t.Fatal("RadioBlocked = false, want true after a Prereqs failure")
	}

	// A subsequent successful transition attempt must clear RadioBlocked.
	failPrereqs = false
	if err := m.toSTA(context.Background()); err != nil {
		t.Fatalf("toSTA() err = %v, want nil on the recovery attempt", err)
	}
	got = m.Status()
	if got.State != ManagerOnline {
		t.Fatalf("State = %v, want ManagerOnline", got.State)
	}
	if got.RadioBlocked {
		t.Fatal("RadioBlocked = true, want false after a successful transition")
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

// withShrunkSTAAttemptBound temporarily shrinks the package-level
// staAttemptBound so a test can exercise a real (deadline-based) timeout
// without waiting out the production 45s, restoring it on cleanup.
func withShrunkSTAAttemptBound(t *testing.T, d time.Duration) {
	t.Helper()
	orig := staAttemptBound
	staAttemptBound = d
	t.Cleanup(func() { staAttemptBound = orig })
}

// TestManagerToSTABoundsAttemptAndClassifiesTimeoutAsOrdinaryFailure is the
// failing-first TDD test for the "bounded STA attempt" review finding: a
// fakeDriver.AttemptSTA that only returns once ITS ctx dies must not be able
// to hang toSTA past staAttemptBound, and the resulting error must be
// timeout-flavoured while leaving the parent (caller's) ctx untouched.
func TestManagerToSTABoundsAttemptAndClassifiesTimeoutAsOrdinaryFailure(t *testing.T) {
	withShrunkSTAAttemptBound(t, 10*time.Millisecond)

	driver := &fakeDriver{attemptSTABlockUntilCtxDone: true}
	dnsmasq, _ := newTestDnsmasq()

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		Prereqs: func(context.Context) error { return nil },
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
	})

	parentCtx := context.Background()
	start := time.Now()
	err := m.toSTA(parentCtx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("toSTA took %v, want bounded by the shrunk staAttemptBound", elapsed)
	}
	if err == nil {
		t.Fatal("toSTA() err = nil, want a timeout-flavoured error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("toSTA() err = %v, want context.DeadlineExceeded (timeout, not cancellation)", err)
	}
	if parentCtx.Err() != nil {
		t.Fatalf("parent ctx.Err() = %v, want nil — only toSTA's internal attempt ctx should have timed out", parentCtx.Err())
	}
}

// TestManagerRunOnlineWaitBoundsCheckEvaluateAndClassifiesTimeoutAsDegradation
// is the failing-first TDD test for "bound the online recheck": runOnlineWait
// calls m.d.Check.Evaluate directly (not through toSTA), so a probe that only
// returns once ITS ctx dies must not be able to hang the online-watch phase
// forever — it must be bounded by staAttemptBound exactly as toSTA's own
// AttemptSTA/Evaluate calls are, and the resulting timeout must be treated as
// an ordinary degradation (proceeding to a full toSTA reattempt), never
// mistaken for parent ctx cancellation.
func TestManagerRunOnlineWaitBoundsCheckEvaluateAndClassifiesTimeoutAsDegradation(t *testing.T) {
	withShrunkSTAAttemptBound(t, 10*time.Millisecond)

	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}

	// blockingCheck's Assoc probe hangs until ITS ctx (the bounded child
	// runOnlineWait must construct) is done — mirroring a real probe (e.g. a
	// hanging wpa_cli invocation) that only returns on ctx death.
	blockingCheck := NewCheckWithProbes(Probes{
		Assoc: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		DHCP:    func(context.Context) error { return nil },
		DNS:     func(context.Context) error { return nil },
		Captive: func(context.Context) error { return nil },
	})

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   blockingCheck,
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-Z", Addr: "192.168.4.1/24"},
		After:   after.After,
		Now:     time.Now,
	})

	type result struct {
		next      managerPhase
		cancelled bool
		err       error
	}
	resultCh := make(chan result, 1)
	parentCtx := context.Background()
	go func() {
		next, cancelled, err := m.runOnlineWait(parentCtx)
		resultCh <- result{next, cancelled, err}
	}()

	// Let runOnlineWait past its onlineRecheckInterval wait so it reaches the
	// Check.Evaluate call under test.
	fire(t, after.nth(t, 1))

	select {
	case res := <-resultCh:
		if res.cancelled {
			t.Fatal("runOnlineWait misclassified a bounded Check.Evaluate timeout as ctx cancellation")
		}
		if res.err != nil {
			t.Fatalf("runOnlineWait() err = %v, want nil (the toSTA reattempt's own AttemptSTA succeeds; only Check.Evaluate hangs)", res.err)
		}
		// blockingCheck also fails toSTA's own Evaluate call, so the
		// reattempt fails too and Run falls all the way back to AP.
		if res.next != phaseAPWait {
			t.Fatalf("next phase = %v, want phaseAPWait (STA reattempt also failed via the same blocking probe)", res.next)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runOnlineWait did not return within 2s — Check.Evaluate was not bounded by staAttemptBound")
	}
	if parentCtx.Err() != nil {
		t.Fatal("parent ctx must remain uncancelled — only the internal recheck ctx should have timed out")
	}
	if got := m.Status().State; got != ManagerAPFallback {
		t.Fatalf("Status().State = %v, want ManagerAPFallback", got)
	}
}

// TestManagerRunSTAAttemptTimeoutFallsBackToAPNotCancellation confirms Run
// treats a bounded-attempt timeout as an ordinary STA failure (falling back
// to AP), never confusing it with the parent-ctx-cancellation path that
// d23a7d7 already covers (TestManagerRunCtxCancelMidSTAAttemptReturnsCleanly,
// which must stay green and is deliberately left untouched by this fix).
func TestManagerRunSTAAttemptTimeoutFallsBackToAPNotCancellation(t *testing.T) {
	withShrunkSTAAttemptBound(t, 10*time.Millisecond)

	driver := &fakeDriver{attemptSTABlockUntilCtxDone: true, apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-T", Addr: "192.168.4.1/24"},
		After:   (&fakeAfter{}).After,
		Now:     time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	// If the timeout were misclassified as parent-ctx cancellation, Run
	// would return nil almost immediately instead of ever publishing
	// APFallback; waitForState's own deadline catches that.
	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
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

// Advance moves the fake clock forward by d, mutex-guarded — used to
// simulate real elapsed time for waitAPFallback's Now-tracked remaining
// budget without waiting it out.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
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

	// Boot consumes TWO STA attempts now: the initial one plus the
	// once-per-process instant-failure fast retry (issue #47), which fires
	// because the fake clock never advances during an attempt (0s < the 2s
	// threshold). Both must fail for boot to fall back to AP, so the check
	// only starts passing on the third association (the real AP-fallback
	// retry).
	var assocCalls int
	check := NewCheckWithProbes(Probes{
		Assoc: func(context.Context) error {
			assocCalls++
			if assocCalls <= 2 {
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

	// waitAPFallback wakes on a 30s heartbeat cadence well before the full
	// 5-minute budget — advance the fake clock past the budget so this wake
	// is recognised as the real retry timeout, not another heartbeat tick.
	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
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

	// See TestManagerRunFullFallbackRetrySuccessCycle: advance past the
	// heartbeat-tracked budget so this wake is the real retry timeout.
	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
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

	// See TestManagerRunFullFallbackRetrySuccessCycle: advance past the
	// heartbeat-tracked budget so this wake is the real retry timeout, then
	// note provisioning against that same (advanced) "now" so it's still
	// well inside the suppression window.
	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
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

	// As in TestManagerRunFullFallbackRetrySuccessCycle, boot burns two STA
	// attempts (initial + the once-per-process instant-failure fast retry, #47)
	// because the frozen fake clock makes every attempt look instant; both must
	// fail so boot falls back to AP, leaving the RetryNow-triggered attempt
	// (association #3) to succeed.
	var assocCalls int
	check := NewCheckWithProbes(Probes{
		Assoc: func(context.Context) error {
			assocCalls++
			if assocCalls <= 2 {
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

// (i) ctx cancellation arriving while a driver call is in flight must still
// produce a clean nil return, never escalation — even though the in-flight
// call (and the subsequent AP fallback attempt it triggers) both fail with
// context errors once ctx is done.
func TestManagerRunCtxCancelMidSTAAttemptReturnsCleanly(t *testing.T) {
	driver := &fakeDriver{attemptSTABlockUntilCtxDone: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-I", Addr: "192.168.4.1/24"},
		After:   (&fakeAfter{}).After,
		Now:     time.Now,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	// Wait until Run is blocked inside AttemptSTA before cancelling, so the
	// driver call genuinely races the ctx cancellation.
	waitForCall(t, driver, "AttemptSTA")
	cancel()

	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil (ctx cancel mid-transition must be a clean shutdown, not escalation)", err)
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

// --- watchdog reboot loop fix: waitAPFallback heartbeats -------------------

// TestManagerWaitAPFallbackBeatsOnHeartbeatWithoutConsumingRetryBudget is the
// failing-first TDD test for the AP-fallback watchdog reboot loop: a manager
// parked in waitAPFallback is healthy and must keep beating well inside the
// watchdog's deadline, without that heartbeat cadence itself triggering the
// 5-minute STA retry early. Firing ONLY heartbeat ticks (never advancing the
// fake clock anywhere near apFallbackRetryWait) must raise the Beat count
// while waitAPFallback stays blocked (no retry, no cancellation).
func TestManagerWaitAPFallbackBeatsOnHeartbeatWithoutConsumingRetryBudget(t *testing.T) {
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))
	var beats atomic.Int32

	m := NewManager(ManagerDeps{
		Now:   clock.Now,
		After: after.After,
		Beat:  func() { beats.Add(1) },
	})

	type result struct{ viaRetry, cancelled bool }
	resultCh := make(chan result, 1)
	go func() {
		viaRetry, cancelled := m.waitAPFallback(context.Background())
		resultCh <- result{viaRetry, cancelled}
	}()

	// Fire several heartbeat ticks without ever advancing the clock: with a
	// frozen Now, every wake looks like "well short of the budget" and must
	// be treated as a mere heartbeat (Beat + re-enter the wait), never the
	// real 5-minute timeout.
	const ticks = 5
	for i := 1; i <= ticks; i++ {
		fire(t, after.nth(t, i))
	}
	waitForCond(t, func() bool { return beats.Load() >= ticks })

	select {
	case res := <-resultCh:
		t.Fatalf("waitAPFallback returned early (viaRetry=%v cancelled=%v) after only %d heartbeat ticks; want it still blocked", res.viaRetry, res.cancelled, ticks)
	default:
	}

	// Confirm it is genuinely still parked waiting for a further After()
	// call (i.e. it re-entered the wait loop) rather than having returned
	// through some other path undetected above.
	after.nth(t, ticks+1)
}

// TestManagerRunHeartbeatTicksDuringAPWaitDontBypassSuppression drives the
// full Run loop through several heartbeat ticks (Beat rising, no retry
// sequence started), then genuinely reaches the 5-minute mark while a
// provisioning session is active — asserting the intervening heartbeat wakes
// neither reset nor bypass the existing provisioning-suppression semantics
// (TestManagerRunSuppressesRetryDuringActiveProvisioning's contract).
func TestManagerRunHeartbeatTicksDuringAPWaitDontBypassSuppression(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	var beats atomic.Int32

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{} }, // no wifi -> straight to AP
		AP:      APConfig{SSID: "Trainboard-J", Addr: "192.168.4.1/24"},
		Beat:    func() { beats.Add(1) },
		Now:     clock.Now,
		After:   after.After,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	// Two heartbeat ticks (clock never advanced): Beat rises, but the retry
	// sequence (StopAP) must never start from these alone.
	fire(t, after.nth(t, 1))
	fire(t, after.nth(t, 2))
	waitForCond(t, func() bool { return beats.Load() >= 2 })

	for _, c := range driver.Calls() {
		if c == "StopAP" {
			t.Fatalf("StopAP called after mere heartbeat ticks: %v", driver.Calls())
		}
	}

	// Now genuinely reach the 5-minute mark while provisioning is active:
	// suppression must still apply, unweakened by the intervening heartbeat
	// wakes.
	timer := after.nth(t, 3)
	clock.Advance(apFallbackRetryWait)
	m.NoteProvisioning(clock.Now())
	fire(t, timer)

	after.nth(t, 4) // suppressed: Run looped back and is waiting again.

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

// --- issue #47: instant-first-STA-failure fast retry -----------------------

// capturingHandler is a minimal slog.Handler test double recording every
// emitted message, so the fast-retry tests can assert the fast-retry log line
// is emitted exactly once (or never).
type capturingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler            { return h }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.msgs = append(h.msgs, r.Message)
	h.mu.Unlock()
	return nil
}

func (h *capturingHandler) count(sub string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			n++
		}
	}
	return n
}

// countCalls returns how many times driver recorded name.
func countCalls(driver *fakeDriver, name string) int {
	n := 0
	for _, c := range driver.Calls() {
		if c == name {
			n++
		}
	}
	return n
}

// TestManagerFastRetryOnInstantFirstSTAFailure pins the issue #47 manager fix:
// when the very first STA attempt of the process fails within fastRetryThreshold
// (the fake clock never advances, so the attempt looks instant — a cold
// wpa_supplicant control-socket race, not a genuine association failure), the
// manager retries STA once immediately before ever paying the ~5-minute AP
// fallback detour. Here both attempts fail, so it still ends in AP fallback —
// but via exactly TWO AttemptSTA calls, with the fast-retry log emitted once.
func TestManagerFastRetryOnInstantFirstSTAFailure(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true, attemptSTAErr: errors.New("ctrl socket not ready")}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0)) // frozen: every attempt looks instant
	logs := &capturingHandler{}

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-FR", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
		Log:     slog.New(logs),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	if got := countCalls(driver, "AttemptSTA"); got != 2 {
		t.Fatalf("AttemptSTA called %d times, want 2 (boot attempt + one fast retry)", got)
	}
	if got := logs.count("fast retry"); got != 1 {
		t.Fatalf("fast-retry log emitted %d times, want 1", got)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// TestManagerFastRetrySkippedWhenFirstSTAFailureSlow confirms the fast retry is
// gated on speed, not merely on being the first failure: a first attempt that
// takes 3s (past fastRetryThreshold) is a genuine association failure, not a
// socket race, so it must NOT trigger a fast retry — exactly ONE AttemptSTA
// then straight to AP fallback, no fast-retry log.
func TestManagerFastRetrySkippedWhenFirstSTAFailureSlow(t *testing.T) {
	clock := newFakeClock(time.Unix(0, 0))
	driver := &fakeDriver{
		apActiveDefault: true,
		attemptSTAErr:   errors.New("association timed out"),
		attemptSTAHook:  func() { clock.Advance(3 * time.Second) }, // the attempt is slow
	}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	logs := &capturingHandler{}

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-SL", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
		Log:     slog.New(logs),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	if got := countCalls(driver, "AttemptSTA"); got != 1 {
		t.Fatalf("AttemptSTA called %d times, want 1 (a slow first failure gets no fast retry)", got)
	}
	if got := logs.count("fast retry"); got != 0 {
		t.Fatalf("fast-retry log emitted %d times, want 0", got)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// TestManagerFastRetryOncePerProcessLifetime pins the once-per-process budget:
// boot consumes the single fast retry (2 AttemptSTA calls), and a LATER
// AP-fallback retry cycle whose STA attempt ALSO fails instantly gets NO
// second fast retry — total AttemptSTA is 3 (2 boot + exactly 1 retry) and the
// fast-retry log stays at exactly one.
func TestManagerFastRetryOncePerProcessLifetime(t *testing.T) {
	driver := &fakeDriver{apActiveDefault: true, attemptSTAErr: errors.New("ctrl socket not ready")}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0)) // frozen: every attempt looks instant
	logs := &capturingHandler{}

	m := NewManager(ManagerDeps{
		Driver:  driver,
		Check:   passingCheck(),
		Dnsmasq: dnsmasq,
		STA:     func() STAConfig { return STAConfig{SSID: "home", PSK: "secret"} },
		AP:      APConfig{SSID: "Trainboard-OP", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
		Log:     slog.New(logs),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	// Boot: initial attempt + the one lifetime fast retry, both fail -> AP.
	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })
	if got := countCalls(driver, "AttemptSTA"); got != 2 {
		t.Fatalf("boot AttemptSTA = %d, want 2 (initial + fast retry)", got)
	}

	// A later AP-fallback retry whose STA attempt also fails instantly.
	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
	fire(t, timer)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback && s.LastSTAErr != "" })

	if got := countCalls(driver, "AttemptSTA"); got != 3 {
		t.Fatalf("AttemptSTA = %d, want 3 (2 boot + exactly 1 retry; no second fast retry)", got)
	}
	if got := logs.count("fast retry"); got != 1 {
		t.Fatalf("fast-retry log emitted %d times, want exactly 1 (once per process lifetime)", got)
	}

	cancel()
	if err := waitRunDone(t, done); err != nil {
		t.Fatalf("Run() err = %v, want nil", err)
	}
}

// --- issue #48: one in-process AP-restore retry before the fatal path -------

// TestManagerRunAPRestoreFailureRetriesInProcessOnce pins the issue #48
// manager fix: when the AP-fallback retry cycle's AP restore (toAP) fails,
// Run must NOT exit fatally on the first failure — it logs, re-runs the full
// toAP transition once, and on success publishes a verified APFallback (with
// LastSTAErr recorded) and keeps running. Before the fix the first toAP
// failure here was fatal and recovery took the systemd watchdog reboot
// (~10 min total observed on hardware) instead of an in-process self-heal.
func TestManagerRunAPRestoreFailureRetriesInProcessOnce(t *testing.T) {
	// APActive script: boot toAP verifies (true); the retry cycle's first toAP
	// fails BOTH its internal attempts (false, false); the manager-level
	// in-process retry then verifies (default true).
	driver := &fakeDriver{
		apActiveSeq:     []boolErr{{true, nil}, {false, nil}, {false, nil}},
		apActiveDefault: true,
	}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))
	logs := &capturingHandler{}

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
		AP:      APConfig{SSID: "Trainboard-R", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
		Log:     slog.New(logs),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	// Advance past the heartbeat-tracked budget so this wake is the real
	// retry timeout (see TestManagerRunFullFallbackRetrySuccessCycle).
	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
	fire(t, timer)

	// The in-process retry restored a VERIFIED AP: APFallback republished with
	// the hotspot and the STA failure recorded.
	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback && s.LastSTAErr != "" })
	if got := m.Status(); got.Hotspot == nil || got.Hotspot.SSID != "Trainboard-R" {
		t.Fatalf("Hotspot = %+v, want SSID Trainboard-R restored by the in-process retry", got.Hotspot)
	}
	if got := logs.count("AP restore failed; one in-process retry"); got != 1 {
		t.Fatalf("retry log emitted %d times, want exactly 1", got)
	}
	// Boot toAP (1 StartAP) + failed toAP (2, one per internal attempt) +
	// in-process retry toAP (1, verified first attempt) = 4.
	if got := countCalls(driver, "StartAP"); got != 4 {
		t.Fatalf("StartAP = %d, want 4 (boot 1 + failed restore 2 + in-process retry 1); calls: %v", got, driver.Calls())
	}

	// Run must still be alive — no fatal exit happened.
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

// TestManagerRunEscalatesAfterInProcessAPRestoreRetryFails extends the fatal
// AP-restore path (see TestManagerRunEscalatesWhenAPVerificationPermanentlyFails
// for the boot-time flavour): if the in-process retry's toAP ALSO fails, Run
// exits with the existing fatal error verbatim — the watchdog backstop is
// unchanged, and there is exactly ONE manager-level retry (it provably cannot
// loop: 2 toAP transitions = 4 restore StartAP calls, never more).
func TestManagerRunEscalatesAfterInProcessAPRestoreRetryFails(t *testing.T) {
	// Boot toAP verifies (true); every later APActive is false, so both the
	// first restore and the single in-process retry fail all their attempts.
	driver := &fakeDriver{
		apActiveSeq:     []boolErr{{true, nil}},
		apActiveDefault: false,
	}
	dnsmasq := newTestDnsmasqRecordingInto(driver)
	after := &fakeAfter{}
	clock := newFakeClock(time.Unix(0, 0))
	var beats atomic.Int32

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
		AP:      APConfig{SSID: "Trainboard-S", Addr: "192.168.4.1/24"},
		Now:     clock.Now,
		After:   after.After,
		Beat:    func() { beats.Add(1) },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runManagerAsync(ctx, m)

	waitForState(t, m, func(s Status) bool { return s.State == ManagerAPFallback })

	timer := after.nth(t, 1)
	clock.Advance(apFallbackRetryWait)
	fire(t, timer)

	err := waitRunDone(t, done)
	if err == nil {
		t.Fatal("Run() err = nil, want the fatal AP-restore error after the in-process retry also fails")
	}
	if !strings.Contains(err.Error(), "net: manager: AP fallback retry: AP restore failed") {
		t.Fatalf("err = %v, want containing %q (existing fatal error verbatim)", err, "net: manager: AP fallback retry: AP restore failed")
	}
	if got := m.Status(); got.State == ManagerAPFallback {
		t.Fatalf("State = %v, want not APFallback after escalation", got.State)
	}
	// Exactly one manager-level retry: boot 1 + first restore 2 + retry 2 = 5
	// StartAP calls, never more — the retry cannot loop.
	if got := countCalls(driver, "StartAP"); got != 5 {
		t.Fatalf("StartAP = %d, want 5 (boot 1 + restore 2 + single retry 2); calls: %v", got, driver.Calls())
	}
	// Heartbeat arithmetic (see runAPWait): Beat at the boot iteration (1) and
	// the AP-wait iteration (2), plus exactly one extra Beat immediately before
	// the in-process retry (3) so the retry's toAP never extends the
	// worst-case beat gap past the watchdog deadline.
	if got := beats.Load(); got != 3 {
		t.Fatalf("Beat called %d times, want exactly 3 (boot + AP-wait iteration + pre-retry beat)", got)
	}
}
