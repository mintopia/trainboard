#!/usr/bin/env bash
# Builds the flashable trainboard SD image (spec:
# docs/superpowers/specs/2026-07-10-distribution-image-design.md §2).
# Stages: fetch → inject → bake → snapshot → smoke. Root required from
# inject onward (loop devices); designed for ubuntu-24.04-arm CI runners
# and equally runnable on any arm64 Linux box for debugging.
set -euo pipefail

usage() { echo "usage: $0 --tag vX.Y.Z --work DIR --stage fetch|inject|bake|snapshot|smoke|all" >&2; exit 2; }

TAG=""
WORK=""
STAGE=all
while [ $# -gt 0 ]; do
  case "$1" in
    --tag) [ $# -ge 2 ] || usage; TAG=$2; shift 2;;
    --work) [ $# -ge 2 ] || usage; WORK=$2; shift 2;;
    --stage) [ $# -ge 2 ] || usage; STAGE=$2; shift 2;;
    *) usage;;
  esac
done
[ -n "$TAG" ] && [ -n "$WORK" ] || usage
case "$STAGE" in
  fetch|inject|bake|snapshot|smoke|all) ;;
  *) usage;;
esac

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

# mount_boot / umount_all: losetup -Pf --show on $WORK/base.img, mount
# ${LOOP}p1 at $WORK/boot (later stages additionally mount ${LOOP}p2 at
# $WORK/root — umount_all already accounts for that so bake/snapshot can
# reuse it). LOOP is the single source of truth for "is anything attached
# right now"; the EXIT trap calls umount_all unconditionally so a failure
# partway through inject never leaves a loop device or mount behind.
LOOP=""

mount_boot() {
  [ "$(id -u)" -eq 0 ] || { echo "stage inject requires root (loop devices)" >&2; exit 1; }
  LOOP=$(losetup -Pf --show "$WORK/base.img")
  mkdir -p "$WORK/boot"
  mount "${LOOP}p1" "$WORK/boot"
}

umount_all() {
  # Registered as an unconditional EXIT trap, so this must never abort
  # partway through under set -e: every cleanup step below is made
  # failure-tolerant (falls back to a lazy unmount, then just warns) so
  # `losetup -d` always runs. A `losetup -d` that never happens leaks the
  # loop device for the life of the CI runner and breaks every build after it.
  # stage_bake nests the FAT boot partition inside the mounted rootfs
  # (at /boot/firmware on Bookworm-layout images, /boot on older ones) so
  # the partition appears where the OS's own fstab expects it; unmount the
  # nested mount before its parent rootfs.
  for nested in "$WORK/root/boot/firmware" "$WORK/root/boot"; do
    if mountpoint -q "$nested" 2>/dev/null; then
      umount "$nested" 2>/dev/null || umount -l "$nested" 2>/dev/null || echo "WARN: unmount $nested failed" >&2
    fi
  done
  if mountpoint -q "$WORK/root" 2>/dev/null; then
    umount "$WORK/root" 2>/dev/null || umount -l "$WORK/root" 2>/dev/null || echo "WARN: unmount $WORK/root failed" >&2
  fi
  if mountpoint -q "$WORK/boot" 2>/dev/null; then
    umount "$WORK/boot" 2>/dev/null || umount -l "$WORK/boot" 2>/dev/null || echo "WARN: unmount $WORK/boot failed" >&2
  fi
  if [ -n "$LOOP" ]; then
    losetup -d "$LOOP" 2>/dev/null || echo "WARN: loop detach failed: $LOOP" >&2
    LOOP=""
  fi
}

trap umount_all EXIT

