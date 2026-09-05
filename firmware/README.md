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
| `[GESTURE]`| `[GESTURE] flip rot=3`  (BtnB hold ≥ 1500 ms, 180° rotation)         |
| `[BTN]`    | `[BTN] gpio=11 click page`  (blue button short → page cycle)     |
| `[BTN]`    | `[BTN] gpio=11 hold refresh`  (blue button long → POST /v1/refresh)|
| `[BTN]`    | `[BTN] gpio=12 click refresh`  (side button → POST /v1/refresh) |
| `[REFRESH]`| `[REFRESH] code=200 ms=310`  (POST /v1/refresh, fresh data ready) |
| `[REFRESH]`| `[REFRESH] code=202 retry`  (server poll still running)        |
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

## Screen rotation & gestures (ORDER #53 REVISED, task 58)

The BMI270 IMU is **not used** — `internal_imu` is `false` and `M5.Imu.begin()`
is never called. The device has no vibration motor.

### Hold-to-flip screen

Rotation is toggled by **BtnB (GPIO 12, side button) hold**:
- Click (release before 1500 ms): POST `/v1/refresh` + conditional GET.
- Hold (≥ 1500 ms): toggle rotation 1 ↔ 3, persist to NVS, emit `[GESTURE]`.
- Hint (at 500 ms into a hold): amber "hold to flip 180°" in the footer.

The detection is a pure C++17 state machine in `usage/hold_flip.{h,cpp}`,
host-tested by `test/host/test_hold_flip.cpp`. `M5.BtnB.setHoldThresh(1500)`
is set in `boardInit()` for documentation — the state machine handles timing
via `isPressed()` polling.

Rotation is persisted in NVS (namespace `"usaged"`, key `"rot"`) as 1
(upright) or 3 (180° flip). Loaded before the first paint so a flipped device
never shows one upside-down frame. Survives reboot, deep sleep, and OTA.

### Button gestures

ORDER #49 settled the physical button mapping empirically. The footer
relables them by physical position, not GPIO number:

- **Blue button** (GPIO 11, BtnA) short press: cycles to the next page.
- **Blue button** long press (600 ms threshold): POSTs `/v1/refresh` then does
  a conditional GET (ORDER #38).
- **Side button** (GPIO 12, BtnB) short press: POSTs `/v1/refresh` then does a
  conditional GET, with a 10 s throttle and a single 202-retry after ~2 s.

The device emits `[BTN] gpio=11 click page`, `[BTN] gpio=11 hold refresh`, or
`[BTN] gpio=12 click refresh` on every button event. The `gpio=` field is the
physical GPIO number so Felipe can correlate a press with the hardware pin
without consulting the source.
