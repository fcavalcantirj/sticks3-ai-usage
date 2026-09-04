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

**Current wake design:** ext1 buttons (GPIO11/12, pullup+pulldown pair) and a
60-second timer backstop. On USB the device never sleeps (`vbusPresent()` via
`VbusDebouncer`). Cable insertion is noticed within one minute via the timer.

**Instant-wake guard (monitor-only):** `g_powerGuard.ext0InstantWakeCount`
in `main.cpp` counts consecutive ext0 wakes with vbus < 4000. Ext0 is not
armed, so the counter stays 0 in normal operation — it is retained for the
follow-up experiment that re-enables ext0 via PM1 I2C IRQ register reads.

**Follow-up experiment (not yet implemented):** test whether the PM1
already asserts its IRQ line on 5VIN insertion with its default configuration.
If so, `esp_sleep_enable_ext0_wakeup(GPIO_NUM_13, 0)` with the standard
pullup/pulldown pair may work without writing any PM1 IRQ registers. This
must be validated on hardware before any code change.

### IMU calibration (task 48)

The BMI270 is at I2C address 0x68 (not 0x69) on SDA GPIO47 / SCL GPIO48.
M5Unified probes both; the hardcoded 0x68 value is the working address on
this board. `internal_imu` is set to `true` in `boardInit()`.

Double-tap thresholds were tuned from real captures (device on USB, double-tapping
the case):

| Parameter       | Value  | Rationale                                        |
|-----------------|--------|--------------------------------------------------|
| Spike threshold | 2.5 g  | Sharp tap transient above gravity baseline       |
| Spike duration  | 120 ms | Longer than this is a slow ramp (pick-up motion) |
| Inter-tap gap   | 120–500 ms | Too fast or too slow is not a natural double-tap |
| Lockout         | 1 s    | Prevents accidental triple-tap triggers          |

Raw magnitude samples were logged as `[IMU] mag=<f>` behind a build flag during
tuning. The thresholds separate genuine double-taps from pick-up/set-down ramps
and button presses.

Rotation is persisted in NVS (namespace `"usated"`, key `"rot"`) as values
1 (upright) or 3 (flipped). It survives reboot, deep sleep, and OTA.

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

## Unit #1 — off-limits

Unit #1 (`sticks3-ptt`, MAC `AC:27:6E:D2:68:B8`) belongs to other projects.
**Never** target or flash it from this repo.
