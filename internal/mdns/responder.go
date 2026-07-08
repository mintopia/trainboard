package mdns

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// pollInterval is how often Run re-enumerates interfaces to detect churn.
const pollInterval = 5 * time.Second

// announceGap is the delay between the two announcements sent on
// interface-appear, per RFC 6762 §8.3 (announce at least twice, ~1s apart).
const announceGap = 1 * time.Second

// readBufSize bounds a single inbound packet. mDNS queries on a LAN fit well
// within a standard Ethernet MTU; oversized packets are truncated and rejected
// by the decoder, which the reader survives.
const readBufSize = 1500

// mdnsPort is the fixed mDNS UDP port (RFC 6762 §5).
const mdnsPort = 5353

// DNS header flag bits (RFC 1035 §4.1.1). flagResponse marks the QR bit;
// flagResponseAA additionally sets the authoritative-answer bit, which every
// response and announcement this responder emits must carry (RFC 6762 §18.4).
const (
	flagResponse   uint16 = 0x8000
	flagResponseAA uint16 = 0x8400
)

// mdnsGroupV4 is the IPv4 mDNS multicast group (RFC 6762 §3).
var mdnsGroupV4 = net.IPv4(224, 0, 0, 251)

// netPacketConn is the test seam over *net.UDPConn: the subset of methods the
// responder needs from a per-interface multicast socket.
type netPacketConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	Close() error
}

// Config wires the responder. All fields are required unless noted; the
// interface/socket/time seams fall back to production defaults when nil.
type Config struct {
	// Suffix is the macTail result, e.g. "AB12", used to build host names.
	Suffix string
	// SuppressWlan0 reports whether wlan0 must stay silent (hotspot up).
	// nil = never suppress.
	SuppressWlan0 func() bool
	// Beat is the watchdog heartbeat, called at the start of each poll tick.
	// nil = no-op.
	Beat func()
	// Log receives per-packet and churn diagnostics. nil = discard.
	Log *slog.Logger

	// Interfaces enumerates candidate interfaces. nil = net.Interfaces.
	Interfaces func() ([]net.Interface, error)
	// ListenV4 opens a per-interface IPv4 multicast listener. nil = a real
	// ListenMulticastUDP join of 224.0.0.251:5353 on the interface.
	ListenV4 func(ifi *net.Interface) (netPacketConn, error)
	// Addrs returns an interface's addresses. nil = ifi.Addrs.
	Addrs func(ifi *net.Interface) ([]net.Addr, error)
	// Now supplies the current time. nil = time.Now.
	Now func() time.Time

	// clk is an unexported full time seam (ticker + After) used by in-package
	// tests to drive the loop deterministically. Production leaves it nil and
	// a real-time clock is built from Now.
	clk clock
}

// clock is the responder's time seam. Unexported: production always uses real
// time; only in-package tests inject a fake.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
	NewTicker(d time.Duration) ticker
}

// ticker abstracts *time.Ticker so a fake can be driven manually.
type ticker interface {
	Chan() <-chan time.Time
	Stop()
}

type realClock struct{ now func() time.Time }

