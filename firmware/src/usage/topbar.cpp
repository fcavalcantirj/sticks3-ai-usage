// firmware/src/usage/topbar.cpp — pure-C++17 top-bar layout computation.
// Host-testable by `make fw-test` (included in the CMake glob of src/usage/*.cpp).
#include "usage/topbar.h"

#include <cstdio>
#include <cstring>

namespace sticks3 {
namespace topbar {

// batteryLabelText formats the battery label: "87%", "87%+", "--".
// (Same logic as battery::batteryLabel but inlined to avoid the
// M5/Arduino-free battery module cross-referencing the measure callback.)
void batteryLabelText(int battPct, bool battOnUsb, bool battKnown,
                      char* out, size_t n) {
    if (!battKnown) {
        if (n >= 3) { std::strncpy(out, "--", n - 1); out[n-1] = '\0'; }
        return;
    }
    if (battOnUsb) {
        std::snprintf(out, n, "%d%%+", battPct);
    } else {
        std::snprintf(out, n, "%d%%", battPct);
    }
}

void compute(Layout& out, int16_t W,
             uint8_t page, uint8_t pageCount,
             const char* title, int battPct, bool battOnUsb, bool battKnown,
             int (*measure)(const char*)) {
    std::memset(&out, 0, sizeof(out));
    out.W = W;

    // Items that are always drawn.
    int16_t cursor = W - kRightMargin;  // right edge of the packer

    // 1. Wifi dot (r=3, diameter 6).  Centre at (cursor - r), right edge at cursor.
    int16_t dotCx = cursor - 3;
    out.wifiDot.x = dotCx;
    out.wifiDot.w = 6;       // diameter
    out.wifiDot.drawn = true;
    cursor = dotCx - 3 - kGapWifi;  // left edge of dot, minus 8px gap

    // 2. Battery: label text is to the LEFT of the gauge.
    char battLabel[8];
    batteryLabelText(battPct, battOnUsb, battKnown, battLabel, sizeof(battLabel));
    int16_t labelW = measure(battLabel);

    // Gauge (32 + 3 nub = 35) + label + 2px gap between label and gauge.
    int16_t unitRight = cursor;
    int16_t gaugeLeft = unitRight - kGaugeW - kNubW;
    int16_t labelLeft = gaugeLeft - labelW - 2;

    out.battery.x = gaugeLeft;
    out.battery.w = kGaugeW + kNubW;  // 35
    out.battery.drawn = true;

    out.battLabel.x = labelLeft;
    out.battLabel.w = labelW;
    out.battLabel.drawn = true;

    cursor = labelLeft - kGap;  // left of the battery unit, minus 4px gap

    // Title width — used for overlap checks.
    const char* tstr = (title != nullptr) ? title : "";
    int16_t titleW = measure(tstr);
    out.title.w = titleW;

    // Minimum safe position: title needs its left edge >= 5.
    int16_t titleMinRight = 5 + titleW;

    // Page indicator (only when pageCount > 1).  Dropped before the title.
    out.pageInd.drawn = false;
    out.pageInd.x = -1;
    out.pageInd.w = 0;
    if (pageCount > 1) {
        char pageBuf[8];
        std::snprintf(pageBuf, sizeof(pageBuf), "%u/%u",
                      (unsigned)(page + 1), (unsigned)pageCount);
        int16_t pw = measure(pageBuf);
        int16_t px = cursor - pw;
        if (px >= titleMinRight + kGap) {
            out.pageInd.x = px;
            out.pageInd.w = pw;
            out.pageInd.drawn = true;
            cursor = px - kGap;
        }
    }

    // Title (left-aligned at x=5, drawn only if it doesn't overlap).
    out.title.x = -1;
    out.title.drawn = false;
    if (5 + titleW + kGap <= cursor) {
        out.title.x = 5;
        out.title.drawn = true;
    }
}

} // namespace topbar
} // namespace sticks3
