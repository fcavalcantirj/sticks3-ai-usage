// firmware/test/host/test_power.cpp — tests for the powerDecide state machine.
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/power.h"

// --- on USB ----------------------------------------------------------------

TEST(power_usb_never_sleeps) {
    // On USB: always stay awake, even if idle for a very long time.
    ASSERT_TRUE(usage::powerDecide(true, 600000, 0, 20000)
                == usage::PowerAction::StayAwake);
    ASSERT_TRUE(usage::powerDecide(true, 600000, 100000, 20000)
                == usage::PowerAction::StayAwake);
}

TEST(power_battery_within_grace_stays_awake) {
    uint32_t now = 100000;
    uint32_t graceAnchor = now - 10000;  // 10 s ago, within 20 s grace
    ASSERT_TRUE(usage::powerDecide(false, now, graceAnchor, 20000)
                == usage::PowerAction::StayAwake);
}

TEST(power_battery_past_grace_sleeps) {
    uint32_t now = 100000;
    uint32_t graceAnchor = now - 30000;  // 30 s ago, past 20 s grace
    ASSERT_TRUE(usage::powerDecide(false, now, graceAnchor, 20000)
                == usage::PowerAction::SleepNow);
}

TEST(power_battery_at_grace_boundary_sleeps) {
    uint32_t now = 100000;
    uint32_t graceAnchor = now - 20000;  // exactly at grace boundary
    ASSERT_TRUE(usage::powerDecide(false, now, graceAnchor, 20000)
                == usage::PowerAction::SleepNow);
}

TEST(power_battery_just_inside_grace) {
    uint32_t now = 100000;
    uint32_t graceAnchor = now - 19999;  // 1 ms inside grace
    ASSERT_TRUE(usage::powerDecide(false, now, graceAnchor, 20000)
                == usage::PowerAction::StayAwake);
}

// --- wake scenarios --------------------------------------------------------

TEST(power_timer_wake_vbus_present_goes_awake) {
    // Timer wake with VBUS present → stay awake (even if idle for hours)
    ASSERT_TRUE(usage::powerDecide(true, 4000000, 0, 20000)
                == usage::PowerAction::StayAwake);
}

TEST(power_button_wake_battery_then_resleep) {
    // Button wake on battery: graceAnchor = wake time (recent) → awake
    uint32_t wake = 100000;
    ASSERT_TRUE(usage::powerDecide(false, wake, wake, 20000)
                == usage::PowerAction::StayAwake);
    // 25 s later, past grace → sleep
    ASSERT_TRUE(usage::powerDecide(false, wake + 25000, wake, 20000)
                == usage::PowerAction::SleepNow);
}

// --- ORDER #30 grace-anchor semantics -----------------------------------------

// An unplug with a 10-minute-old lastActivity resets the grace anchor to now,
// so the device stays awake for a full 20 s after cable removal.
TEST(power_unplug_resets_grace_anchor) {
    uint32_t now = 100000;
    uint32_t stale = now - 600000;  // 10 minutes ago — past grace
    // If the caller did NOT reset the anchor, the device should sleep:
    ASSERT_TRUE(usage::powerDecide(false, now, stale, 20000)
                == usage::PowerAction::SleepNow);
    // On USB->battery transition the caller resets the anchor to now:
    ASSERT_TRUE(usage::powerDecide(false, now, now, 20000)
                == usage::PowerAction::StayAwake);
    // Stays awake for 19 s after the transition:
    ASSERT_TRUE(usage::powerDecide(false, now + 19999, now, 20000)
                == usage::PowerAction::StayAwake);
    // Sleeps at 20 s + 1 ms:
    ASSERT_TRUE(usage::powerDecide(false, now + 20001, now, 20000)
                == usage::PowerAction::SleepNow);
}

// A wake whose fetch takes 12 s: grace starts at render time (t=12s),
// not at boot (t=0).  Before the render the caller passes now as the anchor
// (grace not yet active).  After render the anchor is locked to render time.
TEST(power_wake_grace_starts_at_render) {
    uint32_t boot = 100000;
    uint32_t renderDone = boot + 12000;  // fetch took 12 s

    // Before render (grace not active, anchor = now): always awake.
    ASSERT_TRUE(usage::powerDecide(false, boot + 11999, boot + 11999, 20000)
                == usage::PowerAction::StayAwake);

    // At render completion (grace anchor = render time): alive.
    ASSERT_TRUE(usage::powerDecide(false, renderDone, renderDone, 20000)
                == usage::PowerAction::StayAwake);

    // 19 s after render: still alive.
    ASSERT_TRUE(usage::powerDecide(false, renderDone + 19999, renderDone, 20000)
                == usage::PowerAction::StayAwake);

    // 20 s + 1 ms after render: sleep.
    ASSERT_TRUE(usage::powerDecide(false, renderDone + 20001, renderDone, 20000)
                == usage::PowerAction::SleepNow);
}

// --- VBUS debounce (ORDER #26) ------------------------------------------------

// A single spurious "battery" sample (or a 0 mV I2C glitch) must NOT cause
// powerDecide to return SleepNow.  The VbusDebouncer requires N consecutive
// battery readings before settling to false; a 0 mV read is suspect and ignored.
TEST(power_vbus_spurious_false_no_sleep) {
    usage::VbusDebouncer debouncer(3);  // need 3 consecutive battery reads

    // USB present: debouncer settles true.
    ASSERT_TRUE(debouncer.sample(5000) == true);
    // Spurious 0 mV (I2C glitch): ignored, stays true.
    ASSERT_TRUE(debouncer.sample(0) == true);
    ASSERT_TRUE(debouncer.sample(0) == true);
    // powerDecide sees vbusPresent=true → StayAwake even on "battery".
    ASSERT_TRUE(usage::powerDecide(debouncer.vbusPresent(), 600000, 0, 20000)
                == usage::PowerAction::StayAwake);

    // Two spurious low readings do NOT flip to battery (need 3).
    ASSERT_TRUE(debouncer.sample(3000) == true);  // count=1
    ASSERT_TRUE(debouncer.sample(0) == true);     // ignored, count still 1
    ASSERT_TRUE(debouncer.vbusPresent() == true);
    ASSERT_TRUE(usage::powerDecide(debouncer.vbusPresent(), 600000, 0, 20000)
                == usage::PowerAction::StayAwake);

    // Third consecutive battery reading finally settles to false.
    debouncer.sample(3000);  // count=2
    debouncer.sample(3000);  // count=3 → settled=false
    ASSERT_TRUE(debouncer.vbusPresent() == false);
    // Now powerDecide past grace → SleepNow (correct behaviour on real battery).
    ASSERT_TRUE(usage::powerDecide(debouncer.vbusPresent(), 600000, 0, 20000)
                == usage::PowerAction::SleepNow);

    // One USB reading resets immediately.
    ASSERT_TRUE(debouncer.sample(5000) == true);
    ASSERT_TRUE(debouncer.vbusPresent() == true);
}