func (c realClock) Now() time.Time                         { return c.now() }
func (c realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (c realClock) NewTicker(d time.Duration) ticker       { return &realTicker{t: time.NewTicker(d)} }

type realTicker struct{ t *time.Ticker }

func (r *realTicker) Chan() <-chan time.Time { return r.t.C }
func (r *realTicker) Stop()                  { r.t.Stop() }

// defaultListenV4 opens a real IPv4 mDNS multicast socket joined on ifi.
func defaultListenV4(ifi *net.Interface) (netPacketConn, error) {
	conn, err := net.ListenMulticastUDP("udp4", ifi, &net.UDPAddr{IP: mdnsGroupV4, Port: mdnsPort})
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// ifaceConn is one active per-interface listener: its socket plus the immutable
// zone (addresses baked in at open time) its reader answers from.
type ifaceConn struct {
	name string
	conn netPacketConn
	zone *Zone
}

// ifaceInfo is a poll's view of one eligible interface.
type ifaceInfo struct {
	name string
	ifi  net.Interface
	v4   net.IP
	v6   net.IP
}

// responder holds the loop state. The conns map is owned exclusively by the
// Run goroutine (poll + shutdown); reader goroutines never touch it, so no
// mutex is required. Reader goroutines only read from their own conn and write
// replies to it — *net.UDPConn (and the fake) permit concurrent ReadFrom and
// WriteTo, and concurrent WriteTo from a reader and from shutdown's goodbye.
type responder struct {
	cfg   Config
	clk   clock
	log   *slog.Logger
	dst   *net.UDPAddr
	conns map[string]*ifaceConn
	wg    sync.WaitGroup
}

// Run blocks until ctx is done. Every pollInterval it re-enumerates interfaces,
// opening a multicast listener for each newly-eligible interface (announcing
// twice ~1s apart) and closing listeners for interfaces that disappeared or
// became suppressed. Each listener answers matching queries by multicasting an
// authoritative response to 224.0.0.251:5353.
//
// This responder is announce-only: it does no probing or conflict resolution,
// and it does not honor the unicast-response bit (RFC 6762 §5.4) — every reply
// is multicast. On ctx cancellation it sends a goodbye (all records at TTL 0)
// on every open listener before closing.
//
// Run returns a non-nil error only for setup impossibilities. Per-packet errors
// are logged and survived; on ctx done it returns nil.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Suffix == "" {
		return errors.New("mdns: Config.Suffix is required")
	}
	if cfg.Interfaces == nil {
		cfg.Interfaces = net.Interfaces
	}
	if cfg.ListenV4 == nil {
		cfg.ListenV4 = defaultListenV4
	}
	if cfg.Addrs == nil {
		cfg.Addrs = func(ifi *net.Interface) ([]net.Addr, error) { return ifi.Addrs() }
	}
	if cfg.Beat == nil {
		cfg.Beat = func() {}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	clk := cfg.clk
	if clk == nil {
		now := cfg.Now
		if now == nil {
			now = time.Now
		}
		clk = realClock{now: now}
	}

	r := &responder{
		cfg:   cfg,
		clk:   clk,
		log:   cfg.Log,
		dst:   &net.UDPAddr{IP: mdnsGroupV4, Port: mdnsPort},
		conns: make(map[string]*ifaceConn),
	}
	defer r.shutdown()

	r.poll(ctx)

	tk := clk.NewTicker(pollInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tk.Chan():
			r.poll(ctx)
		}
	}
}

// poll re-enumerates interfaces, closing listeners that vanished and opening
// (then announcing on) newly-eligible ones.
func (r *responder) poll(ctx context.Context) {
	r.cfg.Beat()

	ifaces, err := r.cfg.Interfaces()
	if err != nil {
		r.log.Warn("mdns: list interfaces", "err", err)
		return
	}

	desired := make(map[string]ifaceInfo, len(ifaces))
	for i := range ifaces {
		ifi := ifaces[i]
		addrs, err := r.cfg.Addrs(&ifi)
		if err != nil {
			r.log.Warn("mdns: interface addrs", "iface", ifi.Name, "err", err)
			continue
		}
		if !eligible(ifi, addrs, r.cfg.SuppressWlan0) {
			continue
		}
		desired[key(ifi)] = ifaceInfo{
			name: ifi.Name,
			ifi:  ifi,
			v4:   usableV4(addrs),
			v6:   usableV6(addrs),
		}
	}

	// Close listeners whose interface disappeared or became ineligible.
	for k, ic := range r.conns {
		if _, ok := desired[k]; !ok {
			delete(r.conns, k)
			_ = ic.conn.Close()
			r.log.Info("mdns: interface removed", "iface", ic.name)
		}
	}

	// Open listeners for newly-eligible interfaces.
	var appeared []*ifaceConn
	for k, info := range desired {
		if _, ok := r.conns[k]; ok {
			continue
		}
		conn, err := r.cfg.ListenV4(&info.ifi)
		if err != nil {
			r.log.Warn("mdns: open listener", "iface", info.name, "err", err)
			continue
		}
		ic := &ifaceConn{
			name: info.name,
			conn: conn,
			// Bake freshly-copied addresses into the zone so nothing aliases
			// the interface's address slices.
			zone: NewZone(r.cfg.Suffix, dupIP(info.v4), dupIP(info.v6)),
		}
		r.conns[k] = ic
		r.wg.Add(1)
		go r.reader(ic)
		appeared = append(appeared, ic)
		r.log.Info("mdns: interface added", "iface", info.name)
	}

	if len(appeared) > 0 {
		r.announce(ctx, appeared)
	}
}

// announce sends the unsolicited announce set to each conn twice, announceGap
// apart, aborting the second shot if ctx is cancelled during the gap.
func (r *responder) announce(ctx context.Context, conns []*ifaceConn) {
	for _, ic := range conns {
		r.writeRecords(ic, ic.zone.Announcement())
	}
	select {
	case <-ctx.Done():
		return
	case <-r.clk.After(announceGap):
	}
	for _, ic := range conns {
		r.writeRecords(ic, ic.zone.Announcement())
	}
}

