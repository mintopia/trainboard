# Distribution Round Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make trainboard installable by a stranger: README + MIT license, and a CI pipeline that bakes a flashable Pi Zero 2 W SD image (DietPi + trainboard preinstalled) and publishes it to Cloudflare R2.

**Architecture:** Shell scripts under `deploy/image/` do all the work (fetch base → inject automation → pre-bake boot via systemd-nspawn on an arm64 runner → snapshot/shrink → smoke gate → R2 publish); a new `image.yml` workflow chains them after each release. The shipped image is generic (no WiFi creds) and boots offline into AP-mode provisioning with self-update ready.

**Tech Stack:** Bash (+shellcheck), systemd-nspawn, losetup/parted/e2fsprogs, xz, aws-cli (S3-compatible R2 API), GitHub Actions `ubuntu-24.04-arm` runners.

## Global Constraints

- Hardware target: **Pi Zero 2 W only** (arm64); base image = pinned DietPi RPi-ARMv8 Bookworm
- R2: bucket **`mintopia-github`**, public URL **`https://github-files.mintopia.net`**, ALL keys under the **`trainboard/`** prefix — every upload/copy/prune operation MUST be scoped to that prefix (shared bucket)
- Download URL for docs: `https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz`
- License: MIT, `Copyright (c) 2026 Jessica Smith`
- Shipped image: NO WiFi credentials, NO SSH host keys, NO machine-id; first-boot rootfs resize re-armed; boots offline into AP-mode provisioning
- Install layout (must match docs/deploy.md §Self-update exactly): launcher at `/opt/trainboard/launcher`, slot binary at `/opt/trainboard/slots/a/trainboard` (slot `b` empty), state at `/var/lib/trainboard/updater/state.json` seeded active=a/known_good=a with the release version, unit at `/etc/systemd/system/trainboard.service` with `ExecStart=/opt/trainboard/launcher --production --manage-network`, enabled
- M4 gadget lifeline included: `deploy/gadget/*` installed per deploy.md §10 (script+conf to `/usr/local/lib/trainboard/`, two units enabled, `dtoverlay=dwc2` handling per that section)
- Keyring is embedded in the binaries — no keyring file exists or is installed
- Release binaries come from the GitHub release assets: `trainboard_<tag>_linux_arm64.gz`, `trainboard-launcher_<tag>_linux_arm64.gz`
- No Go code changes; `make check` stays green; all new shell passes `shellcheck`
- Branch: `feat/distribution-image` off `main`

## Sequencing note (spec risk)

Task 4 (pre-bake PoC) is the go/no-go gate for the whole pipeline. Tasks 1–3 are safe regardless; do NOT start Tasks 5–8 until Task 4 has produced a booted, marker-confirmed bake in a real CI run. If DietPi first-run cannot be made to work under nspawn (or the QEMU fallback) after honest effort, STOP and escalate — the spec names the fallback decision (revisit base OS) as the human's call.

---

### Task 1: LICENSE + README

**Files:**
- Create: `LICENSE`
- Create: `README.md`
- Modify: `docs/deploy.md` (top of §1 — add the prebaked-image fast path)

**Interfaces:**
- Produces: the README quick-start references the R2 URL and the AP-setup flow; later tasks do not depend on this task.

- [ ] **Step 1: Write LICENSE**

Standard MIT text, exactly:

```
MIT License

Copyright (c) 2026 Jessica Smith

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 2: Write README.md**

Structure (write real prose, not this outline; keep it tight — the deep guide is docs/deploy.md):

```markdown
# Trainboard

A UK railway departure board for a 256×64 SSD1322 OLED, driven by a
Raspberry Pi Zero 2 W. Live departures from National Rail's Darwin feed,
a phone-friendly web admin UI, hotspot-based first-time setup, mDNS
discovery, signed over-the-air self-updates, and an optional train
headcode column via the RealTime Trains API.

[board photo / preview screenshot — reuse an existing docs/design asset if
present, else a placeholder line noting where to drop one]

## Hardware

| Part | Notes |
|---|---|
| Raspberry Pi Zero 2 W | the image is arm64; original Pi Zero W is not supported |
| SSD1322 256×64 SPI OLED | |
| microSD card, 4GB+ | |

Wiring (BCM numbering): MOSI/SCLK/CS on SPI0 (GPIO10/GPIO11/CE0),
D/C GPIO24, RST GPIO25.

## Install

1. Download the latest SD image:
   https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz
