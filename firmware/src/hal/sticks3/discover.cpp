// firmware/src/hal/sticks3/discover.cpp — mDNS discovery of the ai-usage agent.
// See discover.h for the contract and for why MDNS.end() is never called here.
#include "hal/sticks3/discover.h"

#include "hal/sticks3/board.h"  // serialLine

#include <ESPmDNS.h>
#include <WiFi.h>
#include <cstdio>
#include <cstring>

namespace sticks3 {

namespace {

// --- run state --------------------------------------------------------------

DiscoverState g_state = DiscoverState::Idle;

char     g_host[usage::provision::kMaxHost + 1] = {0};
uint16_t g_port                                  = 0;
bool     g_portFromService                       = false;

uint8_t  g_attempts   = 0;  // attempts consumed by this run
uint32_t g_nextTryMs  = 0;  // earliest millis() for the next blocking query
bool     g_loggedUp   = false;  // serial noise guard only — NEVER a readiness cache

// Backoff for attempt n (0-based): base << n, saturated at the cap.
uint32_t backoffMs(uint8_t attempt) {
    uint32_t ms = kDiscoverBaseBackoffMs;
    for (uint8_t i = 0; i < attempt && ms < kDiscoverMaxBackoffMs; i++) {
        ms *= 2;
    }
    return ms > kDiscoverMaxBackoffMs ? kDiscoverMaxBackoffMs : ms;
}

// Bring the shared mDNS responder up, and RE-ASSERT IT ON EVERY ATTEMPT.
//
// A readiness flag would be a lie here, and this is the trap that would make
// discovery fail silently forever.  ArduinoOTA::end() calls MDNS.end() —
// mdns_free() — UNCONDITIONALLY whenever mDNS is enabled, even when OTA was
// never initialised (ArduinoOTA.cpp:365-372), and net.cpp's otaBegin() calls
// ArduinoOTA.end() as its FIRST statement on every transition to connected.
// So an ordinary Wi-Fi reconnect destroys the responder this module borrows,
// and when NO OTA password is stored otaBegin() returns before
// ArduinoOTA.begin(), so nothing ever brings it back.  A cached "ready" would
// then send every query into a freed responder.
//
// Re-initialising costs a few microseconds when the responder is already up,
// and is the difference between working and never finding the agent when it is
// not.  mdns_init() reports ESP_ERR_INVALID_STATE for an already-running
// responder, which is a success for our purposes: we need the querier, and
// asking for it twice must not read as a failure.
//
// The hostname is the device's OWN DHCP hostname, read back from the radio
// rather than restated here, so this module cannot drift from net.cpp's
// WiFi.setHostname().  It is a courtesy, not a requirement — queries need
// mdns_init(), not a hostname — so a board with none still discovers.
bool ensureMdns() {
    const char* host = WiFi.getHostname();
    if (host != nullptr && host[0] != '\0') {
        // MDNS.begin() is mdns_init() + mdns_hostname_set().
        if (MDNS.begin(host)) {
            if (!g_loggedUp) {
                g_loggedUp = true;
                char buf[80];
                std::snprintf(buf, sizeof(buf), "[MDNS] responder up host=%s", host);
                serialLine(buf);
            }
            return true;
        }
        // begin() returning false is ambiguous: it is also what an
        // already-initialised responder produces when mdns_init() answers
        // ESP_ERR_INVALID_STATE.  Fall through to the direct init, which
        // reports that case unambiguously.
    }

    esp_err_t err = mdns_init();
    if (err == ESP_OK || err == ESP_ERR_INVALID_STATE) {
        if (!g_loggedUp) {
            g_loggedUp = true;
            serialLine(err == ESP_OK ? "[MDNS] responder up (query only)"
                                     : "[MDNS] responder already running");
        }
        return true;
    }

    g_loggedUp = false;
    char buf[64];
    std::snprintf(buf, sizeof(buf), "[MDNS] init failed err=%d", (int)err);
    serialLine(buf);
    return false;
}

// Write a dotted-quad into dst.  Returns false when the address is unusable or
// the buffer is too small, so a truncated address can never reach the fetcher.
bool writeIp(char* dst, size_t n, const IPAddress& ip) {
    if (dst == nullptr || n == 0) {
        return false;
    }
    dst[0] = '\0';
    // 0.0.0.0 is what ESPmDNS returns for "not found" and for a result with no
    // IPv4 record; it is never a real agent.
    if (static_cast<uint32_t>(ip) == 0) {
        return false;
    }
    int written = std::snprintf(dst, n, "%u.%u.%u.%u",
                                (unsigned)ip[0], (unsigned)ip[1],
                                (unsigned)ip[2], (unsigned)ip[3]);
    if (written < 0 || (size_t)written >= n) {
        dst[0] = '\0';
        return false;
    }
    return true;
}

// Record a successful find.
void takeEndpoint(const char* host, uint16_t port, bool fromService) {
    std::snprintf(g_host, sizeof(g_host), "%s", host);
    g_port = port;
    g_portFromService = fromService;
    g_state = DiscoverState::Found;

    char buf[96];
    std::snprintf(buf, sizeof(buf), "[MDNS] agent=%s:%u src=%s attempt=%u",
                  g_host, (unsigned)g_port,
                  fromService ? "_ai-usage._tcp" : "ai-usage.local",
                  (unsigned)(g_attempts + 1));
    serialLine(buf);
}

// PHASE 1 — browse _ai-usage._tcp.  Blocks ~3 s (ESPmDNS hardcodes that timeout).
// Returns true when an endpoint was taken.
bool browseService() {
    int n = MDNS.queryService(kAgentService, kAgentProto);
    if (n <= 0) {
        char buf[64];
        std::snprintf(buf, sizeof(buf), "[MDNS] browse _%s._%s n=%d",
                      kAgentService, kAgentProto, n);
        serialLine(buf);
        return false;
    }

    // First advertisement that carries BOTH an IPv4 address and a real port
    // wins.  A record missing either is skipped rather than repaired: guessing
    // the missing half is exactly the hardcoding this module exists to remove.
    for (int i = 0; i < n; i++) {
        uint16_t port = MDNS.port(i);
        if (port == 0) {
            continue;
        }
        char host[usage::provision::kMaxHost + 1];
        if (!writeIp(host, sizeof(host), MDNS.IP(i))) {
            continue;
        }
        takeEndpoint(host, port, true);
        return true;
    }

    char buf[64];
    std::snprintf(buf, sizeof(buf), "[MDNS] browse n=%d no usable record", n);
    serialLine(buf);
    return false;
}

// PHASE 2 — resolve ai-usage.local.  Blocks up to kDiscoverHostQueryMs.
// Returns true when an endpoint was taken.
bool resolveHost() {
    IPAddress ip = MDNS.queryHost(kAgentHostLabel, kDiscoverHostQueryMs);
    char host[usage::provision::kMaxHost + 1];
    if (!writeIp(host, sizeof(host), ip)) {
        char buf[64];
        std::snprintf(buf, sizeof(buf), "[MDNS] %s.local not found",
                      kAgentHostLabel);
        serialLine(buf);
        return false;
    }
    // No advertisement, so no advertised port: fall back to the one default
    // both sides already share.
    takeEndpoint(host, usage::provision::kDefaultPort, false);
    return true;
}

// An attempt (browse + A query) came back empty.  Schedule the next one, or
// give up so the caller is never blocked by a run that cannot succeed.
void attemptFailed(uint32_t nowMs) {
    g_attempts++;
    if (g_attempts >= kDiscoverMaxAttempts) {
        g_state = DiscoverState::Failed;
        char buf[64];
        std::snprintf(buf, sizeof(buf), "[MDNS] giving up after %u attempts",
                      (unsigned)g_attempts);
        serialLine(buf);
        return;
    }
    uint32_t wait = backoffMs(g_attempts);
    g_nextTryMs = nowMs + wait;
    g_state = DiscoverState::Backoff;

    char buf[64];
    std::snprintf(buf, sizeof(buf), "[MDNS] retry %u in %ums",
                  (unsigned)g_attempts, (unsigned)wait);
    serialLine(buf);
}

} // namespace

// --- one-shot ---------------------------------------------------------------

bool discoverAgent(char* hostOut, size_t n, uint16_t& portOut) {
    if (hostOut == nullptr || n == 0) {
        return false;
    }
    hostOut[0] = '\0';
    portOut = 0;

    // One fresh attempt, whatever a previous run left behind: the one-shot and
    // the driven form share this module's state, so entering here restarts it
    // rather than half-joining a run already in flight.
    g_host[0] = '\0';
    g_port = 0;
    g_portFromService = false;
    g_attempts = 0;
    g_state = DiscoverState::Browsing;

    if (WiFi.status() != WL_CONNECTED) {
        serialLine("[MDNS] skipped: station is not connected");
        g_state = DiscoverState::Failed;
        return false;
    }
    if (!ensureMdns()) {
        g_state = DiscoverState::Failed;
        return false;
    }

    if (!browseService() && !resolveHost()) {
        g_attempts = 1;
        g_state = DiscoverState::Failed;
        return false;
    }

    if (std::strlen(g_host) >= n) {
        // Refuse to hand back a truncated address.
        serialLine("[ERR] discover: host buffer too small");
        return false;
    }
    std::snprintf(hostOut, n, "%s", g_host);
    portOut = g_port;
    return true;
}

// --- driven form ------------------------------------------------------------

void discoverBegin(uint32_t nowMs) {
    g_host[0] = '\0';
    g_port = 0;
    g_portFromService = false;
    g_attempts = 0;
    g_nextTryMs = nowMs;
    g_state = DiscoverState::Browsing;
    serialLine("[MDNS] searching for the agent");
}

void discoverUpdate(uint32_t nowMs) {
    switch (g_state) {
    case DiscoverState::Idle:
    case DiscoverState::Found:
    case DiscoverState::Failed:
        return;
    case DiscoverState::Backoff:
        if ((int32_t)(nowMs - g_nextTryMs) < 0) {
            return;
        }
        g_state = DiscoverState::Browsing;
        return;  // spend the blocking query on the NEXT pass, never on the one
                 // that only noticed the timer expired
    case DiscoverState::Browsing:
    case DiscoverState::Resolving:
        break;
    }

    // A station that dropped cannot answer; wait rather than burn an attempt.
    if (WiFi.status() != WL_CONNECTED) {
        return;
    }
    if (!ensureMdns()) {
        attemptFailed(nowMs);
        return;
    }

    if (g_state == DiscoverState::Browsing) {
        if (!browseService()) {
            g_state = DiscoverState::Resolving;
        }
        return;
    }

    // Resolving: the A-record fallback closes the attempt either way.
    if (!resolveHost()) {
        attemptFailed(nowMs);
    }
}

DiscoverState discoverState() {
    return g_state;
}

bool discoverFound() {
    return g_state == DiscoverState::Found;
}

bool discoverEndpoint(char* hostOut, size_t n, uint16_t& portOut) {
    if (hostOut == nullptr || n == 0 || g_state != DiscoverState::Found) {
        return false;
    }
    if (std::strlen(g_host) >= n) {
        return false;
    }
    std::snprintf(hostOut, n, "%s", g_host);
    portOut = g_port;
    return true;
}

bool discoverPortFromService() {
    return g_portFromService;
}

uint8_t discoverAttempts() {
    return g_attempts;
}

void discoverReset() {
    g_host[0] = '\0';
    g_port = 0;
    g_portFromService = false;
    g_attempts = 0;
    g_nextTryMs = 0;
    g_state = DiscoverState::Idle;
}

} // namespace sticks3
