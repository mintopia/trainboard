# M5 — Self-update from GitHub releases (design)

**Date:** 2026-07-09
**Issues:** #16 (A/B slots + signed manifest), #17 (keyring + version epochs),
#18 (stable launcher + boot counter + rollback), #19 (trigger UI + opt-in auto)
**Status:** Approved

## Goal

The device updates itself from GitHub releases: signed, downgrade-proof,
A/B-slotted with automatic rollback, triggered manually from the web UI by
default with opt-in auto-apply. A bad release can never brick the board or
require anything worse than one attended recovery over the AP/web UI.

## Decisions made during brainstorm

- **Key custody:** the release-signing minisign private key lives in a GitHub
  Actions secret; CI signs on tag push. Accepted risk (hobby device on a
  trusted LAN, consistent with ADR 0004's threat model).
- **Emergency recovery:** a second, offline **recovery keypair**, generated
  once on the operator's machine; private half only in the operator's password
  manager, never in CI. Both public keys ship in the device keyring, so a CI
  key compromise/loss is recoverable by shipping a signed update (signed by
  the recovery key) rather than SSHing every device.
- **Versioning:** semver git tags starting at `v0.1.0`. GitHub's "prerelease"
  flag on a release is the prerelease channel; latest non-prerelease is
  stable.
- **Launcher shape:** a tiny separate Go binary that `exec()`s the active
  slot (approach A). Rejected: bash `ExecStartPre` (safety-critical logic in
  untestable shell) and systemd `OnFailure`/`StartLimitBurst` machinery
  (counts restarts-per-interval, not boots-per-update — can't distinguish a
  bad slot from a WiFi flap).

## 1. Release pipeline, manifest, signing

### Pipeline

New `.github/workflows/release.yml`, triggered by tag push `v*`:

1. Run the same gate as `ci.yml` (tests, lint, vet).
2. Build linux/arm64 `trainboard` (version stamped via the existing
   `-ldflags -X …buildinfo.version=` hook = the tag) and
   `trainboard-launcher`.
3. Write `manifest.json`, sign it with minisign using the CI secret key.
4. Create a GitHub release with assets:
   - `trainboard_vX.Y.Z_linux_arm64.gz`
   - `trainboard-launcher_vX.Y.Z_linux_arm64.gz`
   - `manifest.json`
   - `manifest.json.minisig`

Cutting a prerelease = ticking GitHub's "prerelease" checkbox (and the
manifest carries `"channel": "prerelease"`).

### Manifest

The manifest is the only signed object; it binds everything else:

```json
{
  "version": "v0.2.0",
  "channel": "stable",
  "commit": "abc1234",
  "arch": "linux/arm64",
  "asset": "trainboard_v0.2.0_linux_arm64.gz",
  "sha256": "<hex of the DECOMPRESSED binary>",
  "min_version": "v0.1.0"
}
```

### Keys and trust (#17)

- Two minisign keypairs: **CI key** (Actions secret) and **recovery key**
  (offline, operator-held).
- Both public keys are embedded in the payload binary as the **keyring**. A
  manifest verifies if *any* keyring key produced the signature.
- Device-side verification uses `github.com/jedisct1/go-minisign`
  (verify-only library by minisign's author).
- **No wall-clock trust checks anywhere** — no key expiry, no signature
  timestamps. The Pi has no RTC; after power loss and before NTP it must
  still be able to verify updates.

### Anti-rollback / replay ("version epochs")

All checks run against the *signed* manifest, in the payload, before any
bytes are written to a slot:

1. `arch` must equal `linux/arm64`.
2. `version` must be strictly greater than the running version
   (`golang.org/x/mod/semver`).
3. `version` must be ≥ the device's persisted **version floor** — the
   high-water mark of every `min_version` it has ever accepted, stored in
   updater state. Raising `min_version` in a release raises the floor
   fleet-wide; replaying an old validly-signed manifest below the floor is
   rejected.

Key rotation rides the same mechanism: a new release embeds an updated
keyring and raises `min_version` past the last release the retired key
signed.

## 2. Device layout, launcher, rollback (#16, #18)

### Filesystem layout

```
/opt/trainboard/
  launcher                    # stable shim, manually installed, NOT touched by A/B
  slots/a/trainboard          # payload binaries
  slots/b/trainboard
/var/lib/trainboard/
  config.json                 # existing
  updater/state.json
```

`state.json` schema:

```json
{
  "active": "a",
  "active_version": "v0.2.0",
  "known_good": "a",
  "boot_attempts": 0,
  "version_floor": "v0.1.0",
  "rolled_back_from": ""
}
```

All state writes use the existing temp-file → fsync → rename pattern from the
config store. The systemd unit's `ExecStart` becomes
`/opt/trainboard/launcher --production`.

### Launcher (`cmd/trainboard-launcher`)

~200 lines; imports nothing from the rest of the app except the state-file
schema package. Logic, in order:

1. Read `state.json`. Missing or corrupt → safe defaults
   (`active=a, known_good=a, attempts=0`) — degrade, never refuse to boot.
2. `boot_attempts++` and **write state before exec**, so a slot that
   segfaults instantly still burned an attempt.
3. Select slot:
   - `attempts ≤ 3` → active slot.
   - `attempts > 3` and `active ≠ known_good` → **rollback**: set
     `active = known_good`, reset counter, set `rolled_back_from` to the
     state file's `active_version` (recorded at apply time — the launcher
     never has to interrogate a binary for its version), exec known-good.
   - `attempts > 3` and `active == known_good` → **double fault**: exec
     known-good with `--recovery`.
