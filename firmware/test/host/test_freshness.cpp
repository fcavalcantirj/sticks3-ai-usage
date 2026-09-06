// firmware/test/host/test_freshness.cpp — tests for the pure-C++17
// freshnessTier() and accumulateAge() helpers (ORDER #65 task 65).
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/freshness.h"

// --- freshnessTier -----------------------------------------------------------

TEST(freshness_green_within_2x_nextsec) {
    // nextSec=900 → green threshold is 1800s.  Age right at the boundary is green.
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(0, 900));
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(1799, 900));
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(1800, 900));
}

TEST(freshness_yellow_between_2x_and_4x) {
    // 1801s → yellow; 3600s → yellow (boundary); 3601s → red.
    ASSERT_EQ(1u, (unsigned)usage::freshnessTier(1801, 900));
    ASSERT_EQ(1u, (unsigned)usage::freshnessTier(3599, 900));
    ASSERT_EQ(1u, (unsigned)usage::freshnessTier(3600, 900));
}

TEST(freshness_red_over_4x) {
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(3601, 900));
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(7200, 900));
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(999999, 900));
}

TEST(freshness_nextsec_zero_is_red) {
    // nextSec == 0 would divide by zero in a different formulation; we guard.
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(0, 0));
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(500, 0));
}

TEST(freshness_tier_scales_with_nextsec) {
    // With a shorter poll interval the thresholds scale too.
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(10, 5));   // 10s < 2*5=10, green
    ASSERT_EQ(1u, (unsigned)usage::freshnessTier(11, 5));   // 11s > 10, yellow
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(21, 5));   // 21s > 4*5=20, red
}

// --- accumulateAge ------------------------------------------------------------

TEST(accumulate_age_adds_elapsed_seconds) {
    // 42s server age + 3000ms elapsed → 45s.
    ASSERT_EQ(45u, (unsigned)usage::accumulateAge(42, 3000));
    // 0ms elapsed → age unchanged.
    ASSERT_EQ(42u, (unsigned)usage::accumulateAge(42, 0));
    // 1999ms → truncates to 1s.
    ASSERT_EQ(43u, (unsigned)usage::accumulateAge(42, 1999));
    // Large elapsed: 1 hour of sleep.
    ASSERT_EQ(3642u, (unsigned)usage::accumulateAge(42, 3600000));
}

// --- ORDER #65 fix: age reset on device-initiated refresh ---------------------

// When the user presses the refresh button, doDeviceRefresh() POSTs
// /v1/refresh (forcing the server to re-poll providers), then does a
// conditional GET.  If the GET returns 304, the firmware has no fresh age
// from the body — it must rely on the reset done in doDeviceRefresh().
// These tests verify the accumulateAge + freshnessTier combination produces
// green when the age accumulator is reset to 0, even after a short delay.

TEST(freshness_reset_age_yields_green) {
    // After doDeviceRefresh resets g_dataAgeAtFetch=0 and g_lastFetchMs=now,
    // a 304 GET 5s later: effectiveAge = accumulateAge(0, 5000) = 5.
    uint32_t effAge = usage::accumulateAge(0, 5000);
    ASSERT_EQ(5u, (unsigned)effAge);
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(effAge, 900)); // green
}

TEST(freshness_reset_age_vs_stale_without_reset) {
    // Without the reset, a 304 at age 1801s (already yellow) 5s later:
    uint32_t staleAge = usage::accumulateAge(1801, 5000); // 1806
    ASSERT_EQ(1u, (unsigned)usage::freshnessTier(staleAge, 900)); // yellow

    // With the reset, the same 5s later: green.
    uint32_t freshAge = usage::accumulateAge(0, 5000); // 5
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(freshAge, 900)); // green
}

TEST(freshness_reset_age_304_still_green_after_30s) {
    // A 304 response gives no body and no fresh age.  After the reset,
    // 30s of no-change polling keeps it green (30 < 2*900=1800).
    uint32_t effAge = usage::accumulateAge(0, 30000);
    ASSERT_EQ(30u, (unsigned)effAge);
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(effAge, 900)); // green
}

