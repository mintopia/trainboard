# Distribution Round — README, License, CI SD-Card Image → R2

**Date:** 2026-07-10
**Status:** Approved
**Scope:** Make the project installable by a stranger: README, MIT license,
install docs, and a CI pipeline that produces a flashable SD-card image for
the Pi Zero 2 W, published to Cloudflare R2.

## Decisions (from brainstorming)

- **Hardware target: Pi Zero 2 W only** (arm64). No ARMv6/original Zero W
  support — it would double the build matrix and the fleet is Zero 2 W.
- **Image hosting: R2 only.** GitHub Releases keep binaries + manifest as
  today; the image is not attached to releases.
- **Image base: DietPi, pre-baked in CI.** CI boots the DietPi rootfs once
  (systemd-nspawn on a native arm64 runner; QEMU fallback) with network so
  its automated first-run completes, installs trainboard, then shrinks and
  snapshots. The shipped image boots offline
  straight into AP-mode provisioning. This matches the OS the fleet runs.
- **License: MIT, Copyright (c) 2026 Jessica Smith** (confirmed).

## 1. README + LICENSE

- `LICENSE`: MIT, `Copyright (c) 2026 Jessica Smith`.
- `README.md`:
  - What it is: a UK train departure board on a 256×64 SSD1322 OLED driven
    by a Pi Zero 2 W, with live Darwin data, a web admin UI, AP-mode setup,
    mDNS, and signed self-updates. Board screenshot/photo up top.
  - Hardware: Pi Zero 2 W, SSD1322 256×64 SPI OLED, wiring table (BCM: SPI0
    MOSI/SCLK/CE0, D/C GPIO24, RST GPIO25) — same table as docs/deploy.md.
  - Quick start (image path): download `trainboard-latest.img.xz` from the
    R2 URL → flash with Raspberry Pi Imager or balenaEtcher → power on →
    join the board's setup hotspot → open the printed URL → set WiFi,
    admin password, station, Darwin token.
  - Requirements: free Darwin (OpenLDBWS) token; optional RTT credentials
    for headcodes.
  - Development: Go toolchain version, `make check`, fixture dev-run
    pointer, repo layout one-liner, links into docs/ (deploy, design, ADRs).
  - License section pointing at LICENSE.
- README links to docs/deploy.md rather than duplicating the operator
  guide; deploy.md gains a "flash the prebaked image" fast path section
  alongside the existing manual flow.

## 2. Image build pipeline (`.github/workflows/image.yml`)

Trigger: release tag push, running after (and gated on) the existing
release job, so the image embeds the exact released binary; plus
`workflow_dispatch` (with a tag input) for retries/backfills.

Steps (implemented as scripts under `deploy/image/` so they run locally as
well as in CI):

1. **Fetch base:** download the pinned DietPi RPi-ARMv8 Bookworm image
   (pinned URL + checksum committed; bumping the base is a reviewed change).
2. **Inject automation:** mount the FAT boot partition; write `dietpi.txt`
   for unattended setup — hostname `trainboard`, locale/keyboard GB,
   `CONFIG_WIFI_COUNTRY_CODE=GB`, survey off, `dtparam=spi=on` in
   config.txt. No WiFi credentials — the shipped image is generic.
3. **Pre-bake boot:** boot the image's rootfs once in CI so DietPi's
   automated first-run completes. Primary mechanism: `systemd-nspawn
   --boot` on a native arm64 runner (`ubuntu-24.04-arm` — no emulation
   needed); fallback if DietPi's first-run misbehaves in a container:
   qemu-system-aarch64. Wait for completion via a marker file. The
   trainboard install runs as DietPi's own post-setup hook
   (`AUTO_SETUP_CUSTOM_SCRIPT_EXEC` pointing at a script staged on the
   boot partition in step 2): it lays down exactly what docs/deploy.md
   prescribes — slots directory + launcher + released arm64 binary in the
   active slot + seeded updater state.json + the systemd unit enabled
   with `--production --manage-network` (AP-mode provisioning is the
   image's whole first-boot story) + the M4 USB-gadget lifeline units
   (usb0 at 10.55.0.1) — then writes a completion marker so step 4 knows
   the bake succeeded. The minisign keyring is embedded in the binaries
   (internal/update/keyring.go), so there is no keyring file to install.
   **Self-update works out of the box on first boot.**
4. **Snapshot:** clean shutdown; fsck both partitions; reset first-boot
   state so per-device identity (SSH host keys, machine-id) regenerates on
   the user's first boot; shrink the rootfs; zero free space; truncate;
   compress to `trainboard-vX.Y.Z.img.xz`; emit `.sha256`.
5. **Smoke gate:** loop-mount the final artifact read-only and assert: the
   systemd unit is enabled (with `--manage-network`), launcher + slot
   binary present and the slot binary's `--version` output matches the tag
   (executable natively on the arm64 runner), updater state seeded
   (active=a), first-boot resize re-armed, and no WiFi credentials or SSH
   host keys baked in. Publishing is gated on this check.

Risk note: the pre-bake boot is the novel piece. The plan must sequence a
standalone proof-of-concept script (runnable locally and in a branch CI
run) before the workflow wiring, with a fallback decision point: if DietPi
first-run under QEMU proves unworkable, revisit the base-OS decision rather
than forcing it.

## 3. R2 publishing

- Existing bucket **`mintopia-github`**, public at
  **https://github-files.mintopia.net** — all trainboard artifacts under
  the **`trainboard/`** prefix. CI must scope every operation (upload,
  copy, prune) to that prefix; the bucket is shared with other projects.
- S3-compatible API (rclone or aws-cli in CI). Secrets: `R2_ACCOUNT_ID`,
  `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`. **Attended step for Jess:**
  create an API token scoped to the bucket, add the three repo secrets.
- Keys: `trainboard/trainboard-vX.Y.Z.img.xz` + `.sha256`, and a
  `trainboard/trainboard-latest.img.xz` (+`.sha256`) alias updated by
  server-side copy after a successful versioned upload. README quick-start
  URL: `https://github-files.mintopia.net/trainboard/trainboard-latest.img.xz`.
- Retention: CI prunes to the newest 5 versioned images under the
  `trainboard/` prefix only (images are ~300-500MB).

## 4. Testing & gates

- Image scripts get shellcheck (added to lint if not already covering
  deploy/) and, where logic warrants, bats-style or Go-driven unit tests
  for pure helpers; the smoke-mount gate is the pipeline's own regression
  test.
- README quick start is verified end-to-end once by flashing a CI-built
  image onto real hardware (attended acceptance with Jess's Pi + a spare
  SD card).
- No Go code changes expected; `make check` stays the repo gate.
- GitHub issues + milestone for the round; Codex review at the end.

## Out of scope

- ARMv6 / original Pi Zero W support
- Attaching the image to GitHub Releases
- OS-level update mechanism for the image (self-update covers the app;
  OS refresh = reflash)
