// firmware/src/hal/sticks3/bleprov.cpp — the GATT server, the pairing dialogue
// and the apply-and-join machine.  See bleprov.h for the design and
// docs/BLE_PROVISIONING.md for the wire contract; usage/bleprov.h owns every
// rule that can be decided without a radio, and there is NO PARSING here.
//
// TWO TASKS TOUCH THIS FILE AND THE SPLIT IS THE WHOLE DESIGN.
//
//   The bluedroid GATTS/GAP task runs every callback below.  Those callbacks do
//   exactly three things: take the mutex, hand the bytes to the pure decoder,
//   and set a volatile flag.  No serial, no NVS, no radio, no drawing — the
//   same discipline portal.cpp's Wi-Fi event handler follows, for the same
//   reason: a long or blocking call in a stack callback is a hang nobody can
//   reproduce.
//
//   The Arduino loop task runs bleProvUpdate(), which owns everything slow:
//   credsSave(), netBegin(), the join timeout, every serial line and every
//   status notification.
//
// Nothing here logs a credential, and nothing here logs the passkey.  The
// [BLE] lines carry state names, lengths and the fixed sentences from
// usage::bleprov::errorText().
#include "hal/sticks3/bleprov.h"

#include "hal/sticks3/board.h"   // serialLine
#include "hal/sticks3/creds.h"   // credsSave
#include "hal/sticks3/net.h"     // netBegin, netUp
#include "usage/bleprov.h"
#include "usage/portal.h"        // apIdentity, joinFailText, kJoinReasonUnknown
#include "usage/provision.h"
#include "usage/serial_proto.h"  // fmtErr

#include <BLE2902.h>
#include <BLEDevice.h>
#include <BLESecurity.h>
#include <WiFi.h>

#include <esp_bt.h>
#include <esp_bt_main.h>
#include <esp_gap_ble_api.h>
#include <esp_gatt_defs.h>

#include <freertos/FreeRTOS.h>
#include <freertos/semphr.h>

#include <cstdio>
#include <cstring>
#include <new>
#include <string>