2. Flash it with Raspberry Pi Imager or balenaEtcher (choose "Use custom
   image"; no OS customisation needed — the image is self-configuring).
3. Insert the card and power the Pi. First boot takes a couple of minutes.
4. The board starts its own WiFi hotspot; the panel shows the network name
   and setup address. Join it and open the address in a browser.
5. Set your WiFi, an admin password, your station, and a free Darwin
   token (link) — the board restarts onto your network and shows trains.

You'll need a free Darwin (OpenLDBWS) token: [registration link — copy
from config_network.html's hint]. Optional: a RealTime Trains API account
(api.rtt.io) to show train headcodes.

## Manual install / updating / troubleshooting

See [docs/deploy.md](docs/deploy.md) — flashing a stock DietPi manually,
the A/B slot self-update system, fault codes, USB gadget lifeline.
Updates after install happen from the board's own web UI (or
automatically overnight, if enabled).

## Development

Go ≥1.26. `make check` runs vet + lint + tests. See docs/ for design
docs and ADRs; docs/deploy.md §dev-run for running against fixture data
without hardware.

## License

MIT — see [LICENSE](LICENSE).
```

Verify the Darwin registration URL and wiring values against `internal/web/templates/config_network.html` and `docs/deploy.md` §1 rather than trusting this outline.

- [ ] **Step 3: deploy.md fast path**

At the top of docs/deploy.md §1 ("Flash the SD card"), add a short callout:

```markdown
> **Fast path:** a prebaked image with trainboard preinstalled is published
> for each release — download
> https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz,
> flash it with Raspberry Pi Imager/balenaEtcher, and skip straight to
> first boot: the board comes up in hotspot setup mode (§9) with
> self-update ready. The rest of §1 is the manual path this image bakes
> for you.
```

- [ ] **Step 4: Verify + commit**

Check every link/URL in the README resolves (the R2 latest URL will 404 until the first image publishes — note that in the commit message body, not a reason to omit it).

```bash
git add LICENSE README.md docs/deploy.md
git commit -m "docs: README + MIT license + prebaked-image fast path"
```

---

### Task 2: base-image fetch + boot-partition injection (`build-image.sh` stage 1)

**Files:**
- Create: `deploy/image/BASE_IMAGE` (pinned base URL + sha256, one `KEY=value` per line)
- Create: `deploy/image/build-image.sh`
- Test: shellcheck + a `--stage inject` dry run against a downloaded base (local or CI)

**Interfaces:**
- Produces: `build-image.sh` — single entrypoint with stages, invoked as
  `deploy/image/build-image.sh --tag vX.Y.Z --work <dir> --stage fetch|inject|bake|snapshot|smoke|all`.
  Stage `fetch` leaves `<work>/base.img`; stage `inject` mutates it in place and stages `<work>/assets/` (binaries) onto the boot partition. Later tasks add the remaining stages to THIS script (keep stages as functions: `stage_fetch`, `stage_inject`, `stage_bake`, `stage_snapshot`, `stage_smoke`).
- Consumes: GitHub release assets for `--tag` (downloaded with `gh release download`).

- [ ] **Step 1: Pin the base image**

`deploy/image/BASE_IMAGE`:

```
# Pinned DietPi base for the prebaked image. Bumping this is a reviewed
# change: re-run the full image pipeline on a branch before merging.
URL=https://dietpi.com/downloads/images/DietPi_RPi234-ARMv8-Bookworm.img.xz
SHA256=<fill with the real checksum: download once and sha256sum it>
```

The implementer downloads the current image once and records its real sha256. (DietPi republishes this filename with new content periodically — that is exactly why the checksum is pinned; a mismatch on fetch must be a hard, loud failure telling the operator to re-verify and bump deliberately.)

- [ ] **Step 2: Write `build-image.sh` skeleton + fetch + inject**

```bash
#!/usr/bin/env bash
# Builds the flashable trainboard SD image (spec:
# docs/superpowers/specs/2026-07-10-distribution-image-design.md §2).
# Stages: fetch → inject → bake → snapshot → smoke. Root required from
# inject onward (loop devices); designed for ubuntu-24.04-arm CI runners
# and equally runnable on any arm64 Linux box for debugging.
set -euo pipefail

usage() { echo "usage: $0 --tag vX.Y.Z --work DIR --stage fetch|inject|bake|snapshot|smoke|all" >&2; exit 2; }

TAG= WORK= STAGE=all
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) TAG=$2; shift 2;;
    --work) WORK=$2; shift 2;;
    --stage) STAGE=$2; shift 2;;
    *) usage;;
  esac
done
[ -n "$TAG" ] && [ -n "$WORK" ] || usage
HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$WORK"

# shellcheck source=/dev/null
. "$HERE/BASE_IMAGE"   # provides URL, SHA256

