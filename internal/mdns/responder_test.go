package mdns

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes
//
// The responder's concurrency is driven entirely through injected seams so the
// tests are deterministic and free of wall-clock waits:
//
//   - fakeClock hands out a manually-fired ticker channel and per-call After
//     channels the test releases explicitly. There is no virtual time to race
//     on; every timing edge is a channel hand-off with a clear happens-before.
//   - fakeConn models a *net.UDPConn: ReadFrom blocks on an inbound channel,
//     WriteTo records to an outbound channel, Close returns net.ErrClosed to a
//     blocked reader. Concurrent ReadFrom/WriteTo touch disjoint channels.
//   - harness owns the interface/addr/listen seams behind a mutex.
// ---------------------------------------------------------------------------

type afterCall struct {
	d  time.Duration
	ch chan time.Time
}

type fakeClock struct {
	now     time.Time
	tickC   chan time.Time
	tickerC chan struct{}  // signals NewTicker was called (initial poll done)
	afters  chan afterCall // one entry per After() call, released by the test
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now:     time.Unix(0, 0),
		tickC:   make(chan time.Time, 8),
		tickerC: make(chan struct{}, 4),
		afters:  make(chan afterCall, 8),
	}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.afters <- afterCall{d: d, ch: ch}
	return ch
}

func (c *fakeClock) NewTicker(time.Duration) ticker {
	select {
	case c.tickerC <- struct{}{}:
	default:
	}
	return &fakeTicker{c: c.tickC}
}

// tick fires one poll.
func (c *fakeClock) tick() { c.tickC <- c.now }

// releaseAfter unblocks the next pending clock.After call, returning the
// duration it was asked to wait. It blocks until an After call is pending, so
// it doubles as a barrier proving the responder reached its announce gap.
func (c *fakeClock) releaseAfter() time.Duration {
	ac := <-c.afters
	ac.ch <- c.now
	return ac.d
}

// waitTicker blocks until Run has created its poll ticker, i.e. the initial
// poll has fully completed.
func (c *fakeClock) waitTicker(t *testing.T) {
	t.Helper()
	select {
	case <-c.tickerC:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for poll ticker to be created")
	}
}

type fakeTicker struct{ c chan time.Time }

func (t *fakeTicker) Chan() <-chan time.Time { return t.c }
func (t *fakeTicker) Stop()                  {}

type writeRec struct {
	data []byte
	addr net.Addr
}

type fakeConn struct {
	writeCh chan writeRec
	readCh  chan writeRec
	closed  chan struct{}
	once    sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		writeCh: make(chan writeRec, 64),
		readCh:  make(chan writeRec, 16),
		closed:  make(chan struct{}),
	}
}

func (c *fakeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case p := <-c.readCh:
		n := copy(b, p.data)
		return n, p.addr, nil
	}
}

func (c *fakeConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	cp := append([]byte(nil), b...)
	c.writeCh <- writeRec{data: cp, addr: addr}
	return len(b), nil
}

func (c *fakeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeConn) inject(data []byte) {
	c.readCh <- writeRec{data: data, addr: &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 5353}}
}

func (c *fakeConn) nextWrite(t *testing.T) writeRec {
	t.Helper()
	select {
	case w := <-c.writeCh:
		return w
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a write")
		return writeRec{}
	}
}

func (c *fakeConn) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-c.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for conn to close")
	}
}

func (c *fakeConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func drainWrites(c *fakeConn) []writeRec {
	var out []writeRec
	for {
		select {
		case w := <-c.writeCh:
			out = append(out, w)
		default:
			return out
		}
	}
}

type harness struct {
	mu       sync.Mutex
	ifaces   []net.Interface
	addrs    map[string][]net.Addr
	conns    map[string]*fakeConn
	listened []string

	suppress atomic.Bool
	beats    atomic.Int64
	beatCh   chan struct{}
	clk      *fakeClock
}

func newHarness() *harness {
	return &harness{
		addrs:  map[string][]net.Addr{},
		conns:  map[string]*fakeConn{},
		beatCh: make(chan struct{}, 64),
		clk:    newFakeClock(),
	}
}

func (h *harness) config() Config {
	return Config{
		Suffix:        "AB12",
		SuppressWlan0: func() bool { return h.suppress.Load() },
		Beat: func() {
			h.beats.Add(1)
			select {
			case h.beatCh <- struct{}{}:
			default:
			}
		},
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Interfaces: h.interfaces,
		ListenV4:   h.listenV4,
		Addrs:      h.addrsFor,
		clk:        h.clk,
	}
}

func (h *harness) interfaces() ([]net.Interface, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]net.Interface, len(h.ifaces))
	copy(out, h.ifaces)
	return out, nil
}

func (h *harness) addrsFor(ifi *net.Interface) ([]net.Addr, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.addrs[ifi.Name], nil
}

func (h *harness) listenV4(ifi *net.Interface) (netPacketConn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listened = append(h.listened, ifi.Name)
	c := newFakeConn()
	h.conns[ifi.Name] = c
	return c, nil
}

func (h *harness) setIfaces(ifaces []net.Interface, addrs map[string][]net.Addr) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ifaces = ifaces
	if addrs != nil {
		h.addrs = addrs
	}
}