namespace sticks3 {

namespace {

namespace bp = usage::bleprov;

// --- the contract ------------------------------------------------------------
//
// Frozen, and shared with the Go daemon.  The four characteristics differ only
// in the first 32-bit group so a packet capture reads at a glance.

constexpr char kSvcUuid[]    = "3e12c7ff-ef1e-4bcc-8b8c-0185d4474539";
constexpr char kInfoUuid[]   = "3e12c701-ef1e-4bcc-8b8c-0185d4474539";
constexpr char kCtrlUuid[]   = "3e12c702-ef1e-4bcc-8b8c-0185d4474539";
constexpr char kDataUuid[]   = "3e12c703-ef1e-4bcc-8b8c-0185d4474539";
constexpr char kStatusUuid[] = "3e12c704-ef1e-4bcc-8b8c-0185d4474539";

// One service handle, four characteristics at two handles each, one CCCD: ten.
// Twenty leaves room for a future characteristic without a silent
// ESP_GATT_INSUF_HANDLES at service-start time.
constexpr uint16_t kNumHandles = 20;

// --- tuning ------------------------------------------------------------------

// The same upper bound portal.cpp puts on one join attempt, for the same
// reason: the failure the owner will actually hit is a wrong password, and a
// dialog that never answers reads as a hang.
constexpr uint32_t kJoinTimeoutMs  = 15000;
constexpr uint8_t  kJoinEventsMax  = 2;

// After Applied is notified, hold the link open long enough for the
// notification and its ACK to leave and for the daemon to disconnect on its
// own, BEFORE the stack is torn out from under it.
constexpr uint32_t kAppliedDwellMs = 2000;

// --- fixed sentences ---------------------------------------------------------
//
// Short enough for a 240 px line at text size 1, and containing no submitted
// byte by construction — the same rule usage/bleprov.h's errorText() follows.

constexpr char kMsgPairFailed[]  = "pairing failed - try again";
constexpr char kMsgPairUnsup[]   = "unsupported pairing method";
constexpr char kMsgSaveFailed[]  = "could not save credentials";
constexpr char kMsgJoined[]      = "connected";

// --- state -------------------------------------------------------------------

// The library objects, cleared to null rather than deleted on teardown:
// BLEDevice::deinit() frees the controller and the host stack but not one of
// these C++ wrappers.  A second session in the same boot leaks this graph
// again, which is why the documented lifecycle is one BLE session per boot.
BLEServer*         g_server  = nullptr;
BLEService*        g_service = nullptr;
BLECharacteristic* g_info    = nullptr;
BLECharacteristic* g_ctrl    = nullptr;
BLECharacteristic* g_data    = nullptr;
BLECharacteristic* g_status  = nullptr;

// Recursive: notify() reaches our own callbacks, and a re-entrant take on a
// plain mutex is a deadlock nobody would find twice.
SemaphoreHandle_t g_lock = nullptr;

// ~550 bytes of plain storage, no heap.  Written by the GATTS task through
// onControl/onChunk and by the loop through markApplying/markApplied/
// markFailed/scrub — always under g_lock.
bp::Decoder g_dec;

bool         g_active   = false;
bool         g_released = false;  // esp_bt_mem_release() has run: never re-init
BleProvState g_state    = BleProvState::Off;

char     g_name[usage::portal::kApSsidCap + 1] = {0};
char     g_passkey[bp::kPasskeyLen + 1]        = {0};
char     g_message[64]                         = {0};
uint32_t g_rev                                 = 0;
uint8_t  g_mac[6]                              = {0};

// Written from the bluedroid task, read from the loop: scalars only.
volatile bool g_connected   = false;
volatile bool g_wantAdv     = false;  // a disconnect left advertising stopped
volatile bool g_dirty       = false;  // the decoder moved; publish the status
volatile bool g_touched     = false;  // something the screen shows changed
volatile bool g_numericReq  = false;  // a numeric-comparison pairing was asked for
volatile bool g_authDone    = false;
volatile bool g_authOk      = false;
volatile uint8_t g_authFail = 0;

// The apply-and-join machine, driven only by the loop.
enum class Phase : uint8_t {
    Serving,  // advertising or linked; waiting for a COMMIT to validate
    Joining,  // credentials saved, WiFi.begin issued
    Applied,  // Applied notified; dwelling before teardown
};
Phase    g_phase     = Phase::Serving;
uint32_t g_phaseAtMs = 0;

// Written from the Wi-Fi event task, read from the loop: scalars only.
volatile uint8_t g_joinReason = usage::portal::kJoinReasonUnknown;
volatile uint8_t g_joinEvents = 0;
wifi_event_id_t  g_evtHandler = 0;

// Last logged transfer verdict, so one line is emitted per change.
bp::State g_lastState = bp::State::Idle;
bp::Error g_lastError = bp::Error::None;

// --- small helpers -----------------------------------------------------------

struct Guard {
    Guard() {
        if (g_lock != nullptr) {
            xSemaphoreTakeRecursive(g_lock, portMAX_DELAY);
        }
    }
    ~Guard() {
        if (g_lock != nullptr) {
            xSemaphoreGiveRecursive(g_lock);
        }
    }
};

// A plain memset over a dead local is licensed to disappear.  The record this
// wipes held a Wi-Fi password and a device token for the length of one call and
// has no business staying in the loop task's stack frame afterwards.
void secureWipe(void* p, size_t n) {
    volatile uint8_t* v = static_cast<volatile uint8_t*>(p);
    while (n-- > 0) {
        *v++ = 0;
    }
}

void setMessage(const char* s) {
    std::snprintf(g_message, sizeof(g_message), "%s", s != nullptr ? s : "");
    ++g_rev;
}

void setState(BleProvState s) {
    if (g_state != s) {
        g_state = s;
        ++g_rev;
    }
}

void errLine(const char* what) {
    char buf[80];
    usage::fmtErr(buf, sizeof(buf), what);
    serialLine(buf);
}

// Encode the current status and publish it.  Held under one lock so a read
// arriving from the bluedroid task cannot slip a different value in between the
// setValue() and the notify() the daemon is waiting on.
void publishStatus(bool notify) {
    if (g_status == nullptr) {
        return;
    }
    Guard g;
    uint8_t buf[bp::kStatusLen];
    const size_t n = bp::encodeStatus(g_dec, buf, sizeof(buf));
    if (n == 0) {
        return;
    }
    g_status->setValue(buf, n);
    if (notify) {
        // notify(), not indicate(): indicate blocks the caller on a
        // confirmation semaphore for up to a second, and this runs in the loop.
        g_status->notify();
    }
}

// --- callbacks (bluedroid task) ----------------------------------------------
//
// EVERY ONE OF THESE IS SHORT ON PURPOSE.  They take the mutex, touch pure
// state, and return.

class ControlCallbacks : public BLECharacteristicCallbacks {
public:
    using BLECharacteristicCallbacks::onWrite;
    void onWrite(BLECharacteristic* c, esp_ble_gatts_cb_param_t*) override {
        // getData()/getLength(), not param->write.value: the library reaches
        // this callback from ESP_GATTS_EXEC_WRITE_EVT too
        // (BLECharacteristic.cpp:230-235), where the param union holds
        // exec_write and write.value is meaningless.  The stored value is right
        // on both paths.
        Guard g;
        g_dec.onControl(c->getData(), c->getLength());
        g_dirty = true;
    }
};

class DataCallbacks : public BLECharacteristicCallbacks {
public:
    using BLECharacteristicCallbacks::onWrite;
    void onWrite(BLECharacteristic* c, esp_ble_gatts_cb_param_t*) override {
        Guard g;
        g_dec.onChunk(c->getData(), c->getLength());
        g_dirty = true;
    }
};

class StatusCallbacks : public BLECharacteristicCallbacks {
public:
    using BLECharacteristicCallbacks::onRead;
    void onRead(BLECharacteristic* c, esp_ble_gatts_cb_param_t*) override {
        // The library invokes this BEFORE it reads m_value to build the
        // response (BLECharacteristic.cpp:392-396), so refreshing here is what
        // makes a plain read answer with the current counters rather than
        // whatever the last notification left behind.
        Guard g;
        uint8_t buf[bp::kStatusLen];
        const size_t n = bp::encodeStatus(g_dec, buf, sizeof(buf));
        if (n > 0) {
            c->setValue(buf, n);
        }
    }
};

class ServerCallbacks : public BLEServerCallbacks {
public:
    using BLEServerCallbacks::onConnect;
    using BLEServerCallbacks::onDisconnect;

