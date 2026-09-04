// firmware/src/usage/power.cpp — pure-C++17 power policy state machine.
//
// Host-compiled: no M5/Arduino headers, no heap allocation.  The signed
// 32-bit delta trick avoids uint32_t wraparound pitfalls at the 49.7-day
// millis() boundary.
#include "usage/power.h"

namespace usage {

PowerAction powerDecide(bool vbusPresent, uint32_t nowMs,
                        uint32_t graceAnchorMs, uint32_t graceMs) {
    // On USB: never sleep.  The device is always powered and the user may
    // be actively using the Mac it's propped against.
    if (vbusPresent) {
        return PowerAction::StayAwake;
    }

    // On battery: sleep after the grace window since the caller-set anchor.
    // (int32_t) cast makes the subtraction safe across the uint32_t wrap.
    if ((int32_t)(nowMs - graceAnchorMs) >= (int32_t)graceMs) {
        return PowerAction::SleepNow;
    }

    return PowerAction::StayAwake;
}

} // namespace usage
