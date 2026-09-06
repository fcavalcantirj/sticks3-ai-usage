# Backlog

Deferred by decision, not by omission. The project closed at **78 of 79 ledger
tasks passing** (`spec.json`).

> **Before publishing a firmware build anywhere, read item 3.** `secrets.h`
> values are compiled into `firmware.bin` as plaintext — including the Wi-Fi
> password — and a stranger's flash cannot work regardless. That item is the
> top priority if this project is ever resumed.

## 1. Task-hub rows — `POST /v1/rows` (spec.json task 78)

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

## 3. Onboarding — and the reason the firmware cannot be published as-is

**This is now the top item.** It is not polish; it blocks distribution and it
leaks credentials.

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

### The shape of the fix, in the order it must be built

Felipe's instinct is to put Wi-Fi under Settings in the web UI. That is right
for **changing** networks and wrong for **bootstrapping**, and the distinction
is the whole design: the web UI is served by the Mac, so a device that has
never joined a network cannot reach it to be told how to join one. The device
must serve its own first screen.

1. **Device-hosted captive portal (bootstrap).** With no stored credentials the
   device boots as its own access point. You join it from a phone, pick your
   network from a scan, enter the password, and give it the agent's address.
   Store both in NVS. This is the only step that cannot live on the Mac.
2. **Pairing code.** Once on the LAN the device shows a short code; you type it
   into the dashboard and the server hands back a device token it stores in NVS.
   That removes the token from the build.
3. **Wi-Fi under Settings (Felipe's request), for the everyday case.** Once the
   device is reachable, changing networks belongs in the dashboard like every
   other setting. Push new credentials to the device and have it store them and
   re-join — with the portal as the fallback when the new network fails, or the
   only recovery is a USB reflash.

The end state: `secrets.h` disappears from the user's path entirely, the binary
carries no credentials, and a published build is safe to hand to a stranger.

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