// --- ORDER #64: 12-hour sleep gap must yield RED, not green ----------------

// The ORDER #64 correction: the warm-boot path stores the effective age
// before sleeping (g_effectiveAgeAtSleep = accumulateAge at sleep time) and
// restores it on wake.  After a 12-hour sleep the effective age has grown by
// ~43,200 s, which is well past 4×900=3600 → RED.  A freshness indicator that
// shows green on 12-hour-old data is worse than none.

TEST(freshness_12h_sleep_gap_is_red) {
    // Server reported age at last fetch: 10s.  Sleep duration: 12h = 43,200,000 ms.
    // Effective age on wake = 10 + 43200 = 43210 s.
    uint32_t effAge = usage::accumulateAge(10, 43200000);
    ASSERT_EQ(43210u, (unsigned)effAge);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(effAge, 900)); // red (> 4*900=3600)
}

TEST(freshness_12h_gap_accumulate_no_wrap_no_saturate) {
    // accumulateAge must not wrap or saturate at a 12-hour gap.
    // 12h = 43,200,000 ms; server age 0 → effective = 43,200 s.
    uint32_t effAge = usage::accumulateAge(0, 43200000);
    ASSERT_EQ(43200u, (unsigned)effAge);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(effAge, 900)); // red
}

TEST(freshness_6h_sleep_is_red) {
    // 6h = 21,600,000 ms > 4*900=3600 → red.
    uint32_t effAge = usage::accumulateAge(0, 21600000);
    ASSERT_EQ(21600u, (unsigned)effAge);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(effAge, 900)); // red
}

TEST(freshness_successful_fetch_resets_to_green) {
    // After a 12h sleep (effective age ~43210s, red), a successful 200 GET
    // provides a fresh server age (e.g. 15s).  The firmware resets
    // g_dataAgeAtFetch = model.age (15) and g_lastFetchMs = nowMs().
    // A 304 5s later: effectiveAge = accumulateAge(15, 5000) = 20 → green.
    uint32_t staleAge = usage::accumulateAge(10, 43200000);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(staleAge, 900)); // was red

    uint32_t freshAge = usage::accumulateAge(15, 5000); // 200 response age=15, 5s later
    ASSERT_EQ(20u, (unsigned)freshAge);
    ASSERT_EQ(0u, (unsigned)usage::freshnessTier(freshAge, 900)); // green after fetch
}

TEST(freshness_failed_fetch_keeps_red) {
    // After a 12h sleep (effective age ~43210s, red), a FAILED fetch does NOT
    // reset the age — the stale value persists.  The device does not paper over
    // staleness by forcing green.
    uint32_t staleAge = usage::accumulateAge(10, 43200000);
    ASSERT_EQ(43210u, (unsigned)staleAge);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(staleAge, 900)); // red, stays red

    // 5s pass while retrying: age grows.
    uint32_t aged = usage::accumulateAge(staleAge, 5000);
    ASSERT_EQ(43215u, (unsigned)aged);
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(aged, 900)); // still red
}

// --- sleepDuration ------------------------------------------------------------

// sleepDuration computes the real sleep duration from RTC clock readings
// before and after deep sleep.  ESP-IDF maintains gettimeofday across sleep.

TEST(sleep_duration_normal_90s) {
    // 90-second sleep: wake at epoch 1000, sleep at epoch 910.
    ASSERT_EQ(90u, (unsigned)usage::sleepDuration(1000, 910));
}

TEST(sleep_duration_zero_is_unknown) {
    // Same timestamp — clock did not advance or did not survive.
    ASSERT_EQ(usage::kSleepUnknown, (unsigned)usage::sleepDuration(1000, 1000));
}

