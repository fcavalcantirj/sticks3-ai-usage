---
slug: ble-zero-config
date: 2026-09-07
status: open
round: 0
author_session: the 2026-09-06 overnight session that ran the task 75 spike, landed task 76, built 77-79, then pivoted to BLE
---

# Handover — BLE zero-config provisioning

## Mission

A stranger flashes the StickS3 from M5Burner and types **nothing but their Wi-Fi
password** — ideally not even that. Felipe's words: *"once we pair the sticks3 and mac via
bluetooth, WHY DON'T THE MAC DAEMON SEND EVERYTHING? SSID AND PASSWORD, EVERYTHING...
DAEMON IP, PORT... IT KNOWS."*

The daemon runs on the owner's computer, holds the AI provider keys, and is the thing that
knows every value the device needs. So it pairs over BLE and writes the lot.

## Where things stand (verified)

- [REAL] HEAD `05f891f`, tree clean. Ledger **80/84**; open: 77, 78, 79 (all name Felipe in
  their verify steps) and 83 (deferred by decision).
- [REAL] **The device is healthy and needs no cable.** Provisioned, on Wi-Fi at
  192.168.0.136, `sticks3-usage.local` resolves, agent reports `state connected,
  last_status 200`. OTA is armed. It runs the pre-BLE firmware.
- [REAL] `make fw-test` **Failed 0 (359 tests)**, `go build ./...` clean, firmware 33.5%.
- [REAL] **BLE costs +578,928 B flash, +20,904 B RAM** → 50.2% of the app partition when
  actually linked. Measured with a throwaway probe env, since deleted.
- [REAL] **`tinygo.org/x/bluetooth` v0.16.0 works on this Mac** — adapter enabled, 60
  devices in a 6 s scan. macOS is CENTRAL-only (no advertising, no GATT server), which is
  exactly right: the device advertises, the Mac connects.
- [REAL] Dependency footprint: 13 modules in `go.sum`, but only **4 compile on darwin** —
  `golang.org/x/sys/unix`, `sirupsen/logrus`, `tinygo-org/cbgo`, `tinygo.org/x/bluetooth`.
  Felipe approved this explicitly ("go for it"); it is the project's only dependency and
  `AGENTS.md` still says stdlib-only — **that rule has not been updated yet.**
- [REAL] `docs/BLE_PROVISIONING.md` is the wire contract and is complete: service and
  characteristic UUIDs, chunked framing correct at ATT_MTU 23, CRC-32, TLV payload,
  bonding with a 6-digit passkey displayed on the device.
- [REAL] `usage/bleprov.{h,cpp}` — the pure decoder — is done and host-tested.

### Unfinished, and NOT to be trusted

Three agents stalled mid-write. Their files compile, which is misleading:

- [UNVERIFIED] `hal/sticks3/bleprov.{h,cpp}` — **the BLE stack is not actually linked**:
  the firmware measures 33.5%, not the ~50% a real BLE build produces. It cannot work yet.
- [UNVERIFIED] `internal/bleprov/` (the Go central) and `internal/api/setup.go` — compile,
  never exercised against a radio.
- The dashboard half was **reverted rather than committed red**: the agent wrote tests for
  a setup card it never built and left a credential input in the page. `internal/web` is
  untouched at HEAD.

**Nothing in the BLE path has talked to hardware.**

## Blocking constraints (restate these before planning)

1. **A green test proves nothing across a process or hardware boundary.** Every serious
   defect in this project — six now — was documented behaviour that was wrong on the wire.
   The BLE code compiling is not evidence it works.
2. **Never log, render or commit a credential value.** Lengths or "set"/"unset" only. The
   daemon will be handling the user's actual Wi-Fi password.
3. **Never hand-start `usaged` on port 8765** — it prevents launchd binding and every
   later log reading is worthless. `make build && launchctl kickstart -k
   gui/$(id -u)/com.fcavalcanti.usaged`, then confirm with `lsof -nP -iTCP:8765 -sTCP:LISTEN`.
4. **Redraw-only-on-change**: the snapshot body is hashed into `rev`; anything per-request
   must live outside it. Verified intact after tasks 78/79.
5. **Every device upload needs Felipe's explicit authorization for that upload.**

## Hard-won facts, measured on hardware 2026-09-06

