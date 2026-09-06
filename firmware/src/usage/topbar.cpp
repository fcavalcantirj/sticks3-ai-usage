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

// freshnessColor returns the RGB565 colour for a freshness tier.
// 0=green, 1=yellow, 2=red.
uint16_t freshnessColor(uint8_t tier) {
    switch (tier) {
        case 1: return 0xFD20;  // yellow
        case 2: return 0xF800;  // red
        default: return 0x07E0; // green
    }
}

void compute(Layout& out, int16_t W,
             uint8_t page, uint8_t pageCount,
             const char* title, int battPct, bool battOnUsb, bool battKnown,
             bool wifiOk, uint8_t freshnessTier,
             int (*measure)(const char*)) {
    (void)freshnessTier;  // colour is applied by the caller (screen.cpp)
    std::memset(&out, 0, sizeof(out));
    out.W = W;

    // --- right-hand cluster (freshness dot + wifi bars + battery unit) ---
    // Packed right-to-left from x = W - kRightMargin.
    // Order: [freshness dot] [8px] [wifi bars] [4px] [battery] [pageInd] [title]
    int16_t cursor = W - kRightMargin;  // cursor starts at the right margin

    // 1. Freshness dot (r=3, diameter 6).  Always drawn; colour from the tier.
    //    Centre at (cursor - r), right edge at cursor.
    int16_t dotCx = cursor - 3;
    out.freshnessDot.x = dotCx;
    out.freshnessDot.w = 6;       // diameter
    out.freshnessDot.drawn = true;
    // cursor now = left edge of dot (dotCx - 3) minus the 8px gap.
    cursor = dotCx - 3 - kGapWifi;

    // 2. Wifi bars (3×1px ascending).  Dropped FIRST when space runs out
    //    (lower drop precedence than the dot and battery, which are always drawn).
    //    Drawn whenever wifiOk; dimmed when not ok.
    out.wifiBars.drawn = false;
    out.wifiBars.x = -1;
    out.wifiBars.w = 0;
    if (wifiOk) {
        // Try to place the 7px-wide bars between the dot and the battery.
        // Need: kWifiBarsW + kGap (4px) before the battery unit.
        int16_t barsRight = cursor;
        int16_t barsLeft = barsRight - kWifiBarsW;
        out.wifiBars.x = barsLeft;
        out.wifiBars.w = kWifiBarsW;
        out.wifiBars.drawn = true;
        // cursor moves left past the bars + 4px gap to the battery.
        cursor = barsLeft - kGap;
    } else {
        // No wifi bars drawn — but if there's room, still try to draw dimmed bars.
        // Actually: bars are drawn whenever space permits, dimmed when !wifiOk.
        // This gives the user visual feedback that wifi is present but not linked.
        int16_t barsRight = cursor;
        int16_t barsLeft = barsRight - kWifiBarsW;
        if (barsLeft - kGap >= 5) {  // need room for bars + gap + at least title start
            out.wifiBars.x = barsLeft;
            out.wifiBars.w = kWifiBarsW;
            out.wifiBars.drawn = true;
            cursor = barsLeft - kGap;
        }
        // If no room, bars stay dropped; cursor stays at dot gap.
    }

    // 3. Battery: label text is to the LEFT of the gauge.
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
