---
slug: provisioning-spike
date: 2026-09-06
status: approved
round: 1
author_session: planner session that steered a separate builder agent through the Solvr room for the whole 2026-09-05/06 build (ledger tasks ~45-74)
---

# Handover — Wi-Fi provisioning for the StickS3 usage monitor

## Mission

`usaged` is a Go agent on Felipe's Mac that polls Claude, ChatGPT/Codex, OpenRouter and
Groq every 15 minutes, serves a dashboard on `127.0.0.1:8765`, and feeds an M5StickS3 that
**redraws only when the data actually changes**. It works and is signed off.

The remaining work is one group: **Wi-Fi provisioning (tasks 75-79)**. Two reasons it
matters, one of them urgent:

1. `secrets.h` is a compile-time header, so the Wi-Fi SSID, **Wi-Fi password**, device
   token and host address are compiled into `firmware.bin` as plaintext. **The firmware
   cannot be published to M5Burner or anywhere else** until this is fixed.
2. A stranger who flashes a published build gets a device that tries to join *our* SSID
   and reach *our* Mac at a fixed address, with no screen anywhere to correct it.

**Done** means: a binary carrying no credentials, and a device a stranger can set up with
no cable, no build step, and no file edited.

**You are both planner and builder.** Earlier sessions split the roles across a Solvr room
with Felipe couriering messages. That is over — there is no room to read and no relay.

## Where things stand (verified)

- [REAL] Repo `~/dev/m5/sticks3-ai-usage`, HEAD `c1d18a0`, working tree clean — verified
  2026-09-06 via `git status --short` (empty).
- [REAL] Ledger `spec.json`: **78 of 84 tasks passing**. Open: 75-79 (provisioning) and 83
  (optional task-hub rows, deferred by decision — leave it).
- [REAL] Agent running under launchd as pid 39603, owning the listener on `:8765` —
  verified via `launchctl print gui/501/com.fcavalcanti.usaged` and
  `lsof -nP -iTCP:8765 -sTCP:LISTEN`.
- [REAL] Device is on firmware `cd50477`, currently **asleep on battery** (ping to
  `192.168.0.136` fails; last access-log User-Agent was `sticks3-usage/cd50477`).
- [REAL] Device MAC `14:c1:9f:d4:d5:34`, hostname `sticks3-usage.local`, last known IP
  `192.168.0.136`. The MAC is hard-coded as a flash guard in `upload_ota.sh`.
- [REAL] **Credentials are plaintext in the binary.** Verified 2026-09-06 by extracting
  each value from `firmware/include/secrets.h` and grepping a real build:

  ```
  strings .pio/build/m5stack-sticks3/firmware.bin | grep -qxF "<value>"
  ```

  WIFI_SSID, WIFI_PASS, USAGED_DEVICE_TOKEN and USAGED_HOST all matched. `secrets.h` is
  gitignored (`.gitignore:10`), which protects the repo and does nothing for the artefact.

- [REAL] The fields, verbatim from `firmware/include/secrets.h.example` (values elided):

  ```c
  #define WIFI_SSID "<value>"
  #define WIFI_PASS "<value>"
  #define USAGED_HOST "<value>"
  #define USAGED_PORT 8765
  #define USAGED_DEVICE_TOKEN "<value>"
  #define OTA_PASS "<value>"
  ```

- [REAL] **No server-to-device config channel exists.** The firmware parses only
  `v, rev, seq, generated_at, next_sec, age, providers` from the snapshot
  (`firmware/src/usage/model.cpp:69-91`). `GET /v1/device` is server-to-**browser** and is
  not read by the firmware. Routes today (`internal/api/server.go:91-104`): `GET /`,
  `/index.html`, `/healthz`, `/v1/usage`, `/v1/usage.txt`, `/v1/device`, `/v1/stats`,
  `POST /v1/refresh`, `GET|PUT /v1/config`, `PUT /v1/config/interval`,
  `POST|DELETE /v1/keys`.
- [REAL] `WiFiProv`, `BLE` and `SimpleBLE` ship with the Arduino core in use; nothing in
  `platformio.ini` `build_flags` disables BLE. Verified by listing
  `~/.platformio/packages/framework-arduinoespressif32/libraries/`.
- [REAL] Firmware size headroom is ample: the current build is 1,051,312 bytes, ~31% of
  the app partition; RAM ~16.6%.
- [TEST] `go test ./...`, `make verify`, `make smoke` all pass at HEAD; `make fw-test`
  reported 177 passing on the last firmware commit.
