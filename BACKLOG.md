# Backlog

Deferred by decision, not by omission. The project closed at **78 of 79 ledger
tasks passing** (`spec.json`); the one open task is the first item below.

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

## 3. Onboarding without touching files

Today a new device needs `secrets.h` at build time and a device token pasted
into the dashboard. The intended flow:

1. A device with no stored credentials boots as its own access point with a
   captive portal; you pick your network from a scan and enter the password.
2. It joins the LAN and shows a short pairing code.
3. You type that code into the dashboard; the server hands the device a token
   it stores in NVS.

**Wi-Fi provisioning must come first** — a pairing code cannot help a device
that is not on the network yet, which is the ordering mistake to avoid.

Design notes gathered: the ESP32-S3 is **2.4 GHz only**, so a 5 GHz-only or
band-steering SSID simply will not appear and the portal must say why; hidden
SSIDs need manual entry; and the device should fall back to its own AP after
repeatedly failing to join, or the only recovery is a USB reflash — the very
thing this removes.

A hosted (non-local) pairing page needs a relay and is a much larger build.
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
