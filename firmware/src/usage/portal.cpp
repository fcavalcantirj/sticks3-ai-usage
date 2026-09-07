// firmware/src/usage/portal.cpp — the pure half of the SoftAP captive portal:
// AP identity, scan list, page rendering and form rules.  No M5/WiFi/Arduino
// headers; the HAL owns the radio.  See portal.h for the design.
//
// Nothing here formats, copies or renders a credential VALUE.  The page input
// carries no passphrase, no token and no OTA password by construction, and
// every rejection message is a fixed sentence.
#include "usage/portal.h"

#include <cstdio>
#include <cstring>

namespace usage {
namespace portal {

namespace {

// --- small shared helpers ----------------------------------------------------

void emit(Sink sink, void* ctx, const char* s) {
    if (s != nullptr) {
        sink(ctx, s);
    }
}

// The replacement for one byte, or null when it can be emitted as itself.
// Both quote forms are covered because escaped values land inside attributes
// as well as inside elements.
const char* escapeOne(char c) {
    switch (c) {
        case '&':  return "&amp;";
        case '<':  return "&lt;";
        case '>':  return "&gt;";
        case '"':  return "&quot;";
        case '\'': return "&#39;";
        default:   return nullptr;
    }
}

// Emit a caller-supplied string with every HTML metacharacter escaped.  Every
// value that reaches the page goes through here: a scanned SSID is text an
// unknown access point chose.
//
// The source is consumed in SLICES rather than escaped into one worst-case
// buffer, so an input longer than any single field — a notice, say — can never
// be silently truncated, and the stack cost stays a few hundred bytes on a
// server whose handlers already run on the loop task.
void emitEscaped(Sink sink, void* ctx, const char* s) {
    if (s == nullptr) {
        return;
    }
    constexpr size_t kSlice = 48;  // source bytes per pass
    char buf[kSlice * 6 + 1];

    size_t i = 0;
    while (s[i] != '\0') {
        size_t n = 0;
        size_t w = 0;
        while (n < kSlice && s[i + n] != '\0') {
            const char* rep = escapeOne(s[i + n]);
            if (rep != nullptr) {
                size_t len = std::strlen(rep);
                std::memcpy(buf + w, rep, len);
                w += len;
            } else {
                buf[w++] = s[i + n];
            }
            ++n;
        }
        buf[w] = '\0';
        sink(ctx, buf);
        i += n;
    }
}

// Locate src minus leading and trailing spaces/tabs.  Phones append a space
// when they autocomplete a field, and a trailing space in the agent host or
// the device token fails in a way nobody can see: the join works, every fetch
// 401s, and the screen just says the data is stale.  Never applied to a
// passphrase, where a space is a legitimate character.
const char* trimSpan(const char* s, size_t& len) {
    if (s == nullptr) {
        len = 0;
        return "";
    }
    size_t b = 0;
    while (s[b] == ' ' || s[b] == '\t') {
        ++b;
    }
    size_t e = std::strlen(s);
    while (e > b && (s[e - 1] == ' ' || s[e - 1] == '\t')) {
        --e;
    }
    len = e - b;
    return s + b;
}

// Copy len bytes into a cap-capacity field (cap EXCLUDES the terminator) and
// always terminate.  Callers check the length first, so a truncation here is a
// bug elsewhere, not a silent repair.
void copyN(char* dst, size_t cap, const char* src, size_t len) {
    size_t n = len < cap ? len : cap;
    if (n > 0) {
        std::memcpy(dst, src, n);
    }
    dst[n] = '\0';
}

// Parse the port field.  Empty falls back to kDefaultPort, exactly as a
// missing NVS port does.  Port 0 is refused rather than defaulted: it can never
// be dialled, and silently rewriting a number the user typed would hide a typo.
bool parsePort(const char* s, uint16_t& out) {
    out = provision::kDefaultPort;
    size_t len = 0;
    const char* p = trimSpan(s, len);
    if (len == 0) {
        return true;
    }
    uint32_t v = 0;
    for (size_t i = 0; i < len; ++i) {
        if (p[i] < '0' || p[i] > '9') {
            return false;
        }
        v = v * 10 + (uint32_t)(p[i] - '0');
        if (v > 65535) {
            return false;
        }
    }
    if (v == 0) {
        return false;
    }
    out = (uint16_t)v;
    return true;
}

} // namespace

// --- the access-point identity ----------------------------------------------

// 32 symbols with no 0/O and no 1/I, so five bits map to one character that
// cannot be misread off the device screen.  Task 78's pairing code uses the
// same rule for the same reason.
constexpr char kApAlphabet[] = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ";
static_assert(sizeof(kApAlphabet) - 1 == 32, "AP alphabet must be 32 symbols");
static_assert(kApPassLen >= provision::kMinWpaPass,
              "softAP() refuses a passphrase under 8 characters");

void apIdentity(const uint8_t mac[6], ApIdentity& out) {
    // FNV-1a (64-bit) over the six MAC bytes.  Not a security primitive and
    // not claimed to be one — see the limitation recorded in portal.h.  It is
    // here only to spread the two variable MAC bytes across ten symbols so two
    // devices from the same batch do not share a passphrase.
    uint64_t h = 14695981039346656037ULL;
    for (size_t i = 0; i < 6; ++i) {
        h ^= (uint64_t)mac[i];
        h *= 1099511628211ULL;
    }

    for (size_t i = 0; i < kApPassLen; ++i) {
        out.pass[i] = kApAlphabet[(h >> (5 * i)) & 0x1F];
    }
    out.pass[kApPassLen] = '\0';

    // The SSID is picked from a phone's list rather than typed, so plain hex of
    // the last two MAC bytes is fine and matches the ledger's "usaged-A4F2".
    std::snprintf(out.ssid, sizeof(out.ssid), "usaged-%02X%02X",
                  (unsigned)mac[4], (unsigned)mac[5]);
}

// --- the scan list -----------------------------------------------------------

void insertNetwork(Network* list, size_t cap, size_t& count,
                   const char* ssid, int rssi, bool secure) {
    if (list == nullptr || cap == 0) {
        return;
    }
    // A hidden network answers a broadcast probe with an empty SSID.  It cannot
    // be shown and cannot be picked; the manual field is how it is reached.
    if (ssid == nullptr || ssid[0] == '\0') {
        return;
    }

    int8_t r = (int8_t)(rssi < -128 ? -128 : (rssi > 127 ? 127 : rssi));

    // Same SSID already listed — a mesh publishes one per node, and three
    // identical rows read as a bug.  Keep the strongest, then re-insert it so
    // the list stays sorted.
    for (size_t i = 0; i < count; ++i) {
        if (std::strcmp(list[i].ssid, ssid) == 0) {
            if (r <= list[i].rssi) {
                return;
            }
            for (size_t j = i; j + 1 < count; ++j) {
                list[j] = list[j + 1];
            }
            --count;
            break;
        }
    }

    size_t pos = 0;
    while (pos < count && list[pos].rssi >= r) {
        ++pos;
    }
    // Weaker than every network in a full list: the ceiling always drops the
    // ones least likely to be the user's.
    if (pos >= cap) {
        return;
    }
    if (count < cap) {
        ++count;
    }
    for (size_t j = count - 1; j > pos; --j) {
        list[j] = list[j - 1];
    }

    copyN(list[pos].ssid, provision::kMaxSsid, ssid, std::strlen(ssid));
    list[pos].rssi = r;
    list[pos].secure = secure;
}

// --- escaping ----------------------------------------------------------------

size_t escapeHtml(const char* src, char* dst, size_t n) {
    if (dst == nullptr || n == 0) {
        return 0;
    }
    dst[0] = '\0';
    if (src == nullptr) {
        return 0;
    }

    size_t w = 0;
    for (size_t i = 0; src[i] != '\0'; ++i) {
        const char* rep = escapeOne(src[i]);
        if (rep != nullptr) {
            size_t len = std::strlen(rep);
            if (w + len >= n) {
                break;
            }
            std::memcpy(dst + w, rep, len);
            w += len;
        } else {
            if (w + 1 >= n) {
                break;
            }
            dst[w++] = src[i];
        }
    }
    dst[w] = '\0';
    return w;
}

// --- the page ----------------------------------------------------------------

void renderPage(const PageInput& in, Sink sink, void* ctx) {
    if (sink == nullptr) {
        return;
    }

    // ONE self-contained document: inline CSS, no script, no external asset.
    // Every response carries Connection: close and the server takes one client
    // at a time, so a second request is a second full TCP round through the
    // single slot — while the phone is still deciding whether a portal exists.
    emit(sink, ctx,
         "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">"
         "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">"
         "<title>usaged setup</title><style>"
         "body{margin:0;padding:18px;background:#111;color:#eee;font-size:15px;"
         "font-family:-apple-system,system-ui,Roboto,sans-serif}"
         "h1{font-size:19px;margin:0 0 2px}"
         ".id{color:#7fc8a0;font-size:13px;margin:0 0 14px}"
         "label{display:block;margin:14px 0 4px;font-size:13px;color:#bbb}"
         "input,select{width:100%;box-sizing:border-box;padding:9px;font-size:16px;"
         "background:#1c1c1c;color:#eee;border:1px solid #444;border-radius:6px}"
         "button{width:100%;margin-top:20px;padding:13px;font-size:16px;"
         "font-weight:600;border:0;border-radius:6px;background:#2fbf71;color:#04301b}"
         ".msg{padding:9px 11px;border-radius:6px;font-size:13px;line-height:1.45}"
         ".warn{background:#2b2410;border-left:3px solid #d99a20}"
         ".err{background:#2d1414;border-left:3px solid #d94040}"
         ".hint{color:#8a8a8a;font-size:12px;margin:4px 0 0;line-height:1.45}"
         ".next{color:#7fc8a0;font-size:12.5px;margin:16px 0 0;line-height:1.5}"
         // A plain <details> folds the escape hatch away with no script and no
         // second request — the only mechanism available on a server that
         // takes one client at a time.
         "details{margin-top:18px;border:1px solid #333;border-radius:6px;"
         "padding:0 11px 12px}"
         "summary{cursor:pointer;margin:0 -11px;padding:11px;font-size:13px;"
         "color:#9a9a9a}"
         "details[open] summary{border-bottom:1px solid #333;margin-bottom:2px}"
         "</style></head><body><h1>usaged setup</h1><p class=\"id\">device ");
    emitEscaped(sink, ctx, in.apSsid);
    emit(sink, ctx, "</p>");

    if (in.notice != nullptr && in.notice[0] != '\0') {
        emit(sink, ctx, "<p class=\"msg err\">");
        emitEscaped(sink, ctx, in.notice);
        emit(sink, ctx, "</p>");
    }

    // A join is in flight.  No form: the AP is about to vanish underneath this
    // page, because AP_STA is single-channel and the soft-AP adopts the
    // station's channel the moment the device associates.  Say so, and point at
    // the only surface that survives the transition — the device screen.
    if (in.joining) {
        emit(sink, ctx,
             "<p class=\"msg warn\">Joining the network now. This setup Wi-Fi "
             "disappears as soon as the device connects &mdash; that is normal, "
             "not a failure. <b>Watch the device screen:</b> it shows whether the "
             "join worked. If it did not, the setup Wi-Fi comes back and you can "
             "try again.</p></body></html>");
        return;
    }

    // The three scan outcomes are three different sentences.  A failure is not
    // an empty list, and an empty list is not a failure: both happen, and only
    // one of them has anything to do with 2.4 GHz.
    if (in.scan == ScanState::Running) {
        emit(sink, ctx,
             "<p class=\"msg warn\">Scanning for networks&hellip; reload this page "
             "in a few seconds, or just type the network name below.</p>");
    } else if (in.scan == ScanState::Failed) {
        emit(sink, ctx,
             "<p class=\"msg warn\">The scan did not finish &mdash; the radio was "
             "busy. It retries by itself, so reload this page, or type the network "
             "name below.</p>");
    } else if (in.count == 0) {
        emit(sink, ctx,
             "<p class=\"msg warn\">No networks found. This device has a "
             "<b>2.4 GHz</b> radio only, so a 5 GHz-only network never appears "
             "here &mdash; if yours is dual-band, check that its 2.4 GHz side is "
             "switched on. You can also type the name below.</p>");
    }

    emit(sink, ctx, "<form method=\"POST\" action=\"/save\">");

    if (in.count > 0 && in.networks != nullptr) {
        emit(sink, ctx,
             "<label for=\"ssid\">Wi-Fi network</label>"
             "<select id=\"ssid\" name=\"ssid\">");
        for (size_t i = 0; i < in.count; ++i) {
            emit(sink, ctx, "<option value=\"");
            emitEscaped(sink, ctx, in.networks[i].ssid);
            emit(sink, ctx, "\">");
            emitEscaped(sink, ctx, in.networks[i].ssid);
            char meta[40];
            std::snprintf(meta, sizeof(meta), " (%d dBm%s)",
                          (int)in.networks[i].rssi,
                          in.networks[i].secure ? "" : ", open");
            emitEscaped(sink, ctx, meta);
            emit(sink, ctx, "</option>");
        }
        emit(sink, ctx,
             "</select>"
             "<label for=\"ssid_manual\">Or type a network name</label>");
    } else {
        emit(sink, ctx, "<label for=\"ssid_manual\">Network name</label>");
    }

    emit(sink, ctx,
         "<input id=\"ssid_manual\" name=\"ssid_manual\" maxlength=\"32\" "
         "autocapitalize=\"off\" autocorrect=\"off\" spellcheck=\"false\">"
         "<p class=\"hint\">Needed for a hidden network, which never shows up in a "
         "scan. Leave it empty to use the list.</p>");

    // Never prefilled, never echoed back: the page has no way to learn what was
    // typed here, by construction (PageInput carries no passphrase).  This is
    // the LAST thing the default form asks for — everything after it is folded
    // away.
    emit(sink, ctx,
         "<label for=\"pass\">Wi-Fi password</label>"
         "<input id=\"pass\" name=\"pass\" type=\"password\" maxlength=\"63\" "
         "autocapitalize=\"off\" autocorrect=\"off\" spellcheck=\"false\">"
         "<p class=\"hint\">Leave empty only for an open network.</p>");

    // Say what happens next, because the next two steps happen somewhere this
    // page cannot follow: the AP hops channel on the join and this browser
    // session is gone.  A user who has been told to expect the code on the
    // device screen is not a user who thinks the thing broke.
    emit(sink, ctx,
         "<p class=\"next\">That is everything. The device joins this network, "
         "finds the usaged agent on it by itself, then shows a pairing code on "
         "its own screen. Type that code into the usaged dashboard and setup "
         "is done.</p>");

    // --- the escape hatch, folded away ---------------------------------------
    //
    // A plain <details>: no script, no second request, and closed by default on
    // every browser.  These four fields are here because mDNS discovery can be
    // blocked outright on guest and corporate networks and a screenless device
    // with no manual route is stranded — not because anyone is expected to use
    // them.  The copy inside says so.
    emit(sink, ctx,
         "<details><summary>Advanced (not usually needed)</summary>"
         "<p class=\"hint\">Leave every field below empty. The device finds the "
         "agent on the network by itself, and gets its own token from the "
         "pairing code it shows on its screen &mdash; nothing here has to be "
         "typed. Fill these in only if that discovery cannot work, which "
         "happens on networks that block multicast.</p>");

    emit(sink, ctx,
         "<label for=\"host\">Agent address</label>"
         "<input id=\"host\" name=\"host\" maxlength=\"63\" "
         "autocapitalize=\"off\" autocorrect=\"off\" spellcheck=\"false\" value=\"");
    emitEscaped(sink, ctx, in.host);
    emit(sink, ctx,
         "\"><p class=\"hint\">The computer running usaged. Empty means "
         "&ldquo;find it&rdquo;.</p>"
         "<label for=\"port\">Agent port</label>"
         "<input id=\"port\" name=\"port\" type=\"number\" inputmode=\"numeric\" "
         "min=\"1\" max=\"65535\" value=\"");
    char portBuf[8];
    std::snprintf(portBuf, sizeof(portBuf), "%u", (unsigned)in.port);
    emit(sink, ctx, portBuf);
    emit(sink, ctx, "\">");

    emit(sink, ctx,
         "<label for=\"token\">Device token</label>"
         "<input id=\"token\" name=\"token\" type=\"password\" maxlength=\"64\" "
         "autocapitalize=\"off\" autocorrect=\"off\" spellcheck=\"false\">"
         "<p class=\"hint\">Only if you are pasting a token by hand instead of "
         "pairing. Empty is the normal answer.</p>");

    emit(sink, ctx,
         "<label for=\"ota\">Update password</label>"
         "<input id=\"ota\" name=\"ota\" type=\"password\" maxlength=\"63\" "
         "autocapitalize=\"off\" autocorrect=\"off\" spellcheck=\"false\">"
         "<p class=\"hint\">Empty keeps over-the-air updates switched off.</p>"
         "</details>");

    emit(sink, ctx,
         "<button type=\"submit\">Save and join</button></form></body></html>");
}

// --- the form ----------------------------------------------------------------

Reject validate(const Submission& in, provision::Record& out) {
    provision::clear(out);

    // The manual field wins whenever it holds anything, so a hidden network is
    // always reachable even with a full list on screen.  Only the TYPED value
    // is trimmed: a picked SSID came off the radio verbatim and must stay
    // byte-identical or the join will not match.
    size_t manualLen = 0;
    const char* manual = trimSpan(in.ssidManual, manualLen);

    const char* ssid = manual;
    size_t ssidLen = manualLen;
    if (ssidLen == 0) {
        ssid = in.ssidPick != nullptr ? in.ssidPick : "";
        ssidLen = std::strlen(ssid);
    }
    if (ssidLen == 0) {
        return Reject::NoSsid;
    }
    if (ssidLen > provision::kMaxSsid) {
        return Reject::SsidTooLong;
    }
    copyN(out.ssid, provision::kMaxSsid, ssid, ssidLen);

    // A passphrase is never trimmed: a leading or trailing space is a legal
    // character in one, and silently dropping it would produce a join failure
    // nobody could explain.
    const char* pass = in.pass != nullptr ? in.pass : "";
    size_t passLen = std::strlen(pass);
    if (passLen > provision::kMaxPass) {
        return Reject::PassTooLong;
    }
    copyN(out.pass, provision::kMaxPass, pass, passLen);
    // Empty is legal and means an open network; 1..7 characters is a passphrase
    // the radio refuses outright, so storing it guarantees a failed join.
    if (provision::passphraseUnusable(out)) {
        return Reject::PassTooShort;
    }

    // OPTIONAL from here down.  An empty agent address is not a mistake: it is
    // the instruction "find it", and the device answers it with mDNS.  Refusing
    // an empty one would be refusing the whole point of this page.
    size_t hostLen = 0;
    const char* host = trimSpan(in.host, hostLen);
    if (hostLen > provision::kMaxHost) {
        return Reject::HostTooLong;
    }
    copyN(out.host, provision::kMaxHost, host, hostLen);

    if (!parsePort(in.port, out.port)) {
        return Reject::BadPort;
    }

    // Also optional: an empty token means "pair for one".  A token that IS
    // pasted still has to fit, because a truncated one authenticates nothing
    // and fails invisibly.
    size_t tokenLen = 0;
    const char* token = trimSpan(in.token, tokenLen);
    if (tokenLen > provision::kMaxToken) {
        return Reject::TokenTooLong;
    }
    copyN(out.token, provision::kMaxToken, token, tokenLen);

    // Optional by design: a device with no update password simply keeps OTA
    // disarmed (net.cpp), which is degraded rather than unprovisioned.
    const char* ota = in.otaPass != nullptr ? in.otaPass : "";
    size_t otaLen = std::strlen(ota);
    if (otaLen > provision::kMaxOtaPass) {
        return Reject::OtaPassTooLong;
    }
    copyN(out.otaPass, provision::kMaxOtaPass, ota, otaLen);

    // The rule that decides this is provision::joinable(), not the branches
    // above — and sanitize() can still empty a field, because it cuts at the
    // first control character.  A crafted POST is the realistic way that
    // happens.  Refuse rather than store something the boot path would read
    // back as having nothing to try and bounce straight back to this page.
    //
    // Note the asymmetry, which is intentional: sanitize() emptying the SSID
    // is fatal, while sanitize() emptying the host or the token is not — those
    // two simply fall back to discovery and pairing, which is where a default
    // submission starts anyway.
    provision::sanitize(out);
    if (!provision::joinable(out)) {
        provision::clear(out);
        return Reject::Unstorable;
    }
    return Reject::None;
}

const char* rejectText(Reject r) {
    // Fixed sentences, every one of them: a message that quoted the submitted
    // value would put a password into the page, the phone's history and any
    // screenshot of it.
    switch (r) {
        case Reject::None:
            return "";
        case Reject::NoSsid:
            return "Pick a network, or type the name of a hidden one.";
        case Reject::SsidTooLong:
            return "That network name is too long (32 characters maximum).";
        case Reject::PassTooShort:
            return "A Wi-Fi password must be at least 8 characters. Leave it "
                   "empty only if the network is open.";
        case Reject::PassTooLong:
            return "That Wi-Fi password is too long (63 characters maximum).";
        case Reject::HostTooLong:
            return "That address is too long (63 characters maximum). Leaving "
                   "it empty lets the device find the agent by itself.";
        case Reject::BadPort:
            return "The port must be a whole number between 1 and 65535.";
        case Reject::TokenTooLong:
            return "That device token is too long (64 characters maximum).";
        case Reject::OtaPassTooLong:
            return "That update password is too long (63 characters maximum).";
        case Reject::Unstorable:
            return "Those values could not be stored. Remove any unusual "
                   "characters and try again.";
    }
    return "Those values could not be stored.";
}

// --- join failures -----------------------------------------------------------

const char* joinFailText(uint8_t reason) {
    // wifi_err_reason_t, read from
    // framework-arduinoespressif32/tools/sdk/esp32s3/include/esp_wifi/include/
    // esp_wifi_types.h on 2026-09-06.  Short enough for one 240px line at text
    // size 1, because this lands on the DEVICE SCREEN: once the radio is
    // involved the browser session may already be gone.
    switch (reason) {
        case 201:  // WIFI_REASON_NO_AP_FOUND
            return "network not found";
        case 15:   // WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT
        case 202:  // WIFI_REASON_AUTH_FAIL
        case 204:  // WIFI_REASON_HANDSHAKE_TIMEOUT
            return "wrong password";
        case 203:  // WIFI_REASON_ASSOC_FAIL
        case 205:  // WIFI_REASON_CONNECTION_FAIL
            return "router refused";
        case 200:  // WIFI_REASON_BEACON_TIMEOUT
            return "signal too weak";
        case 2:    // WIFI_REASON_AUTH_EXPIRE
        case 4:    // WIFI_REASON_ASSOC_EXPIRE
            return "network dropped us";
        case kJoinReasonUnknown:
            return "timed out";
        default:
            return "join failed";
    }
}

} // namespace portal
} // namespace usage
