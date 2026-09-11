# usaged — AI usage monitor (M5StickS3 + Mac agent)

**Status: SHIPPED. v0.3.0 is released; v0.3.1 is pending (the BLE setup fix below). Zero-config Bluetooth provisioning completed on real hardware for the first time on 2026-09-11.**

118 tasks, 113 passing. `spec.json` is both the acceptance ledger and the
executable build order: take the first entry with `passes: false` whose
description is not prefixed `[WITHDRAWN]`, `[DEFERRED]` or `[BLOCKED`. **There is
currently no buildable work** — every open entry is parked or blocked.

The 2026-09-07 prod-readiness group (tasks 85-93) is **done and approved**, and
so is the 2026-09-10/11 run: the "use this next" recommendation ranked by
headroom (96-101), the redesigned dashboard (103-110), working OTA (113), honest
OTA reporting (114-115), the 0.3.0 release (116), the status-line fix (117) and
the BLE setup fix (118).

**Confirmed on hardware:** six providers across four pages from an M5Burner flash
(2026-09-08); the advise overlay agreeing with `/v1/advise` (2026-09-10); and
**zero-config Bluetooth provisioning, end to end, for the first time
(2026-09-11)** — `state: applied` in 21 s, device fetching from the LAN.

### What is left, and none of it is a coding task

| task | state |
|---|---|
| 77 | `[WITHDRAWN]` — SoftAP captive portal, superseded by BLE provisioning (Felipe, 2026-09-10) |
| 83 | `[DEFERRED]` — task-hub rows, parked by his decision |
| 94 | `[BLOCKED]` — OpenCode Zen credit provider; **no balance API exists** (~45 paths probed; the console value comes from a session-authenticated server function, and a cookie scraper is forbidden) |
| 95 | the closing sweep — **Felipe's to close**, never self-certified |

Loose ends outside the ledger:

- **Rotate `OTA_PASS` in `secrets.h`** — leaked into a transcript via
  `espota.py -d`. The **device token** was leaked the same way on 2026-09-11
  (`webui.sh` prints the LAN URL with it); both are LAN-only and touch no
  provider account, but neither string should be trusted. The daemon's
  `device-ota-pass` was already replaced by the self-healing installer.
- **Groq reads `off`** because `GROQ_API_KEY` lives only in `.env` — see the
  Keychain fact below.
- **The `Blocked — frees in` badge renders with no duration** when
  `blocked_for_sec` is 0: `render.js:84` concatenates
  `formatShortDuration(0)`, which returns `""`, while the Go side honestly
  prints "frees in unknown". Not spec'd — Felipe's call.
- **The device may not run what you published.** On 2026-09-11 the stick
  reported `9ca5636+dirty` (a v0.2.x commit, dirty tree) after a v0.3.0 M5Burner
  upload. Read the User-Agent in the access log before assuming.

## Releasing — use the script, never by hand

`sh scripts/release.sh vX.Y.Z` (add `--dry-run` first). It runs the full suite,
builds the firmware, checks it carries no credentials, **refuses to tag when the
git tag and the firmware's own reported version disagree**, packages, publishes,
confirms the install one-liner returns 200, and ends by telling you the firmware
still has to reach M5Burner by hand.

Three facts it encodes, each of which cost a real failure:

- **PUBLISH A MERGED IMAGE, NEVER `firmware.bin`.** `.pio/build/<env>/firmware.bin`
  is the application image and belongs at `0x10000`. M5Burner writes at `0x0`, so
  it boots to `Invalid image block, can't boot. ets_main.c 329` — v0.2.0 shipped
  that and bricked a stick. The published image must merge bootloader `0x0`,
  partitions `0x8000`, `boot_app0` `0xe000`, app `0x10000`.
  **Byte 0 is `0xE9` in BOTH artifacts**, so the magic byte cannot tell them
  apart; the partition table at `0x8000` can — merged reads `aa50`, app-only
  reads `0342`. Sizes: merged ~1,762,528 vs app-only ~1,696,992.
  `firmware.bin` IS correct for ArduinoOTA, which writes the app partition — so
  "it worked over OTA" proves nothing about flashing at `0x0`.
- **The firmware version comes from the git tag**, injected by
  `firmware/scripts/build_id.py` as `USAGED_FW_VERSION`. It used to be a literal
  in `main.cpp`; the stick shipped reporting `fw=1.0.0` while the project was on
  v0.1.9. There is nothing left to bump by hand.
