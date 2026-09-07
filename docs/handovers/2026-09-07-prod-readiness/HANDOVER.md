---
slug: prod-readiness
date: 2026-09-07
status: approved
round: 1
author_session: the 2026-09-07 session that shipped BLE zero-config, published to GitHub + M5Burner, then found nine shipped bugs the hard way and ran an audit for the rest
---

# Handover — get ai-usage prod ready

## Mission

The product works. A stranger can flash from M5Burner, run one curl command, and
click once to set up the stick — that path is **proven on hardware, end to end,
with the real published artifacts** (2026-09-07 15:53).

What is not ready is everything around it. Nine bugs shipped today, and an audit
found twenty more. **Your job is the confirmed list in `AUDIT.md`, in the order
below, until nothing on the dashboard lies.**

## THE ONE THING TO UNDERSTAND FIRST

Every single bug today had the same root cause:

> **Anything that crossed a boundary out of the process was replaced by a fake in
> tests, so the real path was never executed once.**

| what shipped broken | what faked it |
|---|---|
| keychain never stored a key | `FakeKeyStore` in every test |
| dashboard was blank | tests grepped `index.html` for id strings, never parsed the JS |
| Device Wi-Fi card | Go side tested; **no firmware side existed at all** |
| retry→portal never fired | machine host-tested, call site missing from `main.cpp` |
| released daemon ≠ tested daemon | tested a local `make build` |
| install died on a clean Mac | only ever tested where it already existed |
| BLE panic killed the daemon | error path never executed |
| every settings save discarded | `ConfigPath` empty when no file exists yet |
| loopback 401 on every mutation | fresh-install auth never exercised |

**So: when you fix something, the fix is not done until the REAL thing has run.**
Not a fake, not a unit test, not a green build. Run the binary, hit the endpoint,
read the log, look at the wire.

## Where things stand (verified)

- [REAL] **End to end works.** Fresh purged Mac → `curl | bash` from GitHub →
  M5Burner flash → BLE setup → `peer 192.168.0.194 GET /v1/usage 200`.
  Verified 15:53 and again after the rename.
- [REAL] Published: <https://github.com/fcavalcantirj/sticks3-ai-usage>,
  release **v0.1.8**, firmware **v0.1.7** on M5Burner (Felipe uploaded it).
  Releases v0.1.3–v0.1.7 were withdrawn; each had a shipped defect.
- [REAL] `make verify` 13/13, `make fw-test` 359/0, `make fw-publish-check`
  PUBLISHABLE, flash 50.7%.
- [REAL] **Two audit findings already fixed and pushed today**: the keychain
  never stored a provider key (`security -w` reads the TTY, not stdin), and
  every settings change was discarded (`DefaultConfigFile` missed the rename +
  `ConfigPath` empty when no file existed). Both verified on the running daemon.
- [REAL] `AUDIT.md` in this directory: 27 candidates, **20 confirmed**, each
  re-proved by running its own `how_to_prove` before being kept. Two of those
  twenty are the ones just fixed.

## The work, in order

Do them in this order. The first four are what "prod ready" mostly means here:
**nothing on the page lies.**

