// firmware/test/host/bleprov_fixture.h — the DAEMON's side of the BLE
// provisioning contract, written from docs/BLE_PROVISIONING.md.
//
// THIS IS DELIBERATELY A SECOND IMPLEMENTATION.  It builds streams the way the
// Go daemon will — from the document, not by calling a firmware encoder that
// does not exist — so a contract that cannot be implemented from its own
// description shows up as a failing test rather than as a daemon that never
// provisions anything.
//
// Shared by test_bleprov.cpp (framing and transport) and
// test_bleprov_payload.cpp (fields and tags).  Everything is inline so both
// translation units can include it.
//
// The strings the tests build with are obvious fakes: no real credential value
// ever appears in this repo.
#pragma once

#include <cstdint>
#include <cstring>

#include "usage/bleprov.h"

namespace bleprov_fixture {

using usage::bleprov::Decoder;
using usage::bleprov::Error;
using usage::bleprov::Op;
using usage::bleprov::Tag;
using usage::bleprov::crc32;
using usage::bleprov::kBeginLen;
using usage::bleprov::kCrcLen;
using usage::bleprov::kMaxStream;
using usage::bleprov::kSafeChunkData;
using usage::bleprov::kStreamHeader;
using usage::bleprov::kVersion;

// A payload under construction: the flat TLV region, before framing.
struct Payload {
    uint8_t b[700];
    size_t  n;
};

inline void payloadInit(Payload& p) {
    std::memset(&p, 0, sizeof(p));
}

inline void addTlv(Payload& p, uint8_t tag, const uint8_t* v, size_t len) {
    p.b[p.n++] = tag;
    p.b[p.n++] = (uint8_t)len;
    if (len > 0) {
        std::memcpy(p.b + p.n, v, len);
    }
    p.n += len;
}

inline void addStr(Payload& p, Tag tag, const char* s) {
    addTlv(p, (uint8_t)tag, (const uint8_t*)s, std::strlen(s));
}

inline void addPort(Payload& p, uint16_t port) {
    const uint8_t le[2] = {(uint8_t)(port & 0xFF), (uint8_t)((port >> 8) & 0xFF)};
    addTlv(p, (uint8_t)Tag::Port, le, 2);
}

// [version][payloadLen:2 LE][payload][crc32:4 LE], exactly as the doc states.
inline size_t frame(const Payload& p, uint8_t version, uint8_t* out) {
    out[0] = version;
    out[1] = (uint8_t)(p.n & 0xFF);
    out[2] = (uint8_t)((p.n >> 8) & 0xFF);
    std::memcpy(out + 3, p.b, p.n);
    const size_t   crcAt = kStreamHeader + p.n;
    const uint32_t c     = crc32(out, crcAt);
    out[crcAt + 0] = (uint8_t)(c & 0xFF);
    out[crcAt + 1] = (uint8_t)((c >> 8) & 0xFF);
    out[crcAt + 2] = (uint8_t)((c >> 16) & 0xFF);
    out[crcAt + 3] = (uint8_t)((c >> 24) & 0xFF);
    return crcAt + kCrcLen;
}

// A record a well-behaved daemon would send: everything filled in, because the
// Mac knows all of it.
inline size_t buildFull(uint8_t* out) {
    Payload p;
    payloadInit(p);
    addStr(p, Tag::Ssid, "test-network");
    addStr(p, Tag::Pass, "test-passphrase");
    addStr(p, Tag::Host, "usaged.local");
    addPort(p, 8765);
    addStr(p, Tag::Token, "test-token-0123456789abcdef");
    addStr(p, Tag::OtaPass, "test-ota-pass");
    return frame(p, kVersion, out);
}

// --- driving the decoder the way the GATT server will ------------------------

inline Error sendBegin(Decoder& d, uint8_t version, uint16_t total) {
    const uint8_t cmd[kBeginLen] = {(uint8_t)Op::Begin, version,
                                    (uint8_t)(total & 0xFF),
                                    (uint8_t)((total >> 8) & 0xFF)};
    return d.onControl(cmd, sizeof(cmd));
}

inline Error sendCommit(Decoder& d) {
    const uint8_t cmd[1] = {(uint8_t)Op::Commit};
    return d.onControl(cmd, sizeof(cmd));
}

inline Error sendAbort(Decoder& d) {
    const uint8_t cmd[1] = {(uint8_t)Op::Abort};
    return d.onControl(cmd, sizeof(cmd));
}

// Write the stream as chunks of at most chunkData bytes each, stopping at the
// first refusal.  Returns the last error seen.
inline Error sendChunks(Decoder& d, const uint8_t* stream, size_t total,
                        size_t chunkData) {
    uint8_t seq = 0;
    size_t  off = 0;
    while (off < total) {
        size_t n = total - off;
        if (n > chunkData) {
            n = chunkData;
        }
        uint8_t w[1 + kMaxStream];
        w[0] = seq++;
        std::memcpy(w + 1, stream + off, n);
        const Error e = d.onChunk(w, n + 1);
        if (e != Error::None) {
            return e;
        }
        off += n;
    }
    return Error::None;
}

// The whole transfer: BEGIN, chunks at the given size, COMMIT.
inline Error transfer(Decoder& d, const uint8_t* stream, size_t total,
                      size_t chunkData) {
    Error e = sendBegin(d, kVersion, (uint16_t)total);
    if (e != Error::None) {
        return e;
    }
    e = sendChunks(d, stream, total, chunkData);
    if (e != Error::None) {
        return e;
    }
    return sendCommit(d);
}

// Build a stream from a payload and push it through at MTU-23 chunk size.
inline Error transferPayload(Decoder& d, const Payload& p) {
    uint8_t      stream[kMaxStream + 64];
    const size_t total = frame(p, kVersion, stream);
    return transfer(d, stream, total, kSafeChunkData);
}

} // namespace bleprov_fixture
