// firmware/src/usage/provision.cpp — the credential-record rules and the
// provisioning state machine.  No M5/WiFi/Arduino headers: the HAL owns NVS,
// this module owns the rules.  See provision.h for the design.
//
// Nothing here logs, formats or copies a credential value: sanitize() repairs
// bytes in place, and every other entry point answers with a bool or a State.
#include "usage/provision.h"

#include <cstring>

namespace usage {
namespace provision {

namespace {

// Length of a fixed-size field, bounded by its capacity so a blob that arrived
// from NVS with no terminator anywhere cannot run off the end.  cap EXCLUDES
// the terminator, so the buffer is cap + 1 bytes and index cap is readable.
size_t fieldLen(const char* buf, size_t cap) {
    size_t len = 0;
    while (len < cap && buf[len] != '\0') {
        ++len;
    }
    return len;
}

// Validate and repair one field in place.  Returns true if anything had to be
// corrected, the same contract sanitize() gives its caller.
bool sanitizeField(char* buf, size_t cap) {
    bool fixed = false;

    // A truncated or corrupt blob can arrive with no NUL at all.  fieldLen only
    // stops early on a terminator, so len == cap here means there was none:
    // force one at the last legal byte before anything reads this as a string.
    size_t len = fieldLen(buf, cap);
    if (buf[len] != '\0') {
        buf[cap] = '\0';
        fixed = true;
    }

    // Control characters cannot occur in an SSID, a hostname or a token that a
    // human typed into the portal, so one here is corruption.  Cut the field at
    // the first bad byte rather than handing it to the radio: a field that
    // empties out simply reads as missing, and a missing field sends the device
    // to the portal — the safe end of every failure in this module.
    for (size_t i = 0; i < len; ++i) {
        unsigned char c = (unsigned char)buf[i];
        if (c < 0x20 || c == 0x7f) {
            buf[i] = '\0';
            fixed = true;
            break;
        }
    }

    return fixed;
}

} // namespace

// --- the record --------------------------------------------------------------

void clear(Record& r) {
    std::memset(&r, 0, sizeof(r));
    r.port = kDefaultPort;
}

// Repair a record read from storage, the way loadRotation() repairs a stored
// rotation to 1 or 3.  Every field is repaired even after one has already been
// corrected: the postconditions are promised for the WHOLE record.
bool sanitize(Record& r) {
    bool fixed = false;

    if (sanitizeField(r.ssid, kMaxSsid))       fixed = true;
    if (sanitizeField(r.pass, kMaxPass))       fixed = true;
    if (sanitizeField(r.host, kMaxHost))       fixed = true;
    if (sanitizeField(r.token, kMaxToken))     fixed = true;
    if (sanitizeField(r.otaPass, kMaxOtaPass)) fixed = true;

    // Port 0 can never be dialled, exactly as rotation 0 can never be applied:
    // fall back to the default instead of failing every fetch forever.
    if (r.port == 0) {
        r.port = kDefaultPort;
        fixed = true;
    }

    return fixed;
}

// The completeness rule.  Only the first byte matters: an empty field is a
// missing field, whatever sanitize() left behind it.
bool complete(const Record& r) {
    return r.ssid[0] != '\0' && r.host[0] != '\0' && r.token[0] != '\0';
}

// Empty means an OPEN network and is legal; 1..7 characters is a passphrase the
// radio will refuse, which is a warning, not an unprovisioned verdict.
bool passphraseUnusable(const Record& r) {
    size_t len = fieldLen(r.pass, kMaxPass);
    return len > 0 && len < kMinWpaPass;
}

// --- the state machine -------------------------------------------------------

Machine::Machine(uint8_t maxJoinFailures)
    : state_(State::Unprovisioned),
      failures_(0),
      maxFailures_(maxJoinFailures) {}

uint32_t Machine::backoffMs() const {
    // One doubling per failure after the first: 1s, 2s, 4s, ... capped at 60s.
    // The ceiling is tested BEFORE the multiply, so no failure count — not even
    // a saturated 255 — can overflow the uint32.
    uint32_t ms = kBaseBackoffMs;
    for (uint8_t i = 1; i < failures_; ++i) {
        if (ms > kMaxBackoffMs / 2) {
            return kMaxBackoffMs;
        }
        ms *= 2;
    }
    return ms > kMaxBackoffMs ? kMaxBackoffMs : ms;
}

// Cold boot, or a return from the portal.  A boot always gets a fresh budget of
// attempts; on a cold boot the counter is already 0, and after the portal the
// credentials are new, so the old failures say nothing about them.
State Machine::onBoot(bool recordComplete) {
    failures_ = 0;
    state_ = recordComplete ? State::Joining : State::Portal;
    return state_;
}

State Machine::onJoinResult(bool ok) {
    if (ok) {
        // A single success proves the credentials: the run of failures that led
        // here is over, so the next fall to the portal needs a fresh N.
        failures_ = 0;
        state_ = State::Online;
        return state_;
    }

    // Saturating increment: a device parked in front of a dead router for days
    // must not wrap the counter and start trusting that router again.
    if (failures_ < 0xFF) {
        ++failures_;
    }

    // The edge this machine exists for: after N consecutive failures the device
    // stops retrying credentials that plainly do not work and raises the portal,
    // so a moved house or a changed password needs no USB cable.
    state_ = (failures_ >= maxFailures_) ? State::Portal : State::Retry;
    return state_;
}

State Machine::onRetryElapsed() {
    if (state_ == State::Retry) {
        state_ = State::Joining;
    }
    return state_;
}

State Machine::onPortalSaved(bool recordComplete) {
    if (!recordComplete) {
        // Half a configuration is worse than none: keep the portal up so the
        // user finishes, rather than dropping into a join that cannot succeed.
        state_ = State::Portal;
        return state_;
    }
    failures_ = 0;
    state_ = State::Joining;
    return state_;
}

// Only an ESTABLISHED connection can be lost.  The same radio disconnect event
// also fires during a failed join and while the portal's SoftAP is up, and
// acting on it there would either double-count a failure or tear the portal
// down mid-configuration, so anything but Online is a no-op.  Note what is
// missing: no failure is counted, because losing a working network is not
// evidence the credentials are wrong.
State Machine::onConnectionLost() {
    if (state_ == State::Online) {
        state_ = State::Joining;
    }
    return state_;
}

void Machine::reset() {
    state_ = State::Unprovisioned;
    failures_ = 0;
}

} // namespace provision
} // namespace usage
