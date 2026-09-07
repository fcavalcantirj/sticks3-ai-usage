// firmware/src/hal/sticks3/portal.h — SoftAP captive portal (task 77): the
// radio, the DNS hijack, the web server and the setup screens.
//
// The RULES live in usage/portal.h, which is pure C++17 and host-tested — page
// building, form validation, the AP identity and the scan list.  This file owns
// only what needs hardware.  Same split as usage/power.h (policy) vs
// hal/sticks3/power.h (hardware).
//
// WHY THIS EXISTS: a published firmware carries no credentials (task 76), so a
// device fresh out of the box knows nothing — not the network, not the agent,
// not its token.  It raises its own access point and asks, using the only two
// surfaces it has: a phone browser and its own screen.
//
// THE ONE FACT THAT SHAPES THE WHOLE FLOW: AP_STA IS SINGLE-CHANNEL.  The
// soft-AP adopts the station's channel (esp_wifi.h:812-813), so the MOMENT the
// device associates to the target network, every phone attached to the portal
// AP is dropped.  A "connected!" page in the browser is therefore impossible.
// Outcomes go on the DEVICE SCREEN; the browser only ever sees "joining".
//
// COST: the portal must cost NOTHING once the device is provisioned.  The web
// and DNS servers are heap objects created by portalBegin() and destroyed by
// portalEnd(), and on an ordinary provisioned boot portalBegin() is never
// called at all.
#pragma once

#include <cstdint>

#include "usage/provision.h"

namespace sticks3 {

// What the main loop is waiting to hear.
enum class PortalOutcome : uint8_t {
    Running,  // still serving; keep calling portalUpdate()
    Joined,   // credentials saved AND a join with them succeeded
};

// Raise the access point, the wildcard DNS server and the web server, and paint
// the setup screen.  `seed` supplies the page's agent-address and port prefills
// only — host and port are not credentials, and nothing else is prefilled.
//
// Returns false if the radio or the AP refused to start, in which case nothing
// is left running.
bool portalBegin(const usage::provision::Record& seed);

// True between a successful portalBegin() and portalEnd().
bool portalActive();

// Drive DNS, the web server, the async scan cycle, the join attempt and the
// screen.  Call once per loop pass with nowMs().
void portalUpdate(uint32_t nowMs);

// Running until the device has both stored a record and proved it by joining.
// A failed join returns to Running with the reason on the screen — it never
// reboots into a loop, because the failure case is the one the user will
// actually hit, with a mistyped password.
PortalOutcome portalOutcome();

// Stop the web server, the DNS server and the access point, and free both
// servers.  Idempotent, and safe to call when the portal never started.
void portalEnd();

} // namespace sticks3
