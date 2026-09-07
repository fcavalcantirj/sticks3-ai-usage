---
round: 1
builder_session: fresh 2026-09-07 session, read only the handover directory, then re-verified every [REAL] claim against the live repo, the running daemon and a throwaway PlatformIO build
---

# Plan r1 — BLE zero-config provisioning

## Blocking constraints, restated

1. **Compiling is not working, and a passing test is not a working wire.** Every
   defect this project has had was documented behaviour that turned out false on the
   hardware. So nothing in the BLE path may be called done on the strength of `go test`,
   `make fw-test`, or a clean link. The only evidence that counts is a StickS3 that
   actually joined, and a `[FETCH] code=200` from it afterwards.
2. **A credential value never leaves the process it was read in.** No log line, no
   screen, no HTTP response, no getter, no commit may carry an SSID password, a device
   token or an OTA password. Lengths and `set`/`unset` only. This applies to the six
   passkey digits too — screen only, never serial.
3. **`usaged` is started by launchd and by nothing else.** Hand-starting it on 8765
   takes the port launchd needs, so launchd's copy dies, nothing survives a reboot, and
   every log read afterwards is describing a process nobody is looking at. The lane is
   `make build` then `launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.usaged`, then
   `lsof -nP -iTCP:8765 -sTCP:LISTEN` to confirm the listener's PID is launchd's.
4. **The snapshot body is hashed into `rev`, so anything that varies per request must
   live outside it.** Put a per-request field in the hashed body and the device redraws
   on every poll — the redraw-only-on-change guarantee dies silently.
5. **Each individual device upload needs Felipe's authorization for that upload.** A
   past "flash whenever you want" covers the transfer, never powering the device on, and
   never the next flash by default.

## Handover discrepancies

I re-verified every `[REAL]` claim I could reach. **Seven mismatches, one of them
material enough to change the first move.**

### 1. `next_action` is wrong — the stalled HAL file links fine [MATERIAL]

The handover says: *"Rebuild `hal/sticks3/bleprov.{h,cpp}` properly — the stalled
agent's version does not link the BLE stack. Verify by flash size: a real BLE build is
~50%, not 33.5%."*

The file is not broken. Nothing **references** it, so the linker discards the
translation unit and never pulls the BLE library in. `main.cpp` has zero `bleProv`
occurrences, and the only two files that include `hal/sticks3/bleprov.h` are that header
and its own `.cpp`.

I measured it. Clean worktree at HEAD, one throwaway reference added to `main.cpp`
(`#include` plus an unreachable `if (millis() == 0xFFFFFFFFu) { bleProvBegin(false);
bleProvUpdate(0); bleProvEnd(true); }`), `pio run`:

```
RAM:   [==        ]  23.8% (used 77872 bytes from 327680 bytes)
Flash: [=====     ]  50.7% (used 1693377 bytes from 3342336 bytes)
========================= [SUCCESS] Took 41.50 seconds =========================
```

Baseline `firmware.bin` is 1,120,960 B = 33.54% of the 0x330000 app0 partition. So BLE
costs **+572,417 B flash**, landing at **50.7%** — the handover's +578,928 B / 50.2% was
right to within a rounding of build id. The link produced **no undefined symbols**:
every `BLEDevice`, `BLE2902`, `BLESecurity` and `esp_bt_*` reference in that file
resolved against the framework's bundled `libraries/BLE`. `hal/sticks3/bleprov.cpp.o` is
901,100 B on disk and was already compiling.

Acting on the handover's `next_action` as written would have thrown away a file that
works. The worktree has been removed; the probe patch exists nowhere.

### 2. The Go half is further along than "compiles, never exercised" — but one wire is cut

`internal/api/setup.go` is **wired**: `server.go:126` calls `srv.setup.routes(mux)`,
registering `GET`/`DELETE` on the setup path and `POST` on scan and provision. Its tests
pass, as do `internal/bleprov`'s.

The cut wire is the injection. `server.go:122` says the Provisioner is *"injected by
cmd/usaged (internal/bleprov) through WithProvisioner"* — **`WithProvisioner` does not
exist anywhere in the repo**, and `cmd/usaged` never imports `internal/bleprov` (its
import block ends at `internal/stats`). `srv.provisioner` is therefore permanently nil
and every setup route answers "unavailable" forever.