// reader consumes packets on one listener until it is closed. It answers
// queries and survives malformed packets; it exits on any read error (a closed
// socket, or a broken one — either way the loop stops rather than spin).
func (r *responder) reader(ic *ifaceConn) {
	defer r.wg.Done()
	buf := make([]byte, readBufSize)
	for {
		n, _, err := ic.conn.ReadFrom(buf)
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				r.log.Warn("mdns: read", "iface", ic.name, "err", err)
			}
			return
		}
		r.handle(ic, buf[:n])
	}
}

// handle answers a single inbound packet, ignoring responses and non-matching
// queries. Decode failures are logged and swallowed.
func (r *responder) handle(ic *ifaceConn, pkt []byte) {
	h, qs, err := decodeQuery(pkt)
	if err != nil {
		r.log.Warn("mdns: decode query", "iface", ic.name, "err", err)
		return
	}
	if h.Flags&flagResponse != 0 {
		return // a response, not a query
	}
	var ans []record
	for _, q := range qs {
		ans = append(ans, ic.zone.Answers(q)...)
	}
	if len(ans) == 0 {
		return
	}
	r.writeRecords(ic, ans)
}

// writeRecords multicasts one authoritative response carrying ans.
func (r *responder) writeRecords(ic *ifaceConn, ans []record) {
	if len(ans) == 0 {
		return
	}
	msg, err := encodeMessage(header{Flags: flagResponseAA}, nil, ans)
	if err != nil {
		r.log.Warn("mdns: encode response", "iface", ic.name, "err", err)
		return
	}
	if _, err := ic.conn.WriteTo(msg, r.dst); err != nil {
		if !errors.Is(err, net.ErrClosed) {
			r.log.Warn("mdns: write", "iface", ic.name, "err", err)
		}
	}
}

// shutdown sends a goodbye on every open listener, closes them, and waits for
// reader goroutines to exit. Runs in the Run goroutine after the loop ends.
func (r *responder) shutdown() {
	for k, ic := range r.conns {
		r.writeRecords(ic, ic.zone.Goodbye())
		_ = ic.conn.Close()
		delete(r.conns, k)
	}
	r.wg.Wait()
}

// eligible reports whether ifi should host an mDNS listener: it must be up,
// multicast-capable, not loopback (lo can carry the multicast flag), not a
// suppressed wlan0, and have at least one usable unicast address.
func eligible(ifi net.Interface, addrs []net.Addr, suppress func() bool) bool {
	if ifi.Flags&net.FlagUp == 0 {
		return false
	}
	if ifi.Flags&net.FlagMulticast == 0 {
		return false
	}
	if ifi.Flags&net.FlagLoopback != 0 {
		return false
	}
	if ifi.Name == "wlan0" && suppress != nil && suppress() {
		return false
	}
	return usableV4(addrs) != nil || usableV6(addrs) != nil
}

// usableV4 returns the first global-unicast IPv4 address (net.IP.IsGlobalUnicast
// already admits RFC1918 private space while excluding loopback/link-local), or
// nil. The returned slice is the 4-byte form.
func usableV4(addrs []net.Addr) net.IP {
	for _, a := range addrs {
		ip := addrIP(a)
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil && ip.IsGlobalUnicast() {
			return v4
		}
	}
	return nil
}

// usableV6 returns the first global-unicast IPv6 address (ULA included,
// link-local excluded), or nil.
func usableV6(addrs []net.Addr) net.IP {
	for _, a := range addrs {
		ip := addrIP(a)
		if ip == nil || ip.To4() != nil {
			continue
		}
		if v16 := ip.To16(); v16 != nil && ip.IsGlobalUnicast() {
			return v16
		}
	}
	return nil
}

// addrIP extracts the IP from the address forms ifi.Addrs yields.
func addrIP(a net.Addr) net.IP {
	switch v := a.(type) {
	case *net.IPNet:
		return v.IP
	case *net.IPAddr:
		return v.IP
	}
	return nil
}

// dupIP returns an independent copy of ip (nil stays nil), so a zone never
// aliases an interface's address slice.
func dupIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	c := make(net.IP, len(ip))
	copy(c, ip)
	return c
}

// key identifies a listener by interface name and index, so a re-indexed
// interface of the same name is treated as churn.
func key(ifi net.Interface) string {
	return ifi.Name + "#" + strconv.Itoa(ifi.Index)
}
