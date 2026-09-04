// firmware/test/host/test_gesture.cpp — tests for the pure IMU double-tap
// state machine.  Drives feed() with synthetic sample sequences.
#include "framework.h"
#include "usage/gesture.h"

using sticks3::gesture::DoubleTapDetector;
using sticks3::gesture::Gesture;
using sticks3::gesture::Rotation;
using sticks3::gesture::Threshold;

// Helper: a spike sample (3.0g in the x direction → ~29.43 m/s²).
static float spikeAx = 29.43f; // ~3g → above 2.5g threshold
static float spikeAy = 0.0f;
static float spikeAz = 0.0f;

// Helper: a gravity sample (~1g along z → ~9.81 m/s²).
static float gravAx = 0.0f;
static float gravAy = 0.0f;
static float gravAz = 9.81f;

// Two taps 250 ms apart return exactly one double_tap.
TEST(gesture_two_taps_250ms) {
    DoubleTapDetector det;
    Gesture result = Gesture::none;
    uint32_t t = 0;

    // First tap (spike at t=100, ends at t=200).
    for (; t < 100; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t); // spike starts
    for (; t < 200; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    result = det.feed(gravAx, gravAy, gravAz, t); // spike ends → firstTapSeen

    ASSERT_EQ(Gesture::none, result);

    // 250 ms gap.
    for (; t < 450; t += 20) det.feed(gravAx, gravAy, gravAz, t);

    // Second tap (spike at t=450, ends at t=550).
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 550; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    result = det.feed(gravAx, gravAy, gravAz, t); // spike ends → double_tap

    ASSERT_EQ(Gesture::double_tap, result);

    // After a double-tap, the 1 s lockout suppresses everything.
    for (; t < 1600; t += 20) {
        ASSERT_EQ(Gesture::none, det.feed(spikeAx, spikeAy, spikeAz, t));
    }
}

// Two taps 800 ms apart (gap exceeds 500 ms max) return none.
TEST(gesture_two_taps_800ms_no_double) {
    DoubleTapDetector det;
    uint32_t t = 0;

    // First tap at t=100-200.
    for (; t < 100; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 200; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t); // first tap detected

    // 800 ms gap (exceeds 500 ms max).
    for (; t < 1000; t += 20) det.feed(gravAx, gravAy, gravAz, t);

    // Second tap at t=1000-1100.
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 1100; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    Gesture result = det.feed(gravAx, gravAy, gravAz, t); // second tap, but gap too long

    ASSERT_EQ(Gesture::none, result);
}

// 1 s lockout suppresses a third tap after a double-tap fires.
TEST(gesture_lockout_suppresses_third) {
    DoubleTapDetector det;
    uint32_t t = 0;

    // First tap at t=100.
    for (; t < 100; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 200; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t);

    // Second tap 250 ms later (t=450).
    for (; t < 450; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 550; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    Gesture r = det.feed(gravAx, gravAy, gravAz, t);
    ASSERT_EQ(Gesture::double_tap, r);

    // Immediately after, a third tap (t=600) must NOT produce a double_tap.
    for (; t < 600; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 700; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    r = det.feed(gravAx, gravAy, gravAz, t);
    ASSERT_EQ(Gesture::none, r);
}

// Steady 1 g gravity in any orientation returns none.
TEST(gesture_steady_gravity_no_fire) {
    DoubleTapDetector det;
    uint32_t t = 0;
    for (; t < 5000; t += 20) {
        ASSERT_EQ(Gesture::none, det.feed(gravAx, gravAy, gravAz, t));
    }
    // Also test gravity along x and y axes.
    for (t = 0; t < 1000; t += 20) {
        ASSERT_EQ(Gesture::none, det.feed(9.81f, 0.0f, 0.0f, t));
        ASSERT_EQ(Gesture::none, det.feed(0.0f, 9.81f, 0.0f, t));
    }
}

// A slow ramp above 2.5g (simulating picking the device up sharply) must
// return none, even though the magnitude exceeds spikeG — because the spike
// lasts longer than spikeMs (120 ms).
TEST(gesture_slow_ramp_no_fire) {
    DoubleTapDetector det;
    uint32_t t = 0;

    // Slow ramp: increase from 1g to 3g over 300 ms (well over 120 ms limit).
    // The spike detection should reset this as a slow ramp, not a tap.
    for (; t < 300; t += 20) {
        float frac = float(t) / 300.0f;
        float magG = 1.0f + frac * 2.0f; // 1g → 3g
        ASSERT_EQ(Gesture::none, det.feed(magG * 9.81f, 0.0f, 0.0f, t));
    }

    // Even a second slow ramp should not produce a double_tap.
    for (t = 300; t < 600; t += 20) {
        float frac = float(t - 300) / 300.0f;
        float magG = 1.0f + frac * 2.0f; // 1g → 3g
        ASSERT_EQ(Gesture::none, det.feed(magG * 9.81f, 0.0f, 0.0f, t));
    }
}

// Rotation starts at upright (1) and toggles to flipped (3) on double-tap.
TEST(gesture_rotation_persists_across_resets) {
    DoubleTapDetector det;
    ASSERT_EQ(Rotation::upright, det.rotation());

    // Simulate a double-tap.
    uint32_t t = 0;
    for (; t < 100; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 200; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t);
    for (; t < 450; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 550; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t);

    ASSERT_EQ(Rotation::flipped, det.rotation());

    // reset() clears transient state but preserves rotation.
    det.reset();
    ASSERT_EQ(Rotation::flipped, det.rotation());

    // After reset, we can double-tap again to toggle back.
    for (t = 560; t < 660; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 760; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t);
    for (; t < 1010; t += 20) det.feed(gravAx, gravAy, gravAz, t);
    det.feed(spikeAx, spikeAy, spikeAz, t);
    for (; t < 1110; t += 20) det.feed(spikeAx, spikeAy, spikeAz, t);
    det.feed(gravAx, gravAy, gravAz, t);

    ASSERT_EQ(Rotation::upright, det.rotation());
}
