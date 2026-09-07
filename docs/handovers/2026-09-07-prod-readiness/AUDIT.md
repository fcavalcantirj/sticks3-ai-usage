# Audit findings — 2026-09-07

Adversarially verified: 27 candidates, **20 confirmed**.
Each was re-proved by running its own `how_to_prove` before being kept.

Two are already fixed (keychain Set, settings persistence) — see git log.


## 1. codex_source=cli is dead: the real `codex app-server` invocation closes stdin, the process exits in ~58 ms before answering, and every poll returns "parse error"

- **Severity:** BROKEN (confirmed, but scoped): the cli source is 100% non-functional for anyone who selects it and spec.json task 79 marks it `passes: true` on criteria that never exec the real binary — however it is an `optional`-category, undocumented, opt-in alternate source reachable only via the USAGED_CODEX_SOURCE env var (not config.yaml, not README, not config.example.yaml), it fails safe to an error badge rather than wrong numbers, and the default http path is healthy, so no current user impact.
- **Location:** `internal/providers/codex_cli.go:35`
- **User impact:** Anyone who sets `codex_source: cli` in ~/.config/usaged/config.yaml or `USAGED_CODEX_SOURCE=cli` gets the ChatGPT/Codex card stuck on "error / parse error" forever — no 5h or 7d gauge on the dashboard, the status line, or the StickS3. Felipe is on the default `http` source today (no config.yaml exists on this Mac, and GET /v1/config shows codex ok), so nothing is broken right now; the alternative source that spec.json marks `passes: true` simply does not work.
- **Why it survived refutation:** Reproduced end to end through the daemon's own binary — `USAGED_CODEX_SOURCE=cli /tmp/usaged-probe once --json` returned `('codex','error','parse error')` 4/4 while the default http source returned `('codex','ok','')`; the raw invocation exits in ~75 ms with zero `"id":1` lines across 5 runs, and feeding the identical bytes while holding stdin open yields `hold=0.5s id1=0 / hold=2s id1=1 / hold=5s id1=1`, proving the app-server shuts down on stdin EOF ~1-2 s before it can answer. Refutation failed on every angle: `Fetch` (codex_cli.go:228) has no fallback, `serve.go:57` uses the same `buildFetchers` as `once.go:60`, and `realCodexCLIRunner` is constructed by no test at all (not even the USAGED_LIVE lane) — the only evidence error is that `codex_source: cli` in config.yaml does NOT reach it (FileConfig in internal/config/yaml.go:12-19 has no such key), so only `USAGED_CODEX_SOURCE=cli` selects the dead path.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && printf '%s\n%s\n' '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"usaged","version":"1.0"}}}' '{"jsonrpc":"2.0","id":1,"method":"account/rateLimits/read"}' > /tmp/in.txt && for i in 1 2 3; do codex -s read-only -a never app-server --listen 'stdio://' < /tmp/in.txt 2>/dev/null | grep -c '"id":1'; done   # prints 0 0 0
# then the same bytes with stdin held open:
{ cat /tmp/in.txt; perl -e 'select(undef,undef,undef,3)'; } | codex -s read-only -a never app-server --listen 'stdio://' 2>/dev/null | grep -c '"id":1'   # prints 1
# and end to end:
go build -o /tmp/usaged-probe ./cmd/usaged && USAGED_CODEX_SOURCE=cli USAGED_STATE=/tmp/st.json /tmp/usaged-probe once --json | python3 -c "import sys,json;print([(p['id'],p['status'],p.get('msg')) for p in json.load(sys.stdin)['providers'] if p['id']=='codex'])"
```

## 2. internal/bleprov/creds_darwin.go runs four external commands with ZERO tests of any kind — and the keychain read blocks on a GUI dialog, after which Gather ships the WPA network to the device as "open"

- **Severity:** medium — reproducible defect on the primary zero-config path with zero test coverage of five shell-outs, but it fails visibly (firmware reports JoinFailed, bleprov.cpp:435) and leaks no credential; the harm is a 45 s hang plus a misdiagnosed join failure whose real cause exists only in the launchd log, compounded by a false coverage claim in the file header
- **Location:** `internal/bleprov/creds_darwin.go:291`
- **User impact:** One-click BLE setup, run while nobody is watching the Mac: the dashboard sits for 45 seconds, then provisions the stick with the right SSID and NO password. The device attempts an open join against a WPA access point and fails; the only trace of the real cause is a Warn line in the daemon log. If the owner is at the keyboard and clicks Allow, it works — which is why this has never been noticed.
- **Why it survived refutation:** Confirmed on all counts: `go tool cover -func` reports 0.0% for Gather and for every function in creds_darwin.go (parseWiFiDevice, CurrentSSID, Password, classifyKeychainError, ...) with the package at 23.4%, so creds.go:19-22's "the same FakeRunner ... tests this too" describes a test that does not exist; and the keychain command reproduced exactly — SecurityAgent absent at baseline, then PID 45381 up with `security` (45379) blocked in state SN, SIGALRM at 15 s giving exit 142, 0 bytes stdout, empty stderr. The downstream is code, not inference: creds.go:376-381 leaves Password "" and Open false, `.Open` has NO non-test reader in the BLE path (only a log field at creds.go:176 and the unrelated api/netcfg.go), cmd/usaged/bleprov.go:182 copies the empty password into the Record gating only on Host, wire.go:207-212 accepts it, and firmware provision.cpp:104-108 states "Empty means an OPEN network and is legal". Every refutation failed: the path is wired (serve.go:113-114 under bleSupported; live daemon returns "available":true, which setup.go:710 defines as s.prov != nil), Warnings reach only p.logger.Warn at bleprov.go:173 and never an API response, and the Deny branch is identical to the timeout and likelier — a System.keychain read raises an admin-auth prompt every time with no per-binary caching, so ErrKeychainDenied lands in the same default: arm.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -rn "Gatherer\|Gather(" --include="*_test.go" .   # no output: nothing tests it
perl -e 'alarm 20; exec @ARGV' security find-generic-password -w -D "AirPort network password" -s "AirPort" -a "$(networksetup -listpreferredwirelessnetworks en0 | sed -n '2p' | tr -d '\t')" >/tmp/pw.out 2>/tmp/pw.err & sleep 4; pgrep -lf SecurityAgent; ps -o pid,stat,etime,command -p $!   # dialog is up, security is blocked; wc -c /tmp/pw.out is 0
# and read wire.go:207-212 to see the empty password accepted as an open network
```

