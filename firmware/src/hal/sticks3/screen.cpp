// firmware/src/hal/sticks3/screen.cpp — display HAL implementation.
#include "hal/sticks3/screen.h"

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

void drawPlan(const usage::RenderPlan& plan, bool wifiOk) {
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

    // --- rows ---
    for (uint8_t i = 0; i < plan.lineCount; i++) {
        const usage::Line& line = plan.lines[i];
        int16_t y = 29 + i * 17;

        // Left label.
        uint16_t labelColor = line.dim ? 0x8410 : TFT_WHITE;  // grey / white
        M5.Display.setTextColor(labelColor);
        M5.Display.setCursor(12, y);
        M5.Display.println(line.left);

        // Bar (only when pct >= 0).
        int maxPx = 156;  // 228 - 72
        if (line.pct >= 0) {
            M5.Display.fillRoundRect(72, y + 4, 100, 8, 3, 0x2945);
            uint16_t barColor =
                usage::tierColor565(line.tier, line.dim != 0);
            M5.Display.fillRect(72, y + 4, (int)line.pct, 8, barColor);
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
        fitPrintRight(W - 12, y, rightText, maxPx);
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

} // namespace sticks3
