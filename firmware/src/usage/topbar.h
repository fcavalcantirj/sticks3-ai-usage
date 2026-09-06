// firmware/src/usage/topbar.h — pure-C++17 top-bar layout computation.
//
// The 22-pixel top bar (y=0..21) on the 240×135 StickS3 LCD packs these
// items right-to-left from x = W-4:
//
//   [freshness dot r=3] [gap 8px] [wifi bars 3×1px] [gap 4px]
//   [battery: label + gauge 32×13 + nub] [page indicator] [title — x=5]
//
// 4 px gaps between items (8 px between freshness dot and battery — at 4 they
// read as one blob).  Every item is vertically centred on the bar midline
// (y=11).  Text baseline = y=7 (8-px font, (22-8)/2=7).  Gauge top = y=4
// (13-px tall, (22-13)/2=4.5→4).  Nothing crosses y=21.
//
// The page indicator is CENTRED in the free span between the title's right
// edge and the battery unit's left edge (ORDER #56 task 60: it was stranded
// against the cluster, leaving ~80 px of dead space).  It is dropped before
// the title when there is no room.
//
// ORDER #51: version and seq moved to the footer — they are no longer
// computed in this packer.
//
// ORDER #65: the right-most dot is now a FRESHNESS lamp (green/yellow/red
// from the data-age tier), not a Wi-Fi link indicator.  The link state gets
// its own SHAPE — three ascending vertical bars — placed immediately LEFT of
// the freshness dot so the dot keeps the far-right corner.  When space runs
// out the Wi-Fi bars are dropped first (lower drop precedence than the dot),
// because a stale-data warning matters more than knowing why.
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
static const int16_t kDotY    = 11;   // freshness dot centre y
static const int16_t kGaugeY  = 4;    // battery gauge top y (13px tall)
static const int16_t kGap     = 4;    // gap between items
static const int16_t kGapWifi = 8;    // wider gap between freshness dot and battery (ORDER #51)
static const int16_t kRightMargin = 4;
static const int16_t kGaugeW  = 32;   // battery gauge body width
static const int16_t kGaugeH  = 13;   // battery gauge height
static const int16_t kNubW    = 3;    // battery gauge nub width

// Wi-Fi signal bars geometry (ORDER #65).
static const int16_t kWifiBarsW = 7;  // 3 bars × 1px + 2 gaps × 2px
static const int16_t kWifiBarHeights[3] = {2, 4, 6}; // ascending heights
static const int16_t kWifiBarGap = 2; // pitch between bar centres
static const int16_t kWifiBarsH = 6;  // tallest bar height

// Position of one top-bar item.  x is the left edge (or centre for the
// freshness dot); w is the width (or diameter for the dot).  drawn=false means
// the item was dropped by the packer.
struct Item {
    int16_t x;
    int16_t w;
    bool drawn;
};

// Computed layout for the entire top bar.
struct Layout {
    int16_t W;            // display width
    Item freshnessDot;    // always drawn; x = centre, w = diameter (6)
    Item wifiBars;        // drawn when wifiOk has room; x = left, w = kWifiBarsW
                          // DROPPED FIRST when space runs out (lower precedence than dot)
    Item battery;         // always drawn; x = gauge left, w = gauge+nub width (35)
    Item battLabel;       // always drawn; x = label left, w = label width
    Item pageInd;         // drawn only when pageCount > 1
    Item title;           // drawn only when it fits
};

// compute fills `out` with the packed positions for a W-wide screen.
// buildId, asOf, and title are NUL-terminated strings.  batt provides the
// battery state for the label.  `measure` returns the pixel width of a string
// in the current font (screen.cpp passes M5.Display.textWidth; tests pass a
// fixed-width simulator).
// ORDER #51: buildId and asOf are no longer in the header — they moved to the
// footer.  Only title, page indicator, battery, wifi bars and the freshness
// dot remain.  ORDER #65: freshnessTier (0=green,1=yellow,2=red) controls the
// dot colour; wifiOk controls whether the Wi-Fi bars are solid or dimmed.
void compute(Layout& out, int16_t W,
             uint8_t page, uint8_t pageCount,
             const char* title, int battPct, bool battOnUsb, bool battKnown,
             bool wifiOk, uint8_t freshnessTier,
             int (*measure)(const char*));

// batteryLabelText formats the battery label into buf (same logic as
// battery::batteryLabel, inlined here to avoid a cross-module dependency
// for the measure step).
void batteryLabelText(int battPct, bool battOnUsb, bool battKnown,
                      char* out, size_t n);

// freshnessColor returns the RGB565 colour for a freshness tier.
// 0=green, 1=yellow, 2=red.
uint16_t freshnessColor(uint8_t tier);

} // namespace topbar
} // namespace sticks3
