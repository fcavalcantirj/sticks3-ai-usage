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
//   - ON BATTERY within grace (nowMs - graceAnchorMs < graceMs): StayAwake
//     — finish the current fetch/render, keep the screen lit for a short
//     post-activity grace so the user sees the result.
//   - ON BATTERY past grace: SleepNow — cut leaks, arm wake sources, deep sleep.
//
// The caller owns the grace anchor: it is reset to nowMs on a USB→battery
// transition, on the first render after a wake, or on a fetch failure
// (ORDER #30).  Passing nowMs as graceAnchorMs while grace is not yet active
// yields StayAwake — see main.cpp.
//
// The 20 s default grace mirrors the "paint cached snapshot" window from
// ptt.ino ST_LOOK → ST_READY.  Callers pass a smaller graceMs for the
// post-sleep-paint timer.
PowerAction powerDecide(bool vbusPresent, uint32_t nowMs,
                        uint32_t graceAnchorMs, uint32_t graceMs = 20000);

// VbusDebouncer filters single-sample VBUS glitches so a one-off I2C read
// failure (returns 0 mV) or noise spike can never make the device deep-sleep
// while it is actually on USB.  A reading of exactly 0 mV is always suspect
// and is ignored (the last settled state is returned).  After N consecutive
// "battery" readings (vbus <= 4000, > 0) the settled state flips to false.
// Any "USB" reading (vbus > 4000) resets the counter and flips to true.
//
// The debouncer is pure C++17 (no M5/Arduino headers) so the "one spurious
// sample does not cause SleepNow" invariant is host-tested.
class VbusDebouncer {
public:
    // samplesNeeded consecutive "battery" readings before settling to false.
    explicit VbusDebouncer(uint8_t samplesNeeded = 3)
        : m_samplesNeeded(samplesNeeded), m_batteryCount(0), m_settled(true) {}

    // Feed a raw VBUS reading (mV).  Returns the debounced vbusPresent.
    bool sample(uint32_t vbusMv) {
        // BUG 40c: 0 mV is a legitimate 'no USB' reading, not a glitch.
        if (vbusMv > 4000) {
            // USB confirmed: reset counter, settle true.
            m_batteryCount = 0;
            m_settled = true;
        } else {
            // Battery: increment, but don't flip until threshold.
            if (m_batteryCount < 255) m_batteryCount++;
            if (m_batteryCount >= m_samplesNeeded) {
                m_settled = false;
            }
        }
        return m_settled;
    }

    // Current settled result without consuming a sample.
    bool vbusPresent() const { return m_settled; }

private:
    const uint8_t m_samplesNeeded;
    uint8_t m_batteryCount;
    bool m_settled;
};

} // namespace usage