## 3. POST /v1/device/netcfg is live on the running daemon and has no client at either end — zero firmware code and zero dashboard code

- **Severity:** BROKEN
- **Location:** `internal/api/netcfg.go:217`
- **User impact:** The whole "change the device's Wi-Fi from the agent" feature can never fire. A staged change would sit pending until it expires (netcfgChangeTTL = 2h) because nothing on the device ever polls for it. There is also no way to reach it from the UI, so today it is unreachable from both directions. The endpoint accepts an unauthenticated loopback POST and will happily record a phantom device report that the dashboard's status view would then present as the device's real network state.
- **Why it survived refutation:** Every factual assertion reproduces on the live daemon: the dashboard's full fetch list (15 paths in internal/web/index.html) contains no /v1/netcfg, firmware speaks only /v1/usage, /v1/refresh and /v1/pair/claim, and an unauthenticated loopback POST returned 200 and made my phantom "aabbccddeeff" the daemon's device report — the only other netcfg references in the repo are a shared helper (netcfgClean in setup.go) and prose comments, not callers. It is worse than claimed: netcfg.go:434-436 asserts the password only leaves "to a caller that presented a device token," but commit ad8603b broadened auth.go's loopback exemption to all methods, and I confirmed live that a token-less loopback caller can PUT /v1/netcfg with password "canary-password-123" and read it back verbatim from POST /v1/device/netcfg (staged change then cancelled; netcfg never persists).

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -rn netcfg firmware/src | wc -l  # 0
grep -rn netcfg internal/web | wc -l  # 0
curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{"device_id":"aabbccddeeff"}' http://127.0.0.1:8765/v1/device/netcfg  # 200, from a caller that is not a device
```

## 4. POST /v1/pair/claim: the firmware client exists (400 lines) but is never included by main.cpp, so the linker throws it out of the shipped binary — while the dashboard still renders a working "Open pairing window" button

- **Severity:** BROKEN — the "Open pairing window" button and its on-screen instructions are a genuine dead end (no firmware code can ever claim the window), though impact is bounded below the claim's framing: the BLE path is wired into main.cpp, works, and has already paired the live device (GET /v1/pair shows peer:"ble"), so the device is not unprovisionable, and spec.json task 78 is honestly passes:false. The defect is that the route and UI card shipped ahead of the firmware half.
- **Location:** `firmware/src/hal/sticks3/pair.cpp:20`
- **User impact:** Felipe clicks "Open pairing window" on the dashboard, the agent opens a real window, and the page tells him "No pairing window is open. Open one, then power on the device" / "Waiting for the device to collect its token" — advice that can never complete, because the device firmware in flash contains no pairing code at all. The window silently expires. 809 lines of pairing.go plus 27 test functions in pairing_test.go all pass against httptest. (spec.json task 78 is honestly passes:false, but the route and the UI card ship regardless.)
- **Why it survived refutation:** Confirmed and could not be refuted: `strings firmware.bin | grep -c '/v1/pair/claim'` is 0 (control `/v1/usage` is 2) and `nm -C firmware.elf | grep -c 'sticks3::pair'` is 0 with pair.cpp.o's sections all at 0x0 under "Discarded input sections", while `internal/web/index.html:371` ships the pair card unconditionally (pinned by a test at web_test.go:268, and served live: `curl 127.0.0.1:8765/ | grep -c pair-open-btn` → 3). The strongest refutation — that BLE provisioning consumes the same window — is denied by the code itself at internal/api/pairing.go:761-766: "It deliberately does NOT open, consume or close the pairing window ... there is no request to answer and no /v1/pair/claim the device could reach."

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -c 'hal/sticks3/pair.h' firmware/src/main.cpp  # 0
~/.platformio/packages/toolchain-xtensa-esp-elf/bin/xtensa-esp32s3-elf-nm -C firmware/.pio/build/m5stack-sticks3/firmware.elf | grep -c 'sticks3::pair'  # 0
awk 'NR>2357 && /pair\.cpp\.o/' firmware/.pio/build/m5stack-sticks3/firmware.map | head  # its sections, all at address 0x0
```

## 5. hal/sticks3/discover.cpp (mDNS agent discovery) is linker-discarded, so the ~1200-line mDNS responder the daemon runs in production has no in-repo consumer

