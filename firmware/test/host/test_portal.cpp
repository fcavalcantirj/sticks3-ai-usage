// firmware/test/host/test_portal.cpp — tests for the pure-C++17 half of the
// SoftAP captive portal (task 77): AP identity, scan list, page rendering and
// form rules.
//
// The rules under test are the ones a stranger with a phone actually meets: a
// scan that found nothing must SAY why, a scan that failed must not claim it
// found nothing, an incomplete form must be refused rather than half-saved,
// and no submitted password may ever reach the page.  Every string below is an
// obvious fake — no real credential value appears in this repo.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include <cstring>
#include <string>

#include "framework.h"
#include "usage/portal.h"
#include "usage/provision.h"

using usage::portal::ApIdentity;
using usage::portal::Network;
using usage::portal::PageInput;
using usage::portal::Reject;
using usage::portal::ScanState;
using usage::portal::Submission;
using usage::portal::apIdentity;
using usage::portal::escapeHtml;
using usage::portal::insertNetwork;
using usage::portal::joinFailText;
using usage::portal::kApPassLen;
using usage::portal::kMaxNetworks;
using usage::portal::rejectText;
using usage::portal::renderPage;
using usage::portal::validate;
using usage::provision::Record;
using usage::provision::complete;
using usage::provision::kDefaultPort;

namespace {

// The device under test, so the SSID assertion below is against a real MAC
// rather than an invented one (docs/DEVICES.md unit #2).
const uint8_t kUnit2Mac[6] = {0x14, 0xc1, 0x9f, 0xd4, 0xd5, 0x34};

void appendSink(void* ctx, const char* chunk) {
    static_cast<std::string*>(ctx)->append(chunk);
}

std::string render(const PageInput& in) {
    std::string out;
    renderPage(in, appendSink, &out);
    return out;
}

bool has(const std::string& hay, const char* needle) {
    return hay.find(needle) != std::string::npos;
}

// A page input with a completed scan and nothing found, which every test then
// adjusts one field at a time.
PageInput basePage() {
    PageInput in;
    in.networks = nullptr;
    in.count = 0;
    in.scan = ScanState::Ok;
    in.apSsid = "usaged-D534";
    in.host = "usaged.local";
    in.port = kDefaultPort;
    in.notice = nullptr;
    in.joining = false;
    return in;
}

// A submission that validate() accepts, which tests then break one field at a
// time.  "hunter2-not-real" is a fake, and is also the string the
// never-echoed-back tests search the rendered page for.
Submission baseSubmission() {
    Submission s;
    s.ssidPick = "test-ssid";
    s.ssidManual = "";
    s.pass = "hunter2-not-real";
    s.host = "usaged.local";
    s.port = "8765";
    s.token = "test-token-0123";
    s.otaPass = "test-ota-pass";
    return s;
}

} // namespace

// --- AP identity --------------------------------------------------------------

TEST(portal_ap_ssid_is_usaged_plus_last_two_mac_bytes) {
    ApIdentity id;
    apIdentity(kUnit2Mac, id);
    ASSERT_STREQ(id.ssid, "usaged-D534");
}

TEST(portal_ap_identity_is_deterministic) {
    ApIdentity a;
    ApIdentity b;
    apIdentity(kUnit2Mac, a);
    apIdentity(kUnit2Mac, b);
    // Both values are read off the device screen, so a value that changed
    // between boots would be worse than no value at all.
    ASSERT_STREQ(a.ssid, b.ssid);
    ASSERT_STREQ(a.pass, b.pass);
}

TEST(portal_ap_pass_is_long_enough_for_softap) {
    ApIdentity id;
    apIdentity(kUnit2Mac, id);
    // softAP() returns false outright below 8 characters, so this is the
    // difference between a portal and a device that looks dead.
    ASSERT_TRUE(std::strlen(id.pass) >= usage::provision::kMinWpaPass);
    ASSERT_EQ(std::strlen(id.pass), kApPassLen);
}

