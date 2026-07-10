#!/usr/bin/env bash
# Runs INSIDE the image during the CI pre-bake, as DietPi's
# AUTO_SETUP_CUSTOM_SCRIPT_EXEC hook (staged at the fixed filename
# /boot/Automation_Custom_Script.sh by build-image.sh's inject stage),
# after DietPi first-run completes. Lays down the docs/deploy.md install:
# A/B slot layout + launcher + service (--production --manage-network) +
# M4 gadget lifeline (deploy.md §10), then marks completion for the bake
# supervisor. DESTDIR/SRCDIR/SKIP_SYSTEMCTL exist so the layout logic is
# testable off-device (SRCDIR=/boot, DESTDIR="" in production).
#
# The dwc2 overlay (config.txt/cmdline.txt) is already baked into the boot
# partition by build-image.sh's inject stage — this script only does the
# rootfs side of §10.
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
# known-good=a, at the shipped release's version. Read from the staged
# trainboard-version marker (written by build-image.sh from --tag) rather
# than executing the slot-a binary, which migrate-to-slots.sh does on a
# live device — here the binary may not match the host's own arch (see
# the test's fake trainboard.bin), and the version is already known
# statically at image-build time.
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

# M4 USB gadget lifeline (deploy.md §10 "Install"): script + conf under
# /usr/local/lib/trainboard/, both units under /etc/systemd/system/.
install -m 0755 "$SRCDIR/trainboard-gadget/trainboard-gadget.sh" "$DESTDIR/usr/local/lib/trainboard/trainboard-gadget.sh"
install -m 0644 "$SRCDIR/trainboard-gadget/dnsmasq-usb0.conf" "$DESTDIR/usr/local/lib/trainboard/dnsmasq-usb0.conf"
install -m 0644 "$SRCDIR/trainboard-gadget/trainboard-gadget.service" "$DESTDIR/etc/systemd/system/trainboard-gadget.service"
install -m 0644 "$SRCDIR/trainboard-gadget/trainboard-dnsmasq-usb0.service" "$DESTDIR/etc/systemd/system/trainboard-dnsmasq-usb0.service"

if [ -z "${SKIP_SYSTEMCTL:-}" ]; then
  # dnsmasq package is required by the usb0 lifeline (deploy.md §10 install
  # step, and the same package deploy.md §9 requires for AP-mode fallback —
  # a single apt-get here covers both).
  apt-get install -y -qq dnsmasq >/dev/null || echo "WARN: dnsmasq install failed; usb0 lifeline degraded"
  systemctl daemon-reload
  systemctl enable trainboard.service trainboard-gadget.service trainboard-dnsmasq-usb0.service
fi

touch "$SRCDIR/trainboard-baked"
