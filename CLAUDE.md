# usaged — AI usage monitor (M5StickS3 + Mac agent)

**Status: ACTIVE. Start at `spec.json` task 85 — the prod-readiness group.**

92 tasks, 80 passing. `spec.json` is both the acceptance ledger and the
executable build order: take the first entry with `passes: false`.

The device works. A stranger can flash from M5Burner, run one curl command and
click once to set it up — proven end to end on hardware, 2026-09-07. What is not
ready is everything around it: an audit found twenty confirmed findings and the
dashboard still renders controls wired to nothing. **The open work is tasks
85-92: fix those until nothing on the page lies.** Every one of them comes from
`docs/handovers/2026-09-07-prod-readiness/` — read `AUDIT.md` there for the
reproduction command behind each.

Outside that group: task 77 (SoftAP captive portal) is still open, and task 83
(task-hub rows) is deferred by decision — leave it. Tasks 78 and 79 are being
WITHDRAWN by 87, 90 and 91 rather than built, on Felipe's 2026-09-07 call.

### You are both planner and builder

Earlier sessions split these: a planner steered a separate builder agent through
a Solvr room, with Felipe pasting messages between them. **That is over.** There
is no room to read and no relay. You spike, you implement, you verify, you
report to Felipe directly.

Two habits from that arrangement are worth keeping, because they caught real
defects:

- **Verify against the running system, never the report.** Rebuild AND
  `launchctl kickstart` before believing a host change is live; check the wire
  before believing a firmware change works. Every serious bug on 2026-09-06 was
  found this way and none were found by tests.
- **A task whose verify step names Felipe does not flip to `passes: true` until
  he says so.** Not when the tests pass, not when it looks right on your screen.

### The one thing to understand about tasks 85-92

Every bug shipped on 2026-09-07 had the same root cause:

> **Anything that crossed a boundary out of the process was replaced by a fake
> in tests, so the real path was never executed once.**

The keychain had a `FakeKeyStore` and never stored a key. The dashboard was
grepped for id strings and never parsed, so a blank page shipped. The Device
Wi-Fi card was tested on the Go side while **no firmware side existed at all**.
Nine of them, in one day.

So a fix is not done until the REAL thing has run: the binary, the endpoint, the
log, the wire. Not a fake, not a unit test, not a green build. Every task in
85-92 carries a `Verify:` step naming the live command and its expected output
for exactly that reason — a green `make verify` never satisfies one on its own.

## Read these first, in this order

| file | what it is |
|---|---|
| `AGENTS.md` | the house rules for anyone changing this repo |
| `GOLDEN_RULES.md` | the invariants that must not be broken |
| `spec.json` | the task ledger — the authoritative record of what was built and why |
| `BACKLOG.md` | what was deliberately not built |
| `docs/DEVICES.md` | hardware facts, the button map, and the reserved gestures |
| `docs/SOURCES.md` | every provider endpoint, official or not, and its traps |

## Hard-won facts that cost a day each to find

Every one of these passed its tests and was still wrong on the wire. **When a
change talks to hardware or another process, the only test that counts is the
one that exercises the real path.**

- **`HTTPClient::addHeader("User-Agent", ...)` is silently ignored** by the
  Arduino ESP32 core (`HTTPClient.cpp:1040-1046`, which skips Connection,
  User-Agent, Host and Authorization). Use `setUserAgent()`. The device went
  unidentifiable for hours while a green Go test asserted the opposite, because
  `curl` can set that header and the device cannot.
- **`esp_timer_get_time()` does NOT survive deep sleep on this board.** Use
  `gettimeofday()`, which ESP-IDF maintains across sleep via the RTC. The wrong
  primitive underflows a `uint32_t` subtraction into ~49 days of apparent age.
- **M5Unified's StickS3 init never calls `setBatteryCharge(true)`**, so the PM1
  `CHG_EN` bit stays clear and **the board does not charge from USB at all**.
  `board.cpp` now enables it explicitly. `setChargeCurrent`/`setChargeVoltage`
  are stubs on the M5PM1; the real current comes from the CHG_PROG resistor.
  Charge state is a single PM1 GPIO0 read — low means charging.
- **ArduinoOTA listens on UDP 3232.** A TCP readiness probe can never succeed
  and will refuse to flash a device that is perfectly ready.
- **PM1 GPIO1 conflicts with SDA** and hangs I2C after wake. GPIO0 is a
  different pin and is read-only here.

## Two operational rules, both learned the hard way

**Never hand-start `usaged` on port 8765.** It does not merely lose logs — it
prevents launchd from binding, so nothing persists across a reboot and every
conclusion drawn from the log afterwards is worthless. This produced an
entirely fictional defect ("the device never fetches") that cost an afternoon.
Use `make build` then
`launchctl kickstart -k gui/$(id -u)/com.fcavalcanti.usaged`, and confirm with
`lsof -nP -iTCP:8765 -sTCP:LISTEN` that the listener is launchd's pid.

**An absence of evidence in a log proves nothing until you know who is writing
that log.** Twice, conclusions were drawn from an empty or mis-baselined log.
`scripts/device-report.sh` now prints the device's claimed age beside the
server's authoritative `server_age_s` and their `drift_s`, and explicitly
refuses to report success when no comparison is available — silence is not a
pass.

## Flashing

`sh firmware/scripts/flash_when_awake.sh` builds now (no device needed) and
uploads the moment the device wakes. On battery the awake window is ~19 s and a
build takes ~25 s, so the phases must be split. Uploads take ~31 s and that is
fine: once a transfer starts, `main.cpp` skips the sleep decision while
`otaInProgress()`, so only the invitation must land inside the window.

**Every device upload needs Felipe's explicit authorization.** He gives standing
permission sometimes ("flash whenever you want"); that covers the upload, never
powering the device on. On battery it sleeps behind a 12 h backstop, so ask him
to press the blue button and the script will catch the wake.

## Where things stand physically

- **Device:** flashed with `cd50477`, on battery, charging correctly (task 71 —
  it never charged before that fix). Sleeps until a button press.
- **Agent:** running under launchd, healthy, serving the dashboard on
  `127.0.0.1:8765` and the status line.
- **Build lane:** `git worktree` a clean checkout if the working tree has
  in-progress edits — a stale copy once caused a silent build of the WRONG
  commit, caught only because `OTA_BUILD_ID` is printed. Always check it.
- **Evidence:** `sh scripts/device-report.sh` prints the device's claimed data
  age beside the server's authoritative `server_age_s` and their `drift_s`. Use
  it instead of reconstructing a baseline by hand — doing that by hand produced
  two false defects in one afternoon.

## Do not publish a firmware build

`secrets.h` is a compile-time header, so the Wi-Fi SSID, the **Wi-Fi
password**, the device token and the host address end up as plaintext strings
in `firmware.bin` — verified with `strings` against a real build. Publishing to
M5Burner or anywhere else would hand out the network password of whoever built
it, and a stranger's flash could not work anyway, since it would try to join
our SSID and reach our Mac at a fixed address. `BACKLOG.md` item 3 is the fix
and is the top priority if this project resumes.
