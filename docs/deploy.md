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

## 8. Benchmarks

`cmd/bench` measures SSD1322 flush performance on real hardware and gates the
render architecture (full-frame vs. dirty-region flush). Build, ship, and run
it on the target Pi:

```
GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
scp bench pi@trainboard:/tmp/ && ssh pi@trainboard /tmp/bench --frames 300 --hz 16000000
```

See `docs/benchmarks/README.md` for the results table and the decision this
gates.
