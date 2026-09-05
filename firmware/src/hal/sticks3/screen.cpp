// firmware/src/hal/sticks3/screen.cpp — display HAL implementation.
#include "hal/sticks3/screen.h"

#include "usage/battery.h"   // BatteryView (pure C++17, testable on host)
#include "usage/textfit.h"   // fitRight (pure C++17, testable on host)

#include <M5Unified.h>
#include <cstdio>

namespace sticks3 {

void drawBootScreen(const char* build) {
    M5.Display.fillScreen(TFT_BLACK);

    // "AI USAGE" in size 2, centred.
    const char* msg = "AI USAGE";
    M5.Display.setTextSize(2);
    M5.Display.setTextColor(TFT_WHITE);
    int16_t w = M5.Display.width();
    int16_t h = M5.Display.height();
    int16_t tw = M5.Display.textWidth(msg);
    int16_t th = M5.Display.fontHeight();
    M5.Display.setCursor((w - tw) / 2, (h - th) / 2);
    M5.Display.println(msg);

    // Build id in size 1, centred at the bottom.
    M5.Display.setTextSize(1);
    int16_t bw = M5.Display.textWidth(build);
    int16_t bh = M5.Display.fontHeight();
    M5.Display.setCursor((w - bw) / 2, h - bh - 2);
    M5.Display.println(build);
}

// --- helpers for drawPlan --------------------------------------------------

// Measure a string's pixel width using the M5 display's current font.
static int measureText(const char* s) {
    return (int)M5.Display.textWidth(s);
}

// fitPrintRight shrinks `text` via fitRight and right-aligns it at x=xRight.
static void fitPrintRight(int xRight, int16_t y, const char* text, int maxPx) {
    char buf[64];
    usage::fitRight(text, buf, sizeof(buf), maxPx, measureText);
    int w = measureText(buf);
    M5.Display.setCursor(xRight - w, y);
    M5.Display.println(buf);
}

// --- drawPlan ---------------------------------------------------------------

void drawPlan(const usage::RenderPlan& plan, bool wifiOk,
              const sticks3::battery::BatteryView& batt) {
    M5.Display.fillScreen(TFT_BLACK);
    int16_t W = M5.Display.width();

    M5.Display.setTextSize(1);
    M5.Display.setTextColor(TFT_WHITE);

    // --- top bar (y = 0..21) ---

    // Right-aligned group built left-to-right from the right edge:
    //   [wifi dot] [battery] [version] [page indicator] [asOf]
    // Items are dropped from the right (asOf first, then page indicator) if
    // they would overlap the left-aligned title.  Version and battery are
    // never dropped.  (ORDER #48)

    // Wifi dot at (W-12, 14).
    uint16_t dotColor = wifiOk ? 0x07E0 : 0xFD20;   // green / amber
    M5.Display.fillCircle(W - 12, 14, 3, dotColor);

    // Battery gauge (positioned internally; right edge near W-15).
    drawBatteryGauge(batt.pct, batt.onUsb, batt.known);

    // Compute battery's left boundary so we can place the version to its left.
    char labelTmp[8];
    if (!batt.known) {
        std::snprintf(labelTmp, sizeof(labelTmp), "--");
    } else if (batt.onUsb) {
        std::snprintf(labelTmp, sizeof(labelTmp), "%d%%+", batt.pct);
    } else {
        std::snprintf(labelTmp, sizeof(labelTmp), "%d%%", batt.pct);
    }
    int16_t labelW = measureText(labelTmp);
    int16_t gaugeX = W - 12 - 6 - 32 - 6 - 6;  // matches drawBatteryGauge
    int16_t curRight = gaugeX - 6;  // 6 px gap left of battery label area

    // Version (dim grey, always shown).
    M5.Display.setTextColor(0x8410);  // dim grey
    int16_t vw = measureText(plan.buildId);
    int16_t verX = curRight - vw;
    M5.Display.setCursor(verX, 7);
    M5.Display.println(plan.buildId);
    M5.Display.setTextColor(TFT_WHITE);
    curRight = verX - 6;  // advance left

    // Page indicator (if pageCount > 1).
    if (plan.pageCount > 1) {
        char pageBuf[8];
        std::snprintf(pageBuf, sizeof(pageBuf), "%d/%d",
                      (int)plan.page, (int)plan.pageCount);
        int16_t pw = measureText(pageBuf);
        int16_t pageX = curRight - pw;
        // Only draw if it doesn't overlap the title.
        int16_t titleW = measureText(plan.title);
        if (pageX >= 5 + titleW + 6) {
            M5.Display.setCursor(pageX, 7);
            M5.Display.println(pageBuf);
            curRight = pageX - 6;
        }
    }

    // asOf ("seq N") — dropped first when space is tight (ORDER #48: drop seq
    // before title).
    int16_t aw = measureText(plan.asOf);
    int16_t titleW = measureText(plan.title);
    int16_t asOfX = curRight - aw;
    if (asOfX >= 5 + titleW + 6) {
        M5.Display.setCursor(asOfX, 7);
        M5.Display.println(plan.asOf);
        curRight = asOfX - 6;
    }
    // If asOf didn't fit, fall through — title area is still intact.

    // Title (left).  Drawn last so we know whether the right group overlaps.
    M5.Display.setCursor(5, 7);
    // If the title would overlap the right group, skip it (ORDER #48: drop
    // title text second, never version or battery).
    if (5 + titleW + 6 <= (int16_t)(verX - 6)) {
        M5.Display.setTextColor(TFT_WHITE);
        M5.Display.println(plan.title);
    }

    // Separator line at y = 21.
    M5.Display.drawFastHLine(5, 21, W - 10, 0x4208);

    // --- card background ---
    // Kind stripe: 5 px left stripe in the kind accent colour.
    M5.Display.fillRect(5, 25, 5, 88, plan.kindColor);
    M5.Display.fillRoundRect(10, 25, W - 15, 88, 7, 0x1082);

    // ORDER #36 / task 50: alert banner.  When ANY provider is crit, draw a
    // full-width red banner as the TOP row of the card (every page), pushing
    // usage rows down by one slot (4 rows instead of 5).
    int16_t rowYOffset = 0;
    if (plan.bannerTier == 2 && plan.banner[0] != '\0') {
        M5.Display.fillRect(10, 29, W - 20, 17, 0xF800);  // red banner row
        M5.Display.setTextColor(TFT_BLACK);
        M5.Display.setCursor(15, 29 + (17 - 8) / 2);  // vertically centred
        M5.Display.println(plan.banner);
        M5.Display.setTextColor(TFT_WHITE);
        rowYOffset = 17;  // shift rows down by one slot
    }

    // --- rows ---
    // For credit/free kinds: no bar is drawn; rows show their text right-aligned.
    // For plan kind: bar + percent + reset (current behaviour).
    bool hasBar = (plan.kind == usage::KIND_PLAN);
    int16_t barTrackX = 77;   // 72 + 5 (stripe shift)
    int16_t barTrackW = 100;
    int16_t rightPad  = 12;   // right margin for text

    for (uint8_t i = 0; i < plan.lineCount; i++) {
        const usage::Line& line = plan.lines[i];
        int16_t rowY = 29 + i * 17 + rowYOffset;
        int16_t rowH = 17;

        // ORDER #28: one shared vertical centre for label, bar and value.
        int16_t fontH = M5.Display.fontHeight();
        int16_t textY = rowY + (rowH - fontH) / 2;
        int16_t barY  = rowY + (rowH - 8) / 2;

        // Left label: grey when dim, amber when warn-flagged, white otherwise.
        uint16_t labelColor = line.dim     ? 0x8410
                          : line.warn     ? 0xFD20  // warn tint (ORDER #36 task 50)
                          : TFT_WHITE;
        M5.Display.setTextColor(labelColor);
        M5.Display.setCursor(12, textY);
        M5.Display.println(line.left);

        // Bar (only for plan kind when pct >= 0).
        int maxPx = W - barTrackX - barTrackW - rightPad;
        if (hasBar && line.pct >= 0) {
            M5.Display.fillRoundRect(barTrackX, barY, barTrackW, 8, 3, 0x2945);
            uint16_t barColor =
                usage::tierColor565(line.tier, line.dim != 0);
            // Clamp fill to track width (ORDER #48 task 53: bar overflow at 100%).
            int fillW = (int)line.pct;
            if (fillW > barTrackW) fillW = barTrackW;
            if (fillW < 0) fillW = 0;
            M5.Display.fillRect(barTrackX, barY, fillW, 8, barColor);
            maxPx = (W - barTrackX - rightPad) - fillW;
        }

        // Right text.
        char rightText[32];
        if (hasBar && line.pct >= 0) {
            std::snprintf(rightText, sizeof(rightText), "%d%% %s",
                          (int)line.pct, line.right);
        } else {
            std::snprintf(rightText, sizeof(rightText), "%s", line.right);
        }
        fitPrintRight(W - rightPad, textY, rightText, maxPx);
    }

    // --- footer (y = 116..134) ---
    M5.Display.setTextColor(0xFD20);  // amber
    // ORDER #49: footer uses physical button language, not GPIO-level names.
    const char* footer = plan.footer[0] != '\0'
                             ? plan.footer
                             : "blue: page   side: refresh";
    int16_t fw = M5.Display.textWidth(footer);
    M5.Display.setCursor((W - fw) / 2, 120);
    M5.Display.println(footer);
}

void drawRefreshStatus() {
    M5.Display.fillScreen(TFT_BLACK);
    M5.Display.setTextSize(2);
    M5.Display.setTextColor(TFT_WHITE);
    const char* msg = "refreshing…";
    int16_t w = M5.Display.width();
    int16_t h = M5.Display.height();
    int16_t tw = M5.Display.textWidth(msg);
    int16_t th = M5.Display.fontHeight();
    M5.Display.setCursor((w - tw) / 2, (h - th) / 2);
    M5.Display.println(msg);
}

void drawOtaStatus(uint8_t pct) {
    M5.Display.fillScreen(TFT_BLACK);
    M5.Display.setTextSize(2);
    M5.Display.setTextColor(TFT_YELLOW);
    char buf[32];
    std::snprintf(buf, sizeof(buf), "OTA %d%%", (int)pct);
    int16_t w = M5.Display.width();
    int16_t h = M5.Display.height();
    int16_t tw = M5.Display.textWidth(buf);
    int16_t th = M5.Display.fontHeight();
    M5.Display.setCursor((w - tw) / 2, (h - th) / 2);
    M5.Display.println(buf);
}

// --- battery gauge -----------------------------------------------------------

// drawBatteryGauge draws a PTT-style 32×13 rectangle with a 3×5 nub at the
// right end, filled proportionally to the level, with a colour tier and
// numeric percent label.  On USB, a '+' suffix is appended.
void drawBatteryGauge(int pct, bool onUsb, bool known) {
    int16_t W = M5.Display.width();

    // Position: right-aligned, to the left of the wifi dot at (W-12, 14).
    // Wifi dot is 3px radius at x=W-12; leave 6px gap, then 32px gauge + label.
    int16_t gaugeX = W - 12 - 6 - 32 - 6 - 6;  // left of gauge (32px wide + 6px label gap)
    int16_t gaugeY = 10;  // vertically centred in the 22px top bar
    int16_t gaugeW = 32;
    int16_t gaugeH = 13;

    // Colour tier: green >40%, yellow >20%, red <=20%.
    uint16_t color;
    if (!known) {
        color = 0x8410;  // grey
    } else if (pct > 40) {
        color = 0x07E0;  // green
    } else if (pct > 20) {
        color = 0xFD20;  // amber/yellow
    } else {
        color = 0xF800;  // red
    }

    // Fill (proportional to pct).
    if (known && pct > 0) {
        uint8_t fillW = (gaugeW * pct) / 100;
        if (fillW < 1) fillW = 1;  // always at least 1px when >0%
        if (fillW > gaugeW) fillW = gaugeW;  // clamp to track width
        M5.Display.fillRect(gaugeX + 1, gaugeY + 1, fillW - 1, gaugeH - 2, color);
    }

    // Outline rectangle with nub.
    M5.Display.drawRect(gaugeX, gaugeY, gaugeW, gaugeH, color);
    // Nub (3x5 at right side, centred vertically).
    M5.Display.fillRect(gaugeX + gaugeW, gaugeY + 4, 3, 5, color);

    // Numeric label to the left of the gauge.
    char label[8];
    if (!known) {
        std::snprintf(label, sizeof(label), "--");
    } else if (onUsb) {
        std::snprintf(label, sizeof(label), "%d%%+", pct);
    } else {
        std::snprintf(label, sizeof(label), "%d%%", pct);
    }
    int16_t labelW = M5.Display.textWidth(label);
    M5.Display.setTextColor(color);
    M5.Display.setCursor(gaugeX - labelW - 2, gaugeY + 4);
    M5.Display.println(label);
}

} // namespace sticks3
