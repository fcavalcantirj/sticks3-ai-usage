// firmware/src/hal/sticks3/portal.cpp — SoftAP captive portal implementation.
// See portal.h for the design; usage/portal.h owns every rule that can be
// decided without a radio.
//
// Nothing here logs a credential.  The [SAVE] line prints LENGTHS, the AP
// passphrase reaches the screen and never the serial port, and the join failure
// reason is a phrase, not a value.
#include "hal/sticks3/portal.h"

#include "hal/sticks3/board.h"   // serialLine
#include "hal/sticks3/creds.h"   // credsSave
#include "usage/portal.h"
#include "usage/provision.h"

#include <M5Unified.h>
#include <WiFi.h>
#include <WebServer.h>
#include <DNSServer.h>

#include <cstdio>
#include <cstring>

namespace sticks3 {

namespace {

// --- tuning ------------------------------------------------------------------

constexpr uint32_t kScanPeriodMs   = 20000;  // between successful scans
constexpr uint32_t kScanRetryMs    = 3000;   // after WIFI_SCAN_FAILED
constexpr uint32_t kClientPollMs   = 1000;   // softAPgetStationNum() is not free
constexpr uint32_t kJoinTimeoutMs  = 15000;  // upper bound on one join attempt
constexpr uint32_t kSuccessDwellMs = 2500;   // read "CONNECTED" before restart
constexpr uint32_t kFailDwellMs    = 4000;   // read the reason before the form
constexpr uint8_t  kJoinEventsMax  = 2;      // disconnects before giving up

// --- state -------------------------------------------------------------------

enum class Phase : uint8_t {
    Serving,  // AP up, scanning, waiting for a submission
    Joining,  // a join is in flight; no scans, no submissions
    Failed,   // showing why the join failed, then back to Serving
    Success,  // showing the new IP, then the main loop restarts the device
};

// Heap, not file statics: the portal must cost NOTHING once provisioned, and
// on a provisioned boot portalBegin() is never called.
WebServer* g_web = nullptr;
DNSServer* g_dns = nullptr;

bool          g_active  = false;
Phase         g_phase   = Phase::Serving;
PortalOutcome g_outcome = PortalOutcome::Running;

usage::portal::ApIdentity g_ap;
IPAddress g_apIp;
char      g_apIpText[16] = {0};

// Page prefills.  Host and port are configuration, not credentials.
char     g_seedHost[usage::provision::kMaxHost + 1] = {0};
uint16_t g_seedPort = usage::provision::kDefaultPort;

// Last rejection or join failure.  g_notice is the sentence the page shows;
// g_screenNote is the short phrase the 240px screen shows.  Neither ever
// contains a submitted value — see usage/portal.h rule 1.
char g_notice[192]    = {0};
char g_screenNote[40] = {0};

// The scan list, copied out of the radio's results the moment a scan completes:
// starting the next scan frees the previous result array, so a request handler
// must never read WiFi.SSID() itself.
usage::portal::Network   g_nets[usage::portal::kMaxNetworks];
size_t                   g_netCount  = 0;
usage::portal::ScanState g_scanState = usage::portal::ScanState::Running;
bool                     g_scanRunning = false;
uint32_t                 g_nextScanMs  = 0;

// The accepted record.  Saved BEFORE the join is attempted, per the ledger: a
// power cut mid-setup then leaves credentials the boot path can retry, and if
// they are wrong the state machine falls back here after N failures.
usage::provision::Record g_rec;
bool     g_joinPending  = false;  // POST answered; start the radio next pass
uint32_t g_phaseAtMs    = 0;      // when the current dwell phase began

// Written from the Wi-Fi event task, read from the loop: scalars only.
volatile uint8_t g_joinReason = usage::portal::kJoinReasonUnknown;
volatile uint8_t g_joinEvents = 0;

wifi_event_id_t g_evtHandler = 0;

bool     g_screenDirty    = true;
uint8_t  g_clients        = 0;
uint32_t g_lastClientPoll = 0;

// --- small helpers -----------------------------------------------------------

void ipText(const IPAddress& ip, char* buf, size_t n) {
    std::snprintf(buf, n, "%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3]);
}

void textAt(int16_t x, int16_t y, uint8_t size, uint16_t color, const char* s) {
    M5.Display.setTextSize(size);
    M5.Display.setTextColor(color);
    M5.Display.setCursor(x, y);
    M5.Display.println(s);
}

void textCentered(int16_t y, uint8_t size, uint16_t color, const char* s) {
    M5.Display.setTextSize(size);
    int16_t x = (int16_t)((M5.Display.width() - M5.Display.textWidth(s)) / 2);
    if (x < 0) {
        x = 0;
    }
    textAt(x, y, size, color, s);
}

// --- screens -----------------------------------------------------------------
//
// The screen is the surface that survives every radio transition, so it carries
// the AP name and passphrase during setup and the outcome afterwards.  The
// passphrase is shown here and NOWHERE else — never on the serial line.

void drawPortalScreen() {
    M5.Display.fillScreen(TFT_BLACK);

    textAt(2, 0, 1, TFT_WHITE, "SETUP - join this wi-fi");

    textAt(2, 14, 1, TFT_DARKGREY, "network");
    textAt(2, 24, 2, TFT_WHITE, g_ap.ssid);

    textAt(2, 48, 1, TFT_DARKGREY, "password");
    textAt(2, 58, 2, TFT_WHITE, g_ap.pass);

    textAt(2, 84, 1, TFT_WHITE, "the setup page opens by itself");

    char line[48];
    std::snprintf(line, sizeof(line), "or open http://%s", g_apIpText);
    textAt(2, 94, 1, TFT_DARKGREY, line);

    // One honest status line: whether a phone is attached, and what the scan
    // last did.  A failed scan is never rendered as an empty one.
    char status[48];
    if (g_clients > 0) {
        std::snprintf(status, sizeof(status), "phone connected - %u networks",
                      (unsigned)g_netCount);
    } else if (g_scanState == usage::portal::ScanState::Running) {
        std::snprintf(status, sizeof(status), "scanning for networks...");
    } else if (g_scanState == usage::portal::ScanState::Failed) {
        std::snprintf(status, sizeof(status), "scan busy - retrying");
    } else {
        std::snprintf(status, sizeof(status), "%u networks found",
                      (unsigned)g_netCount);
    }
    textAt(2, 112, 1, TFT_YELLOW, status);
}

void drawJoiningScreen() {
    M5.Display.fillScreen(TFT_BLACK);
    textCentered(28, 2, TFT_WHITE, "JOINING");
    textCentered(58, 1, TFT_YELLOW, g_rec.ssid);
    textCentered(100, 1, TFT_DARKGREY, "the setup wi-fi will drop");
}

void drawSuccessScreen() {
    M5.Display.fillScreen(TFT_BLACK);
    textCentered(24, 2, TFT_GREEN, "CONNECTED");
    char ip[16];
    ipText(WiFi.localIP(), ip, sizeof(ip));
    textCentered(56, 1, TFT_WHITE, ip);
    textCentered(96, 1, TFT_DARKGREY, "setup done - restarting");
}

void drawFailedScreen() {
    // The failure case is the one the user actually hits, and by the time it
    // happens the browser may be the wrong place to look.  Say what went wrong
    // and how to get back in.
    M5.Display.fillScreen(TFT_BLACK);
    textCentered(20, 2, TFT_RED, "JOIN FAILED");
    textCentered(52, 1, TFT_WHITE, g_screenNote);
    textCentered(78, 1, TFT_DARKGREY, "setup wi-fi is back:");
    textCentered(92, 1, TFT_YELLOW, g_ap.ssid);
}

void drawScreen() {
    switch (g_phase) {
        case Phase::Joining: drawJoiningScreen(); break;
        case Phase::Success: drawSuccessScreen(); break;
        case Phase::Failed:  drawFailedScreen();  break;
        case Phase::Serving:
        default:             drawPortalScreen();  break;
    }
}

// --- request handlers ---------------------------------------------------------

void sinkToString(void* ctx, const char* chunk) {
    static_cast<String*>(ctx)->concat(chunk);
}

void servePage() {
    usage::portal::PageInput in;
    in.networks = g_nets;
    in.count    = g_netCount;
    in.scan     = g_scanState;
    in.apSsid   = g_ap.ssid;
    in.host     = g_seedHost;
    in.port     = g_seedPort;
    in.notice   = g_notice[0] != '\0' ? g_notice : nullptr;
    // Only an in-flight join replaces the form.  After a FAILURE the phone is
    // still attached (the join never associated, so the AP never hopped
    // channel), and the most useful thing to hand back is the form plus the
    // reason.
    in.joining  = (g_phase == Phase::Joining);

    String page;
    page.reserve(6144);
    usage::portal::renderPage(in, sinkToString, &page);
    g_web->send(200, "text/html", page);
}

// Log the requested path.  THE FORM MUST STAY A POST for this to be safe: a
// GET form would put the Wi-Fi password and the device token into the query
// string, and this line would then write both to the serial log.
void logPath(const char* path) {
    char buf[64];
    std::snprintf(buf, sizeof(buf), "[HTTP] path=%s", path);
    serialLine(buf);
}

void handleRoot() {
    logPath(g_web->uri().c_str());
    servePage();
}

void handleProbeRedirect() {
    logPath(g_web->uri().c_str());
    // NOT send(204): the header builder still emits Content-Type and
    // Content-Length: 0, which is exactly the "no portal here" answer Android
    // is testing for, and the sheet never pops.  A 302 to the AP is the answer
    // that means "you are behind a portal".
    String url = "http://";
    url += g_apIpText;
    url += "/";
    g_web->sendHeader("Location", url, true);
    g_web->send(302, "text/plain", "");
}

void handleFavicon() {
    // Every portal page load asks for this.  Unhandled it logs a core error and
    // costs an extra TCP round through a server that takes ONE client at a
    // time — measured during the spike.
    g_web->send(404, "text/plain", "");
}

void handleNotFound() {
    logPath(g_web->uri().c_str());
    handleProbeRedirect();
}

void handleSave() {
    logPath("/save");

    if (g_phase == Phase::Joining) {
        servePage();  // a join is already in flight; do not start a second one
        return;
    }

    // Copy every argument out before building the Submission: arg() returns a
    // String by value, and a temporary would be gone before validate() ran.
    String ssid   = g_web->arg("ssid");
    String manual = g_web->arg("ssid_manual");
    String pass   = g_web->arg("pass");
    String host   = g_web->arg("host");
    String port   = g_web->arg("port");
    String token  = g_web->arg("token");
    String ota    = g_web->arg("ota");

    usage::portal::Submission sub;
    sub.ssidPick   = ssid.c_str();
    sub.ssidManual = manual.c_str();
    sub.pass       = pass.c_str();
    sub.host       = host.c_str();
    sub.port       = port.c_str();
    sub.token      = token.c_str();
    sub.otaPass    = ota.c_str();

    usage::provision::Record rec;
    usage::portal::Reject r = usage::portal::validate(sub, rec);
    if (r != usage::portal::Reject::None) {
        std::snprintf(g_notice, sizeof(g_notice), "%s",
                      usage::portal::rejectText(r));
        char buf[48];
        std::snprintf(buf, sizeof(buf), "[SAVE] rejected reason=%u",
                      (unsigned)r);
        serialLine(buf);
        servePage();
        return;
    }

    if (!credsSave(rec)) {
        std::snprintf(g_notice, sizeof(g_notice),
                      "The device could not store those settings. Try again.");
        serialLine("[SAVE] store failed");
        servePage();
        return;
    }

    // Lengths only, never values.
    char buf[112];
    std::snprintf(buf, sizeof(buf),
                  "[SAVE] ssid_len=%d pass_len=%d host_len=%d token_len=%d "
                  "ota_len=%d port=%u",
                  (int)std::strlen(rec.ssid), (int)std::strlen(rec.pass),
                  (int)std::strlen(rec.host), (int)std::strlen(rec.token),
                  (int)std::strlen(rec.otaPass), (unsigned)rec.port);
    serialLine(buf);

    g_rec = rec;
    g_notice[0] = '\0';
    g_phase = Phase::Joining;   // set BEFORE the response, so the page is honest
    g_screenDirty = true;
    servePage();

    // The radio work happens AFTER handleClient() returns.  Starting a join
    // inside the handler would race the response still on the wire — and the
    // join is precisely the thing that takes the AP away from this client.
    g_joinPending = true;
}

void registerRoutes() {
    // Registration order IS routing order: WebServer picks the FIRST matching
    // handler, chosen from the request line BEFORE a single header is read, so
    // the OS probe paths go first and routing on Host is impossible.
    g_web->on("/hotspot-detect.html", HTTP_GET, handleRoot);       // iOS/macOS
    g_web->on("/generate_204", HTTP_GET, handleProbeRedirect);      // Android
    g_web->on("/gen_204", HTTP_GET, handleProbeRedirect);           // Android
    g_web->on("/favicon.ico", HTTP_GET, handleFavicon);
    g_web->on("/", HTTP_GET, handleRoot);
    g_web->on("/save", HTTP_POST, handleSave);
    g_web->onNotFound(handleNotFound);
    // NEVER onFileUpload() here: it sets _ufn, and the handler then claims ANY
    // non-GET method regardless of URI, routing the whole POST body down the
    // raw path so arg("ssid") comes back empty for every submission.
}

// --- radio events -------------------------------------------------------------

void onWifiEvent(arduino_event_id_t event, arduino_event_info_t info) {
    // Runs on the Wi-Fi event task.  Scalars only — no serial, no drawing.
    if (event == ARDUINO_EVENT_WIFI_STA_DISCONNECTED) {
        g_joinReason = (uint8_t)info.wifi_sta_disconnected.reason;
        if (g_joinEvents < 0xFF) {
            g_joinEvents = (uint8_t)(g_joinEvents + 1);
        }
    }
}

// --- the scan cycle -----------------------------------------------------------

void updateScan(uint32_t nowMs) {
    if (g_scanRunning) {
        int16_t n = WiFi.scanComplete();
        if (n == WIFI_SCAN_RUNNING) {
            return;
        }
        g_scanRunning = false;

        if (n < 0) {
            // WIFI_SCAN_FAILED (-2) means "the radio refused, retry" and is NOT
            // an empty result.  Both happen (5.6% and 1.1% in the spike), and
            // rendering -2 as "no networks found" would send the user hunting a
            // 5 GHz problem that is not there.
            g_scanState = usage::portal::ScanState::Failed;
            g_nextScanMs = nowMs + kScanRetryMs;
            serialLine("[SCAN] failed");
        } else {
            g_netCount = 0;
            for (int16_t i = 0; i < n; ++i) {
                String s = WiFi.SSID((uint8_t)i);
                usage::portal::insertNetwork(
                    g_nets, usage::portal::kMaxNetworks, g_netCount,
                    s.c_str(), (int)WiFi.RSSI((uint8_t)i),
                    WiFi.encryptionType((uint8_t)i) != WIFI_AUTH_OPEN);
            }
            g_scanState = usage::portal::ScanState::Ok;
            g_nextScanMs = nowMs + kScanPeriodMs;
            char buf[48];
            std::snprintf(buf, sizeof(buf), "[SCAN] n=%d shown=%d",
                          (int)n, (int)g_netCount);
            serialLine(buf);
        }
        // Free the driver's result array now that the list is copied.
        WiFi.scanDelete();
        g_screenDirty = true;
        return;
    }

    if ((int32_t)(nowMs - g_nextScanMs) < 0) {
        return;
    }

    // ASYNC, always.  The synchronous form blocks on a HARDCODED 10 s
    // (WiFiScan.cpp) regardless of max_ms_per_chan — most of a battery wake
    // window, during which DNS goes unanswered and the phone concludes there is
    // no portal.
    int16_t r = WiFi.scanNetworks(true, false, false, 300);
    if (r == WIFI_SCAN_FAILED) {
        g_scanState = usage::portal::ScanState::Failed;
        g_nextScanMs = nowMs + kScanRetryMs;
        g_screenDirty = true;
        serialLine("[SCAN] failed");
        return;
    }
    g_scanRunning = true;
}

// --- the join -----------------------------------------------------------------

void startJoin(uint32_t nowMs) {
    g_phaseAtMs  = nowMs;
    g_joinReason = usage::portal::kJoinReasonUnknown;
    g_joinEvents = 0;

    // No scanning while a join is in flight: scanning and connecting at once
    // aborts the scan with ESP_ERR_WIFI_STATE, which Arduino reports only as
    // WIFI_SCAN_FAILED — a failure that looks like a radio fault and is really
    // this race.
    if (g_scanRunning) {
        WiFi.scanDelete();
        g_scanRunning = false;
    }

    // Not sufficient on its own — a function-static first_connect in the core
    // forces one reconnect for ANY disconnect reason — which is exactly why the
    // failure path erases the stored config rather than trusting this call.
    WiFi.setAutoReconnect(false);
    WiFi.begin(g_rec.ssid, g_rec.pass);

    char buf[64];
    std::snprintf(buf, sizeof(buf), "[JOIN] state=trying ssid_len=%d",
                  (int)std::strlen(g_rec.ssid));
    serialLine(buf);
}

void failJoin(uint32_t nowMs) {
    uint8_t reason = g_joinReason;

    // Stop the radio re-applying credentials that plainly do not work: eraseap
    // clears the stored STA config, so the core's forced reconnect has nothing
    // to retry with and cannot collide with the next portal scan.
    WiFi.disconnect(false, true);

    const char* why = usage::portal::joinFailText(reason);
    std::snprintf(g_notice, sizeof(g_notice),
                  "Could not join that network: %s. Check the password and the "
                  "network name, then try again.", why);
    std::snprintf(g_screenNote, sizeof(g_screenNote), "%s", why);

    g_phase = Phase::Failed;
    g_phaseAtMs = nowMs;
    g_screenDirty = true;

    char buf[64];
    std::snprintf(buf, sizeof(buf), "[JOIN] state=fail reason=%u",
                  (unsigned)reason);
    serialLine(buf);
}

void updateJoin(uint32_t nowMs) {
    if (WiFi.status() == WL_CONNECTED) {
        g_phase = Phase::Success;
        g_phaseAtMs = nowMs;
        g_screenDirty = true;

        char ip[16];
        ipText(WiFi.localIP(), ip, sizeof(ip));
        char buf[64];
        std::snprintf(buf, sizeof(buf), "[JOIN] state=ok ip=%s", ip);
        serialLine(buf);
        return;
    }

    bool exhausted = g_joinEvents >= kJoinEventsMax;
    bool timedOut  = (int32_t)(nowMs - g_phaseAtMs) >= (int32_t)kJoinTimeoutMs;
    if (exhausted || timedOut) {
        failJoin(nowMs);
    }
}

void updateClients(uint32_t nowMs) {
    if (g_lastClientPoll != 0 &&
        (int32_t)(nowMs - g_lastClientPoll) < (int32_t)kClientPollMs) {
        return;
    }
    g_lastClientPoll = nowMs;

    uint8_t n = WiFi.softAPgetStationNum();
    if (n == g_clients) {
        return;
    }
    char buf[48];
    std::snprintf(buf, sizeof(buf), "[AP] client %s n=%u",
                  n > g_clients ? "joined" : "left", (unsigned)n);
    serialLine(buf);
    g_clients = n;
    g_screenDirty = true;
}

// Free both servers and drop the radio's portal role.  Split out so the
// half-built failure paths in portalBegin() cannot leak.
void teardown() {
    if (g_dns != nullptr) {
        g_dns->stop();
        delete g_dns;
        g_dns = nullptr;
    }
    if (g_web != nullptr) {
        g_web->close();
        delete g_web;
        g_web = nullptr;
    }
    if (g_evtHandler != 0) {
        WiFi.removeEvent(g_evtHandler);
        g_evtHandler = 0;
    }
    if (g_scanRunning) {
        WiFi.scanDelete();
        g_scanRunning = false;
    }
    WiFi.softAPdisconnect(true);
}

} // namespace

// --- public API ----------------------------------------------------------------

bool portalBegin(const usage::provision::Record& seed) {
    if (g_active) {
        return true;
    }

    std::snprintf(g_seedHost, sizeof(g_seedHost), "%s", seed.host);
    g_seedPort = seed.port != 0 ? seed.port : usage::provision::kDefaultPort;

    // persistent(false) is read exactly ONCE, behind a one-shot guard that
    // softAP(), begin() and scanNetworks() all reach.  Called after the first
    // radio call it silently does nothing and the core writes credentials into
    // NVS under its own keys — which is the very thing task 76 removed.
    WiFi.persistent(false);
    // setHostname() only fills a file-static buffer; the ONLY place that buffer
    // reaches the netif is inside mode(), which early-returns when the mode is
    // already the requested one.  After mode() it is a silent no-op.
    WiFi.setHostname("sticks3-usage");

    // macAddress(), NOT softAPmacAddress(uint8_t*): the latter returns the
    // caller's buffer UNMODIFIED when the mode is still WIFI_MODE_NULL — no
    // error, no zeroing — so an AP name derived from it before the radio is up
    // is uninitialised stack.  The two look symmetric and are not.
    uint8_t mac[6] = {0};
    WiFi.macAddress(mac);
    usage::portal::apIdentity(mac, g_ap);

    if (!WiFi.mode(WIFI_AP_STA)) {
        serialLine("[ERR] portal: mode(AP_STA) failed");
        return false;
    }
    // WPA2, not open: an open AP would let a neighbour reach the setup page.
    // The passphrase is >= 8 characters by construction (static_assert in
    // usage/portal.cpp), because softAP() returns false below that.
    if (!WiFi.softAP(g_ap.ssid, g_ap.pass)) {
        serialLine("[ERR] portal: softAP failed");
        WiFi.mode(WIFI_STA);
        return false;
    }

    // READ THE AP ADDRESS BACK.  The 192.168.4.1 default lives in precompiled
    // esp_netif code, not in any header this build can see, so hardcoding it is
    // a guess that would silently break the DNS hijack and every redirect.
    g_apIp = WiFi.softAPIP();
    ipText(g_apIp, g_apIpText, sizeof(g_apIpText));

    g_web = new WebServer(80);
    g_dns = new DNSServer();
    if (g_web == nullptr || g_dns == nullptr) {
        serialLine("[ERR] portal: out of memory");
        teardown();
        return false;
    }

    registerRoutes();
    g_web->begin();

    g_dns->setErrorReplyCode(DNSReplyCode::NoError);
    if (!g_dns->start(53, "*", g_apIp)) {
        // The page is still reachable by address, and the on-screen instruction
        // line covers it, but the sheet will not pop by itself.
        serialLine("[ERR] portal: dns start failed");
    }

    g_evtHandler = WiFi.onEvent(onWifiEvent);

    g_phase       = Phase::Serving;
    g_outcome     = PortalOutcome::Running;
    g_netCount    = 0;
    g_scanState   = usage::portal::ScanState::Running;
    g_scanRunning = false;
    g_nextScanMs  = nowMs();   // scan immediately: the sheet pops in 3-6 s
    g_joinPending = false;
    g_joinReason  = usage::portal::kJoinReasonUnknown;
    g_joinEvents  = 0;
    g_notice[0]      = '\0';
    g_screenNote[0]  = '\0';
    g_clients        = 0;
    g_lastClientPoll = 0;
    g_screenDirty    = true;
    g_active         = true;

    char buf[80];
    // The AP passphrase is deliberately absent: it belongs on the screen, not
    // in a log.
    std::snprintf(buf, sizeof(buf), "[PORTAL] ap=%s ip=%s", g_ap.ssid, g_apIpText);
    serialLine(buf);
    return true;
}

bool portalActive() {
    return g_active;
}

void portalUpdate(uint32_t nowMs) {
    if (!g_active) {
        return;
    }

    // Both must run every pass.  A client that connects and sends nothing pins
    // the single server slot for up to 5 s, during which DNS goes unanswered —
    // precisely while the phone is deciding whether a portal exists.
    g_dns->processNextRequest();
    g_web->handleClient();

    if (g_joinPending) {
        g_joinPending = false;
        startJoin(nowMs);
    }

    switch (g_phase) {
        case Phase::Serving:
            updateScan(nowMs);
            break;
        case Phase::Joining:
            updateJoin(nowMs);
            break;
        case Phase::Failed:
            if ((int32_t)(nowMs - g_phaseAtMs) >= (int32_t)kFailDwellMs) {
                // Back to the form.  The notice stays: the user is about to
                // re-read it on the phone.
                g_phase = Phase::Serving;
                g_nextScanMs = nowMs;
                g_screenDirty = true;
            }
            break;
        case Phase::Success:
            if ((int32_t)(nowMs - g_phaseAtMs) >= (int32_t)kSuccessDwellMs) {
                g_outcome = PortalOutcome::Joined;
            }
            break;
    }

    updateClients(nowMs);

    if (g_screenDirty) {
        drawScreen();
        g_screenDirty = false;
    }
}

PortalOutcome portalOutcome() {
    return g_outcome;
}

void portalEnd() {
    if (!g_active) {
        return;
    }
    teardown();
    g_active = false;
    serialLine("[PORTAL] stopped");
}

} // namespace sticks3
