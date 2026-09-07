// firmware/src/usage/bleprov.cpp — the BLE provisioning wire format and its
// decoder.  No M5/WiFi/BLE/Arduino headers; the HAL owns the GATT server.  See
// bleprov.h for the design and docs/BLE_PROVISIONING.md for the Go central's
// half of the contract.
//
// Nothing here formats, copies out or logs a credential VALUE.  Values move
// exactly one way — out of the stream buffer into rec_ — and leave only through
// record(), which the HAL hands straight to credsSave().
#include "usage/bleprov.h"

#include <cstring>

namespace usage {
namespace bleprov {

namespace {

// Little-endian readers.  The wire is little-endian everywhere — the length,
// the CRC, the port and the status counter — because one rule with no
// exceptions is the rule a second implementation in another language gets
// right.
uint16_t rd16(const uint8_t* p) {
    return (uint16_t)((uint16_t)p[0] | (uint16_t)((uint16_t)p[1] << 8));
}

uint32_t rd32(const uint8_t* p) {
    return (uint32_t)p[0] | ((uint32_t)p[1] << 8) | ((uint32_t)p[2] << 16) |
           ((uint32_t)p[3] << 24);
}

void wr16(uint8_t* p, uint16_t v) {
    p[0] = (uint8_t)(v & 0xFF);
    p[1] = (uint8_t)((v >> 8) & 0xFF);
}

// A byte a human could have typed, or any byte of a UTF-8 sequence.  Rejects
// exactly what provision::sanitize() cuts a field at — C0 controls and DEL —
// so a value that passes here is one sanitize() will not silently shorten.
//
// Bytes at or above 0x80 are ALLOWED: an SSID is UTF-8 and "Café" is a real
// network name.  An embedded NUL is a control character and is refused, which
// is also what stops a crafted value from truncating a field on copy.
bool printableValue(const uint8_t* v, size_t len) {
    for (size_t i = 0; i < len; ++i) {
        if (v[i] < 0x20 || v[i] == 0x7f) {
            return false;
        }
    }
    return true;
}

// Copy a TLV value into a fixed-size field and terminate it.  The caller has
// already proved len <= cap, so this cannot truncate.
void copyField(char* dst, const uint8_t* v, size_t len) {
    std::memcpy(dst, v, len);
    dst[len] = '\0';
}

} // namespace

// --- CRC ---------------------------------------------------------------------

// Bitwise rather than table-driven: 306 bytes at eight iterations each is
// ~2,400 operations once per provisioning attempt, against 1 KB of flash for a
// table that would be used exactly that once.
uint32_t crc32(const uint8_t* data, size_t len) {
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < len; ++i) {
        crc ^= (uint32_t)data[i];
        for (int b = 0; b < 8; ++b) {
            uint32_t mask = (uint32_t)0 - (crc & 1u);
            crc = (crc >> 1) ^ (0xEDB88320u & mask);
        }
    }
    return crc ^ 0xFFFFFFFFu;
}

// --- the decoder -------------------------------------------------------------

Decoder::Decoder() { reset(); }

void Decoder::reset() {
    std::memset(buf_, 0, sizeof(buf_));
    provision::clear(rec_);
    declared_ = 0;
    received_ = 0;
    state_    = State::Idle;
    error_    = Error::None;
    version_  = 0;
    nextSeq_  = 0;
}

// Wipe the secrets, keep the verdict.  The daemon is still holding the
// connection open waiting to read a result, and a status of Idle would read as
// "nothing ever happened here".
void Decoder::scrub() {
    std::memset(buf_, 0, sizeof(buf_));
    provision::clear(rec_);
    declared_ = 0;
    received_ = 0;
    nextSeq_  = 0;
}

// Keep the FIRST error.  A transfer that fails mid-stream keeps receiving the
// chunks already in flight, and each of those would otherwise overwrite the
// real reason with NotBegun before the daemon ever reads the status.
Error Decoder::fail(Error e) {
    if (state_ != State::Failed) {
        state_ = State::Failed;
        error_ = e;
    }
    // The partial stream is worthless and is a credential fragment: drop it
    // here rather than at the next BEGIN, which may never come.
    std::memset(buf_, 0, sizeof(buf_));
    received_ = 0;
    return e;
}

Error Decoder::onControl(const uint8_t* data, size_t len) {
    if (data == nullptr || len < 1) {
        return fail(Error::MalformedControl);
    }

    switch ((Op)data[0]) {
        case Op::Begin: {
            if (len != kBeginLen) {
                return fail(Error::MalformedControl);
            }
            // Version first: refusing before anything is buffered is the whole
            // reason BEGIN repeats the byte that also leads the stream.
            if (data[1] != kVersion) {
                return fail(Error::BadVersion);
            }
            const uint16_t total = rd16(data + 2);
            if (total < kMinStream) {
                return fail(Error::ShortStream);
            }
            if (total > kMaxStream) {
                return fail(Error::TooLarge);
            }
            // A BEGIN is the unconditional restart: it clears a previous
            // failure, which is how the daemon retries after any error.
            reset();
            version_  = data[1];
            declared_ = total;
            state_    = State::Receiving;
            return Error::None;
        }

        case Op::Commit: {
            if (len != kCommitLen) {
                return fail(Error::MalformedControl);
            }
            if (state_ != State::Receiving) {
                return fail(Error::NotBegun);
            }
            if (received_ != declared_) {
                return fail(Error::Truncated);
            }
            const Error e = parse();
            if (e != Error::None) {
                // parse() left rec_ half-filled; clear() before fail() so no
                // fragment of a passphrase survives a rejected commit.
                provision::clear(rec_);
                return fail(e);
            }
            state_ = State::Ready;
            error_ = Error::None;
            return Error::None;
        }

        case Op::Abort: {
            if (len != kAbortLen) {
                return fail(Error::MalformedControl);
            }
            // Always succeeds, from any state: it is the daemon's way of
            // leaving the device clean when the owner closes the dialog.
            reset();
            return Error::None;
        }
    }

    return fail(Error::UnknownOp);
}

Error Decoder::onChunk(const uint8_t* data, size_t len) {
    if (state_ != State::Receiving) {
        return fail(Error::NotBegun);
    }
    // A sequence byte and at least one data byte.  An empty chunk carries no
    // information and would advance the sequence for free, so it is refused
    // rather than tolerated.
    if (data == nullptr || len < 2) {
        return fail(Error::MalformedChunk);
    }
    if (data[0] != nextSeq_) {
        return fail(Error::OutOfOrder);
    }

    const size_t n = len - 1;
    if (received_ + n > declared_) {
        return fail(Error::Overflow);
    }

    // declared_ was bounded by kMaxStream at BEGIN and received_ + n <=
    // declared_ was just proved, so this copy is in range by construction.
    std::memcpy(buf_ + received_, data + 1, n);
    received_ += n;
    ++nextSeq_;  // uint8_t: wraps modulo 256, which is the documented rule
    return Error::None;
}

void Decoder::markApplying() {
    if (state_ == State::Ready) {
        state_ = State::Applying;
    }
}

void Decoder::markApplied() {
    if (state_ == State::Ready || state_ == State::Applying) {
        state_ = State::Applied;
        error_ = Error::None;
    }
}

// The HAL's outcome is authoritative and overwrites whatever was there: unlike
// fail(), this is not a late echo of an earlier problem, it is the answer to
// "did the credentials actually work", which is the only answer the owner
// cares about.
void Decoder::markFailed(Error e) {
    state_ = State::Failed;
    error_ = e;
}

// --- parsing -----------------------------------------------------------------

Error Decoder::parse() {
    // A fresh record every time: a second transfer must never inherit a field
    // from the first one, and clear() also installs kDefaultPort, which is what
    // a stream carrying no PORT record is asking for.
    provision::clear(rec_);

    if (buf_[0] != version_) {
        return Error::BadVersion;
    }

    const size_t payloadLen = rd16(buf_ + 1);
    if (kStreamHeader + payloadLen + kCrcLen != declared_) {
        return Error::LengthMismatch;
    }

    const size_t crcAt = kStreamHeader + payloadLen;
    if (rd32(buf_ + crcAt) != crc32(buf_, crcAt)) {
        return Error::BadCrc;
    }

    bool seen[7] = {false, false, false, false, false, false, false};
    bool sawSsid = false;

    size_t p = kStreamHeader;
    while (p < crcAt) {
        if (p + 2 > crcAt) {
            return Error::MalformedTlv;
        }
        const uint8_t  tag = buf_[p];
        const size_t   len = buf_[p + 1];
        const uint8_t* v   = buf_ + p + 2;
        if (p + 2 + len > crcAt) {
            return Error::MalformedTlv;
        }

        if (tag >= kOptionalTag) {
            // The forward-compatibility escape hatch: a field a future daemon
            // marked as safe to ignore.
            p += 2 + len;
            continue;
        }
        if (tag == 0 || tag > (uint8_t)Tag::OtaPass) {
            return Error::UnknownField;
        }
        if (seen[tag]) {
            return Error::DuplicateField;
        }
        seen[tag] = true;

        // Every mandatory-range value is text the owner or the daemon typed,
        // so the control-character rule applies to all of them uniformly —
        // including PORT, whose two bytes are checked as a number below and
        // must therefore be exempted.
        if ((Tag)tag != Tag::Port && !printableValue(v, len)) {
            return Error::ControlChar;
        }

        switch ((Tag)tag) {
            case Tag::Ssid:
                if (len == 0) {
                    return Error::FieldEmpty;
                }
                if (len > provision::kMaxSsid) {
                    return Error::FieldTooLong;
                }
                copyField(rec_.ssid, v, len);
                sawSsid = true;
                break;

            case Tag::Pass:
                if (len > provision::kMaxPass) {
                    return Error::FieldTooLong;
                }
                // Empty is an OPEN network and is legal.  1..7 is a passphrase
                // the radio refuses outright, so storing it would guarantee a
                // join failure with no explanation — the same rule
                // portal::validate() applies to a typed one.
                if (len > 0 && len < provision::kMinWpaPass) {
                    return Error::PassTooShort;
                }
                copyField(rec_.pass, v, len);
                break;

            case Tag::Host:
                if (len > provision::kMaxHost) {
                    return Error::FieldTooLong;
                }
                copyField(rec_.host, v, len);
                break;

            case Tag::Port: {
                if (len != 2) {
                    return Error::BadPort;
                }
                const uint16_t port = rd16(v);
                if (port == 0) {
                    return Error::BadPort;
                }
                rec_.port = port;
                break;
            }

            case Tag::Token:
                if (len > provision::kMaxToken) {
                    return Error::FieldTooLong;
                }
                copyField(rec_.token, v, len);
                break;

            case Tag::OtaPass:
                if (len > provision::kMaxOtaPass) {
                    return Error::FieldTooLong;
                }
                copyField(rec_.otaPass, v, len);
                break;
        }

        p += 2 + len;
    }

    if (!sawSsid) {
        return Error::MissingSsid;
    }

    // The same two calls the portal makes, in the same order and for the same
    // reason: sanitize() is the last line of defence on bytes that came off a
    // wire, and joinable() is the one rule that decides whether a record is
    // worth handing to the radio at all.
    provision::sanitize(rec_);
    if (!provision::joinable(rec_)) {
        return Error::NotJoinable;
    }
    return Error::None;
}

// --- the readable characteristics --------------------------------------------

size_t encodeStatus(const Decoder& d, uint8_t* out, size_t n) {
    if (out == nullptr || n < kStatusLen) {
        return 0;
    }
    out[0] = kVersion;
    out[1] = (uint8_t)d.state();
    out[2] = (uint8_t)d.error();
    out[3] = d.nextSeq();
    // Bounded by kMaxStream, so the cast can never lose a byte of the count.
    wr16(out + 4, (uint16_t)d.received());
    return kStatusLen;
}

size_t encodeInfo(const uint8_t mac[6], bool provisioned, uint8_t* out, size_t n) {
    if (out == nullptr || n < kInfoLen) {
        return 0;
    }
    out[0] = kVersion;
    out[1] = provisioned ? kFlagProvisioned : 0;
    wr16(out + 2, (uint16_t)kMaxStream);
    if (mac != nullptr) {
        std::memcpy(out + 4, mac, 6);
    } else {
        std::memset(out + 4, 0, 6);
    }
    out[10] = 0;  // reserved, must be sent as zero and ignored on read
    out[11] = 0;
    return kInfoLen;
}

// --- fixed sentences ---------------------------------------------------------
//
// Short enough for a 240 px line at text size 1, and containing no submitted
// byte by construction.

const char* errorText(Error e) {
    switch (e) {
        case Error::None:             return "ok";
        case Error::MalformedControl: return "bad command length";
        case Error::UnknownOp:        return "unknown command";
        case Error::BadVersion:       return "unsupported format version";
        case Error::ShortStream:      return "declared size too small";
        case Error::TooLarge:         return "too big for this device";
        case Error::NotBegun:         return "no transfer in progress";
        case Error::MalformedChunk:   return "empty or malformed chunk";
        case Error::OutOfOrder:       return "chunk out of order";
        case Error::Overflow:         return "more data than declared";
        case Error::Truncated:        return "transfer incomplete";
        case Error::LengthMismatch:   return "length does not match";
        case Error::BadCrc:           return "checksum mismatch";
        case Error::MalformedTlv:     return "malformed field";
        case Error::UnknownField:     return "unknown field";
        case Error::DuplicateField:   return "duplicate field";
        case Error::FieldEmpty:       return "required field is empty";
        case Error::FieldTooLong:     return "field too long";
        case Error::ControlChar:      return "illegal character in a field";
        case Error::PassTooShort:     return "Wi-Fi password too short";
        case Error::BadPort:          return "invalid port";
        case Error::MissingSsid:      return "no network name";
        case Error::NotJoinable:      return "nothing usable to join";
        case Error::SaveFailed:       return "could not save credentials";
        case Error::JoinFailed:       return "could not join that network";
    }
    return "unknown error";
}

const char* stateText(State s) {
    switch (s) {
        case State::Idle:      return "waiting";
        case State::Receiving: return "receiving";
        case State::Ready:     return "received";
        case State::Applying:  return "joining";
        case State::Applied:   return "done";
        case State::Failed:    return "failed";
    }
    return "unknown";
}

// --- the passkey -------------------------------------------------------------

bool formatPasskey(uint32_t passkey, char* out, size_t n) {
    if (out == nullptr || n < kPasskeyLen + 1) {
        return false;
    }
    // BLE passkeys are 000000..999999; anything else is a stack bug upstream,
    // and showing a seventh digit would be worse than showing a wrong one
    // because the central's dialog only accepts six.
    uint32_t v = passkey % 1000000u;
    for (size_t i = kPasskeyLen; i > 0; --i) {
        out[i - 1] = (char)('0' + (v % 10u));
        v /= 10u;
    }
    out[kPasskeyLen] = '\0';
    return true;
}

} // namespace bleprov
} // namespace usage
