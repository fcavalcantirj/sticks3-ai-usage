// firmware/src/usage/freshness.cpp — implementation of freshnessTier and
// accumulateAge (ORDER #65 task 65).
//
// Pure C++17, no Arduino/M5 headers.  Host-tested by test_freshness.cpp.
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

} // namespace usage
