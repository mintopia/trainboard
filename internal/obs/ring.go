// Package obs provides observability primitives for the board: a bounded
// in-memory event ring, a slog handler that tees into it, and the on-screen
// fault-code registry. Stdlib only.
package obs

import (
	"log/slog"
	"sync"
	"time"
)

// DefaultRingCapacity is the ring size used by the application.
const DefaultRingCapacity = 256

// Event is one recorded observation: a log record, state transition, or
// timing sample.
type Event struct {
	Time  time.Time
	Level slog.Level
	Msg   string
	Attrs map[string]string
}

// Ring is a fixed-capacity, thread-safe event buffer that evicts the oldest
// entry when full.
type Ring struct {
	mu    sync.Mutex
	buf   []Event
	start int // index of oldest
	n     int // count
}

// NewRing returns a ring holding at most capacity events. capacity must be
// positive.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("obs: ring capacity must be positive")
	}
	return &Ring{buf: make([]Event, capacity)}
}

// Add appends an event, evicting the oldest if the ring is full.
func (r *Ring) Add(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = e
		r.n++
		return
	}
	r.buf[r.start] = e
	r.start = (r.start + 1) % len(r.buf)
}

// Events returns a copy of the buffered events, oldest first.
func (r *Ring) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, r.n)
	for i := 0; i < r.n; i++ {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}

// Len reports how many events are buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}
