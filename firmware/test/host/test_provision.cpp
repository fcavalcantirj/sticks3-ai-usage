// firmware/test/host/test_provision.cpp — tests for the pure-C++17 credential
// record and provisioning state machine (task 76).
//
// The rules under test are the ones that keep a screenless, cableless device
// recoverable: a partial record is never "provisioned", a corrupt NVS blob
// never reaches the radio, and N consecutive join failures always end in the
// portal.  The strings below are obvious fakes — no real credential value ever
// appears in this repo.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include <cstring>

#include "framework.h"
#include "usage/provision.h"

using usage::provision::Machine;
using usage::provision::Record;
using usage::provision::State;
using usage::provision::clear;
using usage::provision::complete;
using usage::provision::kDefaultPort;
using usage::provision::kMaxHost;
using usage::provision::kMaxOtaPass;
using usage::provision::kMaxPass;
using usage::provision::kMaxSsid;
using usage::provision::kMaxToken;
using usage::provision::passphraseUnusable;
using usage::provision::sanitize;

namespace {

// Fill a fixed-size field the way the HAL will: copy at most cap bytes and
// always terminate.
void setField(char* buf, size_t cap, const char* v) {
    size_t i = 0;
    while (i < cap && v[i] != '\0') {
        buf[i] = v[i];
        ++i;
    }
    buf[i] = '\0';
}

// A record that satisfies the completeness rule, for tests that then break one
// field at a time.
Record makeComplete() {
    Record r;
    clear(r);
    setField(r.ssid, kMaxSsid, "test-ssid");
    setField(r.pass, kMaxPass, "test-passphrase");
    setField(r.host, kMaxHost, "usaged.local");
    setField(r.token, kMaxToken, "test-token");
    setField(r.otaPass, kMaxOtaPass, "test-ota");
    return r;
}

// Drive n consecutive failed joins, obeying the Retry -> Joining edge between
// them the way the caller's backoff timer does.
void failJoins(Machine& m, int n) {
    for (int i = 0; i < n; ++i) {
        m.onJoinResult(false);
        m.onRetryElapsed();  // no-op once the machine has fallen to Portal
    }
}

} // namespace

// --- clear -------------------------------------------------------------------

TEST(provision_clear_zeroes_fields_and_defaults_port) {
    Record r = makeComplete();
    r.port = 1234;
    clear(r);

    ASSERT_EQ('\0', r.ssid[0]);
    ASSERT_EQ('\0', r.pass[0]);
    ASSERT_EQ('\0', r.host[0]);
    ASSERT_EQ('\0', r.token[0]);
    ASSERT_EQ('\0', r.otaPass[0]);
    ASSERT_EQ(kDefaultPort, r.port);
    // A cleared record is by definition unprovisioned.
    ASSERT_FALSE(complete(r));
}

TEST(provision_clear_leaves_nothing_for_sanitize_to_fix) {
    Record r;
    clear(r);
    ASSERT_FALSE(sanitize(r));
}

// --- complete: the rule the ledger calls out ---------------------------------

TEST(provision_complete_requires_ssid_host_token) {
    Record r = makeComplete();
    ASSERT_TRUE(complete(r));
}

TEST(provision_complete_false_without_ssid) {
    Record r = makeComplete();
    r.ssid[0] = '\0';
    ASSERT_FALSE(complete(r));
}

TEST(provision_complete_false_without_host) {
    Record r = makeComplete();
    r.host[0] = '\0';
    ASSERT_FALSE(complete(r));
}

TEST(provision_complete_false_without_token) {
    Record r = makeComplete();
    r.token[0] = '\0';
    ASSERT_FALSE(complete(r));
}

TEST(provision_complete_true_without_pass_open_network) {
    // An open network has no passphrase; that is a legal, complete record.
    Record r = makeComplete();
    r.pass[0] = '\0';
    ASSERT_TRUE(complete(r));
}

TEST(provision_complete_true_without_otapass) {
    // No OTA password is a degraded device, not an unprovisioned one.
    Record r = makeComplete();
    r.otaPass[0] = '\0';
    ASSERT_TRUE(complete(r));
}

