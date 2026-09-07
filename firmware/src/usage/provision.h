// firmware/src/usage/provision.h — pure C++17 credential record and
// provisioning state machine for the StickS3 usage monitor.
//
// No M5, WiFi or Arduino headers: same discipline as hold_flip.{h,cpp} and
// freshness.{h,cpp}, so every rule below is host-tested by `make fw-test`.
// The HAL (hal/sticks3/creds.*) owns NVS; this module owns the RULES.
//
// WHY THIS EXISTS (task 76): secrets.h is a compile-time header, so the Wi-Fi
// SSID, Wi-Fi password, host, device token and OTA password all end up as
// plaintext strings inside firmware.bin — verified with `strings` against a
// real build.  The binary therefore cannot be published anywhere.  Credentials
// move to NVS; secrets.h becomes a DEVELOPER CONVENIENCE ONLY, a first-boot
// seed, never a runtime source.
//
// TWO RULES, NOT ONE, AND THE DIFFERENCE IS THE WHOLE POINT.
//
//   joinable(r)  — an SSID is present, so a join is worth ATTEMPTING.
//   complete(r)  — ssid + host + token, so the agent can also be REACHED and
//                  AUTHENTICATED to.
//
// complete() implies joinable(); the reverse does not hold, and the gap
// between them is where a freshly-set-up device lives.  A stranger types a
// Wi-Fi password and nothing else: the device joins on joinable(), discovers
// the agent over mDNS, shows a pairing code on its screen, is handed a token,
// and only THEN becomes complete().  Requiring complete() before a join would
// put an agent address and a 32-character token back in front of that stranger,
// which is exactly the failure this work exists to remove.
//
// What has not changed: an SSID-less record is still unprovisioned and must
// enter the portal rather than silently retrying a half-configuration forever
// — the failure that strands a device with no screen and no cable.
#pragma once

#include <cstddef>
#include <cstdint>