1. **Enabled toggle is inert** (AUDIT #20). `PUT /v1/config` persists it; the
   scheduler never reads it. Disabling a provider does nothing at all.
2. **Alerts card does nothing** (#10). Both inputs save and persist; nothing in
   the daemon or firmware reads either value.
3. **"Pair a device" card cannot work** (#9). Server half is complete;
   `firmware/src/hal/sticks3/pair.cpp` is 400 lines that `main.cpp` never
   includes, so the linker discards it. Either wire it or remove the card — the
   Device Wi-Fi card was removed today for exactly this reason.
4. **Models tab shows wrong numbers** (#8). "Tokens month" is hardcoded `0`;
   "Tokens today" is actually all-time; "Est. cost month" is all-time cost.
5. **`/v1/device/netcfg`** (#3). Dead at both ends AND a token-less loopback
   caller can read back a staged Wi-Fi password. Remove the endpoint, or gate
   it. Do not leave it live.
6. **BLE keychain hang** (#2). `creds_darwin.go` has ZERO tests. If the keychain
   dialog is not answered, Gather returns an empty password and the device is
   provisioned onto a WPA network as **open** — it fails to join and the real
   cause appears only in the daemon log.
7. **Scan goroutines have no `recover`** (#18, #19). `runProvision` got one
   today after a library panic killed the daemon; `runScan` did not.
8. **Upgrade from v0.1.1/v0.1.2 mints a NEW device token** (#14) and orphans
   every already-paired stick.
9. `codex_source=cli` is completely dead (#1) — opt-in, non-default, fails to an
   error badge rather than wrong numbers. Lowest priority; consider deleting it.

Cosmetic/info, probably not worth doing: #4, #5, #6, #7, #11, #15, #17.

## Blocking constraints (restate these before planning)

1. **A green test proves nothing across a boundary.** See the table above. Nine
   times today. Run the real thing.
2. **Never hand-start the daemon.** `make build` then
   `launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.ai-usage`, then confirm
   with `lsof -nP -iTCP:8765 -sTCP:LISTEN` that the PID is launchd's.
3. **`make build` output is UNSIGNED, and copying it over `~/.local/bin/ai-usage`
   gets the daemon killed with `OS_REASON_CODESIGNING`.** After any `cp`, run
   `codesign --force --sign - ~/.local/bin/ai-usage`. This cost a confusing
   debugging detour; `dist.sh` ad-hoc signs for exactly this reason.
4. **Never log, render or commit a credential.** Lengths or `set`/`unset` only.
5. **Every device upload needs Felipe's explicit authorization for that upload.**
   He is using the stick.
6. **Never edit `.env` or `firmware/include/secrets.h`.** Backups exist at
   `~/ai-usage-backup-2026-09-07-purge/` and `~/usaged-backup-2026-09-07/`.
   `secrets.h` is the only copy of the board's OTA password.

## Accepted residuals / refuted — don't redo these

- **The Device Wi-Fi card was removed deliberately** (no firmware side). Do not
  bring it back without building `netcfg` on the device.
- **Loopback needs no device token** for mutating routes. This supersedes
  ORDER #54 and is deliberate — the token was never what protected those routes;
  the JSON content-type requirement is (no CORS headers, so a cross-origin JSON
  POST is preflight-blocked). **The LAN still requires the token.** Eight tests
  were retargeted to that boundary rather than deleted.
- **`security add-generic-password -w` takes the key as an ARGUMENT**, not
  stdin. Stdin looks safer and stores an empty password. The `ps` exposure is
  the accepted tradeoff and is documented at the call site.
- **BLE window is 10 minutes**, not 2. Two was measured as useless.
- The firmware is renamed: AP/BLE name `ai-usage-XXXX`, mDNS `_ai-usage._tcp`.
  Firmware and daemon must move together — it is a contract.
- `USAGED_*` env vars were deliberately NOT renamed.

## Traps that cost real time today

- **A reflashed device leaves a one-sided Bluetooth bond**, and macOS hides a
  known-but-unpairable peripheral from scans *entirely*. It looks exactly like
  the device is missing. Forget it in System Settings → Bluetooth.
- **macOS Bluetooth can wedge**; only a daemon restart brings scanning back.
- `vbus=0` in the boot line means the stick is on battery and will sleep in
  ~19 s, so it vanishes from scans.
- A serial monitor holding `/dev/cu.usbmodem101` blocks M5Burner from flashing
  `/dev/tty.usbmodem101` — same device.

## Hard rules & human-reserved decisions

- **Whether to remove a feature vs. build its firmware half IS FELIPE'S CALL**
  (applies to items 3 and 5). He chose removal for the Wi-Fi card.
- **Publishing to M5Burner and cutting releases IS FELIPE'S CALL.**
- Powering the device on, and any button-map change, IS FELIPE'S CALL.
- `spec.json` tasks whose verify step names Felipe do not flip to `passes: true`
  until he says so.

## Acceptance checklist (the author approves the plan ONLY against these)

1. The plan addresses items 1–5 above, in that order, and says explicitly what
   it will do about each (fix / remove / defer with reason).
2. For every item, the plan names **how it will be verified against the running
   system** — which command, which log line, which endpoint. Not "add a test".
3. Any control the plan keeps on the dashboard is backed by working code end to
   end; anything not backed is removed rather than left rendering.
4. The plan does not introduce a second spelling of any path, name or constant.
   Two of today's bugs were exactly that.
5. No credential value is logged, rendered, returned or committed.
6. The plan states that `codesign --force --sign -` follows any copy to
   `~/.local/bin/ai-usage`.
7. Nothing is released or flashed without Felipe saying so.
8. Every claim in the final report is labelled `[REAL]` / `[TEST]` /
   `[UNVERIFIED]` with the evidence.

## next_action

Read `AUDIT.md` in this directory, then fix item 1 (the inert Enabled toggle):
make `buildFetchers` in `cmd/usaged/once.go` honour `EffectiveProviders().Enabled`,
and verify by disabling a provider in the dashboard and watching it disappear
from `GET /v1/usage` on the running daemon.

## Open questions

1. Items 3 and 5 — remove the card/endpoint, or build the firmware half? Felipe's
   call; he chose removal last time.
2. Is `codex_source=cli` worth keeping at all?
3. The onboarding text for M5Burner (run the daemon first, configure providers,
   then the stick) — listing, dashboard, or both? Still open from this morning.

## Pointers

- `AUDIT.md` (this directory) — all 20 confirmed findings with reproduction commands.
- `docs/AGENT_PROMPT.md` — the paste-into-an-agent installer, linked from the README.
- `docs/handovers/2026-09-07-ble-zero-config/` — the earlier BLE handover and its review.
- `docs/DEVICES.md` — hardware facts and the button map.
- Secrets: `firmware/include/secrets.h` and repo-root `.env`, both gitignored.