stage_fetch() {
  if [ ! -f "$WORK/base.img" ]; then
    curl -fL --retry 3 -o "$WORK/base.img.xz" "$URL"
    echo "$SHA256  $WORK/base.img.xz" | sha256sum -c -   # hard fail on mismatch
    xz -d "$WORK/base.img.xz"
  fi
  # Release binaries for --tag (gh auth via GH_TOKEN in CI).
  mkdir -p "$WORK/assets"
  gh release download "$TAG" --repo mintopia/trainboard --dir "$WORK/assets" \
    --pattern "trainboard_${TAG}_linux_arm64.gz" \
    --pattern "trainboard-launcher_${TAG}_linux_arm64.gz" --clobber
  gzip -df "$WORK/assets/trainboard_${TAG}_linux_arm64.gz"
  gzip -df "$WORK/assets/trainboard-launcher_${TAG}_linux_arm64.gz"
}

# mount_boot / umount_all helpers: losetup -Pf --show on $WORK/base.img,
# mount ${LOOP}p1 at $WORK/boot (and later ${LOOP}p2 at $WORK/root).
# Always pair with a trap that unmounts + detaches on exit.

stage_inject() {
  mount_boot
  # dietpi.txt: unattended setup, no WiFi creds (flash-sd.sh minus WiFi).
  sed -i \
    -e 's/^AUTO_SETUP_ACCEPTED=.*/AUTO_SETUP_ACCEPTED=1/' \
    -e 's/^AUTO_SETUP_AUTOMATED=.*/AUTO_SETUP_AUTOMATED=1/' \
    -e 's/^AUTO_SETUP_NET_WIFI_ENABLED=.*/AUTO_SETUP_NET_WIFI_ENABLED=0/' \
    -e 's/^AUTO_SETUP_NET_WIFI_COUNTRY_CODE=.*/AUTO_SETUP_NET_WIFI_COUNTRY_CODE=GB/' \
    -e 's/^AUTO_SETUP_NET_HOSTNAME=.*/AUTO_SETUP_NET_HOSTNAME=trainboard/' \
    -e 's/^AUTO_SETUP_HEADLESS=.*/AUTO_SETUP_HEADLESS=1/' \
    -e 's/^CONFIG_NTP_MODE=.*/CONFIG_NTP_MODE=4/' \
    -e 's/^SURVEY_OPTED_IN=.*/SURVEY_OPTED_IN=0/' \
    -e 's|^AUTO_SETUP_CUSTOM_SCRIPT_EXEC=.*|AUTO_SETUP_CUSTOM_SCRIPT_EXEC=/boot/trainboard-install.sh|' \
    "$WORK/boot/dietpi.txt"
  grep -q '^dtparam=spi=on' "$WORK/boot/config.txt" || echo 'dtparam=spi=on' >> "$WORK/boot/config.txt"
  # Stage the install payload where the in-OS hook can reach it.
  install -m 0755 "$HERE/install-trainboard.sh" "$WORK/boot/trainboard-install.sh"
  install -m 0755 "$WORK/assets/trainboard_${TAG}_linux_arm64" "$WORK/boot/trainboard.bin"
  install -m 0755 "$WORK/assets/trainboard-launcher_${TAG}_linux_arm64" "$WORK/boot/trainboard-launcher.bin"
  install -m 0644 "$HERE/../trainboard.service" "$WORK/boot/trainboard.service"
  cp -r "$HERE/../gadget" "$WORK/boot/trainboard-gadget"
  echo "$TAG" > "$WORK/boot/trainboard-version"
  umount_all
}
```

(The exact dietpi.txt keys must be verified against the downloaded base's dietpi.txt — DietPi renames keys between majors; the flash-sd.sh set above is known-good for the pinned Bookworm image. `AUTO_SETUP_CUSTOM_SCRIPT_EXEC` semantics: verify in the same file's comments whether the value is a path or needs `1` + fixed path, and adjust.)

- [ ] **Step 3: Verify**

Run: `shellcheck deploy/image/build-image.sh` → clean.
On any Linux box (or a scratch CI branch run): `sudo deploy/image/build-image.sh --tag <latest-release-tag> --work /tmp/tbimg --stage fetch && sudo ... --stage inject`, then re-mount and eyeball dietpi.txt + staged files. On macOS the loop-mount stages can't run — say so in the report and lean on the CI branch run.

- [ ] **Step 4: Commit**

```bash
git add deploy/image/BASE_IMAGE deploy/image/build-image.sh
git commit -m "feat(image): base fetch + boot-partition automation injection"
```

---

### Task 3: in-OS install hook (`install-trainboard.sh`)

**Files:**
- Create: `deploy/image/install-trainboard.sh`
- Test: `deploy/image/install-trainboard_test.sh` (DESTDIR-based, runs anywhere)

**Interfaces:**
- Consumes: staged files from Task 2 (`/boot/trainboard.bin`, `/boot/trainboard-launcher.bin`, `/boot/trainboard.service`, `/boot/trainboard-gadget/`, `/boot/trainboard-version`).
- Produces: the exact on-disk layout from Global Constraints, then `touch /boot/trainboard-baked` as the completion marker Task 4 polls for. Honours `DESTDIR` (empty in production) and `SRCDIR` (default `/boot`) so the layout logic is unit-testable without root or a Pi.

- [ ] **Step 1: Write the test first**

`install-trainboard_test.sh` (plain bash asserts; no framework):

```bash
#!/usr/bin/env bash
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
mkdir -p "$T/src" "$T/dst"
# Fake staged payload: the "binaries" are scripts that answer --version,
# so state.json seeding works on any host arch.
printf '#!/bin/sh\necho "trainboard v9.9.9 (test)"\n' > "$T/src/trainboard.bin"
printf '#!/bin/sh\n' > "$T/src/trainboard-launcher.bin"
chmod +x "$T/src/trainboard.bin" "$T/src/trainboard-launcher.bin"
cp "$HERE/../trainboard.service" "$T/src/trainboard.service"
cp -r "$HERE/../gadget" "$T/src/trainboard-gadget"
echo v9.9.9 > "$T/src/trainboard-version"