func (h *harness) conn(name string) *fakeConn {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[name]
}

func (h *harness) listenCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.listened)
}

func (h *harness) waitBeat(t *testing.T) {
	t.Helper()
	select {
	case <-h.beatCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a poll beat")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ethIface() net.Interface {
	return net.Interface{Index: 2, Name: "eth0", Flags: net.FlagUp | net.FlagMulticast}
}

func v4Addrs(ip string) []net.Addr {
	return []net.Addr{&net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(24, 32)}}
}

func expectMessage(t *testing.T, ans []record) []byte {
	t.Helper()
	msg, err := encodeMessage(header{Flags: flagResponseAA}, nil, ans)
	if err != nil {
		t.Fatalf("encodeMessage: %v", err)
	}
	return msg
}

func startRun(t *testing.T, h *harness) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, h.config()) }()
	return cancel, done
}

func stopRun(t *testing.T, cancel context.CancelFunc, done chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}
}

const testMulticastAddr = "224.0.0.251:5353"

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRespondsToMatchingQuery: a query for one of our names produces the right
// answer, multicast to 224.0.0.251:5353.
func TestRespondsToMatchingQuery(t *testing.T) {
	h := newHarness()
	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	// Initial poll opens eth0 and announces twice.
	if d := h.clk.releaseAfter(); d != time.Second {
		t.Fatalf("announce gap = %v, want 1s", d)
	}
	conn := h.conn("eth0")
	if conn == nil {
		t.Fatal("eth0 was not opened by the initial poll")
	}
	conn.nextWrite(t) // announcement 1
	conn.nextWrite(t) // announcement 2

	q := question{Name: "trainboard-ab12.local", Type: TypeA, Class: 0x0001}
	query, err := encodeMessage(header{}, []question{q}, nil)
	if err != nil {
		t.Fatalf("encode query: %v", err)
	}
	conn.inject(query)

	w := conn.nextWrite(t)
	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.1"), nil).Answers(q))
	if !bytes.Equal(w.data, want) {
		t.Fatalf("response = %x, want %x", w.data, want)
	}
	if w.addr.String() != testMulticastAddr {
		t.Fatalf("response addr = %s, want %s", w.addr, testMulticastAddr)
	}
}

// TestIgnoresNonMatchingQuery: a query for a name we don't own yields no write;
// a following matching query still gets answered (proving survival + ordering).
func TestIgnoresNonMatchingQuery(t *testing.T) {
	h := newHarness()
	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.clk.releaseAfter()
	conn := h.conn("eth0")
	conn.nextWrite(t)
	conn.nextWrite(t)

	nq := question{Name: "someoneelse.local", Type: TypeA, Class: 0x0001}
	np, _ := encodeMessage(header{}, []question{nq}, nil)
	conn.inject(np)

	q := question{Name: "trainboard-ab12.local", Type: TypeA, Class: 0x0001}
	mp, _ := encodeMessage(header{}, []question{q}, nil)
	conn.inject(mp)

	// The reader processes packets in order, so the first write we observe must
	// be the answer to the matching query; the non-matching one produced none.
	w := conn.nextWrite(t)
	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.1"), nil).Answers(q))
	if !bytes.Equal(w.data, want) {
		t.Fatalf("first write = %x, want the matching answer %x", w.data, want)
	}
	select {
	case extra := <-conn.writeCh:
		t.Fatalf("unexpected extra write: %x", extra.data)
	default:
	}
}

// TestInterfaceAppearAnnouncesTwice: an interface appearing between polls opens
// a listener and announces twice, one announce-gap (1s) apart.
func TestInterfaceAppearAnnouncesTwice(t *testing.T) {
	h := newHarness() // start with no interfaces

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.clk.waitTicker(t) // initial (empty) poll done

	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})
	h.clk.tick()

	if d := h.clk.releaseAfter(); d != time.Second {
		t.Fatalf("announce gap = %v, want 1s", d)
	}
	conn := h.conn("eth0")
	if conn == nil {
		t.Fatal("eth0 was not opened after appearing")
	}
	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.1"), nil).Announcement())
	w1 := conn.nextWrite(t)
	w2 := conn.nextWrite(t)
	if !bytes.Equal(w1.data, want) {
		t.Fatalf("announcement 1 = %x, want %x", w1.data, want)
	}
	if !bytes.Equal(w2.data, want) {
		t.Fatalf("announcement 2 = %x, want %x", w2.data, want)
	}
}

