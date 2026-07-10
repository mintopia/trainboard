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

stage_bake() { echo "not implemented" >&2; exit 3; }
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