- [UNVERIFIED] Everything task 75 exists to measure: whether SoftAP + WebServer +
  DNSServer fit and run together, whether `scanNetworks()` works while the AP is up,
  whether the captive sheet auto-pops on iOS/Android, whether M5Burner's Wi-Fi step writes
  credentials we can read, and whether leaving AP mode needs a reboot. Reasoned from
  vendor docs, **not checked on this hardware**.

### Commands you will actually need (verbatim)

```sh
# rebuild the Go agent and make it live — a host change is NOT live until both run
make build && launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.usaged

# confirm the listener is launchd's, not a hand-started process
lsof -nP -iTCP:8765 -sTCP:LISTEN

# build firmware without the device present, then upload when it wakes
sh firmware/scripts/upload_ota.sh --build-only
sh firmware/scripts/upload_ota.sh --upload-only <firmware.bin> <build_id>
sh firmware/scripts/flash_when_awake.sh          # both, polling for a button press

# what the device actually did — device age vs the server's authoritative age
sh scripts/device-report.sh

# device serial (works while USB-connected; /dev/cu.usbmodem1101 when enumerated)
```

## Blocking constraints (builder: restate these before planning)

1. **A green test is not evidence for anything that crosses a process or hardware
   boundary.** Four defects on 2026-09-06 were documented behaviour that was wrong on the
   wire, each passing its tests: `addHeader("User-Agent")` silently dropped by the Arduino
   core; `esp_timer_get_time()` not surviving deep sleep; a TCP readiness probe against
   UDP port 3232; M5Unified never enabling battery charging. Task 75 exists because tasks
   76-79 rest on five more claims of exactly that kind.
