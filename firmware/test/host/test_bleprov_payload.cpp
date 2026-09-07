// firmware/test/host/test_bleprov_payload.cpp — the PAYLOAD half of the BLE
// provisioning tests: TLV framing, field capacities, tag rules and the port.
// The transport (control commands, chunks, checksum, status) is next door in
// test_bleprov.cpp; the daemon-side encoder both files drive is in
// bleprov_fixture.h.
//
// The rule these tests exist to protect: a stream is accepted only when what
// falls out of it is something the radio can actually try — the SAME
// provision::joinable() verdict the boot path and the captive portal use.  A
// transfer the daemon believes succeeded must never read back as nothing to
// try.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include <cstring>

#include "bleprov_fixture.h"
#include "framework.h"
#include "usage/bleprov.h"
#include "usage/provision.h"

using bleprov_fixture::Payload;
using bleprov_fixture::addPort;
using bleprov_fixture::addStr;
using bleprov_fixture::addTlv;
using bleprov_fixture::payloadInit;
using bleprov_fixture::transferPayload;

using usage::bleprov::Decoder;
using usage::bleprov::Error;
using usage::bleprov::State;
using usage::bleprov::Tag;
using usage::bleprov::kOptionalTag;

// --- open networks -----------------------------------------------------------

// An empty passphrase is a legal value that means "open network", not a
// missing field.  Refusing it would make a whole class of network
// unprovisionable over BLE.
TEST(bleprov_open_network_has_an_empty_password) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-open-net");
    addStr(p, Tag::Pass, "");
    addStr(p, Tag::Host, "usaged.local");
    addStr(p, Tag::Token, "test-token");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_STREQ(d.record().ssid, "test-open-net");
    ASSERT_STREQ(d.record().pass, "");
    ASSERT_TRUE(usage::provision::joinable(d.record()));
    ASSERT_FALSE(usage::provision::passphraseUnusable(d.record()));
}

// Omitting the field entirely is the same thing as sending it empty.
TEST(bleprov_open_network_may_omit_the_password_field) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-open-net");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_STREQ(d.record().pass, "");
    ASSERT_TRUE(usage::provision::joinable(d.record()));
}

// SSID alone is joinable; host and token arrive later by discovery and pairing.
// The BLE flow normally sends everything, but the protocol must not require it
// — demanding a token here would put a 32-character string back in front of the
// stranger this work exists to spare.
TEST(bleprov_ssid_alone_is_accepted_and_not_complete) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_TRUE(usage::provision::joinable(d.record()));
    ASSERT_FALSE(usage::provision::complete(d.record()));
    // A stream with no PORT record asks for the default, not for port 0.
    ASSERT_EQ(d.record().port, usage::provision::kDefaultPort);
}

// --- field capacities --------------------------------------------------------

TEST(bleprov_oversized_ssid_is_refused) {
    char ssid[usage::provision::kMaxSsid + 2];
    std::memset(ssid, 'a', usage::provision::kMaxSsid + 1);
    ssid[usage::provision::kMaxSsid + 1] = '\0';

    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, ssid);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::FieldTooLong);
    ASSERT_EQ(d.state(), State::Failed);
}

TEST(bleprov_oversized_password_is_refused) {
    char pass[usage::provision::kMaxPass + 2];
    std::memset(pass, 'b', usage::provision::kMaxPass + 1);
    pass[usage::provision::kMaxPass + 1] = '\0';

    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addStr(p, Tag::Pass, pass);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::FieldTooLong);
}

TEST(bleprov_oversized_host_token_and_ota_are_refused) {
    char big[128];

    {
        std::memset(big, 'z', sizeof(big));
        big[usage::provision::kMaxHost + 1] = '\0';
        Payload p;
        payloadInit(p);
        addStr(p, Tag::Ssid, "test-network");
        addStr(p, Tag::Host, big);
        Decoder d;
        ASSERT_EQ(transferPayload(d, p), Error::FieldTooLong);
    }
    {
        std::memset(big, 'z', sizeof(big));
        big[usage::provision::kMaxToken + 1] = '\0';
        Payload p;
        payloadInit(p);
        addStr(p, Tag::Ssid, "test-network");
        addStr(p, Tag::Token, big);
        Decoder d;
        ASSERT_EQ(transferPayload(d, p), Error::FieldTooLong);
    }
    {
        std::memset(big, 'z', sizeof(big));
        big[usage::provision::kMaxOtaPass + 1] = '\0';
        Payload p;
        payloadInit(p);
        addStr(p, Tag::Ssid, "test-network");
        addStr(p, Tag::OtaPass, big);
        Decoder d;
        ASSERT_EQ(transferPayload(d, p), Error::FieldTooLong);
    }
}

// The boundary itself must be ACCEPTED, or the capacities in the doc are a lie.
TEST(bleprov_fields_exactly_at_capacity_are_accepted) {
    char ssid[usage::provision::kMaxSsid + 1];
    std::memset(ssid, 'a', usage::provision::kMaxSsid);
    ssid[usage::provision::kMaxSsid] = '\0';

    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, ssid);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_STREQ(d.record().ssid, ssid);
    ASSERT_EQ(std::strlen(d.record().ssid), usage::provision::kMaxSsid);
}

// --- the passphrase rule -----------------------------------------------------

// 1..7 characters is a passphrase the radio refuses outright, so storing it
// would guarantee a join failure the owner cannot diagnose.  The same rule
// portal::validate() applies to a typed one.
TEST(bleprov_short_password_is_refused) {
    for (size_t n = 1; n < usage::provision::kMinWpaPass; ++n) {
        char pass[16];
        std::memset(pass, 'p', n);
        pass[n] = '\0';

        Payload p;
        payloadInit(p);
        addStr(p, Tag::Ssid, "test-network");
        addStr(p, Tag::Pass, pass);

        Decoder d;
        ASSERT_EQ(transferPayload(d, p), Error::PassTooShort);
    }
}

