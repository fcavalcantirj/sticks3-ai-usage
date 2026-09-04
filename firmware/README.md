# StickS3 Firmware

PlatformIO project for the M5StickS3 (ESP32-S3-PICO-1-N8R8) running the usaged
display sketch.

## Prerequisites

- [PlatformIO Core](https://docs.platformio.org/en/latest//core-installation/index.html) (CLI)
- ESP32 toolchain (installed automatically by PlatformIO Core)

## Setup

```sh
cd firmware
cp include/secrets.h.example include/secrets.h   # then edit secrets.h
```

## Build

```sh
pio run
```

## Flash

```sh
pio run -t upload
```

## Monitor

```sh
pio device monitor
```

## Serial protocol

The firmware emits one structured line per serial log entry.  Each line
starts with a tag in brackets that identifies the event type.  This is how
hardware behaviour is verified without a camera or extra tools.

| Tag       | Format                                                                 |
|-----------|------------------------------------------------------------------------|
| `[BOOT]`  | `[BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0`                  |
| `[NET]`   | `[NET] state=connected ip=192.168.0.77`  (ip omitted when absent)      |
| `[FETCH]` | `[FETCH] code=304 rev=abcd1234 ms=120`  (304, no body — no seq)       |
| `[FETCH]` | `[FETCH] code=200 rev=abcd1234 seq=43 ms=310`  (200, new data)        |
| `[FETCH]` | `[FETCH] code=-1 err=timeout ms=8000`  (error; rev reused as err)     |
| `[RENDER]`| `[RENDER] page=1 lines=5 rev=abcd1234`                                 |
| `[GESTURE]`| `[GESTURE] flip rot=3`  (IMU double-tap, 180° rotation)          |
| `[BTN]`    | `[BTN] a_hold refresh`  (BtnA long-press → force fetch)          |
| `[BTN]`    | `[BTN] a_click page`  (BtnA short-press → page cycle)            |
| `[HEAP]`  | `[HEAP] free=123456 min=65432`  (60 s watchdog)                        |
| `[ERR]`   | `[ERR] <what>`                                                        |
| `[WAKE]`  | `[WAKE] cause=timer vbus=0`  (deep-sleep wake cause)                   |
| `[SLEEP]` | `[SLEEP] reason=battery`  (deep-sleep entry)                            |
| `[OTA]`   | `[OTA] start` / `[OTA] pct=47` / `[OTA] end` / `[OTA] err=2`            |

### Fetch variants

- **304 (Not Modified)** — the snapshot did not change, so `seq` is omitted.
  The device keeps its current page.
- **200 (OK)** — the snapshot changed; `seq` is the new sequence number and
  `rev` is the new revision.  The device redraws only if `rev` differs from
  the last render.
- **Error (code < 0)** — e.g. `code=-1 err=timeout`.  The `rev` parameter is
  reused as the error description and printed after `err=`.  No redraw occurs.

### Expected serial transcript

A healthy boot and operation produces this sequence:

```
[BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0
[NET] state=connecting
[NET] state=connected ip=192.168.0.77
[FETCH] code=200 rev=5839af51 seq=1 ms=310
[RENDER] page=1 lines=5 rev=5839af51
[HEAP] free=8388608 min=6543210
...
[HEAP] free=8388500 min=6543000
...
[FETCH] code=304 rev=5839af51 ms=120
```

- `[BOOT]` fires once at startup with the board id, PSRAM size, build id and
  firmware version.
- `[NET]` fires on each Wi-Fi state transition (connecting, connected+ip, lost).
- `[FETCH]` fires on every poll.  A `200` includes `seq` (new data); a `304`
  omits `seq` (unchanged snapshot).  On 200 + rev change, a `[RENDER]` line
  follows immediately.
- `[RENDER]` fires after every redraw (rev change or BtnA page cycle) with the
  1-indexed page, number of lines drawn and the current rev.
- `[HEAP]` fires every 60 seconds regardless of activity, reporting the current
  free heap and the minimum-ever free heap since boot.
- `[ERR]` fires on parse failures (`parse: <reason>`) or fatal errors.  After 12
  consecutive fetch failures the device reboots with
  `[ERR] restart after 12 failures`.

## Screen geometry

The M5StickS3 LCD is 240×135 landscape (rotation 1).  `drawPlan` lays out
5 rows of 17 px each inside the card rectangle (x: 5–235, y: 25–113).

### Row layout (ORDER #28 corrected)

Each row is 17 px tall with a shared vertical centre for the label, bar, and
value text:

```
rowY      = 29 + i * 17          # top of row i
rowH      = 17                   # row height
fontH     = M5.Display.fontHeight()  # 8 px (Font 1, setTextSize(1))
textY     = rowY + (rowH - fontH) / 2   # = rowY + 4  — text cursor y
barY      = rowY + (rowH - 8) / 2       # = rowY + 4  — bar top y
```

- **Left label** at `(12, textY)`, font TFT_WHITE (dimmed: grey 0x8410)
- **Bar** at `(72, barY)`, width 100, height 8, radius 3 — track 0x2945
- **Right value** right-aligned at `x = W - 12`, cursor y = `textY`

The value text and label share `textY` (vertically centred in the row); the bar
shares `barY` (also vertically centred).  Previously the text was top-datum
(`rowY`) while the bar was at `rowY + 4`, making the value look like it belonged
to the row above.

## IMU & gestures (task 48)

The BMI270 IMU is at I2C address 0x68 (not 0x69) on SDA GPIO47 / SCL GPIO48.
M5Unified probes both and the hardcoded 0x68 value is the real address on this
board.  `internal_imu` is set to `true` in `boardInit()`.

### Double-tap to flip

The gesture logic lives entirely in `firmware/src/usage/gesture.{h,cpp}` — a
pure state machine with no M5/Arduino headers, host-tested by
`test/host/test_gesture.cpp`.  The HAL polls `M5.Imu.getAccelData` at ~50 Hz
(every 20 ms) while awake and feeds raw m/s² samples to the detector.

**Detection algorithm:**
1. Compute true-g magnitude: `mag = sqrt(ax² + ay² + az²) / 9.80665`.
2. A spike is a transient where `mag > 2.5g` lasting under 120 ms.
3. Two spikes separated by 120–500 ms form a double-tap.
4. A 1-second lockout follows each double-tap.
5. A slow ramp (picking the device up) exceeds 120 ms and is ignored.

On a double-tap, the screen rotation toggles between 1 (upright) and 3
(180-degree flip).  The rotation is persisted in NVS (namespace `"usaged"`,
key `"rot"`) so it survives reboot, deep sleep, and OTA.  It is loaded during
`boardInit()` before the first paint so a flipped device never shows one
upside-down frame.

### Button gestures

- **BtnA short press** (wasClicked): cycles to the next page.
- **BtnA long press** (wasHold, 600 ms threshold): forces an immediate fetch
  (`[BTN] a_hold refresh`), equivalent to pressing BtnB.
- **BtnB short press**: forces an immediate fetch (ORDER #38 refresh).

### Hardware capture notes (from Felipe's real device)

The thresholds (2.5g spike, 120 ms max spike duration, 120–500 ms gap,
1 s lockout) were tuned from real double-tap captures recorded while the device
was on USB power.  Raw accelerometer magnitude was logged as `[IMU] mag=<f>`
behind a build flag during tuning; see docs/DEVICES.md for the captured values.

The BMI270 draws ~1 mA when active.  During deep-sleep teardown it is
suspended (0x68: 0x7D=0x00, 0x7C=0x03) so it does not waste battery on the
60-second timer wake.  IMU polling is skipped during fetches and OTA transfers.
