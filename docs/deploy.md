# Operator deploy guide

How to flash an SD card, boot the board for the first time, provision it from
its web UI, and install/update the `trainboard` binary. This is the guide the
hardware bench session follows end to end.

## 1. Flash the SD card

Requires macOS, a DietPi Bookworm ARMv8 image for the Pi 2/3/4 family
(`DietPi_RPi234-ARMv8-Bookworm.img.xz`), and a spare SD card in a USB reader.

```
sudo ./deploy/flash-sd.sh DietPi_RPi234-ARMv8-Bookworm.img.xz disk4
```

Replace `disk4` with the actual disk identifier from `diskutil list` — the
script refuses to run against anything that isn't removable media, and asks
you to type the disk id back to confirm before it erases anything.

The script prompts for your WiFi SSID/password, then:

- writes `dietpi.txt` for headless automated setup, hostname `trainboard`,
  WiFi country `GB`
- writes the WiFi credentials into `dietpi-wifi.txt`
- appends `dtparam=spi=on` to `config.txt` (required for the SSD1322 over
  SPI0.0)

### Panel wiring (BCM numbering)

The panel follows luma.core's `spi()` defaults, inherited from the original
Python build (see `reference/assets/pi-display-connections_bb.png`):

| Signal | Pin |
|---|---|
| MOSI / SCLK / CS | SPI0 (GPIO10 / GPIO11 / CE0) |
| D/C | GPIO24 |
| RST | GPIO25 |

**This destroys all data on the target disk.** Double-check the disk
identifier before confirming.

## 2. First boot

1. Insert the card into the Pi Zero 2 W and power it on. DietPi's automated
   setup runs unattended — this takes several minutes and the Pi reboots
   itself partway through.
2. Find it on the network: `ping trainboard.local` (mDNS), or check your
   router's DHCP client list.
3. SSH in if needed: `ssh root@trainboard.local` (password `dietpi` —
   change it).

Once DietPi's setup finishes, `trainboard.service` starts automatically. On a
fresh install there's no config file yet, so the OLED shows the **Error**
scene with fault code **E04** (configuration error) — this is expected and
means the board is up and waiting to be configured, not a fault to
investigate.

## 3. First-run provisioning (`/setup`)

Browse to `http://trainboard.local/` from a device on the same LAN. With no
admin password set yet, every route redirects to `/setup`, which collects:

- **admin password** (min 8 characters) — used for all subsequent logins
- **origin CRS** — the three-letter station code the board departs from
- **Darwin token** — your National Rail Darwin OpenLDBWS access token

Submitting `/setup` writes these into the config file, issues you a session,
and shows a "Setup complete, restarting" page — it does **not** drop you
straight onto the status page. That's deliberate: the process that served
`/setup` was running with no valid config at all (the OLED's static E04
scene, no poller), so nothing would ever start fetching until something
restarts it. Submitting `/setup` schedules that restart, the same
apply-by-restart used by every `/config` save: `trainboard.service`'s
`Restart=always` relaunches the process a couple of seconds later, it loads
the config you just wrote, E04 clears, and the board starts fetching. Wait
for the restart page's countdown to bounce you back to `/` — you'll land on
`/login` — then log back in with the password you just set to fine-tune
things at `/config`.

Running the binary directly in dev mode (no systemd) instead just exits after
`/setup`; nothing restarts it for you, so you'll need to relaunch it by
hand.

> The admin UI is served over **plain HTTP** — see
> [ADR 0004](adr/0004-plain-http-admin-on-trusted-lan.md) for why, and what
> mitigates it (session auth, CSRF, Origin/Host checks, rate limiting,
> write-only secret fields, log/response redaction). Only do this over a LAN
> you trust.

## 4. Day-to-day configuration (`/config`)

Once logged in, `/config` exposes the rest of the board's settings
(destinations, scenes, powersaving schedule, etc.).

**Apply-by-restart semantics:** saving `/config` does not hot-reload state in
the running process. The handler writes the new config to disk and then the
process exits cleanly (`os.Exit(0)`); `trainboard.service`'s `Restart=always`
immediately relaunches it, which picks up the new file on the next `Load`.
Expect a brief (roughly 1-2 second) blip in board rendering and web UI
availability right after saving — this is normal, not a crash. The same
apply-by-restart path is used by the `/actions/restart` button.

`/actions/reboot` is different: it shells out to `systemctl reboot`, so it
restarts the whole Pi, not just the process.

## 5. Install / update the binary

Cross-compile on your workstation and ship the binary over:

```
GOOS=linux GOARCH=arm64 go build \
  -ldflags "-X github.com/mintopia/trainboard/internal/buildinfo.version=vX.Y.Z" \
  -o trainboard ./cmd/trainboard

scp trainboard root@trainboard.local:/usr/local/bin/trainboard
```

Install (or update) the systemd unit:

```
scp deploy/trainboard.service root@trainboard.local:/etc/systemd/system/trainboard.service
ssh root@trainboard.local systemctl daemon-reload
ssh root@trainboard.local systemctl enable --now trainboard
```

