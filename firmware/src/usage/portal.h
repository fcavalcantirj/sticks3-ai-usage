// firmware/src/usage/portal.h — pure C++17 logic for the SoftAP captive
// portal (task 77): the AP identity, the scan list, the setup page and the
// form rules.
//
// No M5, WiFi or Arduino headers: same discipline as provision.{h,cpp}, so
// every rule below is host-tested by `make fw-test`.  The HAL
// (hal/sticks3/portal.*) owns the radio, the DNS server and the web server;
// this module owns everything that can be decided without one.
//
// WHY THIS EXISTS (task 77): a device that carries no credentials has to be
// told four things — which network, its password, which agent, and a device
// token — before it can do anything at all, and it must be told them by a
// stranger with a phone, no cable and no build step.  The portal is that
// conversation.  Getting it wrong strands a screenless device.
//
// THREE RULES THAT SHAPE EVERY FUNCTION HERE:
//
//  1. A SUBMITTED PASSWORD IS NEVER RENDERED BACK.  renderPage() takes no
//     passphrase, no token and no OTA password — not as a prefill, not as a
//     "you typed" echo, not inside a rejection message.  rejectText() returns
//     fixed sentences for the same reason: a reason that quoted the value
//     would put it in the page, in the phone's history and in a screenshot.
//
//  2. THE COMPLETENESS RULE LIVES IN provision.h, NOT HERE.  validate() builds
//     a Record and then answers with the SAME rule the boot path uses, so a
//     submission the portal accepts can never read back as unprovisioned.
//
//  3. THE PAGE IS ONE SELF-CONTAINED DOCUMENT.  Inline CSS, no script, no
//     external asset: every response the ESP32 WebServer sends carries
//     Connection: close and it serves ONE client at a time, so each extra
//     asset is another full TCP round through the single slot — precisely
//     while the phone is deciding whether a captive portal exists.
#pragma once

#include <cstddef>
#include <cstdint>

#include "usage/provision.h"

