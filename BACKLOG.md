# Backlog

Deferred by decision, not by omission. The project closed at **78 of 83 ledger
tasks passing** (`spec.json`). The remaining open items are the optional task hub
(item 1) and the hardware-UAT of the SoftAP portal (task 77, inside item 3
below). Item 3's build — NVS store, captive portal, BLE protocol, and the
publish-check gates — shipped in v0.2.1 (2026-09-08).

> **Before publishing a firmware build anywhere, read item 3.** `secrets.h`
> values are compiled into `firmware.bin` as plaintext — including the Wi-Fi
> password — so the published image must be built without `secrets.h`
> (`make fw-publish-check`). That gate and the runtime provisioning that makes
> it possible shipped in v0.2.1 (2026-09-08); item 3 below is resolved, with
> only the SoftAP portal's on-device UAT still pending.

## 1. Task-hub rows — `POST /v1/rows` (spec.json task 83)

Let any process push a row into the snapshot, namespaced `ext:`, with a TTL.
It then appears on the device and the web page.

**Cheap because the device needs zero new code** — the snapshot already carries
N providers, `rev` hashing already covers them, the 304 path already works, and
the firmware already renders provider blocks. An external row is just another
provider. Roughly an hour.

**Not built because the design question is unanswered: what would actually be
pushed to it?** The device currently answers one question well — am I burning
my quota. A general row-pusher is how that focus dilutes. The original research
verdict on merging AIUsage with TaskHub was that it "solves the wrong problem",
and this is the same tension in miniature.

Three things to weigh if it is revived:
- **The TTL is the entire safety mechanism.** Without it a crashed publisher
  leaves a stale row on the desk forever, and an ambient display that lies is
  worse than a blank one. Expiry must change `rev` so the row disappears.
- **It is a desk feature, not a battery feature.** With the 12 h backstop a
  pushed row is invisible until the device wakes.
- **It costs a page**, on a device that already has four plus the instructions
  page. A page that is empty most of the time makes the device worse.

Revive it only with a concrete publisher in mind (a Temporal run, a worker's
state, a deploy in flight) — not as a place to put things.

## 2. Blue-button hold → reach an AI agent / Claude Code

The gesture is **already reserved and deliberately dead** — see the button map
in `docs/DEVICES.md`. Nothing else may claim it. The refresh that used to live
there was dropped because the side-button click already refreshes.

Unspecified: what "reach an agent" means on a device with no keyboard. Probably
a `POST` to something that starts a run, with the result surfaced as a row —
which makes this a natural consumer of item 1 above, and the concrete publisher
that would justify it.

## 3. Onboarding — provisioning (RESOLVED, shipped in v0.2.1)

**Resolved 2026-09-09.** v0.2.1 was published on 2026-09-08 with the runtime
provisioning system from the design below. The firmware can now be published
safely: `make fw-publish-check` outputs `PUBLISHABLE` (built with no
`secrets.h`, none of the five credential values in the binary), and
`scripts/release.sh` enforces it in step 4/8. The merged-image check
(`aa50` at `0x8000`) is enforced in step 5/8.

What shipped and is verified by build/tests (not hardware — see the block below):

- **NVS credential store + state machine** (provision.cpp, provision.h,
  test_provision.cpp — 727 host-test lines) — spec.json task 75 (PROVISIONING
  1/4), passes=True. This is the mechanism `check_no_secrets.sh` references
  ("Task 76 moves the Wi-Fi SSID… out of the compiled image and into NVS").
- **Captive portal pure core** (portal.cpp, portal.h, test_portal.cpp — 747
  host-test lines) and **BLE provisioning protocol** (bleprov.cpp, bleprov.h,
  test_bleprov.cpp — 649 host-test lines) — the pure C++17 halves in
  `firmware/src/usage/`, host-tested by `make fw-test` (46+ tests pass).
- **Build gates** — `publish_check.sh` (atomic: stash secrets.h → rebuild →
  assert no 5 values in binary → restore) and `check_no_secrets.sh` (checks
  all 5 macros: WIFI_SSID, WIFI_PASS, USAGED_HOST, USAGED_DEVICE_TOKEN,
  OTA_PASS).
- **Prod-ready cleanup** — spec.json task 87 (PROD-READY 4/10) deleted the
  linker-discarded pair.cpp/pair.h and fixed the captive portal copy.

Spec-ledger status for the provisioning group:

| task | description | state |
|---|---|---|
| 75 | PROVISIONING 0/4 — SPIKE, answer the five unknowns | passes=True |
| 76 | PROVISIONING 1/4 — NVS credential store + state machine | passes=True |
| 77 | PROVISIONING 2/4 — SoftAP captive portal hardware UAT | **BLOCKED** — awaiting Felipe's hardware UAT |
| 78 | PROVISIONING 3/4 — pairing code | WITHDRAWN (not built) |
| 79 | PROVISIONING 4/4 — change Wi-Fi from Settings | WITHDRAWN (not built) |