`systemctl restart trainboard` (or the `/actions/restart` web button) picks
up a newly-copied binary or config without a full reboot.

### Where things live

| What | Path |
|---|---|
| Binary | `/usr/local/bin/trainboard` |
| Config | `/var/lib/trainboard/config.json` (override with `--config`) |
| Systemd unit | `/etc/systemd/system/trainboard.service` |

### `cmd/trainboard` flags

| Flag | Default | Purpose |
|---|---|---|
| `--config` | `/var/lib/trainboard/config.json` | Config file path |
| `--production` | `false` | Drive the real SSD1322 over SPI (omit on a dev host to get PNG preview mode instead) |
| `--preview-dir` | `./preview` | Where preview PNGs are written in non-production mode |
| `--fixture` | *(none)* | Path to a JSON board fixture, bypassing live Darwin (dev only) |
| `--http` | `:80` | Address the embedded config/status web server listens on |
| `--manage-network` | `false` | Drive wlan0 (STA connect / AP fallback) via the connectivity manager. **Safety interlock — see §8 before enabling.** |
| `--version` | | Print version and exit |

The shipped unit runs `trainboard --production` with no `--http` override, so
the admin UI listens on **port 80**. `trainboard.service` runs as `User=root`
already (needed for SPI/GPIO access), so binding `:80` needs no additional
capability grant — this was confirmed while writing this guide; no unit
change was required for the web server.

## 6. Fault codes

The board shows a short fault code in the corner of the Error / waiting
scenes for field diagnosis (`internal/obs/faults.go`):

| Code | Meaning | What to check |
|---|---|---|
| E01 | Darwin unreachable | Network connectivity from the Pi; Darwin/OpenLDBWS endpoint status |
| E02 | Darwin token rejected | The token entered at `/setup` or `/config` — re-check it's a valid, current OpenLDBWS token |
| E03 | Waiting for time sync | The Pi hasn't got NTP time yet (common right after boot); wait, or check network/NTP reachability |
| E04 | Configuration error | No config file yet, or the stored config fails validation — visit `/setup` (fresh install) or `/config` (existing install) to fix it |
| E05 | WiFi radio blocked | wlan0 is rfkill soft-blocked or its regulatory domain is unset — only surfaced behind `--manage-network` (§8); check for a hardware kill switch, or run `rfkill unblock wifi` / `iw reg set GB` by hand |
| E06 | Network connectivity | The layered connectivity check (association / DHCP / DNS / captive-portal) failed at the stage it names — only surfaced behind `--manage-network` (§8); the board falls back to its own AP hotspot rather than staying stuck here |

## 7. Troubleshooting

**Board renders fine but the web UI is unreachable at
`http://trainboard.local/`:** the render loop and the web server run
independently (a web server failure is logged, not fatal to the process), so
this usually means the HTTP listener failed to bind. Check the journal:

```
ssh root@trainboard.local journalctl -u trainboard -e
```

Look for a "web server exited" log line with a bind/address-in-use error —
most commonly something else already holding port 80, or (if `--http` was
overridden) a typo'd address.

**mDNS (`trainboard.local`) not resolving:** fall back to finding the Pi's IP
via your router's DHCP client list and browse to that directly.

**Board unexpectedly shows E04 and the web UI offers `/setup` again on an
already-configured device:** a missing config file is expected on a fresh
install, but on a device that's been running, this combination means the
config file has become corrupted or unreadable, and the board has
deliberately reopened first-boot provisioning so it can be fixed. Treat this
as a corrupted config, not routine first-boot: until `/setup` is completed,
the device is claimable by anyone who can reach it on the LAN. Re-run setup
promptly to restore a valid config and close that window.

## 8. Connectivity & AP mode (M3, behind `--manage-network`)

> **Do not enable `--manage-network` on any device whose WiFi you currently
> rely on to reach it, until it has been through the M3b bench migration
> below.** This is a one-way safety interlock, not a preference: the
> connectivity manager takes wlan0 away from ifupdown/`wpa_supplicant`'s
> normal system management and drives it directly (association, DHCP, AP
> fallback), and it will happily tear down the very STA connection you're
> SSH'd in over if the layered check decides it's unhealthy.

The M3 connectivity manager owns wlan0 end to end: it attempts the WiFi
network configured at `/config` (`wifi.ssid` / `wifi.psk`), verifies it with
a layered check (association → DHCP → DNS → captive-portal detection), and
falls back to the board's own WPA2 AP hotspot (`Trainboard-XXXX`, named from
wlan0's MAC) when the configured network can't be reached or none is
configured yet — including on a wholly fresh, unconfigured device (the E04
boot path runs the manager too, purely for AP fallback). While the AP is up,
the panel shows the hotspot's SSID/password/address instead of the normal
departure board.

This is **off by default** (`--manage-network=false`): the M3a bench Pi's
WiFi stays ifupdown-managed until the M3b migration session explicitly hands
wlan0 over. Only pass `--manage-network` (or edit the systemd unit's
`ExecStart` to add it) after that migration:

