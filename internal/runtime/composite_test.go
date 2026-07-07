package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/obs"
)

// connFn builds a conn seam returning a fixed (stage, radioBlocked) pair.
func connFn(stage string, radioBlocked bool) func() (string, bool) {
	return func() (string, bool) { return stage, radioBlocked }
}

// (vii) hs nil + a stage failure in an injectable base state (Initialising)
// -> composed E06 snapshot carrying the stage as FaultDetail, base unmutated.
func TestHotspotSnapshotSourceInjectsConnectivityFault(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DHCP", false),
	)

	got := src()
	if got == base {
		t.Fatal("a connectivity fault must produce a new composed pointer, not the base")
	}
	if got.State != board.StateError {
		t.Fatalf("State = %v, want StateError", got.State)
	}
	if got.Fault != obs.FaultConnectivity {
		t.Fatalf("Fault = %q, want E06 FaultConnectivity", got.Fault)
	}
	if got.FaultDetail != "DHCP" {
		t.Fatalf("FaultDetail = %q, want %q", got.FaultDetail, "DHCP")
	}
	if base.Fault != obs.FaultNone || base.State != board.StateInitialising {
		t.Fatal("base snapshot must remain unmutated")
	}
}

// (viii) radioBlocked -> E05, no FaultDetail.
func TestHotspotSnapshotSourceInjectsRadioBlocked(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("", true),
	)

	got := src()
	if got.State != board.StateError {
		t.Fatalf("State = %v, want StateError", got.State)
	}
	if got.Fault != obs.FaultRadioBlocked {
		t.Fatalf("Fault = %q, want E05 FaultRadioBlocked", got.Fault)
	}
	if got.FaultDetail != "" {
		t.Fatalf("FaultDetail = %q, want empty for a radio-blocked fault", got.FaultDetail)
	}
}

// (ix) radioBlocked outranks a concurrent stage failure (E05 over E06).
func TestHotspotSnapshotSourceRadioBlockedWinsOverStage(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DHCP", true),
	)

	got := src()
	if got.Fault != obs.FaultRadioBlocked {
		t.Fatalf("Fault = %q, want E05 FaultRadioBlocked (radio block outranks stage)", got.Fault)
	}
}

// (x) live departures are never masked: a stage failure while StateDepartures
// leaves the base pointer untouched.
func TestHotspotSnapshotSourceDoesNotInjectOverLiveDepartures(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DHCP", true),
	)

	if got := src(); got != base {
		t.Fatalf("got %p, want the exact base pointer %p (live departures must not be masked)", got, base)
	}
}

// (xi) DECISION: E04 (config error) is more actionable than E06, so an
// existing non-empty base Fault is never overridden.
func TestHotspotSnapshotSourceDoesNotOverrideConfigErrorFault(t *testing.T) {
	base := &board.Snapshot{State: board.StateError, Fault: obs.FaultConfigError}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DHCP", false),
	)

	if got := src(); got != base {
		t.Fatalf("got %p, want the exact base pointer %p (E04 must not be overridden by E06)", got, base)
	}
}

// (xii) an E01 base fault IS overridable (empty-or-E01 is the injection gate).
func TestHotspotSnapshotSourceInjectsOverDarwinUnreachable(t *testing.T) {
	base := &board.Snapshot{State: board.StateError, Fault: obs.FaultDarwinUnreachable}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DNS", false),
	)

	got := src()
	if got == base {
		t.Fatal("E01 is overridable: a stage failure must inject E06")
	}
	if got.Fault != obs.FaultConnectivity || got.FaultDetail != "DNS" {
		t.Fatalf("Fault/detail = %q/%q, want E06/DNS", got.Fault, got.FaultDetail)
	}
}

// (xiii) pointer stable across identical (base, nil-hs, stage) triples.
func TestHotspotSnapshotSourceFaultPointerStable(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		connFn("DHCP", false),
	)

	if first, second := src(), src(); first != second {
		t.Fatalf("identical (base, stage) must return the same pointer: %p != %p", first, second)
	}
}

// (xiv) a stage change produces a fresh composed pointer.
func TestHotspotSnapshotSourceNewPointerOnStageChange(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}
	stage := "DHCP"
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return nil },
		func() (string, bool) { return stage, false },
	)

	first := src()
	stage = "DNS"
	second := src()
	if first == second {
		t.Fatal("a changed stage must produce a new composed pointer")
	}
	if second.FaultDetail != "DNS" {
		t.Fatalf("FaultDetail = %q, want DNS after the stage change", second.FaultDetail)
	}
}

// (xv) the AP scene outranks faults: a non-nil hotspot wins over a concurrent
// stage/radio failure (composed carries the Hotspot, not an injected fault).
func TestHotspotSnapshotSourceHotspotWinsOverStage(t *testing.T) {
	base := &board.Snapshot{State: board.StateError}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(
		func() *board.Snapshot { return base },
		func() *board.Hotspot { return hs },
		connFn("DHCP", true),
	)

	got := src()
	if got.Hotspot == nil || *got.Hotspot != *hs {
		t.Fatalf("got.Hotspot = %+v, want %+v (hotspot must win)", got.Hotspot, hs)
	}
	if got.Fault == obs.FaultConnectivity || got.Fault == obs.FaultRadioBlocked {
		t.Fatalf("Fault = %q, want no connectivity fault injected while a hotspot is present", got.Fault)
	}
}