**What remains is hardware only.** The device-side SoftAP portal (task 77)
needs Felipe to UAT from a phone — this is the `[BLOCKED]` task nobody can
self-certify. The build/test gates prove the image is publishable; the on-device
portal flow is Felipe's to confirm. No new code is needed here; the
`FLASH REQUEST` for task 102 (sha `2f9f64d`) carries the full overlay.

### The leak (verify before ever publishing a build)

`secrets.h` is a compile-time header, so its values become **plaintext strings
inside `firmware.bin`**. Confirmed by `strings` against a real build: the Wi-Fi
SSID, the **Wi-Fi password**, the device token and the host address are all
recoverable from the binary. Publishing that build — to M5Burner or anywhere —
hands out the network password of whoever built it. `secrets.h` is gitignored,
which protects the repo and does nothing for the artefact.

### The failure for a new user

Someone who flashes a published build gets a device that tries to join **our**
SSID and talk to **our** Mac at a fixed address. It cannot work, and there is
no screen anywhere that lets them fix it.

### The fix is now fully specced

**`spec.json` tasks 75-79 — 75 is a SPIKE that must run first (PROVISIONING 0/4 … 4/4).** Researched September 2026;
the reasoning behind the shape is below, the executable detail is in the ledger.

**The decisive constraint:** every off-the-shelf framework — Espressif's
`WiFiProv` (already in our Arduino core), Improv, WiFiManager — conveys **Wi-Fi
credentials only**. This device additionally needs to learn *which agent* to
talk to and to obtain a *device token*. A generic BLE provisioning app solves
half the problem and leaves a second setup step; a custom portal collects all
four fields on one screen. That, more than any feature comparison, is why the
captive portal wins.

**Build order, which is design rather than preference:**

1. **Credential store + state machine** (task 76) — NVS record, validation, and
   the N-failed-joins-back-to-portal rule, so a moved house never needs a cable.
   Pure C++17, host-tested, no radio. Includes the build check that fails if any
   credential appears in `firmware.bin`.
2. **SoftAP captive portal** (task 77) — the only step that cannot live on the
   Mac. Per-device AP name from the MAC, **WPA2-protected with the password shown
   on the device screen** (an open AP lets a neighbour reach setup; the screen is
   why we can do this without a printed label). Live scan, hidden-SSID path, DNS
   hijack so the sheet pops by itself on iOS and Android.
3. **Pairing code** (task 78) — short, unambiguous alphabet, `crypto/rand`,
   short-lived, single-use, rate-limited, LAN-only. The agent issues the token so
   the build never carries one.
4. **Wi-Fi under Settings** (task 79) — Felipe's original request, and correctly
   *last*: the Settings page is served by the Mac, so it maintains rather than
   bootstraps. Its hard part is the failure path — a bad password must fall back
   to the old network or the portal, never strand the device.

**Rejected, with reasons:** BLE via `WiFiProv` is slicker and Espressif ships
official phone apps, but it needs an app install and still only carries Wi-Fi —
keep as a later addition. Improv is neat over Web Serial while the user already
has a cable in hand, but it is Chrome/Edge only and, again, Wi-Fi only. Entering
a password **on the device** with two buttons is miserable and was ruled out;
picking an SSID from a list that way would be fine.

**Worth one cheap experiment first:** M5Burner has its own Wi-Fi configuration
step that injects credentials at flash time rather than into the binary. If we
can read whatever it writes, a published build could arrive pre-configured with
no portal at all. It may be UIFlow-specific, but confirming that costs very
little and would shortcut the whole group.

### Constraints already established

- **ESP32-S3 is 2.4 GHz only.** A 5 GHz-only or band-steering SSID simply will
  not appear in the scan; the portal must say why rather than showing an empty
  list.
- **Hidden SSIDs** need a manual-entry path.
- **Fall back to the AP** after repeated join failures, or a moved house means a
  cable.
- A hosted (non-local) pairing page needs a relay and is a much larger build.
  Local first.

## 4. Unpriced Codex models understate cost

`stats: unpriced models in cost estimate` fires on every scan for
`codex-auto-review` and `fugu`. Neither exists on models.dev, so
`scripts/sync-prices.sh` cannot fill them — see the deliberate note in
`internal/stats/prices.go`. Cost figures are understated by whatever those two
consumed. The `partial` marker means the UI is not lying, only incomplete.
Needs a manual price source or an explicit "unpriced" line in the breakdown.

## 5. OpenRouter balance is not surfaced on the status line

Dropped by choice when the stale ChatGPT credits field was removed: ChatGPT
reports a *count* of reset credits, not dollars, and the only real balances are
OpenRouter's. They were at **$0.02** and **$0.55** at close, both under the
configured $1 alert threshold, which the alert path does cover. Revisit only if
the alert proves too quiet in practice.
