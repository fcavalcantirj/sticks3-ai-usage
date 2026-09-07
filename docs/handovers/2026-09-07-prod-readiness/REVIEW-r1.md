---
verdict: APPROVED
round: 1
---

# Review r1 — prod readiness

**APPROVED.** All eight checklist items satisfied, and the plan found three
things I did not know when I wrote the handover.

## Checklist verdicts

1. **Items 1–5 in order, fix/remove/defer each** — satisfied. 1 fix, 2 fix,
   3 remove, 4 fix, 5 remove, 9 delete; 6–8 deferred with a stated reason rather
   than quietly dropped.
2. **Live verification named per item** — satisfied. Every step carries a
   *Verify (live)* line with the command and the expected output, and not one
   of them is "a test passes". Step 3's `pair-open-btn` count going 3 → 0 and
   Step 5's `POST /v1/device/netcfg` going 200 → 404 are exactly the shape I
   wanted.
3. **Every kept control backed end to end** — satisfied.
4. **No second spelling** — satisfied, and this is the item the plan is
   strongest on. It found the duplicated models-list builder
   (`scan.go:340-395` and `:660-715`) and correctly names it as *the mechanism
   by which #8 comes back*; it kills the page's duplicate 80/95 constants by
   rendering `p.severity` instead of teaching the page to fetch thresholds; and
   it caught `config.example.yaml`'s stale `~/.config/usaged` line unprompted.
   Two of today's nine bugs were a path spelled twice, so this matters more
   than it looks.
5. **No credential logged, rendered, returned or committed** — satisfied, and
   it net-reduces exposure by deleting the one path where a token-less loopback
   caller could read back a staged Wi-Fi password.
6. **`codesign` after any copy** — satisfied, in the standing verification lane.
7. **Nothing released or flashed without Felipe** — satisfied. The firmware
   edit is built and host-tested only, flash held as a separate ask.
8. **Claims labelled with evidence** — satisfied; the report format is fixed up
   front.

## What the plan caught that I had wrong or did not know

Recording these because they change the work, not just the wording:

- **`handleSetConfig` never assigns the alerts back to `s.cfg`**
  (server.go:682-695). My handover said the Alerts inputs "save and persist" and
  only the *reading* was missing. The card was inert one layer earlier than I
  described. Correction accepted.
- **`snapshot.Row.K` already carries `"5h"` / `"7d"`.** That is what makes
  Felipe's two-threshold answer implementable with no wire change, and I did not
  know it when I wrote the handover.
- **Filtering disabled providers at the API layer would break the device.**
  `pollOnce` rebuilds from `s.Fetchers` and `Snapshot.Apply` recomputes `rev`, so
  a filter in `handleUsage` would serve a body that no longer matches its own
  `rev` and the stick's change detection would silently rot. This is the single
  best call in the plan and it is why Step 1 belongs in `buildFetchers`.

## Answers to the builder's questions

1. **Keep the paired-devices readout — relocate it, don't delete it.** Do the
   version you proposed: keep `GET /v1/pair`, drop only the window controls and
   their false promise, and move the `#pair-devices` line into the BLE setup
   card. Felipe's "remove the card" was aimed at controls that cannot work; that
   line is live data from the one setup flow that does. You were right not to
   assume it — and right about the answer.

2. **Fold the `recover` into this round; defer the other two.** A panic in
   `runScan` kills the daemon exactly as the one in `runProvision` did before it
   was guarded today, and the guard is a handful of lines mirroring code that
   already exists. Leaving its sibling unguarded for a round is not a defensible
   asymmetry. The BLE keychain hang (#2) needs hardware and a deliberate deny,
   and the v0.1.1 token rotation (#14) realistically affects one machine — both
   are correctly round 2.

3. **The M5Burner onboarding text is Felipe's**, still open, and not yours to
   settle. Leave it out of this round and carry it forward.

## On your concerns

- **[HIGH] the 70/60 behaviour change reaching the stick** — correct to flag,
  and it is approved: Felipe specified those numbers himself and confirmed the
  device would warn sooner. Say it plainly in the completion report so it is not
  a surprise on the desk.
- **[MEDIUM] `SetFetchers` racing `pollOnce`** — your fallback is the right one.
  If `-race` complains, ship "the toggle takes effect on the next restart" and
  say so, rather than a racy setter. Do not paper over it.
- **[MEDIUM] `/v1/stats` shape change** — agreed, additive and self-healing.
  Your reasoning that the firmware never consumes `/v1/stats` is right; note in
  passing that it does not really consume `/v1/pair/claim` either, since
  `pair.cpp` is linker-discarded.
- **[LOW] proving `pair.cpp` deletion is a no-op via `firmware.bin` size and
  `OTA_BUILD_ID`** — do exactly that. That ID is what caught a stale-worktree
  build of the wrong commit once already.

## Closing

Builder is now the primary session.

First move, per the handover's `next_action`: **Step 1 — make `buildFetchers`
honour `EffectiveProviders().Enabled`**, verified by disabling a provider on the
running launchd daemon and watching it leave `GET /v1/usage` with a changed
`rev`. Ask before writing Felipe's real config file, and restore it after.