4. `exec()` the slot binary, passing through args. Process replacement keeps
   the main PID, so the unit's `WatchdogSec=30` / `NotifyAccess=main` work
   unchanged.

The launcher always execs *something*; it never sits in an exit/restart loop
of its own.

`--recovery` mode (in the payload): AP up + web UI + on-screen fault code
only. No Darwin polling, no updater, minimal code paths. From the recovery
web UI the operator can fix config or manually apply a known-working update.

### Health check → known-good promotion

Runs **inside the payload** (the launcher never judges health — #18): once
the render loop is up *and* the embedded web server answers a loopback
self-probe, within 60 s of start, the payload writes
`boot_attempts = 0, known_good = active`.

Consequence: attempts reset on every healthy start, so WiFi flaps and
watchdog reboots never accumulate strikes; only a never-healthy slot reaches
3. Once known-good is promoted, further rollback for that slot is inherently
suppressed.

### Apply flow (`internal/update`, in the payload)

1. Download `manifest.json` + `.minisig` for the selected release; verify the
   signature against the keyring; run the three §1 checks.
2. Download the asset to a temp file **inside the inactive slot's directory**
   (same filesystem → atomic rename), gunzip streaming, verify `sha256`,
   `chmod +x`, fsync, rename to `slots/<inactive>/trainboard`.
   **The known-good slot is never written** — that is the double-fault
   guarantee (#18).
3. Update state: `active = <inactive>`, `active_version = manifest.version`,
   `boot_attempts = 0`, `version_floor = max(floor, manifest.min_version)`.
   `known_good` keeps pointing at the old slot.
4. Clean process exit (existing apply-by-restart pattern) →
   `Restart=always` → launcher execs the new slot → health check promotes it,
   or three failed boots roll it back automatically (worst case ≈ 3–4 min of
   self-healing).

### Launcher lifecycle

Never updated via A/B. It is version-stamped and shipped as a release asset,
but installing a new one is a deliberate manual `scp` step per deploy.md. Its
interface — the state-file schema and "exec the active slot" contract — is
frozen so old launchers run new payloads indefinitely.

## 3. UI, config, error handling, migration, testing (#19)

### Config

New `update` section in `config.json`:

| Field | Default | Meaning |
|---|---|---|
| `channel` | `"stable"` | `"stable"` or `"prerelease"` |
| `autoApply` | `false` | apply updates unattended |
| `checkEnabled` | `true` | periodic update checks |

### Check → surface → apply

- Background checker in the runtime polls the GitHub releases API every 6 h,
  plus once shortly after boot when connectivity is up, with jitter.
  Unauthenticated API; one device is far inside rate limits.
- Latest release matching the channel with `version > running` → sets an
  "update available" flag in the runtime snapshot.
- Surfaced as:
  - a **subtle on-screen hint** — a small marker in the board's status area;
    exact pixel treatment decided at implementation against the scene
    contract (golden-image tested like everything else). Not a modal
    takeover.
  - a **web UI "Software" section** on the status page: running version,
    available version, release-notes link, *Check now* and *Apply update*
    buttons, behind the existing CSRF/auth machinery (same pattern as
    `/actions/restart`).
- **Auto-apply** (opt-in): applies during the configured powersave window
  (display already dark), or 03:00–05:00 Europe/London if none is configured.
  Manual apply works any time.

### Error handling

- Update failures are **non-fatal by design**: signature/arch/downgrade
  rejection, download timeout, SHA mismatch → log to journald (existing
  slog/obs), keep running the current binary, show the reason in the web UI
  Software section, retry at the next check.
- A completed rollback is surfaced prominently in the web UI ("rolled back
  from v0.3.0 — 3 failed boots") from the `rolled_back_from` marker; marker
  clears when the operator acknowledges or a later update succeeds.
- Recovery mode gets a new on-screen fault code (next free `E##` in the
  deploy.md table).
- Corrupt updater state degrades to safe defaults; it never prevents boot.

### Migration of the deployed Pi

New deploy.md section + `deploy/migrate-to-slots.sh`, one attended SSH
session: create `/opt/trainboard/slots/{a,b}`, move the current binary to
slot a, install the launcher, seed `state.json`, swap the unit's
`ExecStart`, `daemon-reload`, restart. Combinable with the pending
"deploy main" step. First real self-update test: v0.1.0 → v0.1.1.

### Testing

- **Unit (red/green TDD):**
  - manifest verification against a test keyring: good sig, bad sig, unknown
    key, wrong arch, downgrade, floor violation, replayed old manifest;
  - semver ordering edge cases;
  - state-file round-trip + corrupt-file → defaults;
  - slot-selection as a table: fresh boot / crashing new slot / rollback /
    double fault / suppress-after-known-good;
  - apply flow against `httptest` fake release server + temp dirs (including
    truncated download, SHA mismatch, disk-full-ish rename failures).
- **Launcher integration:** Go test compiles tiny fake payloads (exit-0,
  exit-1, sleep-forever) and drives the launcher through the full rollback
  story on a temp filesystem.
- **CI:** the release workflow gets a PR-triggered dry-run path — build,
  sign with a throwaway key, assert the manifest verifies — so the pipeline
  is tested before the first real tag.
- **On-hardware checklist** (deploy.md): real apply end-to-end,
  pull-the-plug mid-download, deliberately broken payload → observe
  rollback, forced double fault → recovery mode.

## Out of scope

- Updating the launcher automatically (manual scp only, by design).
- Delta/differential updates, multiple architectures, fleet management.
- OS/package updates — this updates the trainboard binary only.