DESTDIR="$T/dst" SRCDIR="$T/src" SKIP_SYSTEMCTL=1 "$HERE/install-trainboard.sh"

fail() { echo "FAIL: $1" >&2; exit 1; }
[ -x "$T/dst/opt/trainboard/launcher" ] || fail "launcher missing"
[ -x "$T/dst/opt/trainboard/slots/a/trainboard" ] || fail "slot a missing"
[ -d "$T/dst/opt/trainboard/slots/b" ] || fail "slot b dir missing"
grep -q '"active": "a"' "$T/dst/var/lib/trainboard/updater/state.json" || fail "state not seeded"
grep -q '"active_version": "v9.9.9"' "$T/dst/var/lib/trainboard/updater/state.json" || fail "version not seeded"
grep -q -- '--production --manage-network' "$T/dst/etc/systemd/system/trainboard.service" || fail "manage-network missing from ExecStart"
[ -x "$T/dst/usr/local/lib/trainboard/trainboard-gadget.sh" ] || fail "gadget script missing"
[ -f "$T/dst/etc/systemd/system/trainboard-gadget.service" ] || fail "gadget unit missing"
[ -f "$T/src/trainboard-baked" ] || fail "completion marker missing"
echo OK
```

- [ ] **Step 2: Run it — expect failure** (`install-trainboard.sh` doesn't exist).

- [ ] **Step 3: Write `install-trainboard.sh`**

```bash
#!/usr/bin/env bash
# Runs INSIDE the image during the CI pre-bake, as DietPi's
# AUTO_SETUP_CUSTOM_SCRIPT_EXEC hook, after DietPi first-run completes.
# Lays down the docs/deploy.md install: A/B slot layout + launcher +
# service (--production --manage-network) + M4 gadget lifeline, then
# marks completion for the bake supervisor. DESTDIR/SRCDIR/SKIP_SYSTEMCTL
# exist so the layout logic is testable off-device.
set -euo pipefail
DESTDIR=${DESTDIR:-}
SRCDIR=${SRCDIR:-/boot}
SLOTS=$DESTDIR/opt/trainboard/slots
STATE_DIR=$DESTDIR/var/lib/trainboard/updater

mkdir -p "$SLOTS/a" "$SLOTS/b" "$STATE_DIR" \
  "$DESTDIR/etc/systemd/system" "$DESTDIR/usr/local/lib/trainboard"

install -m 0755 "$SRCDIR/trainboard-launcher.bin" "$DESTDIR/opt/trainboard/launcher"
install -m 0755 "$SRCDIR/trainboard.bin" "$SLOTS/a/trainboard"

# Seed updater state exactly like deploy/migrate-to-slots.sh: active=a,
# known-good=a, at the shipped release's version.
VERSION=$(tr -d '[:space:]' < "$SRCDIR/trainboard-version")
cat > "$STATE_DIR/state.json.tmp" <<EOF
{
  "active": "a",
  "active_version": "$VERSION",
  "known_good": "a",
  "known_good_version": "$VERSION",
  "boot_attempts": 0,
  "version_floor": "",
  "rolled_back_from": ""
}
EOF
mv "$STATE_DIR/state.json.tmp" "$STATE_DIR/state.json"

