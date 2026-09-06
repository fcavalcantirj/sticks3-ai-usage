// firmware/src/hal/sticks3/screen.cpp — display HAL implementation.
#include "hal/sticks3/screen.h"

#include "usage/battery.h"   // BatteryView (pure C++17, testable on host)
#include "usage/textfit.h"   // fitRight (pure C++17, testable on host)
#include "usage/topbar.h"    // topbar::compute (pure layout, host-tested)
#include "usage/render_plan.h" // usage::FooterLayout, footerCompute (ORDER #56)
#include "usage/align.h"       // usage::centerTextY / centerTextX (ORDER #59 task 62)
#include "usage/brightness.h"  // kLevels table for label ticks (ORDER #72 task 72)

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

    // TOP BAR (y = 0..21) — ORDER #51/#65: header keeps only the freshness dot,
    // wifi bars, battery gauge+label, page indicator, and title.  Version and seq
    // moved to the footer.  The packer handles gaps and drop-precedence.

    topbar::Layout tbl;
    topbar::compute(tbl, W,
                    (uint8_t)(plan.page - 1), plan.pageCount,
                    plan.title, batt.pct, batt.onUsb, batt.known,
                    wifiOk, plan.freshnessTier,
                    measureText);

    // Freshness dot (always drawn, far right, colour from the tiered age).
    M5.Display.fillCircle(tbl.freshnessDot.x, topbar::kDotY, 3,
                          topbar::freshnessColor(plan.freshnessTier));

    // Wifi bars (3 ascending 1px bars).  Solid when wifiOk, dimmed grey when not.
    // Dropped first by the packer when space runs out — lower precedence than the
    // freshness dot, which must always be visible.
    if (tbl.wifiBars.drawn) {
        uint16_t barColor = wifiOk ? 0x07E0 : 0x8410;  // green / dim grey
        for (int b = 0; b < 3; b++) {
            int16_t bx = tbl.wifiBars.x + b * topbar::kWifiBarGap; // 2px pitch
            int16_t bh = topbar::kWifiBarHeights[b];
            int16_t by = topbar::kDotY - bh / 2;  // centred on the bar midline
            M5.Display.fillRect(bx, by, 1, bh, barColor);
        }
    }

    // Battery gauge + label (always drawn).
    drawBatteryGauge(tbl.battery.x, topbar::kGaugeY, topbar::kTextY,
                     batt.pct, batt.onUsb, batt.known);

    // Page indicator (if drawn by the packer).
    if (tbl.pageInd.drawn) {
        char pageBuf[8];
        std::snprintf(pageBuf, sizeof(pageBuf), "%u/%u",
                      (unsigned)plan.page, (unsigned)plan.pageCount);
        M5.Display.setCursor(tbl.pageInd.x, topbar::kTextY);
        M5.Display.println(pageBuf);
    }

    // Title (left-aligned) — only if the packer kept it.
    if (tbl.title.drawn) {
        M5.Display.setTextColor(TFT_WHITE);
        M5.Display.setCursor(5, topbar::kTextY);
        M5.Display.println(plan.title);
    }

    // Separator line at y = 21.
    M5.Display.drawFastHLine(5, 21, W - 10, 0x4208);

    // ORDER #73 task 73: instructions page — same top bar, but no kind stripe
    // or card background.  Instead, draw the binding lines with a larger font.
    if (plan.isHelp) {
        M5.Display.setTextSize(1);
        M5.Display.setTextColor(TFT_WHITE);

        // Binding lines: left = "blue click", right = "pages", size 2 font.
        // ORDER #74 task 74: measure-and-fit guard — left is drawn at x=5,
        // right is right-aligned; if they would overlap, fitRight shortens
        // the action text so a binding edit cannot reintroduce the overlap.
        M5.Display.setTextSize(2);
        M5.Display.setTextColor(TFT_WHITE);
        int16_t baseY = 32;
        int16_t lineH = 20;  // 16px font + 4px gap
        int16_t minGap = 4;  // minimum gap between left and right
        int16_t rightPad = 4;
        for (uint8_t i = 0; i < plan.lineCount; i++) {
            const usage::Line& line = plan.lines[i];
            int16_t y = baseY + i * lineH;

            // Left: button + gesture (e.g. "blue click").
            int16_t lw = M5.Display.textWidth(line.left);
            M5.Display.setCursor(5, y);
            M5.Display.print(line.left);

            // Right: action, right-aligned with fit guard.
            int16_t availRight = W - 5 - lw - minGap - rightPad;
            if (availRight < 0) availRight = 0;
            char rightFitted[32];
            usage::fitRight(line.right, rightFitted, sizeof(rightFitted),
                            availRight, measureText);
            int16_t rw = measureText(rightFitted);
            if (rw > 0) {
                M5.Display.setCursor(W - rw - rightPad, y);
                M5.Display.println(rightFitted);
            }
        }

        // Footer: version only (no hint on the help page — hint lives on the
        // last usage page, not here).
        int16_t footerY = 116;
        int16_t footerH = 16;
        int16_t fontH = M5.Display.fontHeight();
        int16_t footerBaseline = usage::centerTextY(footerY, footerH, fontH);
        M5.Display.setTextColor(0xFD20);  // amber
        M5.Display.setCursor(5, footerBaseline);
        M5.Display.println(plan.buildId);

        return;
    }

    // --- card background ---
    // Kind stripe: 5 px left stripe in the kind accent colour.
    M5.Display.fillRect(5, 25, 5, 88, plan.kindColor);
    M5.Display.fillRoundRect(10, 25, W - 15, 88, 7, 0x1082);

    // ORDER #48 defect (e): alert banner is NO LONGER drawn in the card
    // area — it does not steal a row.  It is rendered in the footer below.

    // --- rows ---
    // For credit/free kinds: no bar is drawn; rows show their text right-aligned.
    // For plan kind: bar + percent + reset.
    bool hasBar = (plan.kind == usage::KIND_PLAN);
    int16_t barTrackX = 77;   // 72 + 5 (stripe shift)
    int16_t barTrackW = 100;
    int16_t rightPad  = 12;   // right margin for text

    for (uint8_t i = 0; i < plan.lineCount; i++) {
        const usage::Line& line = plan.lines[i];
        int16_t rowY = 29 + i * 17;
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
            // ORDER #48 defect (f): at 100% the fill must exactly equal the
            // track width.  pct is 0..100 and barTrackW is 100, so fillW ==
            // pct == barTrackW at 100%.
            int fillW = (int)line.pct;
            if (fillW < 0) fillW = 0;
            if (fillW > barTrackW) fillW = barTrackW;
            M5.Display.fillRect(barTrackX, barY, fillW, 8, barColor);
            maxPx = (W - barTrackX - rightPad) - fillW;
        }

        // Right text.
        char rightText[32];
        if (hasBar && line.pct >= 0) {
            // ORDER #53 / task 52: at 100% (3-digit pct) the bar consumes most
            // of the row width, so "100% 21:59" truncates to "100% 21..".
            // Omit the reset and show just "100%" — it always fits.
            if (line.pct >= 100) {
                std::snprintf(rightText, sizeof(rightText), "%d%%",
                              (int)line.pct);
            } else {
                std::snprintf(rightText, sizeof(rightText), "%d%% %s",
                              (int)line.pct, line.right);
            }
        } else {
            std::snprintf(rightText, sizeof(rightText), "%s", line.right);
        }
        fitPrintRight(W - rightPad, textY, rightText, maxPx);
    }

    // --- footer (y = 116..131) ------------------------------------------------
    // ORDER #48 defect (e) + ORDER #51: alerts live HERE, not in the card area.
    // BUG 52: the footer is the DEVICE's own state slot.  When there is a
    // crit banner, draw it with alert colours.  When WiFi is down, say
    // "no hub".  Otherwise, show the version on the left and the button
    // hint on the right.
    //
    // ORDER #59 task 62: ONE baseline for all footer branches.  The footer
    // rect is at y=116, height=16; the cursor Y is computed from the rect
    // and the runtime font height so the text cannot drift (the old code had
    // three literals: 118, 120, 120, and 118 was 2 px too high).  Horizontal
    // centring is within the rect (x=5, W-10) — not the screen midpoint,
    // which only coincidentally shared it.
    int16_t footerY = 116;
    int16_t footerH = 16;
    int16_t footerX = 5;
    int16_t footerW = W - 10;
    int16_t fontH = M5.Display.fontHeight();
    int16_t footerBaseline = usage::centerTextY(footerY, footerH, fontH);

    if (plan.bannerTier == 2 && plan.banner[0] != '\0') {
        // Crit alert: full-width red, white text, centred within the rect.
        M5.Display.fillRect(footerX, footerY, footerW, footerH, 0xF800);
        M5.Display.setTextColor(TFT_WHITE);
        int16_t fw = M5.Display.textWidth(plan.banner);
        M5.Display.setCursor(
            usage::centerTextX(footerX, footerW, fw),
            footerBaseline);
        M5.Display.println(plan.banner);
    } else if (!wifiOk) {
        // Device cannot reach the hub.
        M5.Display.setTextColor(0xFD20);  // amber
        const char* fh = "no hub";
        int16_t fw = M5.Display.textWidth(fh);
        M5.Display.setCursor(
            usage::centerTextX(footerX, footerW, fw),
            footerBaseline);
        M5.Display.println(fh);
    } else {
        // Normal: left-aligned version, right-aligned hint.
        // ORDER #56/57 task 60: seq dropped from footer entirely; hint uses
        // whole words ("click hold" = click refreshes, hold flips).  On
        // collision footerCompute shortens the hint first, then would drop
        // seq (kept for future longer left strings), but the version is NEVER
        // shortened.
        const char* hint = "blue: page   side: click hold";

        usage::FooterLayout fl;
        usage::footerCompute(fl, W, "", plan.buildId, hint, measureText);

        M5.Display.setTextColor(0xFD20);  // amber
        M5.Display.setCursor(fl.leftX, footerBaseline);
        M5.Display.println(fl.left);
        if (fl.hintDrawn) {
            M5.Display.setCursor(fl.hintX, footerBaseline);
            M5.Display.println(fl.hint);
        }
    }
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
void drawBatteryGauge(int16_t gaugeX, int16_t gaugeY, int16_t labelY,
                      int pct, bool onUsb, bool known) {
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
        uint8_t fillW = (topbar::kGaugeW * pct) / 100;
        if (fillW < 1) fillW = 1;  // always at least 1px when >0%
        if (fillW > topbar::kGaugeW) fillW = topbar::kGaugeW;  // clamp to track width
        M5.Display.fillRect(gaugeX + 1, gaugeY + 1, fillW, topbar::kGaugeH - 2, color);
    }

    // Outline rectangle with nub.
    M5.Display.drawRect(gaugeX, gaugeY, topbar::kGaugeW, topbar::kGaugeH, color);
    // Nub (3x5 at right side, centred vertically on the gauge).
    M5.Display.fillRect(gaugeX + topbar::kGaugeW, gaugeY + 4, topbar::kNubW, 5, color);

    // Numeric label to the left of the gauge, at the text baseline.
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
    M5.Display.setCursor(gaugeX - labelW - 2, labelY);
    M5.Display.println(label);
}

