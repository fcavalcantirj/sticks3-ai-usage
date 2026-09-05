// firmware/test/host/test_refresh.cpp — host tests for the pure-C++17 refresh
// throttle and retry policy (ORDER #38).
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/refresh.h"

// --- refreshThrottleOk --------------------------------------------------------

TEST(refresh_throttle_first_time_always_ok) {
    // lastRefreshMs == 0 means "never refreshed" — always allowed.
    ASSERT_TRUE(usage::refreshThrottleOk(1000, 0, usage::kRefreshThrottleMs));
}

TEST(refresh_throttle_within_window_blocks) {
    // 5 s after last refresh, 10 s window → blocked.
    // (lastRefreshMs=0 means "never", which always passes — see next test.)
    ASSERT_TRUE(!usage::refreshThrottleOk(5000, 1000, usage::kRefreshThrottleMs));
    ASSERT_TRUE(!usage::refreshThrottleOk(9000, 1000, usage::kRefreshThrottleMs));
}

TEST(refresh_throttle_after_window_passes) {
    // 11 s after last refresh, 10 s window → allowed.
    ASSERT_TRUE(usage::refreshThrottleOk(11000, 1000, usage::kRefreshThrottleMs));
}

TEST(refresh_throttle_exact_boundary) {
    // Exactly 10 s → allowed (>= comparison).
    ASSERT_TRUE(usage::refreshThrottleOk(10000, 0, usage::kRefreshThrottleMs));
    ASSERT_TRUE(usage::refreshThrottleOk(11000, 1000, usage::kRefreshThrottleMs));
    // 9.999 s → blocked.
    ASSERT_TRUE(!usage::refreshThrottleOk(10999, 1000, usage::kRefreshThrottleMs));
}

TEST(refresh_throttle_uint32_wrap) {
    // Simulated millis() wrap: nowMs just past 0xFFFFFFFF, lastRefreshMs near
    // the top.  The subtraction wraps to a small delta, correctly blocking.
    uint32_t lastRefresh = 0xFFFFFF00u;
    uint32_t nowWrapped  = 0x00000010u;  // ~0x100 after wrap
    // delta = 0x110 — well under 10000 → should block.
    ASSERT_TRUE(!usage::refreshThrottleOk(nowWrapped, lastRefresh, usage::kRefreshThrottleMs));
}

TEST(refresh_throttle_custom_window) {
    // Custom 5 s window.
    ASSERT_TRUE(!usage::refreshThrottleOk(4000, 1000, 5000));
    ASSERT_TRUE(usage::refreshThrottleOk(6000, 1000, 5000));
}

// --- shouldRefreshRetry -------------------------------------------------------

TEST(refresh_retry_202) {
    ASSERT_TRUE(usage::shouldRefreshRetry(202));
}

TEST(refresh_retry_200) {
    ASSERT_TRUE(!usage::shouldRefreshRetry(200));
}

TEST(refresh_retry_error) {
    ASSERT_TRUE(!usage::shouldRefreshRetry(-1));
    ASSERT_TRUE(!usage::shouldRefreshRetry(500));
    ASSERT_TRUE(!usage::shouldRefreshRetry(404));
}
