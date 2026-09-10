// firmware/src/hal/sticks3/net.cpp — Wi-Fi state machine implementation.
#include "hal/sticks3/net.h"

#include "hal/sticks3/board.h"   // serialLine
#include "usage/serial_proto.h"   // fmtNet, fmtOta

// NOTE (task 76): secrets.h is deliberately NOT included here any more, and the
// two #error guards that required it are gone.  Credentials now arrive from NVS
// via netBegin(), because a compile-time header put the SSID, the Wi-Fi
// password and the OTA password into firmware.bin as plaintext strings and made
// the binary impossible to publish.  The production build compiles with no
// secrets.h at all.

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

// Credentials copied out of the NVS record at netBegin().  Owned here so a
// reconnect (which re-issues WiFi.begin) never depends on the caller keeping
// the record alive.  NEVER logged — only lengths or "set"/"unset" ever are.
static char g_ssid[usage::provision::kMaxSsid + 1] = {0};
static char g_pass[usage::provision::kMaxPass + 1] = {0};
static char g_otaPass[usage::provision::kMaxOtaPass + 1] = {0};

static void emitNet(const char* state, const char* ip) {
    char buf[64];
    usage::fmtNet(buf, sizeof(buf), state, ip);
    serialLine(buf);
}

// --- public API -------------------------------------------------------------

void netBegin(const usage::provision::Record& rec) {
    std::snprintf(g_ssid, sizeof(g_ssid), "%s", rec.ssid);
    std::snprintf(g_pass, sizeof(g_pass), "%s", rec.pass);
    std::snprintf(g_otaPass, sizeof(g_otaPass), "%s", rec.otaPass);

    // Must precede the first radio call: _persistent is read exactly once, by
    // wifiLowLevelInit behind a one-shot guard, and only there does the core
    // call esp_wifi_set_storage(WIFI_STORAGE_RAM).  Called later it silently
    // does nothing and esp_wifi_set_config writes the credentials to NVS
    // anyway — which would put them back in flash under the core's own keys.
    WiFi.persistent(false);
    // setHostname() MUST precede mode(): it only writes a file-static buffer
    // (WiFiGeneric.cpp:901-905), and the ONLY place that buffer is pushed to
    // the netif is inside mode() (:1264-1270), which early-returns when the
    // requested mode already equals the current one (:1252-1254).  Called
    // after mode(), it is a silent no-op and the DHCP hostname stays the
    // default esp32s3-<last 3 MAC bytes>.
    // This went unnoticed for a reason worth recording: sticks3-usage.local
    // still resolves, because mDNS is a SEPARATE name published by
    // ArduinoOTA.setHostname() -> MDNS.begin() in otaBegin().  Only the DHCP
    // hostname (what the router shows) was wrong.
    WiFi.setHostname("sticks3-usage");
    WiFi.mode(WIFI_STA);
    WiFi.setAutoReconnect(true);
    WiFi.setSleep(true);
    WiFi.begin(g_ssid, g_pass);

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
        WiFi.begin(g_ssid, g_pass);
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

void netStop() {
    // false: leave the radio powered — the portal still needs AP mode.
    // false: keep the stored credentials, this is not a factory reset.
    WiFi.disconnect(false, false);
    g_state = NET_DISCONNECTED;
    g_lastBegin = 0;
    emitNet("stopped", nullptr);
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

    // No stored OTA password means OTA stays DISARMED.  Arming it unauthenticated
    // would let anyone on the LAN reflash the device, which is a strictly worse
    // outcome than losing over-the-air updates until one is provisioned.
    if (g_otaPass[0] == '\0') {
        serialLine("[OTA] disarmed (no ota_pass stored)");
        g_otaStarted = false;
        return;
    }

    ArduinoOTA.setHostname("sticks3-usage");
    ArduinoOTA.setPassword(g_otaPass);

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

void otaRearm(const char* pass) {
    // Copy the new password into g_otaPass (bounded + terminated, matching
    // netBegin's discipline) then re-arm.  otaBegin() itself is the no-empty-
    // password guard: an empty pass leaves OTA disarmed rather than calling
    // ArduinoOTA.begin() with no password.
    if (pass != nullptr) {
        size_t i = 0;
        while (i < usage::provision::kMaxOtaPass && pass[i] != '\0') {
            g_otaPass[i] = pass[i];
            ++i;
        }
        g_otaPass[i] = '\0';
    }
    otaBegin();
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