TEST(bleprov_eight_character_password_is_accepted) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addStr(p, Tag::Pass, "12345678");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_STREQ(d.record().pass, "12345678");
    ASSERT_FALSE(usage::provision::passphraseUnusable(d.record()));
}

// --- the required field ------------------------------------------------------

TEST(bleprov_missing_ssid_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Pass, "test-passphrase");
    addStr(p, Tag::Host, "usaged.local");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::MissingSsid);
}

TEST(bleprov_empty_ssid_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::FieldEmpty);
}

TEST(bleprov_empty_payload_is_refused) {
    Payload p;
    payloadInit(p);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::MissingSsid);
}

// --- tags --------------------------------------------------------------------

// Last-wins would make the meaning of a stream depend on the order it happened
// to be written in.
TEST(bleprov_duplicate_field_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addStr(p, Tag::Ssid, "test-other-network");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::DuplicateField);
}

// A daemon sending a field this firmware has never heard of is describing a
// configuration the firmware cannot honour; ignoring it would report success
// and behave wrongly.
TEST(bleprov_unknown_mandatory_tag_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    const uint8_t v[2] = {0x11, 0x22};
    addTlv(p, 0x07, v, sizeof(v));  // below kOptionalTag: must not be ignored

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::UnknownField);
}

TEST(bleprov_zero_tag_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    const uint8_t v[1] = {0};
    addTlv(p, 0x00, v, sizeof(v));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::UnknownField);
}

// The forward-compatibility hatch: a future daemon may append fields at or
// above kOptionalTag and this firmware keeps provisioning.
TEST(bleprov_optional_tags_are_skipped) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    const uint8_t v[5] = {1, 2, 3, 4, 5};
    addTlv(p, kOptionalTag, v, sizeof(v));
    addTlv(p, 0xFF, v, 0);
    addStr(p, Tag::Host, "usaged.local");

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_STREQ(d.record().ssid, "test-network");
    ASSERT_STREQ(d.record().host, "usaged.local");
}

// An optional tag may carry bytes that would be illegal in a real field: it is
// skipped without being inspected at all.
TEST(bleprov_optional_tags_are_not_validated) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    const uint8_t raw[4] = {0x00, 0x01, 0x7f, 0x1f};
    addTlv(p, kOptionalTag, raw, sizeof(raw));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
}

// --- TLV framing -------------------------------------------------------------

TEST(bleprov_truncated_tlv_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    // A record that claims 40 bytes but only 2 follow.
    p.b[p.n++] = (uint8_t)Tag::Host;
    p.b[p.n++] = 40;
    p.b[p.n++] = 'a';
    p.b[p.n++] = 'b';

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::MalformedTlv);
}

TEST(bleprov_dangling_tag_byte_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    p.b[p.n++] = (uint8_t)Tag::Host;  // a tag with no length byte after it

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::MalformedTlv);
}

// --- value bytes -------------------------------------------------------------

// A control character cannot survive into NVS: provision::sanitize() would cut
// the field at it, so a passphrase containing one would be silently stored as a
// different, shorter passphrase.
TEST(bleprov_control_character_in_a_value_is_refused) {
    const uint8_t bad[] = {'a', 'b', 0x00, 'c', 'd', 'e', 'f', 'g', 'h'};
    Payload       p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addTlv(p, (uint8_t)Tag::Pass, bad, sizeof(bad));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::ControlChar);
}

TEST(bleprov_del_character_in_a_value_is_refused) {
    const uint8_t bad[] = {'a', 0x7f, 'c'};
    Payload       p;
    payloadInit(p);
    addTlv(p, (uint8_t)Tag::Ssid, bad, sizeof(bad));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::ControlChar);
}

// UTF-8 SSIDs are real: "Café" must go through untouched, so the rule is
// "no C0 controls and no DEL", never "ASCII only".
TEST(bleprov_high_bytes_are_accepted_so_utf8_ssids_work) {
    const uint8_t utf8[] = {'C', 'a', 'f', 0xC3, 0xA9};  // "Café"
    Payload       p;
    payloadInit(p);
    addTlv(p, (uint8_t)Tag::Ssid, utf8, sizeof(utf8));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_EQ(std::memcmp(d.record().ssid, utf8, sizeof(utf8)), 0);
    ASSERT_EQ(d.record().ssid[sizeof(utf8)], '\0');
}

// --- the port ----------------------------------------------------------------

TEST(bleprov_port_must_be_two_bytes) {
    const uint8_t one[1] = {0x3d};
    Payload       p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addTlv(p, (uint8_t)Tag::Port, one, sizeof(one));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::BadPort);
}

TEST(bleprov_port_zero_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addPort(p, 0);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::BadPort);
}

TEST(bleprov_port_is_little_endian) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    const uint8_t le[2] = {0x3D, 0x22};  // 0x223D = 8765
    addTlv(p, (uint8_t)Tag::Port, le, sizeof(le));

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_EQ(d.record().port, (uint16_t)8765);
}

// The port is the one numeric field, so the control-character rule must NOT be
// applied to it: 0x0A 0x00 is port 10 and is perfectly legal.
TEST(bleprov_port_bytes_are_not_subject_to_the_text_rule) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addPort(p, 10);

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::None);
    ASSERT_EQ(d.record().port, (uint16_t)10);
}
