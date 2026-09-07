// firmware/src/usage/bleprov.h — the PURE half of BLE provisioning: the wire
// format the Mac daemon writes and the decoder that turns it into a
// usage::provision::Record.
//
// No M5, WiFi, BLE or Arduino headers: same discipline as provision.{h,cpp}
// and portal.{h,cpp}, so every rule below is host-tested by `make fw-test`.
// The HAL (hal/sticks3/bleprov.*) owns the GATT server, the pairing dialogue
// and the screen; this module owns the BYTES and every verdict on them.
//
// THE GOAL, IN THE OWNER'S WORDS: "once we pair the sticks3 and mac via
// bluetooth, the mac daemon sends EVERYTHING — SSID and password, daemon IP,
// port. It knows it all."  The user types nothing.  A device flashed from
// M5Burner boots unprovisioned, advertises, and waits to be told.
//
// See docs/BLE_PROVISIONING.md for the GATT shape, the UUIDs and the Go
// central's side of the contract.  This header is the normative description of
// the PAYLOAD; the doc restates it for an implementer who is not reading C++.
//
// FOUR RULES THAT SHAPE EVERY FUNCTION HERE
//
//  1. NOTHING HERE FORMATS, RENDERS OR RETURNS A CREDENTIAL VALUE.  errorText()
//     and stateText() return fixed sentences that can never contain submitted
//     bytes, exactly like portal::rejectText().  The only way a value leaves
//     this module is record(), which the HAL hands straight to credsSave().
//
//  2. THE STORABILITY RULE LIVES IN provision.h, NOT HERE.  A committed stream
//     is accepted only when the assembled record satisfies provision::joinable()
//     after provision::sanitize() — the SAME rule the boot path and the portal
//     apply — so a transfer the daemon believes succeeded can never read back
//     as nothing to try.
//
//  3. CORRECT AT MTU 23.  A BLE central that never negotiates an MTU can write
//     20 bytes per ATT operation, which is 19 bytes of stream after the
//     sequence byte.  Nothing in this protocol requires a larger write; chunk
//     size is the daemon's choice and the device only ever checks that the
//     total does not exceed what BEGIN declared.
//
//  4. NO HEAP, NO EXCEPTIONS, NO <string>.  A Decoder is ~550 bytes of plain
//     storage that a HAL object owns as a member.  Only <cstdint>, <cstddef>
//     and <cstring> are used.
#pragma once

#include <cstddef>
#include <cstdint>

#include "usage/provision.h"