- **Severity:** info — accurate observation, not a defect: documented intentional WIP for open spec tasks 76-78, no user-reachable path and no impact (the one component that could depend on it, cmd/usaged/bleprov.go, explicitly compensates)
- **Location:** `firmware/src/hal/sticks3/discover.cpp:135`
- **User impact:** None observable today — the device gets an explicit host:port in its provisioning record over BLE or the captive portal, so it does not need discovery. But the whole mDNS responder runs on every daemon start, and the only client written for it cannot run. Nothing in this repo has ever exercised the device→agent discovery path end to end; the 39 mDNS tests all talk to an in-process responder.
- **Why it survived refutation:** The linkage facts are exactly right and I could not refute them — `xtensa-esp32s3-elf-nm -C firmware/.pio/build/m5stack-sticks3/firmware.elf | grep -c 'sticks3::discover'` is 0, every `discover.cpp.o` section in firmware.map lies inside the "Discarded input sections" block (line 2357 through "Memory Configuration" at 52702), and a widened grep for every identifier in discover.h (including discoverState/discoverFound/discoverReset/DiscoverState/kDiscover, which the reporter's grep missed) finds no consumer anywhere in firmware/src or firmware/test. But it is not a defect: cmd/usaged/bleprov.go:15-24 already documents this exact fact verbatim ("hal/sticks3/discover.cpp is NOT REFERENCED by main.cpp, so on the firmware that exists today it is compiled and then discarded by the linker") and deliberately always sends a real host so the empty-Host="discover over mDNS" branch is never taken; commit c318338 that added the file says "Not yet wired into main.cpp ... committing the green state as a checkpoint", spec.json tasks 76/77/78 (the whole provisioning group) are passes:false, and hal/sticks3/pair.cpp is in the identical discarded state — so this is documented, intentional staging for open work, with zero user-reachable path. (Minor misread: internal/mdns/live_test.go:27 TestLiveResponder, gated on USAGED_LIVE=1, does verify the responder against macOS's own dns-sd resolver, so the Go half is not only tested in-process.)

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -c 'hal/sticks3/discover.h' firmware/src/main.cpp  # 0
~/.platformio/packages/toolchain-xtensa-esp-elf/bin/xtensa-esp32s3-elf-nm -C firmware/.pio/build/m5stack-sticks3/firmware.elf | grep -c 'sticks3::discover'  # 0
dns-sd -B _ai-usage._tcp local   # the Go side answers; nothing on the device asks
```

## 6. usage::provision::Machine::onPortalSaved and Machine::reset are host-tested but absent from the flashed image — the same shape as the already-found "retry->portal edge main.cpp never called"

- **Severity:** COSMETIC
- **Location:** `firmware/src/usage/provision.h:157`
- **User impact:** None on the wire — the reboot re-enters onBoot(), which reaches the same state. But four host assertions cover a transition the device provably never takes, so a regression in onPortalSaved would go on passing forever, and anyone reading provision.h will believe main.cpp drives the machine through the portal edge when it does not.
- **Why it survived refutation:** Reproduced exactly: `nm -C firmware.elf | grep 'Machine::'` yields only Machine::Machine, onBoot, onJoinResult, onConnectionLost — onPortalSaved and reset are linker-eliminated — and `grep -rn 'onPortalSaved' firmware/src` finds only the definition (provision.cpp:170) and declaration (provision.h:157), with both provisioning paths calling ESP.restart() instead (main.cpp:951-959, 990-1002); the ELF is current (its firmware.bin contains the just-added "[NET] joins keep failing" string from 02d5dc8), so this is not a stale build, and onRetryElapsed is dead in the same way, which the claim missed. Refutation failed on every angle, but impact is nil: portal.cpp:684 only sets PortalOutcome::Joined from Phase::Success, so the record is definitionally usable and the reboot's onBoot(true) reaches the identical state.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -rn 'onPortalSaved' firmware/src/main.cpp   # no output
~/.platformio/packages/toolchain-xtensa-esp-elf/bin/xtensa-esp32s3-elf-nm -C firmware/.pio/build/m5stack-sticks3/firmware.elf | grep -E 'Machine::(onBoot|onJoinResult|onPortalSaved|reset)'
```

## 7. Two more declared-and-defined firmware functions with no caller: sticks3::fetchConfigured (the guard against fetching before configure) and sticks3::emitSleep

