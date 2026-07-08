# M4: OTG Gadget Ethernet + mDNS Discovery — Design

**Issues:** #14 (gadget ethernet), #15 (mDNS). **Approved:** 2026-07-08 (Jess).

## Goal

Two always-works paths to reach the board without knowing its IP:

1. **USB gadget ethernet**: plug the Pi's micro-USB data port into any computer
   → the Pi appears as a USB NIC → `http://10.55.0.1` works. The debug
   lifeline: independent of wlan0, the connectivity manager, and the app
   itself. (Jess's stated M3.5 shortcut: wired access instead of waiting out
   AP retry cycles.)
2. **mDNS**: `trainboard-XXXX.local` (+ `trainboard.local` convenience alias)
   and `_http._tcp` service discovery on every LAN-ish interface.

Dongle (USB-host ethernet) ships software-side only; hardware validation
deferred until Jess's OTG adapter + dongle are accessible.

## Decisions (operator)

- Both wired paths in scope; gadget is the supported default (#14 wording).
- usb0 addressing: **static Pi + DHCP for the host** (10.55.0.0/29, Pi=.1),
  served by a second dnsmasq instance. Not link-local-only.
- mDNS: **stdlib mini-responder**, announce-only per #15 — no avahi, no
  vendored lib, no RFC 6762 probing/conflict-rename. `-XXXX` suffix is the
  collision defense.
- Names: `trainboard-XXXX.local` canonical (matches AP SSID tail) AND plain
  `trainboard.local` alias; two-board race on the alias is accepted.

## Architecture (Approach A: OS-level gadget, app-level mDNS)

The gadget is declarative OS config in `deploy/` — no Go code owns USB
descriptors, so the lifeline survives app crashes/restarts and `internal/net`
is untouched (M3.5 hardens that code next; don't churn it now). The app's
only new surface is `internal/mdns`.

### 1. Gadget ethernet (deploy/, docs)

- `/boot/config.txt`: `dtoverlay=dwc2`; kernel cmdline `modules-load=dwc2`.
  Documented in deploy.md §9 (new section); one-time per device.
- `deploy/gadget/trainboard-gadget.sh`: configfs bring-up:
  - gadget `g1`, VID/PID `0x1d6b:0x0104` (Linux Foundation multifunction
    composite gadget — the conventional pair for configfs gadgets), serial/
    product strings from the device-tree serial;
  - **NCM function preferred + ECM legacy** as the two configurations
    (Windows 10 1903+/11, macOS, Linux all drive one of them natively; NOT
    RNDIS, per #14);
  - host+device MACs **derived from the board serial** (stable across boots:
    the host OS sees the same adapter every plug);
  - bind first available UDC; `ip addr add 10.55.0.1/29 dev usb0; ip link
    set usb0 up`.
  - Idempotent (re-run safe) and reversible (`stop` arg = full configfs
    teardown in reverse order — #14 explicitly warns teardown discipline is
    what keeps later role-switching possible).
- `deploy/gadget/trainboard-gadget.service`: `Type=oneshot`,
  `RemainAfterExit=yes`, `ExecStart=… start`, `ExecStop=… stop`.
- `deploy/gadget/dnsmasq-usb0.conf` + `trainboard-dnsmasq-usb0.service`:
  second dnsmasq, `bind-interfaces` + `interface=usb0` only, DHCP range
  10.55.0.2–10.55.0.6, 12h leases, `dhcp-option=option:router` **empty** and
  no DNS push (device link, not an internet path — host keeps its normal
  default route; mirrors how the AP dnsmasq is scoped to wlan0 so the two
  instances never fight).
- Failure isolation: gadget unit failure affects nothing else; app crash
  leaves the lifeline up.

### 2. internal/mdns — stdlib mini-responder

- **Responder**: one goroutine per eligible interface family; sockets join
  `224.0.0.251:5353` (IPv4) and `[FF02::FB]:5353` (IPv6) with
  `golang.org/x/net`-free stdlib setup (`net.ListenMulticastUDP` per
  interface). Answers queries for our names; sends unsolicited announcements
  on start and on interface-appear; goodbye packets (TTL 0) on shutdown.
  Announce-only: no probe, no conflict rename (#15).
- **Records**:
  - `trainboard-XXXX.local` A + AAAA (per-interface addresses);
  - `trainboard.local` A + AAAA alias;
  - `_http._tcp.local` PTR → instance `Trainboard XXXX._http._tcp.local`,
    SRV port 80 target `trainboard-XXXX.local`, TXT `path=/`.
- **Wire format**: hand-rolled marshal/unmarshal for exactly these record
  types (A/AAAA/PTR/SRV/TXT + header/name compression on encode only;
  decode tolerates compression). Table-driven tests against golden packets
  captured from a real macOS/avahi exchange.
- **Interface policy** (#15's churn + AP separation): poll `net.Interfaces()`
  every 5s. Eligible = up ∧ multicast-capable ∧ has a usable unicast addr ∧
  NOT (wlan0 while the hotspot is active). Hotspot state arrives via an
  injected `func() bool` seam wired in cmd from the manager's Status — the
  same pattern as the web seams; `internal/mdns` imports neither
  `internal/net` nor `internal/web`.
- **Lifecycle**: started from cmd in both boot paths, registered on the
  watchdog beat aggregate (like poller/manager), context-cancelled shutdown.

### 3. Dongle path + wiring + observability

- deploy.md §9: `allow-hotplug eth0` + `iface eth0 inet dhcp` ifupdown
  stanza (dongle is eth0; NOT wlan0, so zero manager interaction). mDNS
  picks it up via churn handling. Attended hardware validation deferred —
  tracked as the #14 checkbox that stays open.
- cmd: `--mdns` flag, default **true** (`--mdns=false` to disable). Hostname
  suffix from the existing MAC-tail helper (same as AP SSID).
- Status page: interface list gains mDNS state per interface (announcing /
  suppressed-AP / down) via one new nil-tolerated Sources seam.
- Web/e2e: no new HTTP routes; route matrix untouched.

## Testing

- `internal/mdns`: golden packet marshal/unmarshal; responder loop against
  injected fake PacketConns (query→answer, churn→announce, hotspot→wlan0
  silence, shutdown→goodbye). `-race` clean.
- Gadget script: `bash -n` + shellcheck gate (same as bench artifacts);
  idempotency asserted by running start twice in the script's own self-check.
- On-hardware acceptance (attended, Jess): plug into Mac → adapter appears,
  lease granted, `http://10.55.0.1` + `http://trainboard-XXXX.local` +
  plain alias all load; unplug/replug stability; `dns-sd -B _http._tcp`
  shows the service. Documented as a checklist in deploy.md §9.

## Out of scope

- Role switching / host-mode via the single micro-USB port (manual, #14).
- mDNS conflict probing/renaming (#15 explicitly defers).
- Advertising inside the AP (captive DNS owns that world).
- Dongle hardware validation (deferred to hardware availability).
