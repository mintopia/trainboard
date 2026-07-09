#!/bin/sh
# One-time migration of a pre-M5 device to the A/B slot layout
# (docs/superpowers/specs/2026-07-09-m5-self-update-design.md §3).
# Run as root ON the Pi, AFTER copying the launcher into place:
#   scp trainboard-launcher root@trainboard.local:/opt/trainboard/launcher
#   ssh root@trainboard.local 'sh -s' < deploy/migrate-to-slots.sh
set -eu

SLOTS=/opt/trainboard/slots
STATE_DIR=/var/lib/trainboard/updater
LAUNCHER=/opt/trainboard/launcher
OLD_BIN=/usr/local/bin/trainboard

[ -x "$LAUNCHER" ] || { echo "ERROR: launcher not installed at $LAUNCHER — scp it first"; exit 1; }

mkdir -p "$SLOTS/a" "$SLOTS/b" "$STATE_DIR"

if [ ! -x "$SLOTS/a/trainboard" ]; then
  [ -x "$OLD_BIN" ] || { echo "ERROR: no existing binary at $OLD_BIN to migrate"; exit 1; }
  cp "$OLD_BIN" "$SLOTS/a/trainboard"
  chmod 0755 "$SLOTS/a/trainboard"
  echo "migrated $OLD_BIN -> $SLOTS/a/trainboard"
fi

if [ ! -f "$STATE_DIR/state.json" ]; then
  # Seed state: slot a is active AND known-good. A "dev" version is fine —
  # a non-semver running version never blocks the first real update.
  VERSION=$("$SLOTS/a/trainboard" --version) || { echo "ERROR: $SLOTS/a/trainboard --version failed — binary may be broken or wrong-arch"; exit 1; }
  VERSION=$(printf '%s' "$VERSION" | awk '{print $2}')
  [ -n "$VERSION" ] || { echo "ERROR: could not parse a version from '$SLOTS/a/trainboard --version' output"; exit 1; }
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
  echo "seeded $STATE_DIR/state.json (version $VERSION)"
fi

cat <<'DONE'
Slot layout ready. Finish by installing the updated unit and restarting:
  scp deploy/trainboard.service root@trainboard.local:/etc/systemd/system/trainboard.service
  # IMPORTANT: if your current unit's ExecStart carries extra flags
  # (--manage-network), re-add them to the new ExecStart line first.
  ssh root@trainboard.local 'systemctl daemon-reload && systemctl restart trainboard'
Then delete the old binary once the board is confirmed up:
  ssh root@trainboard.local rm /usr/local/bin/trainboard
DONE
