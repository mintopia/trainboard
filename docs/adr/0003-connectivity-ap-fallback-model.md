# 3. Connectivity model: hostapd/wpa_supplicant with AP fallback

Date: 2026-07-02
Status: Accepted

## Context

The device must self-provision (first run) and stay reachable when its configured wifi
fails, on a Pi Zero 2 W running minimal DietPi where boot speed is the top priority. The
onboard wifi chip (CYW43436) cannot reliably run AP and station (STA) modes at the same
time. DietPi does not ship NetworkManager.

## Decision

**Mechanism:** A Connectivity Manager orchestrates `wpa_supplicant` (STA: scan /
connect / status via `wpa_cli`), `hostapd` (AP), and `dnsmasq` (DHCP + captive-portal
DNS on the AP interface). NetworkManager is deliberately not used — keeping DietPi lean
protects boot time.

**Layered connectivity check:** connectivity is evaluated as distinct states —
association → DHCP → DNS → captive-portal → Darwin reachability — each with backoff,
rather than a single blunt timeout (avoids false failures on weak wifi / slow DHCP).

**AP as fallback, not just first-run:** The device enters AP Mode when no wifi is
configured (first run) or after a layered connectivity failure. Because AP+STA can't run
concurrently, the retry is a **tear-down-and-retry loop**: while in fallback AP mode the
device, **every 5 minutes**, tears down the AP, attempts the configured wifi, and on
success resumes normal operation (AP gone) or on failure restores the AP. The hotspot
drops for ~10-20s per attempt; the Hotspot Info Scene signals the retry. The 5-minute
interval matches the data-staleness grace period (keep last board 5 min, then Error).

**AP-restore is a hard invariant.** On STA failure the manager verifies the AP SSID is
beaconing and the DHCP lease service is up **before** declaring fallback restored, and a
systemd `WatchdogSec` recovers the device if the manager wedges. So a failed `hostapd`
restart after a failed STA attempt cannot silently strand the device.

**Do not disrupt an active provisioning session.** The 5-minute STA retry is suppressed
while a user is actively provisioning (recent DHCP lease + HTTP activity on the AP), not
merely when a client is associated — and a user-triggered "retry now" is always
available, so a phone left idly associated cannot block auto-recovery forever.

**Credential handoff.** Submitted credentials are syntactically validated while the AP is
still up; the user is warned the hotspot will briefly drop; then the AP is torn down for
a bounded STA attempt; on failure the AP is restored with the error preserved for the
reconnecting user.

**Refinements (Fable review):**
- **First-boot prerequisites, or AP is dead-on-arrival:** set a default wireless
  regulatory country + `rfkill unblock wifi` before the first AP attempt (virgin images
  soft-block wlan0; hostapd needs `country_code` on the CYW43436). "rfkill blocked" is a
  detected on-screen fault code, not a silent failure.
- **Interface ownership:** exclude wlan0 from DietPi's own network scripts/dhcpcd so only
  the Connectivity Manager owns it, and name the STA DHCP client explicitly.
- **Evaluate `wpa_supplicant` native AP mode (`mode=2`) before hostapd** — it makes AP↔STA
  a `wpa_cli select_network` on one daemon and removes the hostapd start/stop handoff (the
  riskiest transition here). hostapd remains the fallback if `mode=2` is inadequate on
  brcmfmac. dnsmasq stays for DHCP + captive DNS either way.
- **Watchdog heartbeat aggregates all critical goroutines** (render + poller + manager);
  a healthy render loop must not pet the `WatchdogSec` while the manager is deadlocked.

## Consequences

- **Positive:** Lean image, fast boot; no dependency on NetworkManager. Auto-recovers
  from transient wifi outages without user intervention.
- **Negative:** We own low-level networking state transitions and the AP/STA toggle
  logic, which is fiddly and must be tested carefully (a bad transition can strand the
  device off-network). The periodic hotspot drop is a minor UX wart.
- Alternative (NetworkManager `nmcli`) was rejected for boot-time/footprint reasons
  despite its simpler API.
