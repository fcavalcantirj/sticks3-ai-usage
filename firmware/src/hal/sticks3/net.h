// firmware/src/hal/sticks3/net.h — always-on non-blocking Wi-Fi state machine.
//
// Modeled on Colibrino's startOtaNetworking/updateOta, WITHOUT OTA.
// Tracks {connecting, connected, lost}, re-issues WiFi.begin every 30s
// while not connected, emits [NET] lines only on state transitions.
#pragma once

#include <cstddef>
#include <cstdint>

#include "usage/provision.h"   // usage::provision::Record (pure, no Arduino headers)

namespace sticks3 {

// Initialise Wi-Fi: STA mode, hostname "sticks3-usage", auto-reconnect,
// then WiFi.begin() with the credentials from the record.  Emits
// "[NET] state=connecting".
//
// The record comes from NVS (task 76), NOT from secrets.h.  Compile-time
// credentials are what made firmware.bin unpublishable; the ssid, passphrase
// and OTA password are copied into file-static buffers here so a later
// re-provision cannot leave this module pointing at freed storage.
void netBegin(const usage::provision::Record& rec);

// Poll the connection state.  Call once per loop with nowMs().
void netUpdate(uint32_t nowMs);

// True if Wi-Fi is currently connected (WL_CONNECTED).
bool netUp();

// Stop the station: cancel any join in flight and the driver's own reconnect,
// WITHOUT clearing the stored credentials.
//
// THE PORTAL CANNOT SCAN WHILE THE STATION IS CHASING A NETWORK THAT IS NOT
// THERE. AP_STA shares one radio, and a station stuck retrying starves
// scanNetworks() — every scan comes back failed, so the setup page lists
// nothing and the recovery path leads somewhere unusable. Observed on hardware
// 2026-09-07: 17 consecutive "[SCAN] failed" immediately after the portal was
// raised by repeated join failures.
//
// docs/DEVICES.md records the measurement behind this: scanning in AP_STA was
// proven fine, but only ever with the station CONNECTED.
void netStop();

// Writes the current local IP into buf (dotted-quad), returns buf.
// Always NUL-terminated within n bytes.
const char* netIp(char* buf, size_t n);

// --- OTA -------------------------------------------------------------------
//
// Armed after the first Wi-Fi connection in setup.  The hostname is
// "sticks3-usage" and the password comes from the NVS record passed to
// netBegin().  When no OTA password is stored, OTA is NOT armed at all: an
// unauthenticated OTA listener would let anyone on the LAN reflash the device.
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

// Update the stored OTA password in RAM and re-arm OTA, WITHOUT tearing down
// Wi-Fi.  Called when a partial BLE update delivers a new otaPass to an already-
// provisioned device: credsSaveOtaPass() persists it, then otaRearm() copies it
// into g_otaPass and calls otaBegin().  No-op if the password is empty (OTA
// stays disarmed).
void otaRearm(const char* pass);

// Drive an in-progress transfer (calls ArduinoOTA.handle()).
void otaHandle();

// True while an authenticated firmware transfer is running.
bool otaInProgress();

// Last reported percentage (0..100) of the current/most recent transfer.
// Returns 255 when idle.
uint8_t otaPercent();

} // namespace sticks3
