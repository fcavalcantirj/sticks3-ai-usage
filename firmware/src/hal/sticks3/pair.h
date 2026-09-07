// firmware/src/hal/sticks3/pair.h — get a device token from the agent, so
// nobody ever types one (task 78, device half).
//
// WHY THIS EXISTS: secrets.h is a compile-time header, so a baked-in device
// token is a plaintext string inside firmware.bin.  A publishable binary
// therefore carries no token and the device must be GIVEN one.  A 32-hex-
// character token is also not something a human types off a screen, so the
// direction is reversed: the DEVICE shows a 6-character code, the owner types
// THAT into the dashboard, and the agent hands back the token.
//
// THE SERVER IS THE AUTHORITY.  Every rule below is read out of
// internal/api/pairing.go — the code alphabet, the code length, the window TTL,
// the states, the status codes.  Nothing here re-implements a rule; this module
// polls one endpoint and reports what came back.
//
//   POST /v1/pair/claim   (unauthenticated BY DESIGN — an unpaired device has
//                          no credential to present; the authorisation is the
//                          pairing window the owner opened from the dashboard,
//                          plus a LAN-only peer check and a rate limit)
//
//     request  {"device_id":"AA:BB:CC:DD:EE:FF","name":"..."}
//              Content-Type: application/json  (auth.go enforces it)
//              The "code" field is deliberately NOT sent: the agent mints the
//              code with crypto/rand and we display what it returns, so the
//              screen and the server can never disagree — including across a
//              device reboot, which would otherwise invent a second code.
//
//     202      {"ok":true,"state":"claimed","code":"ABC123","expires_in_sec":N}
//     200      {"ok":true,"state":"paired","device_id":"...","token":"<32 hex>"}
//     403      no window open ({"state":"idle"}), or the peer is not on the LAN
//     409      another device holds the window
//     429      rate limited
//     400      malformed request
//
// THE TOKEN IS NEVER LOGGED, NEVER RENDERED AND NEVER RETURNED BY A GETTER.
// It goes straight into the record this module hands to credsSave(); the serial
// line carries its LENGTH, exactly like the provider keys.
//
// THIS MODULE DOES NOT DRAW.  pairCode() returns the string; main.cpp decides
// how it looks on a 1.14" screen.
#pragma once

#include <cstddef>
#include <cstdint>

#include "usage/provision.h"

namespace sticks3 {

// Poll cadence.  pairing.go allows 60 claims/minute and its own comment sizes
// that limit against "~20/min at a 3 s interval", so 3 s is the rate the server
// was built to expect.
inline constexpr uint32_t kPairPollMs = 3000;

// After a 429 the next poll waits longer, so a rate-limited device backs off
// instead of spending the whole window being refused.
inline constexpr uint32_t kPairRateLimitMs = 10000;

// Consecutive transport failures (agent unreachable, connection reset) before
// the run gives up.  Bounded so a wrong address or a stopped agent surfaces on
// screen instead of polling forever.
inline constexpr uint8_t kPairMaxTransportFails = 10;

// The pairing code is read off the screen and typed by a human.  pairing.go
// fixes it at 6 characters; the buffer is oversized so a server that lengthens
// it still displays, and a longer value is truncated rather than overflowing.
inline constexpr size_t kPairCodeMax = 15;

// Short human line for the screen (never a credential — see pairMessage()).
inline constexpr size_t kPairMessageMax = 63;

enum class PairState : uint8_t {
    Idle,     // pairBegin() has not run, or pairReset() cleared the run
    Waiting,  // no pairing window is open — the owner must open one
    Claimed,  // we hold the window; pairCode() is the code to put on screen
    Busy,     // another device holds the window; we keep polling
    Paired,   // the token was issued AND stored; pairRecord() is what was saved
    Failed,   // this run cannot succeed (see pairMessage()); pairBegin() retries
};

// Start (or restart) a pairing run against the agent named by `base`.
//
// `base` is the record as it stands: ssid, pass, otaPass and the agent host and
// port DISCOVERED over mDNS (discover.h).  This module never invents an address
// — it refuses to start when base.host is empty or base.port is zero, because
// there is nothing to talk to and a default would be exactly the hardcoded
// address this work removes.
//
// A private copy is kept, so the caller need not keep `base` alive.  On success
// the copy gains the issued token and is written with credsSave().
//
// Returns false (and leaves the state Idle) when there is no agent to poll.
bool pairBegin(const usage::provision::Record& base, uint32_t nowMs);

// Drive the run: at most one POST per kPairPollMs.  A no-op in Idle, Paired and
// Failed, so a finished run costs nothing.  BLOCKS for the duration of the HTTP
// round trip when a poll is due (bounded by the client's 5 s timeout).
void pairUpdate(uint32_t nowMs);

// Current state.  Paired and Failed are terminal until pairBegin().
PairState pairState();

// The code to display, "" unless the state is Claimed.  It is whatever the
// agent minted — the device never generates one — copied bounded and stripped
// of anything unprintable, so a corrupt response cannot paint garbage.
const char* pairCode();

// Seconds left in the window as the AGENT last reported them, 0 when unknown.
// Server-reported on purpose: the device's clock is not the authority on a
// deadline the server enforces.
int pairExpiresInSec();

// A short line for the screen: what the device is waiting for, or why the run
// failed.  Carries the agent's own error text where there is one.  NEVER
// contains a code or a token — pairing.go keeps both out of its error strings,
// and this module only ever copies the "error" field.
const char* pairMessage();

// The record that was stored, valid once the state is Paired: the caller's
// `base` plus the issued token.  Hand it to fetchConfigure()/netBegin() rather
// than re-reading NVS.
const usage::provision::Record& pairRecord();

// Consecutive transport failures in this run (0..kPairMaxTransportFails).
uint8_t pairTransportFails();

// Abandon the run and go back to Idle.  Clears the code and the message; the
// stored record, if one was written, stays written.
void pairReset();

} // namespace sticks3
