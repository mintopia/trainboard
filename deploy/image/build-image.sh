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

# e2fsck's exit status is a bitmask, not a simple pass/fail:
#   0  = clean
#   1  = filesystem errors CORRECTED
#   2  = errors corrected, reboot advised
#   4  = errors left UNCORRECTED
#   8  = operational error   16 = usage/syntax   32 = cancelled by user
# `-y` auto-answers every prompt, so 1/2 (fixed) are expected and fine on a
# loop-mounted image; anything >=4 means the fsck could not make the fs
# consistent and every downstream step (resize2fs, the shipped rootfs) would
# inherit the damage — that must hard-fail. `... || true` (the shape this
# replaced) masked 4/8/16 too, which is exactly the class we must NOT ship.
e2fsck_ok() {
  local dev=$1 rc=0
  e2fsck -fy "$dev" || rc=$?
  if [ "$rc" -ge 4 ]; then
    echo "e2fsck: $dev could not be made consistent (exit $rc, bit >=4 set)" >&2
    return 1
  fi
  return 0
}

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
  # stage_bake and stage_snapshot nest the FAT boot partition inside the
  # mounted rootfs (at /boot/firmware on Bookworm-layout images, /boot on
  # older ones) so the partition appears where the OS's own fstab expects
  # it; stage_smoke does the same against a decompressed COPY under
  # $WORK/smoke-root. Unmount every nested FAT mount before its parent
  # rootfs, for both roots.
  for base in "$WORK/root" "$WORK/smoke-root"; do
    for nested in "$base/boot/firmware" "$base/boot"; do
      if mountpoint -q "$nested" 2>/dev/null; then
        umount "$nested" 2>/dev/null || umount -l "$nested" 2>/dev/null || echo "WARN: unmount $nested failed" >&2
      fi
    done
    if mountpoint -q "$base" 2>/dev/null; then
      umount "$base" 2>/dev/null || umount -l "$base" 2>/dev/null || echo "WARN: unmount $base failed" >&2
    fi
  done
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
  e2fsck_ok "${loop}p2" || { losetup -d "$loop"; echo "grow: e2fsck failed" >&2; exit 1; }
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
  # DietPi's first-run connectivity gate (dietpi-globals G_CHECK_NET) does
  # `ping -4c1 CONFIG_CHECK_CONNECTION_IP` (default 9.9.9.9). GitHub-hosted
  # runners block outbound ICMP to the internet, so that ping fails,
  # dietpi-update aborts, and Prompt_on_Failure FORCES an interactive
  # whiptail prompt that blocks unattended until the hard timeout — run 4's
  # exact death. Point the gate at loopback for the bake only; the TCP/DNS/
  # HTTPS that dietpi-update actually uses are unaffected (verified: apt +
  # GitHub reachable from the container). Reverted in unprep, and inert on
  # the device regardless (install_stage=2 skips first-run there, and
  # fs_partition_resize re-migrates the untouched FAT copy on real boot).
  # Also point the IPv6 leg at loopback: G_CHECK_NET skips it when the runner
  # has no IPv6 default route (the usual GH-hosted case, so normally a no-op),
  # but if a runner ever has a global IPv6 address with ICMP still filtered
  # the ping to CONFIG_CHECK_CONNECTION_IPV6 (2620:fe::fe) would abort just
  # like the IPv4 leg — ::1 is always reachable. Both reverted in unprep.
  local dietpitxt="$WORK/root/boot/dietpi.txt"
  if [ -f "$dietpitxt" ]; then
    [ -f "$WORK/.conn_ip.prebake" ] || grep -m1 '^CONFIG_CHECK_CONNECTION_IP=' "$dietpitxt" > "$WORK/.conn_ip.prebake" 2>/dev/null || true
    [ -f "$WORK/.conn_ipv6.prebake" ] || grep -m1 '^CONFIG_CHECK_CONNECTION_IPV6=' "$dietpitxt" > "$WORK/.conn_ipv6.prebake" 2>/dev/null || true
    sed -i \
      -e 's/^CONFIG_CHECK_CONNECTION_IP=.*/CONFIG_CHECK_CONNECTION_IP=127.0.0.1/' \
      -e 's/^CONFIG_CHECK_CONNECTION_IPV6=.*/CONFIG_CHECK_CONNECTION_IPV6=::1/' \
      "$dietpitxt"
    bake_log "bake-only: CONFIG_CHECK_CONNECTION_IP{,V6} -> loopback (runner blocks outbound ICMP)"
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
  # Revert the bake-only ICMP-gate overrides to the image's shipped values.
  if [ -f "$WORK/.conn_ip.prebake" ] && [ -s "$WORK/.conn_ip.prebake" ]; then
    sed -i "s|^CONFIG_CHECK_CONNECTION_IP=.*|$(cat "$WORK/.conn_ip.prebake")|" "$WORK/root/boot/dietpi.txt"
    bake_log "restored CONFIG_CHECK_CONNECTION_IP to shipped value"
  fi
  if [ -f "$WORK/.conn_ipv6.prebake" ] && [ -s "$WORK/.conn_ipv6.prebake" ]; then
    sed -i "s|^CONFIG_CHECK_CONNECTION_IPV6=.*|$(cat "$WORK/.conn_ipv6.prebake")|" "$WORK/root/boot/dietpi.txt"
    bake_log "restored CONFIG_CHECK_CONNECTION_IPV6 to shipped value"
  fi
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
  # Stale-marker footgun (local iteration): the boot loop treats an existing
  # marker as an instant GO *without booting*. On a fresh CI runner the image
  # is pristine so this never bites, but re-running bake against a locally
  # re-used $WORK (or a base.img already baked once) would false-GO on the old
  # receipt and skip the actual install. Simplest fix: delete any pre-existing
  # marker up front so every bake re-proves the install from scratch.
  rm -f "$marker"

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
    # --resolv-conf=off KEEPS the image's shipped /etc/resolv.conf
    # (nameserver 9.9.9.9) instead of stamping the runner's over it — GH
    # runners ship a 127.0.0.53 systemd-resolved stub that is meaningless
    # inside the container and would break DNS. --timezone=off avoids
    # nspawn bind-mounting a host /etc/localtime the RPi image doesn't want.
    # --setenv=TERM=linux: with the console piped to a file, PID 1 inherits
    # TERM=unknown and passes it to console-getty -> the autologin root
    # shell; DietPi's whiptail/tput UI then crashes dietpi-update
    # (arithmetic on empty `tput cols`) and Prompt_on_Failure PERMANENTLY
    # disarms the automation (AUTO_SETUP_AUTOMATED=0 + autologin removal).
    # Local PoC evidence, boot 1 of the migration-fix iteration.
    systemd-nspawn --directory="$WORK/root" --boot --machine=tbbake \
      --resolv-conf=off --timezone=off --setenv=TERM=linux \
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
# ── snapshot ──────────────────────────────────────────────────────────
# Turn the baked image into a shippable, per-device-fresh artifact:
#   1. scrub the baked identity (SSH host keys, machine-id) so no two
#      flashed cards share it, and RE-ARM regeneration for the real first
#      boot (install_stage=2 means DietPi's first-run never runs on device,
#      so anything DietPi's firstboot would normally do we must arm here);
#   2. strip the FAT staging payload + logs + apt caches;
#   3. shrink the rootfs back down (bake grew it +1GiB) and xz it into
#      trainboard-<tag>.img.xz (+ .sha256).
# stage_smoke re-verifies every ship-critical property on a decompressed
# COPY — snapshot does the work, smoke refuses to trust it.