TEST(provision_partial_record_never_counts_as_provisioned) {
    // Every proper subset of {ssid, host, token} is unprovisioned: a device
    // that half-believes it is configured retries forever with no way back.
    Record r;

    clear(r);
    setField(r.ssid, kMaxSsid, "test-ssid");
    ASSERT_FALSE(complete(r));

    clear(r);
    setField(r.host, kMaxHost, "usaged.local");
    ASSERT_FALSE(complete(r));

    clear(r);
    setField(r.token, kMaxToken, "test-token");
    ASSERT_FALSE(complete(r));

    clear(r);
    setField(r.ssid, kMaxSsid, "test-ssid");
    setField(r.host, kMaxHost, "usaged.local");
    ASSERT_FALSE(complete(r));  // token still missing

    clear(r);
    setField(r.ssid, kMaxSsid, "test-ssid");
    setField(r.token, kMaxToken, "test-token");
    ASSERT_FALSE(complete(r));  // host still missing

    clear(r);
    setField(r.host, kMaxHost, "usaged.local");
    setField(r.token, kMaxToken, "test-token");
    ASSERT_FALSE(complete(r));  // ssid still missing
}

TEST(provision_partial_record_boots_to_portal) {
    // The rule end to end: an incomplete record must raise the portal, not a
    // join attempt that can never succeed.
    Record r;
    clear(r);
    setField(r.ssid, kMaxSsid, "test-ssid");

    Machine m;
    ASSERT_EQ(State::Portal, m.onBoot(complete(r)));
}

// --- sanitize ----------------------------------------------------------------

TEST(provision_sanitize_clean_record_returns_false) {
    Record r = makeComplete();
    ASSERT_FALSE(sanitize(r));
    ASSERT_TRUE(complete(r));
    ASSERT_STREQ("test-ssid", r.ssid);
    ASSERT_EQ(kDefaultPort, r.port);
}

TEST(provision_sanitize_unterminated_buffer_is_terminated) {
    // A truncated NVS blob: the whole ssid buffer, terminator byte included, is
    // non-NUL.  Reading this as a C string would run into pass.
    Record r = makeComplete();
    for (size_t i = 0; i <= kMaxSsid; ++i) {
        r.ssid[i] = 'A';
    }

    ASSERT_TRUE(sanitize(r));
    ASSERT_EQ('\0', r.ssid[kMaxSsid]);
    ASSERT_EQ(kMaxSsid, std::strlen(r.ssid));
}

TEST(provision_sanitize_no_nul_in_any_field) {
    // Every field unterminated at once — the shape of a wholly corrupt blob.
    Record r;
    std::memset(&r, 0xAB, sizeof(r));

    ASSERT_TRUE(sanitize(r));
    ASSERT_EQ(kMaxSsid, std::strlen(r.ssid));
    ASSERT_EQ(kMaxPass, std::strlen(r.pass));
    ASSERT_EQ(kMaxHost, std::strlen(r.host));
    ASSERT_EQ(kMaxToken, std::strlen(r.token));
    ASSERT_EQ(kMaxOtaPass, std::strlen(r.otaPass));
    // 0xABAB is a legal port, so it survives; the point is that nothing is 0.
    ASSERT_TRUE(r.port != 0);
}

TEST(provision_sanitize_exact_capacity_string_is_untouched) {
    // kMaxSsid characters terminated at index kMaxSsid is legal, not corrupt.
    Record r = makeComplete();
    for (size_t i = 0; i < kMaxSsid; ++i) {
        r.ssid[i] = 'A';
    }
    r.ssid[kMaxSsid] = '\0';

    ASSERT_FALSE(sanitize(r));
    ASSERT_EQ(kMaxSsid, std::strlen(r.ssid));
}

TEST(provision_sanitize_strips_embedded_control_character) {
    Record r = makeComplete();
    setField(r.ssid, kMaxSsid, "home\001net");

    ASSERT_TRUE(sanitize(r));
    ASSERT_STREQ("home", r.ssid);  // cut at the first bad byte
}

TEST(provision_sanitize_rejects_del_and_newline) {
    Record r = makeComplete();
    setField(r.host, kMaxHost, "mac\177local");
    setField(r.token, kMaxToken, "tok\nen");

    ASSERT_TRUE(sanitize(r));
    ASSERT_STREQ("mac", r.host);
    ASSERT_STREQ("tok", r.token);
}

TEST(provision_sanitize_control_char_first_empties_the_field) {
    // A field that starts with corruption empties out, and an empty ssid means
    // unprovisioned — the device goes to the portal instead of to the radio.
    Record r = makeComplete();
    r.ssid[0] = 0x03;

    ASSERT_TRUE(sanitize(r));
    ASSERT_EQ('\0', r.ssid[0]);
    ASSERT_FALSE(complete(r));
}

