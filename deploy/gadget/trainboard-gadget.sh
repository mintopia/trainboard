#!/bin/bash
# USB gadget ethernet (NCM + ECM) for the M4 OTG link (docs/superpowers/plans/
# 2026-07-08-m4-otg-mdns.md). Runs on the Pi Zero W 2's dwc2 UDC at boot via
# trainboard-gadget.service (Type=oneshot, RemainAfterExit=yes) — a laptop
# plugged into the OTG port sees a native NCM adapter (ECM as a legacy
# fallback for hosts without NCM support); never RNDIS (Windows-only compat
# baggage this project doesn't need).
#
#   trainboard-gadget.sh start   # idempotent: no-op if already bound to a UDC
#   trainboard-gadget.sh stop    # full teardown; safe to call on a partial
#                                 # or already-torn-down gadget
#
# Safety invariant: this runs unattended at every boot. A broken start must
# never hang the boot (no unbounded waits — everything here is a single
# sysfs/configfs write or a short command) and a failed start is reported
# loudly but must not wedge systemd (oneshot failure is fine; a hang is
# not). stop() must be safe to call from any partial state left by a
# failed start — every step is tolerant of "already done" / "never
# happened" except the final existence check, which is not.
set -euo pipefail

GADGET_DIR=/sys/kernel/config/usb_gadget/g1
UDC_DIR=/sys/class/udc
SERIAL_FILE=/proc/device-tree/serial-number

log() { echo "[$(date '+%H:%M:%S')] $*" >&2; }

usage() {
  echo "Usage: $0 {start|stop}" >&2
}

# device_serial_string — human-readable serial for the gadget's USB string
# descriptor. Falls back to a fixed placeholder when not running on a Pi
# (no device-tree serial-number node) so the gadget still enumerates with a
# stable, if generic, identity.
device_serial_string() {
  local serial=""
  if [[ -r "$SERIAL_FILE" ]]; then
    serial=$(tr -d '\0' <"$SERIAL_FILE")
  fi
  [[ -n "$serial" ]] || serial="trainboard"
  echo "$serial"
}

# mac_tail — last 6 hex characters of the device-tree serial, used to build
# both functions' locally-administered MAC pairs. Must always yield exactly
# 6 hex characters: a missing serial file, an unreadable one, or a serial
# that (unexpectedly) isn't hex all fall back to a fixed tail rather than
# feeding non-hex characters into a MAC address.
mac_tail() {
  local tail_chars=""
  if [[ -r "$SERIAL_FILE" ]]; then
    tail_chars=$(tr -d '\0' <"$SERIAL_FILE" | tail -c 6)
  fi
  if [[ ! "$tail_chars" =~ ^[0-9A-Fa-f]{6}$ ]]; then
    tail_chars="000000"
  fi
  echo "${tail_chars,,}"
}

