// firmware/src/hal/sticks3/net.cpp — Wi-Fi state machine implementation.
#include "hal/sticks3/net.h"

#include "hal/sticks3/board.h"   // serialLine
#include "usage/serial_proto.h"   // fmtNet

#include "secrets.h"  // WIFI_SSID, WIFI_PASS — must exist (git-ignored)

#ifndef WIFI_SSID
#error "copy include/secrets.h.example to include/secrets.h"
#endif

#include <WiFi.h>
#include <cstdio>

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

} // namespace sticks3
