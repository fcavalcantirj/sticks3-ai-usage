// firmware/src/usage/align.h — pixel-perfect text centring helpers.
//
// Pure C++17, no Arduino/M5 headers.  Given a rectangle and the runtime font
// height (or measured text width), these compute the cursor position that
// centres text within the rectangle.  They replace the hardcoded y literals
// and screen-midpoint centring that ORDER #59 (task 62) found were drifting
// out of alignment.
//
// screen.cpp calls these at draw time; the host test suite validates the
// arithmetic independently so the firmware never has to "eyeball it".
#pragma once

#include <cstdint>

namespace usage {

// centerTextY returns the cursor Y that vertically centres `fontH` pixels of
// text inside a rectangle at `rectY` of height `rectH`.
//
// When the font is taller than the rect (fontH > rectH) the slack is clamped
// to zero so the text top aligns with the rect top — the cursor never goes
// negative.
inline int16_t centerTextY(int16_t rectY, int16_t rectH, int16_t fontH) {
    int16_t slack = rectH - fontH;
    if (slack < 0) {
        slack = 0; // font taller than rect: clamp to rect top
    }
    return rectY + slack / 2;
}

// centerTextX returns the cursor X that horizontally centres a string of
// width `textW` inside a rectangle at `rectX` of width `rectW`.  When the
// text is wider than the rect the string is left-aligned at rectX.
inline int16_t centerTextX(int16_t rectX, int16_t rectW, int16_t textW) {
    int16_t slack = rectW - textW;
    if (slack < 0) {
        slack = 0;
    }
    return rectX + slack / 2;
}

} // namespace usage
