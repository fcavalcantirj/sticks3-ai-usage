// firmware/src/usage/render_plan.h — page layout and change detection for the
// 135×240 StickS3 LCD (240×135 landscape, 5 visible rows per page).
//
// ORDER #36 / task 51: pages are grouped by provider KIND (plan/credit/free),
// one page per kind that has at least one row, in the fixed order plan, credit,
// free.  A kind with no rows is skipped (does not consume a page number).
// More than 5 rows overflow onto another page of the same kind.
#pragma once

#include "model.h"

#include <cstdint>
#include <cstddef>

namespace usage {

// Kind identifies a provider's billing category — drives the firmware's
// page-grouping logic.  Mirrors the Go snapshot's "kind" field.
static const uint8_t KIND_PLAN   = 1;  // paid subscription with quota windows
static const uint8_t KIND_CREDIT = 2;  // prepaid balance that real money drains
static const uint8_t KIND_FREE   = 3;  // no per-request cost

// Line: one rendered row on the 5-line screen.
struct Line {
    char left[11];       // row label, ≤10 chars + NUL
    int16_t pct;          // -1 = null (no percentage bar)
    char right[12];       // txt, ≤11 chars + NUL
    uint8_t tier;         // 0 ok, 1 warn, 2 crit, 3 off
    uint8_t dim;          // 1 when provider is stale/auth/error
    uint8_t warn;         // 1 when provider severity is warn (tint label amber)
};

// RenderPlan: the full frame buffer for one screen page.
struct RenderPlan {
    char title[12];       // "PLANS", "CREDITS", "FREE" (was "AI USAGE")
    char asOf[12];        // "seq N" — device has no clock
    char buildId[13];     // "v<buildid>" — visible without serial
    uint8_t kind;         // KIND_PLAN / KIND_CREDIT / KIND_FREE
    uint16_t kindColor;   // RGB565 accent for stripe + title
    uint8_t page;         // 1-indexed for display
    uint8_t pageCount;    // total pages across all kinds
    uint8_t freshnessTier; // ORDER #65: 0=green, 1=yellow, 2=red from data age
    uint8_t lineCount;
    Line lines[5];        // max 5 lines per page (4 when crit banner active)
    char footer[25];      // ORDER #51+57: when no crit banner, just the version
                          //     "v<sha>" (seq dropped entirely — ORDER #57 task 60).
                          //     When crit, the banner text (full-width alert).
                          //     screen.cpp left-aligns this and right-aligns the hint.
    char banner[25];      // crit/warn banner text (24 chars + NUL), "" when none
    uint8_t bannerTier;   // 0 none, 2 crit (warn uses per-row tint, not banner)
};

// View: persistent UI state across snapshots.
struct View {
    char lastRev[9];       // 8 hex chars + NUL
    uint8_t page;          // 0-indexed internally
    bool needsRedraw;
};

// FooterLayout: the computed positions and fitted strings for the footer row
// (ORDER #56 task 60).  The footer is a left-aligned "seq N · v<sha>" and a
// right-aligned hint.  On collision the hint is shortened first, then the seq,
// but the version is NEVER shortened.
struct FooterLayout {
    char left[48];       // fitted left string: "seq N · v<sha>" or just "v<sha>"
    char hint[64];       // fitted hint string (possibly shortened or empty)
    int16_t leftX;       // x for left string (always 5)
    int16_t hintX;       // x for hint string (right-aligned)
    bool hintDrawn;      // false when the hint was dropped entirely
};

// buildPlan fills in the render plan for the given 0-indexed page.
// The buildId (git short sha, or "unknown") is rendered as "v<buildId>" in
// the FOOTER (ORDER #51: moved from the top bar) so an OTA is visible without
// serial.
//
// Pages are grouped by kind (plan → credit → free), one page per kind with
// at least one row.  More than 5 rows (4 with a crit banner) overflow onto
// additional pages of the same kind.
void buildPlan(const Model& model, uint8_t page, const char* buildId,
               RenderPlan& out);

// countPages returns the total number of kind-grouped pages for the model.
uint8_t countPages(const Model& model);

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

// footerCompute lays out the footer row: left-aligned "seq N · v<sha>" and
// right-aligned hint, with measure-then-fit shrinking.  On collision the
// hint is shortened first (via fitRight), then the seq prefix is dropped
// (keeping only the version), but the version is NEVER shortened.
// W is the display width; measure returns the pixel width of a string.
void footerCompute(FooterLayout& out, int16_t W,
                   const char* asOf, const char* buildId,
                   const char* hint,
                   int (*measure)(const char*));

} // namespace usage