TEST(portal_ap_pass_avoids_ambiguous_characters) {
    // Read off a 240x135 screen and typed into a phone: 0/O and 1/I are the
    // pairs that cost a user three attempts.
    const uint8_t macs[4][6] = {
        {0x14, 0xc1, 0x9f, 0xd4, 0xd5, 0x34},
        {0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
        {0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
        {0xac, 0x27, 0x6e, 0xd2, 0x68, 0xb8},
    };
    for (int m = 0; m < 4; ++m) {
        ApIdentity id;
        apIdentity(macs[m], id);
        for (size_t i = 0; id.pass[i] != '\0'; ++i) {
            char c = id.pass[i];
            ASSERT_TRUE(c != '0' && c != 'O');
            ASSERT_TRUE(c != '1' && c != 'I');
            ASSERT_TRUE((c >= '2' && c <= '9') || (c >= 'A' && c <= 'Z'));
        }
    }
}

TEST(portal_ap_pass_differs_between_devices) {
    ApIdentity a;
    ApIdentity b;
    const uint8_t other[6] = {0x14, 0xc1, 0x9f, 0xd4, 0xd5, 0x35};
    apIdentity(kUnit2Mac, a);
    apIdentity(other, b);
    ASSERT_TRUE(std::strcmp(a.pass, b.pass) != 0);
    ASSERT_TRUE(std::strcmp(a.ssid, b.ssid) != 0);
}

// --- the scan list ------------------------------------------------------------

TEST(portal_scan_list_sorts_strongest_first) {
    Network list[kMaxNetworks];
    size_t count = 0;
    insertNetwork(list, kMaxNetworks, count, "weak", -80, true);
    insertNetwork(list, kMaxNetworks, count, "strong", -35, true);
    insertNetwork(list, kMaxNetworks, count, "middle", -60, false);

    ASSERT_EQ(count, (size_t)3);
    ASSERT_STREQ(list[0].ssid, "strong");
    ASSERT_STREQ(list[1].ssid, "middle");
    ASSERT_STREQ(list[2].ssid, "weak");
    ASSERT_FALSE(list[1].secure);
}

TEST(portal_scan_list_keeps_one_entry_per_ssid) {
    Network list[kMaxNetworks];
    size_t count = 0;
    // A mesh publishes one AP per node.  Three identical rows read as a bug.
    insertNetwork(list, kMaxNetworks, count, "mesh", -70, true);
    insertNetwork(list, kMaxNetworks, count, "mesh", -42, true);
    insertNetwork(list, kMaxNetworks, count, "mesh", -88, true);

    ASSERT_EQ(count, (size_t)1);
    ASSERT_EQ((int)list[0].rssi, -42);
}

TEST(portal_scan_list_drops_hidden_ssids) {
    Network list[kMaxNetworks];
    size_t count = 0;
    // A hidden network answers a broadcast probe with an empty SSID; it cannot
    // be picked, which is exactly what the manual field exists for.
    insertNetwork(list, kMaxNetworks, count, "", -40, true);
    insertNetwork(list, kMaxNetworks, count, nullptr, -40, true);
    ASSERT_EQ(count, (size_t)0);
}

TEST(portal_scan_list_caps_and_drops_the_weakest) {
    Network list[4];
    size_t count = 0;
    char name[8];
    for (int i = 0; i < 6; ++i) {
        std::snprintf(name, sizeof(name), "n%d", i);
        insertNetwork(list, 4, count, name, -30 - i * 10, true);
    }
    ASSERT_EQ(count, (size_t)4);
    ASSERT_STREQ(list[0].ssid, "n0");
    ASSERT_STREQ(list[3].ssid, "n3");

    // A newcomer weaker than a full list is simply not there.
    insertNetwork(list, 4, count, "faint", -99, true);
    ASSERT_EQ(count, (size_t)4);
    ASSERT_STREQ(list[3].ssid, "n3");

    // A newcomer strong enough displaces the weakest.
    insertNetwork(list, 4, count, "loud", -10, true);
    ASSERT_EQ(count, (size_t)4);
    ASSERT_STREQ(list[0].ssid, "loud");
    ASSERT_STREQ(list[3].ssid, "n2");
}

// --- escaping -----------------------------------------------------------------

TEST(portal_escape_html_covers_every_metacharacter) {
    char buf[64];
    escapeHtml("<a href=\"x\">&'", buf, sizeof(buf));
    ASSERT_STREQ(buf, "&lt;a href=&quot;x&quot;&gt;&amp;&#39;");
}

TEST(portal_escape_html_truncates_without_overrunning) {
    char buf[6];
    // "&amp;" is 5 bytes and fits; the second one cannot, so it is dropped
    // whole rather than half-written.
    size_t n = escapeHtml("&&", buf, sizeof(buf));
    ASSERT_STREQ(buf, "&amp;");
    ASSERT_EQ(n, (size_t)5);
}

TEST(portal_escape_html_survives_null_input) {
    char buf[8];
    ASSERT_EQ(escapeHtml(nullptr, buf, sizeof(buf)), (size_t)0);
    ASSERT_STREQ(buf, "");
}

// --- the page -----------------------------------------------------------------

TEST(portal_page_is_one_self_contained_document) {
    PageInput in = basePage();
    std::string page = render(in);
    // Every external asset is another full TCP round through a server that
    // takes ONE client at a time, while the phone is still deciding whether a
    // portal exists.
    ASSERT_FALSE(has(page, "<script"));
    ASSERT_FALSE(has(page, "<link"));
    ASSERT_FALSE(has(page, "http://"));
    ASSERT_FALSE(has(page, "https://"));
    ASSERT_TRUE(has(page, "<style>"));
    ASSERT_TRUE(has(page, "</html>"));
}

TEST(portal_page_carries_every_field_the_device_needs) {
    Network list[2];
    size_t count = 0;
    insertNetwork(list, 2, count, "home-wifi", -45, true);

    PageInput in = basePage();
    in.networks = list;
    in.count = count;
    std::string page = render(in);

    ASSERT_TRUE(has(page, "name=\"ssid\""));         // pick-list
    ASSERT_TRUE(has(page, "name=\"ssid_manual\""));  // hidden networks
    ASSERT_TRUE(has(page, "name=\"pass\""));
    ASSERT_TRUE(has(page, "name=\"host\""));
    ASSERT_TRUE(has(page, "name=\"port\""));
    ASSERT_TRUE(has(page, "name=\"token\""));
    ASSERT_TRUE(has(page, "action=\"/save\""));
    ASSERT_TRUE(has(page, "home-wifi"));
    ASSERT_TRUE(has(page, "-45 dBm"));
    ASSERT_TRUE(has(page, "usaged.local"));   // agent host prefill
    ASSERT_TRUE(has(page, "value=\"8765\"")); // agent port prefill
    ASSERT_TRUE(has(page, "usaged-D534"));    // which device this is
}

TEST(portal_page_explains_an_empty_scan_with_the_24ghz_reason) {
    PageInput in = basePage();  // scan Ok, count 0
    std::string page = render(in);
    // An empty list with no explanation is the single most confusing outcome a
    // new user can be handed, and the reason is almost always the same one.
    ASSERT_TRUE(has(page, "2.4 GHz"));
    ASSERT_TRUE(has(page, "No networks found"));
    // The manual field must still be there, or an empty scan is a dead end.
    ASSERT_TRUE(has(page, "name=\"ssid_manual\""));
    ASSERT_TRUE(has(page, "<button"));
}

TEST(portal_page_does_not_call_a_failed_scan_empty) {
    PageInput in = basePage();
    in.scan = ScanState::Failed;
    std::string page = render(in);
    // WIFI_SCAN_FAILED (-2) and an empty scan (0) are different events and both
    // happen (measured: 5.6% and 1.1%).  Rendering -2 as "no networks found"
    // would send the user to hunt a 5 GHz problem that is not there.
    ASSERT_FALSE(has(page, "No networks found"));
    ASSERT_FALSE(has(page, "2.4 GHz"));
    ASSERT_TRUE(has(page, "did not finish"));
}

TEST(portal_page_says_a_scan_is_still_running) {
    PageInput in = basePage();
    in.scan = ScanState::Running;
    std::string page = render(in);
    ASSERT_FALSE(has(page, "No networks found"));
    ASSERT_TRUE(has(page, "Scanning"));
}

TEST(portal_page_escapes_a_hostile_ssid) {
    Network list[2];
    size_t count = 0;
    insertNetwork(list, 2, count, "<b>\"pwn\"</b>", -50, true);

    PageInput in = basePage();
    in.networks = list;
    in.count = count;
    std::string page = render(in);

    // A scanned SSID is text an unknown access point chose, and it lands both
    // inside an attribute and inside an element.
    ASSERT_FALSE(has(page, "<b>\"pwn\"</b>"));
    ASSERT_TRUE(has(page, "&lt;b&gt;&quot;pwn&quot;&lt;/b&gt;"));
}

TEST(portal_joining_page_has_no_form_and_points_at_the_screen) {
    PageInput in = basePage();
    in.joining = true;
    std::string page = render(in);
    // AP_STA is single-channel: the soft-AP adopts the station's channel, so
    // the phone is dropped the moment the join succeeds.  A "connected!" page
    // can never arrive, and the device screen is the only surface that lasts.
    ASSERT_FALSE(has(page, "<form"));
    ASSERT_FALSE(has(page, "name=\"pass\""));
    ASSERT_TRUE(has(page, "device screen"));
    ASSERT_TRUE(has(page, "</html>"));
}

TEST(portal_page_shows_a_notice_when_there_is_one) {
    PageInput in = basePage();
    in.notice = rejectText(Reject::PassTooShort);
    std::string page = render(in);
    ASSERT_TRUE(has(page, "at least 8 characters"));
}

TEST(portal_page_never_renders_a_submitted_password) {
    // The strongest form of this rule is structural: PageInput carries no
    // passphrase, no token and no OTA password, so there is nothing to leak.
    // This test guards the other half — that no rejection message quotes one.
    Submission s = baseSubmission();
    s.pass = "short";  // rejected: 1..7 characters
    Record rec;
    Reject r = validate(s, rec);
    ASSERT_TRUE(r == Reject::PassTooShort);

    PageInput in = basePage();
    in.notice = rejectText(r);
    std::string page = render(in);
    ASSERT_FALSE(has(page, "short"));
    ASSERT_FALSE(has(page, "hunter2-not-real"));
    ASSERT_FALSE(has(page, "test-token-0123"));
}

// --- form validation ----------------------------------------------------------

TEST(portal_validate_accepts_a_complete_submission) {
    Record rec;
    ASSERT_TRUE(validate(baseSubmission(), rec) == Reject::None);
    ASSERT_STREQ(rec.ssid, "test-ssid");
    ASSERT_STREQ(rec.pass, "hunter2-not-real");
    ASSERT_STREQ(rec.host, "usaged.local");
    ASSERT_EQ(rec.port, (uint16_t)8765);
    ASSERT_STREQ(rec.token, "test-token-0123");
    ASSERT_STREQ(rec.otaPass, "test-ota-pass");
    // The portal can never save something the boot path reads back as
    // unprovisioned: that would bounce the user straight back to this page.
    ASSERT_TRUE(complete(rec));
}

TEST(portal_validate_prefers_a_typed_ssid_over_the_list) {
    Submission s = baseSubmission();
    s.ssidManual = "hidden-net";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::None);
    ASSERT_STREQ(rec.ssid, "hidden-net");
}

TEST(portal_validate_rejects_a_submission_with_no_network) {
    Submission s = baseSubmission();
    s.ssidPick = "";
    s.ssidManual = "   ";  // whitespace only is not a network name
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::NoSsid);
    ASSERT_FALSE(complete(rec));
}

