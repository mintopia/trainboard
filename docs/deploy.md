# Operator deploy guide

How to flash an SD card, boot the board for the first time, provision it from
its web UI, and install/update the `trainboard` binary. This is the guide the
hardware bench session follows end to end.

## 1. Flash the SD card

> **Fast path:** a prebaked image with trainboard preinstalled is published
> for each release — download
> https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz,
> flash it with Raspberry Pi Imager/balenaEtcher, and skip straight to
> first boot: the board comes up in hotspot setup mode (§9) with
> self-update ready. The rest of §1 is the manual path this image bakes
> for you. Images are cut on major versions only (§12), so the flashed
> image may be a few releases behind — the board offers (or auto-applies,
> if enabled) the newest update after setup finishes; that's normal, not a
> bug.

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

The host's own system timezone doesn't matter and never needs configuring:
the board always renders and compares times in Europe/London (BST-aware),
via a zoneinfo database compiled into the binary — no dependency on the
image shipping `/usr/share/zoneinfo`.

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

**Live board preview:** the old `GET /preview.png` route is gone. The status
page's live board preview now renders client-side from `GET /api/board` (a
JSON row-model of the current scene, polled every 5s) via `static/board.js` —
if you had a personal bookmark or script pointed at `/preview.png`, point it
at `/api/board` instead. The preview is a faithful emulation of the panel:
it uses the panel's own Dot Matrix fonts (subsetted `.woff2`) and mirrors the
OLED's scene geometry and animation timing, and its clock runs on board time
(from `/api/board`'s `time` field), flagging drift against the browser clock.
Station and operator search in the config forms is served offline from
bundled tables (`GET /api/stations?q=`, `GET /api/tocs?q=`) — like
`/api/station`, both are public and work pre-setup. The admin UI's fonts
(Rail Alphabet + Dot Matrix, subsetted `.woff2`) and JS (htmx) are bundled
and served from `/static/` — the page works fully with no internet access
from the browser, same as before.

## 5. Install the binary (first-time bootstrap)

> **This is the one-time bootstrap flow only.** Once a device has been
> migrated to the A/B slot layout (§6), stop using `scp` for updates — cut a
> signed release and apply it from the web UI or let the board pick it up
> on its own. The manual flow below still matters for getting the very
> first binary onto a brand-new device (there's nothing in `/usr/local/bin`
> for `migrate-to-slots.sh` to migrate otherwise), and for the launcher
> binary itself, which is shipped the same way.

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
| `--manage-network` | `false` | Drive wlan0 (STA connect / AP fallback) via the connectivity manager. **Safety interlock — see §9 before enabling.** |
| `--mdns` | `true` | Advertise the board via mDNS as `trainboard-XXXX.local` (see §10) |
| `--version` | | Print version and exit |

The shipped unit runs `trainboard --production` with no `--http` override, so
the admin UI listens on **port 80**. `trainboard.service` runs as `User=root`
already (needed for SPI/GPIO access), so binding `:80` needs no additional
capability grant — this was confirmed while writing this guide; no unit
change was required for the web server.

## 6. Self-update (M5)

From this milestone on, the board updates itself instead of waiting for an
operator with `scp`. A stable **launcher** binary (`ExecStart` in
`deploy/trainboard.service`) execs whichever of two on-disk **slots** (`a`
or `b`) the updater state file names as active, so applying a new release
never touches the running payload's own binary. Releases are published as
a manifest plus asset, both signed with minisign; the payload only ever
installs a manifest whose signature verifies against the device's embedded
keyring, downloads it into the *inactive* slot, and flips `active` to it
immediately once the download and signature check pass. `known_good` is
the field that's confirmation-gated: it only advances to the new slot once
the payload has actually booted there and passed its post-start health
check — if that never happens, the launcher itself flips `active` back to
the last known-good slot on its own, no operator intervention required.
The full design, including the anti-rollback
version floor and the double-fault recovery path, is in
[`docs/superpowers/specs/2026-07-09-m5-self-update-design.md`](superpowers/specs/2026-07-09-m5-self-update-design.md).

### Where things live

| What | Path |
|---|---|
| Launcher (stable shim) | `/opt/trainboard/launcher` |
| Slot binaries | `/opt/trainboard/slots/{a,b}/trainboard` |
| Updater state | `/var/lib/trainboard/updater/state.json` |
| Version floor (repo) | `deploy/release/MIN_VERSION` |

### Migrating an existing device

A device that predates M5 has a single binary at `/usr/local/bin/trainboard`
and no slot layout yet. `deploy/migrate-to-slots.sh` does the one-time move;
copy the launcher over first, then run the script against the Pi:

```
scp trainboard-launcher root@trainboard.local:/opt/trainboard/launcher
ssh root@trainboard.local 'sh -s' < deploy/migrate-to-slots.sh
```

It creates the `a`/`b` slot directories, moves the existing binary into slot
`a`, and seeds `state.json` marking slot `a` both active and known-good at
whatever version that binary reports (a dev build's non-semver `dev`
version is fine here — it never blocks the first real update, it's just
not a version the anti-rollback floor can compare against). It refuses to
run if the launcher isn't in place yet, and is safe to re-run: both the
slot-`a` copy and the state seed are skipped once they already exist.
Finish by installing the updated unit and restarting:

```
scp deploy/trainboard.service root@trainboard.local:/etc/systemd/system/trainboard.service
# IMPORTANT: if your current unit's ExecStart carries extra flags
# (--manage-network), re-add them to the new ExecStart line first.
ssh root@trainboard.local 'systemctl daemon-reload && systemctl restart trainboard'
```

The new unit's `StartLimitIntervalSec=0` line matters here, not just as
boilerplate: without it, systemd's own start-limit can mark the unit
"failed" partway through the rollback ladder (§6 below) before the
launcher's boot-attempt counter gets a chance to converge — an operator
hand-editing an old unit in place instead of copying the new one must add
that line too.

Once the board is confirmed up under the launcher (`systemctl status
trainboard` shows the launcher's PID execing into the payload), delete the
now-unused old binary: `ssh root@trainboard.local rm /usr/local/bin/trainboard`.

### Key ceremony

Run once, on your workstation, before the first release can be cut. Two
minisign key pairs exist by design: a CI signing key that lives unencrypted
in a GitHub Actions secret (so the release workflow can sign headlessly),
and an offline recovery key that never leaves your password manager. Either
key alone is enough to satisfy the device's keyring check, which is what
makes rotating one of them later a normal signed update rather than a
reflash. Builds compiled BEFORE this ceremony runs ship with an empty
keyring and can never self-update — the first post-ceremony build must
reach slot a via the §5 bootstrap `scp` or the migration script below
before self-update starts working on that device.

```
brew install minisign

# CI signing key (unencrypted — it lives in a GitHub Actions secret):
minisign -G -W -f -p ci.pub -s ci.key -c "trainboard CI signing key"
gh secret set MINISIGN_SECRET_KEY < ci.key

# Offline recovery key (password-protected; NEVER uploaded anywhere):
minisign -G -f -p recovery.pub -s recovery.key -c "trainboard recovery key"
# → store recovery.key's CONTENT and its password in the password manager.

# Embed both PUBLIC keys in the device keyring: copy the second line of
# ci.pub and recovery.pub into internal/update/keyring.go's embeddedKeys
# slice, then commit. Finally: rm ci.key recovery.key ci.pub recovery.pub
```

`-W` on the CI key generation skips the passphrase prompt, since a key that
GitHub Actions must use non-interactively can't be protected by one; the
recovery key omits `-W` deliberately, so it's useless to anyone who doesn't
also have its password.

### Cutting a release

```
git tag v0.1.0 && git push origin v0.1.0
```

The release workflow (`.github/workflows/release.yml`) picks up the tag
push, builds and signs the manifest, and publishes a GitHub release. A
prerelease suffix on the tag (`v0.1.0-rc1`) routes it to the `prerelease`
channel instead of `stable` — use that for anything you want on a bench
device before it reaches every board.

`deploy/release/MIN_VERSION` is the anti-rollback floor baked into every
manifest: a device won't accept a manifest whose `min_version` is lower
than the highest floor it's already seen, so replaying an old signed
manifest can't downgrade a board. Bump this file (and cut a release off the
bump) when a security fix or a key rotation means older versions should no
longer be installable — not for routine releases.

### On-hardware test checklist (attended, after first release exists)

- [ ] Migrate the Pi (`migrate-to-slots.sh`), confirm board boots via
      launcher (`systemctl status trainboard` shows launcher → payload PID).
- [ ] Cut `v0.1.0`, then `v0.1.1`; web UI shows "Available v0.1.1"; Apply;
      board restarts into v0.1.1; status shows promoted
      (`known_good_version: v0.1.1` in `state.json`).
- [ ] Pull power mid-download; on reboot the board still runs the old
      version and a re-apply succeeds.
- [ ] Break slot b deliberately (`ssh: echo garbage >
      /opt/trainboard/slots/b/trainboard` after staging an update, before
      restart); observe 3 failed boots then automatic rollback + web UI
      banner.
- [ ] Force a double fault so the ladder actually reaches E07 — corrupting
      a binary so `execve` itself fails (e.g. `echo garbage > ...`) doesn't
      work for this: the launcher's fast-fallback path treats an exec
      failure as instant (single-boot) rollback to known-good, so the
      3-strikes ladder never runs and E07 is never reached (systemd just
      loops the launcher instead). Corrupt both slots in an
      exec-succeeds-but-unhealthy way instead, so each boot burns a real
      attempt against `BootAttempts`:
      ```
      ssh root@trainboard.local systemctl stop trainboard
      ssh root@trainboard.local 'cp /bin/false /opt/trainboard/slots/a/trainboard; cp /bin/false /opt/trainboard/slots/b/trainboard'
      ssh root@trainboard.local systemctl start trainboard
      ```
      Observe the ladder run its full course (3 boots on the active slot,
      rollback, 3 boots on the other slot) and end in E07 on-glass + web UI
      reachable; restore by re-applying a release from the recovery web UI.

## 7. Fault codes

The board shows a short fault code in the corner of the Error / waiting
scenes for field diagnosis (`internal/obs/faults.go`):

| Code | Meaning | What to check |
|---|---|---|
| E01 | Darwin unreachable | Network connectivity from the Pi; Darwin/OpenLDBWS endpoint status |
| E02 | Darwin token rejected | The token entered at `/setup` or `/config` — re-check it's a valid, current OpenLDBWS token |
| E03 | Waiting for time sync | The Pi hasn't got NTP time yet (common right after boot); wait, or check network/NTP reachability |
| E04 | Configuration error | No config file yet, or the stored config fails validation — visit `/setup` (fresh install) or `/config` (existing install) to fix it |
| E05 | WiFi radio blocked | wlan0 is rfkill soft-blocked or its regulatory domain is unset — only surfaced behind `--manage-network` (§9); check for a hardware kill switch, or run `rfkill unblock wifi` / `iw reg set GB` by hand |
| E06 | Network connectivity | The layered connectivity check (association / DHCP / DNS / captive-portal) failed at the stage it names — only surfaced behind `--manage-network` (§9); the board falls back to its own AP hotspot rather than staying stuck here |
| E07 | Update recovery mode | The launcher hit a double fault (both slots failing); the board serves only the web UI + AP. Fix config or apply a known-good release from the web UI, or reflash. |

## 8. Troubleshooting

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

## 9. Connectivity & AP mode (M3, behind `--manage-network`)

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
falls back to the board's own open AP hotspot (`Trainboard-XXXX`, named from
wlan0's MAC) when the configured network can't be reached or none is
configured yet — including on a wholly fresh, unconfigured device (the E04
boot path runs the manager too, purely for AP fallback). The setup AP is
open (no password — issue #44, operator decision, risk accepted): joining
needs no credential. While the AP is up, the panel shows the hotspot's SSID
and address instead of the normal departure board.

This is **off by default** (`--manage-network=false`): the M3a bench Pi's
WiFi stays ifupdown-managed until the M3b migration session explicitly hands
wlan0 over. Only pass `--manage-network` (or edit the systemd unit's
`ExecStart` to add it) after that migration:

1. Confirm the device is reachable some other way (ethernet, physical
   console, or you're comfortable losing WiFi and driving it from the AP).
2. Install `dnsmasq` if it isn't already present (`apt-get install -y
   dnsmasq`) — M3a never runs it; the connectivity manager's AP fallback
   needs it for DHCP + captive DNS on wlan0.
3. Switch NTP to daemon mode **before enabling `--manage-network`** (the bench
   Pi is already converted):
   ```bash
   sed -i 's/CONFIG_NTP_MODE=./CONFIG_NTP_MODE=4/' /boot/dietpi.txt && systemctl enable --now systemd-timesyncd
   ```
   The default `CONFIG_NTP_MODE=2` syncs the clock once at boot only; this
   device's wlan0 comes up later under the app's own network manager, so
   boot-time sync never completes and the clock drifts (there is no RTC to
   persist time). Mode 4 runs systemd-timesyncd as a daemon for ongoing sync.
4. Hand wlan0 over from ifupdown to the connectivity manager — this is a
   one-way step, do it in this order:
   1. Comment out the `iface wlan0 ...` block (and any `wpa-conf`/
      `wpa-ssid` lines under it) in `/etc/network/interfaces`.
   2. `systemctl disable --now ifup@wlan0.service` (or `ifdown wlan0`
      followed by disabling whatever wlan0 unit DietPi's ifupdown
      generated — check `systemctl list-units 'ifup@wlan0*'` if the exact
      unit name differs).
   3. **Only then** add `--manage-network` to the unit's `ExecStart`.
5. `systemctl daemon-reload && systemctl restart trainboard`.
6. Watch `journalctl -u trainboard -f` through the first STA attempt/AP
   fallback before disconnecting your other access path.

From this point on, wlan0 is manager-owned at boot: ifupdown will not touch
it again unless the interfaces-file edit from step 4.1 is reverted, so a
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

**Fault codes E05/E06** (§7) are only ever surfaced behind this flag — with
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

## 10. USB gadget ethernet + mDNS discovery (M4)

A second, independent way to reach the board: plug a USB cable into the Pi
Zero 2 W's OTG **data** port and the board shows up as its own USB network
adapter — no WiFi, no router, no `--manage-network` involvement at all. This
is entirely OS-level (a kernel gadget driver plus two systemd units), so it
works even with wlan0 down, the connectivity manager wedged, or
`trainboard.service` itself dead.

### One-time device prep: the dwc2 overlay

The gadget needs the Broadcom dwc2 USB controller running in peripheral
mode, which the DietPi default `config.txt`/`cmdline.txt` don't enable on
their own. Do this once per device, over SSH, before installing anything
else in this section.

**Find the firmware config first.** On DietPi Bookworm the firmware
partition is mounted at `/boot/firmware/` (verified on the bench device);
older images mount it directly at `/boot/`. `ls /boot/firmware/config.txt`
tells you which layout you have — the commands below assume the Bookworm
path; substitute `/boot/` on older images. Editing a `config.txt` that
isn't on the mounted vfat firmware partition silently does nothing (the
GPU never reads it), and `/boot/config.txt` may not even exist to warn
you — a stray rootfs file gets created and the overlay never applies.

```
ssh root@trainboard.local \
  "grep -qx 'dtoverlay=dwc2' /boot/firmware/config.txt || echo 'dtoverlay=dwc2' >> /boot/firmware/config.txt"
ssh root@trainboard.local \
  "grep -q 'modules-load=dwc2' /boot/firmware/cmdline.txt || sed -i 's/rootwait/rootwait modules-load=dwc2/' /boot/firmware/cmdline.txt"
ssh root@trainboard.local systemctl reboot
```

Both commands are idempotent (safe to re-run) — the `grep` guard means a
second pass is a no-op instead of a duplicate line. `cmdline.txt` is a
**single-line file**: the `sed` above splices `modules-load=dwc2` after the
`rootwait` token on the existing line, it never inserts a newline. A stray
newline there is a classic way to make a Pi fail to boot. **A reboot is
required** — the overlay is only applied at kernel boot, so nothing in the
rest of this section works until the Pi comes back up. After it does,
`ls /sys/class/udc/` must show a controller (e.g. `3f980000.usb`); if it's
empty, the overlay didn't apply — check you edited the mounted firmware
partition (`findmnt /boot/firmware`).

### Install

Ship `deploy/gadget/*` to the Pi and enable the two units. This uses the
same no-scp cat-over-ssh idiom as the `bench` binary in
[`docs/benchmarks/README.md`](benchmarks/README.md) — no `scp` binary needed
on either end, just `ssh` and a redirect:

```
ssh root@trainboard.local mkdir -p /usr/local/lib/trainboard
ssh root@trainboard.local 'cat > /usr/local/lib/trainboard/trainboard-gadget.sh' \
  < deploy/gadget/trainboard-gadget.sh
ssh root@trainboard.local 'cat > /usr/local/lib/trainboard/dnsmasq-usb0.conf' \
  < deploy/gadget/dnsmasq-usb0.conf
ssh root@trainboard.local chmod +x /usr/local/lib/trainboard/trainboard-gadget.sh

ssh root@trainboard.local 'cat > /etc/systemd/system/trainboard-gadget.service' \
  < deploy/gadget/trainboard-gadget.service
ssh root@trainboard.local 'cat > /etc/systemd/system/trainboard-dnsmasq-usb0.service' \
  < deploy/gadget/trainboard-dnsmasq-usb0.service

ssh root@trainboard.local systemctl daemon-reload
ssh root@trainboard.local systemctl enable --now trainboard-gadget trainboard-dnsmasq-usb0
```

`trainboard-gadget.service` builds the gadget's configfs descriptor and
brings up `usb0` and `usb1` (NCM and ECM, respectively — same address on
both, only one is ever active per host); `trainboard-dnsmasq-usb0.service`
(`Requires=`/`After=` the former) runs a DHCP-only dnsmasq scoped to both
interfaces. Both are
`WantedBy=multi-user.target`, so a normal reboot brings the gadget back
without re-running any of this.

### What you get

Plug a USB cable into the Pi's OTG **data** port (the inner micro-USB port —
**not** the PWR-labelled one, which is power-only and has no data lines) into
any laptop or desktop. The host enumerates a native USB network adapter (NCM
on macOS/Windows 11/modern Linux, falling back to the older ECM class on
hosts without NCM support — deliberately never RNDIS, which exists only for
Windows compatibility baggage this project doesn't need) and DHCP-leases an
address in `10.55.0.2`–`10.55.0.6` from the board's `10.55.0.1`. Only one of
NCM (`usb0`) or ECM (`usb1`) is ever active per host, and both carry the
same `10.55.0.1/29` addressing and DHCP range, so this is transparent to the
operator — whichever class the host negotiates, `10.55.0.1` answers the
same way. From there the board answers on:

- `http://10.55.0.1` — the point-to-point gadget address, always works
- `http://trainboard-XXXX.local` — mDNS rides `usb0` exactly like every
  other eligible interface (see below)

Because the gadget is a kernel driver plus two independently-running
systemd units, none of this depends on `trainboard.service` being healthy —
it's there even with wlan0 down, the connectivity manager wedged mid-AP
fallback, or the trainboard binary itself crash-looping.

### Dongle path (wired eth0, hardware validation pending)

If the Pi instead has a USB Ethernet dongle (rather than being plugged in
via its own OTG gadget port), add a standard DHCP-client stanza to
`/etc/network/interfaces`:

```
allow-hotplug eth0
iface eth0 inet dhcp
```

`eth0` is untouched by the connectivity manager — it only ever drives wlan0
— so ifupdown's normal DHCP client handles it exactly as on a stock DietPi
install, and the mDNS responder picks the interface up automatically once
it's up with an address, the same as any other eligible interface. This
path has not yet been exercised against real dongle hardware; **issue #14
stays open on the dongle-validation item** until that hardware pass runs.

### mDNS

The board announces itself over multicast DNS on every eligible interface
(wlan0, `usb0`, and — once validated — a dongle's `eth0`) under two names:

- `trainboard-XXXX.local` — `XXXX` is the last 4 hex characters of wlan0's
  MAC address, uppercased (the same suffix the AP hotspot SSID uses)
- `trainboard.local` — a fixed alias, so the guide's existing
  `ping trainboard.local` / `ssh root@trainboard.local` instructions (§2)
  keep working regardless of which board is on the network
- `_http._tcp` — the admin/config web UI is also advertised as a DNS-SD
  service, so it shows up in service-browser tools, not just as a bare
  hostname

It's on by default; disable it with `--mdns=false` (see the flags table in
§5). **wlan0 goes silent while the AP hotspot is up** (§9) — the hotspot's
own dnsmasq already answers DHCP/DNS on that subnet, and the responder would
just be contending for the same broadcast domain — but `usb0` (and a wired
dongle) keep answering throughout, which is exactly what makes the gadget
useful as a recovery path when wlan0 is mid-AP-fallback.

Verify it from another machine on the same network:

```
dns-sd -B _http._tcp local.          # macOS
avahi-browse -rt _http._tcp          # Linux
```

Both should list the board's service instance; resolving
`trainboard-XXXX.local` or `trainboard.local` (e.g. `ping` or a browser)
should return an address on whichever interface(s) are currently eligible.

### Attended acceptance checklist (#14 / #15)

This is the on-hardware gate — none of it can be verified from a
workstation alone. Run it with a Mac (or other NCM-capable host) and a USB
cable connected to the Pi's OTG data port:

- [ ] The adapter enumerates on the host (visible in System
      Settings/Network or `ip link`) without installing any driver
- [ ] A DHCP lease appears within roughly 5 seconds of plugging in
- [ ] All three URLs load: `http://10.55.0.1`,
      `http://trainboard-XXXX.local`, and `http://trainboard.local`
- [ ] Unplug/replug the cable three times in a row — the board stays
      healthy and re-enumerates cleanly each time, no restart needed
- [ ] `dns-sd -B _http._tcp local.` (or `avahi-browse -rt _http._tcp`)
      shows the board's service on **both** WiFi and `usb0` simultaneously
- [ ] AP mode: bring the hotspot up (§9), confirm `trainboard-XXXX.local`
      does **not** answer on the AP's own subnet (192.168.4.0/24) — wlan0
      suppression working — but **does** still answer over `usb0`

Once this passes, close #14 (minus the dongle-validation note above, which
stays open until a dongle is tested) and #15.

## 11. Benchmarks

`cmd/bench` measures SSD1322 flush performance on real hardware and gates the
render architecture (full-frame vs. dirty-region flush). Build, ship, and run
it on the target Pi:

```
GOOS=linux GOARCH=arm64 go build -o bench ./cmd/bench
scp bench pi@trainboard:/tmp/ && ssh pi@trainboard /tmp/bench --frames 300 --hz 16000000
```

See `docs/benchmarks/README.md` for the results table and the decision this
gates.

## 12. Image pipeline

The `image` GitHub Actions workflow (`.github/workflows/image.yml`) bakes
the prebaked SD image referenced by the §1 fast path. It takes a pinned
DietPi Bookworm ARMv8 base (`deploy/image/BASE_IMAGE`), boots it under
`systemd-nspawn` on an `ubuntu-24.04-arm` (arm64) runner so DietPi's own
unattended first-run install completes for real, then installs the
trainboard launcher + A/B slot layout, the USB gadget lifeline (§10), and
leaves the board ready to boot straight into hotspot setup mode (§9) — the
same state a fresh flash-and-first-boot reaches by hand, just captured
once instead of repeated per device. `deploy/image/build-image.sh` drives
the whole thing in five stages (fetch → inject → bake → snapshot → smoke);
the final smoke stage re-verifies every ship-critical property (identity
scrubbed, slots populated, gadget units present, first-run disarmed) against
the compressed artifact before it's trusted.

The finished image is published to the Cloudflare R2 bucket `mintopia-github`
under the `trainboard/` prefix — never outside it, since the bucket is
shared with other projects — and served publicly at
https://github-files.mintopia.net/trainboard/. Both a versioned object
(`trainboard-vX.Y.Z.img.xz` + `.sha256`) and a `trainboard-latest.img.xz`
alias (the one §1's fast path links) are written; the previous 5 versioned
images are kept, older ones pruned (`deploy/image/publish-r2.sh`).

**Manually dispatched, major versions only.** Baking and publishing a new
image is not part of the normal release flow — minor releases reach
already-flashed boards entirely through self-update (§6). Run the workflow
by hand only when cutting a major version:

```
gh workflow run image.yml -f tag=vX.Y.Z
```

`tag` must name an existing GitHub release; the workflow refuses to run
against a tag that hasn't been released yet.

**Bumping `deploy/image/BASE_IMAGE`.** Changing the pinned DietPi URL/SHA256
is a reviewed change, not a routine edit: `workflow_dispatch` runs against
any ref, so push the bump to a branch first and dispatch the workflow
against it — `gh workflow run image.yml --ref <branch> -f tag=vX.Y.Z` —
and confirm bake + smoke pass before merging the bump into `main`.

**Building locally.** The pipeline needs an arm64 Linux host with
`systemd-nspawn`, `losetup`, `parted`, `xz`, and `gh` on `PATH` — a spare
arm64 machine, or a VM (e.g. a Lima VM on a Mac: `limactl start
--arch=aarch64 default`) works fine for debugging a failed bake without
burning CI minutes:

```
sudo deploy/image/build-image.sh --tag vX.Y.Z --work <dir> --stage all
```

Pass `--stage fetch|inject|bake|snapshot|smoke` instead of `all` to re-run
a single stage against an already-populated `--work` directory.

### Hardware-acceptance checklist

CI's smoke stage only checks what's true of the image on disk — it can't
verify the image actually boots real hardware correctly. Run this once
against the first CI-built image flashed to a spare SD card, and again
after any change to `deploy/image/*.sh` or `BASE_IMAGE`:

- [ ] Boots to hotspot setup mode (§9) — no manual intervention needed
- [ ] No interactive first-boot hang: DietPi's unattended install doesn't
      block on a prompt (the empty `machine-id` / `ConditionFirstBoot` path
      runs clean)
- [ ] SSH host keys regenerate on first boot (the `trainboard-regen-hostkeys`
      dropbear oneshot) — `ssh root@trainboard.local` works and each card
      gets its own keys, not the base image's shared ones
- [ ] Self-update sees the current release as available/newer than what
      shipped
- [ ] The usb0 lifeline answers at `http://10.55.0.1` (§10)

The image ships DietPi's default root password (`dietpi`) unchanged —
change it after setup, the same as any manually-flashed DietPi install.
