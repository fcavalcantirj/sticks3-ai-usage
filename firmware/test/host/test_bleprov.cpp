// firmware/test/host/test_bleprov.cpp — the TRANSPORT half of the BLE
// provisioning tests: the checksum, the control commands, the chunk stream and
// the readable characteristics.  Field and tag rules live next door in
// test_bleprov_payload.cpp; the daemon-side encoder both files drive is in
// bleprov_fixture.h.
//
// The rules under test are the ones that decide whether a stranger's device
// gets configured or bricked by a half-written record: a stream is accepted
// only whole, only checksummed, and only in order.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include <cstring>

#include "bleprov_fixture.h"
#include "framework.h"
#include "usage/bleprov.h"
#include "usage/provision.h"

using bleprov_fixture::Payload;
using bleprov_fixture::addStr;
using bleprov_fixture::addTlv;
using bleprov_fixture::buildFull;
using bleprov_fixture::frame;
using bleprov_fixture::payloadInit;
using bleprov_fixture::sendAbort;
using bleprov_fixture::sendBegin;
using bleprov_fixture::sendChunks;
using bleprov_fixture::sendCommit;
using bleprov_fixture::transfer;
using bleprov_fixture::transferPayload;

using usage::bleprov::Decoder;
using usage::bleprov::Error;
using usage::bleprov::Op;
using usage::bleprov::State;
using usage::bleprov::Tag;
using usage::bleprov::crc32;
using usage::bleprov::encodeInfo;
using usage::bleprov::encodeStatus;
using usage::bleprov::errorText;
using usage::bleprov::formatPasskey;
using usage::bleprov::kInfoLen;
using usage::bleprov::kMaxStream;
using usage::bleprov::kMinStream;
using usage::bleprov::kOptionalTag;
using usage::bleprov::kSafeChunkData;
using usage::bleprov::kStatusLen;
using usage::bleprov::kStreamHeader;
using usage::bleprov::kVersion;
using usage::bleprov::stateText;

// --- the checksum both languages must agree on -------------------------------

// The single easiest thing to get wrong across a C++ device and a Go daemon.
// 0xCBF43926 is the published check value for CRC-32/ISO-HDLC, which is what
// Go's hash/crc32.ChecksumIEEE computes.
TEST(bleprov_crc32_matches_the_ieee_check_vector) {
    const uint8_t v[] = "123456789";
    ASSERT_EQ(crc32(v, 9), (uint32_t)0xCBF43926u);
}

TEST(bleprov_crc32_of_nothing_is_zero) {
    ASSERT_EQ(crc32(nullptr, 0), (uint32_t)0u);
}

TEST(bleprov_crc32_changes_on_a_single_bit_flip) {
    const uint8_t a[] = {1, 2, 3, 4, 5, 6, 7, 8};
    const uint8_t b[] = {1, 2, 3, 4, 5, 6, 7, 9};
    ASSERT_TRUE(crc32(a, sizeof(a)) != crc32(b, sizeof(b)));
}

// --- the clean round trip ----------------------------------------------------

TEST(bleprov_round_trip_at_mtu_23) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(d.state(), State::Idle);
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);

    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_EQ(d.error(), Error::None);
    ASSERT_EQ(d.received(), total);
    ASSERT_EQ(d.declared(), total);

    const usage::provision::Record& r = d.record();
    ASSERT_STREQ(r.ssid, "test-network");
    ASSERT_STREQ(r.pass, "test-passphrase");
    ASSERT_STREQ(r.host, "usaged.local");
    ASSERT_EQ(r.port, (uint16_t)8765);
    ASSERT_STREQ(r.token, "test-token-0123456789abcdef");
    ASSERT_STREQ(r.otaPass, "test-ota-pass");
    ASSERT_TRUE(usage::provision::complete(r));
}

// A negotiated 517-byte MTU lets the daemon push the whole record in one write.
// The protocol must not care.
TEST(bleprov_round_trip_in_a_single_chunk) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kMaxStream), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_STREQ(d.record().ssid, "test-network");
}