- **A publish-check killed with SIGKILL leaves `secrets.h` stashed** as
  `secrets.h.publishcheck`, which is NOT gitignored. Its trap restores on
  EXIT/INT/TERM only. If a build is interrupted, check that file exists before
  doing anything else.

## The daemon reads keys from the Keychain, NOT `.env`

Measured: the running daemon's process environment holds only `USAGED_LISTEN`
and `USAGED_DEVICE_TOKEN`, the launchd plist declares only those plus `PATH`,
and no Go code parses a dotenv file. Every provider key resolves from the macOS
Keychain (service `usaged`, account = provider id). That is why OpenRouter works
and Groq says "no key" while `GROQ_API_KEY` sits in `.env`. `.env` only matters
for `usaged once` run from a shell that sourced it. Store keys through the
dashboard.

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

### The one thing to understand about tasks 85-95

Every bug shipped on 2026-09-07 had the same root cause:

> **Anything that crossed a boundary out of the process was replaced by a fake
> in tests, so the real path was never executed once.**

The keychain had a `FakeKeyStore` and never stored a key. The dashboard was
grepped for id strings and never parsed, so a blank page shipped. The Device
Wi-Fi card was tested on the Go side while **no firmware side existed at all**.
Nine of them, in one day.

So a fix is not done until the REAL thing has run: the binary, the endpoint, the
log, the wire. Not a fake, not a unit test, not a green build. Every task in
85-95 carries a `Verify:` step naming the live command and its expected output
for exactly that reason — a green `make verify` never satisfies one on its own.

## `make verify` now guards three surfaces, not one

`verify: fmt vet lint test test-js test-statusline test-install build`. The two
shell suites exist because both surfaces shipped broken while every Go test
stayed green:

- **`scripts/statusline.test.sh`** — the status line defaulted to the
  pre-rename state path (`~/.local/state/usaged/`) and told Felipe the daemon was
  dead on every prompt for days. It also caught a second defect: tab is an IFS
  *whitespace* character, so an empty field from `jq` collapsed and every value
  in the bar shifted one slot left.
- **`scripts/install-release.test.sh`** — runs the installer's real generator
  lines and measures them against the wire limits.

Both were confirmed to FAIL against the pre-fix code before being trusted. A test
that has never failed has proved nothing.

## Read these first, in this order

| file | what it is |
|---|---|
| `AGENTS.md` | the house rules for anyone changing this repo |
| `GOLDEN_RULES.md` | the invariants that must not be broken |
| `spec.json` | the task ledger — the authoritative record of what was built and why |
| `BACKLOG.md` | what was deliberately not built |
| `docs/DEVICES.md` | hardware facts, the button map, and the reserved gestures |
| `docs/SOURCES.md` | every provider endpoint, official or not, and its traps |

## BLE provisioning: what 2026-09-11 cost, and the rule it produced

Zero-config setup **completed on real hardware for the first time on 2026-09-11**
(`state: applied`, 21 s, device on the LAN at .195). Getting there burned an hour
on a one-character bug, and the way it hid is more important than the bug.

**THE FAILURE NAMED ITSELF NOWHERE.** Three layers each dropped the reason:

- the dashboard rendered *"Setup did not finish. Press the blue button"* — about
  a device that was never contacted;
- the log recorded `stage:"" device_code:0`, which is the shape of "we have no
  idea";
- and `cmd/usaged/bleprov.go` **discarded the error value one line before it
  could be printed**, because this package deliberately never logs a
  provisioner's error.

The cause was `install-release.sh` minting a 64-character OTA password against a
63-byte limit (`bleprov.MaxOtaPass`, `kMaxOtaPass`): `od -tx1` renders 24 bytes
as **48 hex characters**, and `base64` then encoded that *text*, not the bytes.
`Encode()` refused the record locally — no radio traffic at all, which is why
every run died in ~0.4 s with `steps: []`.

**`USAGED_BLE_DEBUG=1` is the only reason this was ever found.** It logs the raw
provisioning error (a CoreBluetooth transport error carries no credential). Set
it with `launchctl setenv USAGED_BLE_DEBUG 1` and kickstart. **Use it first, not
last, on any setup failure** — every minute spent theorising before reading that
line was wasted, and two of the theories (a stale macOS bond, the 10-minute
advertising window) were confidently wrong.

Facts worth keeping from the same session:

