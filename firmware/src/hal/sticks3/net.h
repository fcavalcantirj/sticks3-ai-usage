// firmware/src/hal/sticks3/net.h — always-on non-blocking Wi-Fi state machine.
//
// Modeled on Colibrino's startOtaNetworking/updateOta, WITHOUT OTA.
// Tracks {connecting, connected, lost}, re-issues WiFi.begin every 30s
// while not connected, emits [NET] lines only on state transitions.
#pragma once

#include <cstddef>
#include <cstdint>

namespace sticks3 {

// Initialise Wi-Fi: STA mode, hostname "sticks3-usage", auto-reconnect,
// then WiFi.begin(WIFI_SSID, WIFI_PASS).  Emits "[NET] state=connecting".
void netBegin();

// Poll the connection state.  Call once per loop with nowMs().
void netUpdate(uint32_t nowMs);

// True if Wi-Fi is currently connected (WL_CONNECTED).
bool netUp();

// Writes the current local IP into buf (dotted-quad), returns buf.
// Always NUL-terminated within n bytes.
const char* netIp(char* buf, size_t n);

} // namespace sticks3