// Chunk boundaries must not change the outcome, including sizes that leave a
// one-byte final chunk and sizes that divide the stream exactly.
TEST(bleprov_round_trip_at_every_chunk_size) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    for (size_t chunk = 1; chunk <= total; ++chunk) {
        Decoder d;
        ASSERT_EQ(transfer(d, stream, total, chunk), Error::None);
        ASSERT_EQ(d.state(), State::Ready);
        ASSERT_STREQ(d.record().ssid, "test-network");
    }
}

// One byte per chunk over a 300-byte stream crosses the 256-chunk boundary, so
// the documented "sequence wraps modulo 256" rule is exercised for real.
TEST(bleprov_sequence_number_wraps_past_255) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    // Pad the stream past 256 bytes with ignorable optional fields.
    uint8_t filler[200];
    std::memset(filler, 'x', sizeof(filler));
    addTlv(p, kOptionalTag, filler, sizeof(filler));
    addTlv(p, (uint8_t)(kOptionalTag + 1), filler, 60);

    uint8_t      stream[kMaxStream + 64];
    const size_t total = frame(p, kVersion, stream);
    ASSERT_TRUE(total > 256);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, 1), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_STREQ(d.record().ssid, "test-network");
}

// Every field at its provision.h capacity — the worst case the doc's 306-byte
// figure and the 512-byte buffer are sized against.
TEST(bleprov_largest_legal_record_fits_and_round_trips) {
    char ssid[usage::provision::kMaxSsid + 1];
    char pass[usage::provision::kMaxPass + 1];
    char host[usage::provision::kMaxHost + 1];
    char token[usage::provision::kMaxToken + 1];
    char ota[usage::provision::kMaxOtaPass + 1];
    std::memset(ssid, 'a', usage::provision::kMaxSsid);
    std::memset(pass, 'b', usage::provision::kMaxPass);
    std::memset(host, 'c', usage::provision::kMaxHost);
    std::memset(token, 'd', usage::provision::kMaxToken);
    std::memset(ota, 'e', usage::provision::kMaxOtaPass);
    ssid[usage::provision::kMaxSsid]     = '\0';
    pass[usage::provision::kMaxPass]     = '\0';
    host[usage::provision::kMaxHost]     = '\0';
    token[usage::provision::kMaxToken]   = '\0';
    ota[usage::provision::kMaxOtaPass]   = '\0';

    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, ssid);
    addStr(p, Tag::Pass, pass);
    addStr(p, Tag::Host, host);
    bleprov_fixture::addPort(p, 65535);
    addStr(p, Tag::Token, token);
    addStr(p, Tag::OtaPass, ota);

    uint8_t      stream[kMaxStream + 64];
    const size_t total = frame(p, kVersion, stream);
    ASSERT_EQ(total, (size_t)306);  // the number quoted in bleprov.h and the doc
    ASSERT_TRUE(total <= kMaxStream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_STREQ(d.record().ssid, ssid);
    ASSERT_STREQ(d.record().pass, pass);
    ASSERT_STREQ(d.record().otaPass, ota);
    ASSERT_EQ(d.record().port, (uint16_t)65535);
}

// --- transfer failures -------------------------------------------------------

TEST(bleprov_truncated_stream_is_refused_at_commit) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    // Everything but the last four bytes.
    ASSERT_EQ(sendChunks(d, stream, total - 4, kSafeChunkData), Error::None);
    ASSERT_EQ(d.state(), State::Receiving);
    ASSERT_EQ(sendCommit(d), Error::Truncated);
    ASSERT_EQ(d.state(), State::Failed);
    ASSERT_EQ(d.error(), Error::Truncated);
}

TEST(bleprov_corrupted_checksum_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);
    stream[total - 1] ^= 0x01;  // flip a bit of the CRC itself

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::BadCrc);
    ASSERT_EQ(d.state(), State::Failed);
    ASSERT_EQ(d.error(), Error::BadCrc);
}

TEST(bleprov_corrupted_payload_is_caught_by_the_checksum) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);
    stream[kStreamHeader + 5] ^= 0x20;  // a byte inside the SSID value

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::BadCrc);
}

