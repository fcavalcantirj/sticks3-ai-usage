// firmware/src/usage/gesture.h — pure IMU double-tap state machine.
//
// No M5/Arduino headers.  The HAL polls the BMI270 at ~50 Hz and calls
// feed() with raw acceleration samples; this module decides whether a
// double-tap gesture occurred and tracks the screen rotation state.
//
// DOUBLE TAP detection: two sharp jerk transients (magnitude spike above
// ~2.5 g lasting under 120 ms) separated by 120-500 ms, with a 1 s lockout
// afterwards.  A slow magnitude ramp (picking the device up) must never
// register.  Thresholds are tuned from real captures (see docs/DEVICES.md).
#pragma once

#include <cstdint>

namespace sticks3 {
namespace gesture {

// Detected gesture result.
enum class Gesture {
    none,
    double_tap,
};

// Rotation state: 1 = portrait upright, 3 = 180-degree flip.
// Persisted in NVS by the HAL; this enum is the canonical value.
enum class Rotation : uint8_t {
    upright = 1,
    flipped = 3,
};

// Thresholds (tuned from real captures, see docs/DEVICES.md).
struct Threshold {
    float spikeG;        // magnitude above this counts as a transient  (2.5f)
    uint32_t spikeMs;    // transient must be shorter than this          (120)
    uint32_t gapMinMs;   // min gap between the two taps                (120)
    uint32_t gapMaxMs;   // max gap between the two taps                (500)
    uint32_t lockoutMs;  // lockout after a double-tap fires             (1000)
};

// Default thresholds used in production.
static const Threshold kDefaultThreshold = {
    2.5f,   // spikeG
    120,    // spikeMs
    120,    // gapMinMs
    500,    // gapMaxMs
    1000,   // lockoutMs
};

// DoubleTapDetector is a pure state machine fed with acceleration samples.
// No hardware dependency — host-tested via make fw-test.
class DoubleTapDetector {
public:
    // gravity = 1.0f (in g-units).  Pass trueG = sqrt(ax^2+ay^2+az^2) / 9.81.
    DoubleTapDetector(const Threshold& t = kDefaultThreshold);

    // Feed one accelerometer sample.  Returns Gesture::double_tap exactly once
    // when two qualifying spikes are detected within the gap window, then
    // enters lockout.  Call at the poll rate (~50 Hz).
    Gesture feed(float ax, float ay, float az, uint32_t nowMs);

    // Current rotation (persisted by the caller via NVS).
    Rotation rotation() const { return rotation_; }

    // Toggle rotation to the opposite orientation.  Returns the new value.
    Rotation toggleRotation();

    // Reset the detector (e.g. on wake from sleep) — clears transient state
    // but preserves rotation_.
    void reset();

private:
    Threshold t_;

    // --- spike detection state ---
    bool inSpike;          // currently above spikeG
    uint32_t spikeStartMs; // when the current spike began
    uint32_t lastSpikeEndMs; // when the most recent spike ended (rising edge)

    // --- double-tap state ---
    uint32_t lastTapEndMs;   // end of the first tap in a potential pair
    bool firstTapSeen;      // waiting for second tap
    uint32_t lockoutUntilMs; // lockout window after a double-tap fires

    // --- rotation state ---
    Rotation rotation_;

    // Compute true-g magnitude from raw m/s² components.
    static float magnitudeG(float ax, float ay, float az);
};

} // namespace gesture
} // namespace sticks3