SNAP_TAG="[snapshot]"
snap_log() { echo "$SNAP_TAG $*" >&2; }

# The per-device SSH-host-key regeneration unit written into the rootfs.
# WHY THIS IS REQUIRED (verified against the baked image, not assumed):
# the shipped SSH server is Dropbear, started by dropbear.service with
#   ExecStart=/usr/sbin/dropbear -EF -p 22 $DROPBEAR_EXTRA_ARGS
# and DROPBEAR_EXTRA_ARGS is empty — no `-r` (host-key file) and no `-R`
# (auto-generate). Empirically confirmed: with the host keys removed,
# `dropbear -EF -p 22` exits immediately with "No hostkeys available.
# 'dropbear -R' may be useful or run dropbearkey." — it does NOT
# self-regenerate. DietPi normally generates keys during its first-run,
# but this image ships install_stage=2 so first-run never executes on the
# device. Without this unit, deleting the baked keys (which we MUST, or
# every flashed card ships the base image's Jun-2025 keys) would leave SSH
# permanently dead. This oneshot regenerates each key iff absent, ordered
# before dropbear starts. It is idempotent (the `-s` guard) so it is inert
# on every boot after the first.
REGEN_UNIT_NAME="trainboard-regen-hostkeys.service"
write_regen_unit() {
  local unit="$WORK/root/etc/systemd/system/$REGEN_UNIT_NAME"
  cat > "$unit" <<'EOF'
[Unit]
Description=Regenerate Dropbear SSH host keys on first boot if absent
Documentation=trainboard SD-image snapshot (deploy/image/build-image.sh)
Before=dropbear.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'for t in rsa ecdsa ed25519; do f="/etc/dropbear/dropbear_${t}_host_key"; [ -s "$f" ] || /usr/bin/dropbearkey -t "$t" -f "$f"; done'

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$unit"
  # Enable it the way `systemctl enable` would, but offline (no running
  # systemd in this loop-mount): the [Install] WantedBy symlink.
  local wants="$WORK/root/etc/systemd/system/multi-user.target.wants"
  mkdir -p "$wants"
  ln -sf "../$REGEN_UNIT_NAME" "$wants/$REGEN_UNIT_NAME"
  snap_log "installed + enabled $REGEN_UNIT_NAME (dropbear does not self-regenerate keys)"
}

