// firmware/src/usage/gesture.cpp — pure IMU double-tap state machine.
// No M5/Arduino headers.  See gesture.h for the design.
#include "usage/gesture.h"

#include <cmath>

namespace sticks3 {
namespace gesture {

DoubleTapDetector::DoubleTapDetector(const Threshold& t)
    : t_(t),
      inSpike(false),
      spikeStartMs(0),
      lastSpikeEndMs(0),
      lastTapEndMs(0),
      firstTapSeen(false),
      lockoutUntilMs(0),
      rotation_(Rotation::upright) {}

float DoubleTapDetector::magnitudeG(float ax, float ay, float az) {
    // Convert from m/s² to g-units (standard gravity = 9.80665 m/s²).
    float mag = std::sqrt(ax * ax + ay * ay + az * az);
    return mag / 9.80665f;
}

Gesture DoubleTapDetector::feed(float ax, float ay, float az, uint32_t nowMs) {
    // If we're in a lockout window, ignore all input until it expires.
    if (nowMs < lockoutUntilMs) {
        // Still track spikes for the rising edge, but don't start new taps.
        inSpike = false;
        firstTapSeen = false;
        return Gesture::none;
    }

    float mag = magnitudeG(ax, ay, az);

    // --- spike detection: sharp transient above spikeG, lasting < spikeMs ---
    if (mag > t_.spikeG) {
        if (!inSpike) {
            // Rising edge of a new spike.
            inSpike = true;
            spikeStartMs = nowMs;
        }
        // In a spike; check duration — if it exceeds spikeMs, it's a slow ramp
        // (e.g. picking the device up), not a sharp tap.  Reset and wait.
        if ((int32_t)(nowMs - spikeStartMs) > (int32_t)t_.spikeMs) {
            // Spike lasted too long → slow ramp, ignore.
            inSpike = false;
            firstTapSeen = false;
        }
    } else {
        if (inSpike) {
            // Falling edge: spike ended.  Record the end time as a "tap".
            inSpike = false;
            lastSpikeEndMs = nowMs;

            if (firstTapSeen) {
                // Second tap in a potential pair — check the gap window.
                uint32_t gap = (uint32_t)(nowMs - lastTapEndMs);
                if (gap >= t_.gapMinMs && gap <= t_.gapMaxMs) {
                    // Double-tap confirmed!
                    firstTapSeen = false;
                    lockoutUntilMs = nowMs + t_.lockoutMs;
                    rotation_ = toggleRotation();
                    return Gesture::double_tap;
                } else {
                    // Gap out of range — treat this tap as the new "first".
                    lastTapEndMs = nowMs;
                    // firstTapSeen stays true (this is now the first of a new pair)
                }
            } else {
                // First tap in a potential pair.
                firstTapSeen = true;
                lastTapEndMs = nowMs;
            }
        }
    }

    return Gesture::none;
}

Rotation DoubleTapDetector::toggleRotation() {
    rotation_ = (rotation_ == Rotation::upright)
                    ? Rotation::flipped
                    : Rotation::upright;
    return rotation_;
}

void DoubleTapDetector::reset() {
    // Clear transient spike/tap state but preserve rotation_.
    inSpike = false;
    spikeStartMs = 0;
    lastSpikeEndMs = 0;
    firstTapSeen = false;
    lastTapEndMs = 0;
    lockoutUntilMs = 0;
}

} // namespace gesture
} // namespace sticks3