- **Severity:** COSMETIC
- **Location:** `firmware/src/hal/sticks3/fetch.h:23`
- **User impact:** None today. Both provisioning paths call ESP.restart() (main.cpp:959, main.cpp:1001) so a fetch before fetchConfigure cannot currently happen — the guard is written for a hazard nothing reaches. If either restart is ever replaced with a live mode switch, the guard exists but is not wired, and doFetch would POST to port 0 with an empty X-Device-Token.
- **Why it survived refutation:** Repo-wide grep (not just firmware/src, tests included) returns only the declaration and definition for each: fetchConfigured at fetch.h:23 / fetch.cpp:56 and emitSleep at power.h:66 / power.cpp:195, with no '##' token-pasting anywhere in firmware/src that could hide a generated caller; nm on firmware.elf lists sticks3::fetchConfigure and sticks3::emitWake (both genuinely called, at main.cpp:873 and main.cpp:721 — the control that validates the method) but neither fetchConfigured nor emitSleep, and emitSleep's [SLEEP] output is instead produced inline inside powerSleep() at power.cpp:157-159, so nothing is lost.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && grep -rn 'fetchConfigured\|emitSleep' firmware/src --include='*.cpp' | grep -v 'fetch.cpp:56\|power.cpp:195'   # no call sites
~/.platformio/packages/toolchain-xtensa-esp-elf/bin/xtensa-esp32s3-elf-nm -C firmware/.pio/build/m5stack-sticks3/firmware.elf | grep -E 'fetchConfigured|emitSleep'   # absent
```

## 8. Models tab: "Tokens month" is hardcoded to 0, "Tokens today" is all-time, "Est. cost month" is all-time cost — three of five column headers are lies

- **Severity:** BROKEN
- **Location:** `/Users/fcavalcanti/dev/m5/sticks3-ai-usage/internal/web/index.html:331 (header) and :1577 (row builder)`
- **User impact:** The Models tab is the only per-model breakdown in the product. One column is permanently zero; the other three are ~25x too large because they are lifetime figures under "today"/"month" headers. Anyone reading it gets a wrong answer to "what did I spend this month on Opus".
- **Why it survived refutation:** Confirmed on the served page (curl of / returns the same lines 331 and 1577): `monthTok: 0` is a literal and fmtTokens(0) returns "0", so every "Tokens month" cell renders 0; `m.tokens`/`m.cost` come from stats.Model built at scan.go:347 as `Tokens: modelAgg[model]`, the whole-scan-window aggregate, never day- or month-filtered (only src.Today/src.Month are windowed, in the loop at scan.go:316-338). Live daemon now: claude-fable-5 renders tok_today_col=1,968,442,641 and cost_month_col=$3490.58 while REAL today across ALL sources = 114,726,557 tokens and REAL month cost across ALL sources = $874.14 — and that same lifetime number is the sort key, so the ranking is mislabeled too. No other row builder or override exists, the Models tab is user-reachable (button :309, panel :328), and the correctly windowed data is in the same payload (sources.*.today/month, days[].by_model) and used properly by the header pills at :1515-1516.

**Prove it:**
```sh
curl -s http://127.0.0.1:8765/v1/stats | python3 -c 'import json,sys;d=json.load(sys.stdin);rows=[(m["model"],sum(m["tokens"].values()),0,m["cost"]) for s in d["sources"].values() for m in s["models"]];rows.sort(key=lambda r:-r[1]);[print("%-20s tok_today_col=%d tok_month_col=%d cost_month_col=%.2f"%r) for r in rows[:4]];print("REAL today:",sum(sum(s["today"]["tokens"].values()) for s in d["sources"].values()));print("REAL month:",sum(sum(s["month"]["tokens"].values()) for s in d["sources"].values()));print("REAL month cost: %.2f"%sum(s["month"]["cost"] for s in d["sources"].values()))'
```

## 9. "Pair a device" card: the whole server half works and the firmware half is never called — the device can never show the code the card tells you to wait for

- **Severity:** BROKEN
- **Location:** `/Users/fcavalcanti/dev/m5/sticks3-ai-usage/internal/web/index.html:377 (button) and :1139-1150 (the promise text)`
- **User impact:** Clicking "Open pairing window" opens a real, live 3-minute window on the daemon and tells the owner to power on the device and wait for a code. No firmware build can ever POST /v1/pair/claim, so no code ever appears, the code field stays disabled, and the window silently expires. The card is indistinguishable from a working feature until you have waited out the timer.
- **Why it survived refutation:** Refutation failed on every angle: `grep -rn 'pairBegin|pairUpdate|pairState|pairCode' firmware/src firmware/test` hits only inside pair.h/pair.cpp itself, main.cpp's include block (lines 19-38) has no `hal/sticks3/pair.h` (its only "pair" string is a BLE-passkey comment at :891), there is no test_pair.cpp in firmware/test/host/, screen.h exports no pairing-code draw function, and the live daemon serves the card enabled (GET / returns the exact string "left. Power on the device and wait for its code to appear." and GET /v1/pair returns 200 with state/window_ttl_sec:180). It is in fact worse than reported: the captive portal's own default copy makes the same promise (firmware/src/usage/portal.cpp:378-384, "then shows a pairing code on its own screen. Type that code into the usaged dashboard"), so a portal-provisioned device also ends up with token_len=0 and no route to a token except the folded Advanced paste field.

**Prove it:**
```sh
grep -c 'pairBegin\|pairUpdate' firmware/src/main.cpp   # 0
grep -rn 'pairBegin\|pairUpdate' firmware/src firmware/test | grep -v 'src/hal/sticks3/pair'   # no output
ls firmware/test/host/    # no test_pair.cpp
python3 -c "import json;print(json.load(open('spec.json'))[77]['passes'])"   # False (task 78)
```

## 10. Alerts card: both inputs save and persist, and nothing in the daemon or firmware ever reads either value — all four thresholds are hardcoded elsewhere

- **Severity:** BROKEN
- **Location:** `/Users/fcavalcanti/dev/m5/sticks3-ai-usage/internal/web/index.html:346-355`
- **User impact:** Setting "Quota warn at 70" or "OpenRouter low balance 5.00" and clicking Save settings returns success, writes the value to the config file, and reloads it into the form on the next visit — while the Needs-attention tab, the row tiers and the OpenRouter warning keep using 80/95/100 and $1. The owner believes they have retuned their alerting and have not.
- **Why it survived refutation:** Could not refute: repo-wide grep shows AlertOpenRouterLowUSD/AlertQuotaWarnPct only at declaration, defaults, YAML load/write-back, GET /v1/config echo and tests — never read by a decision path, and the signatures rule it out (format.Tier(pct,status)/format.Severity(status,rows) take no threshold; NewOpenRouter(client,id,label,key) gets no Config), so the live thresholds stay the literals at format.go:52-57/69-77, openrouter.go:183 (`*balCents < 100`) and index.html:1722-1724 (80/95); the live daemon serves the card at index.html:346-355 and returns {"openrouter_low_usd":1,"quota_warn_pct":95}. It is in fact worse than claimed: the PUT handler applies interval/listen/tz/providers in memory but never assigns the alerts back to s.cfg (server.go:682-694), and since ~/.config/usaged/ does not exist s.configPath is "" (config.go:132-152), so saveConfigAtomic is skipped (server.go:672) and a Save returns ok:true while discarding both values entirely.

**Prove it:**
```sh
grep -rn 'AlertQuotaWarnPct\|AlertOpenRouterLowUSD' --include='*.go' /Users/fcavalcanti/dev/m5/sticks3-ai-usage   # only config parse/serialize + GET /v1/config; no consumer
sed -n '41,68p' internal/format/format.go        # hardcoded 50/80 and 95/100
sed -n '176,188p' internal/providers/openrouter.go  # hardcoded `*balCents < 100`
sed -n '1720,1726p' internal/web/index.html      # hardcoded 80/95 in the page
```

## 11. Activity heatmap is drawn as a GitHub-style calendar over an array that contains only ACTIVE days, so idle stretches vanish and the "0" legend swatch can never appear

- **Severity:** MINOR — cosmetic/spec-fidelity gap, not BROKEN. The layout does not deliver the 26-week x 7-day grid task 68 specified, but nothing misreports data: totals, active-days, peak and each cell's date/token tooltip are all correct, and there are no weekday or month axis labels (the `Mon/Wed/Fri` labels were never built and `.heat-month-label` at :272 matches no element) to actively sell it as a contribution calendar. Felipe already closed the UAT on this live rendering (spec.json:489).
- **Location:** `/Users/fcavalcanti/dev/m5/sticks3-ai-usage/internal/web/index.html:1601-1608 (week grouping) and :121 (52-column grid)`
- **User impact:** The Activity tab reads as a contribution calendar and is not one. Two weeks off look identical to two weeks of steady work, and the "0" swatch in the legend labels a colour the grid never draws.
- **Why it survived refutation:** Could not refute the core: `grep -n "grid-auto-flow\|grid-template-rows" internal/web/index.html` returns nothing, so `.heatmap { grid-template-columns: repeat(52,1fr) }` fills row-major over a flat cell string, and scan.go:365-390 appends only transcript-present days (live: claude_code 37 cells over a 47-day span = 10 missing; codex 17 over 50 = 33 missing), so idle stretches genuinely vanish and the weeks/26-padding loop is a provable no-op. But sub-claim (d) is REFUTED — `.heat-0` comes from `level < 0.01`, not `tok === 0`, and is drawn live for 2026-07-26 (claude_code) and 2026-08-09/08-17/08-30 (codex), so the legend's "0" swatch is not an unreachable colour.

**Prove it:**
```sh
curl -s http://127.0.0.1:8765/v1/stats | python3 -c 'import json,sys,datetime;d=json.load(sys.stdin)
for sn,s in d["sources"].items():
 ds=[x["date"] for x in s["days"]];span=(datetime.date.fromisoformat(ds[-1])-datetime.date.fromisoformat(ds[0])).days+1
 print(sn,"cells drawn=",len(ds),"calendar days spanned=",span,"missing=",span-len(ds))'
