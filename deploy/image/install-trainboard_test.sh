#!/usr/bin/env bash
# DESTDIR-based test for install-trainboard.sh: exercises the layout logic
# rootless, on any host arch, without touching a real Pi (deploy.md §10).
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
grep -q '^ExecStart=/opt/trainboard/launcher --production --manage-network$' "$T/dst/etc/systemd/system/trainboard.service" || fail "ExecStart rewrite failed or malformed"
[ -x "$T/dst/usr/local/lib/trainboard/trainboard-gadget.sh" ] || fail "gadget script missing"
[ -f "$T/dst/etc/systemd/system/trainboard-gadget.service" ] || fail "gadget unit missing"
[ -f "$T/dst/etc/systemd/system/trainboard-dnsmasq-usb0.service" ] || fail "dnsmasq unit missing"
[ -f "$T/dst/usr/local/lib/trainboard/dnsmasq-usb0.conf" ] || fail "dnsmasq config missing"
[ -f "$T/src/trainboard-baked" ] || fail "completion marker missing"

# Negative test: guard must trip if ExecStart line is corrupted.
echo "=== Testing guard trips on corrupted ExecStart ==="
T2=$(mktemp -d); trap 'rm -rf "$T" "$T2"' EXIT
mkdir -p "$T2/src" "$T2/dst"
cp "$HERE/../trainboard.service" "$T2/src/trainboard.service"
# Corrupt the service: remove the newline structure (space after =).
sed -i.bak 's/^ExecStart=/ExecStart= /' "$T2/src/trainboard.service"
cp -r "$HERE/../gadget" "$T2/src/trainboard-gadget"
printf '#!/bin/sh\necho "trainboard v9.9.9 (test)"\n' > "$T2/src/trainboard.bin"
printf '#!/bin/sh\n' > "$T2/src/trainboard-launcher.bin"
chmod +x "$T2/src/trainboard.bin" "$T2/src/trainboard-launcher.bin"
echo v9.9.9 > "$T2/src/trainboard-version"
if DESTDIR="$T2/dst" SRCDIR="$T2/src" SKIP_SYSTEMCTL=1 "$HERE/install-trainboard.sh"; then
  fail "guard did not trip on corrupted ExecStart"
fi
echo "Guard correctly rejected corrupted ExecStart"

echo OK
