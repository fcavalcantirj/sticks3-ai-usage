// firmware/test/host/test_brightness.cpp — pure-C++17 tests for the
// BrightnessController level stepper and mode state machine.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the entry.
#include "framework.h"
#include "usage/brightness.h"
#include <cstring>

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

// --- ORDER #74 task 74: gauge layout (evenly spaced by index) -----------------

// The raw PWM ladder is deliberately non-linear: 13, 26, 64, 128, 191, 255.
// Spacing ticks by raw value clusters 5/10/25% on the left (11, 21, 50 px on
// a 198 px track), causing label pile-up.  The fix spaces by INDEX.
TEST(brightness_levels_raw_are_non_linear) {
    // Explicit: gaps are 13, 38, 64, 63, 64 — not uniform.
    ASSERT_EQ(13, kLevels[1].raw - kLevels[0].raw);
    ASSERT_EQ(38, kLevels[2].raw - kLevels[1].raw);
    ASSERT_EQ(64, kLevels[3].raw - kLevels[2].raw);
    ASSERT_EQ(63, kLevels[4].raw - kLevels[3].raw);
    ASSERT_EQ(64, kLevels[5].raw - kLevels[4].raw);
}

// gaugeSegmentW divides innerW into (kLevelCount-1) equal segments.
TEST(brightness_gauge_segment_width) {
    int innerW = 198; // trackW - 2 = 200 - 2
    int segW = BrightnessController::gaugeSegmentW(innerW);
    ASSERT_EQ(innerW / (kLevelCount - 1), segW); // 198 / 5 = 39
}

// Tick positions are evenly spaced by index, using gaugeTickX directly
// (segW * i would truncate early and drift from gaugeTickX for non-divisible
// innerW).  Gaps are 39 or 40 px (at most 1 px deviation from ideal 39.6).
TEST(brightness_gauge_ticks_evenly_spaced) {
    int innerW = 198;
    int denom = kLevelCount - 1; // 5
    for (int i = 0; i < kLevelCount; i++) {
        int t = BrightnessController::gaugeTickX(i, innerW);
        // Tick i is at (innerW * i) / denom, with max 1 px deviation from
        // the ideal evenly-spaced position i * innerW / denom.
        int ideal = i * innerW / denom;
        ASSERT_TRUE(t == ideal || t == ideal + 1);
    }
    // First tick at offset 0, last tick lands at innerW (no overhang).
    ASSERT_EQ(0, BrightnessController::gaugeTickX(0, innerW));
    ASSERT_EQ(innerW, BrightnessController::gaugeTickX(kLevelCount - 1, innerW));
    // Monotonic and strictly increasing.
    for (int i = 1; i < kLevelCount; i++) {
        ASSERT_TRUE(BrightnessController::gaugeTickX(i, innerW) >
                  BrightnessController::gaugeTickX(i - 1, innerW));
    }
}

// Tick labels (5/10/25/50/75/100) do not overlap at size 1 (6 px/char).
// The gauge renders labels at Font0 size 1, not size 2.
// With gaugeTickX spacing (39 or 40 px gaps), the widest label pair "75%+100%"
// (18+24 px, half-sum 21) fits well within a 40 px gap.
TEST(brightness_gauge_labels_non_overlapping) {
    int innerW = 198;
    int glyphW = 6; // Font0 size 1: 6 px (screen.cpp uses setTextSize(1) for labels)

    for (int i = 1; i < kLevelCount; i++) {
        char cur[8], prev[8];
        std::snprintf(cur, sizeof(cur), "%d%%", (int)kLevels[i].percent);
        std::snprintf(prev, sizeof(prev), "%d%%", (int)kLevels[i-1].percent);
        int curW = (int)std::strlen(cur) * glyphW;
        int prevW = (int)std::strlen(prev) * glyphW;
        // Adjacent labels must not overlap: gap between tick centers must
        // exceed the sum of their half-widths.
        int tickGap = BrightnessController::gaugeTickX(i, innerW)
                    - BrightnessController::gaugeTickX(i - 1, innerW);
        ASSERT_TRUE(tickGap >= (prevW + curW) / 2);
    }
}

// Fill width matches the level index: 0 at level 0, innerW at the top.
TEST(brightness_gauge_fill_clamped) {
    int innerW = 198;
    ASSERT_EQ(0, BrightnessController::gaugeFillWidth(0, innerW));
    ASSERT_EQ(innerW, BrightnessController::gaugeFillWidth(
        kLevelCount - 1, innerW));
    // Every level's fill is a valid sub-range of [0, innerW].
    for (int i = 0; i < kLevelCount; i++) {
        int w = BrightnessController::gaugeFillWidth(i, innerW);
        ASSERT_TRUE(w >= 0);
        ASSERT_TRUE(w <= innerW);
    }
}

// gaugeIdleTick maps an idleRaw to the nearest level index.
TEST(brightness_gauge_idle_tick) {
    ASSERT_EQ(0u, BrightnessController::gaugeIdleTick(0));      // below 13 → 5%
    ASSERT_EQ(0u, BrightnessController::gaugeIdleTick(13));    // exact 5%
    ASSERT_EQ(1u, BrightnessController::gaugeIdleTick(20));    // 6 from 26, 7 from 13
    ASSERT_EQ(1u, BrightnessController::gaugeIdleTick(26));    // exact 10%
    ASSERT_EQ(2u, BrightnessController::gaugeIdleTick(64));    // exact 25%
    ASSERT_EQ(3u, BrightnessController::gaugeIdleTick(128));   // exact 50%
    ASSERT_EQ(4u, BrightnessController::gaugeIdleTick(191));   // exact 75%
    ASSERT_EQ(5u, BrightnessController::gaugeIdleTick(255));   // exact 100%
}
