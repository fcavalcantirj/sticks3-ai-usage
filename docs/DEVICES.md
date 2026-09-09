# Devices

Physical hardware inventory for the StickS3 AI-usage monitor.

## Unit #2 — `sticks3-usage` (primary)

| Field        | Value                              |
|--------------|------------------------------------|
| MAC          | `14:c1:9f:d4:d5:34`                |
| Chip         | ESP32-S3-PICO-1-N8R8 rev v0.2      |
| USB mode     | USB-Serial/JTAG (native CDC)       |
| Hostname     | `sticks3-usage`                    |
| First light  | 2026-09-03                         |
| Serial port  | `/dev/cu.usbmodem101`              |

### First-light serial transcript

```
[BOOT] board=26 psram=8386231 build=01f4ee7+dirty fw=1.0.0
[NET] state=connecting
[NET] state=connected ip=192.168.0.136
[FETCH] code=200 rev=bf7b04c8 seq=0 ms=143
[RENDER] page=1 lines=5 rev=bf7b04c8
[HEAP] free=281168 min=275316
```

The screen shows the `AI USAGE` boot banner, then the page-1 usage card
(five rows: CLAUDE 5h / 7d / FABLE 7d / GPT 5h / 7d). BtnA cycles to
page 2 (OpenRouter main + OpenRouter fallback + Groq).

### Build flag note (ORDER #19)

`-DARDUINO_USB_CDC_ON_BOOT=1` is required in `firmware/platformio.ini`
`build_flags`. Without it, `ARDUINO_USB_MODE=1` (set by the devkitc board)
makes `Serial` use UART0, which is invisible over the native USB-CDC port.
With this flag, `Serial.begin(115200)` publishes to the USB-CDC interface
and the monitor shows `[BOOT]`, `[NET]`, `[FETCH]`, `[RENDER]`, `[HEAP]`
lines.

### Power / wake errata (ORDER #29)

**USB-insert ext0 wake is disabled.** Driving PM1 GPIO1 as a push-pull IRQ
output to signal 5VIN insertion to ESP32 GPIO13 causes the line to conflict
with the PM1 I2C SDA pin. After deep-sleep wake, the first `getVBUSVoltage()`
(I2C read of PM1 regs 0x24/0x25) hangs because SDA is held by the IRQ driver,
freezing the device. The firmware never writes PM1 GPIO1 IRQ registers in
the sleep path.

