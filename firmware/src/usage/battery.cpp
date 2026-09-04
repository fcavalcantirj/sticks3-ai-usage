// firmware/src/usage/battery.cpp — pure battery indicator logic.
// No M5/Arduino headers.
#include "usage/battery.h"

#include <cstdio>

namespace sticks3 {
namespace battery {

int batteryPctClamped(int raw) {
    if (raw < 0) {
        return -1; // unknown
    }
    if (raw > 100) {
        return 100;
    }
    return raw;
}

void batteryLabel(const BatteryView& v, char* out, size_t n) {
    if (!v.known) {
        std::snprintf(out, n, "--");
        return;
    }
    if (v.onUsb) {
        std::snprintf(out, n, "%d%%+", v.pct);
    } else {
        std::snprintf(out, n, "%d%%", v.pct);
    }
}

uint8_t batteryTier(const BatteryView& v) {
    if (!v.known) {
        return 3;
    }
    if (v.pct > 40) {
        return 0;
    }
    if (v.pct > 20) {
        return 1;
    }
    return 2;
}

} // namespace battery
} // namespace sticks3