stage_snapshot() {
  [ "$(id -u)" -eq 0 ] || { echo "stage snapshot requires root (loop devices)" >&2; exit 1; }
  [ -f "$WORK/base.img" ] || { echo "stage snapshot: $WORK/base.img missing (run bake first)" >&2; exit 1; }
  mount_bake   # rootfs -> $WORK/root, FAT -> $BOOTMNT (Bookworm: /boot/firmware)
  local r="$WORK/root"
  [ -f "$BOOTMNT/trainboard-baked" ] || { echo "stage snapshot: no trainboard-baked marker — image is not baked" >&2; exit 1; }
  snap_log "scrubbing baked identity + re-arming per-device first boot"

  # --- Identity scrub -------------------------------------------------
  # Dropbear host keys: the base image ships a fixed set (dated at base
  # build time, NOT regenerated during the bake) — shared across every
  # download of this image. Remove so each device gets its own via the
  # regen unit below.
  rm -f "$r"/etc/dropbear/dropbear_*_host_key
  # OpenSSH keys: this base uses Dropbear, but strip any OpenSSH host keys
  # defensively so a future base swap can't silently ship shared keys.
  rm -f "$r"/etc/ssh/ssh_host_*
  # machine-id: TRUNCATE (do not delete). systemd PID 1 regenerates an
  # empty /etc/machine-id during early boot (documented: an empty file is
  # the canonical "reset for reprovisioning" trigger, and ConditionFirstBoot
  # keys off it) — deleting it instead can trip services that stat the path.
  : > "$r/etc/machine-id"
  chmod 0444 "$r/etc/machine-id" 2>/dev/null || true
  # dbus machine-id: absent on this base (dbus reads /etc/machine-id). If a
  # future base ships a separate real file, reset it too; never touch a
  # symlink (that IS the /etc/machine-id indirection).
  if [ -f "$r/var/lib/dbus/machine-id" ] && [ ! -L "$r/var/lib/dbus/machine-id" ]; then
    : > "$r/var/lib/dbus/machine-id"
    snap_log "reset /var/lib/dbus/machine-id"
  fi
  write_regen_unit

  # --- Strip the FAT staging payload ---------------------------------
  # These were only needed so the in-image install hook could run during
  # the bake. Keep trainboard-version (a harmless human-readable receipt of
  # what shipped). trainboard-baked is a bake receipt with no on-device
  # meaning — drop it.
  local f
  for f in trainboard.bin trainboard-launcher.bin trainboard.service \
           Automation_Custom_Script.sh trainboard-baked; do
    rm -f "$BOOTMNT/$f"
  done
  rm -rf "$BOOTMNT/trainboard-gadget"
  # The install hook was ALSO migrated onto the rootfs /boot by
  # prep_container during the bake — remove that copy too, so it can never
  # be re-run. (Belt to install_stage=2's braces: with install_stage=2
  # DietPi's first-run automation, the only thing that executes
  # Automation_Custom_Script.sh, never runs on the device regardless.)
  rm -f "$r/boot/Automation_Custom_Script.sh"
  snap_log "removed FAT staging payload (kept trainboard-version)"

  # --- Logs + apt caches ---------------------------------------------
  # /var/log: clear file contents but keep the directory tree (RAMlog and
  # services expect their dirs to exist).
  find "$r/var/log" -type f -exec sh -c ': > "$1"' _ {} \; 2>/dev/null || true
  rm -rf "$r"/var/lib/dietpi/logs/* 2>/dev/null || true
  rm -f "$r"/var/cache/apt/archives/*.deb "$r"/var/cache/apt/archives/partial/*.deb 2>/dev/null || true
  rm -f "$r"/var/lib/apt/lists/*Packages* "$r"/var/lib/apt/lists/*Release* 2>/dev/null || true
  rm -f "$r"/root/.bash_history "$r"/home/*/.bash_history 2>/dev/null || true
  snap_log "cleared logs + apt caches"

  # --- Sanity assertions on what the device will boot from ------------
  # unprep_container (bake) already restored the real connectivity-check
  # IPs and the resize service is expected still-armed — but assert, don't
  # trust, before we shrink and ship. (stage_smoke re-checks all of this on
  # the compressed artifact; these are the cheap in-place guards.)
  if grep -q '^CONFIG_CHECK_CONNECTION_IP=127\.' "$BOOTMNT/dietpi.txt" "$r/boot/dietpi.txt" 2>/dev/null; then
    echo "stage snapshot: connectivity-check IP still points at loopback (bake unprep did not run?)" >&2
    exit 1
  fi
  # NB: the enable is an ABSOLUTE symlink (-> /etc/systemd/system/...), so
  # `-e` follows it to the HOST root and wrongly reports missing inside a
  # loop-mount; `-L` (symlink present) is the correct armed test here.
  if [ ! -L "$r/etc/systemd/system/local-fs.target.wants/dietpi-fs_partition_resize.service" ]; then
    echo "stage snapshot: dietpi-fs_partition_resize.service is NOT armed — the user's SD would never expand" >&2
    exit 1
  fi
  snap_log "verified: connectivity IPs real + partition-resize armed"

  umount_all   # release the loop before re-attaching for the offline shrink

  shrink_and_compress
}

