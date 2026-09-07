// firmware/src/hal/sticks3/fetch.cpp — conditional HTTP fetch implementation.
#include "hal/sticks3/fetch.h"

#include "hal/sticks3/board.h"   // nowMs, serialLine
#include "usage/serial_proto.h"   // fmtFetch

// NOTE (task 76): secrets.h is deliberately NOT included here any more, and the
// #error guard that required it is gone.  The agent address and the device
// token arrive from NVS via fetchConfigure(); as compile-time macros they were
// plaintext strings inside firmware.bin.

#include <HTTPClient.h>
#include <WiFi.h>
#include <cstdio>

namespace sticks3 {

namespace {

// Agent address and credential, copied from the NVS record by fetchConfigure().
// The token is NEVER logged — the access-log and serial paths deliberately
// carry only status codes and rev values.
char     g_host[usage::provision::kMaxHost + 1]   = {0};
uint16_t g_port                                    = 0;
char     g_token[usage::provision::kMaxToken + 1] = {0};

// Copy the rev portion out of an ETag header value, stripping a leading
// W/" (weak validator) or bare " (strong validator) prefix and any
// trailing quote.  Always NUL-terminates within n bytes.
void extractRev(char* out, size_t n, const char* etag) {
    if (n == 0) return;
    out[0] = '\0';
    if (etag == nullptr) return;

    const char* p = etag;
    if (p[0] == 'W' && p[1] == '/' && p[2] == '"') {
        p += 3;
    } else if (p[0] == '"') {
        p += 1;
    }

    size_t i;
    for (i = 0; i < n - 1 && p[i] != '\0' && p[i] != '"'; i++)
        out[i] = p[i];
    out[i] = '\0';
}

} // namespace

void fetchConfigure(const usage::provision::Record& rec) {
    std::snprintf(g_host, sizeof(g_host), "%s", rec.host);
    std::snprintf(g_token, sizeof(g_token), "%s", rec.token);
    g_port = rec.port != 0 ? rec.port : usage::provision::kDefaultPort;
}

bool fetchConfigured() {
    return g_host[0] != '\0' && g_token[0] != '\0';
}

bool fetchUsage(const char* lastRev, FetchResult& out, uint32_t ageS) {
    out.code = 0;
    out.rev[0] = '\0';
    out.ms = 0;
    out.body = "";

    WiFiClient client;
    HTTPClient http;
    http.setTimeout(8000);
    http.setReuse(false);

    char url[128];
    if (ageS > 0) {
        std::snprintf(url, sizeof(url), "http://%s:%d/v1/usage?age_s=%u",
                      g_host, (int)g_port, ageS);
    } else {
        std::snprintf(url, sizeof(url), "http://%s:%d/v1/usage",
                      g_host, (int)g_port);
    }
    http.begin(client, url);

    // User-Agent so the server classifies this HTTP client as the StickS3
    // device (ORDER #63 task 64), not a browser or loopback curl.
    // NOTE: HTTPClient::addHeader silently skips "User-Agent" (managed
    // internally — HTTPClient.cpp:1040-1046).  Must use setUserAgent().
    char ua[64];
    std::snprintf(ua, sizeof(ua), "sticks3-usage/%s", buildId());
    http.setUserAgent(ua);

    // Tell HTTPClient to capture the ETag response header.
    const char* keys[] = {"ETag"};
    http.collectHeaders(keys, 1);

    // Device-token header (always sent to the usaged server).
    http.addHeader("X-Device-Token", g_token);

    // Conditional request: If-None-Match with quoted lastRev.
    if (lastRev != nullptr && lastRev[0] != '\0') {
        char etag[16];
        std::snprintf(etag, sizeof(etag), "\"%s\"", lastRev);
        http.addHeader("If-None-Match", etag);
    }

    uint32_t startMs = nowMs();
    int code = http.GET();
    out.ms = nowMs() - startMs;

    if (code < 0) {
        // Transport error: -4 and below are timeouts, -1/-2/-3 are conn errors.
        const char* errDesc = (code <= -4) ? "timeout" : "conn";
        char buf[64];
        usage::fmtFetch(buf, sizeof(buf), code, errDesc, 0, out.ms);
        serialLine(buf);
        out.code = code;
        http.end();
        return false;
    }

    // Extract ETag rev from the response headers.
    String etagStr = http.header("ETag");
    if (etagStr.length() > 0) {
        extractRev(out.rev, sizeof(out.rev), etagStr.c_str());
    }

    if (code == 304) {
        // Not Modified: no body, no seq.
        char buf[64];
        usage::fmtFetch(buf, sizeof(buf), code, out.rev, 0, out.ms);
        serialLine(buf);
        out.code = code;
        http.end();
        return true;
    }

    if (code == 200) {
        // OK: read body, reject if larger than 8 KB.
        String body = http.getString();
        if (body.length() > 8192) {
            char buf[64];
            usage::fmtFetch(buf, sizeof(buf), -1, "body too large", 0, out.ms);
            serialLine(buf);
            out.code = -1;
            http.end();
            return false;
        }
        out.body = body;
        out.code = code;
        char buf[64];
        usage::fmtFetch(buf, sizeof(buf), code, out.rev, 0, out.ms);
        serialLine(buf);
        http.end();
        return true;
    }

    // Other HTTP status codes (401, 403, 500, …).
    char buf[64];
    usage::fmtFetch(buf, sizeof(buf), code, out.rev, 0, out.ms);
    serialLine(buf);
    out.code = code;
    http.end();
    return false;
}

// --- ORDER #38: POST /v1/refresh -----------------------------------------------

bool refreshUpstream(FetchResult& out) {
    out.code = 0;
    out.rev[0] = '\0';
    out.ms = 0;
    out.body = "";

    WiFiClient client;
    HTTPClient http;
    http.setTimeout(8000);
    http.setReuse(false);

    char url[128];
    std::snprintf(url, sizeof(url), "http://%s:%d/v1/refresh",
                  g_host, (int)g_port);
    http.begin(client, url);

    // User-Agent so the server classifies this HTTP client as the StickS3
    // device (ORDER #63 task 64).  Must use setUserAgent, not addHeader.
    char ua[64];
    std::snprintf(ua, sizeof(ua), "sticks3-usage/%s", buildId());
    http.setUserAgent(ua);

    // Device-token header (always sent to the usaged server).
    http.addHeader("X-Device-Token", g_token);
    http.addHeader("Content-Type", "application/json");

    uint32_t startMs = nowMs();
    int code = http.POST("");
    out.ms = nowMs() - startMs;

    if (code < 0) {
        // Transport error.
        const char* errDesc = (code <= -4) ? "timeout" : "conn";
        char buf[64];
        usage::fmtRefresh(buf, sizeof(buf), code, out.ms);
        serialLine(buf);
        out.code = code;
        http.end();
        return false;
    }

    // Extract ETag rev if present (refresh carries the same ETag semantics).
    String etagStr = http.header("ETag");
    if (etagStr.length() > 0) {
        extractRev(out.rev, sizeof(out.rev), etagStr.c_str());
    }

    if (code == 202) {
        // Poll still running — caller should retry once.
        char buf[64];
        usage::fmtRefresh(buf, sizeof(buf), 202, out.ms);
        serialLine(buf);
        out.code = 202;
        http.end();
        return true;  // true = "response received", code tells the caller what to do
    }

    if (code == 200) {
        String body = http.getString();
        if (body.length() > 8192) {
            char buf[64];
            usage::fmtRefresh(buf, sizeof(buf), -1, out.ms);
            serialLine(buf);
            out.code = -1;
            http.end();
            return false;
        }
        out.body = body;
        out.code = 200;
        char buf[64];
        usage::fmtRefresh(buf, sizeof(buf), 200, out.ms);
        serialLine(buf);
        http.end();
        return true;
    }

    // Other HTTP status codes.
    char buf[64];
    usage::fmtRefresh(buf, sizeof(buf), code, out.ms);
    serialLine(buf);
    out.code = code;
    http.end();
    return false;
}

} // namespace sticks3
