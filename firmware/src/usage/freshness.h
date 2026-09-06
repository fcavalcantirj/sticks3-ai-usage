// firmware/src/usage/freshness.h — pure-C++17 data-freshness logic for the
// StickS3 usage monitor.
//
// ORDER #65 task 65: the device has no real-time clock, so it cannot compute
// "how old is this data".  The server sends an `age` field (seconds since
// checked_at, evaluated at response time) on 200 responses.  The device adds
// its own elapsed milliseconds since that fetch to get the effective age:
//   effectiveAge = serverAgeAtLastFetch + (nowMs - lastFetchMs) / 1000
// This stays correct across a 12-hour deep sleep because the device knows how
// long it slept (via the [WAKE] gap in the access log).
#pragma once

#include <cstdint>

namespace usage {

// Freshness tier for data age (ORDER #65 task 65).
// 0 = green  (age <= 2 × next_sec)   — fresh
// 1 = yellow (age <= 4 × next_sec)   — stale
// 2 = red    (age >  4 × next_sec)   — very stale
//
// Tiers are derived from the snapshot's next_sec (900 today) so changing the
// poll interval in the settings page moves them automatically.
uint8_t freshnessTier(uint32_t ageSec, uint16_t nextSec);

// Accumulate the effective data age: the server-reported age at last fetch
// plus the elapsed milliseconds since that fetch, converted to seconds.
// This lets a clockless device track staleness across deep sleep — the
// device simply adds the sleep duration to the age it last received.
uint32_t accumulateAge(uint32_t serverAgeAtFetch, uint32_t elapsedMs);

} // namespace usage