# Shrink the rootfs to (minimum + margin), pull the partition boundary in
# to match, truncate the image file, then xz it. Runs on its own loop
# device (fs must be UNmounted for resize2fs to shrink).
shrink_and_compress() {
  local img="$WORK/base.img" loop
  snap_log "shrinking rootfs (bake grew it +1GiB)"
  loop=$(losetup -Pf --show "$img")
  # resize2fs refuses a dirty fs; shrink to minimum, then grow back by a
  # small margin so the shipped rootfs is not 100% full before the on-device
  # partition-resize runs.
  e2fsck_ok "${loop}p2" || { losetup -d "$loop"; exit 1; }
  resize2fs -M "${loop}p2" || { losetup -d "$loop"; echo "resize2fs -M failed" >&2; exit 1; }
  local blk_size min_blocks margin_blocks target_blocks
  blk_size=$(dumpe2fs -h "${loop}p2" 2>/dev/null | awk -F: '/Block size/{gsub(/[[:space:]]/,"",$2);print $2}')
  min_blocks=$(dumpe2fs -h "${loop}p2" 2>/dev/null | awk -F: '/Block count/{gsub(/[[:space:]]/,"",$2);print $2}')
  # ~32 MiB of slack.
  margin_blocks=$(( 32 * 1024 * 1024 / blk_size ))
  target_blocks=$(( min_blocks + margin_blocks ))
  resize2fs "${loop}p2" "$target_blocks" || { losetup -d "$loop"; echo "resize2fs grow-to-margin failed" >&2; exit 1; }
  e2fsck_ok "${loop}p2" || { losetup -d "$loop"; exit 1; }

  # Pull partition 2's boundary in to match the shrunk filesystem. parted's
  # `resizepart` REFUSES to shrink in --script mode (it prints "Shrinking a
  # partition can cause data loss" and answers its own prompt No → non-zero),
  # so drive the MBR table with `sfdisk -N 2` instead, which is fully
  # non-interactive. Keep the existing start sector and type; only the size
  # changes. resize2fs already made the fs smaller than the new boundary, so
  # no data is at risk. Work in 512-byte sectors, aligned to 1 MiB (2048
  # sectors) for SD-card friendliness.
  local sect=512 align=2048 p2_start_b start_sect fs_sect size_sect mib=$(( 1024 * 1024 ))
  p2_start_b=$(parted -sm "$loop" unit B print | awk -F: '/^2:/{gsub(/B/,"",$2);print $2}')
  start_sect=$(( p2_start_b / sect ))
  local fs_bytes
  fs_bytes=$(( target_blocks * blk_size ))
  fs_sect=$(( (fs_bytes + sect - 1) / sect ))
  # Round the partition size up to the alignment grid, and guarantee it is
  # >= the filesystem.
  size_sect=$(( ( (fs_sect + align - 1) / align ) * align ))
  echo "size=${size_sect}" | sfdisk --no-reread --no-tell-kernel -N 2 "$loop" \
    || { losetup -d "$loop"; echo "sfdisk -N 2 resize failed" >&2; exit 1; }
  losetup -d "$loop"

  # Truncate the image to just past the end of partition 2 (MBR table, no
  # trailing secondary GPT to preserve).
  local end_sect=$(( start_sect + size_sect ))
  local new_size=$(( end_sect * sect ))
  truncate -s "$new_size" "$img"
  snap_log "image shrunk to $(( new_size / mib )) MiB (rootfs fs $(( fs_bytes / mib )) MiB + margin)"

  # Compress. -T0 auto-splits into per-thread blocks; -9 for size (the
  # artifact Jess flashes). Keep base.img (-k via -c redirect) so a re-run
  # of smoke could still reach it if needed.
  local out="$WORK/trainboard-${TAG}.img.xz"
  snap_log "compressing -> $out (xz -9 -T0)"
  xz -9 -T0 -c "$img" > "$out"
  ( cd "$WORK" && sha256sum "trainboard-${TAG}.img.xz" > "trainboard-${TAG}.img.xz.sha256" )
  snap_log "wrote $out ($(du -h "$out" | cut -f1)) + .sha256"
}

