// firmware/src/usage/model.h — v1 snapshot Model parsed from the Go API JSON.
//
// Pure C++17, NO Arduino/M5 headers (ArduinoJson single header allowed — it is
// platform-independent).  All fields are bounded POD: no heap allocation, no
// std::string, suitable for an ESP32-S3 fetch/render path.
#pragma once

#include <cstddef>
#include <cstdint>

namespace usage {

// Row: a single usage metric within a provider block.
struct Row {
    char k[12];       // up to 11 chars + NUL
    char label[11];   // up to 10 chars + NUL
    int16_t pct;      // -1 = null (no percentage)
    char txt[12];     // up to 11 chars + NUL
    uint8_t tier;     // 0 ok, 1 warn, 2 crit, 3 off
};

// Provider: one AI provider's usage block.
struct Provider {
    char id[24];      // up to 23 chars + NUL  (e.g. "openrouter:main")
    char label[24];   // up to 23 chars + NUL  (e.g. "OpenRouter main", "OpenRouter fallback")
    char plan[12];    // up to 11 chars + NUL  (e.g. "max_20x", "plus", "free")
    char kind[8];     // "plan", "credit", "free" — drives firmware page grouping
    uint8_t severity; // 0 ok, 1 warn, 2 crit, 3 off
    uint8_t status;   // 0 ok, 1 stale, 2 auth, 3 error, 4 off
    char msg[25];     // up to 24 chars + NUL  (e.g. "run claude", "no key")
    uint8_t rowCount;
    Row rows[6];
};

// Model: the parsed snapshot — the ONE v1 contract every firmware client uses.
struct Model {
    uint8_t v;            // must be 1
    uint32_t seq;         // +1 only when rev changes
    char rev[9];          // 8 hex chars + NUL
    uint32_t generatedAt; // unix s of last CHANGE
    uint16_t nextSec;     // seconds until next poll (900)
    uint32_t age;         // ORDER #65: server-computed age in seconds (now - checked_at at fetch time)
    uint8_t providerCount;
    Provider providers[6];
};

// parseSnapshot parses a v1 JSON snapshot into a bounded Model.
// Returns true on success.  On failure returns false and writes a
// human-readable reason into err (up to errLen bytes, NUL-terminated).
// Uses ArduinoJson (heap-allocated JsonDocument on the host; acceptable at
// fetch time on the device — NOT in the render loop).
bool parseSnapshot(const char* json, size_t len, Model& out,
                   char* err, size_t errLen);

} // namespace usage
