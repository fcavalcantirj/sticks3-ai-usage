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

### Hold-to-flip screen (ORDER #53 REVISED, task 58)

The BMI270 IMU is **not used**. `internal_imu` is `false` in `boardInit()`
and `M5.Imu.begin()` is never called. The device has no vibration motor, so
haptic feedback is not an option.

Screen rotation is controlled by **BtnB (GPIO 12, the side button) hold**:
- **Click** (release before 1500 ms): POST `/v1/refresh` + conditional GET,
  same as today.
- **Hold** (1500 ms threshold): toggle the screen 180° (rotation 1 ↔ 3).
- **Hint** (500 ms into a hold): a brief amber "hold to flip 180°" text
  appears in the footer area without a full repaint.

The logic is a pure C++17 state machine in `usage/hold_flip.{h,cpp}`,
host-tested by `test/host/test_hold_flip.cpp` — no sensor polling, no
hardware dependency. `M5.BtnB.setHoldThresh(1500)` documents the threshold
even though the state machine handles the timing via `isPressed()` polling.

Rotation is persisted in NVS (namespace `"usated"`, key `"rot"`) as values
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

## Unit #1 — off-limits

Unit #1 (`sticks3-ptt`, MAC `AC:27:6E:D2:68:B8`) belongs to other projects.
**Never** target or flash it from this repo.
