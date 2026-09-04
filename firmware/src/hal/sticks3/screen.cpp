// firmware/src/hal/sticks3/screen.cpp — display HAL implementation.
#include "hal/sticks3/screen.h"

#include <M5Unified.h>

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

} // namespace sticks3
