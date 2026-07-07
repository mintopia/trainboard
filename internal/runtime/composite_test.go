package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"testing"

	"github.com/mintopia/trainboard/internal/board"
)

// (i) hs nil -> base pointer returned UNCHANGED (identity — critical for
// scene-swap semantics: the render loop rebuilds on pointer inequality).
func TestHotspotSnapshotSourceNilHotspotReturnsBaseUnchanged(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return nil })

	got := src()
	if got != base {
		t.Fatalf("got %p, want the exact base pointer %p", got, base)
	}
}

// (ii) hs non-nil -> composed snapshot has Hotspot set, base unmutated.
func TestHotspotSnapshotSourceComposesHotspotWithoutMutatingBase(t *testing.T) {
	base := &board.Snapshot{State: board.StateDepartures}
	hs := &board.Hotspot{SSID: "trainboard-setup", Password: "hunter22", Addr: "192.168.4.1"}
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs })

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
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs })

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
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs })

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
	src := HotspotSnapshotSource(func() *board.Snapshot { return base }, func() *board.Hotspot { return hs })

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
	src := HotspotSnapshotSource(func() *board.Snapshot { return nil }, func() *board.Hotspot { return hs })

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