TEST(sleep_duration_underflow_wraps_to_unknown) {
    // The signature from the failed hardware probe: wake comes back SMALL
    // against a large sleep value (clock did not survive).  The uint32
    // subtraction would wrap to ~4 billion — instead we return UNKNOWN.
    // 4295195 was the observed value; simulate the pattern with larger numbers.
    ASSERT_EQ(usage::kSleepUnknown, (unsigned)usage::sleepDuration(100, 4000000000u));
}

TEST(sleep_duration_12h_backstop) {
    // 12 hours = 43200 seconds.  This is the maximum legitimate sleep.
    uint32_t sleep = 1788709643u;
    uint32_t wake = sleep + 43200;
    ASSERT_EQ(43200u, (unsigned)usage::sleepDuration(wake, sleep));
}

TEST(sleep_duration_exceeds_ceiling_is_unknown) {
    // 3 days exceeds the 2-day ceiling — treat as unknown.
    uint32_t sleep = 1788709643u;
    uint32_t wake = sleep + 259200; // 3 days
    ASSERT_EQ(usage::kSleepUnknown, (unsigned)usage::sleepDuration(wake, sleep));
}

TEST(sleep_duration_clock_backward_is_unknown) {
    // Wake time before sleep time — clock went backwards.
    ASSERT_EQ(usage::kSleepUnknown, (unsigned)usage::sleepDuration(500, 1000));
}

TEST(sleep_duration_unknown_clamps_age) {
    // When sleepDuration returns kSleepUnknown, the caller must not add it
    // to the age (that would set age to ~4 billion).  Instead it treats the
    // age as MAX_UINT32 so freshnessTier returns red.
    uint32_t age = usage::accumulateAge(10, 0);
    ASSERT_EQ(10u, (unsigned)age);
    // In the caller: if sleep == kSleepUnknown, age = kSleepUnknown (MAX_UINT32).
    // freshnessTier(MAX_UINT32, 900) is red (> 4*900=3600).
    ASSERT_EQ(2u, (unsigned)usage::freshnessTier(usage::kSleepUnknown, 900));
}

// --- ORDER #65 defect fix: shouldApplySleepDuration ----------------------------

// RTC_DATA_ATTR survives OTA reboots, not only deep-sleep wakes.  The
// sleep-duration restore must be gated on an actual deep-sleep wake cause
// (Ext1/Timer), not on g_justSlept alone — otherwise a reboot with stale
// g_justSlept invents phantom elapsed time.

TEST(should_apply_sleep_duration_reboot_does_not_apply) {
    // A software-reset reboot (PowerOn wake) with g_justSlept stale-true:
    // no sleep occurred, so no sleep duration should be computed.
    ASSERT_FALSE(usage::shouldApplySleepDuration(true, false));
}

TEST(should_apply_sleep_duration_ext1_wake_applies) {
    // Button wake from deep sleep: real sleep, should apply.
    ASSERT_TRUE(usage::shouldApplySleepDuration(true, true));
}

TEST(should_apply_sleep_duration_timer_wake_applies) {
    // Timer backstop wake from deep sleep: real sleep, should apply.
    ASSERT_TRUE(usage::shouldApplySleepDuration(true, true));
}

TEST(should_apply_sleep_duration_no_justSlept_does_not_apply) {
    // g_justSlept was already consumed on a prior wake — no sleep to account.
    ASSERT_FALSE(usage::shouldApplySleepDuration(false, true));
    ASSERT_FALSE(usage::shouldApplySleepDuration(false, false));
}

TEST(should_apply_sleep_duration_reboot_consumes_justSlept) {
    // The caller's else-branch must consume g_justSlept even on a reboot,
    // so the stale flag does not propagate into a later sleep-wake cycle.
    // This test documents that invariant by checking the predicate returns
    // false for a reboot (the caller will set g_justSlept = false in the
    // else-branch, matching the fix at main.cpp).
    ASSERT_FALSE(usage::shouldApplySleepDuration(true, false));
}
