// firmware/src/usage/refresh.cpp — implementation of the pure-C++17 refresh
// throttle/retry policy.  No M5/Arduino headers.
#include "usage/refresh.h"

namespace usage {

// refreshThrottleOk returns true when at least throttleMs have elapsed since
// lastRefreshMs.  The uint32 subtraction wraps safely at the 49.7-day boundary:
// if nowMs wrapped past lastRefreshMs the delta is huge and we permit the
// refresh.  A lastRefreshMs of 0 (never refreshed) always passes.
bool refreshThrottleOk(uint32_t nowMs, uint32_t lastRefreshMs,
                       uint32_t throttleMs) {
    if (lastRefreshMs == 0) {
        return true;
    }
    // Safe across uint32_t wrap: if now < lastRefresh, this is a large positive
    // delta, so we proceed (the clock just wrapped, not went backwards).
    return (uint32_t)(nowMs - lastRefreshMs) >= throttleMs;
}

// shouldRefreshRetry returns true when a 202 (Accepted) response should trigger
// a single retry — the server's poll is still running.  Only 202 triggers a
// retry; 200 is immediate, anything else is a hard failure (no retry).
bool shouldRefreshRetry(int code) {
    return code == 202;
}

} // namespace usage