TEST(provision_sanitize_keeps_high_bytes) {
    // Non-ASCII is legal in an SSID (UTF-8); only control bytes are corruption.
    Record r = makeComplete();
    setField(r.ssid, kMaxSsid, "caf\xc3\xa9-wifi");

    ASSERT_FALSE(sanitize(r));
    ASSERT_STREQ("caf\xc3\xa9-wifi", r.ssid);
}

TEST(provision_sanitize_port_zero_falls_back_to_default) {
    Record r = makeComplete();
    r.port = 0;

    ASSERT_TRUE(sanitize(r));
    ASSERT_EQ(kDefaultPort, r.port);
}

TEST(provision_sanitize_keeps_a_custom_port) {
    Record r = makeComplete();
    r.port = 9090;

    ASSERT_FALSE(sanitize(r));
    ASSERT_EQ((uint16_t)9090, r.port);
}

TEST(provision_sanitize_is_idempotent) {
    // Once repaired, a second pass has nothing left to correct.
    Record r = makeComplete();
    setField(r.ssid, kMaxSsid, "home\001net");
    r.port = 0;
    for (size_t i = 0; i <= kMaxToken; ++i) {
        r.token[i] = 'T';
    }

    ASSERT_TRUE(sanitize(r));
    ASSERT_FALSE(sanitize(r));
    ASSERT_STREQ("home", r.ssid);
    ASSERT_EQ(kDefaultPort, r.port);
    ASSERT_EQ(kMaxToken, std::strlen(r.token));
}

TEST(provision_sanitize_repairs_every_field_in_one_pass) {
    // Corruption in one field must not stop the others being checked: the
    // postconditions are promised for the whole record.
    Record r = makeComplete();
    setField(r.ssid, kMaxSsid, "a\001b");
    setField(r.pass, kMaxPass, "c\002d");
    setField(r.host, kMaxHost, "e\003f");
    setField(r.token, kMaxToken, "g\004h");
    setField(r.otaPass, kMaxOtaPass, "i\005j");

    ASSERT_TRUE(sanitize(r));
    ASSERT_STREQ("a", r.ssid);
    ASSERT_STREQ("c", r.pass);
    ASSERT_STREQ("e", r.host);
    ASSERT_STREQ("g", r.token);
    ASSERT_STREQ("i", r.otaPass);
}

// --- passphraseUnusable -------------------------------------------------------

TEST(provision_passphrase_empty_is_open_network) {
    Record r = makeComplete();
    r.pass[0] = '\0';
    ASSERT_FALSE(passphraseUnusable(r));
}

TEST(provision_passphrase_one_to_seven_is_unusable) {
    Record r = makeComplete();
    const char* tooShort[] = {"1", "12", "123", "1234", "12345", "123456", "1234567"};
    for (size_t i = 0; i < sizeof(tooShort) / sizeof(tooShort[0]); ++i) {
        setField(r.pass, kMaxPass, tooShort[i]);
        ASSERT_TRUE(passphraseUnusable(r));
    }
}

TEST(provision_passphrase_eight_is_usable) {
    Record r = makeComplete();
    setField(r.pass, kMaxPass, "12345678");  // exactly kMinWpaPass
    ASSERT_FALSE(passphraseUnusable(r));
}

TEST(provision_passphrase_full_capacity_is_usable) {
    Record r = makeComplete();
    for (size_t i = 0; i < kMaxPass; ++i) {
        r.pass[i] = 'p';
    }
    r.pass[kMaxPass] = '\0';
    ASSERT_FALSE(passphraseUnusable(r));
}

TEST(provision_passphrase_unterminated_is_usable_not_short) {
    // An unterminated buffer must be measured against the capacity, not walked
    // off the end; 63 bytes of passphrase is long, not unusable.
    Record r = makeComplete();
    for (size_t i = 0; i <= kMaxPass; ++i) {
        r.pass[i] = 'p';
    }
    ASSERT_FALSE(passphraseUnusable(r));
}

TEST(provision_passphrase_short_record_is_still_complete) {
    // "This join will fail" is not "unprovisioned": the two verdicts are
    // deliberately separate.
    Record r = makeComplete();
    setField(r.pass, kMaxPass, "short");
    ASSERT_TRUE(passphraseUnusable(r));
    ASSERT_TRUE(complete(r));
}