    void onConnect(BLEServer*, esp_ble_gatts_cb_param_t*) override {
        g_connected = true;
        g_wantAdv   = false;
        g_touched   = true;
    }

    void onDisconnect(BLEServer*, esp_ble_gatts_cb_param_t*) override {
        // The library's own comment says it restarts advertising here and the
        // code does not (BLEServer.cpp:207-220) — it only decrements the peer
        // count.  The loop restarts it.
        g_connected = false;
        g_wantAdv   = true;
        g_touched   = true;
        Guard g;
        g_passkey[0] = '\0';  // dead with the link
    }
};

class SecurityCallbacks : public BLESecurityCallbacks {
public:
    // Cannot happen with ESP_IO_CAP_OUT (DisplayOnly): a passkey REQUEST is the
    // stack asking this device to type one, and it has no keyboard.  Answering
    // 0 fails the pairing, which is the honest outcome.
    uint32_t onPassKeyRequest() override { return 0; }

    void onPassKeyNotify(uint32_t passkey) override {
        Guard g;
        bp::formatPasskey(passkey, g_passkey, sizeof(g_passkey));
        g_touched = true;
        // NOT LOGGED.  The six digits belong on the screen, exactly like the
        // portal's AP passphrase.
    }

    bool onSecurityRequest() override { return true; }

    void onAuthenticationComplete(esp_ble_auth_cmpl_t cmpl) override {
        Guard g;
        g_passkey[0] = '\0';  // stale the moment the link bonds
        g_authOk     = cmpl.success;
        g_authFail   = cmpl.fail_reason;
        g_authDone   = true;
        g_touched    = true;
    }