stage_inject() {
  mount_boot
  # dietpi.txt: unattended setup, no WiFi creds (flash-sd.sh minus WiFi).
  #
  # Keys verified against the actual boot partition of the pinned base
  # image (DietPi_RPi234-ARMv8-Bookworm, current as of BASE_IMAGE's SHA256)
  # and cross-checked against dietpi.txt's own in-file comments:
  #   - AUTO_SETUP_ACCEPTED and AUTO_SETUP_HEADLESS do not exist in current
  #     DietPi Bookworm images (absent from the shipped dietpi.txt and from
  #     upstream MichaIng/DietPi master) — dropped, not sed'd, so we don't
  #     carry a no-op line that implies a key that isn't there.
  #     AUTO_SETUP_AUTOMATED=1 alone is the unattended-install gate; there
  #     is no separate "acceptance" flag anymore. The intent behind
  #     AUTO_SETUP_HEADLESS (no desktop) is covered by AUTO_SETUP_DESKTOP,
  #     which we now set explicitly (its default is already "none", but
  #     pinning it guards against a future base bump changing that).
  #   - AUTO_SETUP_CUSTOM_SCRIPT_EXEC is NOT a path field: per dietpi.txt's
  #     own comments it is "0" (run the script at the fixed path
  #     /boot/Automation_Custom_Script.sh if present) or a URL (download +
  #     run). There is no "point it at an arbitrary boot-relative path"
  #     option. We pin it to "0" (already the default) and stage the
  #     install hook at the fixed filename DietPi actually looks for,
  #     below — not at a custom name referenced by this variable.
  #   - AUTO_SETUP_KEYBOARD_LAYOUT is pinned to "gb" per the design spec
  #     (already the upstream default — explicit/defensive, same rationale
  #     as AUTO_SETUP_DESKTOP above). AUTO_SETUP_LOCALE is deliberately left
  #     un-sed'd at its shipped "C.UTF-8" default: flash-sd.sh's manual flow
  #     never sets locale either, so there's no en_GB.UTF-8 precedent to
  #     match here.
  sed -i \
    -e 's/^AUTO_SETUP_AUTOMATED=.*/AUTO_SETUP_AUTOMATED=1/' \
    -e 's/^AUTO_SETUP_NET_WIFI_ENABLED=.*/AUTO_SETUP_NET_WIFI_ENABLED=0/' \
    -e 's/^AUTO_SETUP_NET_WIFI_COUNTRY_CODE=.*/AUTO_SETUP_NET_WIFI_COUNTRY_CODE=GB/' \
    -e 's/^AUTO_SETUP_KEYBOARD_LAYOUT=.*/AUTO_SETUP_KEYBOARD_LAYOUT=gb/' \
    -e 's/^AUTO_SETUP_NET_HOSTNAME=.*/AUTO_SETUP_NET_HOSTNAME=trainboard/' \
    -e 's/^AUTO_SETUP_DESKTOP=.*/AUTO_SETUP_DESKTOP=none/' \
    -e 's/^CONFIG_NTP_MODE=.*/CONFIG_NTP_MODE=4/' \
    -e 's/^SURVEY_OPTED_IN=.*/SURVEY_OPTED_IN=0/' \
    -e 's/^AUTO_SETUP_CUSTOM_SCRIPT_EXEC=.*/AUTO_SETUP_CUSTOM_SCRIPT_EXEC=0/' \
    "$WORK/boot/dietpi.txt"
  grep -q '^dtparam=spi=on' "$WORK/boot/config.txt" || echo 'dtparam=spi=on' >> "$WORK/boot/config.txt"
  # USB gadget lifeline (docs/deploy.md §10 "One-time device prep: the
  # dwc2 overlay"): the dwc2 controller needs peripheral mode enabled in
  # firmware config + kernel cmdline before the M4 gadget units (installed
  # rootfs-side by Task 3's install hook) can bring up usb0. §10 documents
  # this as a one-time manual SSH step against a live device; baking it
  # into the shipped image means it works out of the box on first boot.
  # cmdline.txt is a single-line file — splice after "rootwait" exactly as
  # §10 prescribes, never append a newline (a stray one there fails to boot).
  grep -qx 'dtoverlay=dwc2' "$WORK/boot/config.txt" || echo 'dtoverlay=dwc2' >> "$WORK/boot/config.txt"
  grep -q 'modules-load=dwc2' "$WORK/boot/cmdline.txt" || sed -i 's/rootwait/rootwait modules-load=dwc2/' "$WORK/boot/cmdline.txt"
  # Stage the install payload where the in-OS hook can reach it.
  # Automation_Custom_Script.sh is the fixed filename DietPi's first-run
  # automation actually executes (see AUTO_SETUP_CUSTOM_SCRIPT_EXEC note
  # above) — not a name of our choosing.
  install -m 0755 "$HERE/install-trainboard.sh" "$WORK/boot/Automation_Custom_Script.sh"
  install -m 0755 "$WORK/assets/trainboard_${TAG}_linux_arm64" "$WORK/boot/trainboard.bin"
  install -m 0755 "$WORK/assets/trainboard-launcher_${TAG}_linux_arm64" "$WORK/boot/trainboard-launcher.bin"
  install -m 0644 "$HERE/../trainboard.service" "$WORK/boot/trainboard.service"
  # Re-runs must overwrite, not nest: `cp -r` onto an existing target dir
  # copies gadget/ *into* it (trainboard-gadget/gadget/...) instead of
  # replacing it, so clear any prior copy first.
  rm -rf "$WORK/boot/trainboard-gadget"
  cp -r "$HERE/../gadget" "$WORK/boot/trainboard-gadget"
  echo "$TAG" > "$WORK/boot/trainboard-version"
  umount_all
}