# find_udc — first (only, on a Pi Zero W 2) entry under /sys/class/udc.
# Empty output means dwc2 never registered a UDC — almost always the dwc2
# overlay is missing from config.txt; caller turns that into a loud, fatal
# error rather than silently limping on with no working gadget.
find_udc() {
  shopt -s nullglob
  local udcs=("$UDC_DIR"/*)
  shopt -u nullglob
  ((${#udcs[@]} > 0)) || return 0
  basename "${udcs[0]}"
}

start() {
  # Command substitution trims the trailing newline the UDC attribute may
  # read back with, so this is robust to whichever way "unbound" happens to
  # be spelled on read (empty string vs. a lone "\n") rather than relying
  # on a raw file-size check.
  if [[ -d "$GADGET_DIR" ]]; then
    local bound_udc
    bound_udc=$(cat "$GADGET_DIR/UDC" 2>/dev/null || true)
    if [[ -n "$bound_udc" ]]; then
      log "gadget already built and bound to UDC $bound_udc; nothing to do"
      return 0
    fi
  fi

  log "loading libcomposite"
  modprobe libcomposite

  local serial dev_mac host_mac
  serial=$(device_serial_string)
  local tail
  tail=$(mac_tail)
  dev_mac="02:42:${tail:0:2}:${tail:2:2}:${tail:4:2}:01"
  host_mac="02:42:${tail:0:2}:${tail:2:2}:${tail:4:2}:02"

  log "building gadget descriptor under $GADGET_DIR (serial=$serial)"
  mkdir -p "$GADGET_DIR"
  echo 0x1d6b >"$GADGET_DIR/idVendor"
  echo 0x0104 >"$GADGET_DIR/idProduct"

  mkdir -p "$GADGET_DIR/strings/0x409"
  echo "$serial" >"$GADGET_DIR/strings/0x409/serialnumber"
  echo "Trainboard" >"$GADGET_DIR/strings/0x409/product"
  echo "mintopia" >"$GADGET_DIR/strings/0x409/manufacturer"

  # ncm.usb0 created before ecm.usb1: u_ether hands out netdev names
  # (usb0, usb1, ...) in function-creation order, and usb0 is the interface
  # this script (and dnsmasq-usb0.conf) configures — so NCM must claim it.
  log "creating ncm.usb0 function (dev=$dev_mac host=$host_mac)"
  mkdir -p "$GADGET_DIR/functions/ncm.usb0"
  echo "$dev_mac" >"$GADGET_DIR/functions/ncm.usb0/dev_addr"
  echo "$host_mac" >"$GADGET_DIR/functions/ncm.usb0/host_addr"

  log "creating ecm.usb1 function (legacy fallback, same MACs)"
  mkdir -p "$GADGET_DIR/functions/ecm.usb1"
  echo "$dev_mac" >"$GADGET_DIR/functions/ecm.usb1/dev_addr"
  echo "$host_mac" >"$GADGET_DIR/functions/ecm.usb1/host_addr"

  # No os_desc: that's RNDIS-era Windows compat (extended compat ID so
  # Windows auto-installs the RNDIS driver). NCM enumerates natively on
  # every host we care about, so it's deliberately left unconfigured.
  log "wiring configs (c.1=NCM, c.2=ECM)"
  mkdir -p "$GADGET_DIR/configs/c.1/strings/0x409"
  echo "NCM" >"$GADGET_DIR/configs/c.1/strings/0x409/configuration"
  ln -sf "$GADGET_DIR/functions/ncm.usb0" "$GADGET_DIR/configs/c.1/ncm.usb0"

  mkdir -p "$GADGET_DIR/configs/c.2/strings/0x409"
  echo "ECM" >"$GADGET_DIR/configs/c.2/strings/0x409/configuration"
  ln -sf "$GADGET_DIR/functions/ecm.usb1" "$GADGET_DIR/configs/c.2/ecm.usb1"

  local udc
  udc=$(find_udc)
  if [[ -z "$udc" ]]; then
    log "FATAL: no UDC found under $UDC_DIR — the dwc2 overlay is probably" \
      "missing from config.txt; see docs/deploy.md §9"
    exit 1
  fi

  log "binding to UDC $udc"
  echo "$udc" >"$GADGET_DIR/UDC"

  log "bringing up usb0 at 10.55.0.1/29"
  ip addr replace 10.55.0.1/29 dev usb0
  ip link set usb0 up

  log "gadget started (UDC=$udc)"
}

stop() {
  log "bringing usb0 down (tolerated if already down or never existed)"
  ip link set usb0 down 2>/dev/null || true

  if [[ -d "$GADGET_DIR" ]]; then
    log "unbinding UDC"
    echo "" >"$GADGET_DIR/UDC" 2>/dev/null || true

    log "unlinking functions from configs"
    rm -f "$GADGET_DIR/configs/c.1/ncm.usb0" 2>/dev/null || true
    rm -f "$GADGET_DIR/configs/c.2/ecm.usb1" 2>/dev/null || true

    log "removing config strings"
    rmdir "$GADGET_DIR/configs/c.1/strings/0x409" 2>/dev/null || true
    rmdir "$GADGET_DIR/configs/c.2/strings/0x409" 2>/dev/null || true

    log "removing configs"
    rmdir "$GADGET_DIR/configs/c.1" 2>/dev/null || true
    rmdir "$GADGET_DIR/configs/c.2" 2>/dev/null || true

    log "removing functions"
    rmdir "$GADGET_DIR/functions/ncm.usb0" 2>/dev/null || true
    rmdir "$GADGET_DIR/functions/ecm.usb1" 2>/dev/null || true

    log "removing gadget strings"
    rmdir "$GADGET_DIR/strings/0x409" 2>/dev/null || true

    log "removing gadget dir"
    rmdir "$GADGET_DIR" 2>/dev/null || true
  fi

  # Not tolerated: if g1 is still here, teardown genuinely failed (something
  # still has a handle on one of its children) and that must surface loudly
  # rather than report a clean stop that didn't happen.
  if [[ -d "$GADGET_DIR" ]]; then
    log "FATAL: teardown incomplete, $GADGET_DIR still present"
    exit 1
  fi

  log "gadget stopped"
}

case "${1:-}" in
  start) start ;;
  stop) stop ;;
  *)
    usage
    exit 1
    ;;
esac