// --- state machine: transitions ----------------------------------------------

TEST(provision_machine_starts_unprovisioned) {
    Machine m;
    ASSERT_EQ(State::Unprovisioned, m.state());
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
}

TEST(provision_boot_incomplete_goes_to_portal) {
    Machine m;
    ASSERT_EQ(State::Portal, m.onBoot(false));
    ASSERT_EQ(State::Portal, m.state());
}

TEST(provision_boot_complete_goes_to_joining) {
    Machine m;
    ASSERT_EQ(State::Joining, m.onBoot(true));
    ASSERT_EQ(State::Joining, m.state());
}

TEST(provision_join_ok_goes_online) {
    Machine m;
    m.onBoot(true);
    ASSERT_EQ(State::Online, m.onJoinResult(true));
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
}

TEST(provision_join_fail_goes_to_retry_and_counts) {
    Machine m;
    m.onBoot(true);
    ASSERT_EQ(State::Retry, m.onJoinResult(false));
    ASSERT_EQ(1u, (unsigned)m.consecutiveFailures());
}

TEST(provision_retry_elapsed_returns_to_joining) {
    Machine m;
    m.onBoot(true);
    m.onJoinResult(false);
    ASSERT_EQ(State::Joining, m.onRetryElapsed());
    // The counter is not cleared by a retry — only a success clears it.
    ASSERT_EQ(1u, (unsigned)m.consecutiveFailures());
}

TEST(provision_retry_elapsed_is_noop_outside_retry) {
    Machine m;
    ASSERT_EQ(State::Unprovisioned, m.onRetryElapsed());

    m.onBoot(true);
    ASSERT_EQ(State::Joining, m.onRetryElapsed());

    m.onJoinResult(true);
    ASSERT_EQ(State::Online, m.onRetryElapsed());
}

TEST(provision_n_consecutive_failures_reach_portal) {
    // The edge the whole machine exists for.
    Machine m;
    m.onBoot(true);

    for (uint8_t i = 1; i < Machine::kMaxJoinFailures; ++i) {
        ASSERT_EQ(State::Retry, m.onJoinResult(false));
        ASSERT_EQ((unsigned)i, (unsigned)m.consecutiveFailures());
        ASSERT_EQ(State::Joining, m.onRetryElapsed());
    }

    ASSERT_EQ(State::Portal, m.onJoinResult(false));  // the Nth failure
    ASSERT_EQ((unsigned)Machine::kMaxJoinFailures, (unsigned)m.consecutiveFailures());
}

TEST(provision_portal_stays_portal_on_further_failures) {
    Machine m;
    m.onBoot(true);
    failJoins(m, Machine::kMaxJoinFailures);
    ASSERT_EQ(State::Portal, m.state());

    // A retry timer that keeps firing must not walk the device out of the portal.
    ASSERT_EQ(State::Portal, m.onRetryElapsed());
    ASSERT_EQ(State::Portal, m.onJoinResult(false));
}

TEST(provision_success_midway_resets_the_failure_counter) {
    Machine m;
    m.onBoot(true);

    // One short of the portal.
    failJoins(m, Machine::kMaxJoinFailures - 1);
    ASSERT_EQ(State::Joining, m.state());
    ASSERT_EQ((unsigned)(Machine::kMaxJoinFailures - 1), (unsigned)m.consecutiveFailures());

    ASSERT_EQ(State::Online, m.onJoinResult(true));
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());

    // It now takes N FRESH failures to reach the portal again, not one.
    m.onConnectionLost();
    failJoins(m, Machine::kMaxJoinFailures - 1);
    ASSERT_EQ(State::Joining, m.state());
    ASSERT_EQ(State::Portal, m.onJoinResult(false));
}

TEST(provision_connection_lost_returns_to_joining_without_counting) {
    Machine m;
    m.onBoot(true);
    m.onJoinResult(true);
    ASSERT_EQ(State::Online, m.state());

    ASSERT_EQ(State::Joining, m.onConnectionLost());
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());

    // A flapping network can drop many times; none of them are evidence the
    // credentials are wrong, so none of them push the device to the portal.
    for (int i = 0; i < 20; ++i) {
        m.onJoinResult(true);
        ASSERT_EQ(State::Joining, m.onConnectionLost());
    }
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
    ASSERT_EQ(State::Joining, m.state());
}

