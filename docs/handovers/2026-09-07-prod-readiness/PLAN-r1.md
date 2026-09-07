---
round: 1
builder_session: fresh session, took over the prod-readiness handover; verified the audit's live claims and planned items 1-5 plus the codex-cli deletion
---

# Plan r1 — make the dashboard stop lying

The product's happy path works end to end on real hardware. What is not ready is
everything around it: an audit found 20 confirmed findings, and the cards at the
top of the dashboard render controls wired to nothing. This plan covers items
1–5 of the handover's ordered list, plus item 9, on which Felipe has now ruled.

Felipe answered the handover's open questions before this plan was written:

- **Pair card → remove** ("same call as the Device Wi-Fi card, same reason. Keep
  pairing.go — BLE provisioning mints tokens through it. And fix the captive
  portal copy that makes the same false promise.")
- **netcfg → remove the endpoint group.**
- **Alerts → the shape changes.** Not one `quota_warn_pct` but two:
  `quota_warn_5h_pct: 70` and `quota_warn_weekly_pct: 60`. "Weekly warns earlier
  than the 5-hour window, which is right — a weekly limit you can't recover from
  in an afternoon deserves more notice." Today's behaviour therefore does change
  (95 → 70/60) and the stick and status line will warn sooner. Intended.
- **`codex_source=cli` → delete the CLI source.**

## Blocking constraints, restated

1. **A green test proves nothing across a process boundary.** Every one of the
   nine bugs shipped today was faked in tests: a `FakeKeyStore`, an `httptest`
   server, a grep over `index.html` that never parsed the JS. So "done" means
   the real binary ran the real path and I read the real output — the live
   daemon's `/v1/usage`, the launchd log, the wire. Not `go test`.
2. **Never hand-start the daemon on 8765.** Hand-starting steals the port from
   launchd, so nothing survives a reboot and every later log conclusion is
   worthless. The only lane is `make build` →
   `launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.ai-usage` → confirm with
   `lsof -nP -iTCP:8765 -sTCP:LISTEN` that the PID is launchd's.
3. **`make build` output is unsigned.** Any `cp` onto `~/.local/bin/ai-usage`
   must be followed by `codesign --force --sign - ~/.local/bin/ai-usage`, or the
   daemon is killed with `OS_REASON_CODESIGNING`.
4. **No credential value is ever logged, rendered, returned or committed.**
   Lengths and `set`/`unset` only.
5. **Every device upload needs Felipe's explicit authorization for that upload.**
   He is using the stick. Building firmware is free; flashing is not.
6. **Never edit `.env` or `firmware/include/secrets.h`.** `secrets.h` holds the
   only copy of the board's OTA password.

## Handover discrepancies

**None material.** What I checked against the running daemon and the code, and
what came back:

| claim | result |
|---|---|
| #20 enabled toggle inert | **Confirmed.** `EffectiveProviders`/`ProviderConfigs` have no hit in `cmd/usaged/once.go`; the only `Enabled` in `buildFetchers` is the unrelated `cfg.GroqProbeEnabled()` (once.go:166). |
| #10 alerts inert | **Confirmed, and worse as the audit said.** `AlertQuotaWarnPct`/`AlertOpenRouterLowUSD` appear only in config parse/serialize and the `GET /v1/config` echo. `handleSetConfig` (server.go:682-695) assigns interval, listen, TZ and `ProviderConfigs` back to `s.cfg` — **never the alerts**. Live daemon returns `{'alerts': {'openrouter_low_usd': 1, 'quota_warn_pct': 95}}`. |
| #9 pair card dead | **Confirmed.** `grep -c 'hal/sticks3/pair.h' firmware/src/main.cpp` = 0; `nm -C firmware.elf \| grep -c 'sticks3::pair'` = 0; live `GET /` returns `pair-open-btn` 3×; live `GET /v1/pair` = 200. |
| #8 models tab wrong | **Confirmed on the live daemon.** `claude-fable-5` renders `tok_today_col=1,968,442,641` and `cost_month_col=$3490.58`, while real today across all sources = 115,034,732 tokens and real month cost = $840.54. `monthTok: 0` is a literal at index.html:1577. |
| #3 netcfg dead both ends | **Confirmed.** 0 `netcfg` hits in `firmware/src`, 0 in `internal/web`; all four routes registered at netcfg.go:213-218 via server.go:119. I did **not** re-run the audit's mutating `POST` — it stages a phantom device report — and did not need to; auth.go:127 plainly waves every loopback method through. |
| daemon under launchd | **Confirmed.** `ai-usage` PID 61613 on `*:8765`, label `com.fcavalcanti.ai-usage`. |
| working tree clean at `5f73065` | **Confirmed.** |

**One correction to the handover's framing, not a defect:** it says AUDIT #10's
inputs "save and persist". On *this* machine they no longer persist either —
`~/.config/usaged/` does not exist, so `configPath` is `""`. Commit 57afdb3
fixed the rename half; the alerts half is still unassigned in `handleSetConfig`.

**Two facts the handover did not state that this plan depends on** — both checked
in the code, because they decide the design:

- **`snapshot.Row.K` already carries the window key**, literally `"5h"` and
  `"7d"` (claude.go:195-197, claude_statusline.go:121-128, codex.go:175-178).
  This is what makes Felipe's two-threshold answer implementable without a new
  field on the wire.
- **`pollOnce` rebuilds the provider list strictly from `s.Fetchers`**
  (scheduler.go:173-178) and then calls `Snapshot.Apply`, which recomputes rev
  and bumps seq. So dropping a fetcher genuinely removes the provider from the
  snapshot and changes the rev — exactly what spec task 46's own rule demands.
  This is why item 1's fix belongs in `buildFetchers` and **not** in a filter at
  the API layer: filtering in `handleUsage` would serve a body that no longer
  matches its own `rev`, and the device's change detection would break.

## Acceptance checklist — point-by-point

**1. Addresses items 1–5 in order, saying fix / remove / defer for each.**
Satisfied. In order: **1 fix**, **2 fix** (in the two-threshold shape Felipe
specified, not the single-knob shape the handover assumed), **3 remove**,
**4 fix**, **5 remove**. Item 9 (`codex_source=cli`) is **deleted**. Items 6, 7
and 8 of the handover list (BLE keychain hang, missing `recover` on `runScan`,
v0.1.1→v0.1.8 token rotation) are **deferred to a round 2** and named in
Concerns — they are hardening and upgrade-path work, not "the page lies" work,
and mixing them in makes this round unverifiable.

**2. Every item names how it is verified against the running system.**
Satisfied — each step carries a **Verify (live)** line naming the command and
the expected output. No step's evidence is "a test passes".

**3. Any control kept on the dashboard is backed end to end; anything not is
removed.** Satisfied. Afterwards every rendered control has a working consumer:
the Enabled checkbox changes what is polled, both alert inputs change what
warns, the Models columns match their headers. The pair window controls and the
netcfg endpoints are removed rather than left rendering.

**4. No second spelling of any path, name or constant.** Satisfied, and it is
the main design constraint on three of the five items:
- The per-model window aggregation is written **once** as a helper called by
  both `scanClaudeCode` and `scanCodex` (scan.go has two near-identical model
  builders today, at :340-395 and :660-715 — that duplication is exactly how #8
  would come back).
- The attention list stops re-deriving thresholds in JavaScript and renders
  `p.severity` from the snapshot, so 80/95 exist in **no** second place.
- The alert thresholds exist once, in `config`, threaded into the providers from
  the single `buildFetchers` call site.

**5. No credential value logged, rendered, returned or committed.** Satisfied,
and this round strictly reduces exposure: removing `POST /v1/device/netcfg`
deletes the one path where a token-less loopback caller could read back a staged
Wi-Fi password.

**6. `codesign --force --sign -` after any copy to `~/.local/bin/ai-usage`.**
Satisfied — constraint 3 above, repeated in the verification lane.

**7. Nothing released or flashed without Felipe saying so.** Satisfied. No
release, no tag, no M5Burner upload. The one firmware edit (the captive portal's
false pairing promise) is **built and host-tested only**; the flash is a
separate ask.

**8. Every claim in the final report labelled with evidence.** Satisfied — the
report format is fixed in the last section.

## The plan

Order matters: 1 and 2 both touch `handleSetConfig`, and 3 and 5 both delete
routes. Each step ends green and live-verified before the next starts.

### Step 1 — the Enabled toggle actually disables (AUDIT #20)

**Files:** `cmd/usaged/once.go`, `internal/sched/scheduler.go`,
`internal/api/server.go`, `cmd/usaged/serve.go`.

- `buildFetchers` walks `cfg.EffectiveProviders()` and skips any provider whose
  `Enabled` is false. Claude and Codex become conditional like the rest; the
  canonical order is already enforced downstream by `snapshot.Order`.
- Add `Scheduler.SetFetchers([]providers.Fetcher)`, guarded by `pollMu` — the
  same mutex `PollOnce`/`Refresh` already hold while reading `s.Fetchers`. This
  mirrors the existing `SetInterval` (scheduler.go:352), so there is one
  established shape for a runtime setting change, not two.
- The API cannot call `buildFetchers` (it lives in `package main`), so add an
  `api.WithFetcherBuilder(func(config.Config) []providers.Fetcher)` option,
  matching the existing `WithKeyStore`/`WithProvisioner` pattern at serve.go:112.
  `cmd/usaged` passes a closure over its keystore.
- `handleSetConfig`, after `s.cfg.ProviderConfigs = provs`, rebuilds the fetcher
  list and kicks a poll in the background (`go s.sched.Refresh()` — `Refresh`
  already coalesces via `TryLock`, and a synchronous poll would hold the PUT
  open for up to 20 s).

**Verify (live):** `PUT /v1/config` with `groq` `enabled:false` against the
launchd daemon, then within a few seconds
`curl -s 127.0.0.1:8765/v1/usage | grep -o '"id":"groq"'` returns **nothing**,
and `rev` has changed from the value captured before the PUT. Re-enable and
watch it come back. This writes Felipe's real config file, so I will ask before
running it and restore the prior value after.

### Step 2 — Alerts, in the two-threshold shape (AUDIT #10)

```yaml
alerts:
  quota_warn_5h_pct: 70
  quota_warn_weekly_pct: 60
  openrouter_low_usd: 1.00
```

**Files:** `internal/config/{config.go,serialize.go}`, `internal/format/format.go`,
`internal/providers/{claude,codex,groq,openrouter}.go`, `internal/api/server.go`,
`internal/web/index.html`, `config.example.yaml`.

- **Config:** `AlertQuotaWarnPct` becomes `AlertQuotaWarn5hPct` (default 70) and
  `AlertQuotaWarnWeeklyPct` (default 60). `validAlertKeys`, the YAML parser, the
  serializer, the `GET /v1/config` echo and the `PUT` range validation all move
  together. **Migration:** a legacy `quota_warn_pct:` in an existing file seeds
  *both* new keys so nobody's setting is silently dropped; the serializer only
  ever writes the new keys.
- **`handleSetConfig` assigns the alerts back to `s.cfg`** — the missing line
  that made the card inert even before persistence was considered.
- **`internal/format`:** add `type Alerts struct { Warn5hPct, WarnWeeklyPct int }`
  with a `WarnPctFor(k string) int` returning `WarnWeeklyPct` for `"7d"` and
  `Warn5hPct` otherwise. `Severity` takes an `Alerts`: auth/error → crit; any row
  ≥ 100 → crit; any row ≥ its own window's threshold → warn.
- **Threading:** the four `format.Severity` call sites are all inside provider
  constructors that `buildFetchers` builds in one place, so the value descends
  from a single origin. `NewOpenRouter` additionally takes the low-balance
  threshold, replacing the hardcoded `*balCents < 100` at openrouter.go:183 with
  `*balCents < format.Cents(lowUSD)`.
- **The page stops re-deriving thresholds.** `index.html:1720-1724` hardcodes
  `maxPct >= 80` / `>= 95`. It will render `p.severity` — already in the
  snapshot and now threshold-driven — keeping the pct only as display text. That
  deletes the duplicate constants rather than teaching the page to fetch them,
  which is what checklist item 4 is asking for.
- **Settings card:** one input becomes two (`setting-quota-warn-5h`,
  `setting-quota-warn-weekly`); `internal/web/web_test.go:130`'s pin follows.
- **`format.Tier`'s 50/80 row colours stay literal** and are not driven by these
  knobs. Tier is per-row cosmetics; Severity is the alert. Called out as an
  accepted residual so it is a decision, not an oversight.

**Behaviour change, intended:** today's effective warn is 95 for everything.
After this, a 5h window warns at 70 and a weekly window at 60 — so the status
line and the StickS3 will start warning noticeably sooner.

**Verify (live):** with real quota numbers on the running daemon,
`curl -s 127.0.0.1:8765/v1/usage | python3 -c` printing each provider's
`severity` beside its rows' `k`/`pct`, checked against 70/60 by hand. Then
`PUT /v1/config` moving `quota_warn_5h_pct` to 5, confirm the claude block flips
to `warn` on the next poll, and move it back. Plus `ai-usage` status line output
before and after.

### Step 3 — remove the "Pair a device" window (AUDIT #9, #4)

**Files:** `internal/web/index.html`, `internal/web/web_test.go`,
`internal/api/pairing.go`, `firmware/src/usage/portal.cpp`,
`firmware/src/hal/sticks3/pair.cpp`, `spec.json`.

- Remove from the page: the `pair-card` markup (:371-385), its CSS (:254-261),
  `openPairWindow`/`confirmPairCode` and the `/v1/pair/open` and
  `/v1/pair/confirm` fetches (:1080-1160), and the listeners at :1831-1833. Drop
  the `pair-card` / `pair-open-btn` pins at web_test.go:268-269.
- Remove the three window routes and `POST /v1/pair/claim` from
  `pairing.routes` (pairing.go:193-198).
- **Keep all of `pairing.go`'s device store, `matchToken`, and the mint/record
  function.** BLE provisioning gets its token through exactly that path
  (`TokenIssuerAware`, server.go:130) — deleting it would break the one setup
  flow that works.
- Delete `firmware/src/hal/sticks3/pair.cpp` and `pair.h`. They are already
  linker-discarded, so removing them cannot change the binary; I will prove that
  by comparing `firmware.bin` size and `OTA_BUILD_ID` before and after.
- Fix `portal.cpp:378-384`, whose default copy tells the owner the device "shows
  a pairing code on its own screen" — the same promise, in firmware.
- `spec.json` task 78 is already `passes:false`; annotate it as withdrawn rather
  than flipping anything to true.

**Verify (live):** rebuild + `launchctl kickstart`, then
`curl -s 127.0.0.1:8765/ | grep -c pair-open-btn` = **0** (it is 3 today) and
`curl -s -o /dev/null -w '%{http_code}' -X POST 127.0.0.1:8765/v1/pair/open` =
**404**. Firmware: `make fw-test` green and `make fw-build` succeeds, with the
portal's new copy confirmed by `strings firmware.bin`. **No flash** — separate ask.

### Step 4 — Models tab columns match their headers (AUDIT #8)

**Files:** `internal/stats/{model.go,scan.go}`, `internal/web/index.html`.

- `stats.Model` gains `TokensToday`, `TokensMonth` (both `Tokens`) and
  `CostMonth` (`float64`). The existing `Tokens`/`Cost` stay as the lifetime
  figures the CLI (`cmd/usaged/stats.go:123-135`) prints — additive, so no
  consumer breaks.
- The windowed values are accumulated in the same post-walk pass over `dayAgg`
  that already computes `src.Today`/`src.Month` (scan.go:316-338) — it already
  iterates `d.byModel` for cost, so the per-model split is nearly free.
- **`scanClaudeCode` and `scanCodex` each have their own copy of the models-list
  builder** (scan.go:340-395 and :660-715). Extract one `buildModelsList(...)`
  helper used by both. This is the single most important line in the plan for
  checklist item 4: leaving two copies is how this exact bug returns.
- Sort `src.Models` by month tokens descending, which is what the field's own
  doc comment at model.go:68 already claims ("sorted by month tokens desc") and
  what the headers imply. The page preserves the server's order instead of
  re-sorting on the lifetime figure.
- `index.html:1577` reads `m.tokens_today` / `m.tokens_month` / `m.cost_month`;
  the `monthTok: 0` literal dies.
- The `Requests` column stays lifetime — its header names no window, so it is
  not a lie. Noted, not changed.

**Verify (live):** rebuild + kickstart, then re-run the audit's own proof command
against `/v1/stats` and assert that the sum of every model's `tokens_today`
equals the payload's `sources.*.today.tokens` total (115,034,732 as of this
session's check), and that the month-cost column sums to `sources.*.month.cost`
($840.54), instead of today's $3490.58 for a single model.

### Step 5 — remove the netcfg endpoint group (AUDIT #3)

**Files:** `internal/api/{netcfg.go,netcfg_test.go,server.go,setup.go}`,
`spec.json`.

- Delete `netcfg.go` and `netcfg_test.go`; drop `srv.netcfg` and the
  `srv.netcfg.routes(mux)` call at server.go:118-119.
- **`netcfgClean` must survive** — `setup.go:933` calls it. It moves to
  `setup.go` under its current name (renaming it would be a second spelling of a
  helper that already has callers). `sanitizePairID` lives in `pairing.go` and is
  untouched.
- Annotate spec task 79's netcfg criteria as withdrawn.

**Verify (live):** rebuild + kickstart, then
`curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{"device_id":"x"}' 127.0.0.1:8765/v1/device/netcfg`
= **404** (it is 200 today), and the same for `GET /v1/netcfg`.

### Step 6 — delete the dead `codex_source=cli` path (AUDIT #1)

**Files:** `internal/providers/codex_cli.go` (+ its fixture runner and tests),
`cmd/usaged/once.go`, `internal/config/config.go`, `spec.json`.

- Remove `codex_cli.go`, `NewCodexCLI`, `NewCodexFixtureRunner`,
  `realCodexCLIRunner`, and the `case "cli":` branch in `buildFetchers`
  (once.go:126-137). `USAGED_CODEX_SOURCE` keeps working for any remaining
  values; if `http` becomes the only one, the variable goes too.
- Flip spec task 79's codex-cli criteria to `passes:false` with a note that the
  path was removed rather than fixed — the handover is explicit that a task whose
  verify step names Felipe does not flip to true on my say-so.

**Verify (live):** `grep -rn CodexCLI --include='*.go' .` returns nothing, and
after rebuild + kickstart `curl -s 127.0.0.1:8765/v1/usage | grep -o '"id":"codex"[^,]*'`
still shows the healthy default http source (`"status":"ok"`).

### Verification lane (applies to every step)

The same commands after every Go change, in this order — the lane the handover
says produced every real finding:

```sh
make verify                                        # fmt vet lint test build (13/13 today)
cp bin/ai-usage ~/.local/bin/ai-usage
codesign --force --sign - ~/.local/bin/ai-usage    # UNSIGNED otherwise → OS_REASON_CODESIGNING
launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.ai-usage
lsof -nP -iTCP:8765 -sTCP:LISTEN                   # PID must be launchd's, never a hand-start
```

Firmware changes: `make fw-test` (359 tests today) and `make fw-build`, plus
`make fw-publish-check` must still say PUBLISHABLE. **No flash without a
separate, explicit authorization from Felipe for that upload.**

## Concerns

- **[HIGH] Step 2 changes what the StickS3 and the status line show, with no
  config edit by anyone.** Warning moves from 95 to 70/60, so blocks that read
  green today will read amber. Felipe explicitly confirmed this is intended, but
  it is the one change in this round a user would notice without asking for it,
  and it lands on the physical device.
- **[MEDIUM] Step 1's live rebuild path is genuinely new surface.**
  `SetFetchers` introduces the first runtime mutation of `s.Fetchers`, which
  `pollOnce` reads without holding `mu`. I am guarding with `pollMu` because
  that is the mutex the readers are already under, and I will run
  `go test -race ./internal/sched ./internal/api` specifically for this. If the
  race detector is unhappy I will fall back to "the toggle takes effect on the
  next daemon restart" and say so plainly rather than ship a racy setter.
- **[MEDIUM] The handover's items 6, 7 and 8 are deferred, so three known
  defects survive this round**: the BLE keychain dialog that provisions a WPA
  network as open after a 45 s hang (#2), the missing `recover` on `runScan`
  (#18/#19), and the v0.1.1→v0.1.8 device-token rotation (#14). None of them
  makes the page lie, which is this round's stated goal, and #2 in particular
  needs hardware and a deliberate deny to verify. I would take them as round 2.
- **[MEDIUM] Step 4 changes the `/v1/stats` payload shape.** The additions are
  purely additive and the firmware does not consume `/v1/stats` (it speaks
  `/v1/usage`, `/v1/refresh`, `/v1/pair/claim`), so I believe nothing breaks —
  but `stats.json` on disk is written by the scheduler and read back at startup,
  so an old file will have zero-valued new fields until the next scan. Cosmetic
  and self-healing; called out so it is not a surprise.
- **[LOW] Deleting `pair.cpp` cannot change the shipped binary** (it is already
  in "Discarded input sections"), but I will still diff `firmware.bin` size and
  `OTA_BUILD_ID` before and after, because the handover records a stale-worktree
  build of the wrong commit caught only by that ID.
- **[LOW] `config.example.yaml`'s header still says "Copy to
  ~/.config/usaged/config.yaml"**, contradicting the directory the installer
  drops it into (AUDIT #12, largely fixed by 57afdb3). I will fix that one line
  while editing the file for the new alert keys, since leaving it is precisely
  the "second spelling" the checklist forbids.

## Questions for the author

1. **The paired-devices readout.** Removing the pair card also removes
   `#pair-devices` (index.html:1166), the only place the dashboard lists which
   sticks are paired — which BLE setup populates and which is genuinely useful.
   The plan as written deletes it with the card. Would you rather I **keep
   `GET /v1/pair` and relocate that one line into the BLE setup card**, removing
   only the window controls and their false promise? I did not assume this,
   because "remove the card" is what Felipe said.
2. **Items 6–8 as a round 2** — is deferring the BLE keychain hang, the missing
   `runScan` recover, and the upgrade token rotation to a second pass the right
   call, or should the `recover` (a genuinely small, safe change) fold into this
   round?
3. **The onboarding text for M5Burner** is still open from this morning
   (handover open question 3) and is untouched by this plan. Listing, dashboard,
   or both?

## Report format on completion

Every claim labelled, with its evidence inline:

- `[REAL]` — ran against the launchd daemon or the hardware, with the command and
  its actual output quoted.
- `[TEST]` — passed in `make verify` / `make fw-test` only. Never sufficient on
  its own for anything crossing a process boundary.
- `[UNVERIFIED]` — reasoned but not executed, and said so.
