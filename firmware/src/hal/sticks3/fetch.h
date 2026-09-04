// firmware/src/hal/sticks3/fetch.h — conditional HTTP fetch of /v1/usage.
#pragma once

#include <Arduino.h>  // String
#include <cstdint>

namespace sticks3 {

// Result of fetchUsage: code is the HTTP status (negative = error);
// rev holds the stripped ETag; body holds the response (only on 200).
struct FetchResult {
    int code;         // HTTP status code (negative = transport error)
    char rev[9];      // ETag stripped of W/ prefix and quotes
    uint32_t ms;      // elapsed time in milliseconds
    String body;      // response body (only populated on 200)
};

// Fetch /v1/usage with If-None-Match conditional request.
// Emits a [FETCH] line via serial_proto.  Returns true on 200/304,
// false on transport error or non-200/304 HTTP codes.
bool fetchUsage(const char* lastRev, FetchResult& out);

} // namespace sticks3
