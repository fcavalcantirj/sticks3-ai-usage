// firmware/src/hal/sticks3/power.h — power management HAL for the M5StickS3.
//
// Only this file (and main.cpp) may include <M5Unified.h>/<esp_sleep.h>.
// The policy (powerDecide) lives in usage/power.h (pure C++17, host-tested).
#pragma once

#include <cstdint>

namespace sticks3 {

// Why the device woke from deep sleep.
enum class WakeCause : uint8_t {
    PowerOn,  // cold boot (no valid magic in RTC memory)
    Ext0,     // USB insert (PM1 IRQ on GPIO13) — ORDER #29: not armed, kept for monitoring
    Ext1,     // Button (BtnA GPIO11 / BtnB GPIO12, ext1)
    Timer,    // 60 s backstop timer (ORDER #29)
    Unknown,  // unrecognised ESP-IDF wake cause
};

// Map a wake cause to its serial-line string.
const char* wakeCauseStr(WakeCause c);

// VBUS voltage in millivolts (0 on read failure).  >4000 mV = USB present.
uint16_t vbusMv();

// True if USB is plugged in.
bool vbusPresent();

// Read the battery percentage (0..100), or -1 if the sensor reports unknown.
int batteryLevel();

// Read the raw battery voltage in millivolts (0 on read failure).
// Logged alongside the percentage in [BATT] so a flat cell (~3.3-3.5 V)
// is distinguishable from a broken read.  (ORDER #48 defect c)
int32_t batteryVoltageMv();

// Turn the screen fully off: display sleep + flush + cut the PM1 GPIO2
// (LCD panel) rail.  M5GFX re-asserts the rail on the next boot.
void screenOff();

// Full teardown (screen, radio, codec, IMU, PA rail) + arm the wake sources
// (buttons ext1, 60 s timer) + esp_deep_sleep_start().  Never returns.
// Call when powerDecide() returns SleepNow.
//
// ORDER #29: ext0 (USB-insert IRQ via PM1 GPIO1) is NOT armed — driving
// GPIO1 push-pull conflicts with SDA and hangs PMIC I2C after wake.
// The instant-wake guard counter (g_powerGuard in main.cpp) is retained
// as a monitor for the follow-up ext0 experiment; it has no effect on the
// normal sleep path.
void powerSleep();

// Read the ESP-IDF wake cause from boot registers.
WakeCause readWakeCause();

// Emit a [WAKE] line via fmtWake.
void emitWake(WakeCause cause, uint16_t vbus);

// Emit a [SLEEP] line via fmtSleep.
void emitSleep(const char* reason);

} // namespace sticks3