sed -n '376,398p' internal/stats/scan.go   # only days present in transcripts are appended
sed -n '1601,1608p' internal/web/index.html
```

## 12. CONFIG_DIR was renamed to ~/.config/ai-usage but the daemon still reads ~/.config/usaged/config.yaml — the config directory the installer creates is never read

- **Severity:** BROKEN
- **Location:** `scripts/install-release.sh:31 vs internal/config/config.go:74`
- **User impact:** A user who does the obvious thing — edit or create config.yaml in the directory the installer made and populated — gets a file that is silently ignored. interval_sec, listen, tz, alert thresholds and every provider toggle in it have no effect, with no warning in the log. Conversely a stale ~/.config/usaged/config.yaml left over from v0.1.1/v0.1.2 is still loaded by v0.1.8 and silently wins over the defaults.
- **Why it survived refutation:** Reproduced against the shipped dist/ai-usage-v0.1.8 binary: a NOT_YAML config.yaml in ~/.config/ai-usage/ (the only dir the installer creates, scripts/install-release.sh:31,79,84-86) produces 0 "config file" errors, while the same file in ~/.config/usaged/ errors ("config file .../usaged/config.yaml: line 1: expected 'key: value', got \"NOT_YAML\""), because internal/config/config.go:74 still hardcodes DefaultConfigFile = "$HOME/.config/usaged/config.yaml" — commit 045b0ab ("rename: usaged -> ai-usage ... directories ~/.config, Logs, state -> .../ai-usage") moved CONFIG_DIR in the installer and left the Go constant behind. Every refutation angle fails: no Go code references ~/.config/ai-usage (grep over *.go), there is no USAGED_CONFIG env override (only USAGED_LISTEN/INTERVAL_SEC/DEVICE_TOKEN/... at config.go:156-236), neither plist passes --config (install-release.sh ProgramArguments = [ai-usage, serve]; scripts/run.sh:17 execs `./bin/ai-usage serve`), the installer migrates only the state dir (OLD_STATE→NEW_STATE), never the config dir, and on Felipe's machine ~/.config/ai-usage exists (config.example.yaml + device-token) while ~/.config/usaged does not exist at all. Aggravating and previously unstated: with no file at the usaged path, cfg.ConfigPath == "" is passed to api.New (cmd/usaged/serve.go:120), so PUT /v1/config skips saveConfigAtomic behind `if s.configPath != ""` (internal/api/server.go:671-679) and returns ok:true — the Settings tab on a fresh release install silently persists nothing either. The only mitigation is that config.example.yaml's own header line 2 says "Copy to ~/.config/usaged/config.yaml", which contradicts the directory the installer drops it into and is the opposite of the obvious in-place rename.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && S=$(mktemp -d) && B=$S/x/ai-usage-v0.1.8-darwin-universal/ai-usage && mkdir -p $S/x && tar -xzf dist/ai-usage-v0.1.8-darwin-universal.tar.gz -C $S/x && mkdir -p $S/h/.config/ai-usage $S/h/.config/usaged && echo NOT_YAML > $S/h/.config/ai-usage/config.yaml && echo '--- ai-usage dir:' && env -i HOME=$S/h PATH=/usr/bin:/bin $B once 2>&1 | grep -ci 'config file' && rm $S/h/.config/ai-usage/config.yaml && echo NOT_YAML > $S/h/.config/usaged/config.yaml && echo '--- usaged dir:' && env -i HOME=$S/h PATH=/usr/bin:/bin $B once 2>&1 | grep -ci 'config file'   # prints 0 then 1
```

## 13. On any fresh install no config.yaml is ever created, so every Settings change returns {"ok":true} and is silently discarded on the next restart

- **Severity:** BROKEN
- **Location:** `scripts/install-release.sh:84-86 (never writes config.yaml) + internal/api/server.go:671-679, 762-764`
- **User impact:** Anything the user changes in the dashboard Settings — poll interval, provider enable/disable, labels, plan cost, timezone, alert thresholds — appears to save, works until the daemon restarts, then reverts. The LaunchAgent has KeepAlive=true and RunAtLoad=true, so it restarts on every login and every crash. Provider API keys are unaffected (they go to the Keychain). Combined with finding 1, the user cannot fix this by hand either: the file must go in ~/.config/usaged/, not the ~/.config/ai-usage/ the installer built.
- **Why it survived refutation:** Reproduced verbatim in an isolated HOME: PUT /v1/config/interval returned {"interval_sec":1800,"ok":true}, in-process read-back was 1800, `find $HOME/.config -type f` found nothing (the dir was never created), and after restart the value was 900 — because internal/config/config.go:136-152 sets cfg.ConfigPath only when os.Stat of ~/.config/usaged/config.yaml succeeds, no installer or subcommand ever creates that file (install-release.sh:84-86 only copies config.example.yaml into ~/.config/ai-usage/), and the plist passes no --config; the live agent (pid 27493, HOME=/Users/fcavalcanti, ~/.config/usaged absent) therefore runs with configPath=="" so server.go:672 and :762 skip persistence while still answering ok:true, and index.html:1005 prints "Saved" regardless.

**Prove it:**
```sh
S=$(mktemp -d); go build -o $S/ai-usage ./cmd/usaged; mkdir -p $S/h; env -i HOME=$S/h PATH=/usr/bin:/bin USAGED_LISTEN=127.0.0.1:18765 $S/ai-usage serve & sleep 3; curl -s -X PUT -H 'Content-Type: application/json' -d '{"interval_sec":1800}' http://127.0.0.1:18765/v1/config/interval; find $S/h/.config -type f; pkill -f "$S/ai-usage serve"; sleep 1; env -i HOME=$S/h PATH=/usr/bin:/bin USAGED_LISTEN=127.0.0.1:18765 $S/ai-usage serve & sleep 3; curl -s http://127.0.0.1:18765/v1/config | tr ',' '\n' | grep interval_sec; pkill -f "$S/ai-usage serve"
```

## 14. Upgrading from v0.1.1/v0.1.2 mints a NEW device token: the rename migration moves the state dir but not ~/.config/usaged/device-token