TEST(bleprov_out_of_order_chunk_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);

    uint8_t w[1 + kSafeChunkData];
    w[0] = 0;
    std::memcpy(w + 1, stream, kSafeChunkData);
    ASSERT_EQ(d.onChunk(w, 1 + kSafeChunkData), Error::None);
    ASSERT_EQ(d.nextSeq(), (uint8_t)1);

    // Chunk 2 arrives where chunk 1 was expected.
    w[0] = 2;
    std::memcpy(w + 1, stream + 2 * kSafeChunkData, kSafeChunkData);
    ASSERT_EQ(d.onChunk(w, 1 + kSafeChunkData), Error::OutOfOrder);
    ASSERT_EQ(d.state(), State::Failed);
    ASSERT_EQ(d.error(), Error::OutOfOrder);
}

// A repeat of the chunk just accepted is an out-of-order chunk too: the device
// deliberately does not try to be clever about retransmits, it restarts.
TEST(bleprov_repeated_chunk_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    uint8_t w[1 + kSafeChunkData];
    w[0] = 0;
    std::memcpy(w + 1, stream, kSafeChunkData);
    ASSERT_EQ(d.onChunk(w, 1 + kSafeChunkData), Error::None);
    ASSERT_EQ(d.onChunk(w, 1 + kSafeChunkData), Error::OutOfOrder);
}

// The recovery path the daemon actually takes after any failure: BEGIN again.
TEST(bleprov_begin_recovers_from_a_failed_transfer) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    const uint8_t bad[2] = {7, 0x00};
    ASSERT_EQ(d.onChunk(bad, sizeof(bad)), Error::OutOfOrder);
    ASSERT_EQ(d.state(), State::Failed);

    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_EQ(d.error(), Error::None);
    ASSERT_STREQ(d.record().ssid, "test-network");
}

// A late chunk from the transfer that already failed must not overwrite the
// reason it failed — that reason is what the daemon shows the owner.
TEST(bleprov_first_error_survives_later_noise) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    uint8_t w[2] = {9, 0x41};
    ASSERT_EQ(d.onChunk(w, sizeof(w)), Error::OutOfOrder);
    // Two more writes arrive from the central's queue.
    w[0] = 1;
    ASSERT_EQ(d.onChunk(w, sizeof(w)), Error::NotBegun);
    ASSERT_EQ(d.error(), Error::OutOfOrder);
    ASSERT_EQ(sendCommit(d), Error::NotBegun);
    ASSERT_EQ(d.error(), Error::OutOfOrder);
}

TEST(bleprov_chunk_without_begin_is_refused) {
    Decoder       d;
    const uint8_t w[4] = {0, 1, 2, 3};
    ASSERT_EQ(d.onChunk(w, sizeof(w)), Error::NotBegun);
    ASSERT_EQ(d.state(), State::Failed);
}

TEST(bleprov_commit_without_begin_is_refused) {
    Decoder d;
    ASSERT_EQ(sendCommit(d), Error::NotBegun);
    ASSERT_EQ(d.state(), State::Failed);
}

TEST(bleprov_empty_chunk_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    const uint8_t seqOnly[1] = {0};
    ASSERT_EQ(d.onChunk(seqOnly, 1), Error::MalformedChunk);
    ASSERT_EQ(d.state(), State::Failed);
}

TEST(bleprov_zero_length_chunk_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    const uint8_t none[1] = {0};
    ASSERT_EQ(d.onChunk(none, 0), Error::MalformedChunk);

    // A null pointer is the same refusal — but only while a transfer is in
    // flight.  "No transfer in progress" is checked FIRST and is the more
    // useful diagnosis, so the second decoder starts a fresh one.
    Decoder d2;
    ASSERT_EQ(sendBegin(d2, kVersion, (uint16_t)total), Error::None);
    ASSERT_EQ(d2.onChunk(nullptr, 4), Error::MalformedChunk);
}

// The state check runs before the shape check: a malformed chunk arriving when
// nothing is in flight reports NotBegun, which is what the daemon needs to
// know.
TEST(bleprov_chunk_state_is_checked_before_its_shape) {
    Decoder d;
    ASSERT_EQ(d.onChunk(nullptr, 0), Error::NotBegun);
}

TEST(bleprov_more_data_than_declared_is_refused) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    // Declare a shorter stream than we then try to send.
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)(total - 10)), Error::None);
    ASSERT_EQ(sendChunks(d, stream, total, kSafeChunkData), Error::Overflow);
    ASSERT_EQ(d.error(), Error::Overflow);
}

