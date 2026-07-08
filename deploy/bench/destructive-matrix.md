# mode2 destructive matrix (issue #13)

Run these by hand at the bench, one at a time, on top of a device already
running with `--manage-network` (M3b migration done, per `docs/deploy.md`
§8) — not as part of `eval-mode2.sh`, which only covers the three
non-destructive-ish experiments (AP bring-up, toggle x10, bad-PSK
AP-restore). This matrix is the harder failure injection issue #7/#13
called for: kill daemons out from under a live manager and watch what the
board does, with the same dead-man-switch discipline as `eval-mode2.sh`
(arm it before each row; a wedge here is exactly the scenario that switch
exists for).

Fault codes and stage names below match `docs/deploy.md` §6 and the scenes
rendered as of commit c621208 (`internal/board/scenes.go`,
`internal/net/check.go`'s `Stage` constants).

| # | Scenario | Setup commands | Expected board behavior | Expected recovery | Observed |
|---|---|---|---|---|---|
| 1 | Bad PSK | Set `wifi.psk` in `/config` to a wrong value for a real, in-range SSID; force a retry (`retry-now` action or wait for STA attempt) | STA association fails at `StageAssoc` — the fault that condition maps to is **E06** (`FaultConnectivity`, detail `ASSOC`, per `internal/runtime/composite.go`), but a non-nil `Hotspot` always outranks it, so on-glass the board falls back to (or stays on) the **hotspot scene** (AP mode) with no visible fault code; `LastSTAError` in logs/journal shows the association failure | Correcting the PSK via `/config` (from the hotspot's portal) and retrying succeeds; no manual bench intervention needed | |
| 2 | Missing SSID | Set `wifi.ssid` to an SSID that is not currently broadcasting (typo, or the real AP powered off) | `wpa_supplicant` never associates; `pollStatus` (`internal/net/driver.go`) exhausts 10x500ms; `StageAssoc` failure, same as row 1 — same underlying **E06**/`ASSOC`, masked the same way by the **hotspot scene** (AP mode) | Same as row 1 — fix via portal once the AP the board provides is reachable | |
| 3 | DHCP timeout (dnsmasq killed) | While the board is in AP fallback with dnsmasq confirmed alive, `pkill -9 -F /run/trainboard-dnsmasq.pid` | **Nothing changes on-glass immediately** — `Dnsmasq.Alive` is only checked inside `attemptAP`'s verify step (`internal/net/manager.go`), not polled continuously, so the **hotspot scene** (AP mode) keeps rendering (no fault code — the manager still considers the AP up) while new clients silently get no DHCP lease | Self-heals only at the next periodic STA-retry cycle (~5 min per ADR 0003): the manager tears down, retries STA, and on falling back to AP again calls `Dnsmasq.Start` fresh, restoring the leases. A manual "retry now" also forces this sooner | |
| 4 | Daemon crash (`pkill -9 wpa_supplicant` mid-AP) | With the AP up and a client associated, kill the wpa_supplicant PID directly | Existing associated clients drop immediately (radio gone); the **hotspot scene** (AP mode) may keep rendering stale state until the next check — still no on-glass fault code, since a non-nil `Hotspot` outranks **E05**/**E06** in `composite.go` even once the manager notices the dead radio | Self-heals at the next periodic retry cycle: `ensureDaemon`'s `wpa_cli status` fails, so `StartAP` starts a fresh `wpa_supplicant -B -i wlan0 -c <conf>` and re-selects the AP network — the ~10-20s AP drop the ADR already documents for the retry loop | |
| 5 | Reboot mid-transition | Trigger `reboot` (or pull power) while the manager is in `ManagerSTARetry` (AP torn down, STA attempt in flight — watch `journalctl -u trainboard -f` for the state) | No stuck state persists across a reboot — the manager has no on-disk transition state. On boot, `trainboard.service` starts fresh at `ManagerBoot` and runs the normal STA-attempt-then-AP-fallback sequence; **E04** (config error) does not apply here — this row's config is validly shaped, just briefly mid-transition | Normal cold boot recovers on its own; confirm it lands on either the departure board (STA succeeded, no fault) or the **hotspot scene** (STA failed, AP mode), never stuck on the initialising scene | |
| 6 | Client associated during retry | Associate a phone to the hotspot and keep hitting the portal (`/setup` or `/config`) with HTTP requests as the 5-minute STA-retry mark approaches | The retry is **suppressed** — `NoteProvisioning`'s recent-DHCP-lease + recent-HTTP-activity window (ADR 0003: "Do not disrupt an active provisioning session") holds the **hotspot scene** (AP mode) up rather than tearing it down for a STA attempt; no fault code | Provisioning finishes (or activity goes idle past the window) and the next retry proceeds normally; a manual "retry now" from the portal is always available and is not suppressed | |

Fault-code cross-reference for this matrix (`docs/deploy.md` §6):

- **E04** (config error) — does not apply to any row here; all six start from a
  validly-shaped config (just a wrong PSK/SSID, or an in-flight transition),
  never a missing/invalid config file.
- **E05** (radio blocked, rfkill/regulatory-domain) — does not apply to any
  row here either; none of these inject a radio block. It's exercised
  separately (`rfkill block wifi` or leaving the regulatory domain unset) if
  that scenario is ever added to this matrix.
- **E06** (connectivity, stage detail) — the real underlying fault for rows 1
  and 2 (`ASSOC`), but never visible on-glass in this matrix because the
  board is always still offering its AP hotspot when it would otherwise show
  it; `composite.go`'s `HotspotSnapshotSource` deliberately lets a non-nil
  `Hotspot` outrank E05/E06 on every row.

## Notes for the bench operator

- Fill in the **Observed** column live; timestamps and `journalctl` excerpts
  are more useful here than a bare pass/fail.
- Row 3 and row 4 are intentionally *not* fast — if the board self-heals in
  under a minute instead of waiting out the retry cycle, that's worth
  flagging (it means something is polling more aggressively than ADR 0003
  describes, which changes the failure-mode story).
- These six rows plus `eval-mode2.sh`'s three experiments are the full
  input to the ADR 0003 addendum (mode2-vs-hostapd verdict) — don't land
  the addendum on partial data.