namespace usage {
namespace provision {

// Field capacities in bytes, excluding the NUL terminator.  These are not
// arbitrary: the radio itself caps SSID at 32 bytes, and the Arduino core
// copies a WPA2 passphrase into a 64-byte field WITHOUT terminating it when
// the source is 64 bytes or longer, so 63 is the longest safely-terminated
// passphrase.
inline constexpr size_t kMaxSsid    = 32;
inline constexpr size_t kMaxPass    = 63;
inline constexpr size_t kMaxHost    = 63;
inline constexpr size_t kMaxToken   = 64;
inline constexpr size_t kMaxOtaPass = 63;

// The agent's default port.  A stored port of 0 is invalid and falls back here,
// exactly as rotation falls back to 1 and brightness to index 2.
inline constexpr uint16_t kDefaultPort = 8765;

// A WPA2 passphrase shorter than 8 characters is rejected by the radio
// (softAP/begin), so storing one guarantees a join failure.  Empty is legal and
// distinct: it means an OPEN network.
inline constexpr size_t kMinWpaPass = 8;

// The whole on-device credential record.  Fixed-size buffers, no heap: this is
// read on the boot path and must not allocate.
struct Record {
    char     ssid[kMaxSsid + 1];
    char     pass[kMaxPass + 1];
    char     host[kMaxHost + 1];
    uint16_t port;
    char     token[kMaxToken + 1];
    char     otaPass[kMaxOtaPass + 1];
};

// Zero every field and set port to kDefaultPort.
void clear(Record& r);

// Validate and repair a record read from storage, the way loadRotation()
// validates to 1 or 3.  Guarantees on return: every buffer is NUL-terminated
// within its capacity, port is non-zero, and no field contains a control
// character (a truncated or corrupt NVS blob must not reach the radio).
// Returns true if anything had to be corrected, so the caller can log it.
bool sanitize(Record& r);

// JOINABLE: the ssid is present, so a join attempt makes sense.  Nothing else
// is required — not the agent address, which the device DISCOVERS over mDNS,
// and not the token, which it is GIVEN by pairing.  This is the bar the setup
// PORTAL clears, and it is deliberately the lowest bar that can still lead
// somewhere: with an SSID the device can get onto the network, and everything
// after that is negotiated at runtime.
//
// Like complete(), it does not require pass — an open network is legal.
bool joinable(const Record& r);

// COMPLETE: ssid, host and token are ALL non-empty, so the device can reach
// AND authenticate to the agent.  This is the bar the FETCH path clears, not
// the bar the portal clears; a joinable-but-incomplete record is the normal
// state of a device that has joined and is waiting to be paired.
//
// Deliberately does NOT require pass (open networks are legal) or otaPass
// (a device that cannot be OTA-flashed is degraded, not unprovisioned).
bool complete(const Record& r);

// True when the stored passphrase cannot possibly work: non-empty but shorter
// than the WPA2 minimum.  Kept separate from complete() because it is a
// "this join will fail" warning, not an "unprovisioned" verdict.
bool passphraseUnusable(const Record& r);

// --- the state machine ------------------------------------------------------
//
//   Unprovisioned ──────────────────────────────► Portal
//   Joinable ───────────────────────────────────► Joining
//   Joining ──(ok)──────────────────────────────► Online
//   Joining ──(fail)────────────────────────────► Retry (with backoff)
//   Retry ──(backoff elapsed)───────────────────► Joining
//   Retry ──(N consecutive failures)────────────► Portal
//   Online ──(connection lost)──────────────────► Joining
//
// The N-failures-to-Portal edge is the entire reason this machine exists:
// without it, a moved house or a changed password can only be recovered with a
// USB cable — precisely what this work removes.

enum class State : uint8_t {
    Unprovisioned,  // no complete record; nothing to try
    Portal,         // SoftAP + captive portal is running
    Joining,        // a join attempt is in flight
    Retry,          // waiting out the backoff before the next attempt
    Online,         // joined and serving
};

class Machine {
public:
    // maxJoinFailures: consecutive failed joins before falling back to Portal.
    explicit Machine(uint8_t maxJoinFailures = kMaxJoinFailures);

    State   state() const { return state_; }
    uint8_t consecutiveFailures() const { return failures_; }

    // Milliseconds to wait before the next join attempt.  Doubles per failure
    // from kBaseBackoffMs and saturates at kMaxBackoffMs, so a device with a
    // dead router backs off instead of hammering the radio flat.
    uint32_t backoffMs() const;

    // Cold boot, or a return from the portal.  recordUsable is the verdict on
    // the stored record: pass joinable(), because an SSID is all a join needs
    // and the rest arrives once the device is on the network.  (complete() is
    // the wrong predicate here — it would send a device that has an SSID but
    // no token yet to the portal instead of letting it join and pair.)
    State onBoot(bool recordUsable);

    // The outcome of a join attempt.  Success clears the failure counter.
    State onJoinResult(bool ok);

    // The backoff has elapsed; try again.  No-op unless in Retry.
    State onRetryElapsed();

    // The portal wrote a record.  recordUsable is joinable() on what was
    // saved: an SSID-less save keeps the portal up rather than dropping the
    // user into a join that cannot succeed.  A save with an SSID and no token
    // is NOT such a case — that device joins and then pairs.
    State onPortalSaved(bool recordUsable);

    // An established connection dropped.  Returns to Joining WITHOUT counting
    // a failure: losing a working network is not evidence the credentials are
    // wrong, and counting it would eventually push a perfectly good device
    // into the portal.
    State onConnectionLost();

    // Back to the cold-boot state, failure counter cleared.
    void reset();

    static constexpr uint8_t  kMaxJoinFailures = 5;
    static constexpr uint32_t kBaseBackoffMs   = 1000;
    static constexpr uint32_t kMaxBackoffMs    = 60000;

private:
    State   state_;
    uint8_t failures_;
    uint8_t maxFailures_;
};

} // namespace provision
} // namespace usage
