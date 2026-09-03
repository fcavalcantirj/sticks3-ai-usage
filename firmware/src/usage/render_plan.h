// firmware/src/usage/render_plan.h — page layout and change detection for the
// 135×240 StickS3 LCD (240×135 landscape, 5 visible rows per page).
#pragma once

#include "model.h"

#include <cstdint>

namespace usage {

// Line: one rendered row on the 5-line screen.
struct Line {
    char left[11];       // row label, ≤10 chars + NUL
    int16_t pct;          // -1 = null (no percentage bar)
    char right[12];       // txt, ≤11 chars + NUL
    uint8_t tier;         // 0 ok, 1 warn, 2 crit, 3 off
    uint8_t dim;          // 1 when provider is stale/auth/error
};

// RenderPlan: the full frame buffer for one screen page.
struct RenderPlan {
    char title[12];       // "AI USAGE"
    char asOf[12];        // "seq N" — device has no clock
    uint8_t page;         // 1-indexed for display
    uint8_t pageCount;
    uint8_t lineCount;
    Line lines[5];        // max 5 lines per page
    char footer[25];      // msg of first non-ok provider on this page, else ""
};

// View: persistent UI state across snapshots.
struct View {
    char lastRev[9];       // 8 hex chars + NUL
    uint8_t page;          // 0-indexed internally
    bool needsRedraw;
};

// buildPlan fills in the render plan for the given 0-indexed page.
// Page 0 = claude + codex rows (max 5, excluding bal rows).
// Page 1 = openrouter + groq rows (max 5).
// pageCount = 2 when any provider beyond codex exists, 1 otherwise.
void buildPlan(const Model& model, uint8_t page, RenderPlan& out);

// onSnapshot updates the view: sets needsRedraw only if rev changed
// (and stores the new rev).  Called on every poll.
void onSnapshot(View& view, const Model& model);

// nextPage cycles to the next page and marks needsRedraw.
void nextPage(View& view, const Model& model);

// tierName returns the human-readable tier string.
const char* tierName(uint8_t tier);

// tierColor565 returns the RGB565 color for a tier, optionally dimmed
// (dim halves each 565 channel to signal stale/auth/error providers).
//   ok   0x3B9F blue   warn 0xFD20 amber   crit 0xF800 red   off 0x8410 grey
uint16_t tierColor565(uint8_t tier, bool dim);

} // namespace usage
