// firmware/src/usage/brightness.h — pure-C++17 brightness level controller.
//
// No M5/Arduino headers.  The HAL (firmware/src/hal/sticks3/board.cpp)
// persists the active level to NVS; main.cpp drives the controller from
// button events and updateBrightness() reads rawLevel()/idleRaw() to set
// the backlight.  Host-tested by test_brightness.cpp.
//
// Design (ORDER #72, task 72):
//   - Discrete levels 5/10/25/50/75/100% mapped onto the 0-255 range
//     M5GFX accepts.  25% (raw 64) is the default — Felipe's starting point.
//   - Brightness MODE: entered by double-clicking BtnA (wasDoubleClicked).
//     Inside the mode, BtnA wasSingleClicked = stepUp, BtnB click = stepDown.
//     BtnB HOLD still flips the screen (the hold_flip detector keeps running
//     inside the mode).  Exits after kModeTimeoutMs of inactivity.
//   - BtnA HOLD is RESERVED for a future AI-agent action — never bound to
//     refresh.  The 600 ms threshold is too short for a deliberate hold;
//     the click detector fires first (ORDER #74 task 74 diagnosis).
//   - The user's chosen level becomes the ACTIVE level; the idle dim level
//     is active / 4, clamped to >= 1 — so choosing 5% still dims further.
//   - Saves on exit, not on every press, to minimise NVS wear.
#pragma once

#include <cstdint>

namespace sticks3 {
namespace brightness {

// A discrete brightness step: percent (1-100) and the raw 0-255 value.
struct Level {
    uint8_t percent;
    uint8_t raw;
};

// The fixed ladder of brightness levels, ascending.
// 5%→13, 10%→26, 25%→64, 50%→128, 75%→191, 100%→255.
static constexpr Level kLevels[] = {
    {  5,  13},
    { 10,  26},
    { 25,  64},
    { 50, 128},
    { 75, 191},
    {100, 255},
};
static constexpr int kLevelCount = 6;
static constexpr uint8_t kDefaultLevelIdx = 2;  // 25%
static constexpr uint32_t kModeTimeoutMs = 5000;  // exit after 5s inactivity
static constexpr uint8_t kIdleDivisor = 4;

class BrightnessController {
public:
    BrightnessController();

    // Set the active level by raw value.  Finds the closest discrete level
    // so a persisted raw from NVS (which might be a saved level) lands on the
    // ladder.  Validates out-of-range values to kDefaultLevelIdx.
    void setLevel(uint8_t raw);

    // Set the active level by index (used by loadBrightness validation).
    void setLevelIdx(uint8_t idx);

    // Enter brightness adjustment mode (reset the inactivity timer).
    void enterMode(uint32_t nowMs);

    // Exit brightness adjustment mode.
    void exitMode();

    // Step up / down the ladder, clamped at both ends.  Resets the
    // inactivity timer so the gauge stays visible while adjusting.
    void stepUp(uint32_t nowMs);
    void stepDown(uint32_t nowMs);

    // Returns true after kModeTimeoutMs of inactivity since enterMode() or
    // the last step.  Caller should call exitMode() when this returns true.
    bool shouldExit(uint32_t nowMs) const;

    // True while in brightness adjustment mode.
    bool inMode() const;

    // Current active raw level (for setBrightness()).
    uint8_t rawLevel() const;

    // Current percentage (0-100), for display.
    uint8_t percent() const;

    // Idle dim level: active / kIdleDivisor, clamped to >= 1 so the screen
    // is never fully dark but always dimmer than the active level.
    uint8_t idleRaw() const;

    // Index of the current level (0-based, for NVS persistence).
    uint8_t levelIdx() const;

    // --- gauge layout (ORDER #74 task 74) ---
    //
    // The brightness gauge is a DISCRETE stepper, so tick positions are
    // evenly spaced by INDEX, not by raw PWM value.  The raw ladder
    // (13, 26, 64, 128, 191, 255) is non-linear and causes left-side labels
    // to pile up.  Fill is also segmented by index — a proportional bar
    // under evenly-spaced labels would be internally inconsistent.

    // gaugeTickX returns the x-offset (from trackX+1) of the tick mark for
    // level index i.  Positions are evenly spaced across innerW:
    // 0, innerW/5, 2*innerW/5, …, innerW.  The last tick lands exactly at
    // innerW so 100% does not overhang.
    static int gaugeTickX(int levelIdx, int innerW) {
        int denom = kLevelCount - 1;  // 5
        return (innerW * levelIdx) / denom;
    }

    // gaugeSegmentW returns the uniform pixel gap between adjacent level ticks
    // for the given inner track width.  Ticks are evenly spaced by index, so
    // the segment width is innerW / (kLevelCount - 1).
    static int gaugeSegmentW(int innerW) {
        int denom = kLevelCount - 1;  // 5
        return innerW / denom;
    }

    // gaugeFillWidth returns the fill width for the given level index.
    // Equivalent to gaugeTickX — fill extends to the current level's tick.
    // At levelIdx=0 the fill is 0; at the top level it equals innerW.
    static int gaugeFillWidth(int levelIdx, int innerW) {
        int w = gaugeTickX(levelIdx, innerW);
        if (w < 0) w = 0;
        if (w > innerW) w = innerW;
        return w;
    }

    // gaugeIdleTick returns the level index whose raw value is closest to
    // idleRaw, for positioning the idle marker.  Clamped to 0..kLevelCount-1.
    static uint8_t gaugeIdleTick(uint8_t idleRaw_);

private:
    uint8_t levelIdx_;
    bool inMode_;
    uint32_t lastActivityMs_;  // last step or enterMode timestamp
};

} // namespace brightness
} // namespace sticks3