// --- BEGIN validation --------------------------------------------------------

TEST(bleprov_unsupported_version_is_refused_before_any_bytes) {
    Decoder d;
    ASSERT_EQ(sendBegin(d, (uint8_t)(kVersion + 1), 64), Error::BadVersion);
    ASSERT_EQ(d.state(), State::Failed);
    ASSERT_EQ(d.received(), (size_t)0);
}

// BEGIN and the stream header carry the same byte and must agree; a daemon that
// gets that wrong is confused about its own format.
TEST(bleprov_version_mismatch_between_begin_and_stream_is_refused) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    uint8_t      stream[kMaxStream + 64];
    const size_t total = frame(p, (uint8_t)(kVersion + 7), stream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::BadVersion);
}

TEST(bleprov_declared_size_below_the_minimum_is_refused) {
    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)(kMinStream - 1)),
              Error::ShortStream);
}

TEST(bleprov_declared_size_above_capacity_is_refused) {
    Decoder d;
    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)(kMaxStream + 1)),
              Error::TooLarge);
    ASSERT_EQ(d.state(), State::Failed);
}

// A stream whose header disagrees with what BEGIN declared, with a CRC that is
// nonetheless valid: only the explicit length cross-check catches this.
TEST(bleprov_header_length_must_match_the_declared_total) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    uint8_t      stream[kMaxStream + 64];
    const size_t total = frame(p, kVersion, stream);

    // Shorten the header's payload length and re-checksum, so the CRC passes
    // and the mismatch is the only thing left to find.
    stream[1] -= 2;
    const size_t crcAt = kStreamHeader + stream[1];
    const uint32_t c   = crc32(stream, crcAt);
    stream[crcAt + 0]  = (uint8_t)(c & 0xFF);
    stream[crcAt + 1]  = (uint8_t)((c >> 8) & 0xFF);
    stream[crcAt + 2]  = (uint8_t)((c >> 16) & 0xFF);
    stream[crcAt + 3]  = (uint8_t)((c >> 24) & 0xFF);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::LengthMismatch);
}

TEST(bleprov_begin_with_a_wrong_length_is_refused) {
    Decoder       d;
    const uint8_t shortCmd[3] = {(uint8_t)Op::Begin, kVersion, 0x40};
    ASSERT_EQ(d.onControl(shortCmd, sizeof(shortCmd)), Error::MalformedControl);

    Decoder       d2;
    const uint8_t longCmd[5] = {(uint8_t)Op::Begin, kVersion, 0x40, 0x00, 0x00};
    ASSERT_EQ(d2.onControl(longCmd, sizeof(longCmd)), Error::MalformedControl);
}

TEST(bleprov_commit_with_a_wrong_length_is_refused) {
    Decoder       d;
    const uint8_t cmd[2] = {(uint8_t)Op::Commit, 0};
    ASSERT_EQ(d.onControl(cmd, sizeof(cmd)), Error::MalformedControl);
}

TEST(bleprov_unknown_opcode_is_refused) {
    Decoder       d;
    const uint8_t cmd[1] = {0x7f};
    ASSERT_EQ(d.onControl(cmd, sizeof(cmd)), Error::UnknownOp);
}

TEST(bleprov_empty_control_write_is_refused) {
    Decoder       d;
    const uint8_t cmd[1] = {0};
    ASSERT_EQ(d.onControl(cmd, 0), Error::MalformedControl);
    ASSERT_EQ(d.onControl(nullptr, 4), Error::MalformedControl);
}

// --- ABORT, scrub and the HAL outcome hooks ----------------------------------

TEST(bleprov_abort_returns_to_idle_from_any_state) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);
    ASSERT_EQ(d.state(), State::Ready);
    ASSERT_EQ(sendAbort(d), Error::None);
    ASSERT_EQ(d.state(), State::Idle);
    ASSERT_EQ(d.error(), Error::None);
    ASSERT_EQ(d.received(), (size_t)0);
    ASSERT_STREQ(d.record().ssid, "");
    ASSERT_STREQ(d.record().pass, "");

    // And from Failed.
    Decoder d2;
    ASSERT_EQ(sendCommit(d2), Error::NotBegun);
    ASSERT_EQ(sendAbort(d2), Error::None);
    ASSERT_EQ(d2.state(), State::Idle);
}

