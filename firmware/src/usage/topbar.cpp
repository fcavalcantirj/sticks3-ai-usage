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

    // --- right-hand cluster (wifi dot + battery unit) ---
    // Packed right-to-left from x = W - kRightMargin.
    int16_t cursor = W - kRightMargin;  // cursor starts at the right margin

    // 1. Wifi dot (r=3, diameter 6).  Centre at (cursor - r), right edge at cursor.
    int16_t dotCx = cursor - 3;
    out.wifiDot.x = dotCx;
    out.wifiDot.w = 6;       // diameter
    out.wifiDot.drawn = true;
    cursor = dotCx - 3 - kGapWifi;  // left edge of dot, minus gap

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

    // cursor now = left edge of the battery unit (labelLeft), minus 4px gap.
    cursor = labelLeft - kGap;

    // --- title (left-aligned at x=5) ---
    const char* tstr = (title != nullptr) ? title : "";
    int16_t titleW = measure(tstr);
    out.title.w = titleW;
    int16_t titleRight = 5 + titleW;  // right edge of title text
    bool titleFits = (titleRight + kGap <= cursor);

    out.title.x = -1;
    out.title.drawn = false;
    if (titleFits) {
        out.title.x = 5;
        out.title.drawn = true;
    }

    // --- page indicator (CENTERED in the free span) ---
    // Placed in the gap between the title's right edge and the cluster's left
    // edge.  Dropped BEFORE the title when there is no room (drop-precedence:
    // the indicator only appears when the title also fits).
    out.pageInd.drawn = false;
    out.pageInd.x = -1;
    out.pageInd.w = 0;
    if (pageCount > 1 && titleFits) {
        char pageBuf[8];
        std::snprintf(pageBuf, sizeof(pageBuf), "%u/%u",
                      (unsigned)(page + 1), (unsigned)pageCount);
        int16_t pw = measure(pageBuf);

        // Free span: [titleRight + kGap, cursor - 0] (cursor = clusterLeft).
        int16_t freeLeft = titleRight + kGap;
        int16_t freeW = cursor - freeLeft;
        if (freeW >= pw) {
            // Centre the indicator in the free span.
            out.pageInd.x = (freeLeft + cursor - pw) / 2;
            out.pageInd.w = pw;
            out.pageInd.drawn = true;
        }
    }
}

} // namespace topbar
} // namespace sticks3
