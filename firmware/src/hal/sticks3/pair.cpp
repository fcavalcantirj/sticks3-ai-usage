// firmware/src/hal/sticks3/pair.cpp — device half of the pairing handshake.
// See pair.h for the endpoint contract; internal/api/pairing.go is the
// authority for every rule this file merely reports.
#include "hal/sticks3/pair.h"

#include "hal/sticks3/board.h"  // nowMs, serialLine, buildId
#include "hal/sticks3/creds.h"  // credsSave

#include <ArduinoJson.h>
#include <HTTPClient.h>
#include <WiFi.h>
#include <cstdio>
#include <cstring>

namespace sticks3 {

namespace {

// The one route the device speaks to.  pairing.go: pairClaimPath.
constexpr char kClaimPath[] = "/v1/pair/claim";

// Self-reported, cosmetic name.  pairing.go sanitises it to 40 printable
// characters and it is never used for authorisation.  Plain ASCII with no
// quote or backslash, so the hand-built JSON body below needs no escaping.
constexpr char kDeviceName[] = "StickS3 AI usage";

// The HTTP round trip is synchronous, so the timeout is also the worst case a
// single loop pass can stall for.
constexpr uint16_t kHttpTimeoutMs = 5000;

// A claim response is a handful of small fields; anything larger is not one.
constexpr size_t kMaxBodyBytes = 1024;

// --- run state --------------------------------------------------------------

PairState g_state = PairState::Idle;

usage::provision::Record g_base{};    // ssid/pass/host/port/otaPass to keep
usage::provision::Record g_record{};  // what was actually stored, once Paired

char g_deviceId[24]                   = {0};  // "AA:BB:CC:DD:EE:FF"
char g_code[kPairCodeMax + 1]         = {0};
char g_message[kPairMessageMax + 1]   = {0};
int  g_expiresInSec                   = 0;
uint32_t g_nextPollMs                 = 0;
uint8_t  g_transportFails             = 0;

// --- helpers ----------------------------------------------------------------

void setMessage(const char* s) {
    // Copy printable ASCII only.  The source is a server-authored error string,
    // which is trusted enough to show but not trusted enough to paint raw on a
    // screen driver that has no idea what a control byte means.
    size_t j = 0;
    if (s != nullptr) {
        for (size_t i = 0; s[i] != '\0' && j < kPairMessageMax; i++) {
            unsigned char c = (unsigned char)s[i];
            if (c >= 0x20 && c < 0x7f) {
                g_message[j++] = (char)c;
            }
        }
    }
    g_message[j] = '\0';
}

// Copy the code the AGENT minted.  Same discipline as setMessage: printable
// only, bounded, and whatever length the server chose — the alphabet and the
// length are pairing.go's rules, not this file's, so they are not restated
// here.  Returns false when nothing usable survived.
bool setCode(const char* s) {
    size_t j = 0;
    if (s != nullptr) {
        for (size_t i = 0; s[i] != '\0' && j < kPairCodeMax; i++) {
            unsigned char c = (unsigned char)s[i];
            if (c > 0x20 && c < 0x7f) {
                g_code[j++] = (char)c;
            }
        }
    }
    g_code[j] = '\0';
    return j > 0;
}

// A token must survive a round trip through NVS and an HTTP header, so reject
// anything with whitespace, a control byte or no length at all.  This is a
// storage guard, not a re-implementation of the server's format.
bool tokenUsable(const char* t) {
    if (t == nullptr) {
        return false;
    }
    size_t len = std::strlen(t);
    if (len == 0 || len > usage::provision::kMaxToken) {
        return false;
    }
    for (size_t i = 0; i < len; i++) {
        unsigned char c = (unsigned char)t[i];
        if (c <= 0x20 || c >= 0x7f) {
            return false;
        }
    }
    return true;
}

void fail(const char* why) {
    g_state = PairState::Failed;
    g_code[0] = '\0';
    g_expiresInSec = 0;
    setMessage(why);

    char buf[96];
    std::snprintf(buf, sizeof(buf), "[PAIR] state=failed why=%s", g_message);
    serialLine(buf);
}

// Read this device's station MAC as the stable pairing id.  pairing.go accepts
// letters, digits and . : - _ , so the colon form passes its sanitiser
// unchanged, and re-pairing the SAME device then replaces its record instead of
// leaving a stale token behind.
//
// The uint8_t* form is used deliberately: it falls back to esp_read_mac() when
// the radio is not up, where the String form would return a MAC of zeros.
void loadDeviceId() {
    uint8_t mac[6] = {0};
    WiFi.macAddress(mac);
    std::snprintf(g_deviceId, sizeof(g_deviceId),
                  "%02X:%02X:%02X:%02X:%02X:%02X",
                  mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
}

// Store the issued token beside the credentials already in hand.
void storeToken(const char* token) {
    usage::provision::Record rec = g_base;
    std::snprintf(rec.token, sizeof(rec.token), "%s", token);

    if (!credsSave(rec)) {
        // A token the device holds but cannot remember is worse than no token:
        // the next boot would be unprovisioned again with the window closed.
        fail("could not store the token");
        return;
    }

    g_record = rec;
    g_state = PairState::Paired;
    g_code[0] = '\0';
    g_expiresInSec = 0;
    setMessage("paired");

    // LENGTH ONLY.  The token never appears on the serial line, on the screen,
    // or in any getter — it goes to NVS and to the X-Device-Token header.
    char buf[96];
    std::snprintf(buf, sizeof(buf),
                  "[PAIR] state=paired device_id=%s token_len=%d complete=%d",
                  g_deviceId, (int)std::strlen(rec.token),
                  usage::provision::complete(rec) ? 1 : 0);
    serialLine(buf);
}

// Interpret one claim response.  `body` is empty for codes with no payload.
void handleResponse(int code, const String& body) {
    JsonDocument doc;
    bool parsed = false;
    if (body.length() > 0 && body.length() <= kMaxBodyBytes) {
        // The (pointer, length) overload, exactly as model.cpp uses it: it does
        // not depend on ArduinoJson's Arduino-String integration being compiled
        // in, and it cannot read past the body.  DeserializationError converts
        // to true when it IS an error.
        DeserializationError derr = deserializeJson(doc, body.c_str(), body.length());
        parsed = !derr;
    }

    const char* state = parsed ? (doc["state"] | "") : "";
    const char* err   = parsed ? (doc["error"] | "") : "";

    switch (code) {
    case 202: {  // claimed — show the code the agent minted
        const char* c = parsed ? (doc["code"] | "") : "";
        if (!setCode(c)) {
            fail("agent sent no pairing code");
            return;
        }
        g_expiresInSec = parsed ? (doc["expires_in_sec"] | 0) : 0;
        if (g_state != PairState::Claimed) {
            setMessage("type this code into the dashboard");
        }
        g_state = PairState::Claimed;

        char buf[80];
        std::snprintf(buf, sizeof(buf),
                      "[PAIR] state=claimed code_len=%d expires_s=%d",
                      (int)std::strlen(g_code), g_expiresInSec);
        serialLine(buf);
        return;
    }

    case 200: {  // approved — the token is here, exactly once
        const char* token = parsed ? (doc["token"] | "") : "";
        if (!tokenUsable(token)) {
            // The window has already closed on the server side, so there is no
            // second chance in this run: surface it rather than poll a window
            // that will never answer again.
            fail("agent sent an unusable token");
            return;
        }
        storeToken(token);
        return;
    }

    case 403:
        // Two different 403s.  pairing.go answers the "no window" case with
        // {"state":"idle"}; the LAN-peer refusal carries no state at all, and
        // is not something waiting will fix.
        if (std::strcmp(state, "idle") == 0) {
            g_state = PairState::Waiting;
            g_code[0] = '\0';   // an expired window's code is dead — stop showing it
            g_expiresInSec = 0;
            setMessage("open pairing on the dashboard");
            serialLine("[PAIR] state=waiting (no window open)");
            return;
        }
        fail(err[0] != '\0' ? err : "the agent refused this device");
        return;

    case 409:  // another device holds the window
        g_state = PairState::Busy;
        g_code[0] = '\0';
        g_expiresInSec = 0;
        setMessage(err[0] != '\0' ? err : "another device is pairing");
        serialLine("[PAIR] state=busy");
        return;

    case 429:  // rate limited — slow down, keep the current state
        g_nextPollMs += kPairRateLimitMs;
        serialLine("[PAIR] http=429 backing off");
        return;

    case 400:  // our request shape is wrong: a bug, not something to retry
        fail(err[0] != '\0' ? err : "the agent rejected the request");
        return;

    default: {
        char buf[64];
        std::snprintf(buf, sizeof(buf), "[PAIR] http=%d unexpected", code);
        serialLine(buf);
        setMessage("the agent answered unexpectedly");
        return;
    }
    }
}

// One POST /v1/pair/claim.  Blocks for the round trip.
void poll() {
    WiFiClient client;
    HTTPClient http;
    http.setTimeout(kHttpTimeoutMs);
    http.setReuse(false);

    char url[128];
    std::snprintf(url, sizeof(url), "http://%s:%u%s",
                  g_base.host, (unsigned)g_base.port, kClaimPath);
    http.begin(client, url);

    // setUserAgent, never addHeader: HTTPClient silently DROPS a User-Agent
    // added as a header (HTTPClient.cpp:1040-1046 skips Connection,
    // User-Agent, Host and Authorization).  The agent classifies the caller by
    // this string, so a dropped one makes the device unidentifiable.
    char ua[64];
    std::snprintf(ua, sizeof(ua), "sticks3-usage/%s", buildId());
    http.setUserAgent(ua);

    // auth.go enforces the JSON content type even on the unauthenticated claim
    // route, so a cross-site form POST bounces before the handler.
    http.addHeader("Content-Type", "application/json");
    // No X-Device-Token: an unpaired device has none, and this is the one route
    // that does not want one.

    // Both values are plain ASCII with no quote or backslash (a MAC in hex and
    // a fixed literal), so the body needs no escaping.
    char body[128];
    std::snprintf(body, sizeof(body), "{\"device_id\":\"%s\",\"name\":\"%s\"}",
                  g_deviceId, kDeviceName);

    int code = http.POST(String(body));
    if (code < 0) {
        g_transportFails++;
        char buf[80];
        std::snprintf(buf, sizeof(buf), "[PAIR] err=%s code=%d fails=%u",
                      code <= -4 ? "timeout" : "conn", code,
                      (unsigned)g_transportFails);
        serialLine(buf);
        http.end();
        if (g_transportFails >= kPairMaxTransportFails) {
            fail("cannot reach the agent");
        } else {
            setMessage("looking for the agent...");
        }
        return;
    }

    g_transportFails = 0;
    String payload;
    if (http.getSize() != 0) {
        payload = http.getString();
        if (payload.length() > kMaxBodyBytes) {
            payload = "";
        }
    }
    http.end();

    handleResponse(code, payload);
}

} // namespace

// --- public API -------------------------------------------------------------

bool pairBegin(const usage::provision::Record& base, uint32_t nowMs) {
    if (base.host[0] == '\0' || base.port == 0) {
        // Nothing was discovered, so there is nothing to poll.  Inventing a
        // default here is precisely the hardcoded address this work removes.
        serialLine("[PAIR] refused: no agent address discovered");
        return false;
    }

    g_base = base;
    g_base.token[0] = '\0';  // a stale token must not travel into the new record
    g_record = usage::provision::Record{};
    g_code[0] = '\0';
    g_expiresInSec = 0;
    g_transportFails = 0;
    g_nextPollMs = nowMs;  // poll on the next pairUpdate()
    g_state = PairState::Waiting;
    setMessage("open pairing on the dashboard");
    loadDeviceId();

    char buf[96];
    std::snprintf(buf, sizeof(buf), "[PAIR] start agent=%s:%u device_id=%s",
                  g_base.host, (unsigned)g_base.port, g_deviceId);
    serialLine(buf);
    return true;
}

void pairUpdate(uint32_t nowMs) {
    switch (g_state) {
    case PairState::Idle:
    case PairState::Paired:
    case PairState::Failed:
        return;
    default:
        break;
    }

    if ((int32_t)(nowMs - g_nextPollMs) < 0) {
        return;
    }
    // Schedule the next poll BEFORE the blocking round trip, so a slow response
    // does not turn into a burst the moment it returns.  handleResponse() may
    // push this further out on a 429.
    g_nextPollMs = nowMs + kPairPollMs;

    if (WiFi.status() != WL_CONNECTED) {
        setMessage("waiting for Wi-Fi...");
        return;
    }
    poll();
}

PairState pairState() {
    return g_state;
}

const char* pairCode() {
    return g_state == PairState::Claimed ? g_code : "";
}

int pairExpiresInSec() {
    return g_expiresInSec;
}

const char* pairMessage() {
    return g_message;
}

const usage::provision::Record& pairRecord() {
    return g_record;
}

uint8_t pairTransportFails() {
    return g_transportFails;
}

void pairReset() {
    g_state = PairState::Idle;
    g_code[0] = '\0';
    g_message[0] = '\0';
    g_expiresInSec = 0;
    g_transportFails = 0;
    g_nextPollMs = 0;
}

} // namespace sticks3
