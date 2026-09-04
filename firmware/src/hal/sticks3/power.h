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
    Ext0,     // USB insert (PM1 IRQ on GPIO13, ext0)
    Ext1,     // Button (BtnA GPIO11 / BtnB GPIO12, ext1)
    Timer,    // 1 h backstop timer
    Unknown,  // unrecognised ESP-IDF wake cause
};

// Map a wake cause to its serial-line string.
const char* wakeCauseStr(WakeCause c);

// VBUS voltage in millivolts (0 on read failure).  >4000 mV = USB present.
uint16_t vbusMv();

// True if USB is plugged in.
bool vbusPresent();

// Turn the screen fully off: display sleep + flush + cut the PM1 GPIO2
// (LCD panel) rail.  M5GFX re-asserts the rail on the next boot.
void screenOff();

// Full teardown (screen, radio, codec, IMU, PA rail) + arm the three wake
// sources (USB-insert ext0, buttons ext1, 1-h timer) + esp_deep_sleep_start().
// Never returns.  Call when powerDecide() returns SleepNow.
void powerSleep();

// Read the ESP-IDF wake cause from boot registers.
WakeCause readWakeCause();

// Emit a [WAKE] line via fmtWake.
void emitWake(WakeCause cause, uint16_t vbus);

// Emit a [SLEEP] line via fmtSleep.
void emitSleep(const char* reason);

} // namespace sticks3