1. Confirm the device is reachable some other way (ethernet, physical
   console, or you're comfortable losing WiFi and driving it from the AP).
2. Install `dnsmasq` if it isn't already present (`apt-get install -y
   dnsmasq`) — M3a never runs it; the connectivity manager's AP fallback
   needs it for DHCP + captive DNS on wlan0.
3. Hand wlan0 over from ifupdown to the connectivity manager — this is a
   one-way step, do it in this order:
   1. Comment out the `iface wlan0 ...` block (and any `wpa-conf`/
      `wpa-ssid` lines under it) in `/etc/network/interfaces`.
   2. `systemctl disable --now ifup@wlan0.service` (or `ifdown wlan0`
      followed by disabling whatever wlan0 unit DietPi's ifupdown
      generated — check `systemctl list-units 'ifup@wlan0*'` if the exact
      unit name differs).
   3. **Only then** add `--manage-network` to the unit's `ExecStart`.
4. `systemctl daemon-reload && systemctl restart trainboard`.
5. Watch `journalctl -u trainboard -f` through the first STA attempt/AP
   fallback before disconnecting your other access path.

From this point on, wlan0 is manager-owned at boot: ifupdown will not touch
it again unless the interfaces-file edit from step 3.1 is reverted, so a
crash-looped `trainboard.service` means wlan0 sits idle rather than falling
back to ifupdown's own DHCP client.

**Before running the migration on real hardware**, run the bench protocol in
[`deploy/bench/`](../deploy/bench/): `eval-mode2.sh` exercises AP bring-up,
10x AP↔STA toggling, and the bad-PSK AP-restore invariant behind a dead-man
switch that restores the pre-bench network config and reboots on its own if
the session goes sideways, and `destructive-matrix.md` walks the harder
failure-injection rows (daemon crashes, DHCP timeout, reboot mid-transition,
etc. — issue #13). The mode2-vs-hostapd verdict is **not** decided by this
doc — it lands as an ADR 0003 addendum after that bench session runs, not
before.

Because this bench session runs *before* the ifupdown migration above,
`eval-mode2.sh` detects that wlan0 is still ifupdown/system-managed and, once
its own dead-man switch is armed and verified, stops the system
`wpa_supplicant` and `ifdown`s wlan0 itself for the duration of the run so
the driver under test isn't fighting the wrong daemon (see the script's step
1b/2b). It hands wlan0 back to ifupdown on a normal exit, and the dead-man's
standalone restore script does the same if the session itself goes
sideways — either way a pre-migration Pi comes back ifupdown-managed. Treat
the bench session itself like the migration's SSH warning above: drive it
over ethernet/console, or accept that wlan0 is unavailable to you for its
duration.

**Fault codes E05/E06** (§6) are only ever surfaced behind this flag — with
it off, wlan0 is left to the OS as before and neither can occur.

**Recovering WiFi after provisioning (AP fallback).** Once a board has been
provisioned it will not re-run first-time setup, so `/setup`'s WiFi form is
gone. If the configured network later becomes unreachable (bad PSK, router
swap, outage) the board drops back to its own hotspot. To recover: rejoin the
`Trainboard-XXXX` hotspot (or let the captive-portal sheet pop) and open
`http://192.168.4.1/` — `/setup` now shows a small read-only status page with
the last join error. From there log in at `http://192.168.4.1/login` and use
**Retry WiFi now** on the actions page (or fix `wifi.ssid`/`wifi.psk` at
`/config` first). Two caveats worth knowing: (1) any HTTP request from a
client on the AP subnet counts as provisioning activity and *defers* the
manager's periodic auto-retry, so a phone left sitting on the status page can
delay a spontaneous rejoin — the **Retry WiFi now** action always bypasses
this suppression, so use it rather than waiting; (2) the `192.168.4.1`
address and the AP subnet are hardcoded to `192.168.4.0/24` (see
`internal/web`'s `apSetupURL` and the captive-portal probe handlers) — a board
brought up on a different AP subnet would not match these on-screen URLs.

**Watchdog:** `trainboard.service` already ships with `WatchdogSec=30` (see
the unit file) regardless of `--manage-network`; the internal aggregator
(`internal/obs.Watchdog`) only pets systemd once every 10s tick where the
render loop, poller, *and* (when `--manage-network` is set) the connectivity
manager have all beaten within their own deadlines. If the manager's `Run`
loop ever exits — its own doc calls this "no safe software recovery from
neither STA nor a verified AP coming up" — its heartbeat is deliberately
never re-registered, so the next unhealthy watchdog tick lets systemd reboot
the unit. That reboot is the intended escalation path, not a bug: don't
"fix" a report of the board rebooting after a `manager Run exited` log line
by silencing it without first checking what actually failed (radio,
firmware, hardware).

## 9. Benchmarks

`cmd/bench` measures SSD1322 flush performance on real hardware and gates the
render architecture (full-frame vs. dirty-region flush). Build, ship, and run
it on the target Pi:

```
GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
scp bench pi@trainboard:/tmp/ && ssh pi@trainboard /tmp/bench --frames 300 --hz 16000000
```

See `docs/benchmarks/README.md` for the results table and the decision this
gates.
