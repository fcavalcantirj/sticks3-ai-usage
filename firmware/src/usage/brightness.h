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

private:
    uint8_t levelIdx_;
    bool inMode_;
    uint32_t lastActivityMs_;  // last step or enterMode timestamp
};

} // namespace brightness
} // namespace sticks3