# ── bake ──────────────────────────────────────────────────────────────
# Boot the injected image under systemd-nspawn so DietPi's unattended
# first-run install AND our Automation_Custom_Script.sh hook run to
# completion, exactly as they would on real hardware. The hook touches
# /boot/trainboard-baked on success; that marker (and only that marker) is
# the GO signal. Everything here is deliberately verbose — this is the
# pipeline's riskiest stage and its logs are the whole point.
BAKE_LOG=""
bake_log() { echo "[bake $(date -u +%H:%M:%S)] $*" | tee -a "$BAKE_LOG" >&2; }

# Grow base.img so DietPi's apt/dietpi-software work has headroom (the
# base rootfs ships near-full). Idempotent via a sentinel so re-running
# bake on an already-grown image doesn't stack +1G every time. The
# snapshot stage (Task 5) shrinks the filesystem back down before export.
grow_image() {
  if [ -f "$WORK/.grown" ]; then
    bake_log "image already grown (sentinel present) — skipping"
    return 0
  fi
  bake_log "growing base.img by 1GiB for apt headroom"
  truncate -s +1G "$WORK/base.img"
  local loop
  loop=$(losetup -Pf --show "$WORK/base.img")
  # Extend the rootfs partition (p2) into the new space, re-read the table,
  # fsck (resize2fs refuses a dirty fs), then grow the ext4 filesystem.
  parted -s "$loop" resizepart 2 100% || { losetup -d "$loop"; echo "parted resizepart failed" >&2; exit 1; }
  partprobe "$loop" 2>/dev/null || true
  udevadm settle 2>/dev/null || true
  e2fsck -fy "${loop}p2" || true   # -y: auto-answer; nonzero on fixups is fine
  resize2fs "${loop}p2" || { losetup -d "$loop"; echo "resize2fs failed" >&2; exit 1; }
  losetup -d "$loop"
  touch "$WORK/.grown"
  bake_log "grow complete"
}

# BOOTMNT = where the FAT partition is mounted inside the rootfs tree.
# DietPi Bookworm RPi images use the RPi-OS-Bookworm layout: the FAT
# partition mounts at /boot/firmware, while /boot is an ext4 directory
# holding the DietPi script tree (/boot/dietpi/...). Run 1 of this PoC
# mounted the FAT at /boot, shadowing that tree — every dietpi-*.service
# ExecStart=/boot/dietpi/... then failed instantly and first-run never
# started. Detect the layout from the image's own fstab instead of
# assuming.
BOOTMNT=""