// --- brightness gauge overlay (ORDER #72 task 72) ---------------------------

// drawBrightnessGauge paints a full-screen brightness overlay:
//   - Black background (full screen).
//   - "brightness N%" at the top, centred.
//   - A 200px-wide track (y≈70), grey outline, segmented green fill.
//   - Six evenly-spaced tick labels (5/10/25/50/75/100) under the track.
//   - A filled pip at the current level's tick.
//   - An amber pip at the idle dim position (nearest level).
//
// ORDER #74 task 74: ticks are spaced evenly by INDEX, not by raw PWM value
// (the raw ladder 13, 26, 64, 128, 191, 255 is non-linear and piles up labels
// on the left).  Fill is segmented by index to match.
void drawBrightnessGauge(uint8_t currentRaw, uint8_t currentPercent,
                         uint8_t idleRaw, uint8_t levelIdx) {
    M5.Display.fillScreen(TFT_BLACK);
    int16_t W = M5.Display.width();
    int16_t H = M5.Display.height();

    M5.Display.setTextSize(1);
    M5.Display.setTextColor(TFT_WHITE);

    // Title at the top, centred.
    char title[24];
    std::snprintf(title, sizeof(title), "brightness %d%%", (int)currentPercent);
    int16_t tw = M5.Display.textWidth(title);
    M5.Display.setCursor((W - tw) / 2, 4);
    M5.Display.println(title);

    // Track geometry: 200px wide, 12px tall, centred horizontally.
    int16_t trackW = 200;
    int16_t trackH = 12;
    int16_t trackX = (W - trackW) / 2;
    int16_t trackY = 70;
    int16_t trackInnerW = trackW - 2;  // inner width (between border pixels)

    // Grey track outline.
    M5.Display.drawRect(trackX, trackY, trackW, trackH, 0x8410);

    // Current level index for fill and pip.
    uint8_t curIdx = levelIdx;
    if (curIdx >= sticks3::brightness::kLevelCount) curIdx = 0;

    // Segmented fill: segments 0..curIdx-1 are filled (green).
    int fillW = sticks3::brightness::BrightnessController::gaugeFillWidth(
        curIdx, trackInnerW);
    if (fillW > 0) {
        M5.Display.fillRect(trackX + 1, trackY + 1, fillW, trackH - 2, 0x07E0);
    }

    // Idle marker: amber pip at the nearest level tick.
    uint8_t idleIdx = sticks3::brightness::BrightnessController::gaugeIdleTick(idleRaw);
    int idleTickX = trackX + 1 + sticks3::brightness::BrightnessController::gaugeTickX(
        idleIdx, trackInnerW);
    if (idleRaw > 0) {
        // Pip: 3px dot centred on the tick.
        M5.Display.fillRect(idleTickX - 1, trackY + (trackH - 3) / 2, 3, 3, 0xFD20);
    }

    // Level tick labels under the track, evenly spaced by index.
    // Use gaugeTickX (not segW * i) for exact integer-division consistency
    // with the pips above — segW*i truncates early and drifts from gaugeTickX.
    M5.Display.setTextColor(0x8410);  // dim grey for labels
    for (int i = 0; i < sticks3::brightness::kLevelCount; i++) {
        char lbl[8];
        std::snprintf(lbl, sizeof(lbl), "%d%%",
                      sticks3::brightness::kLevels[i].percent);
        int tickX = trackX + 1 +
            sticks3::brightness::BrightnessController::gaugeTickX(i, trackInnerW);
        int16_t lw = M5.Display.textWidth(lbl);
        M5.Display.setCursor(tickX - lw / 2, trackY + trackH + 4);
        M5.Display.println(lbl);
    }

    // Current level pip: white dot at the current tick, drawn last so it's
    // visible on top of the fill.
    int curTickX = trackX + 1 +
        sticks3::brightness::BrightnessController::gaugeTickX(curIdx, trackInnerW);
    M5.Display.fillRect(curTickX - 1, trackY + (trackH - 3) / 2, 3, 3, TFT_WHITE);
}

} // namespace sticks3