### 3. Signature mismatch nobody has bridged — an adapter is required

Not mentioned in the handover. The concrete type does not satisfy the interface:

| `api.Provisioner` (setup.go:78) | `bleprov.Central` (central.go) |
|---|---|
| `Scan(ctx) ([]FoundDevice, error)` | `Scan(ctx, window time.Duration) ([]Peripheral, error)` |
| `Provision(ctx, addr) error` | `Provision(ctx, addr string, rec Record, onProgress func(Progress)) (*Result, error)` |

The extra `Record` argument is the whole design: the interface deliberately takes no
credentials so `internal/api` cannot hold one. The adapter is where the record gets
built, and it belongs in `cmd/usaged`, not in either package.

### 4. The running daemon is a stale binary

`bin/usaged` is dated **Sep 6 23:30**, which predates `05f891f` (setup.go landed
00:06–00:12 on Sep 7). So `GET /v1/setup`, `/v1/setup/status` and `/v1/setup/scan` all
fall through to the dashboard HTML on the live agent right now. Not a code defect — a
build that was never redone. Listener is launchd's, PID 16949, `state = running`.

### 5. Open question 1 is already answered in the code the handover distrusts

*"Can `BLEDevice::deinit()` actually reclaim the ~579 KB at runtime?"* —
`hal/sticks3/bleprov.cpp:97-100` and `:376-395` answer it, with citations:

- **Flash is unconditional and is never reclaimed.** The 572 KB is in the image whether
  BLE runs or not. Only RAM comes back.
- RAM is released by `esp_bt_mem_release(ESP_BT_MODE_BTDM)` — chosen over
  `esp_bt_controller_mem_release` because it also frees the *host* stack's BSS and data
  — and only after proving `esp_bt_controller_get_status() == ESP_BT_CONTROLLER_STATUS_IDLE`,
  because `deinit()` returns void and checks nothing.
- `deinit(false)`, not `deinit(true)`: the release_memory branch skips clearing the
  library's `initialized` flag (`BLEDevice.cpp:649-662`), leaving the library convinced
  BLE is still up.
- The lifecycle is deliberately **one BLE session per boot** — the C++ wrapper objects
  are nulled rather than deleted, so a second session leaks the graph again.

Still `[UNVERIFIED]` on hardware, but it is a designed answer with line references, not
an unanswered question.

### 6. Open question 2 is also already answered in code

*"Where does the daemon get the Wi-Fi password?"* — `internal/bleprov/creds_darwin.go`
decided this on 2026-09-06 with commands actually run on this Mac (macOS 26.5, build
25F71): the password comes from **`/Library/Keychains/System.keychain`, not the login
keychain**, via `security` with `desc="AirPort network password"` and `svce="AirPort"`,
under a bounded context because an unanswered authorization dialog blocks `security`
forever (a 30 s bounded run exited 124 with no output). The SSID is asked of three
sources in decreasing trust, and a preferred-network guess is returned tagged
`ErrSSIDRedacted` rather than passed off as an observation, because macOS 14+ redacts the
SSID for a process without Location Services authorization. The dashboard confirming a
redacted SSID is the design's answer, and it is the option the handover offered.

### 7. Smaller ones

- **HEAD is `71885a8`**, not `05f891f` — the handover names the commit before itself.
  Tree clean, branch `main`.