namespace usage {
namespace bleprov {

// --- the format version ------------------------------------------------------
//
// The stream's LEADING BYTE is the version, and BEGIN repeats it so an
// unsupported version is refused before a single byte is buffered.  A future
// daemon that changes the encoding bumps this and an old device answers
// Error::BadVersion instead of misparsing.
inline constexpr uint8_t kVersion = 1;

// --- sizes -------------------------------------------------------------------

// Stream layout:  [version:1][payloadLen:2 LE][payload][crc32:4 LE]
inline constexpr size_t kStreamHeader = 3;
inline constexpr size_t kCrcLen       = 4;
inline constexpr size_t kMinStream    = kStreamHeader + kCrcLen;  // 7

// The buffer a Decoder carries.  The largest LEGAL stream is 306 bytes — every
// field at its provision.h capacity, plus the header and the CRC — so 512
// leaves room for a future daemon to append optional tags (see kOptionalTag)
// without a firmware change, and still costs half a kilobyte of RAM.
inline constexpr size_t kMaxStream = 512;

// What a central that never negotiated an MTU can put in one chunk write:
// ATT_MTU 23 minus the 3-byte ATT_WRITE_REQ header is 20 bytes on the wire,
// minus the 1-byte sequence number.  Advisory — the device accepts any chunk
// size that keeps the running total within the declared length.
inline constexpr size_t kSafeChunkData = 19;

// --- the control characteristic ----------------------------------------------
//
// One byte of opcode, then fixed operands.  A control write whose length does
// not match its opcode is refused rather than guessed at.
//
//   BEGIN  [0x01][version:1][totalLen:2 LE]   reset and accept a stream
//   COMMIT [0x02]                             validate what arrived
//   ABORT  [0x03]                             discard everything, wipe the RAM
enum class Op : uint8_t {
    Begin  = 0x01,
    Commit = 0x02,
    Abort  = 0x03,
};

inline constexpr size_t kBeginLen  = 4;
inline constexpr size_t kCommitLen = 1;
inline constexpr size_t kAbortLen  = 1;

// --- the payload -------------------------------------------------------------
//
// A flat sequence of TLV records: [tag:1][len:1][value:len].  Order is free and
// every field is optional except the SSID — the same asymmetry provision.h
// draws between joinable() and complete().
//
// TAGS BELOW kOptionalTag ARE MANDATORY TO UNDERSTAND.  An unknown one is
// Error::UnknownField, because a daemon that sends a field this firmware has
// never heard of is describing a device configuration this firmware cannot
// honour, and silently ignoring it would strand the user with a device that
// reports success and behaves wrongly.  Tags at or above kOptionalTag are
// SKIPPED, which is the forward-compatibility escape hatch: a future daemon
// puts genuinely optional additions there and old firmware keeps working.
enum class Tag : uint8_t {
    Ssid    = 0x01,  // 1..kMaxSsid    bytes, required
    Pass    = 0x02,  // 0..kMaxPass    bytes, empty = OPEN network (legal)
    Host    = 0x03,  // 0..kMaxHost    bytes, empty = discover over mDNS
    Port    = 0x04,  // exactly 2 bytes, little-endian, non-zero
    Token   = 0x05,  // 0..kMaxToken   bytes, empty = get one by pairing
    OtaPass = 0x06,  // 0..kMaxOtaPass bytes, empty = OTA stays disarmed
};

inline constexpr uint8_t kOptionalTag = 0x80;

// --- state -------------------------------------------------------------------
//
// Idle ──BEGIN──► Receiving ──chunks──► Receiving ──COMMIT(ok)──► Ready
//                     │                                             │
//                     └──any error──► Failed                        │
//                                                       HAL: markApplying()
//                                                                   ▼
//                                                              Applying
//                                                          ok ↙        ↘ fail
//                                                     Applied          Failed
//
// The three tail states are set BY THE HAL, not reached by parsing: only the
// hardware knows whether the record actually saved and joined, and the daemon
// needs to hear that outcome rather than "the bytes were well formed".
enum class State : uint8_t {
    Idle      = 0,  // nothing in flight
    Receiving = 1,  // BEGIN accepted; chunks are arriving
    Ready     = 2,  // COMMIT validated; record() is populated
    Applying  = 3,  // the HAL is saving and joining
    Applied   = 4,  // stored AND joined — provisioning succeeded
    Failed    = 5,  // see error(); a fresh BEGIN is required to retry
};

// Every way a transfer can be refused.  Precise on purpose: the daemon shows
// these to the owner, and "it didn't work" is not a diagnosis.  Values are
// stable across firmware versions — the Go side switches on the number.
enum class Error : uint8_t {
    None = 0,

    // control-characteristic framing
    MalformedControl = 1,   // write length does not match the opcode
    UnknownOp        = 2,

    // BEGIN
    BadVersion       = 3,   // unsupported version, or BEGIN and stream disagree
    ShortStream      = 4,   // declared total below kMinStream
    TooLarge         = 5,   // declared total above kMaxStream

    // chunk framing
    NotBegun         = 6,   // a chunk or COMMIT with no BEGIN in force
    MalformedChunk   = 7,   // shorter than a sequence byte plus one data byte
    OutOfOrder       = 8,   // sequence number is not the expected one
    Overflow         = 9,   // this chunk would exceed the declared total

    // COMMIT
    Truncated        = 10,  // fewer bytes arrived than BEGIN declared
    LengthMismatch   = 11,  // header payload length disagrees with the total
    BadCrc           = 12,

    // payload
    MalformedTlv     = 13,  // a record runs past the end of the payload
    UnknownField     = 14,  // a mandatory-range tag this firmware does not know
    DuplicateField   = 15,
    FieldEmpty       = 16,  // a field that must carry bytes arrived empty
    FieldTooLong     = 17,  // longer than the provision.h capacity
    ControlChar      = 18,  // a byte below 0x20, or 0x7f, inside a value
    PassTooShort     = 19,  // 1..7 bytes: the radio refuses it outright
    BadPort          = 20,  // not two bytes, or zero
    MissingSsid      = 21,  // no SSID record at all
    NotJoinable      = 22,  // survived every check and still failed joinable()

