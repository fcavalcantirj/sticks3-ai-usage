// firmware/src/usage/topbar.h — pure-C++17 top-bar layout computation.
//
// The 22-pixel top bar (y=0..21) on the 240×135 StickS3 LCD packs these
// items right-to-left from x = W-4:
//
//   [wifi dot r=3] [battery: label + gauge 32×13 + nub] [version]
//   [page indicator] [seq N] [title — left-aligned]
//
// 4 px gaps between items.  Every item is vertically centred on the bar
// midline (y=11).  Text baseline = y=7 (8-px font, (22-8)/2=7).  Gauge top
// = y=4 (13-px tall, (22-13)/2=4.5→4).  Nothing crosses y=21.
//
// Items are dropped right-to-left when space is tight: seq first, then the
// title text.  The version and battery are never dropped.
//
// This module is host-testable (no M5/Arduino headers).  screen.cpp
// consumes the computed Layout to draw at the exact positions.
#pragma once

#include <cstdint>
#include <cstddef>

namespace sticks3 {
namespace topbar {

// Vertical layout constants (see file header).
static const int16_t kBarH    = 22;   // top bar height in pixels
static const int16_t kTextY   = 7;    // 8-px font baseline, centred in 22px bar
static const int16_t kDotY    = 11;   // wifi dot centre y
static const int16_t kGaugeY  = 4;    // battery gauge top y (13px tall)
static const int16_t kGap     = 4;    // gap between items
static const int16_t kRightMargin = 4;
static const int16_t kGaugeW  = 32;   // battery gauge body width
static const int16_t kGaugeH  = 13;   // battery gauge height
static const int16_t kNubW    = 3;    // battery gauge nub width

// Position of one top-bar item.  x is the left edge (or centre for the
// wifi dot); w is the width (or diameter for the dot).  drawn=false means
// the item was dropped by the packer.
struct Item {
    int16_t x;
    int16_t w;
    bool drawn;
};

// Computed layout for the entire top bar.
struct Layout {
    int16_t W;           // display width
    Item wifiDot;        // always drawn; x = centre, w = diameter (6)
    Item battery;        // always drawn; x = gauge left, w = gauge+nub width (35)
    Item battLabel;      // always drawn; x = label left, w = label width
    Item version;        // always drawn
    Item pageInd;        // drawn only when pageCount > 1
    Item asOf;           // drawn only when it fits
    Item title;          // drawn only when it fits
};

// compute fills `out` with the packed positions for a W-wide screen.
// buildId, asOf, and title are NUL-terminated strings.  batt provides the
// battery state for the label.  `measure` returns the pixel width of a string
// in the current font (screen.cpp passes M5.Display.textWidth; tests pass a
// fixed-width simulator).
void compute(Layout& out, int16_t W, const char* buildId,
             uint8_t page, uint8_t pageCount, const char* asOf,
             const char* title, int battPct, bool battOnUsb, bool battKnown,
             int (*measure)(const char*));

// batteryLabelText formats the battery label into buf (same logic as
// battery::batteryLabel, inlined here to avoid a cross-module dependency
// for the measure step).
void batteryLabelText(int battPct, bool battOnUsb, bool battKnown,
                      char* out, size_t n);

} // namespace topbar
} // namespace sticks3