mount_bake() {
  [ "$(id -u)" -eq 0 ] || { echo "stage bake requires root (loop devices + nspawn)" >&2; exit 1; }
  LOOP=$(losetup -Pf --show "$WORK/base.img")
  mkdir -p "$WORK/root"
  mount "${LOOP}p2" "$WORK/root"
  if grep -Eq '^[^#]*[[:space:]]/boot/firmware[[:space:]]' "$WORK/root/etc/fstab"; then
    BOOTMNT="$WORK/root/boot/firmware"
  else
    BOOTMNT="$WORK/root/boot"
  fi
  mkdir -p "$BOOTMNT"
  mount "${LOOP}p1" "$BOOTMNT"
}

# Container-visible prep the real device never needs, plus recording of
# the pre-bake state so it can be restored afterwards:
#  - /etc/.dietpi_hw_model_identifier=75 pins DietPi's hardware detection
#    to "Container" for the duration of the bake. Without it,
#    dietpi-obtain_hw_model's dtb glob (/boot/{,firmware/}bcm*-rpi-*.dtb)
#    detects "RPi", then finds no Revision line in the runner's
#    /proc/cpuinfo and falls back to RPi 1 — whose first-boot path WRITES
#    arm_freq=900/over_voltage overclock lines into the shipped
#    config.txt. Model 75 is DietPi's sanctioned container identity: it
#    skips CPU-governor setup, serial-console setup, NTP mode setup and
#    the RPi clock rewrite. Restored (or removed) after a successful bake
#    so the flashed device re-detects its real RPi model on first boot.
#  - FAT->rootfs config migration: on Bookworm-layout images the copy of
#    dietpi.txt that DietPi actually READS is /boot/dietpi.txt on the
#    ROOTFS; the FAT partition's copy (the one inject edits, and the one
#    users edit after flashing) is migrated over it on first boot by
#    fs_partition_resize.sh — which in a container dies before the
#    migration (its `mount -o remount,rw /` can't resolve the fstab root
#    UUID). Local PoC evidence: firstboot then read the shipped
#    AUTO_SETUP_AUTOMATED=0, never wrote the autologin drop-ins, and the
#    container idled at a login prompt for the whole 30-min budget.
#    Replicate the migration here, host-side, using
#    fs_partition_resize.sh's own file list.
prep_container() {
  local dst="$WORK/logs" hwid="$WORK/root/etc/.dietpi_hw_model_identifier" f
  # Ground truth for the task report, captured before every boot loop.
  cp "$WORK/root/etc/fstab" "$dst/image-fstab.txt" 2>/dev/null || true
  ls -la "$WORK/root/boot" > "$dst/rootfs-boot-listing.txt" 2>/dev/null || true
  if [ -f "$hwid" ] && [ ! -f "$WORK/.hwid.prebake" ]; then
    cp "$hwid" "$WORK/.hwid.prebake"
    bake_log "pre-bake hw_model_identifier: $(cat "$hwid")"
  fi
  echo 75 > "$hwid"
  if [ "$BOOTMNT" = "$WORK/root/boot/firmware" ]; then
    # Same list fs_partition_resize.sh migrates for the RPi /boot/firmware
    # layout on real hardware.
    for f in dietpi.txt dietpi-wifi.txt Automation_Custom_PreScript.sh \
             Automation_Custom_Script.sh unattended_pivpn.conf dietpi-k3s.yaml; do
      if [ -f "$BOOTMNT/$f" ]; then
        rm -f "$WORK/root/boot/$f"   # never write through a stale symlink
        cp "$BOOTMNT/$f" "$WORK/root/boot/$f"
        bake_log "migrated $f from FAT to rootfs /boot (fs_partition_resize is dead in-container)"
      fi
    done
  fi
}