# ── smoke ─────────────────────────────────────────────────────────────
# Read-only acceptance gate on the SHIPPED artifact. Decompress a fresh
# copy, verify its checksum, loop-mount read-only, and assert every
# ship-critical property. Every failed assertion is reported (not just the
# first) before exiting 1, so one CI run surfaces the full list.
SMOKE_FAILS=0
smoke_assert() {
  # smoke_assert "<description>" <test-command...>
  local desc=$1; shift
  if "$@"; then
    echo "  PASS: $desc" >&2
  else
    echo "  FAIL: $desc" >&2
    SMOKE_FAILS=$(( SMOKE_FAILS + 1 ))
  fi
}

stage_smoke() {
  [ "$(id -u)" -eq 0 ] || { echo "stage smoke requires root (loop devices)" >&2; exit 1; }
  local xzimg="$WORK/trainboard-${TAG}.img.xz"
  [ -f "$xzimg" ] || { echo "stage smoke: $xzimg missing (run snapshot first)" >&2; exit 1; }

  echo "$SMOKE_TAG verifying checksum of $xzimg" >&2
  ( cd "$WORK" && sha256sum -c "trainboard-${TAG}.img.xz.sha256" ) \
    || { echo "stage smoke: checksum mismatch on $xzimg" >&2; exit 1; }

  local simg="$WORK/smoke.img"
  echo "$SMOKE_TAG decompressing a fresh copy -> $simg" >&2
  rm -f "$simg"
  xz -dc "$xzimg" > "$simg"

  LOOP=$(losetup -Pf --show "$simg")
  local sr="$WORK/smoke-root"
  mkdir -p "$sr"
  mount -o ro "${LOOP}p2" "$sr"
  local sboot
  if grep -Eq '^[^#]*[[:space:]]/boot/firmware[[:space:]]' "$sr/etc/fstab"; then
    sboot="$sr/boot/firmware"
  else
    sboot="$sr/boot"
  fi
  mkdir -p "$sboot"
  mount -o ro "${LOOP}p1" "$sboot"
  echo "$SMOKE_TAG mounted read-only: rootfs=$sr FAT=$sboot" >&2
  echo "$SMOKE_TAG === assertions ===" >&2

  # --- service unit -------------------------------------------------
  local svc="$sr/etc/systemd/system/trainboard.service"
  smoke_assert "trainboard.service present" test -f "$svc"
  smoke_assert "trainboard.service has --production --manage-network" \
    grep -q -- '^ExecStart=/opt/trainboard/launcher --production --manage-network$' "$svc"
  smoke_assert "trainboard.service enabled (multi-user.target.wants symlink)" \
    test -L "$sr/etc/systemd/system/multi-user.target.wants/trainboard.service"

  # --- A/B slots + launcher ------------------------------------------
  smoke_assert "launcher executable" test -x "$sr/opt/trainboard/launcher"
  smoke_assert "slots/a/trainboard executable" test -x "$sr/opt/trainboard/slots/a/trainboard"
  smoke_assert "slots/b is an empty dir" smoke_empty_dir "$sr/opt/trainboard/slots/b"

  # slot-a --version must equal the tag (native arm64 exec off the mount).
  local ver rc=0
  ver=$("$sr/opt/trainboard/slots/a/trainboard" --version 2>/dev/null) || rc=$?
  if [ "$rc" -eq 0 ] && printf '%s' "$ver" | grep -qw -- "$TAG"; then
    echo "  PASS: slots/a/trainboard --version reports $TAG (got: $ver)" >&2
  else
    echo "  FAIL: slots/a/trainboard --version != $TAG (rc=$rc, got: $ver)" >&2
    SMOKE_FAILS=$(( SMOKE_FAILS + 1 ))
  fi

  # --- updater state -------------------------------------------------
  local state="$sr/var/lib/trainboard/updater/state.json"
  smoke_assert "state.json present" test -f "$state"
  smoke_assert "state.json active=a" grep -Eq '"active"[[:space:]]*:[[:space:]]*"a"' "$state"
  smoke_assert "state.json active_version=$TAG" \
    grep -Eq "\"active_version\"[[:space:]]*:[[:space:]]*\"$TAG\"" "$state"

  # --- gadget lifeline ----------------------------------------------
  smoke_assert "trainboard-gadget.service present" test -f "$sr/etc/systemd/system/trainboard-gadget.service"
  smoke_assert "trainboard-dnsmasq-usb0.service present" test -f "$sr/etc/systemd/system/trainboard-dnsmasq-usb0.service"
  smoke_assert "gadget helper script present" test -f "$sr/usr/local/lib/trainboard/trainboard-gadget.sh"
  smoke_assert "dnsmasq binary present in rootfs" test -x "$sr/usr/sbin/dnsmasq"

  # --- identity scrub -----------------------------------------------
  smoke_assert "no Dropbear host keys shipped" smoke_no_glob "$sr/etc/dropbear/dropbear_"'*_host_key'
  smoke_assert "no OpenSSH host keys shipped" smoke_no_glob "$sr/etc/ssh/ssh_host_"'*'
  smoke_assert "machine-id is empty" smoke_empty_file "$sr/etc/machine-id"
  smoke_assert "host-key regen unit present" test -f "$sr/etc/systemd/system/$REGEN_UNIT_NAME"
  smoke_assert "host-key regen unit enabled" \
    test -L "$sr/etc/systemd/system/multi-user.target.wants/$REGEN_UNIT_NAME"
  smoke_assert "no WiFi credentials (dietpi-wifi.txt SSID empty/absent)" smoke_no_wifi "$sboot" "$sr"

  # --- automation hook cannot re-run --------------------------------
  smoke_assert "no Automation_Custom_Script.sh on FAT" test ! -e "$sboot/Automation_Custom_Script.sh"
  smoke_assert "no Automation_Custom_Script.sh on rootfs /boot" test ! -e "$sr/boot/Automation_Custom_Script.sh"
  smoke_assert "install_stage=2 (DietPi first-run disarmed on device)" \
    smoke_file_eq "$sr/boot/dietpi/.install_stage" 2

  # --- connectivity IPs real in BOTH dietpi.txt copies ---------------
  smoke_assert "FAT dietpi.txt connectivity IP is not loopback" smoke_ip_not_loopback "$sboot/dietpi.txt"
  smoke_assert "rootfs dietpi.txt connectivity IP is not loopback" smoke_ip_not_loopback "$sr/boot/dietpi.txt"

  # --- partition resize armed ---------------------------------------
  # `-L` not `-e`: the enable is an absolute symlink that `-e` would chase
  # to the host root inside this loop-mount.
  smoke_assert "dietpi-fs_partition_resize.service armed (SD expands on first boot)" \
    test -L "$sr/etc/systemd/system/local-fs.target.wants/dietpi-fs_partition_resize.service"

  # --- receipt kept -------------------------------------------------
  smoke_assert "trainboard-version receipt present on FAT" test -f "$sboot/trainboard-version"
  smoke_assert "trainboard-version == $TAG" smoke_file_eq "$sboot/trainboard-version" "$TAG"

  umount_all

  echo "$SMOKE_TAG === $SMOKE_FAILS failure(s) ===" >&2
  [ "$SMOKE_FAILS" -eq 0 ] || { echo "stage smoke: $SMOKE_FAILS assertion(s) failed" >&2; exit 1; }
  echo "$SMOKE_TAG GO: all assertions passed for $TAG" >&2
}
SMOKE_TAG="[smoke]"

