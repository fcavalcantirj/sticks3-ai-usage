// firmware/src/usage/freshness.cpp — implementation of freshnessTier,
// accumulateAge, and sleepDuration (ORDER #65 task 65).
//
// Pure C++17, no Arduino/M5 headers.  Host-tested by test_freshness.cpp.
// sleepDuration uses gettimeofday epoch-seconds (maintained across deep sleep
// by ESP-IDF's RTC) to compute the real sleep duration, clamping wraps and
// absurd deltas to kSleepUnknown.
#include "usage/freshness.h"

namespace usage {

uint8_t freshnessTier(uint32_t ageSec, uint16_t nextSec) {
    // A snapshot with next_sec == 0 is unexpected; treat it as immediately stale
    // to be safe rather than dividing by zero.
    if (nextSec == 0) return 2; // red

    uint32_t twoX  = (uint32_t)nextSec * 2;  // 1800 for 900
    uint32_t fourX = (uint32_t)nextSec * 4;  // 3600 for 900

    if (ageSec <= twoX) return 0;  // green
    if (ageSec <= fourX) return 1; // yellow
    return 2;                      // red
}

uint32_t accumulateAge(uint32_t serverAgeAtFetch, uint32_t elapsedMs) {
    return serverAgeAtFetch + elapsedMs / 1000;
}

// sleepDuration: given wakeEpochSec (clock reading on wake) and
// sleepEpochSec (clock reading before powerSleep), compute the real sleep
// duration in seconds.  ESP-IDF's gettimeofday is maintained by the RTC
// across deep sleep, so the delta is the true elapsed wall-clock time.
//
// Defensive: if the clock did not survive (rtcEnd small against large
// rtcStart, producing a negative delta) or if the delta exceeds kMaxSleepSec
// (2 days — far above the 12h backstop), return kSleepUnknown so the caller
// treats the age as unknown rather than inventing an absurd number.
uint32_t sleepDuration(uint32_t wakeEpochSec, uint32_t sleepEpochSec) {
    if (wakeEpochSec <= sleepEpochSec) {
        // Clock went backwards or did not survive — unknown duration.
        return kSleepUnknown;
    }
    uint32_t delta = wakeEpochSec - sleepEpochSec;
    if (delta > kMaxSleepSec) {
        // Absurdly large delta — treat as unknown rather than propagating.
        return kSleepUnknown;
    }
    return delta;
}

} // namespace usage
