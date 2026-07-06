#!/bin/bash
# Flash DietPi to an SD card and preconfigure it for headless trainboard use:
# WiFi + SSH + SPI enabled, hostname "trainboard". macOS only. Run with sudo.
#
#   sudo ./deploy/flash-sd.sh <image.img.xz> <diskN>
#   e.g. sudo ./deploy/flash-sd.sh DietPi_RPi234-ARMv8-Bookworm.img.xz disk4
#
# DESTROYS ALL DATA on the target disk.
set -euo pipefail

IMG="${1:?usage: flash-sd.sh <image.img.xz> <diskN>}"
DISK="${2:?usage: flash-sd.sh <image.img.xz> <diskN>}"

[[ "$(uname)" == "Darwin" ]] || { echo "macOS only" >&2; exit 1; }
[[ -f "$IMG" ]] || { echo "image not found: $IMG" >&2; exit 1; }
[[ "$DISK" =~ ^disk[0-9]+$ ]] || { echo "disk must look like disk4, got: $DISK" >&2; exit 1; }
[[ $EUID -eq 0 ]] || { echo "run with sudo" >&2; exit 1; }

diskutil info "/dev/$DISK" | grep -q "Removable Media:.*Removable" \
  || { echo "/dev/$DISK is not removable media — refusing" >&2; exit 1; }

echo "== Target:"
diskutil list "/dev/$DISK"
printf "Type the disk id (%s) to confirm ERASE: " "$DISK"
read -r CONFIRM
[[ "$CONFIRM" == "$DISK" ]] || { echo "aborted" >&2; exit 1; }

printf "WiFi SSID: "
read -r WIFI_SSID
printf "WiFi password (hidden): "
read -rs WIFI_PSK
echo

echo "== Flashing (a few minutes)…"
diskutil unmountDisk force "/dev/$DISK"
xz -dc "$IMG" | dd of="/dev/r$DISK" bs=4m
sync

echo "== Mounting boot partition…"
diskutil mountDisk "/dev/$DISK" >/dev/null
sleep 2
BOOT=""
for v in /Volumes/*; do
  [[ -f "$v/dietpi.txt" ]] && BOOT="$v" && break
done
[[ -n "$BOOT" ]] || { echo "boot partition with dietpi.txt not found" >&2; exit 1; }
echo "   boot at: $BOOT"

echo "== Preconfiguring dietpi.txt (headless, WiFi, hostname trainboard)…"
sed -i '' \
  -e 's/^AUTO_SETUP_ACCEPTED=.*/AUTO_SETUP_ACCEPTED=1/' \
  -e 's/^AUTO_SETUP_NET_WIFI_ENABLED=.*/AUTO_SETUP_NET_WIFI_ENABLED=1/' \
  -e 's/^AUTO_SETUP_NET_WIFI_COUNTRY_CODE=.*/AUTO_SETUP_NET_WIFI_COUNTRY_CODE=GB/' \
  -e 's/^AUTO_SETUP_NET_HOSTNAME=.*/AUTO_SETUP_NET_HOSTNAME=trainboard/' \
  -e 's/^AUTO_SETUP_HEADLESS=.*/AUTO_SETUP_HEADLESS=1/' \
  "$BOOT/dietpi.txt"

echo "== Writing WiFi credentials…"
python3 - "$BOOT/dietpi-wifi.txt" "$WIFI_SSID" "$WIFI_PSK" <<'PY'
import re, sys
path, ssid, psk = sys.argv[1:4]
s = open(path).read()
s = re.sub(r"aWIFI_SSID\[0\]='[^']*'", f"aWIFI_SSID[0]='{ssid}'", s)
s = re.sub(r"aWIFI_KEY\[0\]='[^']*'", f"aWIFI_KEY[0]='{psk}'", s)
open(path, "w").write(s)
PY

echo "== Enabling SPI for the SSD1322…"
grep -q '^dtparam=spi=on' "$BOOT/config.txt" || echo 'dtparam=spi=on' >> "$BOOT/config.txt"

echo "== Ejecting…"
sync
diskutil eject "/dev/$DISK"

cat <<EOF

Done. Next:
  1. Insert the card in the Pi Zero 2 W and power it. First boot runs
     DietPi's automated setup (several minutes; it reboots itself).
  2. Find it:  ping trainboard.local   (or check your router)
  3. SSH:      ssh root@trainboard.local   (password: dietpi — change it!)
EOF