namespace usage {
namespace portal {

// --- the access-point identity ----------------------------------------------
//
// Both values are SHOWN ON THE DEVICE SCREEN and typed into a phone, so they
// are derived from the MAC: stable across reboots, unique per device, and
// needing no label, no NVS entry and no first-boot randomness.
//
// HONEST LIMITATION, recorded rather than hidden: the AP MAC is broadcast in
// every beacon, so anyone holding this source can compute the passphrase.
// WPA2 here buys exactly one thing over an open AP — a neighbour who has not
// read the screen cannot reach the setup page by accident — and that is the
// bar the task sets.  It is not a secret and must never be treated as one.

// "usaged-" + 4 hex digits of the last two MAC bytes, e.g. "usaged-D534".
// The SSID is PICKED from a list, never typed, so plain hex is fine here.
inline constexpr size_t kApSsidCap = 15;

// The passphrase IS typed, off a 240x135 screen, so it comes from a 32-symbol
// alphabet with no 0/O and no 1/I — the same unambiguous-alphabet rule task 78
// applies to the pairing code.  Ten symbols is 50 bits; softAP() rejects
// anything under 8 characters outright.
inline constexpr size_t kApPassLen = 10;

struct ApIdentity {
    char ssid[kApSsidCap + 1];
    char pass[kApPassLen + 1];
};

// Derive the AP SSID and passphrase from the six MAC bytes.  Deterministic:
// the same MAC always yields the same pair, on every boot and every build.
void apIdentity(const uint8_t mac[6], ApIdentity& out);

// --- the scan list -----------------------------------------------------------

// One scanned network as the page needs it.  No radio types: the HAL converts.
struct Network {
    char   ssid[provision::kMaxSsid + 1];
    int8_t rssi;    // dBm, as reported
    bool   secure;  // anything but an open network
};

// Sixteen is more than a phone list can usefully show and is a hard ceiling on
// the page size; the list is sorted strongest-first, so the overflow is always
// the networks least likely to be the user's.
inline constexpr size_t kMaxNetworks = 16;

// What the last scan actually did.  Failed and an empty Ok are DIFFERENT
// events and both happen — the spike measured 5.6% failures and 1.1% genuinely
// empty scans.  Rendering a failure as "no networks found" would be actively
// misleading, and rendering an empty result as "retrying" would hide the 2.4
// GHz explanation the user actually needs.
enum class ScanState : uint8_t {
    Running,  // a scan is in flight and nothing has been shown yet
    Ok,       // the scan completed; count may legitimately be 0
    Failed,   // the radio refused (WIFI_SCAN_FAILED) — retrying
};

// Insert one scan result into the list, keeping it sorted by RSSI descending
// and keeping only the STRONGEST entry per SSID.  A mesh network otherwise
// appears three times, which reads as a bug.  An empty SSID (a hidden network
// answering a broadcast probe) is dropped — that is what the manual field is
// for.  count is updated in place and never exceeds cap.
void insertNetwork(Network* list, size_t cap, size_t& count,
                   const char* ssid, int rssi, bool secure);

// --- the page ----------------------------------------------------------------

// Everything the page renders.  Note what is absent: no passphrase, no token,
// no OTA password.  Rule 1 above is enforced by the shape of this struct.
struct PageInput {
    const Network* networks;  // may be null when count == 0
    size_t         count;
    ScanState      scan;
    const char*    apSsid;    // this device's AP name, shown as its identity
    const char*    host;      // agent address prefill (not a credential)
    uint16_t       port;      // agent port prefill
    const char*    notice;    // last rejection / join failure, or null
    bool           joining;   // a join is in flight: status page, no form
};

// The page is streamed rather than returned: it is far larger than any buffer
// this project puts on the stack, and the pure layer allocates nothing.  The
// HAL appends each chunk to the response; the host test appends to a string.
using Sink = void (*)(void* ctx, const char* chunk);

// Render the whole document.  Emits, in this order: the device identity, any
// notice, then either the joining status (in.joining) or the form — network
// pick-list, manual SSID for hidden networks, Wi-Fi password, agent address
// and port, device token, optional OTA password.
//
// When the scan completed with nothing (ScanState::Ok, count 0) the page says
// SO, and says why: the ESP32-S3 radio is 2.4 GHz only, so a 5 GHz-only or
// band-steering SSID simply cannot appear.  An empty list with no explanation
// is the single most confusing outcome a new user can be handed.
void renderPage(const PageInput& in, Sink sink, void* ctx);

// HTML-escape src into dst, always NUL-terminating within n bytes.  Scanned
// SSIDs are attacker-controlled text that lands inside an attribute and inside
// an element, so both quote forms are escaped.  Returns the bytes written,
// excluding the terminator.
size_t escapeHtml(const char* src, char* dst, size_t n);

// Worst case for escapeHtml: every byte becomes "&quot;" (6 bytes).  Sized for
// the longest field the page renders back (the agent host).
inline constexpr size_t kEscapeCap = 6 * (provision::kMaxHost + 1) + 1;

// --- the form ----------------------------------------------------------------

// The submitted fields, exactly as the web server hands them over (already
// URL-decoded).  A missing field arrives as an empty string, never null, but
// null is tolerated so a caller cannot crash the device with a short form.
struct Submission {
    const char* ssidPick;    // <select name="ssid">      — from the scan list
    const char* ssidManual;  // <input name="ssid_manual"> — hidden networks
    const char* pass;        // may be empty: an open network is legal
    const char* host;
    const char* port;        // decimal text; empty falls back to kDefaultPort
    const char* token;
    const char* otaPass;     // optional; empty disarms OTA (net.cpp)
};

// Why a submission was refused.  Every value maps to a fixed sentence that
// contains no submitted text — see rule 1.
enum class Reject : uint8_t {
    None = 0,
    NoSsid,
    SsidTooLong,
    PassTooShort,   // 1..7 characters: the radio refuses it, so storing it
                    // guarantees a join failure
    PassTooLong,
    NoHost,
    HostTooLong,
    BadPort,
    NoToken,
    TokenTooLong,
    OtaPassTooLong,
    Unstorable,     // survived every field check and still failed
                    // provision::complete() after sanitize() — the realistic
                    // cause is a control character in a crafted POST, which
                    // sanitize() cuts a field at
};

// Turn a submission into a Record.  The manual SSID wins when it is non-empty,
// so a hidden network can always be reached even with a list on screen.
//
// Returns Reject::None only when the resulting record satisfies
// provision::complete() — the same rule credsLoad() applies on the boot path,
// so the portal can never save something that reads back as unprovisioned and
// bounces the user straight back here.  On rejection `out` is left cleared.
Reject validate(const Submission& in, provision::Record& out);

// A short, fixed sentence for a rejection.  Safe to render into the page and
// safe to log: it never contains a submitted value.
const char* rejectText(Reject r);

// --- join failures -----------------------------------------------------------
//
// The failure case is the one the user actually hits — a mistyped password —
// and it must arrive as a sentence, not a number.  Codes are wifi_err_reason_t
// (esp_wifi_types.h, read 2026-09-06); they live here as plain integers so the
// mapping stays host-testable.

inline constexpr uint8_t kJoinReasonUnknown = 0;  // our own timeout sentinel

// A short phrase for the DEVICE SCREEN — measured against a 240px line at text
// size 1 — describing why a join failed.
const char* joinFailText(uint8_t reason);

} // namespace portal
} // namespace usage
