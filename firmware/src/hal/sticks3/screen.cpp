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
    M5.Display.setCursor(5, 7);
    M5.Display.println(plan.title);                  // "AI USAGE"

    // asOf right-aligned at x = W - 70.
    int16_t aw = M5.Display.textWidth(plan.asOf);
    M5.Display.setCursor(W - 70 - aw, 7);
    M5.Display.println(plan.asOf);

    // Battery gauge (left of the wifi dot).
    drawBatteryGauge(batt.pct, batt.onUsb, batt.known);

    // Wifi dot at (W-12, 14).
    uint16_t dotColor = wifiOk ? 0x07E0 : 0xFD20;   // green / amber
    M5.Display.fillCircle(W - 12, 14, 3, dotColor);

    // Page indicator "N/M" at x = W - 40 when pageCount > 1.
    if (plan.pageCount > 1) {
        char pageBuf[8];
        std::snprintf(pageBuf, sizeof(pageBuf), "%d/%d",
                      (int)plan.page, (int)plan.pageCount);
        M5.Display.setCursor(W - 40, 7);
        M5.Display.println(pageBuf);
    }

    // Separator line at y = 21.
    M5.Display.drawFastHLine(5, 21, W - 10, 0x4208);

    // --- card background ---
    M5.Display.fillRoundRect(5, 25, 230, 88, 7, 0x1082);

    // ORDER #36 / task 50: alert banner.  When any provider is crit, draw a
    // full-width red banner as the TOP row of the card (every page), pushing
    // usage rows down by one slot (4 rows instead of 5).
    int16_t rowYOffset = 0;
    if (plan.bannerTier == 2 && plan.banner[0] != '\0') {
        M5.Display.fillRect(5, 29, 230, 17, 0xF800);  // red banner row
        M5.Display.setTextColor(TFT_BLACK);
        M5.Display.setCursor(12, 29 + (17 - 8) / 2);  // vertically centred
        M5.Display.println(plan.banner);
        M5.Display.setTextColor(TFT_WHITE);
        rowYOffset = 17;  // shift rows down by one slot
    }

    // --- rows ---
    for (uint8_t i = 0; i < plan.lineCount; i++) {
        const usage::Line& line = plan.lines[i];
        int16_t rowY = 29 + i * 17 + rowYOffset;
        int16_t rowH = 17;

        // ORDER #28: one shared vertical centre for label, bar and value.
        // Font 1 height (size 1) is 8 px; bar height is 8 px.  Centring both
        // at rowY + rowH/2 avoids the "value looks like the row above" misalignment
        // where top-datum text sat 4 px above the bar.
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

        // Bar (only when pct >= 0).
        int maxPx = 156;  // 228 - 72
        if (line.pct >= 0) {
            M5.Display.fillRoundRect(72, barY, 100, 8, 3, 0x2945);
            uint16_t barColor =
                usage::tierColor565(line.tier, line.dim != 0);
            M5.Display.fillRect(72, barY, (int)line.pct, 8, barColor);
            maxPx = 156 - (int)line.pct;  // space after bar fill
        }

        // Right text (pct >= 0: "NN% txt"; else: "txt").
        char rightText[32];
        if (line.pct >= 0) {
            std::snprintf(rightText, sizeof(rightText), "%d%% %s",
                          (int)line.pct, line.right);
        } else {
            std::snprintf(rightText, sizeof(rightText), "%s", line.right);
        }
        fitPrintRight(W - 12, textY, rightText, maxPx);
    }

    // --- footer (y = 116..134) ---
    M5.Display.setTextColor(0xFD20);  // amber
    const char* footer = plan.footer[0] != '\0'
                             ? plan.footer
                             : "BtnA page  BtnB refresh";
    int16_t fw = M5.Display.textWidth(footer);
    M5.Display.setCursor((W - fw) / 2, 120);
    M5.Display.println(footer);
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
    int16_t gaugeX = W - 12 - 6 - 32 - 6;  // left of gauge (32px wide + 6px label gap)
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
