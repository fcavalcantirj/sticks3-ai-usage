// firmware/src/usage/power.h — pure-C++17 power policy state machine.
//
// No M5/Arduino headers.  All hardware register writes live in the HAL
// (firmware/src/hal/sticks3/power.{h,cpp}); this file decides *when* to
// sleep, the HAL decides *how*.
#pragma once

#include <cstdint>

namespace usage {

// Action returned by powerDecide.
enum class PowerAction : uint8_t {
    StayAwake,   // keep running (screen on, polling, OTA armed)
    SleepNow,    // enter deep sleep
};

// Pure state machine: decide whether to stay awake or sleep.
//
//   - ON USB (vbusPresent = true):  always StayAwake — screen on, 300 s poll,
//     OTA armed, never sleep.
//   - ON BATTERY within grace (nowMs - lastActivityMs < graceMs): StayAwake
//     — finish the current fetch/render, keep the screen lit for a short
//     post-activity grace so the user sees the result.
//   - ON BATTERY past grace: SleepNow — cut leaks, arm wake sources, deep sleep.
//
// The 20 s default grace mirrors the "paint cached snapshot" window from
// ptt.ino ST_LOOK → ST_READY.  Callers pass a smaller graceMs for the
// post-sleep-paint timer.
PowerAction powerDecide(bool vbusPresent, uint32_t nowMs,
                        uint32_t lastActivityMs, uint32_t graceMs = 20000);

} // namespace usage