TEST(portal_validate_rejects_a_submission_with_no_host) {
    Submission s = baseSubmission();
    s.host = "";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::NoHost);
    ASSERT_FALSE(complete(rec));
}

TEST(portal_validate_rejects_a_submission_with_no_token) {
    // The completeness rule requires a token, so accepting this would store a
    // record that reads back as unprovisioned on the very next boot.
    Submission s = baseSubmission();
    s.token = "  ";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::NoToken);
    ASSERT_FALSE(complete(rec));
}

TEST(portal_validate_rejects_an_unusable_passphrase) {
    Submission s = baseSubmission();
    s.pass = "1234567";  // 7 characters: the radio refuses it outright
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::PassTooShort);
}

TEST(portal_validate_allows_an_open_network) {
    Submission s = baseSubmission();
    s.pass = "";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::None);
    ASSERT_STREQ(rec.pass, "");
    ASSERT_TRUE(complete(rec));
}

TEST(portal_validate_never_trims_a_passphrase) {
    // A leading or trailing space is a legal character in a Wi-Fi password;
    // dropping it silently would produce a join failure nobody could explain.
    Submission s = baseSubmission();
    s.pass = " spaced pass ";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::None);
    ASSERT_STREQ(rec.pass, " spaced pass ");
}

TEST(portal_validate_trims_the_host_and_the_token) {
    // Phones append a space when they autocomplete.  A trailing space here
    // joins the network fine and then 401s on every fetch.
    Submission s = baseSubmission();
    s.host = "  usaged.local ";
    s.token = " test-token-0123  ";
    s.ssidManual = "  typed-net ";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::None);
    ASSERT_STREQ(rec.host, "usaged.local");
    ASSERT_STREQ(rec.token, "test-token-0123");
    ASSERT_STREQ(rec.ssid, "typed-net");
}