- **Severity:** MODERATE — real, but below BROKEN: `devices.json` lives beside the state file (`internal/api/pairing.go:624-632`, i.e. under `~/.local/state/`) and IS migrated, so BLE-paired sticks and the loopback dashboard keep working; the damage is a silently rotated global LAN token (401 for a second Mac, a script, or a stick carrying the old value), a stale secret left at the path v0.1.2's own release notes advertised, and a false "keeps your device token" upgrade promise.
- **Location:** `scripts/install-release.sh:60-74`
- **User impact:** On the only upgrade path a real user can be on (v0.1.1/v0.1.2 -> v0.1.8), the shared LAN token silently rotates. Sticks paired over BLE survive (their per-device tokens are in devices.json, which IS migrated), but anything holding the global token is refused with 401 afterwards: a stick flashed from secrets.h with USAGED_DEVICE_TOKEN (firmware/src/hal/sticks3/creds.cpp:179-180), a second Mac, or any script. The old value is left at ~/.config/usaged/device-token while the installer's closing message points at ~/.config/ai-usage/device-token, so the user reading the wrong file gets a stale secret. The line "-- generated a device token" is the only signal, and it is not phrased as a warning.
- **Why it survived refutation:** Could not refute: `git show v0.1.2:scripts/install-release.sh:34` (identical in v0.1.1) wrote `TOKEN_FILE="${HOME}/.config/usaged/device-token"`, while the only migration in the current 204-line `scripts/install-release.sh` is the state dir (`grep -n usaged scripts/install-release.sh` returns lines 56-68 plus `OLD_STATE=.../state/usaged` and nothing under `~/.config/usaged`), and running the proof over a simulated v0.1.2 layout printed `-- generated a device token (…/.config/ai-usage/device-token)` then `TOKEN CHANGED`, leaving the old file behind — with no fallback anywhere else (the root bootstrap `install.sh` only downloads and `exec`s the release installer; the daemon takes the token solely from the plist's `USAGED_DEVICE_TOKEN` env, `internal/config/config.go:166`), and the v0.1.8 release notes still promise "it keeps your device token".

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && S=$(mktemp -d) && { echo '#!/bin/bash'; echo 'set -euo pipefail'; echo 'launchctl(){ :; }'; sed -n '26,34p' scripts/install-release.sh; echo 'HERE=/nonexistent'; sed -n '54,74p' scripts/install-release.sh; echo 'mkdir -p "$BIN_DIR" "$LOG_DIR" "$CONFIG_DIR"'; sed -n '105,116p' scripts/install-release.sh; } > $S/m.sh && mkdir -p $S/h/.config/usaged $S/h/.local/state/usaged $S/h/.local/bin $S/h/Library/LaunchAgents && printf 'OLDTOKEN0000111122223333' > $S/h/.config/usaged/device-token && echo '{}' > $S/h/.local/state/usaged/devices.json && touch $S/h/Library/LaunchAgents/com.fcavalcanti.usaged.plist && env HOME=$S/h bash $S/m.sh && cmp -s $S/h/.config/usaged/device-token $S/h/.config/ai-usage/device-token && echo SAME || echo 'TOKEN CHANGED'
```

## 15. The release tarball name is spelled three times across two scripts and nothing checks that dist.sh produces the name install.sh asks for

- **Severity:** medium — latent release-process defect on the headline install path: invisible to the maintainer, total for the stranger (curl -fsSL exits 56 printing nothing, aborting before install), already shipped once for ~2h; but it cannot corrupt anything and the current v0.1.8 URL returns 200, so today's installer works
- **Location:** `install.sh:34 and install.sh:56 vs scripts/dist.sh:22-24`
- **User impact:** When it drifts, `make dist` succeeds, the asset uploads fine, the release notes are already written — and every stranger running the advertised one-liner gets a bare curl 404 from install.sh:38. The failure lands entirely on the user's machine; the maintainer sees a successful build. (It is at least loud rather than silent: `curl -fsSL` + `set -e` aborts before anything is installed.)
- **Why it survived refutation:** Could not refute: the proof reproduces exactly (dist.sh would emit ai-usage-v0.1.8-1-g8432429-darwin-universal.tar.gz while install.sh fetches ai-usage-v0.1.8-...), grep over *.md/*.sh/Makefile/*.yml/*.go finds only the three hand-spelled constructions with no CI (.github absent), no release script and no test comparing them, and my best refutation — that `make dist` supplies VERSION — is disproved because the Makefile's `VERSION ?= dev` is not exported into the recipe environment (test makefile: bare make -> VERSION UNSET; make VERSION=v9 -> v9), so bare `make dist` always falls through to `git describe`. The repo's own history contains a realized instance: 045b0ab (14:57) renamed install.sh's expected name from `usaged-${TAG}` to `ai-usage-${TAG}` while releases/latest still returned v0.1.2, whose only tarball is `usaged-v0.1.2-darwin-universal.tar.gz`, so the README one-liner 404'd for ~2 hours until v0.1.8 was published at 16:50.

**Prove it:**
```sh
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && echo "dist.sh would name it: ai-usage-$(git describe --tags --always --dirty)-darwin-universal.tar.gz" && echo "install.sh would fetch:  ai-usage-$(curl -fsSL https://api.github.com/repos/fcavalcantirj/sticks3-ai-usage/releases/latest | awk -F'\"' '/\"tag_name\"/{print $4; exit}')-darwin-universal.tar.gz"
```

## 16. launchctl bootstrap is unguarded under set -e and runs after the binary and plist are already replaced; the bootout before it discards its own failure

- **Severity:** MEDIUM — real and on the primary install/upgrade path, but bounded: a control run with a promptly-exiting job was 0/20 failures, and the live daemon has no SSE/streaming/hijack endpoints and no established connections, so `Shutdown` usually returns at once and the process exits fast; the race needs an in-flight request (device poll, dashboard, statusline) to hold it open. When hit, the user gets a bare "Bootstrap failed: 5" with no statement of machine state, but re-running the one-liner then succeeds (after a failed cycle the job is gone), so it is self-healing on retry rather than a hard brick — contradicting the report's "re-running the one-liner hits the same wall." Note line 165's `kickstart -k ... || true` would have recovered it, but is never reached because 164 aborts first.
- **Location:** `scripts/install-release.sh:79 and scripts/install-release.sh:164`
- **User impact:** If bootout ever fails to remove the job, the installer aborts at line 164 with a bare "Bootstrap failed: 5" after the new binary and the new plist are already on disk, while the OLD daemon keeps running from the old code. Nothing prints what state the machine is in, and re-running the one-liner hits the same wall. The bootout that guards against this is the one step whose failure is deliberately made invisible.
- **Why it survived refutation:** I could not refute the located defect, though I refuted its stated mechanism. The code is exactly as quoted (`set -euo pipefail` at install-release.sh:22, unguarded `launchctl bootstrap` at :164 after `cp` at :80 and the plist heredoc at :123-160; no trap or error handler anywhere in the script), and the proof reproduces: second bootstrap on a loaded label gives `Bootstrap failed: 5: Input/output error`, rc=5. But the report's trigger — "if bootout ever fails" — is wrong, and its own note that it could not reproduce a bootout failure is the tell. I reproduced the abort with **bootout returning 0**: `launchctl bootout` returns before the job is actually gone when the process takes a beat to exit, so with a job shaped like the real daemon (honors SIGTERM, drains ~3s, matching `httpSrv.Shutdown` with its 5s ctx at cmd/usaged/serve.go:153-156) a tight bootout→bootstrap gave 5/10 aborts — failing every cycle in which the job was actually running (`cycle 1: bootout=0 bootstrap=5`, `cycle 3: bootout=0 bootstrap=5`, …). So the `|| true` at :79 is correct and necessary (bootout legitimately returns 3 on a fresh install); the real gap is that :164 never waits for the job to actually disappear and is unguarded. I also checked the impact claim's premise: `cp` over a running executable on macOS succeeds (rc=0, tested), so the new binary and plist genuinely are on disk when the abort happens. Reachability is confirmed — scripts/dist.sh:67 copies install-release.sh into the release tarball as `install.sh`, which is README.md:28's headline `curl -fsSL ... | bash`, i.e. every user install and upgrade. Same shape unfixed at scripts/install.sh:71/119.

**Prove it:**
```sh
L=com.example.bstest; P=$HOME/Library/LaunchAgents/$L.plist; printf '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>/bin/sleep</string><string>3600</string></array><key>RunAtLoad</key><true/></dict></plist>\n' "$L" > "$P"; launchctl bootstrap gui/$(id -u) "$P"; echo "rc1=$?"; launchctl bootstrap gui/$(id -u) "$P"; echo "rc2=$?"; launchctl bootout gui/$(id -u)/$L; rm -f "$P"
```

## 17. install.sh's exec discards its own EXIT trap, so every install and upgrade leaks the unpacked release into /var/folders

- **Severity:** COSMETIC
- **Location:** `install.sh:27 and install.sh:56`
- **User impact:** Roughly 26 MB of /var/folders litter per install or upgrade, never cleaned by the installer. macOS eventually reaps it; nothing is functionally broken.
- **Why it survived refutation:** Running the real install.sh verbatim (network stubbed, inner installer no-op'd) left a 22 MB temp dir behind — TMPDIR before/after diff showed the new /var/folders/.../tmp.CrZjXDkc8m still holding the tarball, SHA256SUMS and the extracted tree — because `exec bash "${TMP}/…/install.sh"` (install.sh:56) replaces the shell image before the `trap 'rm -rf "$TMP"' EXIT` at install.sh:27 can fire; the exec'd inner script (scripts/install-release.sh, per scripts/dist.sh:67) contains no trap and no rm of its own directory, and README.md:28 makes this curl|bash path the documented install route, so no caller covers it.

**Prove it:**
```sh
printf '#!/bin/bash\nset -euo pipefail\nTMP="$(mktemp -d)"\ntrap \x27rm -rf "$TMP"\x27 EXIT\necho "$TMP" > /tmp/tmpdirname\nprintf \x27#!/bin/bash\\ntrue\\n\x27 > "$TMP/inner.sh"\nexec bash "$TMP/inner.sh"\n' > /tmp/t.sh && bash /tmp/t.sh && D=$(cat /tmp/tmpdirname) && [ -d "$D" ] && echo "LEAKED: $D" || echo cleaned
```

## 18. The BLE scan goroutine has no recover, while the sibling provision goroutine was given one today — a scan panic kills the whole daemon

- **Severity:** RISK — a real, unrefuted hardening asymmetry with a proven crash mechanism, but not BROKEN: the specific library defect bc8a645 was written for (Connect returning a zero Device, then DiscoverServices panicking) cannot fire from the scan, because Central.Scan never connects — as bleProvisioner.Scan's own doc comment states. No panic has been observed in the real scan path. Two caveats worth carrying into any fix: the darwin scan path does have its own plausible panic source (Central.stopScan retries adapter.StopScan for up to 2s from a goroutine spawned inside Central.Scan, while Adapter.StopScan does an unsynchronized `a.scanChan <- nil` on a channel Adapter.Scan concurrently closes and nils — a send-on-closed-channel window), and that panic would fire in that inner goroutine, where a recover added to runScan could not catch it either.
- **Location:** `internal/api/setup.go:431 (go s.runScan) and setup.go:443 (found, err := s.prov.Scan(ctx))`
- **User impact:** Identical to the 2026-09-07 outage the provision recover was written for, but reached by pressing "look for a device" instead of "set it up". The plist has KeepAlive=true and ThrottleInterval=30, so launchd restarts the daemon within 30 s and the dashboard shows the scan card simply go blank with no error — the failure is invisible, and every quota reading, cooldown and in-flight run is lost with it. bc8a645's own message records that CoreBluetooth "can wedge such that only a daemon restart brings scanning back", i.e. the scan path is the one already known to misbehave.
- **Why it survived refutation:** Could not refute: `grep -rn "recover()" internal/ cmd/` (non-test) returns only central.go:329/:395 (inside Central.Provision and its deferred Disconnect) and setup.go:602 (callProvisioner) — every layer of the scan path (api.runScan → bleProvisioner.Scan → Central.Scan) is bare, serve.go:114 passes the provisioner unwrapped via WithProvisioner (a one-line field set), and the proof reproduces verbatim on an untouched repo ("panic: bluetooth: nil device ... created by usaged/internal/api.(*setup).handleScan", FAIL usaged/internal/api). The path is user-reachable: the live daemon answers GET /v1/setup with "available":true and internal/web/index.html:1312 startSetupScan() POSTs /v1/setup/scan.

**Prove it:**
```sh
SP=/tmp/scanpanic; mkdir -p $SP
cat > $SP/zz_scanpanic_test.go <<'EOF'
package api

import (
	"context"
	"net/http"
	"testing"
)

type panickingScanner struct{ fakeProvisioner }

func (p *panickingScanner) Scan(context.Context) ([]FoundDevice, error) {
	panic("bluetooth: nil device")
}

func TestAPanickingScanDoesNotKillTheAgent(t *testing.T) {
	fake := &panickingScanner{}
	rig := newSetupRig(t, fake, &fake.fakeProvisioner)
	rec := rig.do(http.MethodPost, setupScanPath, `{}`, setupLoopback, setupDashToken)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("scan: status = %d, want 202", rec.Code)
	}
	rig.setup.wg.Wait()
}
EOF
cat > $SP/overlay.json <<EOF
{"Replace": {"/Users/fcavalcanti/dev/m5/sticks3-ai-usage/internal/api/zz_scanpanic_test.go": "$SP/zz_scanpanic_test.go"}}
EOF
cd /Users/fcavalcanti/dev/m5/sticks3-ai-usage && go test -overlay=$SP/overlay.json ./internal/api -run 'Panicking' -v
```

## 19. Central.Scan has no recover either, and its stop goroutine calls the library from a goroutine nothing guards

- **Severity:** LOW — the missing recover on the scan path is real and reachable, but it largely restates the sibling runScan finding; the part a recover genuinely cannot fix (the cgo callback and stop goroutines) has no demonstrated panic source, making it defense-in-depth rather than a live crash. The stop goroutine's concrete defect is actually a deadlock (blocking send on gap_darwin.go:80 with no receiver when adapter.Scan errors early, hanging `<-stopped` while holding c.mu), not a panic.
- **Location:** `internal/bleprov/central.go:182-238 (Scan) and central.go:201-205 (the stop goroutine)`
- **User impact:** Same outage as the finding above, with one worse variant: if the panic lands on the library's callback goroutine or on the stop goroutine, adding a recover to runScan will NOT fix it — the daemon still dies and launchd still restarts it silently. The user sees the device list never appear and the status line reset, with nothing in the log.
- **Why it survived refutation:** Confirmed on the code: `grep -n 'recover()' internal/bleprov/central.go` returns only 329 and 395, both inside Provision, and the repo's only other recover (internal/api/setup.go:602, callProvisioner) is on the provision path — while the scan chain runs in a *detached* goroutine (`go s.runScan(...)` at internal/api/setup.go:430), so net/http's own handler recover does not cover it either; the callback claim also checks out, since cbgo@v0.0.4/cbhandlers.go:61 is `//export BTCentralManagerDidDiscoverPeripheral`, a C→Go callback dispatched to `adapter_darwin.go:159 cmd.a.peripheralFoundHandler(...)` on CoreBluetooth's own thread, which no deferred recover in Scan/runScan can catch. But the incremental hazard is hypothetical: I found no concrete panic path in the callback (makeScanResult guards its only index with `len(...) > 2`, AdvertisementPayload is always non-nil, usaged's callback only reads/stores under its own mutex) or in StopScan (nil check + channel send + cgo call), the real 2026-09-07 panic was on the Connect path that IS double-guarded, and the impact's "nothing in the log" is false — the plist sets StandardErrorPath to ~/Library/Logs/ai-usage/ai-usage.err.log where a Go panic stack would be printed.

