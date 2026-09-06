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