TEST(portal_validate_rejects_oversized_fields) {
    char big[128];
    std::memset(big, 'x', sizeof(big));
    big[sizeof(big) - 1] = '\0';

    Record rec;
    Submission s = baseSubmission();
    s.ssidManual = big;
    ASSERT_TRUE(validate(s, rec) == Reject::SsidTooLong);

    s = baseSubmission();
    s.pass = big;
    ASSERT_TRUE(validate(s, rec) == Reject::PassTooLong);

    s = baseSubmission();
    s.host = big;
    ASSERT_TRUE(validate(s, rec) == Reject::HostTooLong);

    s = baseSubmission();
    s.token = big;
    ASSERT_TRUE(validate(s, rec) == Reject::TokenTooLong);

    s = baseSubmission();
    s.otaPass = big;
    ASSERT_TRUE(validate(s, rec) == Reject::OtaPassTooLong);
}

TEST(portal_validate_defaults_an_empty_port) {
    Submission s = baseSubmission();
    s.port = "";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::None);
    ASSERT_EQ(rec.port, kDefaultPort);
}

TEST(portal_validate_rejects_an_impossible_port) {
    Record rec;
    Submission s = baseSubmission();
    s.port = "0";  // can never be dialled; defaulting it would hide a typo
    ASSERT_TRUE(validate(s, rec) == Reject::BadPort);

    s.port = "70000";
    ASSERT_TRUE(validate(s, rec) == Reject::BadPort);

    s.port = "87a5";
    ASSERT_TRUE(validate(s, rec) == Reject::BadPort);
}