TEST(bleprov_abort_with_a_wrong_length_is_refused) {
    Decoder       d;
    const uint8_t cmd[2] = {(uint8_t)Op::Abort, 0};
    ASSERT_EQ(d.onControl(cmd, sizeof(cmd)), Error::MalformedControl);
}

TEST(bleprov_hal_reports_the_outcome) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);
    d.markApplying();
    ASSERT_EQ(d.state(), State::Applying);
    d.markApplied();
    ASSERT_EQ(d.state(), State::Applied);
    ASSERT_EQ(d.error(), Error::None);

    Decoder d2;
    ASSERT_EQ(transfer(d2, stream, total, kSafeChunkData), Error::None);
    d2.markApplying();
    d2.markFailed(Error::JoinFailed);
    ASSERT_EQ(d2.state(), State::Failed);
    ASSERT_EQ(d2.error(), Error::JoinFailed);
}

// The HAL's verdict is authoritative and must overwrite an earlier one — unlike
// fail(), which keeps the first error.
TEST(bleprov_hal_failure_overwrites_an_earlier_error) {
    Decoder d;
    ASSERT_EQ(sendCommit(d), Error::NotBegun);
    d.markFailed(Error::SaveFailed);
    ASSERT_EQ(d.error(), Error::SaveFailed);
}

// The HAL cannot promote a transfer that never completed: markApplying and
// markApplied are acknowledgements of real work, not a way to fake a result.
TEST(bleprov_hal_hooks_do_not_promote_an_idle_decoder) {
    Decoder d;
    d.markApplying();
    ASSERT_EQ(d.state(), State::Idle);
    d.markApplied();
    ASSERT_EQ(d.state(), State::Idle);
}

// Once the record is persisted it has no business staying in RAM, but the
// daemon is still waiting to read the outcome.
TEST(bleprov_scrub_wipes_the_secrets_and_keeps_the_verdict) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    ASSERT_EQ(transfer(d, stream, total, kSafeChunkData), Error::None);
    d.markApplied();
    d.scrub();
    ASSERT_EQ(d.state(), State::Applied);
    ASSERT_EQ(d.error(), Error::None);
    ASSERT_STREQ(d.record().ssid, "");
    ASSERT_STREQ(d.record().pass, "");
    ASSERT_STREQ(d.record().token, "");
    ASSERT_EQ(d.received(), (size_t)0);
}

// A rejected commit must not leave half a passphrase behind either.
TEST(bleprov_rejected_commit_leaves_no_fragment) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addStr(p, Tag::Pass, "test-passphrase");
    addStr(p, Tag::Host, "usaged.local");
    const uint8_t v[2] = {0, 0};
    addTlv(p, 0x07, v, sizeof(v));  // unknown mandatory tag, at the very end

    Decoder d;
    ASSERT_EQ(transferPayload(d, p), Error::UnknownField);
    ASSERT_STREQ(d.record().ssid, "");
    ASSERT_STREQ(d.record().pass, "");
    ASSERT_EQ(d.received(), (size_t)0);
}

// --- the readable characteristics --------------------------------------------

TEST(bleprov_status_encodes_the_whole_machine) {
    uint8_t      stream[kMaxStream];
    const size_t total = buildFull(stream);

    Decoder d;
    uint8_t st[kStatusLen];
    ASSERT_EQ(encodeStatus(d, st, sizeof(st)), kStatusLen);
    ASSERT_EQ(st[0], kVersion);
    ASSERT_EQ(st[1], (uint8_t)State::Idle);
    ASSERT_EQ(st[2], (uint8_t)Error::None);
    ASSERT_EQ(st[3], (uint8_t)0);
    ASSERT_EQ(st[4], (uint8_t)0);
    ASSERT_EQ(st[5], (uint8_t)0);

    ASSERT_EQ(sendBegin(d, kVersion, (uint16_t)total), Error::None);
    ASSERT_EQ(sendChunks(d, stream, kSafeChunkData * 2, kSafeChunkData),
              Error::None);
    ASSERT_EQ(encodeStatus(d, st, sizeof(st)), kStatusLen);
    ASSERT_EQ(st[1], (uint8_t)State::Receiving);
    ASSERT_EQ(st[3], (uint8_t)2);
    const uint16_t got = (uint16_t)(st[4] | (st[5] << 8));
    ASSERT_EQ(got, (uint16_t)(kSafeChunkData * 2));

    // And the failure the daemon reads off the same six bytes.
    ASSERT_EQ(sendCommit(d), Error::Truncated);
    ASSERT_EQ(encodeStatus(d, st, sizeof(st)), kStatusLen);
    ASSERT_EQ(st[1], (uint8_t)State::Failed);
    ASSERT_EQ(st[2], (uint8_t)Error::Truncated);
}