# Service unit with the image's flags: AP-mode provisioning needs
# --manage-network (safe here by construction: the image manages wlan0
# from first boot; deploy.md §9's interlock warning is about retrofitting
# devices you currently reach OVER wlan0).
sed 's|^ExecStart=/opt/trainboard/launcher --production$|ExecStart=/opt/trainboard/launcher --production --manage-network|' \
  "$SRCDIR/trainboard.service" > "$DESTDIR/etc/systemd/system/trainboard.service"
grep -q -- '--manage-network' "$DESTDIR/etc/systemd/system/trainboard.service" \
  || { echo "ERROR: ExecStart rewrite failed (unit format changed?)"; exit 1; }

# M4 USB gadget lifeline (deploy.md §10).
install -m 0755 "$SRCDIR/trainboard-gadget/trainboard-gadget.sh" "$DESTDIR/usr/local/lib/trainboard/trainboard-gadget.sh"
install -m 0644 "$SRCDIR/trainboard-gadget/dnsmasq-usb0.conf" "$DESTDIR/usr/local/lib/trainboard/dnsmasq-usb0.conf"
install -m 0644 "$SRCDIR/trainboard-gadget/trainboard-gadget.service" "$DESTDIR/etc/systemd/system/trainboard-gadget.service"
install -m 0644 "$SRCDIR/trainboard-gadget/trainboard-dnsmasq-usb0.service" "$DESTDIR/etc/systemd/system/trainboard-dnsmasq-usb0.service"

if [ -z "${SKIP_SYSTEMCTL:-}" ]; then
  systemctl enable trainboard.service trainboard-gadget.service trainboard-dnsmasq-usb0.service
  # dnsmasq package is required by the usb0 lifeline (deploy.md §10).
  apt-get install -y -qq dnsmasq >/dev/null || echo "WARN: dnsmasq install failed; usb0 lifeline degraded"
fi

touch "$SRCDIR/trainboard-baked"
```

Check deploy.md §10 for anything else it prescribes for the gadget (dwc2 overlay lines in config.txt belong in Task 2's inject stage if required — read §10 and §whatever covers `dtoverlay=dwc2`, and mirror it; if the overlay conflicts with normal USB usage the section will say how it's gated — follow it, don't guess).

- [ ] **Step 4: Run the test — expect OK**; `shellcheck` both scripts → clean.

- [ ] **Step 5: Commit**

```bash
git add deploy/image/install-trainboard.sh deploy/image/install-trainboard_test.sh
git commit -m "feat(image): in-OS install hook laying the deploy.md slot layout"
```

---

### Task 4: pre-bake boot PoC (`stage_bake`) — GO/NO-GO GATE

**Files:**
- Modify: `deploy/image/build-image.sh` (add `stage_bake`)
- Create: `.github/workflows/image-poc.yml` (temporary branch-only workflow; deleted in Task 7)

**Interfaces:**
- Consumes: injected image from Task 2/3.
- Produces: `stage_bake` — boots the image once so DietPi first-run + the install hook complete; exits 0 only when `/boot/trainboard-baked` exists. This is the pipeline's riskiest component; everything after it is routine.

- [ ] **Step 1: Implement `stage_bake` (primary: systemd-nspawn on arm64)**

Shape (the PoC will iterate on the details — flags, marker paths, and DietPi's behaviour in a container are exactly what this task exists to discover):

```bash
stage_bake() {
  mount_all   # loop attach; mount rootfs at $WORK/root, boot at $WORK/root/boot
  # Grow the image by ~1GB first (truncate + parted resizepart + resize2fs)
  # so DietPi's apt work has headroom; the snapshot stage shrinks it back.
  systemd-nspawn --directory="$WORK/root" --boot --machine=tbbake \
    --resolv-conf=replace-host --timezone=off &
  # Poll up to 30 min for $WORK/root/boot/trainboard-baked; then
  # machinectl poweroff tbbake and wait for the nspawn process to exit.
  # On timeout: machinectl terminate, dump the container journal
  # ($WORK/root/var/log/ + journalctl --directory), exit 1.
  umount_all
}
```

Known hazards to work through (budget real time; these are the PoC):
- DietPi's first-run service ordering under nspawn (it targets real hardware; RPi-specific steps — firmware, resize — must skip or tolerate absence). `/boot` inside the container is the mounted FAT partition, so marker/paths line up with real hardware.
- Network inside nspawn on the CI runner (apt + DietPi update need outbound HTTPS).
- DietPi may reboot at the end of first-run phases — nspawn treats reboot as exit; `stage_bake` may need a boot-loop (re-launch until the marker appears or N boots exhausted).

- [ ] **Step 2: Branch CI workflow to prove it**

`.github/workflows/image-poc.yml` (temporary, `workflow_dispatch` only):

```yaml
name: image-poc
on: workflow_dispatch
jobs:
  bake:
    runs-on: ubuntu-24.04-arm
    steps:
      - uses: actions/checkout@v4
      - name: bake
        env: { GH_TOKEN: "${{ github.token }}" }
        run: |
          sudo apt-get update -qq && sudo apt-get install -y -qq systemd-container xz-utils parted
          sudo -E deploy/image/build-image.sh --tag <latest-release-tag> --work /tmp/tbimg --stage fetch
          sudo -E deploy/image/build-image.sh --tag <latest-release-tag> --work /tmp/tbimg --stage inject
          sudo -E deploy/image/build-image.sh --tag <latest-release-tag> --work /tmp/tbimg --stage bake
      - uses: actions/upload-artifact@v4
        if: always()
        with: { name: bake-logs, path: /tmp/tbimg/logs }
