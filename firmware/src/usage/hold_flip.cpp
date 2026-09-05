// firmware/src/usage/hold_flip.cpp — pure C++ BtnB hold-to-flip state machine.
// No M5/Arduino headers.  See hold_flip.h for the design.
#include "usage/hold_flip.h"

namespace sticks3 {
namespace holdflip {

HoldFlipDetector::HoldFlipDetector(uint32_t holdThresholdMs, uint32_t hintDelayMs)
    : holdThresholdMs_(holdThresholdMs),
      hintDelayMs_(hintDelayMs),
      wasPressed_(false),
      holdConsumed_(false),
      hintShown_(false),
      pressStartMs_(0),
      rotation_(Rotation::upright) {}

FlipAction HoldFlipDetector::feed(bool pressed, uint32_t nowMs) {
    // --- rising edge: button just pressed ---
    if (pressed && !wasPressed_) {
        pressStartMs_ = nowMs;
        holdConsumed_ = false;
        hintShown_ = false;
        wasPressed_ = true;
        return FlipAction::none;
    }

    // --- button is held down ---
    if (pressed && wasPressed_) {
        uint32_t heldMs = nowMs - pressStartMs_;

        // Hint: emit once when we cross the hint delay.
        if (!hintShown_ && heldMs >= hintDelayMs_) {
            hintShown_ = true;
            return FlipAction::hint;
        }

        // Flip: emit once when we cross the hold threshold.
        if (!holdConsumed_ && heldMs >= holdThresholdMs_) {
            holdConsumed_ = true;
            rotation_ = toggleRotation();
            return FlipAction::flip;
        }

        return FlipAction::none;
    }

    // --- falling edge: button released ---
    if (!pressed && wasPressed_) {
        wasPressed_ = false;
        uint32_t heldMs = nowMs - pressStartMs_;

        // If the hold threshold was never crossed, this is a click.
        if (heldMs < holdThresholdMs_) {
            return FlipAction::click;
        }
        // Otherwise the flip already fired; release is a no-op.
        return FlipAction::none;
    }

    return FlipAction::none;
}

Rotation HoldFlipDetector::toggleRotation() {
    rotation_ = (rotation_ == Rotation::upright)
                    ? Rotation::flipped
                    : Rotation::upright;
    return rotation_;
}

void HoldFlipDetector::reset() {
    wasPressed_ = false;
    holdConsumed_ = false;
    hintShown_ = false;
    pressStartMs_ = 0;
    // rotation_ is preserved across resets (persisted in NVS).
}

} // namespace holdflip
} // namespace sticks3
