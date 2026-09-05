// firmware/src/usage/hold_flip.h — pure C++ BtnB hold-to-flip state machine.
//
// No M5/Arduino headers.  The HAL polls M5.BtnB and calls feed() with the
// current button state and a monotonic timestamp.  The machine decides
// whether a release is a click (refresh) or a hold (flip 180°), and signals
// when a hold hint should appear (~500 ms into the hold).
//
// Design (ORDER #53 REVISED):
//   - BtnB click (release < kHoldThresholdMs) → FlipAction::click (refresh)
//   - BtnB hold (hold >= kHoldThresholdMs)    → FlipAction::flip
//   - One 1500 ms threshold governs click AND hold on the same button.
//   - Hint appears at kHintDelayMs (500 ms) while still holding.
//   - Rotation toggles 1 <-> 3 on each flip; the caller persists it via NVS.
#pragma once

#include <cstdint>

namespace sticks3 {
namespace holdflip {

// Action the caller should take after feed() returns a decision.
enum class FlipAction {
    none,    // no event this tick
    click,   // button released before kHoldThresholdMs → refresh
    flip,    // button held past kHoldThresholdMs → toggle rotation 180°
    hint,    // hold reached kHintDelayMs → show a visual hint (still holding)
};

// Rotation state: 1 = upright, 3 = 180-degree flip.
// Matches the NVS value used by loadRotation/saveRotation.
enum class Rotation : uint8_t {
    upright = 1,
    flipped = 3,
};

class HoldFlipDetector {
public:
    // holdThresholdMs: time at which a hold becomes a flip (default 1500).
    // hintDelayMs:    time into a hold at which to show the hint (default 500).
    HoldFlipDetector(uint32_t holdThresholdMs = kHoldThresholdMs,
                     uint32_t hintDelayMs = kHintDelayMs);

    // Feed one button sample.  pressed = true if BtnB is currently held down.
    // Returns the action the caller should perform, if any.
    FlipAction feed(bool pressed, uint32_t nowMs);

    // Current rotation (persisted by the caller).  Starts upright.
    Rotation rotation() const { return rotation_; }

    // Toggle rotation and return the new value.
    Rotation toggleRotation();

    // Reset the hold state (e.g. after a flip resolves).  Preserves rotation_.
    void reset();

    // Constants (public so tests and main.cpp can reference them).
    static const uint32_t kHoldThresholdMs = 1500;
    static const uint32_t kHintDelayMs = 500;

private:
    uint32_t holdThresholdMs_;
    uint32_t hintDelayMs_;
    bool wasPressed_;       // previous button state
    bool holdConsumed_;     // true once a flip has fired for this press
    bool hintShown_;        // true once the hint has been emitted for this press
    uint32_t pressStartMs_;  // when the current press began
    Rotation rotation_;
};

} // namespace holdflip
} // namespace sticks3
