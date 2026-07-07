package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"sync"

	"github.com/mintopia/trainboard/internal/board"
	"github.com/mintopia/trainboard/internal/obs"
)

// HotspotSnapshotSource decorates a snapshot source with the Manager's
// hotspot state and, when no hotspot is active, its connectivity faults:
//
//   - While hs() returns non-nil, the returned snapshot is the base snapshot
//     cloned with Hotspot set (the AP setup scene). The AP scene outranks any
//     connectivity fault.
//   - Otherwise, when conn() reports a radio block or a failing stage AND the
//     base snapshot is in an injectable state, the returned snapshot is the
//     base cloned with an E05/E06 fault so the render loop shows it on-glass.
//   - Otherwise the base pointer is returned UNCHANGED (no allocation, no
//     wrapping) so the render loop's identity check behaves exactly as it does
//     without this decorator.
//
// The composed pointer is CACHED and only replaced when its inputs change —
// the render loop rebuilds its scene on pointer inequality (loop.go step()),
// so a fresh pointer every call would rebuild the scene at 25fps. The hotspot
// cache keys on (base pointer, hotspot value); the fault cache keys on (base
// pointer, stage, radioBlocked).
//
// conn is nil when the connectivity feature is off (--manage-network absent):
// no fault is ever injected and the decorator behaves exactly as the
// hotspot-only version did.
//
// Fault-injection rule (applied ONLY when hs() is nil, so the AP scene always
// outranks faults):
//   - the base snapshot must be non-nil and in StateInitialising or
//     StateError (live departures and the stale-grace window are never
//     masked); AND
//   - its existing Fault must be empty or E01 (FaultDarwinUnreachable) — a
//     more actionable fault such as E04 (config error) is never overridden;
//   - radioBlocked → clone with Fault=E05 (FaultRadioBlocked), State=Error;
//   - else stage != "" → clone with Fault=E06 (FaultConnectivity),
//     FaultDetail=stage, State=Error.
//
// If base() returns nil while hs() is non-nil (no snapshot published yet), a
// synthetic StateInitialising snapshot carrying the Hotspot is returned so
// first-boot AP mode shows before the poller's first snapshot.
func HotspotSnapshotSource(base func() *board.Snapshot, hs func() *board.Hotspot, conn func() (stage string, radioBlocked bool)) func() *board.Snapshot {
	c := &hotspotComposer{base: base, hs: hs, conn: conn}
	return c.next
}

// hotspotComposer holds the cached composed pointers plus the inputs they were
// built from. hs is compared by VALUE (SSID+Password+Addr), not pointer, so
// the Manager may hand back a fresh *board.Hotspot every call without
// defeating the cache; the fault cache likewise keys on the (stage,
// radioBlocked) values.
type hotspotComposer struct {
	mu sync.Mutex

	base func() *board.Snapshot
	hs   func() *board.Hotspot
	conn func() (stage string, radioBlocked bool)

	haveComposed bool
	lastBase     *board.Snapshot
	lastHS       board.Hotspot
	composed     *board.Snapshot

	haveFault     bool
	lastFaultBase *board.Snapshot
	lastStage     string
	lastRadio     bool
	composedFault *board.Snapshot
}

func (c *hotspotComposer) next() *board.Snapshot {
	b := c.base()
	h := c.hs()

	if h != nil {
		return c.composeHotspot(b, h)
	}

	var stage string
	var radioBlocked bool
	if c.conn != nil {
		stage, radioBlocked = c.conn()
	}
	if fault := c.faultToInject(b, stage, radioBlocked); fault != obs.FaultNone {
		return c.composeFault(b, fault, stage, radioBlocked)
	}
	return b
}

// composeHotspot returns the base cloned with the Hotspot set, cached on
// (base pointer, hotspot value).
func (c *hotspotComposer) composeHotspot(b *board.Snapshot, h *board.Hotspot) *board.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.haveComposed && b == c.lastBase && *h == c.lastHS {
		return c.composed
	}

	var out board.Snapshot
	if b != nil {
		out = *b
	} else {
		out = board.Snapshot{State: board.StateInitialising}
	}
	out.Hotspot = h

	c.lastBase = b
	c.lastHS = *h
	c.haveComposed = true
	c.composed = &out
	return c.composed
}

// faultToInject decides which fault (if any) to overlay on the base snapshot.
// It returns FaultNone when no fault should be injected.
func (c *hotspotComposer) faultToInject(b *board.Snapshot, stage string, radioBlocked bool) obs.FaultCode {
	if b == nil {
		return obs.FaultNone
	}
	if b.State != board.StateInitialising && b.State != board.StateError {
		return obs.FaultNone
	}
	// Never override a more-actionable existing fault (DECISION: E04 > E06);
	// only an empty or E01 base fault is overridable.
	if b.Fault != obs.FaultNone && b.Fault != obs.FaultDarwinUnreachable {
		return obs.FaultNone
	}
	switch {
	case radioBlocked:
		return obs.FaultRadioBlocked
	case stage != "":
		return obs.FaultConnectivity
	default:
		return obs.FaultNone
	}
}

// composeFault returns the base cloned with the injected fault, cached on
// (base pointer, stage, radioBlocked).
func (c *hotspotComposer) composeFault(b *board.Snapshot, fault obs.FaultCode, stage string, radioBlocked bool) *board.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.haveFault && b == c.lastFaultBase && stage == c.lastStage && radioBlocked == c.lastRadio {
		return c.composedFault
	}

	out := *b
	out.State = board.StateError
	out.Fault = fault
	if fault == obs.FaultConnectivity {
		out.FaultDetail = stage
	} else {
		out.FaultDetail = ""
	}

	c.lastFaultBase = b
	c.lastStage = stage
	c.lastRadio = radioBlocked
	c.haveFault = true
	c.composedFault = &out
	return c.composedFault
}