    // outcomes the HAL reports back through this module
    SaveFailed       = 23,  // credsSave() refused
    JoinFailed       = 24,  // the credentials did not join the network
};

// --- the decoder -------------------------------------------------------------

class Decoder {
public:
    Decoder();

    // A write to the control characteristic.  Returns Error::None when the
    // command was accepted; anything else also lands in error() and moves the
    // machine to Failed, except ABORT, which always succeeds.
    Error onControl(const uint8_t* data, size_t len);

    // A write to the data characteristic: [seq:1][stream bytes:1..].  The
    // sequence number starts at 0 for the first chunk of a transfer and
    // increments by one per chunk, wrapping modulo 256.
    Error onChunk(const uint8_t* data, size_t len);

    State   state() const { return state_; }
    Error   error() const { return error_; }

    // The sequence number the next chunk must carry.  Published in the status
    // characteristic so a daemon that lost track can see where it stands
    // before deciding to start over.
    uint8_t nextSeq() const { return nextSeq_; }

    size_t received() const { return received_; }
    size_t declared() const { return declared_; }

    // Valid only in Ready, Applying and Applied — and only until scrub().
    const provision::Record& record() const { return rec_; }

    // The HAL reports the outcome of the save-and-join it performed on
    // record().  markApplying() is the acknowledgement that the work started,
    // so the daemon sees progress rather than a stalled Ready.
    void markApplying();
    void markApplied();
    void markFailed(Error e);

    // Zero the stream buffer and the record while KEEPING state() and error(),
    // so the daemon can still read the outcome.  Call it the moment the record
    // has been persisted: a provisioning secret has no business sitting in RAM
    // for the rest of the uptime.
    void scrub();

    // Back to Idle with everything zeroed, including state and error.
    void reset();

private:
    // Record an error without ever overwriting the FIRST one: a late chunk from
    // a transfer that already failed must not hide the reason it failed.
    Error fail(Error e);

    // Validate and parse the assembled stream into rec_.
    Error parse();

    uint8_t           buf_[kMaxStream];
    provision::Record rec_;
    size_t            declared_;
    size_t            received_;
    State             state_;
    Error             error_;
    uint8_t           version_;
    uint8_t           nextSeq_;
};

// --- shared primitives -------------------------------------------------------

// CRC-32/ISO-HDLC, the one Go's hash/crc32.ChecksumIEEE computes: reflected,
// polynomial 0xEDB88320, init 0xFFFFFFFF, final xor 0xFFFFFFFF.  Named
// explicitly because a checksum both halves must agree on is the single
// easiest thing to get subtly wrong across two languages; test_bleprov.cpp
// pins it with the standard "123456789" -> 0xCBF43926 vector.
uint32_t crc32(const uint8_t* data, size_t len);

// The status characteristic's value, 6 bytes:
//   [version:1][state:1][error:1][nextSeq:1][received:2 LE]
// Small enough to fit a notification at ATT_MTU 23 with room to spare.
// Returns the number of bytes written, or 0 if n is too small.
inline constexpr size_t kStatusLen = 6;
size_t encodeStatus(const Decoder& d, uint8_t* out, size_t n);

// The info characteristic's value, 12 bytes, readable before any pairing:
//   [version:1][flags:1][capacity:2 LE][mac:6][reserved:2]
// flags bit 0 = the device already holds a provisioned record.  Nothing here
// is a secret: the MAC is in every advertising packet already, and it is what
// lets the dashboard say WHICH StickS3 it found.
inline constexpr size_t kInfoLen      = 12;
inline constexpr uint8_t kFlagProvisioned = 0x01;
size_t encodeInfo(const uint8_t mac[6], bool provisioned, uint8_t* out, size_t n);

// Fixed sentences, safe to print on the serial line and safe to draw on the
// screen: neither can ever contain a byte the daemon sent.
const char* errorText(Error e);
const char* stateText(State s);

// The BLE passkey the pairing dialogue displays, zero-padded to six digits the
// way every phone and Mac shows it.  Pure because the screen must not invent a
// different rendering than the one the central prompts for.  Returns false and
// writes nothing when n is too small.
inline constexpr size_t kPasskeyLen = 6;
bool formatPasskey(uint32_t passkey, char* out, size_t n);

} // namespace bleprov
} // namespace usage