# Undo the container-only identity after a successful bake: the flashed
# device must re-detect its real RPi model on first boot.
unprep_container() {
  local hwid="$WORK/root/etc/.dietpi_hw_model_identifier"
  if [ -f "$WORK/.hwid.prebake" ]; then
    cp "$WORK/.hwid.prebake" "$hwid"
    bake_log "restored pre-bake hw_model_identifier"
  else
    rm -f "$hwid"
    bake_log "removed bake-only hw_model_identifier (image had none)"
  fi
  # /boot/dietpi/.hw_model caches model 75 + a generated hardware UUID
  # from the bake. preboot regenerates it on every boot, but delete it so
  # no two flashed devices share a UUID and nothing reads stale container
  # identity before preboot runs.
  rm -f "$WORK/root/boot/dietpi/.hw_model"
}

# Best-effort capture of what happened inside the container, whatever the
# outcome. The live nspawn console (captured per-boot below) is the richest
# source; these are the persisted extras. DietPi's own logs land in
# /var/lib/dietpi/logs (persistent ext4) — /var/log is a tmpfs (RAMlog)
# whose contents, journal included, evaporate when the container exits.
dump_diagnostics() {
  local dst="$WORK/logs"
  bake_log "collecting diagnostics into $dst"
  # DietPi's first-run transcript is the single most useful artifact.
  for f in dietpi-firstrun-setup.log dietpi-update.log dietpi-firstboot.log \
           fs_partition_resize.log dietpi-ramlog.log; do
    [ -f "$WORK/root/var/lib/dietpi/logs/$f" ] && cp "$WORK/root/var/lib/dietpi/logs/$f" "$dst/" 2>/dev/null || true
  done
  cp "$WORK/root/boot/dietpi/.install_stage" "$dst/install_stage.txt" 2>/dev/null || true
  ls -la "$WORK/root/boot" > "$dst/rootfs-boot-listing-after.txt" 2>/dev/null || true
  ls -la "$BOOTMNT" > "$dst/fat-listing-after.txt" 2>/dev/null || true
  # Make everything the unprivileged runner user can upload: some DietPi
  # log subtrees (ramlog_store/private) ship as mode 700, which breaks
  # actions/upload-artifact's scandir (EACCES). We only copy plain files
  # above, but harden the whole dir regardless.
  chmod -R a+rX "$dst" 2>/dev/null || true
}