**Current wake design (ORDER #60, reversal of ORDER #29):** ext1 buttons
(GPIO11/12, pullup+pulldown pair) are the **primary** wake. A 12-hour timer
backstop acts as a safety net only. On USB the device never sleeps
(`vbusPresent()` via `VbusDebouncer`). **USB insertion does NOT wake a sleeping
device for up to 12 hours** — one button press after plugging in restores
normal always-on behaviour. This is deliberate (the same trade ptt.ino makes)
but a sleeping device will look dead to anyone who does not know, so it is
written here where they would look.

**REVERSAL rationale (ORDER #60):** a 12-minute reachability watch measured ten
clean cycles of ~20 s awake on an 81 s period — a 25% duty cycle with the radio
on, roughly 27 mA average against the 250 mAh cell, giving about 9 hours of
battery. The 60 s backstop from ORDER #29 was 720x more wakeful than necessary;
the button is always there to wake the device, so a 12 h timer restores the
weeks-of-standby the ptt firmware measured (19 h of real use at ~50% battery).

**Instant-wake guard (monitor-only):** `g_powerGuard.ext0InstantWakeCount`
in `main.cpp` counts consecutive ext0 wakes with vbus < 4000. Ext0 is not
armed, so the counter stays 0 in normal operation — it is retained for the
follow-up experiment that re-enables ext0 via PM1 I2C IRQ register reads.

**BMI270 suspend:** the teardown writes 0x7D←0x00 and 0x7C←0x03 to the BMI270 at
I2C 0x68 (`suspendImu` in power.cpp), matching ptt.ino:205-207. `internal_imu`
is false so the sensor is never initialised, but it powers on in normal mode by
default (~1 mA). At a 12 h backstop that leak is the difference between weeks
and days of standby.

**Follow-up experiment (not yet implemented):** test whether the PM1
already asserts its IRQ line on 5VIN insertion with its default configuration.
If so, `esp_sleep_enable_ext0_wakeup(GPIO_NUM_13, 0)` with the standard
pullup/pulldown pair may work without writing any PM1 IRQ registers. This
must be validated on hardware before any code change.

**Instant-wake guard (monitor-only):** `g_powerGuard.ext0InstantWakeCount`
in `main.cpp` counts consecutive ext0 wakes with vbus < 4000. Ext0 is not
armed, so the counter stays 0 in normal operation — it is retained for the
follow-up experiment that re-enables ext0 via PM1 I2C IRQ register reads.

**Follow-up experiment (not yet implemented):** test whether the PM1
already asserts its IRQ line on 5VIN insertion with its default configuration.
If so, `esp_sleep_enable_ext0_wakeup(GPIO_NUM_13, 0)` with the standard
pullup/pulldown pair may work without writing any PM1 IRQ registers. This
must be validated on hardware before any code change.

### Button map (ORDER #53 REVISED, task 58 + ORDER #72, task 72)

The BMI270 IMU is **not used**. `internal_imu` is `false` in `boardInit()`
and `M5.Imu.begin()` is never called. The device has no vibration motor, so
haptic feedback is not an option.

All button logic lives in `main.cpp:buttonsUpdate()`. The binding table is
single-sourced from `render_plan.cpp:kBindings` — the instructions page
(task 73) renders it verbatim, never hand-written literals.

#### BtnA (GPIO 11, the blue button)

| Gesture      | Action              | Mode |
|--------------|---------------------|------|
| single-click | cycle pages         | both |
| double-click | enter/exit brightness mode | normal → brightness |
| **HOLD** (≥1500 ms) | fetch /v1/advise, show use-this-next overlay | normal |

**BtnA HOLD (≥1500 ms)** fetches `/v1/advise` and renders a transient
overlay showing the winner, reason, and ranked recommendations. Any button
click or hold dismisses it, restoring the underlying page. It is NOT a new
page in the cycle — single-click paging behaves exactly as before.

The hold threshold was **600 ms → 1500 ms** (matching BtnB). The old 600 ms
was too short: the click detector consumed the event before
`M5.BtnA.wasHold()` could fire (the 600 ms click window swallowed the hold),
so Felipe reported blue-hold "does nothing." The fix lives in
`board.cpp:boardInit()` and uses `HoldFlipDetector::kHoldThresholdMs`.
ORDER #72 (task 72) had reserved this gesture for "a future AI-agent action"
— this is that action, and the reservation note in `main.cpp` is updated to
reflect the binding rather than leaving it claiming the hold is unbound.

#### BtnB (GPIO 12, the side button)

| Gesture      | Action              | Mode |
|--------------|---------------------|------|
| click        | refresh / step down | normal / brightness |
| hold (≥1500 ms) | flip 180°        | both |
| hint (after 500 ms into a hold) | amber "hold to flip 180°" | both |

The click-vs-hold disambiguation is a pure C++17 state machine in
`usage/hold_flip.{h,cpp}`, host-tested by `test/host/test_hold_flip.cpp`
— no sensor polling, no hardware dependency.
`M5.BtnB.setHoldThresh(1500)` documents the threshold even though the state
machine handles the timing via `isPressed()` polling.

Rotation is persisted in NVS (namespace `"usaged"`, key `"rot"`) as values
1 (upright) or 3 (flipped). It is loaded before the first paint so a flipped
device never shows one upside-down frame. It survives reboot, deep sleep,
and OTA.

**SDA conflict (ORDER #29, reiterated):** Never write PM1 GPIO1 IRQ registers.
The PM1 GPIO1 line shares the I2C SDA bus and driving it as an IRQ output
causes a bus hang after every wake.

A StickS3 can sit latched in ROM download-wait: the flash reports success
but the app never runs and serial stays silent. Recovery:

1. Unplug USB.
2. Double-click the side button (full off).
3. Single press (on).
4. Replug USB.
5. Re-open the serial monitor.

**No vibration motor** — the StickS3 PCB has no haptic actuator, so
hold-to-flip feedback is visual only (the 500 ms hint line).

## Provisioning spike (task 75)

Measured answers to the five unknowns that tasks 76-79 rest on. Everything here is a
number or a quoted source line, never an expectation — the group exists because four
defects on 2026-09-06 were documented behaviour that was wrong on the hardware.

### Q1 — does the portal stack fit?

**Answer: YES, with room to spare. Flash +28,736 bytes (+0.9 pp), static RAM +536 bytes.**
[REAL] measured 2026-09-06, no hardware required.

**Measure the DELTA, not a standalone portal build.** A spike that builds the portal
*instead of* the firmware links to 1,012,561 bytes — smaller than production — and proves
nothing about headroom. Q1 asks what the portal costs *added to* what already ships. So
`[env:spike-delta]` builds every production source PLUS one throwaway translation unit
that instantiates `WebServer` + `DNSServer` and touches `softAP`/`scanNetworks`/wildcard
`start`/`onNotFound`/`sendHeader`/`send` from a global constructor, so nothing is
dead-stripped.

| build | Flash | RAM |
|---|---|---|
| production (`m5stack-sticks3`) | 1,049,165 B — 31.4% | 54,280 B — 16.6% |
| production + portal stack | 1,077,901 B — 32.2% | 54,816 B — 16.7% |
| **delta** | **+28,736 B (+0.9 pp)** | **+536 B (+0.1 pp)** |

That leaves **2,264,435 bytes (67.8%) of the `0x330000` app partition free**. Flash is not
a constraint on task 77 and the question can be closed.

**Two numbers, both correct, easily confused.** PlatformIO's `Flash:` line reports
`program_size`, a sum of ELF sections (1,049,165). `firmware.bin` on disk is 1,051,328 —
2,163 bytes larger. Compute deltas from one source or invent phantom drift.

**The RAM figure above is static `.data`/`.bss` only.** The portal's real cost is runtime
heap: roughly 4-6 KB while one client is mid-request (a 1,436 B `WiFiClientRxBuffer` per
connection, a 1,460 B UDP tx buffer for the life of the DNS server, ~275 B of DNSServer
structs, plus per-request `RequestArgument[]` and response Strings). All of it lands in
internal DRAM, not PSRAM — this build sets `CONFIG_SPIRAM_MALLOC_ALWAYSINTERNAL=4096` and
every one of those allocations is at or below 4096 B. That half of Q1 is a hardware
measurement.

**Build it only as `cd firmware && pio run -e spike`.** Never `make fw-ota PIO_ENV=spike`:
make exports command-line variables into recipe environments and `upload_ota.sh` reads
`${PIO_ENV:-m5stack-sticks3}`, so that flashes the *spike* onto unit #2. The MAC guard
checks the target, not the payload.

**Editing `platformio.ini` deletes the whole `firmware/.pio/build/` tree** on the next
run — not just one env's subdirectory — because the cleaner is handed the parent build dir
and the checksum hashes the entire config. Verified: adding the second env wiped the
already-built `spike/`. Do ini edits before a flash session, never inside the ~19 s
battery wake window.

### Hardware results — measured on unit #2, 2026-09-06

[REAL] Spike firmware flashed over OTA (build `da0ff19+dirty`), device on USB so it never
slept. Every number below is off the wire, from the device's own serial output.

**Q1 runtime heap — the portal costs ~53 KB, and the floor is nowhere near a limit.**
Measured inside ONE firmware, which is the honest comparison:

| moment | free heap |
|---|---|
| at boot, before the portal starts | 315,452 B |
| SoftAP + DNSServer + WebServer all serving | ~262,700 B |
| **cost of the portal stack** | **~53 KB** |
| minimum observed, with a client mid-request | **249,028 B** |

That floor is roughly 3x the ~80 KB level that would have reshaped task 77. Combined with
the +28,736 B flash delta, **Q1 is closed on both axes.**

**Q2 — live scanning in `WIFI_AP_STA` is RELIABLE. Task 77 keeps the live scan.**
50+ scans across two firmware builds, finding 21-33 networks each.

- **Across every client-attached window (Android and iOS): 0 empty, 0 failed, 0
  disassociations.** Scans kept finding 27-33 networks with the phone associated
  throughout.
- Across ~90 scans on two boots: **1 empty (1.1%)** and **5 `WIFI_SCAN_FAILED` (5.6%)**.
  Every one of those failures fell OUTSIDE a client-attached window, clustered around boot
  and around AP reconfiguration — the `ESP_ERR_WIFI_STATE` scan/connect race the IDF header
  documents, not a scanning-under-AP problem.
- Every client drop logged followed `[AP] client left`, i.e. the phone leaving on purpose.

Pre-declared threshold was ">10% empty OR any client disassociation = flaky, fall back to
scan-before-AP". Result: **0% and zero.** The scan-before-AP fallback is NOT needed.

Side effect worth knowing: while scanning continuously in AP_STA, station-side ping RTT
spikes to 1-2 s and drops packets. Harmless for a brief portal phase, but it makes the
device look briefly unreachable, so do not diagnose that as a fault.

**Q3 — the captive sheet auto-pops on BOTH platforms. Confirmed on the wire.**

Android:

```
21:10:43  [AP] client joined
21:10:46  [PROBE] android host=connectivitycheck.gstatic.com   <- OS probe, unprompted
21:10:47  [HTTP] path=/ host=192.168.4.1                        <- portal page served
```

iOS:

```
21:21:56  [AP] client joined
21:22:02  [PROBE] apple host=captive.apple.com                  <- OS probe, unprompted
21:22:02  [HTTP] path=/hotspot-detect.html host=captive.apple.com
```

Felipe independently confirmed the sheet appeared by itself on both handsets, and the wire
agrees: 6 Android probes and 4 Apple probes, each arriving 3-6 s after association with no
browser opened. The probe reaching us at all proves the wildcard DNS hijack worked on both
platforms — `connectivitycheck.gstatic.com` and `captive.apple.com` each resolved to
192.168.4.1. The `ARCount != 0` failure mode predicted from the DNSServer source did NOT
bite either handset.

Both sessions also completed a real form submission (`[SAVE] ssid_len=14 pass_len=12`),
and in both cases the client left on purpose rather than being dropped.

So task 77's captive portal can rely on the sheet as the PRIMARY path on current iOS and
Android. Keep on-screen instructions as a genuine fallback — one handset each is not a
survey, and this failure mode is famously version-dependent — but they need not be the
main route.

**Q5 — no reboot required, and NVS survives one anyway.**

Live transition, sentinel written immediately before it:

```
[Q5] live: wrote sentinel=62169 mode=3 apClients=0
[Q5] live: mode(WIFI_STA)=1 newMode=1 apClients=0 staStatus=3 readback=62169 ip=192.168.0.136
```

Mode 3 (`AP_STA`) to mode 1 (`STA`) live, returning true, **with the station connection
untouched** — `staStatus=3` is `WL_CONNECTED` and the IP did not change.

Write-then-immediate-restart, with no clean shutdown in between:

```
[Q5] reboot: writing sentinel=85739 then restarting NOW
[BOOT] SPIKE task75 build=da0ff19+dirty heap=315452
[Q5] boot: sentinel readback=85739 reset_reason=3
```

Identical readback, `reset_reason=3` (software restart). **Cold boot to fully operational —
station joined, OTA armed, AP up, DNS and web serving — took 3 seconds.** So a reboot is
affordable during setup, and the live switch means it is not even necessary. Credentials
written just before either transition are reliably there afterwards.

**Task 77 MUST distinguish `WIFI_SCAN_FAILED` (-2) from a genuinely empty scan (0).**
They are different events and were both observed: -2 means "the radio refused, retry",
0 means "there is really nothing here, tell the user about 2.4 GHz". Rendering a -2 as
"no networks found" would be actively misleading, and -2 happens often enough (5.6%) to
matter. Retry on -2; only show the empty-list explanation on a true 0.

**A task-77 defect found by accident:** every portal page load triggers `/favicon.ico`,
which with no handler logs a core error and costs an extra TCP round through a server that
takes ONE client at a time. Register a handler for it.

### Q2 / Q3 / Q5 — what the on-disk core source settles before hardware

[REAL] read 2026-09-06 from `~/.platformio/packages/framework-arduinoespressif32`
(Arduino 2.0.17 / IDF 4.4.7). These do not replace the hardware measurements, but each one
either reshapes a task or predicts a specific failure, so they are recorded now.

**Q5 — AP to STA needs NO reboot (Arduino half source-verified).** From `WIFI_AP_STA`,
`WiFi.mode(WIFI_STA)` runs neither the low-level-init branch nor the stop branch: it sets
the STA hostname, calls `esp_wifi_set_mode`, then an already-satisfied `espWiFiStart()`.
The AP teardown happens inside the closed `esp_wifi` blob; the Arduino layer only observes
it via `ARDUINO_EVENT_WIFI_AP_STOP` clearing `AP_STARTED_BIT|AP_HAS_CLIENT_BIT`. So "no
reboot required" is verified; "clients are cleanly deauthed" is inferred and stays a
hardware check.

**Q2 — AP_STA IS SINGLE-CHANNEL, and this reshapes task 77.** `esp_wifi.h:812-813` states
it outright: *"ESP32 is limited to only one channel, so when in the soft-AP+station mode,
the soft-AP will adjust its channel automatically to be the same as the channel of the
ESP32 station."* The `channel` argument to `softAP()` is therefore advisory in AP_STA —
**the moment the device associates to the target network, the portal AP hops channels and
every associated phone is dropped.** Any UX that plans to show a "connected!" page over the
portal AP *after* the join is designed against the hardware. Task 77 must show the outcome
on the DEVICE SCREEN, not in the browser.

**Q2 — a scan issued while the STA is connecting fails silently.** `esp_wifi.h:356-358`:
scanning and connecting at once aborts the scan with `ESP_ERR_WIFI_STATE`. Arduino discards
that and returns only `WIFI_SCAN_FAILED` (-2). Worse, `setAutoReconnect(false)` does **not**
stop the first retry: a function-static `first_connect` forces one reconnect for any
disconnect reason, and that retry is `WiFi.disconnect(); WiFi.begin();` re-applying the
STORED credentials. So a user's mistyped password produces a background reconnect that
collides with the next portal scan — and the reason code is printed with `log_w`, which is
compiled OUT at this project's `CORE_DEBUG_LEVEL=1`. Register a `WiFi.onEvent` handler and
read `info.wifi_sta_disconnected.reason` yourself.

**Q2 — the sync `scanNetworks()` blocks on a HARDCODED 10 s**, not on `max_ms_per_chan`
(that value only feeds `scanComplete()`'s timeout in the async path). Against a ~19 s
battery wake window, the async form plus `scanComplete()` polling is the only safe shape.
On timeout it returns -2 while the driver keeps scanning, and the next call then returns
-1 — two sentinels for one stuck scan.

**Q3 — `DNSServer` refuses any query with `ARCount != 0`, and this is the most likely way
the captive sheet fails.** `requestIncludesOnlyOneQuestion()` demands `QDCount==1` **and**
`ANCount==0` **and** `NSCount==0` **and** `ARCount==0`. Any resolver attaching an EDNS0 OPT
pseudo-record to the additional section gets NXDOMAIN instead of the hijack address. It is
not configurable — fixing it means vendoring `DNSServer.cpp`. If the sheet does not pop,
capture the phone's actual query and read `ARCount` before suspecting anything else.

**Q3 — RFC 8910 (DHCP option 114, the modern captive-portal URI) is NOT available.** The
IDF 4.4.7 dhcpserver option enum has no entry 114, and `dhcps_options_t` exposes only
offer/dns/time/poll, so no arbitrary option can be emitted. DNS hijack plus OS probe-URL
interception is the only mechanism on this stack.

**Q3 — whether the phone is even told to use us as DNS is config-level, not code-level.**
Arduino never sets the softAP's DNS DHCP option. Option 6 rides entirely on
`CONFIG_LWIP_DHCPS_ADD_DNS=y`, whose emitting code is precompiled. If DNS never reaches
port 53 the wildcard server is inert and it will look like a WebServer bug. Confirm the
DHCP ACK carries option 6 before debugging anything above it.

**Handler order and probe answers.** `WebServer` picks the FIRST matching handler in
registration order, from the request line, BEFORE any header is read — so probe paths must
be registered before the catch-all, and routing on the `Host` header is impossible
(`hostHeader()` is readable inside a handler, and is never cleared between requests, so a
probe without a Host header returns the previous request's value). For Android do NOT
`send(204, ...)`: the header builder still emits Content-Type and `Content-Length: 0`,
i.e. exactly the "no portal here" answer. Send a 302 to the AP IP instead.

**Never call `onFileUpload()` on a portal server.** It sets `_ufn`, and the handler then
claims ANY non-GET method regardless of URI, routing the whole POST body down the raw path
so `server.arg("ssid")` is empty for every form submission.

**Keep the portal to ONE self-contained document.** Every response carries
`Connection: close` unconditionally and the server takes one client at a time with a listen
backlog of 4, so each external asset is another full TCP round through a single slot. A
client that connects and sends nothing pins the server for up to 5 s, during which DNS goes
unanswered — precisely while the phone is deciding whether a portal exists.

**Read the AP IP back; do not hardcode 192.168.4.1.** That default lives in precompiled
`esp_netif` code, not in any on-disk header. Either call `softAPConfig()` (and check its
return — it hard-fails outside /24../28, and on an AP IP or gateway inside the 11-address
lease pool) or use `WiFi.softAPIP()` for both `dns.start()` and every `Location` header.

**`WiFi.softAPmacAddress(uint8_t*)` returns your buffer UNMODIFIED** when the mode is
`WIFI_MODE_NULL` — no fallback, no error, no zeroing. A MAC-derived AP SSID built with it
before the radio is up is uninitialized stack. `WiFi.macAddress(uint8_t*)` has an
`esp_read_mac` fallback and is the one to use. The two look symmetric and are not.

**`WiFi.persistent(false)` must precede the FIRST radio call of the process** — it is read
once, behind a one-shot guard, and `softAP()`/`begin()`/`scanNetworks()` all reach that
path. Called later it does nothing and credentials are written to NVS anyway.

### Q4 — can M5Burner's Wi-Fi step do the provisioning for us?

**Answer: NO. Tasks 77 and 79 survive.** [REAL] 2026-09-06.

Read directly from the installed app at
`/Applications/M5Burner.app/Contents/Resources/app.asar` (an Electron bundle; `strings`
and an asar header walk are enough — no web research, GOLDEN_RULES.md #8).

**This is the code that actually runs here.** `ps aux` shows the live process is
`/Applications/M5Burner.app/Contents/MacOS/m5burner`, the same bundle. The two version
numbers are not a contradiction: the Electron **main process** — which does all flashing
and plugin dispatch, i.e. everything below — ships inside `app.asar` at **3.0.0**, while
the **UI shell** is downloaded and self-updated from
`http://m5burner-cdn.m5stack.com/appVersion.info` into `view/` and pushed to the renderer
as `mainWin.webContents.send('get-version', updateInfo.version)` (`app.js`). That
downloaded shell is the `v202605221800` in the window title, which is why none of its UI
strings appear in the bundle.

M5Burner has two credential-injection mechanisms.

**(a) Per-product NVS mixins — hardcoded, unreachable by third-party firmware.**
Exactly four exist: `mixinUIFlow2NVS`, `mixinOpenaiNVS`, `mixinOpenaiCamNVS`,
`mixinStamplcNVS`. Each fills an ESP-IDF `nvs_partition_gen` CSV and mixes the result into
the flash image. Only two namespaces are ever used:

- `config` — keys `wifi_ssid`, `wifi_password`, plus product extras (`openaikey`, `language`)
- `uiflow` — keys `ssid0`/`pswd0`, `server`, `sntp0..2`, `tz`, `boot_option`, `net_mode`, …

Each is bound to one first-party M5Stack product.

**(b) A generic Wi-Fi plugin — real, but not NVS, and not reachable by a community listing.**
Dispatch is data-driven on the firmware's catalog entry:

```js
if(opts.payload.pluginType === PluginTypes.WIFI) {
  pluginConfig = useWifiPacker({...args, address: flashAddr})
}
```

`pluginAddr` comes from that catalog entry, with `'ADDR_END'` resolving to
`binary length + 0x1000`. `useWifiPacker` writes a **100-byte raw blob, not NVS**:

```
buf = 100 bytes, filled 0xff
buf[0]              = ssid length
buf[1 .. 1+n-1]     = ssid bytes
buf[1+n]            = ssid checksum      (sum of the ssid bytes & 0xff)
buf[50]             = password length
buf[51 .. 51+m-1]   = password bytes
buf[51+m]           = password checksum  (sum of the password bytes & 0xff)
```

SSID and password are each capped at 48 bytes by the 50-byte halves.

**Why it is not a shortcut — three independent reasons, any one sufficient:**

1. `pluginType` arrives on the **catalog entry**, which M5Stack controls. Felipe, who has
   published to M5Burner, confirms the publishing flow offers no such option — only a
   description field. A community listing cannot declare it.
2. Even granted, the blob carries **SSID and password only**. It can never carry the agent
   host or the device token. This is exactly the "decisive constraint" task 75 states about
   every off-the-shelf provisioning framework (WiFiProv, Improv, WiFiManager) — now
   **measured for M5Burner specifically** rather than assumed.
3. `ADDR_END` is fragile by construction: it moves with every build and could land inside
   `app1` or `spiffs` in our `default_8MB.csv` layout.

**Decision: the blob is NOT consumed.** Task 76 does not build a reader for it. Recorded
here so it is never rediscovered. Reversing that decision would save a user one field out
of four, on a path a community listing cannot reach, in exchange for a reserved flash
address in every build.

## Unit #1 — off-limits

Unit #1 (`sticks3-ptt`, MAC `AC:27:6E:D2:68:B8`) belongs to other projects.
**Never** target or flash it from this repo.
