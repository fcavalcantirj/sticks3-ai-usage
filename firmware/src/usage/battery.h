// firmware/src/usage/battery.h — pure battery indicator logic.
//
// The HAL reads M5.Power.getBatteryLevel() (0..100) and getVBUSVoltage()
// (mV, >4000 = USB present) and constructs a BatteryView; this module
// formats the display label and computes the tier colour.  No M5/Arduino
// headers — host-tested by make fw-test.
//
// Design mirrors Felipe's PTT firmware drawBatteryGauge (ptt.ino:305-318):
// a 32x13 outlined rectangle with a 3x5 nub, filled proportionally,
// green >40%, yellow >20%, red <=20%, numeric percent next to it, '+'
// suffix while on external power.
#pragma once

#include <cstdint>
#include <cstddef>

namespace sticks3 {
namespace battery {

// BatteryView is the raw state captured by the HAL once per poll cycle.
struct BatteryView {
    int  pct;     // 0..100, or -1 when unknown
    bool onUsb;   // VBUS > 4000 mV
    bool known;   // false when pct is -1 (sensor read failed)
};

// batteryLabel produces the human-readable label:
//   "87%"     — known battery, on battery
//   "87%+"    — known battery, on USB (external power)
//   "--"      — unknown (sensor failure)
void batteryLabel(const BatteryView& v, char* out, size_t n);

// batteryTier returns the colour tier:
//   0 = ok    (pct > 40)
//   1 = warn  (pct > 20)
//   2 = crit  (pct <= 20)
//   3 = unknown (pct not known)
uint8_t batteryTier(const BatteryView& v);

// batteryPctClamped clamps a raw level to [0, 100], returning -1 for
// negative (unknown) inputs.
int batteryPctClamped(int raw);

} // namespace battery
} // namespace sticks3