**Prove it:**
```sh
grep -n 'recover()' internal/bleprov/central.go   # 329 and 395, both inside Provision; Scan has none
sed -n '182,238p' internal/bleprov/central.go     # the unguarded Scan and its stop goroutine
# On hardware: run a scan against a device that was reflashed since the Mac bonded to it (the exact state that produced the 2026-09-07 zero-Device panic in Connect), then check whether pid changes:
lsof -nP -iTCP:8765 -sTCP:LISTEN; curl -s -X POST 127.0.0.1:8765/v1/setup/scan -H 'Content-Type: application/json' -d '{}'; sleep 15; lsof -nP -iTCP:8765 -sTCP:LISTEN
```

## 20. The Settings "enabled" checkbox is inert: PUT /v1/config answers ok:true and persists it, but nothing ever reads it back into the fetcher list

- **Severity:** BROKEN
- **Location:** `cmd/usaged/once.go:92-171 (buildFetchers) vs internal/api/server.go:643 and :695`
- **User impact:** Unchecking a provider in Settings and saving reports success, writes enabled: false to the YAML, and the settings page reads it back as disabled — while the scheduler keeps polling that provider every interval and it keeps occupying a row in /v1/usage and on the StickS3 screen. Not "until restart": forever, because a restart re-runs the same buildFetchers that has never heard of the flag.
- **Why it survived refutation:** buildFetchers (cmd/usaged/once.go:92-171, the only fetcher builder, called from both serve.go:57 and once.go:60) reads only FixturesDir/TZ/ClaudeSource/CodexSource/OpenRouterKeys/GroqKey/GroqProbeEnabled — never Enabled — and no downstream filter exists either (`grep -i 'enabled|disabled' internal/sched/*.go internal/snapshot/*.go` returns nothing, and handleUsage serves sched.Current() verbatim), so the only non-display reader of the flag is the setup readiness denominator at setup.go:1137. Decisively, spec.json task index 46 is marked "passes": true while its own rule reads "`enabled: false` removes the provider from the snapshot entirely (not an 'off' row), which changes the rev" — the config tests only assert YAML parse/serialize round-trips, which is why the suite is green.

**Prove it:**
```sh
grep -rn --include='*.go' 'EffectiveProviders\|ProviderConfigs' internal cmd | grep -v _test.go   # no hit inside buildFetchers
sed -n '92,171p' cmd/usaged/once.go   # the whole fetcher-selection function; no Enabled anywhere
# Live (writes the user's config file, so ask first): PUT /v1/config with groq enabled:false, then
#   curl -s 127.0.0.1:8765/v1/config | grep -o '"id":"groq"[^}]*'     -> enabled false
#   curl -s 127.0.0.1:8765/v1/usage  | grep -o '"id":"groq"[^}]*'     -> still polled and still listed
```