2. **Never hand-start `usaged` on port 8765.** It does not merely lose logs — it prevents
   launchd from binding, so nothing persists and every conclusion drawn from the log
   afterwards is worthless. This produced an entirely fictional defect ("the device never
   fetches") that cost an afternoon. Always confirm the listener is launchd's pid.
3. **An absence of evidence in a log proves nothing until you know who is writing that
   log.** Two false defects came from this. `scripts/device-report.sh` now prints the
   device's claimed age beside the server's authoritative `server_age_s` and their
   `drift_s`, and refuses to report success when no comparison is available.
4. **Do not break the redraw-only-on-change contract.** The snapshot body is hashed into
   `rev`; anything that changes per-request must live OUTSIDE that hash (as `age` and
   `device_state` do) or the device redraws constantly. A 304 must stay a 304 with an
   empty body. This is the invariant the whole product rests on.
5. **Every device upload needs Felipe's explicit authorization.** He sometimes grants it
   standing ("flash whenever you want"); that covers the upload, never powering the device
   on. On battery it sleeps behind a 12-hour backstop and only a button press wakes it.

## Accepted residuals / Refuted — don't fix

- **`config.Load` must NOT enforce the non-loopback device-token rule.** It was moved to
  `api.New` deliberately: `Load` is also called by `once`, `stats` and `config`, none of
  which bind a socket, and the check made the skill's own documented fallback fail from
  any shell that had not sourced `.env`. `api.New` rejects both an empty and the published
  placeholder token. Re-adding it to `Load` re-breaks the CLI.
- **`POST /v1/refresh` is deliberately exempt from the loopback token requirement.** Every
  other mutating route requires it. Refresh only triggers an early poll; requiring a token
  broke the dashboard's main button for no security gain.
- **The status line carries no money figure, on purpose.** ChatGPT reports a *count* of
  reset credits, not dollars. Felipe chose to drop the slot rather than show OpenRouter's
  balance there.
- **The IMU is gone.** Double-tap-to-flip was replaced by a button hold; the accel path
  was deleted and `internal_imu = false`. Do not reintroduce it.
- **BtnA (blue) HOLD is reserved and deliberately dead** — it is for a future
  "reach an AI agent" action. Do not bind anything to it. Refresh lives on the side-button
  click.
- **Task 83 (task-hub rows) is deferred by decision, not oversight.** See `BACKLOG.md`
  item 1. Do not start it.
- **PM1 GPIO1 conflicts with SDA** and hangs I2C after wake — a real bug that was hit.
  GPIO0 (charge status, low = charging) is a different pin and is read-only.
- Two "age offset" defects were filed and later **retracted as planner error**; the
  freshness pipeline was correct all along. Do not go looking for them.

## Hard rules & human-reserved decisions

- A task whose verify step names Felipe **does not flip to `passes: true` until he says
  so**. Not when tests pass, not when it looks right on screen. This was violated six
  times by the previous builder and every flip had to be reverted.
- Edit `spec.json` `passes` flags only; do not rewrite task text you did not author.
- **Whether to build the task hub at all IS FELIPE'S CALL.**
- **Whether to publish a firmware build IS FELIPE'S CALL** — and must not happen before
  task 76 removes credentials from the binary.
- **Powering the device on, and any decision to change the button map, IS FELIPE'S CALL.**
- Never print, log or commit credential values. `secrets.h` and `.env` are gitignored;
  reference them by path.

## Acceptance checklist (the author approves the plan ONLY against these)

1. The plan restates all five blocking constraints correctly, in the builder's own words.
2. The plan does **task 75 (the spike) first**, and commits to writing no implementation
   code for tasks 76-79 until Q1-Q5 are answered with measured numbers.
3. The plan says, per spike question, **what will be measured and how the answer will be
   recorded** — not "investigate X".
4. The plan schedules **Q4 (M5Burner credential injection) early**, on the grounds that it
   is the only question that could delete work rather than add it.
5. The plan states how it will avoid the redraw-only-on-change trap when adding any
   device-directed configuration channel (constraint 4).
6. The plan states how firmware will be built and flashed **without assuming the device is
   awake**, and acknowledges that powering the device on is Felipe's action.
7. The plan includes the credential-leak check — a build with no `secrets.h` whose
   `firmware.bin` contains none of the five values — as an explicit deliverable.
8. The plan does not propose starting task 83, reintroducing the IMU, binding BtnA hold,
   or moving the token check back into `config.Load`.
9. The plan names how it will verify host changes are actually live (rebuild + kickstart +
   check the listener) rather than trusting tests.

## next_action

Read `spec.json` task 75 in full, then answer **Q4 first** — determine where M5Burner
writes the Wi-Fi credentials it collects at flash time, and whether an ordinary Arduino
sketch can read them. It is the cheapest of the five questions and the only one whose
answer could remove tasks 77 and 79 entirely.

## Open questions

1. Does M5Burner's Wi-Fi injection write somewhere a non-UIFlow sketch can read? (Q4)
2. If `scanNetworks()` proves unreliable in AP mode, is scan-before-AP with a cached list
   acceptable UX, or does that need Felipe's opinion?
3. If the captive sheet does not auto-pop on his iPhone, do on-screen "open your browser
   and go to …" instructions become the primary path?

## Pointers

- `CLAUDE.md` — status, the hard-won hardware facts, and the do-not-publish warning.
- `spec.json` — the ledger; tasks 75-79 are the provisioning group, fully specced.
- `BACKLOG.md` — what was deliberately not built, with reasoning.
- `AGENTS.md`, `GOLDEN_RULES.md` — house rules and invariants.
- `docs/DEVICES.md` — hardware facts, button map, reserved gestures.
- `docs/SOURCES.md` — every provider endpoint, official or not, and its traps.
- `progress.txt` — the previous builder's running journal.
- Secrets live in `firmware/include/secrets.h` and the repo-root `.env` — both gitignored.
  Never inline their values.


## SUPERSEDED — corrections (2026-09-06, after REVIEW-r1)

Appended, not rewritten. Where this handover disagrees with what you measure, the
measurement wins.

1. **The credential leak is FIVE values, not four.** `OTA_PASS` is also plaintext in
   `firmware.bin` — re-confirmed by `strings`. Task 76's security check must cover
   WIFI_SSID, WIFI_PASS, USAGED_HOST, USAGED_DEVICE_TOKEN **and OTA_PASS**. An
   undercounted check is one that passes while leaking.
2. **The `strings` command path is wrong from the repo root.** `.pio/` lives under
   `firmware/`, so the path is `firmware/.pio/build/m5stack-sticks3/firmware.bin`. Pasted
   as written it returns nothing, which could be misread as "no leak" — the exact
   absence-of-evidence trap in blocking constraint 3.
3. **HEAD was `7c59ad1`** by the time this was delivered; `c1d18a0` predates the handover
   commit itself.
4. **Binary size is 1,051,328 bytes** on disk, not 1,051,312. The ~31.4% headroom claim
   stands.
5. **`GOLDEN_RULES.md` #8 "NO SOLO RESEARCH" was omitted and belongs in the hard rules.**
   Ruling, so it is not re-litigated: a ledger-committed investigative task IS the approval
   #8 requires; answering it from local files is execution, not scope expansion. Anything
   that cannot be answered locally goes to Felipe rather than to the web.
6. **`spec.json` has no `id` field** — task numbers are positional (task 75 = index 74).
7. **Task numbering in `BACKLOG.md` was off by one** and has been corrected: the
   provisioning group is 75-79, where 75 is the spike.
