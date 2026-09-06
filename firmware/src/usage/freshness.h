// firmware/src/usage/freshness.h — pure-C++17 data-freshness logic for the
// StickS3 usage monitor.
//
// ORDER #65: the device has no real-time clock, so it cannot compute
// "how old is this data".  The server sends an `age` field (seconds since
// checked_at, evaluated at response time) on 200 responses.  The device
// reads the system clock (gettimeofday, maintained across deep sleep by
// ESP-IDF's RTC) immediately before powerSleep() and again on wake, adding
// the real sleep duration to the accumulated effective age:
//   effectiveAge = serverAgeAtLastFetch + elapsed_sec_since_fetch + sleepDuration
// This stays correct across a 12-hour deep sleep because the device knows
// exactly how long it slept (RTC clock delta), not just how long it was
// awake.  sleepDuration is clamped to a sane ceiling (kMaxSleepSec = 2 days)
// and anything beyond reads as MAX_UINT32 (unknown) so a broken clock on a
// 12h backstop never invents a 49-day age.
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

// kMaxSleepSec: the maximum sleep duration we will accept from a clock
// reading.  A 2-day ceiling is far above the 12-hour backstop but catches
// a broken clock that produces a negative or absurd delta.
static constexpr uint32_t kMaxSleepSec = 172800; // 2 days in seconds

// UINT32_MAX sentinel for "unknown sleep duration" (clock broken/clock drift).
static constexpr uint32_t kSleepUnknown = 0xFFFFFFFFu;

// sleepDuration computes the elapsed seconds between a pre-sleep clock
// reading and a post-wake reading, clamping the result.  The inputs are
// time_t/seconds-since-epoch values (from gettimeofday on ESP-IDF, which
// the RTC maintains across deep sleep).  A non-positive delta (clock went
// backwards, or the clock did not actually survive) returns kSleepUnknown
// so the caller treats the age as unknown (RED on the lamp).  A delta
// exceeding kMaxSleepSec also returns kSleepUnknown.  Otherwise the
// clamped, positive delta is returned.
uint32_t sleepDuration(uint32_t wakeEpochSec, uint32_t sleepEpochSec);

// ORDER #65 defect fix: RTC_DATA_ATTR survives an OTA software-reset reboot,
// not only a deep-sleep wake.  If g_justSlept was set before a reboot, the
// g_rtcSleepStartSec value is stale and computing a sleep duration would
// invent phantom elapsed time (the observed ~400s offset).  This predicate
// returns true ONLY when the device actually woke from deep sleep — i.e.
// g_justSlept is set AND the wake cause is a real deep-sleep wake (Ext1
// button or Timer backstop), NOT a PowerOn (cold boot / OTA reboot).
// The caller (main.cpp) maps WakeCause → fromDeepSleepWake before calling.
bool shouldApplySleepDuration(bool justSlept, bool fromDeepSleepWake);

} // namespace usage
