#!/bin/bash
# Bench evaluation of the mode2 wpa_supplicant driver (internal/net/driver_mode2.go)
# ahead of the M3b ifupdown migration (docs/deploy.md §8) and the ADR 0003
# addendum (issues #6/#7/#13). Runs ON the Pi as root during an attended
# bench session — Jess is the rescue plan, but the real safety net is the
# dead-man switch armed in step 2 below: if this script (or the operator's
# SSH session) dies mid-experiment with wlan0 in a half-mutated state, a
# systemd timer restores the pre-bench network config and reboots on its
# own, headless, no SD card reader required.
#
#   sudo ./deploy/bench/eval-mode2.sh --minutes 20 \
#       --ap-pass 'bench-hotspot-pw' --sta-ssid 'HomeWifi' --sta-psk 'homepw'
#
# Safety invariant: nothing that touches wlan0 (writing the wpa conf,
# starting wpa_supplicant, select_network, dnsmasq) may run until the
# dead-man timer is armed AND verified scheduled (step 2). If verification
# fails, the script aborts before any mutation.
set -euo pipefail

# ---------------------------------------------------------------------------
# Config / flags
# ---------------------------------------------------------------------------

MINUTES=20
IFACE=wlan0
COUNTRY=GB
AP_SSID="Trainboard-BENCH"
AP_PASS=""
AP_ADDR="192.168.4.1/24"
STA_SSID=""
STA_PSK=""

# Paths mirrored from production so the bench evaluates what production
# actually writes (internal/net/driver_mode2.go's wpaConfPath, and
# internal/net/dnsmasq.go's Start()).
WPA_CONF=/run/trainboard-wpa.conf
DNSMASQ_CONF=/run/trainboard-dnsmasq.conf
DNSMASQ_PID=/run/trainboard-dnsmasq.pid

# Poll cadence matches pollAttempts/pollInterval in internal/net/driver.go
# (10 attempts, 500ms apart == 5s) so timings are comparable to production.
POLL_ATTEMPTS=10
POLL_INTERVAL=0.5

usage() {
  cat <<EOF
Usage: sudo $0 [options]

  --minutes N       Dead-man budget in minutes (default: $MINUTES)
  --iface IFACE     Wireless interface (default: $IFACE)
  --country CC      Regulatory domain, 2-letter (default: $COUNTRY)
  --ap-pass PASS    AP (Trainboard-BENCH) password; prompted if omitted
  --sta-ssid SSID   Real, reachable SSID for the STA-side experiments;
                    prompted if omitted
  --sta-psk PSK     PSK for --sta-ssid; prompted (hidden) if omitted
  -h, --help        This help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --minutes) MINUTES="$2"; shift 2 ;;
    --iface) IFACE="$2"; shift 2 ;;
    --country) COUNTRY="$2"; shift 2 ;;
    --ap-pass) AP_PASS="$2"; shift 2 ;;
    --sta-ssid) STA_SSID="$2"; shift 2 ;;
    --sta-psk) STA_PSK="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 1 ;;
  esac
done

[[ "$MINUTES" =~ ^[0-9]+$ ]] || { echo "FATAL: --minutes must be a positive integer" >&2; exit 1; }
[[ $EUID -eq 0 ]] || { echo "FATAL: run as root (this drives wlan0 directly)" >&2; exit 1; }

if [[ -z "$AP_PASS" ]]; then
  printf "AP password for %s (hidden): " "$AP_SSID"
  read -rs AP_PASS
  echo
fi
if [[ -z "$STA_SSID" ]]; then
  printf "Real STA SSID (must actually be reachable from the bench): "
  read -r STA_SSID
fi
if [[ -z "$STA_PSK" ]]; then
  printf "STA password for %s (hidden): " "$STA_SSID"
  read -rs STA_PSK
  echo
