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

// --- OTA -------------------------------------------------------------------
//
// Armed after the first Wi-Fi connection in setup.  The hostname is
// "sticks3-usage" and the password comes from OTA_PASS in secrets.h.
//
// otaBegin() must be called once Wi-Fi is up (netUpdate calls it on the
// first transition to NET_CONNECTED).  otaHandle() drives the transfer and
// must be called every loop pass; it may block for the duration of a chunk.
//
// While a transfer is in flight, the main loop should skip fetch/render and
// paint "OTA <pct>%" on screen instead.

// One-time setup: set hostname, password, callbacks, ArduinoOTA.begin().
// Safe to call again after a reconnect (calls end() first).
void otaBegin();

// Drive an in-progress transfer (calls ArduinoOTA.handle()).
void otaHandle();

// True while an authenticated firmware transfer is running.
bool otaInProgress();

// Last reported percentage (0..100) of the current/most recent transfer.
// Returns 255 when idle.
uint8_t otaPercent();

} // namespace sticks3
