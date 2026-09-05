// firmware/src/usage/refresh.h — pure-C++17 refresh throttle/retry policy.
//
// No M5/Arduino headers.  Host-testable with an injected millisecond clock.
// ORDER #38: the device's side-button refresh posts to /v1/refresh before the
// conditional GET so the user gets fresh data, not the Mac agent's cache.
#pragma once

#include <cstdint>

namespace usage {

// Default throttle: one device-initiated refresh per 10 s to avoid hammering
// provider APIs (Anthropic allows at most 1 call per 300 s; the server-side
// minInterval in ORDER #39 is the real guard, but the device throttles too).
static const uint32_t kRefreshThrottleMs = 10000;

// refreshThrottleOk returns true when at least throttleMs have elapsed since
// lastRefreshMs.  When nowMs - lastRefreshMs would underflow (uint32 wrap), the
// subtraction still yields a large positive delta, correctly permitting a
// refresh.  A lastRefreshMs of 0 (never refreshed) always passes.
bool refreshThrottleOk(uint32_t nowMs, uint32_t lastRefreshMs,
                       uint32_t throttleMs = kRefreshThrottleMs);

// shouldRefreshRetry returns true when a 202 (Accepted — server's poll is
// still running) response should trigger a single retry after a short delay.
bool shouldRefreshRetry(int code);

} // namespace usage
