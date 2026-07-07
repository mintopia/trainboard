package obs_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mintopia/trainboard/internal/obs"
)

// wdClock is a manually-advanced clock for deterministic watchdog tests: Run
// still ticks on a real (short) interval, but every staleness decision reads
// this controlled time instead of the wall clock.
type wdClock struct {
	mu  sync.Mutex
	now time.Time
}

func newWdClock(t time.Time) *wdClock { return &wdClock{now: t} }

func (c *wdClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *wdClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestWatchdogPetsWhenAllHealthy(t *testing.T) {
	clock := newWdClock(time.Unix(0, 0))
	notified := make(chan string, 16)
	notify := func(s string) error { notified <- s; return nil }
	w := obs.NewWatchdog(notify, clock.Now)

	beatA := w.Register("render", time.Minute)
	beatB := w.Register("poller", time.Minute)
	beatA()
	beatB()

	if ok, name := w.Healthy(clock.Now()); !ok {
		t.Fatalf("Healthy = (false, %q), want (true, \"\") when all beaten", name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, 5*time.Millisecond)

	select {
	case s := <-notified:
		if s != "WATCHDOG=1" {
			t.Fatalf("notify state = %q, want WATCHDOG=1", s)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog never pet systemd while all components were healthy")
	}
}

func TestWatchdogStaleComponentBlocksPetting(t *testing.T) {
	clock := newWdClock(time.Unix(0, 0))
	notified := make(chan string, 16)
	notify := func(s string) error { notified <- s; return nil }
	w := obs.NewWatchdog(notify, clock.Now)

	beatRender := w.Register("render", time.Minute)
	w.Register("poller", 10*time.Second) // registered, never beaten again
	beatRender()

	// Advance past poller's deadline (registered at t=0) while render stays
	// fresh (beaten at t=0, but its own deadline is much longer).
	clock.Advance(30 * time.Second)

	if ok, name := w.Healthy(clock.Now()); ok || name != "poller" {
		t.Fatalf("Healthy = (%v, %q), want (false, \"poller\")", ok, name)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, 5*time.Millisecond)

	select {
	case s := <-notified:
		t.Fatalf("watchdog pet systemd (%q) despite a stale component", s)
	case <-time.After(100 * time.Millisecond):
		// expected: no pet while unhealthy
	}
}

func TestWatchdogRebeatResumesPetting(t *testing.T) {
	clock := newWdClock(time.Unix(0, 0))
	notified := make(chan string, 16)
	notify := func(s string) error { notified <- s; return nil }
	w := obs.NewWatchdog(notify, clock.Now)

	beatA := w.Register("a", time.Minute)
	beatB := w.Register("b", time.Minute)
	beatA()
	beatB()
	clock.Advance(90 * time.Second) // both now stale

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, 5*time.Millisecond)

	select {
	case s := <-notified:
		t.Fatalf("watchdog pet systemd (%q) despite stale components", s)
	case <-time.After(50 * time.Millisecond):
		// expected: no pet yet
	}

	beatA()
	beatB() // re-beat: lastBeat becomes clock's current (still-advanced) time

	select {
	case s := <-notified:
		if s != "WATCHDOG=1" {
			t.Fatalf("notify state = %q, want WATCHDOG=1", s)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watchdog did not resume petting after re-beat")
	}
}

// TestWatchdogRegisterAfterRunStartedSafe exercises concurrent Register
// calls against a running watchdog under -race.
func TestWatchdogRegisterAfterRunStartedSafe(t *testing.T) {
	clock := newWdClock(time.Unix(0, 0))
	notify := func(string) error { return nil }
	w := obs.NewWatchdog(notify, clock.Now)

	beatA := w.Register("a", time.Minute)
	beatA()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx, time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			beat := w.Register(fmt.Sprintf("late-%d", i), time.Minute)
			beat()
			_, _ = w.Healthy(clock.Now())
		}(i)
	}
	wg.Wait()
	t.Logf("registered and beat 50 late components concurrently under a running watchdog")
}

func TestSdNotifyUnsetEnvReturnsNilWithoutPanic(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if err := obs.SdNotify("WATCHDOG=1"); err != nil {
		t.Fatalf("SdNotify with no NOTIFY_SOCKET = %v, want nil", err)
	}
}

func TestSdNotifyWritesDatagramToSocket(t *testing.T) {
	// A unix socket path is limited to ~104 bytes (sockaddr_un.sun_path) on
	// Darwin; t.TempDir()'s deeply-nested per-test path (under
	// /var/folders/.../TestName.../001) routinely overruns that. Use a
	// short directory directly under /tmp instead, with explicit cleanup.
	dir, err := os.MkdirTemp("/tmp", "tb-wd")
	if err != nil {
		t.Fatalf("mkdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "n.sock")
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: sockPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = conn.Close() }()

	t.Setenv("NOTIFY_SOCKET", sockPath)

	errCh := make(chan error, 1)
	go func() { errCh <- obs.SdNotify("WATCHDOG=1") }()

	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 64)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("read datagram: %v", err)
	}
	if got := string(buf[:n]); got != "WATCHDOG=1" {
		t.Fatalf("datagram = %q, want %q", got, "WATCHDOG=1")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("SdNotify returned error: %v", err)
	}
}

func TestSdNotifyMissingSocketReturnsError(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "does-not-exist.sock"))
	if err := obs.SdNotify("WATCHDOG=1"); err == nil {
		t.Fatal("SdNotify against a nonexistent socket = nil error, want non-nil")
	}
}