- **The BLE window is 10 minutes and does not reopen.** `main.cpp:218`
  `kBleWindowMs`; after it, `bleProvEnd(true)` releases the BT controller memory
  irreversibly — *"once BT memory is released, the process cannot be reversed"* —
  and the portal takes over. **Only a power cycle brings BLE back.** A scan
  finding nothing usually means the window closed, not that anything is broken.
- **Reflashing a device invalidates the macOS bond.** The stick comes back with
  fresh pairing keys while macOS keeps the old bond; `central.go` catches one
  variant (`Connect` returning a zero device → `ErrBondLost`) but not all. If
  pairing misbehaves after a flash, forget the device in System Settings first.
- **Burning the merged image erases NVS.** `0x9000-0xe000` is inside the merged
  artifact as `0xFF`, so a burn wipes the stored credentials and the device comes
  back unprovisioned. That is why a flash forces a re-pair.
- **A device's reported build is not the release you tagged.** The stick that
  paired reported `9ca5636+dirty` — a v0.2.x commit built from a dirty tree —
  even though v0.3.0 had been uploaded to M5Burner. Read the User-Agent in the
  access log before believing a device runs what you published.

**The rule:** a credential this project generates must be checked against the
wire limit *by a test*, not by a comment. The broken generator's own comment said
"~32 chars" while it produced 64. `scripts/install-release.test.sh` now runs the
real generator lines out of the installer and measures them, and
`make verify` runs it.

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

## Publishing a firmware build

**`secrets.h` is a developer seed, never shipped.** It is a compile-time
header, so the Wi-Fi SSID, the **Wi-Fi password**, the device token, the OTA
password and the host address all end up as plaintext strings in
`firmware.bin` — verified with `strings` against a real build. Any image built
with a local `secrets.h` must never be published; it would hand out the network
password of whoever built it, and a stranger's flash could not work anyway,
since it would try to join our SSID and reach our Mac at a fixed address.

**The published image is built without `secrets.h` — but for two releases it was
NOT, and that shipped a Wi-Fi password.** On 2026-09-11 the v0.3.0 and v0.3.1
firmware assets were found to contain one occurrence each of all five values.
`fw-publish-check` moved `secrets.h` aside, proved a build was clean, printed
`PUBLISHABLE` and then **restored the header**; step 5 rebuilt with it present to
stamp the version and merged THAT binary into the asset. The check was real and
pointed at a file nobody shipped. `release.sh` now moves `secrets.h` aside for
the build that becomes the asset AND re-runs `check_no_secrets.sh` against the
merged artifact itself. **Verify the bytes you are about to upload, never a
sibling build — and after publishing, download the asset and check it again.** Runtime provisioning —
the NVS credential store and state machine (`firmware/src/usage/provision.cpp`),
the captive portal (`portal.cpp`) and the BLE protocol (`bleprov.cpp`) — lets
a device collect its own credentials on first boot. `make fw-publish-check`
moves `secrets.h` aside, rebuilds the firmware (proving the tree compiles with
no credentials), then asserts none of the five values appear in the binary:

    PUBLISHABLE: built with no secrets.h and none of the five values are present.

`scripts/release.sh` enforces this in step 4/8 and refuses to proceed if
`fw-publish-check` does not say `PUBLISHABLE`. `BACKLOG.md` item 3 proposed the
design and is now built.

**Always merge the image.** The artifact flashed at `0x0` (by M5Burner or
`flash_when_awake.sh`) must merge bootloader `0x0`, partitions `0x8000`,
`boot_app0` at `0xe000`, app `0x10000`. M5Burner writes at `0x0`, so an
app-only `firmware.bin` boots to `Invalid image block, can't boot.
ets_main.c 329` — v0.2.0 shipped that and bricked a stick. Both an app-only
image and a merged one start with `0xE9`, so the magic byte cannot tell them
apart; the partition table at `0x8000` can — merged reads `aa50`, app-only
reads `0342`. `release.sh` enforces this:

    PT_MAGIC=$(xxd -s 0x8000 -l 2 -p "$ASSET")
    [ "$PT_MAGIC" = "aa50" ] || die "the asset has no partition table at 0x8000 (got $PT_MAGIC) — it is not bootable at 0x0"

**`secrets.h` must exist before building.** It is gitignored; copy it from
`firmware/include/secrets.h.example`, which carries only macro names, not
values. If a publish-check is interrupted, confirm `secrets.h` exists — its
trap restores on EXIT/INT/TERM but not SIGKILL, and the stashed copy
(`secrets.h.publishcheck`) is not gitignored.
