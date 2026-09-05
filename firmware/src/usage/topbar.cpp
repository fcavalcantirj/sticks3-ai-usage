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

void compute(Layout& out, int16_t W, const char* buildId,
             uint8_t page, uint8_t pageCount, const char* asOf,
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
    cursor = dotCx - 3 - kGap;  // left edge of dot, minus gap

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

    cursor = labelLeft - kGap;  // left of the battery unit, minus gap

    // 3. Version (always shown, never dropped).
    const char* vstr = (buildId != nullptr && buildId[0] != '\0') ? buildId : "";
    int16_t verW = measure(vstr);
    int16_t verX = cursor - verW;
    out.version.x = verX;
    out.version.w = verW;
    out.version.drawn = true;
    cursor = verX - kGap;

    // Title width — used for overlap checks.
    const char* tstr = (title != nullptr) ? title : "";
    int16_t titleW = measure(tstr);
    out.title.w = titleW;

    // Minimum safe position: title needs its left edge >= 5.
    int16_t titleMinRight = 5 + titleW;

    // 4. Page indicator (only when pageCount > 1).
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

    // 5. asOf ("seq N") — dropped first when space is tight.
    out.asOf.drawn = false;
    out.asOf.x = -1;
    out.asOf.w = 0;
    const char* astr = (asOf != nullptr) ? asOf : "";
    int16_t aw = measure(astr);
    int16_t ax = cursor - aw;
    if (ax >= titleMinRight + kGap) {
        out.asOf.x = ax;
        out.asOf.w = aw;
        out.asOf.drawn = true;
        cursor = ax - kGap;
    }

    // 6. Title (left-aligned at x=5, drawn only if it doesn't overlap).
    out.title.x = -1;
    out.title.drawn = false;
    if (5 + titleW + kGap <= cursor) {
        out.title.x = 5;
        out.title.drawn = true;
    }
}

} // namespace topbar
} // namespace sticks3