# Smoke assertion helpers (each returns 0/1 for smoke_assert).
smoke_empty_dir() { [ -d "$1" ] && [ -z "$(ls -A "$1" 2>/dev/null)" ]; }
smoke_empty_file() { [ -e "$1" ] && [ ! -s "$1" ]; }
smoke_file_eq() { [ -f "$1" ] && [ "$(tr -d '[:space:]' < "$1")" = "$2" ]; }
smoke_no_glob() {
  # No path matches the glob $1 (passed as a literal pattern string).
  local matches
  matches=$(compgen -G "$1" 2>/dev/null) && [ -n "$matches" ] && return 1
  return 0
}
smoke_ip_not_loopback() {
  local file=$1 ip
  [ -f "$file" ] || return 1
  ip=$(grep -m1 '^CONFIG_CHECK_CONNECTION_IP=' "$file" | cut -d= -f2)
  case "$ip" in 127.*|"") return 1;; *) return 0;; esac
}
smoke_no_wifi() {
  # dietpi-wifi.txt absent, or every aWIFI_SSID[...] empty, in both copies.
  local fat=$1 root=$2 f
  for f in "$fat/dietpi-wifi.txt" "$root/boot/dietpi-wifi.txt"; do
    [ -f "$f" ] || continue
    if grep -Eq "^aWIFI_SSID\[[0-9]+\]='.+'" "$f"; then return 1; fi
  done
  return 0
}

case "$STAGE" in
  fetch) stage_fetch;;
  inject) stage_inject;;
  bake) stage_bake;;
  snapshot) stage_snapshot;;
  smoke) stage_smoke;;
  all)
    stage_fetch
    stage_inject
    stage_bake
    stage_snapshot
    stage_smoke
    ;;
esac