TEST(bleprov_status_refuses_a_short_buffer) {
    Decoder d;
    uint8_t st[kStatusLen];
    ASSERT_EQ(encodeStatus(d, st, kStatusLen - 1), (size_t)0);
    ASSERT_EQ(encodeStatus(d, nullptr, kStatusLen), (size_t)0);
}

TEST(bleprov_info_carries_the_version_capacity_and_mac) {
    const uint8_t mac[6] = {0x24, 0x0A, 0xC4, 0x11, 0xD5, 0x34};
    uint8_t       info[kInfoLen];
    ASSERT_EQ(encodeInfo(mac, false, info, sizeof(info)), kInfoLen);
    ASSERT_EQ(info[0], kVersion);
    ASSERT_EQ(info[1], (uint8_t)0);
    const uint16_t cap = (uint16_t)(info[2] | (info[3] << 8));
    ASSERT_EQ(cap, (uint16_t)kMaxStream);
    ASSERT_EQ(std::memcmp(info + 4, mac, 6), 0);
    ASSERT_EQ(info[10], (uint8_t)0);
    ASSERT_EQ(info[11], (uint8_t)0);

    ASSERT_EQ(encodeInfo(mac, true, info, sizeof(info)), kInfoLen);
    ASSERT_EQ(info[1], usage::bleprov::kFlagProvisioned);

    ASSERT_EQ(encodeInfo(mac, true, info, kInfoLen - 1), (size_t)0);
}

// --- the passkey -------------------------------------------------------------

TEST(bleprov_passkey_is_zero_padded_to_six_digits) {
    char out[16];
    ASSERT_TRUE(formatPasskey(42, out, sizeof(out)));
    ASSERT_STREQ(out, "000042");
    ASSERT_TRUE(formatPasskey(0, out, sizeof(out)));
    ASSERT_STREQ(out, "000000");
    ASSERT_TRUE(formatPasskey(123456, out, sizeof(out)));
    ASSERT_STREQ(out, "123456");
    ASSERT_TRUE(formatPasskey(999999, out, sizeof(out)));
    ASSERT_STREQ(out, "999999");
}

TEST(bleprov_passkey_never_shows_a_seventh_digit) {
    char out[16];
    ASSERT_TRUE(formatPasskey(1234567, out, sizeof(out)));
    ASSERT_EQ(std::strlen(out), (size_t)6);
}

TEST(bleprov_passkey_refuses_a_short_buffer) {
    char out[8];
    ASSERT_FALSE(formatPasskey(42, out, 6));
    ASSERT_FALSE(formatPasskey(42, nullptr, 16));
}

// --- the fixed sentences -----------------------------------------------------

// Every code the daemon can be handed must map to something a human can read,
// and none of them may be empty — a blank line on the screen is worse than a
// number.
TEST(bleprov_every_error_has_a_sentence) {
    for (unsigned i = 0; i <= (unsigned)Error::JoinFailed; ++i) {
        const char* t = errorText((Error)i);
        ASSERT_TRUE(t != nullptr);
        ASSERT_TRUE(t[0] != '\0');
        // Short enough for a 240 px line at text size 1 (~40 characters).
        ASSERT_TRUE(std::strlen(t) <= 40);
    }
}

TEST(bleprov_every_state_has_a_word) {
    for (unsigned i = 0; i <= (unsigned)State::Failed; ++i) {
        const char* t = stateText((State)i);
        ASSERT_TRUE(t != nullptr);
        ASSERT_TRUE(t[0] != '\0');
    }
}
