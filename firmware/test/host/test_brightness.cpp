// firmware/test/host/test_brightness.cpp — pure-C++17 tests for the
// BrightnessController level stepper and mode state machine.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the entry.
#include "framework.h"
#include "usage/brightness.h"

using sticks3::brightness::BrightnessController;
using sticks3::brightness::kLevels;
using sticks3::brightness::kLevelCount;
using sticks3::brightness::kDefaultLevelIdx;
using sticks3::brightness::kModeTimeoutMs;

// --- level stepper ------------------------------------------------------------

// Stepping up progresses through the full ladder: 5→10→25→50→75→100, then
// stays at 100 (clamped).
TEST(brightness_stepper_clamps_at_top) {
    BrightnessController ctrl;
    ASSERT_EQ(kDefaultLevelIdx, ctrl.levelIdx()); // starts at 25%

    ctrl.stepUp(100);
    ASSERT_EQ(3u, (unsigned)ctrl.levelIdx()); // 50%
    ctrl.stepUp(200);
    ASSERT_EQ(4u, (unsigned)ctrl.levelIdx()); // 75%
    ctrl.stepUp(300);
    ASSERT_EQ(5u, (unsigned)ctrl.levelIdx()); // 100%
    ctrl.stepUp(400);
    ASSERT_EQ(5u, (unsigned)ctrl.levelIdx()); // clamped at 100%
}

// Stepping down from 25% goes 25→10→5→5 (clamped).
TEST(brightness_stepper_clamps_at_bottom) {
    BrightnessController ctrl;
    ASSERT_EQ(2u, (unsigned)ctrl.levelIdx()); // 25%

    ctrl.stepDown(100);
    ASSERT_EQ(1u, (unsigned)ctrl.levelIdx()); // 10%
    ctrl.stepDown(200);
    ASSERT_EQ(0u, (unsigned)ctrl.levelIdx()); // 5%
    ctrl.stepDown(300);
    ASSERT_EQ(0u, (unsigned)ctrl.levelIdx()); // clamped at 5%
}

// setLevel finds the closest discrete level by raw value.
TEST(brightness_setlevel_closest) {
    BrightnessController ctrl;
    // raw 64 → 25% (idx 2)
    ctrl.setLevel(64);
    ASSERT_EQ(2u, (unsigned)ctrl.levelIdx());
    // raw 65 → still 25% (closer to 64 than 128)
    ctrl.setLevel(65);
    ASSERT_EQ(2u, (unsigned)ctrl.levelIdx());
    // raw 120 → closer to 128 (50%) than 64 (25%)
    ctrl.setLevel(120);
    ASSERT_EQ(3u, (unsigned)ctrl.levelIdx()); // 50%
    // raw 200 → 75% (idx 4, raw 191)
    ctrl.setLevel(200);
    ASSERT_EQ(4u, (unsigned)ctrl.levelIdx());
    // Out-of-range raw → default (25%)
    ctrl.setLevel(255);
    ASSERT_EQ(5u, (unsigned)ctrl.levelIdx()); // 255 → 100%
}

// setLevelIdx validates the index; out-of-range → default.
TEST(brightness_setlevelidx_validates) {
    BrightnessController ctrl;
    ctrl.setLevelIdx(0);  // 5%
    ASSERT_EQ(0u, (unsigned)ctrl.levelIdx());
    ctrl.setLevelIdx(5);  // 100%
    ASSERT_EQ(5u, (unsigned)ctrl.levelIdx());
    ctrl.setLevelIdx(99); // out of range → default
    ASSERT_EQ(kDefaultLevelIdx, ctrl.levelIdx());
}

// rawLevel and percent return the correct values for the current level.
TEST(brightness_rawlevel_and_percent) {
    BrightnessController ctrl;
    // Default is 25%: raw=64, pct=25.
    ASSERT_EQ(kLevels[2].raw, ctrl.rawLevel());
    ASSERT_EQ(kLevels[2].percent, ctrl.percent());

    ctrl.stepUp(100); // → 50%
    ASSERT_EQ(128u, (unsigned)ctrl.rawLevel());
    ASSERT_EQ(50u, (unsigned)ctrl.percent());

    ctrl.stepUp(200); // → 75%
    ASSERT_EQ(191u, (unsigned)ctrl.rawLevel());
    ASSERT_EQ(75u, (unsigned)ctrl.percent());
}