    // REFUSED DELIBERATELY.  With DisplayOnly on this side the spec's pairing
    // method is Passkey Entry, so numeric comparison means the negotiation
    // landed somewhere the design did not plan for.  Accepting it here would
    // silently invent a third security model on top of the two
    // docs/BLE_PROVISIONING.md section 2 documents, and this device has no
    // confirm button wired to compare the number against.  Refusing makes the
    // situation diagnosable — the loop prints it and the screen says so —
    // instead of mysterious.
    bool onConfirmPIN(uint32_t) override {
        g_numericReq = true;
        g_touched    = true;
        return false;
    }
};

ControlCallbacks  g_ctrlCb;
DataCallbacks     g_dataCb;
StatusCallbacks   g_statusCb;
ServerCallbacks   g_serverCb;
SecurityCallbacks g_secCb;

// --- the Wi-Fi event task ----------------------------------------------------

void onWifiEvent(arduino_event_id_t event, arduino_event_info_t info) {
    // Scalars only — no serial, no drawing.  Same handler shape as portal.cpp,
    // and for the same reason: setAutoReconnect(false) does not stop the core's
    // forced first reconnect, and the disconnect reason is printed with log_w,
    // which is compiled out at CORE_DEBUG_LEVEL=1.
    if (event == ARDUINO_EVENT_WIFI_STA_DISCONNECTED) {
        g_joinReason = (uint8_t)info.wifi_sta_disconnected.reason;
        if (g_joinEvents < 0xFF) {
            g_joinEvents = (uint8_t)(g_joinEvents + 1);
        }
    }
}

// --- teardown ----------------------------------------------------------------

void teardownStack(bool releaseMemory) {
    if (g_evtHandler != 0) {
        WiFi.removeEvent(g_evtHandler);
        g_evtHandler = 0;
    }

    if (esp_bluedroid_get_status() == ESP_BLUEDROID_STATUS_ENABLED) {
        BLEDevice::stopAdvertising();
        if (g_server != nullptr && g_connected) {
            g_server->disconnect(g_server->getConnId());
        }
    }

    g_info    = nullptr;
    g_ctrl    = nullptr;
    g_data    = nullptr;
    g_status  = nullptr;
    g_service = nullptr;
    g_server  = nullptr;

    // deinit(false), NOT deinit(true).  The release_memory branch calls
    // esp_bt_controller_mem_release() and then skips clearing the library's
    // `initialized` flag (BLEDevice.cpp:649-662), leaving the library
    // convinced BLE is still up for the rest of the boot.  Take the safe
    // branch and do the release below, where the controller's status can be
    // checked first.
    BLEDevice::deinit(false);

    if (!releaseMemory) {
        return;
    }
    // esp_bt.h: the release is legal only "before esp_bt_controller_init() or
    // after esp_bt_controller_deinit()", and it is irreversible.  Prove the
    // controller actually reached IDLE rather than assuming deinit() worked —
    // it returns void and checks nothing.
    if (esp_bt_controller_get_status() != ESP_BT_CONTROLLER_STATUS_IDLE) {
        serialLine("[BLE] controller not idle - memory not released");
        return;
    }
    // esp_bt_mem_release, not esp_bt_controller_mem_release: the former also
    // releases the BSS and data of the BT/BLE HOST stack, which is most of what
    // a provisioned device is carrying for nothing.
    if (esp_bt_mem_release(ESP_BT_MODE_BTDM) == ESP_OK) {
        g_released = true;
        serialLine("[BLE] memory released");
    } else {
        serialLine("[BLE] memory release refused");
    }
}

// --- the apply-and-join machine (loop task) ----------------------------------

void startApply(uint32_t nowMs) {
    // Acknowledge that the work started: the daemon must see progress, and a
    // Ready that never moves reads as a hang while the join takes seconds.
    {
        Guard g;
        g_dec.markApplying();
    }
    publishStatus(true);
    setState(BleProvState::Applying);

    usage::provision::Record rec;
    {
        Guard g;
        rec = g_dec.record();
    }

    // The SHAPE of the record, never its contents — the same line the boot path
    // prints.
    char buf[112];
    std::snprintf(buf, sizeof(buf),
                  "[BLE] apply ssid=%d pass=%d host=%d token=%d ota=%d port=%u",
                  (int)std::strlen(rec.ssid), (int)std::strlen(rec.pass),
                  (int)std::strlen(rec.host), (int)std::strlen(rec.token),
                  (int)std::strlen(rec.otaPass), (unsigned)rec.port);
    serialLine(buf);

    // PARTIAL OTA UPDATE (task 113 Part B).  A stream that carries an SSID and
    // an otaPass but no host or token is not a full provision — it is the daemon
    // arming OTA on an already-provisioned device.  The SSID is the device's
    // only identifier on the wire; it must match the stored one or we refuse
    // rather than arm the wrong device.  If it matches, update just the
    // otaPass in NVS, re-arm OTA, and report Applied — no WiFi re-join needed.
    if (rec.ssid[0] != '\0' && rec.otaPass[0] != '\0' &&
        rec.host[0] == '\0' && rec.token[0] == '\0') {
        usage::provision::Record existing;
        if (credsLoad(existing)) {
            if (std::strcmp(existing.ssid, rec.ssid) == 0) {
                if (credsSaveOtaPass(rec.otaPass)) {
                    otaRearm(rec.otaPass);
                    {
                        Guard g;
                        g_dec.markApplied();
                        g_dec.scrub();
                    }
                    publishStatus(true);
                    setMessage(kMsgJoined);
                    serialLine("[BLE] ota_pass updated — OTA re-armed");
                    secureWipe(&rec, sizeof(rec));
                    secureWipe(&existing, sizeof(existing));
                    g_phase     = Phase::Applied;
                    g_phaseAtMs = nowMs;
                    return;
                }
                // credsSaveOtaPass failed — fall through to the full path, which
                // will report SaveFailed at its own credsSave.
            }
        }
        // SSID mismatch or not provisioned: refuse the partial, fall through to
        // the full path.  A full provision with a host/token is the correct
        // response to "this device is not the one I expected".
    }

    if (!credsSave(rec)) {
        secureWipe(&rec, sizeof(rec));
        {
            Guard g;
            g_dec.markFailed(bp::Error::SaveFailed);
            g_dec.scrub();
        }
        publishStatus(true);
        setMessage(kMsgSaveFailed);
        errLine("bleprov: credsSave failed");
        return;  // stay in Serving: a fresh BEGIN is the documented retry
    }

    // Saved BEFORE the join is attempted, exactly as the portal does: a power
    // cut mid-setup then leaves credentials the boot path can retry, and if
    // they are wrong the provisioning state machine falls back after N
    // failures.
    g_joinReason = usage::portal::kJoinReasonUnknown;
    g_joinEvents = 0;
    netBegin(rec);
    secureWipe(&rec, sizeof(rec));

    g_phase     = Phase::Joining;
    g_phaseAtMs = nowMs;
}

void updateJoin(uint32_t nowMs) {
    if (netUp()) {
        {
            Guard g;
            g_dec.markApplied();
            // The secret has been persisted; it has no business sitting in RAM
            // for the rest of the uptime.  scrub() keeps state and error, so
            // the notification below still says Applied.
            g_dec.scrub();
        }
        publishStatus(true);
        setMessage(kMsgJoined);

        char ip[16];
        char buf[48];
        netIp(ip, sizeof(ip));
        std::snprintf(buf, sizeof(buf), "[BLE] join=ok ip=%s", ip);
        serialLine(buf);

        g_phase     = Phase::Applied;
        g_phaseAtMs = nowMs;
        return;
    }

    const bool exhausted = g_joinEvents >= kJoinEventsMax;
    const bool timedOut  = (int32_t)(nowMs - g_phaseAtMs) >= (int32_t)kJoinTimeoutMs;
    if (!exhausted && !timedOut) {
        return;
    }

    const uint8_t reason = g_joinReason;
    // Stop the radio re-applying credentials that plainly do not work: eraseap
    // clears the stored STA config so the core's forced reconnect has nothing
    // to retry with.  The NVS record stays — the daemon is expected to send a
    // corrected one, and the boot path's own retry/backoff owns the rest.
    WiFi.disconnect(false, true);

    {
        Guard g;
        g_dec.markFailed(bp::Error::JoinFailed);
        g_dec.scrub();
    }
    publishStatus(true);
    setMessage(usage::portal::joinFailText(reason));

    char buf[48];
    std::snprintf(buf, sizeof(buf), "[BLE] join=fail reason=%u", (unsigned)reason);
    serialLine(buf);

    g_phase = Phase::Serving;
}

void logVerdict() {
    bp::State st;
    bp::Error er;
    {
        Guard g;
        st = g_dec.state();
        er = g_dec.error();
    }
    if (st == g_lastState && er == g_lastError) {
        return;
    }
    g_lastState = st;
    g_lastError = er;

    char buf[96];
    std::snprintf(buf, sizeof(buf), "[BLE] xfer=%s err=%u %s", bp::stateText(st),
                  (unsigned)er, bp::errorText(er));
    serialLine(buf);

    if (er != bp::Error::None) {
        setMessage(bp::errorText(er));
    }
}

void refreshState() {
    if (g_phase == Phase::Joining) {
        setState(BleProvState::Applying);
        return;
    }
    bp::State st;
    {
        Guard g;
        st = g_dec.state();
    }
    if (st == bp::State::Receiving) {
        setState(BleProvState::Receiving);
        return;
    }
    setState(g_connected ? BleProvState::Linked : BleProvState::Advertising);
}

// --- construction ------------------------------------------------------------

// Everything that must be true before a single advertising packet goes out.
// Split from bleProvBegin() so every failure path can take one teardown.
bool buildServer(bool provisioned) {
    // SECURITY BEFORE ANYTHING ELSE.  BLEDevice::init() leaves the I/O
    // capability at ESP_IO_CAP_NONE (BLEDevice.cpp:420-425), which is Just
    // Works — no passkey, no MITM protection, and no prompt on the Mac.
    BLEDevice::setSecurityCallbacks(&g_secCb);

    BLESecurity sec;  // every setter pushes straight to the stack; keeps no state
    sec.setAuthenticationMode(ESP_LE_AUTH_REQ_SC_MITM_BOND);
    sec.setCapability(ESP_IO_CAP_OUT);  // DisplayOnly: the stack picks the digits
    sec.setInitEncryptionKey(ESP_BLE_ENC_KEY_MASK | ESP_BLE_ID_KEY_MASK);
    sec.setRespEncryptionKey(ESP_BLE_ENC_KEY_MASK | ESP_BLE_ID_KEY_MASK);
    sec.setKeySize(16);

    // Without this the peer may negotiate DOWN to a weaker mode and the stack
    // accepts it; esp_gap_ble_api.h:1777-1784 names this as step 2 of accepting
    // secure-connections-only.  BLESecurity exposes no setter, so set the
    // parameter directly.
    uint8_t onlySpecified = ESP_BLE_ONLY_ACCEPT_SPECIFIED_AUTH_ENABLE;
    esp_ble_gap_set_security_param(ESP_BLE_SM_ONLY_ACCEPT_SPECIFIED_SEC_AUTH,
                                   &onlySpecified, sizeof(onlySpecified));

    g_server = BLEDevice::createServer();
    if (g_server == nullptr) {
        errLine("bleprov: createServer failed");
        return false;
    }
    g_server->setCallbacks(&g_serverCb);

    g_service = g_server->createService(BLEUUID(kSvcUuid), kNumHandles);
    if (g_service == nullptr) {
        errLine("bleprov: createService failed");
        return false;
    }

    g_info   = g_service->createCharacteristic(kInfoUuid,
                                               BLECharacteristic::PROPERTY_READ);
    g_ctrl   = g_service->createCharacteristic(kCtrlUuid,
                                               BLECharacteristic::PROPERTY_WRITE);
    g_data   = g_service->createCharacteristic(kDataUuid,
                                               BLECharacteristic::PROPERTY_WRITE);
    g_status = g_service->createCharacteristic(
        kStatusUuid,
        BLECharacteristic::PROPERTY_READ | BLECharacteristic::PROPERTY_NOTIFY);
    if (g_info == nullptr || g_ctrl == nullptr || g_data == nullptr ||
        g_status == nullptr) {
        errLine("bleprov: createCharacteristic failed");
        return false;
    }

    // THE LINE THAT MAKES THE DESIGN A DESIGN.  A BLECharacteristic defaults to
    // ESP_GATT_PERM_READ | ESP_GATT_PERM_WRITE (BLECharacteristic.h:110) —
    // open to any central that can connect, and any BLE peripheral in range
    // accepts a connection from anyone.  Without these calls the pairing step
    // is theatre and a stranger could point this device at their own agent.
    const esp_gatt_perm_t encMitm = (esp_gatt_perm_t)(ESP_GATT_PERM_READ_ENC_MITM |
                                                      ESP_GATT_PERM_WRITE_ENC_MITM);
    g_ctrl->setAccessPermissions(encMitm);
    g_data->setAccessPermissions(encMitm);
    g_status->setAccessPermissions(encMitm);
    // Info is deliberately readable with no pairing — it carries only what is
    // already in the advertising packet — but it is not a write target, and the
    // default would have made it one.
    g_info->setAccessPermissions(ESP_GATT_PERM_READ);

    // The CCCD.  The Arduino library adds none and notify() silently refuses to
    // send without one (BLECharacteristic.cpp:505-515).
    //
    // ITS PERMISSIONS MATTER AS MUCH AS THE CHARACTERISTIC'S, and this is not
    // in the protocol doc: BLEDescriptor also defaults to READ | WRITE
    // (BLEDescriptor.h:54), and subscribing to Status is what is supposed to
    // TRIGGER pairing (doc section 8, step 5).  Left at the default the
    // subscribe succeeds on an unpaired link, the Mac never prompts, and the
    // owner is left staring at a device that shows no passkey.
    BLE2902* cccd = new (std::nothrow) BLE2902();
    if (cccd == nullptr) {
        errLine("bleprov: out of memory");
        return false;
    }
    cccd->setAccessPermissions(encMitm);
    g_status->addDescriptor(cccd);

    g_ctrl->setCallbacks(&g_ctrlCb);
    g_data->setCallbacks(&g_dataCb);
    g_status->setCallbacks(&g_statusCb);

    uint8_t info[bp::kInfoLen];
    const size_t infoLen = bp::encodeInfo(g_mac, provisioned, info, sizeof(info));
    if (infoLen == 0) {
        errLine("bleprov: encodeInfo failed");
        return false;
    }
    g_info->setValue(info, infoLen);

    uint8_t st[bp::kStatusLen];
    const size_t stLen = bp::encodeStatus(g_dec, st, sizeof(st));
    g_status->setValue(st, stLen);

    g_service->start();

    BLEAdvertising* adv = BLEDevice::getAdvertising();
    if (adv == nullptr) {
        errLine("bleprov: no advertising object");
        return false;
    }

    // BUILD THE PAYLOAD BY HAND.  The library's default path memcpys the
    // advertising struct into the scan response and turns the name back on
    // (BLEAdvertising.cpp:223-231), so a 128-bit service UUID (18 B) would be
    // asked to fit alongside flags, TX power and an 11-character name in 31
    // bytes.  BLEAdvertisementData::addData SILENTLY DROPS anything that would
    // overflow (:296-301), so the symptom is a device with no name and no error
    // anywhere.
    //
    //   advertising packet: flags 3 + 128-bit service UUID 18 = 21 of 31
    //   scan response:      complete local name 2 + 11        = 13 of 31
    BLEAdvertisementData advData;
    advData.setFlags(ESP_BLE_ADV_FLAG_GEN_DISC | ESP_BLE_ADV_FLAG_BREDR_NOT_SPT);
    advData.setCompleteServices(BLEUUID(kSvcUuid));
    BLEAdvertisementData scanResp;
    scanResp.setName(std::string(g_name));
    adv->setAdvertisementData(advData);
    adv->setScanResponseData(scanResp);

    BLEDevice::startAdvertising();
    return true;
}

} // namespace

// --- public API ----------------------------------------------------------------

bool bleProvBegin(bool provisioned) {
    if (g_active) {
        return true;
    }
    if (g_released) {
        // esp_bt.h: "once BT memory is released, the process cannot be
        // reversed".  Refusing here is the difference between a logged no and a
        // crash inside btStart().
        errLine("bleprov: BLE memory already released this boot");
        return false;
    }

    if (g_lock == nullptr) {
        g_lock = xSemaphoreCreateRecursiveMutex();
        if (g_lock == nullptr) {
            errLine("bleprov: no mutex");
            return false;
        }
    }

    {
        Guard g;
        g_dec.reset();
    }
    g_lastState = bp::State::Idle;
    g_lastError = bp::Error::None;

    // The SAME identity the captive-portal AP uses, so one device has one name
    // however it is set up.  macAddress(), not softAPmacAddress(): the latter
    // returns the caller's buffer UNMODIFIED when the mode is still
    // WIFI_MODE_NULL, and here the radio has usually never been started at all.
    // macAddress() falls back to esp_read_mac() in exactly that case
    // (WiFiSTA.cpp:532-541), so it works with the radio down.
    std::memset(g_mac, 0, sizeof(g_mac));
    WiFi.macAddress(g_mac);
    usage::portal::ApIdentity id;
    usage::portal::apIdentity(g_mac, id);
    std::snprintf(g_name, sizeof(g_name), "%s", id.ssid);
    // id.pass is the portal's AP passphrase and is not used here.

    BLEDevice::init(std::string(g_name));
    // BLEDevice::init() sets its own `initialized` flag BEFORE btStart() and
    // returns void from every failure path (BLEDevice.cpp:333-341), so
    // getInitialized() answers true even when nothing came up.  Ask the stack
    // itself instead.
    if (esp_bluedroid_get_status() != ESP_BLUEDROID_STATUS_ENABLED) {
        errLine("bleprov: bluedroid did not start");
        teardownStack(false);
        return false;
    }

    if (!buildServer(provisioned)) {
        teardownStack(false);
        return false;
    }

    g_evtHandler = WiFi.onEvent(onWifiEvent);

    g_phase      = Phase::Serving;
    g_phaseAtMs  = 0;
    g_connected  = false;
    g_wantAdv    = false;
    g_dirty      = false;
    g_touched    = false;
    g_numericReq = false;
    g_authDone   = false;
    g_authOk     = false;
    g_authFail   = 0;
    g_joinReason = usage::portal::kJoinReasonUnknown;
    g_joinEvents = 0;
    g_message[0] = '\0';
    g_passkey[0] = '\0';
    g_state      = BleProvState::Advertising;
    g_active     = true;
    ++g_rev;

    char buf[64];
    // The name is public — it is in every scan response.  Nothing else is.
    std::snprintf(buf, sizeof(buf), "[BLE] adv name=%s provisioned=%d", g_name,
                  provisioned ? 1 : 0);
    serialLine(buf);
    return true;
}

bool bleProvActive() {
    return g_active;
}

void bleProvUpdate(uint32_t nowMs) {
    if (!g_active) {
        return;
    }

    // One repaint for everything the callbacks touched since the last pass.
    if (g_touched) {
        g_touched = false;
        ++g_rev;
    }

    if (g_numericReq) {
        g_numericReq = false;
        setMessage(kMsgPairUnsup);
        serialLine("[BLE] auth=numeric-comparison refused");
    }

    if (g_authDone) {
        g_authDone = false;
        if (g_authOk) {
            serialLine("[BLE] auth=ok");
            setMessage("");
        } else {
            char buf[48];
            std::snprintf(buf, sizeof(buf), "[BLE] auth=fail reason=%u",
                          (unsigned)g_authFail);
            serialLine(buf);
            setMessage(kMsgPairFailed);
        }
    }

    // A disconnect leaves the controller not advertising, and the library does
    // not restart it.  Only while nothing is being applied: a join in flight
    // owns the radio and a new central mid-apply has nothing useful to do.
    if (g_wantAdv && !g_connected && g_phase == Phase::Serving) {
        g_wantAdv = false;
        BLEDevice::startAdvertising();
        serialLine("[BLE] adv=restarted");
    }

    // Whatever the write callbacks changed, published once, from this task.
    if (g_dirty) {
        g_dirty = false;
        publishStatus(true);
        logVerdict();
    }

    switch (g_phase) {
        case Phase::Serving: {
            bp::State st;
            {
                Guard g;
                st = g_dec.state();
            }
            if (st == bp::State::Ready) {
                startApply(nowMs);
            }
            break;
        }
        case Phase::Joining:
            updateJoin(nowMs);
            break;
        case Phase::Applied:
            if ((int32_t)(nowMs - g_phaseAtMs) >= (int32_t)kAppliedDwellMs) {
                bleProvEnd(true);
                g_state = BleProvState::Provisioned;
                ++g_rev;
            }
            break;
    }

    if (g_active) {
        refreshState();
    }
}

BleProvState bleProvState() {
    return g_state;
}

const char* bleProvName() {
    return g_name;
}

const char* bleProvPasskey() {
    return g_passkey;
}

const char* bleProvMessage() {
    return g_message;
}

uint32_t bleProvRevision() {
    return g_rev;
}

uint16_t bleProvReceived() {
    Guard g;
    return (uint16_t)g_dec.received();
}

uint16_t bleProvDeclared() {
    Guard g;
    return (uint16_t)g_dec.declared();
}

void bleProvEnd(bool releaseMemory) {
    if (!g_active) {
        return;
    }
    {
        Guard g;
        g_dec.reset();  // no credential fragment survives the session
    }
    teardownStack(releaseMemory);

    g_active     = false;
    g_connected  = false;
    g_wantAdv    = false;
    g_dirty      = false;
    g_phase      = Phase::Serving;
    g_passkey[0] = '\0';
    g_state      = BleProvState::Off;
    ++g_rev;
    serialLine("[BLE] stopped");
}

} // namespace sticks3
