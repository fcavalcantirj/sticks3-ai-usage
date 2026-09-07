// firmware/src/hal/sticks3/discover.h — find the usaged agent on the LAN with
// mDNS, so nobody ever types an address (task 79).
//
// WHY THIS EXISTS: the setup portal used to ask for the agent's address and
// port.  A stranger does not know either, and the answer changes every time the
// Mac takes a new DHCP lease — a typed address is a setup step that is both
// impossible for a newcomer and wrong within a week.  The agent announces
// itself instead, and the device listens.
//
// THE CONTRACT WITH THE AGENT (agreed; neither half may be changed alone):
//
//   * the agent advertises the service type "_ai-usage._tcp" on its REAL listen
//     port, so the PORT arrives in the advertisement and is never typed;
//   * the agent ALSO answers an A query for the host label "ai-usage"
//     (ai-usage.local), which is the fallback when the browse comes back empty.
//
// Those two names are the protocol.  They are the only thing either side is
// allowed to assume — in particular NEITHER SIDE MAY ASSUME AN ADDRESS, and
// nothing in this module may ever grow a literal IP or a default host.
//
// WHAT THIS MODULE NEVER DOES: call MDNS.end().  ESPmDNS::end() is mdns_free(),
// which tears down the ONE responder shared with ArduinoOTA — the OTA
// advertisement would vanish with it, and on a device with no cable attached
// OTA is the only way back in.  Discovery borrows the responder; it does not
// own it.
//
// AND THE MIRROR OF THAT, WHICH BITES: ArduinoOTA::end() calls MDNS.end()
// UNCONDITIONALLY (ArduinoOTA.cpp:365-372), and net.cpp's otaBegin() calls
// ArduinoOTA.end() as its first statement on EVERY transition to connected.  So
// an ordinary reconnect frees the responder underneath this module — and when
// no OTA password is stored, otaBegin() returns before ArduinoOTA.begin() and
// nothing brings it back.  Every attempt therefore re-asserts mdns_init()
// rather than trusting a "started once" flag.  A discovery that must survive a
// reconnect depends on that, not on luck.
#pragma once

#include <cstddef>
#include <cstdint>

#include "usage/provision.h"  // kMaxHost, kDefaultPort (pure, no Arduino headers)

namespace sticks3 {

// The advertised service, as agreed with the agent.  ESPmDNS prepends the
// underscores, so these are written without them: "ai-usage" + "tcp" queries
// _ai-usage._tcp.
inline constexpr char kAgentService[] = "ai-usage";
inline constexpr char kAgentProto[]   = "tcp";

// The host label the agent answers an A query for: ai-usage.local.  Used only
// when the service browse finds nothing; the port then falls back to
// usage::provision::kDefaultPort, which is the agent's compiled-in default and
// the one value both sides already share.
inline constexpr char kAgentHostLabel[] = "ai-usage";

// --- timing -----------------------------------------------------------------
//
// ESPmDNS's queries are SYNCHRONOUS and there is no async form: queryService()
// blocks on a hardcoded 3000 ms (ESPmDNS.cpp, mdns_query_ptr(..., 3000, 20, ...))
// and queryHost() blocks on the timeout it is given.  A single attempt is
// therefore split across TWO calls to discoverUpdate(), so no loop pass ever
// stalls for the sum of both — which matters on battery, where the whole awake
// window is ~19 s.
inline constexpr uint32_t kDiscoverHostQueryMs = 1500;

// Consecutive failed attempts before discoverState() reports Failed.  Bounded
// on purpose: a device whose agent is switched off must fall out of discovery
// and let the caller decide, never spin on the radio forever.  Calling
// discoverBegin() again starts a fresh run.
inline constexpr uint8_t kDiscoverMaxAttempts = 5;

// Backoff between attempts: doubles from the base and saturates at the cap.
inline constexpr uint32_t kDiscoverBaseBackoffMs = 2000;
inline constexpr uint32_t kDiscoverMaxBackoffMs  = 30000;

enum class DiscoverState : uint8_t {
    Idle,       // discoverBegin() has not run, or discoverReset() cleared it
    Browsing,   // the next discoverUpdate() spends ~3 s browsing _usaged._tcp
    Resolving,  // the next discoverUpdate() spends ~1.5 s on usaged.local
    Backoff,    // this attempt failed; waiting before the next one
    Found,      // an endpoint is available from discoverEndpoint()
    Failed,     // kDiscoverMaxAttempts exhausted; call discoverBegin() to retry
};

// --- one-shot form ----------------------------------------------------------

// Browse, then fall back to an A query, and write the agent's address and port
// into the caller's buffers.  Returns true only when BOTH are known.
//
// hostOut receives a dotted-quad IPv4 literal, not a name: fetch.cpp builds
// "http://<host>:<port>/..." and an address literal skips the resolver
// entirely, so a fetch cannot fail on a name lookup at the moment the agent is
// most needed.  It is discovered at runtime, never compiled in.
//
// BLOCKS for up to ~4.5 s (3 s browse + 1.5 s A query).  Use it from setup or
// from a deliberate "find the agent again" action; drive the state machine
// below from the loop instead.  Callers must size hostOut for at least
// usage::provision::kMaxHost + 1 bytes; a shorter buffer is truncated safely
// and the call reports failure rather than handing back half an address.
//
// It shares this module's state with the driven form, so it RESTARTS the run:
// afterwards discoverState() is Found or Failed, never something in flight.
bool discoverAgent(char* hostOut, size_t n, uint16_t& portOut);

// --- driven form ------------------------------------------------------------

// Start (or restart) a discovery run.  Clears any previous result and schedules
// the first attempt for the next discoverUpdate().  Safe to call at any time.
void discoverBegin(uint32_t nowMs);

// Advance the run.  Performs AT MOST ONE blocking mDNS query per call, and only
// when the backoff has elapsed, so an idle run costs nothing.  A no-op unless
// the state is Browsing, Resolving or Backoff.
void discoverUpdate(uint32_t nowMs);

// Current state.  Found and Failed are both terminal until discoverBegin().
DiscoverState discoverState();

// Convenience: discoverState() == DiscoverState::Found.
bool discoverFound();

// Copy the discovered endpoint out.  Returns false unless the state is Found,
// so a caller can never read a half-filled address.
bool discoverEndpoint(char* hostOut, size_t n, uint16_t& portOut);

// True when the port came from the service advertisement rather than from
// usage::provision::kDefaultPort.  Evidence for the serial log, not a decision
// input.
bool discoverPortFromService();

// Attempts consumed by the current run (0..kDiscoverMaxAttempts).
uint8_t discoverAttempts();

// Forget the result and go back to Idle.  Does NOT touch the mDNS responder.
void discoverReset();

} // namespace sticks3
