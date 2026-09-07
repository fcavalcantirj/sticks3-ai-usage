// firmware/src/hal/sticks3/creds.h — NVS-backed credential store (task 76).
//
// Only creds.cpp (and main.cpp) may include <Preferences.h>; the RULES live in
// usage/provision.h, which is pure C++17 and host-tested.  Same split as
// usage/power.h (policy) vs hal/sticks3/power.h (hardware).
//
// WHY THIS EXISTS: secrets.h is a compile-time header, so WIFI_SSID,
// WIFI_PASS, USAGED_HOST, USAGED_DEVICE_TOKEN and OTA_PASS all end up as
// plaintext strings inside firmware.bin — verified with `strings` against a
// real build — and the binary therefore cannot be published.  Credentials live
// in NVS instead.  secrets.h survives ONLY as a first-boot seed for a
// developer's own board; the production build compiles with no secrets.h at
// all and carries no credential strings.
//
// The record lives in NVS namespace "usaged" — the SAME namespace as rotation
// and brightness (board.cpp) — under these keys:
//
//   wifi_ssid    string   ssid
//   wifi_pass    string   pass     (empty = open network, a legal value)
//   agent_host   string   host
//   agent_port   uint16   port     (unset or 0 falls back to kDefaultPort)
//   dev_token    string   token
//   ota_pass     string   otaPass
//
// NVS caps a key at 15 characters; static_asserts in creds.cpp keep it so.
//
// Nothing here logs a credential.  Callers that want evidence on the serial
// line must print lengths or a "set"/"unset" flag, never a value.
#pragma once

#include "usage/provision.h"

namespace sticks3 {

// Read the whole record from NVS.  A missing key reads as empty and a missing
// port falls back to kDefaultPort, exactly as loadRotation() falls back to 1.
// The result is always run through usage::provision::sanitize() before it is
// returned, so a truncated or corrupt blob can never reach the radio.
//
// Returns usage::provision::complete(out): true means the device is
// provisioned, false means it must enter the portal.
bool credsLoad(usage::provision::Record& out);

// Write the whole record to NVS.  Sanitizes a COPY, so the caller's record is
// never mutated.  Returns false if any field failed to write; every field is
// attempted even after an earlier failure, so a partial save leaves an
// incomplete record (→ portal) rather than a silently wrong one.
bool credsSave(const usage::provision::Record& r);

// Remove the six credential keys — factory reset / re-provision.  Deliberately
// NOT Preferences::clear(): the "usaged" namespace is shared with rotation and
// brightness, which must survive a re-provision.
void credsClear();

// DEVELOPER CONVENIENCE, compiled in only when firmware/include/secrets.h
// exists.  If the stored record is not complete, fill one from the
// compile-time macros and save it, so a developer's own board keeps working
// straight off a flash with no portal round trip.  Returns true if it seeded.
//
// NVS always wins: a complete stored record is never overwritten by the
// header.  A seed that is not itself complete is not written either — it would
// only wear flash and still land in the portal.
//
// With no secrets.h present this compiles to a function that does nothing and
// returns false, with no edits anywhere: that is the production build.
bool credsSeedFromSecretsIfEmpty();

} // namespace sticks3
