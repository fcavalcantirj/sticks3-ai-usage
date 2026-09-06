# usaged — AI usage monitor (M5StickS3 + Mac agent)

**Status: closed 2026-09-06 at 78 of 79 ledger tasks passing.** The remaining
task is deferred by decision — see `BACKLOG.md`. Do not treat it as unfinished
work to pick up unasked.

A Go agent (`usaged`) polls Claude, ChatGPT/Codex, OpenRouter and Groq every
15 minutes, serves a dashboard on `127.0.0.1:8765`, and pushes a snapshot to an
M5StickS3 that **redraws only when the data actually changes**.

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

**Every device upload needs Felipe's explicit authorization.**

## Do not publish a firmware build

`secrets.h` is a compile-time header, so the Wi-Fi SSID, the **Wi-Fi
password**, the device token and the host address end up as plaintext strings
in `firmware.bin` — verified with `strings` against a real build. Publishing to
M5Burner or anywhere else would hand out the network password of whoever built
it, and a stranger's flash could not work anyway, since it would try to join
our SSID and reach our Mac at a fixed address. `BACKLOG.md` item 3 is the fix
and is the top priority if this project resumes.