- **The stdlib-only rule is in `docs/GROUND_RULES.md`, not `AGENTS.md`.** The handover
  says AGENTS.md; I grepped both plus GOLDEN_RULES.md and AGENTS.md contains no
  dependency rule at all. It matters because the rule is *mechanical*: "`go list -m all
  | wc -l` must print 1". See Concern 4 — that command no longer even runs.
- Server snapshot age is **21,856 s (~6 h)** as of this session (`/v1/usage`,
  `seq 240 rev eca58c5a`). Unrelated to BLE, flagged in passing.

### Verified clean, no mismatch

Ledger 80/84, open = 77, 78, 79, 83 (`spec.json` is a bare 84-element array).
`make fw-test` → **359 PASS, Failed 0**. `go build ./...`, `go vet ./...`, `go test ./...`
all clean across 13 packages. Device answers ping at 192.168.0.136 and last fetched
`200` at 08:11:52 on firmware `c318338+dirty` (pre-BLE, as stated). `internal/web` has
`pair-card` and **zero** setup-card identifiers — untouched, as stated.
`docs/BLE_PROVISIONING.md` is 507 lines and does specify the four UUIDs, the ATT_MTU-23
framing, the CRC-32 vector, `setAccessPermissions(ESP_GATT_PERM_READ_ENC_MITM |
ESP_GATT_PERM_WRITE_ENC_MITM)`, and the Just-Works-plus-button-hold fallback.

## Acceptance checklist — point-by-point

**The handover has no acceptance checklist section.** The skill makes it the author's
authority made explicit, so I will not invent one and then grade myself against it.
Below is what I derived from the handover's blocking constraints, hard rules and
residuals, offered for the author to ratify, amend or replace in REVIEW-r1. Each is
answered against the plan that follows.

| # | Proposed criterion | How this plan satisfies it |
|---|---|---|
| 1 | The plan does not rewrite `hal/sticks3/bleprov.{h,cpp}` | Step 2 adds a call site in `main.cpp` and touches the HAL file only if hardware shows a defect. Measured evidence in Discrepancy 1. |
| 2 | Firmware evidence is a measured flash figure, not an assertion | Step 2 gates on `Flash: 50.7%` from `pio run` and on `make fw-test` still 359/0. |
| 3 | No credential value is logged, drawn, returned or committed | Step 4 injects `MintToken`; Step 5 renders `set`/`unset` and lengths only; Step 7 greps the diff and the built image. `Record.LogValue()` already redacts. |
| 4 | The daemon is only ever restarted through launchd | Every verify step is `make build && launchctl kickstart -k …`, then the `lsof` PID check. Stated as Step 0's habit and repeated at Steps 4 and 5. |
| 5 | `rev` stays a pure function of the snapshot body | Setup state is served by `/v1/setup*`, a separate route family with its own state; nothing in Steps 3-5 writes to the snapshot. Step 6 re-checks `rev` is unchanged across two polls with the setup card open. |
| 6 | No device upload happens without Felipe's authorization for that upload | Step 6 stops and asks. Nothing before Step 6 touches the device. |
| 7 | Tasks 77/78/79 stay `passes: false` until Felipe says so | Step 8 writes evidence into `spec.json` notes and leaves `passes` alone. |
| 8 | The portal is not deleted and the portal form does not regain the agent address or token | No step touches `hal/sticks3/portal.*`, `usage/portal.*` or the portal form. |
| 9 | The approved dependency exception is recorded where the rule actually lives | Step 1 amends `docs/GROUND_RULES.md` (not `AGENTS.md`) and fixes the now-broken mechanical test. |
| 10 | Every claim in the final report carries `[REAL]`/`[TEST]`/`[UNVERIFIED]` and a date | Step 8. |

## The plan

Ordered so the cheapest disproof comes first and nothing touches the device until the
two halves have each been proven on their own side.

### Step 0 — baseline (no edits)

Rebuild and kickstart so the live daemon matches HEAD, then confirm `/v1/setup` returns
JSON saying "unavailable" rather than dashboard HTML. That single transition proves
Discrepancy 4 was staleness and not a routing bug, and it costs one command.

### Step 1 — record the dependency exception where the rule lives

`docs/GROUND_RULES.md` "Go: stdlib only" currently reads *"No external Go dependencies.
`go list -m all | wc -l` must print 1"*. Amend it to name the one approved dependency
(`tinygo.org/x/bluetooth`, Felipe's "go for it"), say why (macOS is central-only and
CoreBluetooth has no stdlib path), and **replace the mechanical test**, which no longer
runs at all — see Concern 4. Proposed replacement, which I ran and which prints 4:

```
go list -deps ./... | grep -c '^[^/]*\.[^/]*/'
```

Add a one-line pointer in `AGENTS.md` so the next reader looking there finds it.
This is the item the handover lists as *"still owed"*.

### Step 2 — give the firmware a call site (the corrected `next_action`)

In `main.cpp`, in the existing unprovisioned branch at `:745` (`[CREDS] unprovisioned —
raising the setup portal`), begin BLE alongside the portal, and drive `bleProvUpdate()`
from the same place `portalUpdate()` is driven at `:774`. Terminal `BleProvState::Provisioned`
takes the same restart path the portal's `Joined` already takes at `:785`.

**Measure before assuming they coexist.** BLE at 23.8% RAM was measured *without* the
SoftAP, DNS server and HTTP server up. Both radios share one 2.4 GHz front end and the
portal is already known to be timing-sensitive (Concern 1). So this step's verification
is a `pio run` for the flash figure **plus** a `[HEAP]` reading taken on hardware in
Step 6 with both up — not a claim that it is fine.

Verify: `Flash: ~50.7%`, `make fw-test` 359/0, `go build ./...` untouched.

### Step 3 — `api.WithProvisioner`

Add the functional option `server.go:122` already promises, matching the file's existing
option style. Its test is that a `nil` provisioner still yields the "unavailable"
responses `setup_test.go` already asserts, and a fake yields the wired ones.

### Step 4 — the adapter, in `cmd/usaged`

A small type in `cmd/usaged` that holds a `*bleprov.Central` and a `*bleprov.Gatherer`
and implements `api.ProgressProvisioner` (the progress variant, since the central
already reports `Progress`):

- `Scan(ctx)` → `Central.Scan(ctx, window)` with the window chosen here, mapping
  `[]bleprov.Peripheral` → `[]api.FoundDevice`.
- `ProvisionProgress(ctx, addr, report)` → gather the record, then
  `Central.Provision(ctx, addr, rec, onProgress)`, mapping `Progress` onto the `Step*`
  constants and the error onto `*api.ProvisionFailure`.

The record is built here and nowhere else: SSID and password from
`Gatherer`/`creds_darwin.go`, `Host` left **empty** so the device discovers the agent
over the `_usaged._tcp` mDNS record that `c318338` already publishes (this is what keeps
the device working when the Mac's IP moves), `Port` from config, `OTAPass` from config.

**`Gatherer.MintToken` must be injected**, not used bare. `creds.go:240` says so
explicitly: a token the agent has not recorded in `internal/api`'s paired-device store is
a credential nothing accepts. The injected function mints *and* records, reusing
`pairing.go`'s existing per-device token machinery.

Verify: `go test ./...`, then `make build && launchctl kickstart -k`, then
`POST /v1/setup/scan` on the live daemon and see a real radio scan — the first moment
anything in this path touches hardware.

### Step 5 — the dashboard card

Build the setup card in `internal/web/index.html` against `/v1/setup*`, with tests in
`web_test.go` mirroring the `pair-card` ones. It shows: the found device and its RSSI,
the SSID **with its source** and a confirm affordance when `ErrSSIDRedacted` came back,
the live step list, and the failure sentence with its next step. It renders no password,
no token, and no passkey — the passkey is on the device screen and macOS asks for it.

Note the handover's residual: a previous agent left a credential input in this page and
the work was reverted rather than committed red. Start from HEAD's untouched
`index.html`.

### Step 6 — hardware, gated on Felipe

**Stop here and ask.** Flashing needs authorization for that upload, and the device is
on battery behind a 12 h backstop, so Felipe must press the blue button for
`flash_when_awake.sh` to catch the wake.

Then the real run, in this order: passkey appears on the panel → macOS prompts → bond →
record transfers → device joins → `[FETCH] code=200` → `sh scripts/device-report.sh`
shows the drift converging. Read `[HEAP]` while BLE and the portal are both up (Step 2's
open measurement), and confirm advertising stops once provisioned.

### Step 7 — the publish gate

`make fw-publish-check` — builds with `secrets.h` moved aside and greps the image. It is
the real gate; `make fw-check-secrets` failing on a dev build is correct and expected.
Whether to actually publish remains Felipe's call and is not part of this plan.

### Step 8 — the ledger

Write the measured evidence into `spec.json` for 77/78/79 and leave `passes: false`.
Only Felipe flips those.

## Concerns

- **[HIGH] BLE and the SoftAP portal have never been up at the same time.** The 23.8%
  RAM figure is BLE alone. The portal is already the most timing-fragile thing in the
  firmware — a scan with the AP up takes ~9.2 s against a deadline that was 6 s and
  failed 100% of the time, and the spike's numbers did not transfer because the station
  was connected in one case and idle in the other. Adding a second radio user to that is
  exactly the shape of the four defects this project has already paid for. It may turn
  out that BLE should come up first and the portal only as a timed fallback. I have made
  it a measurement in Step 2/Step 6 rather than a guess, but it could still change the
  design and it is the single most likely thing to.

- **[HIGH] The handover's `next_action` would have destroyed working code.** Documented
  under Discrepancy 1 with the build output. Raising it as a concern, not just a
  correction, because it means the author's model of why flash was 33.5% was wrong, and
  anything else reasoned from "the stalled agent's files don't work" deserves the same
  re-check. `internal/bleprov` and `setup.go` got that re-check here; they are in better
  shape than described too.

- **[MEDIUM] The wire contract needs a button gesture that is reserved.**
  `docs/BLE_PROVISIONING.md` says advertising runs while unprovisioned *"plus a bounded
  window the owner opens deliberately (a button hold) on a device that is already
  provisioned"*. `docs/DEVICES.md:109-118` says **BtnA HOLD is reserved** for a future
  AI-agent action (ORDER #72) and is deliberately unbound, and separately that the 600 ms
  threshold is too short for a deliberate hold. BtnB hold is taken by flip-180°. So the
  re-provisioning gesture has no home. Button-map changes are Felipe's call; this plan
  ships **unprovisioned-only advertising** and leaves the re-provisioning window out
  until he decides. Question 2.

- **[MEDIUM] `go list -m all` no longer runs, so the stdlib-only gate is unenforceable
  as written.** It fails on `github.com/tdakkota/win32metadata@v0.1.0` — *"remote:
  Repository not found"* — a transitive dependency of `saltosystems/winrt-go`, the
  Windows BLE backend. It does not affect this Mac: `go build`, `go vet`, `go test` and
  `go mod download` all succeed, because the darwin build tags never reach that path.
  But `docs/GROUND_RULES.md`'s literal test errors out instead of printing a number, and
  `scripts/lint.sh` never enforced it anyway (staticcheck only). Step 1 replaces the
  test. Worth knowing that an upstream dependency has a dead repo in its graph.

- **[MEDIUM] No acceptance checklist to be judged against.** I proposed ten above rather
  than block, but the author's verdict should replace them with the real ones if these
  are not it. Question 1.

- **[LOW] The live daemon serves a binary older than the code.** Fixed by Step 0. Noted
  because any conclusion drawn from the running agent before Step 0 is about Sep 6's
  code, and this project has twice been burned by reading a log without knowing who
  wrote it.

- **[LOW] Snapshot is ~6 h stale** (`age: 21856`). Outside this work's scope; mentioning
  it because Step 6 reads `device-report.sh`, and a stale server makes drift rows harder
  to read.

## Questions for the author

1. **Do you ratify the ten proposed acceptance criteria**, or should REVIEW-r1 carry the
   real checklist? I did not want to grade myself against a rubric I wrote.

2. **BLE + SoftAP: both up when unprovisioned, or BLE first with the portal as a timed
   fallback?** Concern 1. The residuals say keep the portal, and I am keeping it — the
   question is only whether they run concurrently or in sequence. If you have no strong
   view, Step 2's `[HEAP]` measurement decides it, but sequencing is cheaper to build
   than to retrofit.

3. **The re-provisioning gesture on an already-provisioned device has no free button**
   (Concern 3). I am shipping unprovisioned-only advertising. Confirm that is right, or
   this waits for Felipe on the button map.

4. **Handover open question 3 is still open and is Felipe's**: does the onboarding text
   ("run the daemon first, configure providers there, then the stick") go in the
   M5Burner listing, the dashboard, or both? Nothing in Steps 1-8 depends on the answer,
   so it need not block approval — but it is unanswered and should not be silently
   dropped.

5. Your open questions 1 and 2 are **answered in the code** (Discrepancies 5 and 6).
   Confirm you accept those answers so they can be struck, rather than re-litigated by
   the next session that reads the handover.