fi
[[ ${#AP_PASS} -ge 8 ]] || { echo "FATAL: AP password must be at least 8 chars (WPA2-PSK)" >&2; exit 1; }

BACKUP_DIR="/root/bench-backup-$(date +%s)"
LOG="$BACKUP_DIR/eval-mode2.log"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG" >&2; }

# ---------------------------------------------------------------------------
# Small helpers shared by all three experiments
# ---------------------------------------------------------------------------

# wpa_field NAME — reads one KEY=VALUE field from `wpa_cli status`, empty
# string (not an error) if wpa_supplicant isn't up or the field is absent.
wpa_field() {
  wpa_cli -i "$IFACE" status 2>/dev/null | awk -F= -v k="$1" '$1==k{print $2; found=1} END{if(!found) print ""}'
}

# write_conf STA_SSID STA_PSK — renders both network blocks exactly like
# internal/net/driver_mode2.go's renderConf (id_str sta/ap, disabled=1 on
# both; select_network at runtime is what actually activates one). Args let
# Experiment 3 pass a deliberately wrong PSK without touching the AP block.
write_conf() {
  local sta_ssid="$1" sta_psk="$2"
  cat > "$WPA_CONF" <<EOF
ctrl_interface=/run/wpa_supplicant
country=$COUNTRY
network={
    id_str="sta"
    ssid="$sta_ssid"
    psk="$sta_psk"
    disabled=1
}
network={
    id_str="ap"
    ssid="$AP_SSID"
    mode=2
    frequency=2437
    key_mgmt=WPA-PSK
    psk="$AP_PASS"
    disabled=1
}
EOF
  chmod 600 "$WPA_CONF"
}

# ensure_daemon — mirrors mode2Driver.ensureDaemon: start wpa_supplicant if
# it isn't answering, else tell the running one to reload the conf we just
# wrote.
ensure_daemon() {
  if ! wpa_cli -i "$IFACE" status >/dev/null 2>&1; then
    wpa_supplicant -B -i "$IFACE" -c "$WPA_CONF"
  else
    wpa_cli -i "$IFACE" reconfigure >/dev/null
  fi
}

# wait_state WANT_STATE WANT_MODE_OR_EMPTY — polls POLL_ATTEMPTS times,
# POLL_INTERVAL apart. Echoes elapsed whole seconds and returns 0 on match,
# 1 after exhausting attempts (mirrors driver.go's pollStatus).
wait_state() {
  local want_state="$1" want_mode="$2"
  local start elapsed i state mode
  start=$(date +%s)
  for ((i = 0; i < POLL_ATTEMPTS; i++)); do
    state=$(wpa_field wpa_state)
    mode=$(wpa_field mode)
    if [[ "$state" == "$want_state" ]] && { [[ -z "$want_mode" ]] || [[ "$mode" == "$want_mode" ]]; }; then
      elapsed=$(( $(date +%s) - start ))
      echo "$elapsed"
      return 0
    fi
    (( i < POLL_ATTEMPTS - 1 )) && sleep "$POLL_INTERVAL"
  done
  elapsed=$(( $(date +%s) - start ))
  echo "$elapsed"
  return 1
}

dnsmasq_alive() {
  pkill -0 -F "$DNSMASQ_PID" 2>/dev/null
}

start_dnsmasq() {
  # internal/net/dnsmasq.go's Start() hardcodes "interface=wlan0" regardless
  # of the driver's configured iface, so this literal mirrors it exactly
  # rather than substituting $IFACE.
  cat > "$DNSMASQ_CONF" <<'EOF'
interface=wlan0
bind-interfaces
dhcp-range=192.168.4.10,192.168.4.100,10m
dhcp-option=option:router,192.168.4.1
address=/#/192.168.4.1
no-resolv
EOF
  dnsmasq --conf-file="$DNSMASQ_CONF" --pid-file="$DNSMASQ_PID"
}

stop_dnsmasq() {
  pkill -F "$DNSMASQ_PID" 2>/dev/null || true
}

# ---------------------------------------------------------------------------
# 1. Preflight
# ---------------------------------------------------------------------------

mkdir -p "$BACKUP_DIR"
log "== Preflight =="
log "backup dir: $BACKUP_DIR"

for bin in wpa_supplicant wpa_cli ip dhclient iw systemd-run systemctl pkill; do
  command -v "$bin" >/dev/null 2>&1 || { echo "FATAL: required binary missing: $bin" >&2; exit 1; }
done

if ! command -v dnsmasq >/dev/null 2>&1; then
  printf "dnsmasq not found. Install via apt now? [y/N] "
  read -r ans
  [[ "$ans" =~ ^[Yy] ]] || { echo "FATAL: dnsmasq required, aborting" >&2; exit 1; }
  apt-get update && apt-get install -y dnsmasq
fi

# Radio prerequisites (mirrors internal/net/prereq.go's CheckPrereqs so a
# soft-blocked radio or unset regulatory domain isn't mistaken for a mode2
# wedge later): unblock any soft-blocked wlan rfkill device, then set the
# regulatory country if it reads as unset (00).
for soft in /sys/class/rfkill/rfkill*/soft; do
  [[ -e "$soft" ]] || continue
  dir=$(dirname "$soft")
  [[ "$(cat "$dir/type" 2>/dev/null)" == "wlan" ]] || continue
  if [[ "$(cat "$soft")" == "1" ]]; then
    log "rfkill soft-blocked on $dir, unblocking"
    echo 0 > "$soft"
  fi
done
if iw reg get 2>/dev/null | grep -q "country 00"; then
  log "regulatory domain unset, setting $COUNTRY"
  iw reg set "$COUNTRY"
fi

log "snapshotting current network state"
ip addr show dev "$IFACE" > "$BACKUP_DIR/ip-addr-before.txt" 2>&1 || true
wpa_cli -i "$IFACE" status > "$BACKUP_DIR/wpa-status-before.txt" 2>&1 || echo "(wpa_supplicant not running)" > "$BACKUP_DIR/wpa-status-before.txt"

# BACKUP_FILES lists every original path we've mirrored under
# $BACKUP_DIR/<original path>; both the in-process trap-based restore
# (restore_files, below) and the dead-man's standalone restore script read
# from this same layout.
BACKUP_FILES=()
backup_file() {
  local orig="$1"
  if [[ -e "$orig" ]]; then
    local dest="$BACKUP_DIR$orig"
    mkdir -p "$(dirname "$dest")"
    cp -a "$orig" "$dest"
    BACKUP_FILES+=("$orig")
    log "  backed up: $orig"
  fi
}
backup_file /etc/network/interfaces
backup_file /etc/wpa_supplicant/wpa_supplicant.conf
if compgen -G "/etc/wpa_supplicant/wpa_supplicant-*.conf" > /dev/null; then
  for f in /etc/wpa_supplicant/wpa_supplicant-*.conf; do
    backup_file "$f"
  done
fi
printf '%s\n' "${BACKUP_FILES[@]}" > "$BACKUP_DIR/manifest.txt"

# ---------------------------------------------------------------------------
# 2. Arm the dead-man switch — BEFORE any wlan0 mutation
# ---------------------------------------------------------------------------

log "== Arming dead-man switch (${MINUTES}m) =="

DEADMAN_RESTORE="$BACKUP_DIR/deadman-restore.sh"
cat > "$DEADMAN_RESTORE" <<EOF
#!/bin/bash
# Generated by eval-mode2.sh. Runs standalone (not depending on the bench
# script's process still being alive) if the \${MINUTES}m budget expires:
# restore whatever original files were backed up, kill anything the bench
# started, and reboot so the box comes back on its normal ifupdown-managed
# wlan0 without anyone at the bench.
set -uo pipefail
while IFS= read -r orig; do
  [[ -n "\$orig" ]] || continue
  dest="$BACKUP_DIR\$orig"
  if [[ -e "\$dest" ]]; then
    mkdir -p "\$(dirname "\$orig")"
    cp -a "\$dest" "\$orig"
  fi
done < "$BACKUP_DIR/manifest.txt"
pkill -9 -f "wpa_supplicant.*$WPA_CONF" 2>/dev/null || true
pkill -9 -F "$DNSMASQ_PID" 2>/dev/null || true
ip addr flush dev "$IFACE" 2>/dev/null || true
systemctl restart networking 2>/dev/null || ifup "$IFACE" 2>/dev/null || true
reboot
EOF
chmod +x "$DEADMAN_RESTORE"

systemd-run --on-active="${MINUTES}m" --unit=bench-deadman.service /bin/bash "$DEADMAN_RESTORE" >/dev/null

if ! systemctl is-active --quiet bench-deadman.timer; then
  echo "FATAL: dead-man timer did not arm (bench-deadman.timer not active) — aborting before any wlan0 mutation" >&2
  exit 1
fi
log "dead-man armed and verified scheduled: bench-deadman.timer active, fires in ${MINUTES}m"

# ---------------------------------------------------------------------------
# Teardown — the single path everything funnels through on exit, success or
# failure alike, via the trap below. Order matters: stop what we started,
# restore original files, THEN disarm the dead-man (last, so a failure
# earlier in teardown leaves the dead-man armed as the final backstop rather
# than disarming and hoping manual cleanup is complete).
# ---------------------------------------------------------------------------

restore_files() {
  for orig in "${BACKUP_FILES[@]}"; do
    local dest="$BACKUP_DIR$orig"
    if [[ -e "$dest" ]]; then
      cp -a "$dest" "$orig"
      log "  restored: $orig"
    fi
  done
}

print_verdict() {
  log "== Verdict (paste into issue #7 / ADR 0003 addendum) =="
  {
    echo ""
    echo "| Experiment | Result | Detail |"
    echo "|---|---|---|"
    echo "| 1. AP bring-up | ${EXP1_RESULT:-NOT RUN} | ${EXP1_DETAIL:-} |"
    echo "| 2. AP<->STA toggle x10 | ${EXP2_RESULT:-NOT RUN} | ${EXP2_DETAIL:-} |"
    echo "| 3. Bad-PSK AP-restore | ${EXP3_RESULT:-NOT RUN} | ${EXP3_DETAIL:-} |"
    echo ""
  } | tee -a "$LOG"
}

teardown() {
  local rc=$?
  log "== Teardown (exit code $rc) =="
  stop_dnsmasq
  pkill -f "wpa_supplicant.*$WPA_CONF" 2>/dev/null || true
  ip addr flush dev "$IFACE" 2>/dev/null || true
  restore_files
  systemctl restart networking 2>/dev/null || ifup "$IFACE" 2>/dev/null || true
  print_verdict
  systemctl stop bench-deadman.timer 2>/dev/null || true
  systemctl reset-failed 2>/dev/null || true
  log "dead-man disarmed. Backup + log kept at $BACKUP_DIR for the record."
  exit "$rc"
}
trap teardown EXIT

# ---------------------------------------------------------------------------
# 3. Experiment 1 — AP bring-up
# ---------------------------------------------------------------------------

log "== Experiment 1: AP bring-up =="
EXP1_RESULT=FAIL
EXP1_DETAIL=""

write_conf "$STA_SSID" "$STA_PSK"
ensure_daemon
wpa_cli -i "$IFACE" select_network 1 >/dev/null

if elapsed=$(wait_state COMPLETED AP); then
  ip addr flush dev "$IFACE"
  ip addr add "$AP_ADDR" dev "$IFACE"
  start_dnsmasq
  sleep 1
  if dnsmasq_alive; then
    log "AP up (${elapsed}s), dnsmasq alive. Associate a phone to '$AP_SSID' now."
    printf "Confirm: DHCP lease obtained AND http://192.168.4.1 loads [y/N]: "
    read -r ans
    if [[ "$ans" =~ ^[Yy] ]]; then
      EXP1_RESULT=PASS
      EXP1_DETAIL="AP COMPLETED in ${elapsed}s; operator confirmed lease+portal"
    else
      EXP1_DETAIL="AP COMPLETED in ${elapsed}s; operator reported lease/portal FAILED"
    fi
  else
    EXP1_DETAIL="AP COMPLETED in ${elapsed}s but dnsmasq did not stay up"
  fi
else
  EXP1_DETAIL="AP never reached COMPLETED/mode=AP within ${elapsed}s"
fi
log "Experiment 1: $EXP1_RESULT — $EXP1_DETAIL"

# ---------------------------------------------------------------------------
# 4. Experiment 2 — 10x AP<->STA toggle
# ---------------------------------------------------------------------------

log "== Experiment 2: AP<->STA toggle x10 =="
EXP2_RESULT=PASS
consecutive_wedges=0
toggle_log=()

for ((n = 1; n <= 10; n++)); do
  # AP -> STA
  write_conf "$STA_SSID" "$STA_PSK"
  wpa_cli -i "$IFACE" reconfigure >/dev/null
  wpa_cli -i "$IFACE" select_network 0 >/dev/null
  if sta_elapsed=$(wait_state COMPLETED ""); then
    dhclient -1 "$IFACE" >/dev/null 2>&1 || true
    consecutive_wedges=0
    toggle_log+=("toggle $n: AP->STA COMPLETED in ${sta_elapsed}s")
  else
    consecutive_wedges=$((consecutive_wedges + 1))
    toggle_log+=("toggle $n: AP->STA WEDGED (${sta_elapsed}s, no COMPLETED)")
    log "  toggle $n: STA side wedged (${consecutive_wedges} consecutive)"
  fi

  # STA -> AP
  wpa_cli -i "$IFACE" select_network 1 >/dev/null
  if ap_elapsed=$(wait_state COMPLETED AP); then
    ip addr flush dev "$IFACE"
    ip addr add "$AP_ADDR" dev "$IFACE"
    consecutive_wedges=0
    toggle_log+=("toggle $n: STA->AP COMPLETED in ${ap_elapsed}s")
  else
    consecutive_wedges=$((consecutive_wedges + 1))
    toggle_log+=("toggle $n: STA->AP WEDGED (${ap_elapsed}s, no COMPLETED)")
    log "  toggle $n: AP side wedged (${consecutive_wedges} consecutive)"
  fi

  if (( consecutive_wedges >= 2 )); then
    EXP2_RESULT=FAIL
    log "  2 consecutive wedges — aborting the toggle loop (not the script) at toggle $n"
    break
  fi
done
EXP2_DETAIL=$(printf '%s; ' "${toggle_log[@]}")
log "Experiment 2: $EXP2_RESULT — $EXP2_DETAIL"

# ---------------------------------------------------------------------------
# 5. Experiment 3 — bad-PSK STA attempt, assert AP-restore invariant
# ---------------------------------------------------------------------------

log "== Experiment 3: bad-PSK STA attempt + AP-restore assertion =="
EXP3_RESULT=FAIL
EXP3_DETAIL=""

write_conf "$STA_SSID" "deliberately-wrong-psk-00000"
wpa_cli -i "$IFACE" reconfigure >/dev/null
wpa_cli -i "$IFACE" select_network 0 >/dev/null
if bad_elapsed=$(wait_state COMPLETED ""); then
  EXP3_DETAIL="ALARM: STA associated with a WRONG PSK in ${bad_elapsed}s (should have failed)"
else
  EXP3_DETAIL="bad PSK correctly failed to associate (${bad_elapsed}s)"
fi
log "  $EXP3_DETAIL"

log "  asserting AP-restore invariant (ADR 0003)"
write_conf "$STA_SSID" "$STA_PSK"
wpa_cli -i "$IFACE" reconfigure >/dev/null
wpa_cli -i "$IFACE" select_network 1 >/dev/null
if restore_elapsed=$(wait_state COMPLETED AP); then
  ip addr flush dev "$IFACE"
  ip addr add "$AP_ADDR" dev "$IFACE"
  stop_dnsmasq
  start_dnsmasq
  sleep 1
  if dnsmasq_alive; then
    EXP3_RESULT=PASS
    EXP3_DETAIL="$EXP3_DETAIL; AP restored in ${restore_elapsed}s, dnsmasq alive"
  else
    EXP3_DETAIL="$EXP3_DETAIL; AP restored in ${restore_elapsed}s but dnsmasq NOT alive"
  fi
else
  EXP3_DETAIL="$EXP3_DETAIL; AP FAILED to restore within ${restore_elapsed}s — invariant broken"
fi
log "Experiment 3: $EXP3_RESULT — $EXP3_DETAIL"

log "== Experiments complete =="
# Falling off the end here runs the EXIT trap (teardown): stop/restore/
# disarm/print-verdict happen exactly once, on this path and on any error
# path alike.
