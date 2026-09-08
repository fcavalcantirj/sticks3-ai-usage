// firmware/src/usage/model.cpp — implementation of parseSnapshot.
//
// Pure C++17, no Arduino/M5 headers.  Uses the vendored ArduinoJson single
// header (firmware/third_party/ArduinoJson.h).
#include "usage/model.h"

// Disable Arduino-specific extensions before pulling in the vendored header
// (same pattern as test_smoke.cpp).
#define ARDUINOJSON_ENABLE_ARDUINO_STRING 0
#define ARDUINOJSON_ENABLE_ARDUINO_STREAM 0
#define ARDUINOJSON_ENABLE_STD_STREAM 0
#include <ArduinoJson.h>
#include <cstdio>
#include <cstring>

namespace usage {

// --- string → enum helpers ----------------------------------------------

static uint8_t tierFromText(const char* s) {
    // 0 ok, 1 warn, 2 crit, 3 off.  Unknown → ok (0).
    if (s == nullptr) return 0;
    if (std::strcmp(s, "warn") == 0) return 1;
    if (std::strcmp(s, "crit") == 0) return 2;
    if (std::strcmp(s, "off") == 0) return 3;
    return 0; // "ok" or anything else
}

static uint8_t statusFromText(const char* s) {
    // 0 ok, 1 stale, 2 auth, 3 error, 4 off.  Unknown → ok (0).
    if (s == nullptr) return 0;
    if (std::strcmp(s, "stale") == 0) return 1;
    if (std::strcmp(s, "auth") == 0) return 2;
    if (std::strcmp(s, "error") == 0) return 3;
    if (std::strcmp(s, "off") == 0) return 4;
    return 0; // "ok" or anything else
}

// --- bounded string copy ------------------------------------------------

// Copies src into dst, truncating silently at n-1 bytes, always NUL-term.
// Handles null src.  This is the strlcpy equivalent the spec asks for.
static void copyStr(char* dst, const char* src, size_t n) {
    if (n == 0) return;
    if (src == nullptr) { dst[0] = '\0'; return; }
    size_t i;
    for (i = 0; i < n - 1 && src[i] != '\0'; i++)
        dst[i] = src[i];
    dst[i] = '\0';
}

// --- parse --------------------------------------------------------------

bool parseSnapshot(const char* json, size_t len, Model& out,
                   char* err, size_t errLen) {
    if (err == nullptr || errLen == 0)
        return false;
    err[0] = '\0';

    // ArduinoJson 7.x: JsonDocument auto-sizes on the heap (host).
    JsonDocument doc;
    DeserializationError derr = deserializeJson(doc, json, len);
    if (derr) {
        std::snprintf(err, errLen, "deserialize: %s", derr.c_str());
        return false;
    }

    // v must be 1.
    JsonVariant vVar = doc["v"];
    if (!vVar.is<int>() || vVar.as<int>() != 1) {
        int got = vVar.is<int>() ? vVar.as<int>() : 0;
        std::snprintf(err, errLen, "v must be 1, got %d", got);
        return false;
    }

    // rev must exist and be non-empty (8 hex chars — we copy bounded).
    const char* rev = doc["rev"].as<const char*>();
    if (rev == nullptr || rev[0] == '\0') {
        std::snprintf(err, errLen, "rev is missing");
        return false;
    }

    // Zero-init the output so untouched fields are clean.
    std::memset(&out, 0, sizeof(out));

    out.v = 1;
    copyStr(out.rev, rev, sizeof(out.rev));
    out.seq = (uint32_t)doc["seq"].as<unsigned long>();
    out.generatedAt = (uint32_t)doc["generated_at"].as<unsigned long>();
    out.nextSec = (uint16_t)doc["next_sec"].as<unsigned int>();
    out.age = (uint32_t)doc["age"].as<unsigned long>(); // ORDER #65: server-computed freshness age

    JsonArray providers = doc["providers"].as<JsonArray>();
    size_t count = providers.size();
    if (count > 6) {
        count = 6;  // clamp: silently drop extras beyond array capacity
    }
    out.providerCount = (uint8_t)count;

    int pi = 0;
    for (JsonObject p : providers) {
        if (pi >= (int)count) break;  // respect clamp
        Provider& prov = out.providers[pi];
        copyStr(prov.id, p["id"].as<const char*>(), sizeof(prov.id));
        copyStr(prov.label, p["label"].as<const char*>(), sizeof(prov.label));
        copyStr(prov.plan, p["plan"].as<const char*>(), sizeof(prov.plan));
        copyStr(prov.kind, p["kind"].as<const char*>(), sizeof(prov.kind));
        prov.severity = tierFromText(p["severity"].as<const char*>());
        prov.status = statusFromText(p["status"].as<const char*>());
        copyStr(prov.msg, p["msg"].as<const char*>(), sizeof(prov.msg));

        JsonArray rows = p["rows"].as<JsonArray>();
        if (rows.size() > 6) {
            std::snprintf(err, errLen, "too many rows for %s: %u",
                          prov.id, (unsigned)rows.size());
            return false;
        }
        prov.rowCount = (uint8_t)rows.size();

        int ri = 0;
        for (JsonObject r : rows) {
            Row& row = prov.rows[ri];
            copyStr(row.k, r["k"].as<const char*>(), sizeof(row.k));
            copyStr(row.label, r["label"].as<const char*>(),
                    sizeof(row.label));
            // pct null → -1; otherwise the integer percentage.
            JsonVariant pctVar = r["pct"];
            if (pctVar.isNull()) {
                row.pct = -1;
            } else {
                row.pct = (int16_t)pctVar.as<int>();
            }
            copyStr(row.txt, r["txt"].as<const char*>(), sizeof(row.txt));
            row.tier = tierFromText(r["tier"].as<const char*>());
            ri++;
        }
        pi++;
    }

    return true;
}

} // namespace usage
