// firmware/src/usage/brightness.cpp — implementation.
// No M5/Arduino headers.  See brightness.h for the design.
#include "usage/brightness.h"

namespace sticks3 {
namespace brightness {

BrightnessController::BrightnessController()
    : levelIdx_(kDefaultLevelIdx),
      inMode_(false),
      lastActivityMs_(0) {}

void BrightnessController::setLevel(uint8_t raw) {
    // Find the closest discrete level by raw value.
    uint8_t bestIdx = kDefaultLevelIdx;
    uint8_t bestDiff = 255;
    for (int i = 0; i < kLevelCount; i++) {
        uint8_t diff = (raw >= kLevels[i].raw)
                           ? (raw - kLevels[i].raw)
                           : (kLevels[i].raw - raw);
        if (diff < bestDiff) {
            bestDiff = diff;
            bestIdx = (uint8_t)i;
        }
    }
    levelIdx_ = bestIdx;
}

void BrightnessController::setLevelIdx(uint8_t idx) {
    if (idx >= kLevelCount) {
        levelIdx_ = kDefaultLevelIdx;
    } else {
        levelIdx_ = idx;
    }
}

void BrightnessController::stepUp(uint32_t nowMs) {
    if (levelIdx_ < (uint8_t)(kLevelCount - 1)) {
        levelIdx_++;
    }
    lastActivityMs_ = nowMs;
}

void BrightnessController::stepDown(uint32_t nowMs) {
    if (levelIdx_ > 0) {
        levelIdx_--;
    }
    lastActivityMs_ = nowMs;
}

void BrightnessController::enterMode(uint32_t nowMs) {
    inMode_ = true;
    lastActivityMs_ = nowMs;
}

void BrightnessController::exitMode() {
    inMode_ = false;
}

bool BrightnessController::shouldExit(uint32_t nowMs) const {
    if (!inMode_) return false;
    // Unsigned subtraction handles millis() wrap (ORDER #65 pattern).
    return (int32_t)(nowMs - lastActivityMs_) >= (int32_t)kModeTimeoutMs;
}

bool BrightnessController::inMode() const {
    return inMode_;
}

uint8_t BrightnessController::rawLevel() const {
    return kLevels[levelIdx_].raw;
}

uint8_t BrightnessController::percent() const {
    return kLevels[levelIdx_].percent;
}

uint8_t BrightnessController::idleRaw() const {
    uint8_t idle = rawLevel() / kIdleDivisor;
    return idle > 0 ? idle : 1;
}

uint8_t BrightnessController::levelIdx() const {
    return levelIdx_;
}

} // namespace brightness
} // namespace sticks3