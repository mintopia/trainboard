package runtime //nolint:revive // internal package; does not collide with any import of stdlib runtime

import (
	"sync"

	"github.com/mintopia/trainboard/internal/board"
)

// HotspotSnapshotSource decorates a snapshot source with the Manager's
// hotspot state: while hs() returns non-nil, the returned snapshot is the
// base snapshot cloned with Hotspot set. The composed pointer is CACHED and
// only replaced when the base pointer or hotspot value changes — the render
// loop rebuilds its scene on pointer inequality (loop.go step()), so a fresh
// pointer every call would rebuild the scene at 25fps.
//
// While hs() is nil the base pointer is returned unchanged (no allocation,
// no wrapping) so the render loop's identity check behaves exactly as it
// does without this decorator. If base() returns nil while hs() is non-nil
// (no snapshot has been published yet), a synthetic StateInitialising
// snapshot carrying the Hotspot is returned instead, so first-boot AP mode
// is shown before the poller's first snapshot.
func HotspotSnapshotSource(base func() *board.Snapshot, hs func() *board.Hotspot) func() *board.Snapshot {
	c := &hotspotComposer{base: base, hs: hs}
	return c.next
}

// hotspotComposer holds the cached composed pointer plus the inputs it was
// built from. hs is compared by VALUE (SSID+Password+Addr), not pointer, so
// the Manager may hand back a fresh *board.Hotspot every call without
// defeating the cache.
type hotspotComposer struct {
	mu sync.Mutex

	base func() *board.Snapshot
	hs   func() *board.Hotspot

	haveComposed bool
	lastBase     *board.Snapshot
	lastHS       board.Hotspot
	composed     *board.Snapshot
}

func (c *hotspotComposer) next() *board.Snapshot {
	b := c.base()
	h := c.hs()

	if h == nil {
		return b
	}

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
