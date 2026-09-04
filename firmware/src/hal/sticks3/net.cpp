// firmware/src/hal/sticks3/net.cpp — Wi-Fi state machine implementation.
#include "hal/sticks3/net.h"

#include "hal/sticks3/board.h"   // serialLine
#include "usage/serial_proto.h"   // fmtNet, fmtOta

#include "secrets.h"  // WIFI_SSID, WIFI_PASS, OTA_PASS — must exist (git-ignored)

#ifndef WIFI_SSID
#error "copy include/secrets.h.example to include/secrets.h"
#endif

#ifndef OTA_PASS
#error "OTA_PASS undefined — copy the OTA_PASS #define from firmware/include/secrets.h.example into firmware/include/secrets.h"
#endif

#include <ArduinoOTA.h>
#include <WiFi.h>
#include <cstdio>
#include <cstring>

namespace sticks3 {

// --- state ------------------------------------------------------------------

enum : uint8_t {
    NET_DISCONNECTED = 0,  // not started
    NET_CONNECTING,      // WiFi.begin issued, waiting
    NET_CONNECTED,       // WL_CONNECTED
    NET_LOST,            // was connected, now down
};

static uint8_t g_state = NET_DISCONNECTED;
static uint32_t g_lastBegin = 0;

static void emitNet(const char* state, const char* ip) {
    char buf[64];
    usage::fmtNet(buf, sizeof(buf), state, ip);
    serialLine(buf);
}

// --- public API -------------------------------------------------------------

void netBegin() {
    WiFi.persistent(false);
    WiFi.mode(WIFI_STA);
    WiFi.setHostname("sticks3-usage");
    WiFi.setAutoReconnect(true);
    WiFi.setSleep(true);
    WiFi.begin(WIFI_SSID, WIFI_PASS);

    g_lastBegin = millis();
    g_state = NET_CONNECTING;
    emitNet("connecting", nullptr);
}

void netUpdate(uint32_t nowMs) {
    bool connected = (WiFi.status() == WL_CONNECTED);

    if (connected) {
        if (g_state != NET_CONNECTED) {
            char ipBuf[16];
            netIp(ipBuf, sizeof(ipBuf));
            emitNet("connected", ipBuf);
            g_state = NET_CONNECTED;
            // Arm OTA on every connect — re-arms after any reconnect
            // (Colibrino ptt.ino lineage: a once-ever guard is the OTA-after-
            // sleep bug; see ORDER #22).
            otaBegin();
        }
        return;
    }

    // Not connected.
    if (g_state == NET_CONNECTED) {
        emitNet("lost", nullptr);
        g_state = NET_LOST;
    }

    // Re-begin every 30 s while not connected.
    if ((int32_t)(nowMs - g_lastBegin) >= 30000) {
        WiFi.begin(WIFI_SSID, WIFI_PASS);
        g_lastBegin = nowMs;
        if (g_state == NET_LOST) {
            emitNet("connecting", nullptr);
        }
        g_state = NET_CONNECTING;
    }
}

bool netUp() {
    return WiFi.status() == WL_CONNECTED;
}

const char* netIp(char* buf, size_t n) {
    IPAddress ip = WiFi.localIP();
    std::snprintf(buf, n, "%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3]);
    return buf;
}

// --- OTA --------------------------------------------------------------------

namespace {

bool g_otaStarted = false;      // ArduinoOTA.begin() called
bool g_otaInProgress = false;   // a transfer is running
uint8_t g_otaPercent = 255;     // 0..100 or 255 when idle

// Emit a serial line through fmtOta.
void emitOta(const char* kind, unsigned int param) {
    char buf[64];
    usage::fmtOta(buf, sizeof(buf), kind, param);
    serialLine(buf);
}

} // namespace

void otaBegin() {
    // Re-arm on every call: end() is a no-op if not started.  This matches the
    // Colibrino ptt.ino lineage where OTA is re-armed on EVERY Wi-Fi join
    // (a `static` once-ever guard is the bug that killed OTA after the first
    // sleep — see ORDER #22 lineage notes).
    ArduinoOTA.end();

    ArduinoOTA.setHostname("sticks3-usage");
    ArduinoOTA.setPassword(OTA_PASS);

    ArduinoOTA.onStart([]() {
        g_otaInProgress = true;
        g_otaPercent = 0;
        emitOta("start", 0);
    });
    ArduinoOTA.onProgress([](unsigned int done, unsigned int total) {
        const unsigned int pct = total == 0 ? 0u : (done * 100u) / total;
        // Throttle: only emit on percent change (matches serial line budget).
        if (pct != g_otaPercent) {
            g_otaPercent = (uint8_t)pct;
            emitOta("pct", pct);
        }
    });
    ArduinoOTA.onEnd([]() {
        emitOta("end", 0);
    });
    ArduinoOTA.onError([](ota_error_t error) {
        g_otaInProgress = false;
        g_otaPercent = 255;
        emitOta("err", static_cast<unsigned int>(error));
    });

    ArduinoOTA.begin();
    g_otaStarted = true;
    g_otaInProgress = false;
    g_otaPercent = 255;
}

void otaHandle() {
    // Only handle when the service is armed; otherwise no-op.
    if (!g_otaStarted) return;
    ArduinoOTA.handle();
}

bool otaInProgress() {
    return g_otaInProgress;
}

uint8_t otaPercent() {
    return g_otaPercent;
}

} // namespace sticks3
