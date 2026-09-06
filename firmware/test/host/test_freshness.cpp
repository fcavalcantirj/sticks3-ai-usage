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
