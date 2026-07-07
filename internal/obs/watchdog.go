package obs

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// watchdogComponent tracks one registered component's heartbeat deadline.
type watchdogComponent struct {
	name     string
	deadline time.Duration
	lastBeat time.Time
}

// Watchdog aggregates component heartbeats and pets systemd only while EVERY
// registered component has beaten within its deadline (M3 spec § Watchdog):
// a healthy render loop must not mask a deadlocked manager.
type Watchdog struct {
	mu         sync.Mutex
	notify     func(state string) error
	now        func() time.Time
	components []*watchdogComponent
}

// NewWatchdog constructs an aggregator. notify is invoked with "WATCHDOG=1"
// once per healthy interval tick in Run — normally obs.SdNotify. now is the
// clock used both to timestamp beats and to evaluate staleness; injectable
// so tests can drive it with a manual clock.
func NewWatchdog(notify func(state string) error, now func() time.Time) *Watchdog {
	return &Watchdog{notify: notify, now: now}
}

// Register adds a component with the given liveness deadline and returns its
// Beat function. The component is considered fresh from the moment of
// registration (lastBeat starts at now()) until deadline elapses without a
// subsequent call to the returned function. Safe to call concurrently with
// Run and with other components' Beat functions.
func (w *Watchdog) Register(name string, deadline time.Duration) func() {
	w.mu.Lock()
	c := &watchdogComponent{name: name, deadline: deadline, lastBeat: w.now()}
	w.components = append(w.components, c)
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		c.lastBeat = w.now()
		w.mu.Unlock()
	}
}

// Healthy reports whether every registered component has beaten within its
// deadline as of now. When false, it also returns the name of the first
// stale component, in registration order.
func (w *Watchdog) Healthy(now time.Time) (bool, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, c := range w.components {
		if now.Sub(c.lastBeat) > c.deadline {
			return false, c.name
		}
	}
	return true, ""
}

// Run pets systemd (via notify) at interval, but only on ticks where every
// registered component is currently healthy; a stale component silently
// withholds the pet, letting systemd's WatchdogSec eventually restart the
// unit. Returns when ctx is cancelled.
func (w *Watchdog) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ok, _ := w.Healthy(w.now()); ok {
				_ = w.notify("WATCHDOG=1")
			}
		}
	}
}

// SdNotify writes state (e.g. "WATCHDOG=1") to $NOTIFY_SOCKET over a
// unixgram datagram socket, systemd's sd_notify(3) protocol. It returns nil
// silently when the env var is unset or empty (dev mode: no systemd
// supervision). A leading '@' in the socket path is rewritten to a NUL byte,
// per systemd's abstract-namespace-socket convention.
func SdNotify(state string) error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "@") {
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("sd_notify: dial %s: %w", path, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("sd_notify: write: %w", err)
	}
	return nil
}