stage_bake() {
  mkdir -p "$WORK/logs"
  BAKE_LOG="$WORK/logs/bake.log"
  : > "$BAKE_LOG"
  bake_log "stage bake starting (tag $TAG)"

  command -v systemd-nspawn >/dev/null 2>&1 || { echo "systemd-nspawn not found (install systemd-container)" >&2; exit 1; }

  # Self-heal from a previous aborted bake on the same host (stale machined
  # registration or supervisor would make every launch fail "already
  # exists"/"currently busy"). No-op on a clean CI runner.
  machinectl terminate tbbake 2>/dev/null || true

  grow_image
  mount_bake
  bake_log "mounted: rootfs=${LOOP}p2 -> $WORK/root, boot=${LOOP}p1 -> $BOOTMNT"
  prep_container
  local marker="$BOOTMNT/trainboard-baked"

  local max_boots=5
  local deadline=$(( $(date +%s) + 30 * 60 ))
  local boot=0 baked=0 boot_start
  while [ "$boot" -lt "$max_boots" ]; do
    boot=$((boot + 1))
    if [ -f "$marker" ]; then baked=1; break; fi
    boot_start=$(date +%s)
    local console="$WORK/logs/nspawn-boot-$boot.log"
    bake_log "boot attempt $boot/$max_boots -> $console"
    : > "$console"
    # Stream the container console straight into the CI step log (as well as
    # the file) so a hang is diagnosable without waiting for the artifact —
    # run 2 timed out with the console trapped in a file the failed
    # upload-artifact step never captured.
    tail -n +1 -f "$console" &
    local tailpid=$!
    # No -n/--network-veth: the container shares the host network namespace,
    # so apt + dietpi-update get the runner's outbound HTTPS directly.
    # --resolv-conf=replace-host gives it working DNS. --timezone=off avoids
    # nspawn bind-mounting a host /etc/localtime the RPi image doesn't want.
    # --setenv=TERM=linux: with the console piped to a file, PID 1 inherits
    # TERM=unknown and passes it to console-getty -> the autologin root
    # shell; DietPi's whiptail/tput UI then crashes dietpi-update
    # (arithmetic on empty `tput cols`) and Prompt_on_Failure PERMANENTLY
    # disarms the automation (AUTO_SETUP_AUTOMATED=0 + autologin removal).
    # Local PoC evidence, boot 1 of the migration-fix iteration.
    systemd-nspawn --directory="$WORK/root" --boot --machine=tbbake \
      --resolv-conf=replace-host --timezone=off --setenv=TERM=linux \
      > "$console" 2>&1 &
    local npid=$!

    while kill -0 "$npid" 2>/dev/null; do
      if [ -f "$marker" ]; then
        bake_log "marker appeared during boot $boot — powering off container"
        machinectl poweroff tbbake 2>/dev/null || kill -TERM "$npid" 2>/dev/null || true
      elif [ "$(date +%s)" -ge "$deadline" ]; then
        bake_log "HARD TIMEOUT — terminating container"
        machinectl terminate tbbake 2>/dev/null || kill -KILL "$npid" 2>/dev/null || true
        wait "$npid" 2>/dev/null || true
        kill "$tailpid" 2>/dev/null || true
        dump_diagnostics
        echo "stage bake: timed out before trainboard-baked marker appeared" >&2
        exit 1
      fi
      sleep 5
    done
    wait "$npid" 2>/dev/null || true
    kill "$tailpid" 2>/dev/null || true
    bake_log "boot $boot ended (nspawn exited after $(( $(date +%s) - boot_start ))s)"
    if [ -f "$marker" ]; then baked=1; break; fi
    # A container that lives under 15s never even reached DietPi's earliest
    # possible mid-first-run reboot — that's nspawn failing to launch (e.g.
    # a stale directory lock). Relaunching would burn the remaining boots
    # on the same error; fail fast instead.
    if [ $(( $(date +%s) - boot_start )) -lt 15 ]; then
      dump_diagnostics
      echo "stage bake: container exited within 15s of launch — nspawn launch failure, see $console" >&2
      exit 1
    fi
    bake_log "no marker yet after boot $boot (DietPi likely rebooted mid-first-run) — relaunching"
  done

  dump_diagnostics
  if [ "$baked" -eq 1 ]; then
    bake_log "GO: trainboard-baked present after $boot boot(s)"
    unprep_container
    umount_all
    return 0
  fi
  echo "stage bake: exhausted $max_boots boots without trainboard-baked marker" >&2
  exit 1
}
stage_snapshot() { echo "not implemented" >&2; exit 3; }
stage_smoke() { echo "not implemented" >&2; exit 3; }

case "$STAGE" in
  fetch) stage_fetch;;
  inject) stage_inject;;
  bake) stage_bake;;
  snapshot) stage_snapshot;;
  smoke) stage_smoke;;
  all)
    stage_fetch
    stage_inject
    # bake/snapshot/smoke are stubs (exit 3) until later tasks land, which
    # makes the calls below genuinely unreachable today per shellcheck's
    # flow analysis — disabled rather than removed, since they're the real
    # pipeline order once those stages exist.
    # shellcheck disable=SC2317
    stage_bake
    # shellcheck disable=SC2317
    stage_snapshot
    # shellcheck disable=SC2317
    stage_smoke
    ;;
esac