TEST(provision_connection_lost_outside_online_is_noop) {
    // The radio's disconnect event also fires during a failed join and while
    // the portal's SoftAP is up; neither may be treated as a lost connection.
    Machine m;
    m.onBoot(false);
    ASSERT_EQ(State::Portal, m.onConnectionLost());

    Machine m2;
    m2.onBoot(true);
    m2.onJoinResult(false);
    ASSERT_EQ(State::Retry, m2.onConnectionLost());
    ASSERT_EQ(1u, (unsigned)m2.consecutiveFailures());
}

TEST(provision_portal_saved_incomplete_keeps_portal_up) {
    Machine m;
    m.onBoot(false);
    ASSERT_EQ(State::Portal, m.onPortalSaved(false));
    ASSERT_EQ(State::Portal, m.state());
}

TEST(provision_portal_saved_complete_joins_with_a_clean_counter) {
    Machine m;
    m.onBoot(true);
    failJoins(m, Machine::kMaxJoinFailures);
    ASSERT_EQ(State::Portal, m.state());
    ASSERT_TRUE(m.consecutiveFailures() > 0);

    ASSERT_EQ(State::Joining, m.onPortalSaved(true));
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
}

TEST(provision_boot_after_portal_clears_the_counter) {
    Machine m;
    m.onBoot(true);
    failJoins(m, Machine::kMaxJoinFailures);
    ASSERT_EQ(State::Portal, m.state());

    ASSERT_EQ(State::Joining, m.onBoot(true));
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
}

TEST(provision_reset_returns_to_cold_boot) {
    Machine m;
    m.onBoot(true);
    m.onJoinResult(false);
    m.reset();

    ASSERT_EQ(State::Unprovisioned, m.state());
    ASSERT_EQ(0u, (unsigned)m.consecutiveFailures());
}

TEST(provision_custom_max_failures_is_honoured) {
    Machine m(2);
    m.onBoot(true);
    ASSERT_EQ(State::Retry, m.onJoinResult(false));
    m.onRetryElapsed();
    ASSERT_EQ(State::Portal, m.onJoinResult(false));
}

// --- state machine: backoff ---------------------------------------------------

TEST(provision_backoff_doubles_per_failure) {
    Machine m;
    m.onBoot(true);

    m.onJoinResult(false);
    ASSERT_EQ(Machine::kBaseBackoffMs, m.backoffMs());   // 1000
    m.onRetryElapsed();

    m.onJoinResult(false);
    ASSERT_EQ(2u * Machine::kBaseBackoffMs, m.backoffMs());   // 2000
    m.onRetryElapsed();

    m.onJoinResult(false);
    ASSERT_EQ(4u * Machine::kBaseBackoffMs, m.backoffMs());   // 4000
}

TEST(provision_backoff_before_any_failure_is_the_base) {
    Machine m;
    ASSERT_EQ(Machine::kBaseBackoffMs, m.backoffMs());
}

TEST(provision_backoff_resets_with_the_counter) {
    Machine m;
    m.onBoot(true);
    failJoins(m, 3);
    ASSERT_EQ(4u * Machine::kBaseBackoffMs, m.backoffMs());

    m.onJoinResult(true);
    ASSERT_EQ(Machine::kBaseBackoffMs, m.backoffMs());
}

TEST(provision_backoff_saturates_at_the_ceiling) {
    // A large N so the doubling runs past 60 s: 1,2,4,8,16,32,64 -> capped.
    Machine m(200);
    m.onBoot(true);

    failJoins(m, 6);
    ASSERT_EQ(32000u, m.backoffMs());

    failJoins(m, 1);
    ASSERT_EQ(Machine::kMaxBackoffMs, m.backoffMs());

    failJoins(m, 10);
    ASSERT_EQ(Machine::kMaxBackoffMs, m.backoffMs());
}

TEST(provision_backoff_never_overflows_at_a_saturated_counter) {
    // 255 is the counter ceiling; 2^254 ms would have wrapped a uint32 long ago.
    Machine m(255);
    m.onBoot(true);
    failJoins(m, 300);

    ASSERT_EQ(255u, (unsigned)m.consecutiveFailures());
    ASSERT_EQ(Machine::kMaxBackoffMs, m.backoffMs());
    ASSERT_TRUE(m.backoffMs() <= Machine::kMaxBackoffMs);
}