// TestHotspotSnapshotSourceConcurrentStress is a -race stress test: one
// goroutine calls src() at high frequency while a second flips the hotspot
// on/off and a third cycles the connectivity stage/radioBlocked pair,
// entirely independently of each other and of the reader. It asserts two
// things: `go test -race` finds no data race across the whole run (proving
// hotspotComposer's cache fields, guarded only by its own mutex, are never
// touched unsynchronized even under concurrent input churn), and that every
// single snapshot the reader observes obeys the Hotspot-xor-fault invariant
// documented on HotspotSnapshotSource — a snapshot never carries both a
// non-nil Hotspot and an injected E05/E06 fault, since the AP scene always
// outranks a connectivity fault.
func TestHotspotSnapshotSourceConcurrentStress(t *testing.T) {
	base := &board.Snapshot{State: board.StateInitialising}

	var hsMu sync.Mutex
	hsOn := false
	hs := func() *board.Hotspot {
		hsMu.Lock()
		defer hsMu.Unlock()
		if !hsOn {
			return nil
		}
		return &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	}

	var connMu sync.Mutex
	stages := []string{"ASSOC", "DHCP", "DNS", "CAPTIVE", ""}
	stageIdx := 0
	radioBlocked := false
	conn := func() (string, bool) {
		connMu.Lock()
		defer connMu.Unlock()
		return stages[stageIdx], radioBlocked
	}

	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, hs, conn)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				hsMu.Lock()
				hsOn = !hsOn
				hsMu.Unlock()
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				connMu.Lock()
				stageIdx = i % len(stages)
				radioBlocked = i%3 == 0
				connMu.Unlock()
				i++
			}
		}
	}()

	var checked atomic.Int64
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) {
		got := src()
		checked.Add(1)
		if got == nil {
			t.Fatal("src() returned nil")
		}
		hasFault := got.Fault == obs.FaultConnectivity || got.Fault == obs.FaultRadioBlocked
		if got.Hotspot != nil && hasFault {
			t.Fatalf("snapshot carries both a Hotspot and an injected fault (%q): %+v", got.Fault, got)
		}
	}
	close(stop)
	wg.Wait()

	if checked.Load() == 0 {
		t.Fatal("the reader loop never executed")
	}
}

// (i) hs nil -> base pointer returned UNCHANGED (identity — critical for
// scene-swap semantics: the render loop rebuilds on pointer inequality).
func TestHotspotSnapshotSourceNilHotspotReturnsBaseUnchanged(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return nil }, nil)

	got := src()
	if got != base {
		t.Fatalf("got %p, want the exact base pointer %p", got, base)
	}
}

// (ii) hs non-nil -> composed snapshot has Hotspot set, base unmutated.
func TestHotspotSnapshotSourceComposesHotspotWithoutMutatingBase(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs }, nil)

	got := src()
	if got == base {
		t.Fatal("composed snapshot must not be the same pointer as base")
	}
	if got.Hotspot == nil || *got.Hotspot != *hs {
		t.Fatalf("got.Hotspot = %+v, want %+v", got.Hotspot, hs)
	}
	if base.Hotspot != nil {
		t.Fatal("base snapshot must remain unmutated (Hotspot still nil)")
	}
}

// (iii) two consecutive calls with the same base+hs -> SAME composed
// pointer (the cache, protecting the render loop from rebuilding at 25fps).
func TestHotspotSnapshotSourceCachesComposedPointer(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs }, nil)

	first := src()
	second := src()
	if first != second {
		t.Fatalf("consecutive calls with unchanged base+hs must return the same pointer: %p != %p", first, second)
	}
}

// (iv) hs value change -> new pointer.
func TestHotspotSnapshotSourceNewPointerOnHotspotValueChange(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs }, nil)

	first := src()
	hs = &board.Hotspot{SSID: "trainboard-setup", Password: "changed", Addr: "192.168.4.1"}
	second := src()
	if first == second {
		t.Fatal("a changed hotspot value must produce a new composed pointer")
	}
}

// (v) base change -> new pointer.
func TestHotspotSnapshotSourceNewPointerOnBaseChange(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs }, nil)

	first := src()
	base = &board.Snapshot{State: board.StateDepartures}
	second := src()
	if first == second {
		t.Fatal("a changed base pointer must produce a new composed pointer")
	}
}

// (vi) nil base + hs non-nil -> synthetic snapshot (StateInitialising +
// Hotspot) so first-boot AP mode shows before any poll has published.
func TestHotspotSnapshotSourceNilBaseSynthesizesInitialising(t *testing.T) {
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return nil }, func() *board.Hotspot { return hs }, nil)

	got := src()
	if got == nil {
		t.Fatal("want a synthetic snapshot, got nil")
	}
	if got.State != board.StateInitialising {
		t.Fatalf("State = %v, want StateInitialising", got.State)
	}
	if got.Hotspot == nil || *got.Hotspot != *hs {
		t.Fatalf("got.Hotspot = %+v, want %+v", got.Hotspot, hs)
	}
}