// idleRaw is active / 4, clamped to >= 1.
TEST(brightness_idleraw_clamped) {
    BrightnessController ctrl;
    // 25% (raw 64): idle = 64/4 = 16
    ASSERT_EQ(16u, (unsigned)ctrl.idleRaw());

    ctrl.setLevelIdx(5); // 100% (raw 255): idle = 255/4 = 63
    ASSERT_EQ(63u, (unsigned)ctrl.idleRaw());

    ctrl.setLevelIdx(0); // 5% (raw 13): idle = 13/4 = 3
    ASSERT_EQ(3u, (unsigned)ctrl.idleRaw());
}

// idleRaw is always dimmer than active (never equal).
TEST(brightness_idleraw_never_exceeds_active) {
    BrightnessController ctrl;
    for (int i = 0; i < kLevelCount; i++) {
        ctrl.setLevelIdx((uint8_t)i);
        ASSERT_TRUE(ctrl.idleRaw() < ctrl.rawLevel());
    }
    // At 5% (raw 13): 13/4 = 3, which is < 13. ✓
}

// --- mode state machine ------------------------------------------------------

// enterMode sets inMode=true; exitMode clears it.
TEST(brightness_mode_enter_exit) {
    BrightnessController ctrl;
    ASSERT_FALSE(ctrl.inMode());
    ctrl.enterMode(1000);
    ASSERT_TRUE(ctrl.inMode());
    ctrl.exitMode();
    ASSERT_FALSE(ctrl.inMode());
}

// shouldExit returns false before the timeout, true after.
TEST(brightness_mode_timeout_exits) {
    BrightnessController ctrl;
    ctrl.enterMode(1000);
    // 4.9 s in: still in mode.
    ASSERT_FALSE(ctrl.shouldExit(1000 + kModeTimeoutMs - 1));
    // Exactly at timeout: exits.
    ASSERT_TRUE(ctrl.shouldExit(1000 + kModeTimeoutMs));
    // 5 s later: still true.
    ASSERT_TRUE(ctrl.shouldExit(1000 + kModeTimeoutMs + 5000));
}

// Steps inside mode reset the inactivity timer.
TEST(brightness_mode_step_resets_timer) {
    BrightnessController ctrl;
    ctrl.enterMode(1000);
    // Without stepping, would exit at 6000.
    // Step at 5500 → timer resets, should NOT exit at 6000.
    ctrl.stepUp(5500);
    ASSERT_FALSE(ctrl.shouldExit(6000));
    // Now exits at 5500 + 5000 = 10500.
    ASSERT_TRUE(ctrl.shouldExit(10500));
}

// After exiting, shouldExit returns false (not in mode).
TEST(brightness_mode_exited_does_not_exit) {
    BrightnessController ctrl;
    ctrl.enterMode(1000);
    ASSERT_TRUE(ctrl.shouldExit(1000 + kModeTimeoutMs));
    ctrl.exitMode();
    ASSERT_FALSE(ctrl.shouldExit(1000 + kModeTimeoutMs + 10000));
}

// Stepping at level boundaries still resets timer.
TEST(brightness_mode_step_at_bounds_resets) {
    BrightnessController ctrl;
    ctrl.setLevelIdx(5); // 100%
    ctrl.enterMode(1000);
    // stepUp at max — no change to level, but timer resets.
    ctrl.stepUp(5500);
    ASSERT_FALSE(ctrl.shouldExit(6000));

    ctrl.setLevelIdx(0); // 5%
    ctrl.enterMode(1000);
    // stepDown at min — no change to level, but timer resets.
    ctrl.stepDown(5500);
    ASSERT_FALSE(ctrl.shouldExit(6000));
}
