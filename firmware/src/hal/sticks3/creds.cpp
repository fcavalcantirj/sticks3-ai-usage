// firmware/src/hal/sticks3/creds.cpp — NVS credential store implementation.
// See creds.h for the layout; usage/provision.h owns the rules.
#include "hal/sticks3/creds.h"

// The developer seed, and the reason the production build needs no edits:
// with no firmware/include/secrets.h on the include path this whole path
// compiles out and the binary contains no credential strings.
#if defined(__has_include)
#  if __has_include("secrets.h")
#    include "secrets.h"
#    define USAGED_HAS_SECRETS 1
#  endif
#endif

#include <Preferences.h>
#include <cstring>

namespace sticks3 {

namespace {

// The SAME namespace board.cpp uses for rotation and brightness, so a
// credential write and a rotation write cannot end up in different stores.
// (An old note in docs spells it "usated"; the store on the device is
// "usaged" — board.cpp is the authority.)
constexpr char kNamespace[] = "usaged";

// NVS caps a key at 15 characters plus the NUL (NVS_KEY_NAME_MAX_SIZE is 16)
// and rejects a longer one at RUNTIME, which would look like a store that
// silently forgets a field.  The static_asserts turn that into a build error.
constexpr char kKeySsid[]  = "wifi_ssid";
constexpr char kKeyPass[]  = "wifi_pass";
constexpr char kKeyHost[]  = "agent_host";
constexpr char kKeyPort[]  = "agent_port";
constexpr char kKeyToken[] = "dev_token";
constexpr char kKeyOta[]   = "ota_pass";

static_assert(sizeof(kKeySsid) <= 16, "NVS key over 15 chars");
static_assert(sizeof(kKeyPass) <= 16, "NVS key over 15 chars");
static_assert(sizeof(kKeyHost) <= 16, "NVS key over 15 chars");
static_assert(sizeof(kKeyPort) <= 16, "NVS key over 15 chars");
static_assert(sizeof(kKeyToken) <= 16, "NVS key over 15 chars");
static_assert(sizeof(kKeyOta) <= 16, "NVS key over 15 chars");

// Read one string key into a fixed-capacity field.  cap excludes the NUL,
// exactly like the kMax* constants, so the buffer is cap + 1 bytes.
//
// Preferences::getString leaves the buffer UNTOUCHED when the key is missing
// and also when the stored string is longer than the buffer, so the field is
// emptied first: an oversized blob then reads as unset and complete() sends
// the device to the portal instead of handing 40 bytes of SSID to the radio.
void loadField(Preferences& pref, const char* key, char* dst, size_t cap) {
    dst[0] = '\0';
    pref.getString(key, dst, cap + 1);
    dst[cap] = '\0';  // belt and braces; sanitize() re-checks anyway
}

// Write one string key.  putString returns strlen(value), so an EMPTY value —
// legal for an open network's passphrase and for a board with no OTA password
// — returns 0 on success just as it does on failure.  Comparing against the
// length we asked it to write is the only distinction the API offers.
bool saveField(Preferences& pref, const char* key, const char* value) {
    return pref.putString(key, value) == std::strlen(value);
}

#if defined(USAGED_HAS_SECRETS)
// Copy a compile-time macro into a fixed-capacity field, truncating at cap
// bytes and always terminating.  Only the seed path needs this: every other
// value arrives already in a Record.
void copyField(char* dst, size_t cap, const char* src) {
    size_t i = 0;
    if (src != nullptr) {
        while (i < cap && src[i] != '\0') {
            dst[i] = src[i];
            i++;
        }
    }
    dst[i] = '\0';
}
#endif

} // namespace

bool credsLoad(usage::provision::Record& out) {
    usage::provision::clear(out);

    Preferences pref;
    // readOnly = false, as loadRotation/loadBrightness do it: opening
    // read-only FAILS outright when the namespace does not exist yet, which is
    // exactly the unprovisioned first boot this has to survive.
    if (!pref.begin(kNamespace, false)) {
        return false;  // no store, no credentials — the caller opens the portal
    }

    loadField(pref, kKeySsid, out.ssid, usage::provision::kMaxSsid);
    loadField(pref, kKeyPass, out.pass, usage::provision::kMaxPass);
    loadField(pref, kKeyHost, out.host, usage::provision::kMaxHost);
    loadField(pref, kKeyToken, out.token, usage::provision::kMaxToken);
    loadField(pref, kKeyOta, out.otaPass, usage::provision::kMaxOtaPass);
    out.port = pref.getUShort(kKeyPort, usage::provision::kDefaultPort);
    pref.end();

    // Always sanitize a record read from storage before anyone can act on it,
    // the way loadRotation() forces a stored value back to 1 or 3.
    usage::provision::sanitize(out);
    return usage::provision::complete(out);
}

bool credsSave(const usage::provision::Record& r) {
    // Sanitize a COPY: a caller holding a Record (the portal's form buffer)
    // must not have it rewritten underneath it.
    usage::provision::Record rec = r;
    usage::provision::sanitize(rec);

    Preferences pref;
    if (!pref.begin(kNamespace, false)) {
        return false;
    }

    // "&& ok" comes LAST on every line so each field is still attempted after
    // an earlier failure: a half-written record must read back as incomplete,
    // not as a mix of new and stale fields.
    bool ok = saveField(pref, kKeySsid, rec.ssid);
    ok = saveField(pref, kKeyPass, rec.pass) && ok;
    ok = saveField(pref, kKeyHost, rec.host) && ok;
    ok = saveField(pref, kKeyToken, rec.token) && ok;
    ok = saveField(pref, kKeyOta, rec.otaPass) && ok;
    ok = (pref.putUShort(kKeyPort, rec.port) == sizeof(uint16_t)) && ok;
    pref.end();

    return ok;
}

void credsClear() {
    Preferences pref;
    if (!pref.begin(kNamespace, false)) {
        return;
    }
    // Six removes, never Preferences::clear(): rotation and brightness share
    // this namespace and must survive a re-provision.  remove() returns false
    // for a key that was never written, which is not an error here.
    pref.remove(kKeySsid);
    pref.remove(kKeyPass);
    pref.remove(kKeyHost);
    pref.remove(kKeyPort);
    pref.remove(kKeyToken);
    pref.remove(kKeyOta);
    pref.end();
}

bool credsSeedFromSecretsIfEmpty() {
#if defined(USAGED_HAS_SECRETS)
    usage::provision::Record stored;
    if (credsLoad(stored)) {
        return false;  // already provisioned — NVS wins over the header
    }

    usage::provision::Record seed;
    usage::provision::clear(seed);
#ifdef WIFI_SSID
    copyField(seed.ssid, usage::provision::kMaxSsid, WIFI_SSID);
#endif
#ifdef WIFI_PASS
    copyField(seed.pass, usage::provision::kMaxPass, WIFI_PASS);
#endif
#ifdef USAGED_HOST
    copyField(seed.host, usage::provision::kMaxHost, USAGED_HOST);
#endif
#ifdef USAGED_PORT
    seed.port = (uint16_t)(USAGED_PORT);
#endif
#ifdef USAGED_DEVICE_TOKEN
    copyField(seed.token, usage::provision::kMaxToken, USAGED_DEVICE_TOKEN);
#endif
#ifdef OTA_PASS
    copyField(seed.otaPass, usage::provision::kMaxOtaPass, OTA_PASS);
#endif

    usage::provision::sanitize(seed);
    // An incomplete seed would wear flash and still land in the portal, so
    // leave the store untouched and let the portal own it.
    if (!usage::provision::complete(seed)) {
        return false;
    }
    return credsSave(seed);
#else
    return false;  // production build: no secrets.h, nothing to seed
#endif
}

} // namespace sticks3