```

Push the branch, run it, iterate until the marker-confirmed bake succeeds. Capture what the hazards actually did in the task report.

- [ ] **Step 3: GO/NO-GO**

GO = a CI run whose log shows DietPi first-run completed + `trainboard-baked` present. NO-GO after honest effort (including the qemu-system-aarch64 fallback) = STOP, report BLOCKED with the failure evidence; the base-OS decision goes back to the human.

- [ ] **Step 4: Commit**

```bash
git add deploy/image/build-image.sh .github/workflows/image-poc.yml
git commit -m "feat(image): nspawn pre-bake stage + PoC workflow"
```

---

### Task 5: snapshot + shrink + smoke gate (`stage_snapshot`, `stage_smoke`)

**Files:**
- Modify: `deploy/image/build-image.sh`

**Interfaces:**
- Produces: `stage_snapshot` → `$WORK/trainboard-<tag>.img.xz` + `.sha256`; `stage_smoke` exits non-zero unless every assertion passes. Task 7's workflow calls both.

- [ ] **Step 1: `stage_snapshot`**

After a successful bake, with the image loop-mounted read-write:
- Remove baked identity: `rm -f` SSH host keys (`etc/ssh/ssh_host_*`), truncate `etc/machine-id`, clear `var/log/*`, apt caches, and the staged `/boot` payload (`trainboard.bin`, `trainboard-launcher.bin`, `trainboard.service`, `trainboard-gadget/`, `trainboard-install.sh`, `trainboard-baked` — keep `trainboard-version`), and unset `AUTO_SETUP_CUSTOM_SCRIPT_EXEC` in dietpi.txt so user first boot doesn't re-run the hook.
- Re-arm per-device first boot: SSH host keys regenerate (DietPi/dropbear or openssh regenerates on absence — verify which SSH server the base ships and that absence-regeneration holds; if not, add a oneshot regen unit), and re-enable DietPi's partition-resize first-boot step so the user's SD expands (find the exact mechanism in the baked image — `dietpi-fs_partition_resize` service or `boot/dietpi/func/...` — and re-enable it the way DietPi's own imager does; document what you found in a comment).
- Shrink: `e2fsck -f` then `resize2fs -M` the rootfs, shrink the partition with parted to fs size + margin, truncate the image file, then `xz -9 -T0` to `trainboard-<tag>.img.xz` and `sha256sum > trainboard-<tag>.img.xz.sha256`.

- [ ] **Step 2: `stage_smoke`**

Decompress a COPY, loop-mount read-only, assert (each failure named, all failures reported before exiting 1):
- `etc/systemd/system/trainboard.service` exists, contains `--production --manage-network`, and the `multi-user.target.wants` symlink exists (unit enabled)
- `opt/trainboard/launcher` and `opt/trainboard/slots/a/trainboard` executable; `slots/b` empty dir
- `opt/trainboard/slots/a/trainboard --version` (run from the mount — native arm64) reports exactly `<tag>`
- `var/lib/trainboard/updater/state.json` has `"active": "a"` and `"active_version": "<tag>"`
- gadget units + script present
- NO `etc/ssh/ssh_host_*` files, empty `machine-id`, no `dietpi-wifi.txt` credentials (file absent or SSID empty), `AUTO_SETUP_CUSTOM_SCRIPT_EXEC` unset
- partition-resize re-armed (assert whatever mechanism Step 1 documented)

- [ ] **Step 3: Verify via the PoC workflow** (extend its run through snapshot+smoke on the branch; artifact-upload the final .img.xz so it can be pulled for Jess's hardware acceptance).

- [ ] **Step 4: Commit**

```bash
git add deploy/image/build-image.sh
git commit -m "feat(image): snapshot/shrink + read-only smoke gate"
```

---

### Task 6: R2 publish (`publish-r2.sh`)

**Files:**
- Create: `deploy/image/publish-r2.sh`
- Test: shellcheck; `--dry-run` mode printing every aws command without executing

**Interfaces:**
- Consumes: `$WORK/trainboard-<tag>.img.xz` + `.sha256` from Task 5.
- Produces: objects `trainboard/trainboard-<tag>.img.xz`(+`.sha256`) and alias `trainboard/trainboard-latest.img.xz`(+`.sha256`) in bucket `mintopia-github`; prunes to newest 5 versioned images. Invoked as `publish-r2.sh --tag vX.Y.Z --work DIR [--dry-run]`; requires env `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`.

- [ ] **Step 1: Write it**

```bash
#!/usr/bin/env bash
# Publishes the baked image to Cloudflare R2 (S3 API). The bucket is
# SHARED with other projects: every operation below is scoped to the
# trainboard/ prefix — widening that scope is a bug, not a convenience.
set -euo pipefail
BUCKET=mintopia-github
PREFIX=trainboard
KEEP=5
# arg parsing: --tag/--work/--dry-run; DRY= prefix each aws call via run().
ENDPOINT="https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
export AWS_ACCESS_KEY_ID=$R2_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY=$R2_SECRET_ACCESS_KEY
AWS=(aws --endpoint-url "$ENDPOINT" --region auto)

run() { if [ -n "$DRY" ]; then echo "DRY: $*"; else "$@"; fi; }

IMG="trainboard-${TAG}.img.xz"
run "${AWS[@]}" s3 cp "$WORK/$IMG"        "s3://$BUCKET/$PREFIX/$IMG"
run "${AWS[@]}" s3 cp "$WORK/$IMG.sha256" "s3://$BUCKET/$PREFIX/$IMG.sha256"
# latest alias: server-side copy AFTER the versioned upload succeeded.
run "${AWS[@]}" s3 cp "s3://$BUCKET/$PREFIX/$IMG"        "s3://$BUCKET/$PREFIX/trainboard-latest.img.xz"
run "${AWS[@]}" s3 cp "s3://$BUCKET/$PREFIX/$IMG.sha256" "s3://$BUCKET/$PREFIX/trainboard-latest.img.xz.sha256"

# Prune: list ONLY trainboard/trainboard-v*.img.xz, sort by version, keep
# newest $KEEP, delete the rest (+ their .sha256). sort -V handles semver
# ordering; prerelease tags sort before their release, acceptable here.
"${AWS[@]}" s3api list-objects-v2 --bucket "$BUCKET" --prefix "$PREFIX/trainboard-v" \
  --query 'Contents[].Key' --output text | tr '\t' '\n' \
  | grep -E "^$PREFIX/trainboard-v[^/]+\.img\.xz$" \
  | sort -V | head -n -"$KEEP" \
  | while read -r key; do
      run "${AWS[@]}" s3 rm "s3://$BUCKET/$key"
      run "${AWS[@]}" s3 rm "s3://$BUCKET/$key.sha256"
    done
```

(Flesh out arg parsing/validation; every deletion path must be provably under `$PREFIX/` — assert `case "$key" in "$PREFIX"/*) ;; *) echo "refusing to delete $key"; exit 1;; esac` before each rm.)

- [ ] **Step 2: Verify**

`shellcheck` clean; run with `--dry-run` and fake env vars, eyeball every printed command for prefix scoping (paste the dry-run output into the task report).

- [ ] **Step 3: Commit**

```bash
git add deploy/image/publish-r2.sh
git commit -m "feat(image): R2 publisher scoped to the trainboard/ prefix"
```

---

### Task 7: `image.yml` workflow (replaces the PoC workflow)

**Files:**
- Create: `.github/workflows/image.yml`
- Delete: `.github/workflows/image-poc.yml`

**Interfaces:**
- Consumes: all `deploy/image/` scripts; repo secrets `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/`R2_SECRET_ACCESS_KEY` (may not exist yet — the publish step must skip with a loud warning, not fail the build, so image CI is green before Jess adds them).

- [ ] **Step 1: Write the workflow**

Policy (Jess, 2026-07-11): images are cut **manually, on major versions only** — minor/patch releases reach flashed devices via OTA self-update. So the trigger is `workflow_dispatch` ONLY; no release chaining.

```yaml
# Builds the flashable SD image and publishes it to R2
# (spec: docs/superpowers/specs/2026-07-10-distribution-image-design.md).
# MANUAL trigger by policy: images are cut deliberately on major versions;
# minor releases reach flashed boards via OTA self-update, not new images.
name: image
on:
  workflow_dispatch:
    inputs:
      tag: { description: "release tag to bake (vX.Y.Z)", required: true }
jobs:
  image:
    runs-on: ubuntu-24.04-arm
    steps:
      - uses: actions/checkout@v4
      - name: resolve tag
        id: tag
        run: |
          TAG="${{ inputs.tag }}"
          case "$TAG" in v*) ;; *) echo "not a release tag: $TAG" >&2; exit 1;; esac
          gh release view "$TAG" --repo mintopia/trainboard >/dev/null  # tag must be a real release
          echo "tag=$TAG" >> "$GITHUB_OUTPUT"
        env: { GH_TOKEN: "${{ github.token }}" }
      - name: deps
        run: sudo apt-get update -qq && sudo apt-get install -y -qq systemd-container xz-utils parted awscli
      - name: build image
        env: { GH_TOKEN: "${{ github.token }}" }
        run: sudo -E deploy/image/build-image.sh --tag "${{ steps.tag.outputs.tag }}" --work /tmp/tbimg --stage all
      - name: publish to R2
        env:
          R2_ACCOUNT_ID: ${{ secrets.R2_ACCOUNT_ID }}
          R2_ACCESS_KEY_ID: ${{ secrets.R2_ACCESS_KEY_ID }}
          R2_SECRET_ACCESS_KEY: ${{ secrets.R2_SECRET_ACCESS_KEY }}
        run: |
          if [ -z "$R2_ACCOUNT_ID" ]; then
            echo "::warning::R2 secrets not configured — image built but NOT published"
            exit 0
          fi
          deploy/image/publish-r2.sh --tag "${{ steps.tag.outputs.tag }}" --work /tmp/tbimg
      - uses: actions/upload-artifact@v4
        if: always()
        with: { name: image-logs, path: /tmp/tbimg/logs }
```

(workflow_dispatch only works once the workflow file exists on the default branch — for the branch test, temporarily keep a push trigger scoped to this branch alongside dispatch, and remove it in the same task once verified, OR verify via the still-present PoC workflow mechanics before the swap. Note R2 secrets ARE configured: the branch test performs a REAL publish — run `publish-r2.sh` mentally/`--dry-run` first, confirm prefix scoping, and delete any test-published objects that shouldn't persist, or bake the current latest release so the publish is the real first artifact.)

- [ ] **Step 2: Delete `image-poc.yml`**, verify the workflow end-to-end once on the branch. Expect: green build, real publish under `trainboard/` (secrets are set), image artifact attached. Verify `https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz` serves the upload afterwards.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/image.yml
git rm .github/workflows/image-poc.yml
git commit -m "feat(ci): manual image workflow — bake a release tag, publish to R2"
```

---

### Task 8: gates, docs sync, PR

**Files:**
- Modify: `docs/deploy.md` (image pipeline section), `CONTEXT.md` if it tracks infra terms

- [ ] **Step 1:** `make check` green (should be untouched — no Go changes). Wire shellcheck into the repo gates so image scripts stay linted: add a `shellcheck` target to the Makefile (`shellcheck deploy/image/*.sh deploy/*.sh`), include it in `check`, and add a shellcheck step to ci.yml's build job. If existing `deploy/*.sh` scripts fail shellcheck, scope the target to `deploy/image/*.sh` only and note the debt — do not "fix" scripts outside this round's scope.
- [ ] **Step 2:** deploy.md gains a short "§ Image pipeline" subsection: what the workflow builds, where it publishes, that it is **manually dispatched on major versions only** (minor releases reach devices via OTA), how to run it (`gh workflow run image.yml -f tag=vX.Y.Z`), and how to bump `BASE_IMAGE`. README + deploy.md fast path get one added sentence setting the expectation: the flashed image may be a few releases behind and offers/self-applies the newest update after setup — that's normal.
- [ ] **Step 3:** Push, PR titled `feat: prebaked SD image pipeline + README/LICENSE`, body covering: the nspawn bake findings from Task 4, the `--manage-network`/gadget inclusion rationale, R2 prefix scoping, the two attended steps (R2 secrets; hardware flash acceptance). Standard footer. Request Codex review per repo rules.

---

## Attended steps (Jess)

1. ~~Add repo secrets `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`~~ — **DONE 2026-07-10** (secrets set before implementation started). The workflow keeps the graceful skip-with-warning anyway, for forks and secret rotation gaps. Task 7's branch test can therefore expect a REAL publish — coordinate: use `--dry-run` first or a scratch tag, and verify the objects land under `trainboard/` only.
2. Acceptance: flash a CI-built image (workflow artifact or the R2 URL once live) to a spare SD card, boot the Pi, complete hotspot setup end-to-end, confirm self-update sees the current release.