// TestWlan0SuppressedThenReleased: while SuppressWlan0 is true, wlan0 is never
// opened; flipping it false opens and announces on the next poll.
func TestWlan0SuppressedThenReleased(t *testing.T) {
	h := newHarness()
	h.suppress.Store(true)
	wlan := net.Interface{Index: 3, Name: "wlan0", Flags: net.FlagUp | net.FlagMulticast}
	h.setIfaces([]net.Interface{wlan}, map[string][]net.Addr{"wlan0": v4Addrs("10.55.0.5")})

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.clk.waitTicker(t) // initial poll done; wlan0 suppressed
	if c := h.listenCount(); c != 0 {
		t.Fatalf("listened %d times while suppressed, want 0", c)
	}

	h.suppress.Store(false)
	h.clk.tick()
	if d := h.clk.releaseAfter(); d != time.Second {
		t.Fatalf("announce gap = %v, want 1s", d)
	}
	conn := h.conn("wlan0")
	if conn == nil {
		t.Fatal("wlan0 was not opened after suppression released")
	}
	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.5"), nil).Announcement())
	if w := conn.nextWrite(t); !bytes.Equal(w.data, want) {
		t.Fatalf("announcement = %x, want %x", w.data, want)
	}
	if c := h.listenCount(); c != 1 {
		t.Fatalf("listened %d times total, want 1 (only after release)", c)
	}
}

// TestInterfaceDisappearClosesConn: when an interface goes away its listener is
// closed.
func TestInterfaceDisappearClosesConn(t *testing.T) {
	h := newHarness()
	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.clk.releaseAfter() // initial announce
	conn := h.conn("eth0")
	if conn == nil {
		t.Fatal("eth0 was not opened")
	}

	h.setIfaces([]net.Interface{}, nil)
	h.clk.tick()
	conn.waitClosed(t)
}

// TestGoodbyeOnCancel: cancelling ctx writes a goodbye (all TTL 0) to every
// open conn, closes it, and Run returns nil.
func TestGoodbyeOnCancel(t *testing.T) {
	h := newHarness()
	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})

	cancel, done := startRun(t, h)

	h.clk.releaseAfter()
	conn := h.conn("eth0")
	conn.nextWrite(t) // announcement 1
	conn.nextWrite(t) // announcement 2

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Run to return")
	}

	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.1"), nil).Goodbye())
	found := false
	for _, w := range drainWrites(conn) {
		if bytes.Equal(w.data, want) {
			found = true
		}
	}
	if !found {
		t.Fatal("goodbye (TTL 0) not written to conn on cancel")
	}
	if !conn.isClosed() {
		t.Fatal("conn not closed after shutdown")
	}
}

// TestMalformedPacketSurvived: a malformed packet is logged and the reader
// keeps running (a following valid query is still answered).
func TestMalformedPacketSurvived(t *testing.T) {
	h := newHarness()
	h.setIfaces([]net.Interface{ethIface()}, map[string][]net.Addr{"eth0": v4Addrs("10.55.0.1")})

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.clk.releaseAfter()
	conn := h.conn("eth0")
	conn.nextWrite(t)
	conn.nextWrite(t)

	conn.inject([]byte{0x00, 0x01}) // too short to be a header

	q := question{Name: "trainboard-ab12.local", Type: TypeA, Class: 0x0001}
	mp, _ := encodeMessage(header{}, []question{q}, nil)
	conn.inject(mp)

	w := conn.nextWrite(t)
	want := expectMessage(t, NewZone("AB12", net.ParseIP("10.55.0.1"), nil).Answers(q))
	if !bytes.Equal(w.data, want) {
		t.Fatalf("follow-up query not answered after malformed packet: got %x", w.data)
	}
}

// TestBeatPerPoll: the watchdog heartbeat fires on every poll tick.
func TestBeatPerPoll(t *testing.T) {
	h := newHarness() // no interfaces; polls stay cheap

	cancel, done := startRun(t, h)
	defer stopRun(t, cancel, done)

	h.waitBeat(t)       // initial poll
	h.clk.waitTicker(t) // loop ready
	h.clk.tick()
	h.waitBeat(t)
	h.clk.tick()
	h.waitBeat(t)

	if got := h.beats.Load(); got < 3 {
		t.Fatalf("beats = %d, want >= 3 (initial + 2 ticks)", got)
	}
}

// TestEligible exercises the interface-eligibility predicate directly.
func TestEligible(t *testing.T) {
	up := net.FlagUp | net.FlagMulticast
	ok := v4Addrs("10.55.0.1")

	tests := []struct {
		name     string
		ifi      net.Interface
		addrs    []net.Addr
		suppress func() bool
		want     bool
	}{
		{"up+multicast+addr", net.Interface{Name: "eth0", Flags: up}, ok, nil, true},
		{"down", net.Interface{Name: "eth0", Flags: net.FlagMulticast}, ok, nil, false},
		{"no multicast", net.Interface{Name: "eth0", Flags: net.FlagUp}, ok, nil, false},
		{"loopback", net.Interface{Name: "lo", Flags: up | net.FlagLoopback}, v4Addrs("127.0.0.1"), nil, false},
		{"no usable addr", net.Interface{Name: "eth0", Flags: up}, nil, nil, false},
		{"link-local only", net.Interface{Name: "eth0", Flags: up}, v4Addrs("169.254.1.2"), nil, false},
		{"wlan0 suppressed", net.Interface{Name: "wlan0", Flags: up}, ok, func() bool { return true }, false},
		{"wlan0 not suppressed", net.Interface{Name: "wlan0", Flags: up}, ok, func() bool { return false }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := eligible(tt.ifi, tt.addrs, tt.suppress); got != tt.want {
				t.Fatalf("eligible(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