- **AP_STA is single-channel** (`esp_wifi.h:812-813`). The soft-AP adopts the station's
  channel, so joining the target network **drops every phone on the portal AP**. Outcomes
  must appear on the DEVICE SCREEN, never in the browser.
- **A scan with the SoftAP up takes ~9.2 s**, and `scanComplete()` kills a scan once
  elapsed exceeds `max_ms_per_chan * 20`. At 300 ms/chan that is a 6 s deadline and **100%
  of scans failed**; at 500 it is 10 s and they pass with ~800 ms to spare. Widen it further.
- Scanning works fine in AP_STA — 50 scans, 21-33 networks, zero client drops — **but only
  measured with the station CONNECTED.** With the station idle the scan runs long. The
  spike's numbers did not transfer, which is what made the portal look broken.
- **The captive sheet auto-pops on BOTH iOS and Android**, confirmed on the wire:
  `captive.apple.com` and `connectivitycheck.gstatic.com` probes arriving 3-6 s after
  association with no browser opened.
- `WIFI_SCAN_FAILED` (-2) and an empty scan (0) both occur and mean opposite things.
- **M5Burner cannot pre-configure a third-party firmware.** Its generic Wi-Fi plugin is a
  100-byte raw flash blob declared in the *catalog entry* (M5Stack-controlled), carrying
  SSID+password only — never a host or token. Written up in `docs/DEVICES.md`.
- The agent now advertises `_usaged._tcp` and `usaged.local` over mDNS, verified with
  `dns-sd`. This is what keeps a provisioned device working when the Mac's IP changes.

## Accepted residuals / refuted — don't redo these

- **Do not re-add the agent address or device token to the portal form.** That is the exact
  UX Felipe rejected. The daemon sends them.
- **Do not delete the SoftAP portal.** It costs 46 KB and is the fallback for Bluetooth
  off, headless daemons, or a refused permission. Felipe asked whether to ditch it; the
  answer was keep, because 1.4 percentage points buys the "it doesn't work" cases.
- **BLE passkey is 6 DIGITS, not letters** — that is the BLE specification, not a choice.
  Felipe asked for 4-6 letters; letters would need an app-layer code not bound to the link
  encryption. Display: size 6 default font, 216x48 px, **centred both axes** on the
  240x135 panel (x=12, y=44), nothing else on screen.
- **`muka/go-bluetooth` is out** — Linux/BlueZ only, archived July 2024.
- The `USAGED_FACTORY_RESET` build flag must ALSO suppress the secrets.h seed, or it wipes
  and immediately re-seeds and the device never comes up unprovisioned. Fixed; observed on
  hardware first.
- A dev build legitimately contains the five credential values (secrets.h is the first-boot
  seed), so `make fw-check-secrets` failing on it is correct. `make fw-publish-check` is
  the real gate: it builds with secrets.h moved aside and greps that image.

## Hard rules & human-reserved decisions

- Tasks whose verify step names Felipe do not flip to `passes: true` until he says so.
  That is 77, 78, 79.
- **Whether to publish a firmware build IS FELIPE'S CALL.**
- Powering the device on, and any button-map change, IS FELIPE'S CALL.
- The BLE dependency is approved; **updating `AGENTS.md`'s stdlib-only rule to record the
  exception is still owed.**

## next_action

Rebuild `hal/sticks3/bleprov.{h,cpp}` properly — the stalled agent's version does not link
the BLE stack. Verify by flash size: a real BLE build is ~50%, not 33.5%. Then the Go
central, then the dashboard's one-click card, then hardware.

## Open questions

1. Should the device advertise BLE only while unprovisioned, and can `BLEDevice::deinit()`
   actually reclaim the ~579 KB at runtime? The stalled agent was asked and never answered.
2. Where does the daemon get the Wi-Fi password — login keychain via `security
   find-generic-password` (one authorization prompt) or typed once in the dashboard?
3. Does the onboarding text belong in the M5Burner listing, the dashboard, or both? Felipe
   asked that installation say: run the daemon first, configure providers there, then the
   stick.

## Pointers

- `docs/BLE_PROVISIONING.md` — the wire contract, authoritative for both halves.
- `docs/DEVICES.md` "## Provisioning spike (task 75)" — every measured hardware fact.
- `spec.json` tasks 76-79 carry `AMENDED BY SPIKE (2026-09-06)` steps.
- Secrets live in `firmware/include/secrets.h` and the repo-root `.env`, both gitignored.
