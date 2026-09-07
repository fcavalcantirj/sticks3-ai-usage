// firmware/src/hal/sticks3/bleprov.h — the BLE peripheral half of zero-typing
// provisioning: advertise, accept the daemon's write, store it, join.
//
// The RULES live in usage/bleprov.h, which is pure C++17 and host-tested —
// every byte of the wire format, every verdict on it, the status/info encoders
// and the passkey formatting.  This file owns only what needs a radio: the GATT
// server, the four characteristics, the pairing dialogue and the apply-and-join
// machine.  IT CONTAINS NO PARSING.  Same split as usage/portal.h (rules) vs
// hal/sticks3/portal.h (hardware), and usage/power.h vs hal/sticks3/power.h.
//
// WHY THIS EXISTS: the captive portal (task 77) asks a stranger to type an SSID
// and a Wi-Fi password on a phone.  The owner's Mac already KNOWS both, plus
// its own address, its port, and a token it can mint on the spot — so the whole
// conversation can happen over BLE with the owner typing nothing but the six
// pairing digits the device puts on its own screen.  The portal stays as the
// fallback for a setup with no Mac; this is the primary path.
//
// THIS MODULE DOES NOT DRAW.  bleProvPasskey(), bleProvName(), bleProvState()
// and bleProvMessage() return what the screen needs; main.cpp decides how it
// looks on a 1.14" panel.  Same rule as pair.h.
//
// NOTHING HERE LOGS A CREDENTIAL, AND NOTHING HERE LOGS THE PASSKEY.  The
// serial line carries the state name, the error NUMBER and its fixed sentence
// from usage::bleprov::errorText() — never a submitted byte, and never the six
// digits, which belong on the screen exactly as the portal's AP passphrase does.
//
// THE STACK IS EXPENSIVE AND THE LIFECYCLE IS ONE-WAY.  BLE costs ~579 KB of
// flash (unconditional — it is in the image whether or not it runs) and tens of
// kilobytes of RAM while live.  bleProvEnd() hands the RAM back, but the call
// that does so is irreversible for the rest of the boot; see the note on
// bleProvEnd() before changing the lifecycle.
#pragma once

#include <cstdint>

#include "usage/provision.h"

namespace sticks3 {

// What the main loop is waiting to hear.  Mirrors PairState: one enum, no
// separate outcome getter, and exactly one terminal value.
//
//   Off ──begin──► Advertising ◄──────────────┐
//                     │ central connects      │ central leaves / transfer refused
//                     ▼                       │
//                  Linked ──BEGIN──► Receiving┤
//                     ▲                 │ COMMIT ok
//                     │                 ▼
//                     └──────────── Applying ──join ok──► Provisioned (terminal)
//
// A REFUSED TRANSFER IS NOT A STATE.  The daemon retries with a fresh BEGIN, so
// a rejection leaves the machine where it was and puts the reason in
// bleProvMessage() — the same shape portal.cpp gives a rejected form post.
enum class BleProvState : uint8_t {
    Off = 0,      // bleProvBegin() has not run, or bleProvEnd() has
    Advertising,  // up and waiting; no central connected
    Linked,       // a central is connected — pairing, or between transfers
    Receiving,    // BEGIN accepted; chunks are arriving
    Applying,     // the record is saved and a join is in flight
    Provisioned,  // stored AND joined AND the daemon was told; BLE is down
};

// Raise the BLE stack, publish the service and start advertising.
//
// `provisioned` sets bit 0 of the Info characteristic's flags — it is what lets
// the dashboard say "this StickS3 already has credentials" before it offers to
// overwrite them.  It is NOT a gate: a provisioned device that is advertising
// was put there deliberately by its owner.
//
// Returns false if any step failed, in which case NOTHING is left running —
// half a GATT server advertising with no security configured is worse than no
// server at all.  Returns false immediately, and forever, once bleProvEnd() has
// released the controller memory in this boot.
bool bleProvBegin(bool provisioned = false);

// True between a successful bleProvBegin() and bleProvEnd().
bool bleProvActive();

// Drive the connection, the apply-and-join machine, the status notifications
// and the advertising restart after a disconnect.  Call once per loop pass with
// nowMs().  On success it performs the teardown itself and leaves the state at
// Provisioned, so the caller's `if (bleProvActive())` branch falls away on its
// own.
void bleProvUpdate(uint32_t nowMs);

BleProvState bleProvState();

// The advertised local name — the SAME "usaged-XXXX" identity the captive
// portal AP uses (usage::portal::apIdentity), so one device has one name
// however it is set up.  Empty before bleProvBegin().
const char* bleProvName();

// The six digits the central's pairing dialog is asking for, zero-padded by
// usage::bleprov::formatPasskey().  Empty ("") whenever no pairing is in
// flight — the caller must not draw a stale passkey after the link has bonded,
// so this is cleared the moment authentication completes or the link drops.
const char* bleProvPasskey();

// One fixed sentence about the last thing that happened, safe to draw and safe
// to print: it is either a constant from this file or one of
// usage::bleprov::errorText() / usage::portal::joinFailText(), never a
// submitted byte.  Empty when there is nothing to say.
const char* bleProvMessage();

// Bumped whenever anything the screen renders changes (state, passkey,
// message, transfer progress).  A caller repaints when it differs from the
// value it last drew, which costs one comparison instead of a full redraw every
// pass — the same job portal.cpp's private g_screenDirty does, exposed instead
// of hidden because main.cpp owns the screen here.
uint32_t bleProvRevision();

// Bytes received of the stream currently in flight, and the total the daemon
// declared.  Both 0 outside a transfer.  Progress only — never content.
uint16_t bleProvReceived();
uint16_t bleProvDeclared();

// Stop advertising, disconnect any central, and tear the BLE stack down.
// Idempotent, and safe when BLE never started.
//
// READ THIS BEFORE CHANGING releaseMemory.  With releaseMemory true (the
// default) this additionally calls esp_bt_mem_release(ESP_BT_MODE_BTDM), which
// returns the controller's BSS and data — documented at "about 70k bytes" in
// esp_bt.h — plus the host stack's, to the heap.  THAT IS IRREVERSIBLE FOR THE
// REST OF THE BOOT: esp_bt.h states "once BT memory is released, the process
// cannot be reversed", and bleProvBegin() refuses afterwards rather than
// crashing inside btStart().  Pass false only for a bounded advertising window
// that may be re-opened in the same boot, and know that re-opening leaks the
// previous BLEServer object graph — BLEDevice::deinit() frees none of it.
void bleProvEnd(bool releaseMemory = true);

} // namespace sticks3