TEST(portal_validate_refuses_a_field_sanitize_would_empty) {
    // sanitize() cuts a field at the first control character, so a crafted POST
    // can produce a record that passes every length check and still reads back
    // as unprovisioned.  Refuse it rather than store it.
    Submission s = baseSubmission();
    s.ssidManual = "\x01hidden";
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::Unstorable);
    ASSERT_FALSE(complete(rec));
}

TEST(portal_validate_tolerates_missing_fields) {
    // A short or hand-made POST must not crash a device with no cable.
    Submission s;
    s.ssidPick = nullptr;
    s.ssidManual = nullptr;
    s.pass = nullptr;
    s.host = nullptr;
    s.port = nullptr;
    s.token = nullptr;
    s.otaPass = nullptr;
    Record rec;
    ASSERT_TRUE(validate(s, rec) == Reject::NoSsid);
}

TEST(portal_reject_text_is_a_sentence_for_every_reason) {
    const Reject all[] = {
        Reject::NoSsid,        Reject::SsidTooLong,   Reject::PassTooShort,
        Reject::PassTooLong,   Reject::NoHost,        Reject::HostTooLong,
        Reject::BadPort,       Reject::NoToken,       Reject::TokenTooLong,
        Reject::OtaPassTooLong, Reject::Unstorable,
    };
    for (Reject r : all) {
        const char* t = rejectText(r);
        ASSERT_TRUE(t != nullptr);
        ASSERT_TRUE(std::strlen(t) > 0);
    }
    ASSERT_STREQ(rejectText(Reject::None), "");
}

// --- join failures ------------------------------------------------------------

TEST(portal_join_fail_text_names_the_common_causes) {
    // The failure the user actually hits is a mistyped password, and it must
    // arrive on the screen as a sentence rather than as the number 202.
    ASSERT_STREQ(joinFailText(202), "wrong password");   // AUTH_FAIL
    ASSERT_STREQ(joinFailText(15), "wrong password");    // 4WAY_HANDSHAKE_TIMEOUT
    ASSERT_STREQ(joinFailText(204), "wrong password");   // HANDSHAKE_TIMEOUT
    ASSERT_STREQ(joinFailText(201), "network not found"); // NO_AP_FOUND
    ASSERT_STREQ(joinFailText(200), "signal too weak");   // BEACON_TIMEOUT
    ASSERT_STREQ(joinFailText(0), "timed out");           // our own sentinel
}

TEST(portal_join_fail_text_is_short_enough_for_the_screen) {
    for (int r = 0; r < 256; ++r) {
        const char* t = joinFailText((uint8_t)r);
        ASSERT_TRUE(t != nullptr);
        ASSERT_TRUE(std::strlen(t) > 0);
        // One 240px line at text size 1 is 40 characters.
        ASSERT_TRUE(std::strlen(t) <= 40);
    }
}
