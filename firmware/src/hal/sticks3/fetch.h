// firmware/src/hal/sticks3/fetch.h — conditional HTTP fetch of /v1/usage.
#pragma once

#include <Arduino.h>  // String
#include <cstdint>

#include "usage/provision.h"   // usage::provision::Record (pure, no Arduino headers)

namespace sticks3 {

// Point the fetcher at the agent named by the NVS record (task 76).  Must be
// called before the first fetchUsage()/refreshUpstream().  The host, port and
// device token are copied into file-static buffers, so the caller need not keep
// the record alive.
//
// Before task 76 these came from USAGED_HOST / USAGED_PORT /
// USAGED_DEVICE_TOKEN in secrets.h, which is exactly why the compiled binary
// carried a LAN address and a live device token as plaintext strings.
void fetchConfigure(const usage::provision::Record& rec);

// True once fetchConfigure() has supplied a non-empty host and token.  A fetch
// attempted before that would issue an unauthenticated request to port 0.
bool fetchConfigured();

// Result of fetchUsage: code is the HTTP status (negative = error);
// rev holds the stripped ETag; body holds the response (only on 200).
struct FetchResult {
    int code;         // HTTP status code (negative = transport error)
    char rev[9];      // ETag stripped of W/ prefix and quotes
    uint32_t ms;      // elapsed time in milliseconds
    String body;      // response body (only populated on 200)
};

// Fetch /v1/usage with If-None-Match conditional request.
// If ageS is non-zero, appends ?age_s=<n> to the URL so the server can verify
// the device's RTC-computed effective data age on the wire (ORDER #65).
// Emits a [FETCH] line via serial_proto.  Returns true on 200/304,
// false on transport error or non-200/304 HTTP codes.
bool fetchUsage(const char* lastRev, FetchResult& out, uint32_t ageS = 0);

// POST /v1/refresh to trigger an immediate server-side poll.  Emits a [REFRESH]
// line via serial_proto.  Returns the FetchResult with code 200 (fresh data
// ready), 202 (poll still running, should retry), or negative (transport error).
// The body is populated only on 200 (the refreshed snapshot).  On 202 the body
// is empty.
bool refreshUpstream(FetchResult& out);

} // namespace sticks3
