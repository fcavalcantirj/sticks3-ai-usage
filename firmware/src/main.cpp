// firmware/src/main.cpp — Arduino entry points for the StickS3 usage monitor.
//
// Includes <M5Unified.h> (allowed for main.cpp) and delegates all M5 calls to
// the HAL layer; usage/ formatters stay pure C++17.
//
// The loop is a single cooperative state machine (no background tasks, no RTOS):
//
//   M5.update()
//   now = nowMs()
//   netUpdate(now)           — Wi-Fi state machine + [NET] lines
//   pollUpdate(now)          — first fetch / periodic poll / backoff
//   buttonsUpdate(now)       — BtnA page, BtnB refresh
//   updateBrightness(now)    — dim after 30 min idle, restore on activity
//   heapWatchdog(now)        — [HEAP] line every 60 s
//   if (view.needsRedraw || (!paintedThisBoot && hasModel)) { redraw }
//   powerDecide(vbus, now, graceActive ? lastActivity : now) → SleepNow? powerSleep()
//   delay(1)
#include <M5Unified.h>

#include "hal/sticks3/board.h"
#include "hal/sticks3/fetch.h"
#include "hal/sticks3/net.h"
#include "hal/sticks3/power.h"
#include "hal/sticks3/screen.h"
#include "usage/model.h"
#include "usage/power.h"
#include "usage/render_plan.h"
#include "usage/serial_proto.h"
#include "usage/gesture.h"
#include "usage/battery.h"

#include <cstdio>
#include <cstring>

using namespace sticks3;

// RTC_DATA_ATTR: survives deep sleep.  On a cold boot the magic is garbage
// and we skip restoration.  On wake from deep sleep (ext0/ext1/timer) the
// magic is "WAKE" and we restore the cached model so a redraw paints instantly.
RTC_DATA_ATTR struct WakeSnapshot {
    char magic[4];
    usage::Model model;
} g_wakeSnapshot;

// ORDER #27: guard against the PM1 IRQ line stuck-low causing an instant
// sleep/wake loop.  Counts consecutive ext0 wakes that found vbus < 4000.
// After 3, ext0 is disabled (timer + ext1 only) to protect the battery.
RTC_DATA_ATTR struct PowerGuard {
    uint8_t ext0InstantWakeCount;
} g_powerGuard;

// --- poll configuration ------------------------------------------------------

static const uint32_t kPollMs = 300000;            // 5 minutes
static const uint32_t kFirstFetchDelayMs = 3000;    // 3 s after netUp
static const uint32_t kBackoffBaseMs = 30000;       // 30 s
static const uint32_t kMaxFails = 12;

// --- brightness / watchdog configuration ------------------------------------

static const uint8_t kBrightnessActive = 80;
static const uint8_t kBrightnessIdle = 20;
static const uint32_t kBrightnessIdleMs = 1800000;  // 30 minutes
static const uint32_t kHeapIntervalMs = 60000;      // 60 seconds

// Firmware version reported in the boot banner.
static const char* kFwVersion = "1.0.0";

// --- poll state -------------------------------------------------------------

static usage::View g_view;
static usage::Model g_model;
static char g_lastRev[9] = "";
static bool g_hasModel = false;
static uint32_t g_netUpAt = 0;
static bool g_netWasUp = false;
static uint32_t g_nextPoll = 0;
static uint32_t g_failCount = 0;

// --- brightness / watchdog state --------------------------------------------

static uint32_t g_lastActivity = 0;  // last render/fetch-fail/button/USB→battery transition
static uint32_t g_lastHeapMs = 0;    // last [HEAP] emit
static bool g_brightnessDimmed = false;

// ORDER #30: grace-window state.  On wake the grace is NOT active until the
// first render or a fetch failure — pre-render the caller passes `now` as the
// anchor so powerDecide always returns StayAwake (slow Wi-Fi can't sleep the
// device before paint).  USB→battery transition resets the anchor to now and
// activates grace.
// ORDER #31: g_paintedThisBoot forces exactly one cached-snapshot paint.
static bool g_graceActive = false;     // grace window started (first render/fetch-fail on this boot)
static bool g_paintedThisBoot = false; // at least one redraw() completed
static bool g_prevVbusPresent = false; // for USB→battery transition detection

// --- IMU gesture state (task 48) ---------------------------------------------
// Double-tap detector: pure state machine, fed from the HAL IMU poll.
static sticks3::gesture::DoubleTapDetector g_gesture;
// IMU poll rate.
static const uint32_t kImuPollMs = 20;
static uint32_t g_lastImuPoll = 0;

// --- battery polling (task 49) ------------------------------------------------
// PTT-style gauge on the top bar; polled every 30 s, redrawn on change.
static const uint32_t kBatteryPollMs = 30000;  // 30 seconds
static battery::BatteryView g_batt;
static battery::BatteryView g_prevBatt;
static bool g_battEverPolled = false;
static uint32_t g_lastBatteryPoll = 0;

// --- helpers ----------------------------------------------------------------

// Exponential backoff: 30 s, 60 s, 120 s, 240 s, then capped at kPollMs (300 s).
static uint32_t pollInterval() {
    uint32_t b = kBackoffBaseMs;
    uint32_t shifts = g_failCount > 0 ? g_failCount - 1 : 0;
    for (uint32_t i = 0; i < shifts && b < kPollMs; i++) {
        b *= 2;
    }
    if (b > kPollMs) b = kPollMs;
    return b;
}

// Full-screen redraw: build plan, draw, emit [RENDER], clear the flag.
static void redraw() {
    usage::RenderPlan plan;
    usage::buildPlan(g_model, g_view.page, plan);
    drawPlan(plan, netUp(), g_batt);

    char buf[64];
    usage::fmtRender(buf, sizeof(buf), plan.page, plan.lineCount,
                     g_model.rev);
    serialLine(buf);

    g_view.needsRedraw = false;
    g_paintedThisBoot = true;   // ORDER #31: exactly one paint per boot
    // ORDER #30: any render starts/extends the grace window.
    g_graceActive = true;
    g_lastActivity = nowMs();
    // Sync g_view.lastRev with the painted model so the next same-rev fetch
    // (304 or 200) doesn't trigger an unnecessary redraw.
    for (size_t i = 0; i < 8; i++)
        g_view.lastRev[i] = g_model.rev[i];
    g_view.lastRev[8] = '\0';
}

// --- battery polling ----------------------------------------------------------

// Poll the HAL battery level + VBUS once per kBatteryPollMs, emit [BATT] on
// change, and force a redraw to refresh the top-bar gauge.
static void pollBattery(uint32_t now) {
    if (g_lastBatteryPoll != 0 &&
        (int32_t)(now - g_lastBatteryPoll) < (int32_t)kBatteryPollMs) {
        return;
    }
    g_lastBatteryPoll = now;

    int pct = battery::batteryPctClamped(batteryLevel());
    bool onUsb = vbusMv() > 4000;
    battery::BatteryView current{pct, onUsb, pct >= 0};

    bool changed = !g_battEverPolled ||
                   current.pct != g_prevBatt.pct ||
                   current.onUsb != g_prevBatt.onUsb;
    if (changed) {
        char buf[64];
        usage::fmtBatt(buf, sizeof(buf), current.pct,
                       current.onUsb ? 1 : 0);
        serialLine(buf);
        g_view.needsRedraw = true;
        g_lastActivity = now;  // cable plug/unplug: keep screen lit
    }
    g_prevBatt = current;
    g_batt = current;
    g_battEverPolled = true;
}

// Fetch /v1/usage and apply the result.  Resets failCount on 200.
static void doFetch() {
    FetchResult result;
    if (fetchUsage(g_lastRev, result)) {
        if (result.code == 200) {
            usage::Model model;
            char err[256];
            bool ok = usage::parseSnapshot(result.body.c_str(),
                                           result.body.length(),
                                           model, err, sizeof(err));
            if (!ok) {
                char msg[260];
                std::snprintf(msg, sizeof(msg), "parse: %s", err);
                char buf[80];
                usage::fmtErr(buf, sizeof(buf), msg);
                serialLine(buf);
            } else {
                usage::onSnapshot(g_view, model);
                if (g_view.needsRedraw) {
                    g_lastActivity = nowMs();
                    // ORDER #30: new data arriving starts/extends the grace
                    // window so the screen stays lit after a fetch+render.
                    g_graceActive = true;
                }
                g_model = model;
                g_hasModel = true;
                // Persist to RTC memory so a wake paints instantly and a
                // 304 needs no redraw.
                g_wakeSnapshot.model = model;
                std::memcpy(g_wakeSnapshot.magic, "WAKE", 4);
                // Save rev for the next conditional request.
                for (size_t i = 0; i < 8; i++)
                    g_lastRev[i] = model.rev[i];
                g_lastRev[8] = '\0';
            }
            g_failCount = 0;
        }
        // 200 or 304: next poll at the regular interval.
        g_nextPoll = nowMs() + kPollMs;
    } else {
        // Fetch error: exponential backoff.
        g_failCount++;
        // ORDER #30: fetch failure starts the grace window so the device
        // stays awake to retry rather than sleeping mid-connect.
        g_graceActive = true;
        g_lastActivity = nowMs();
        if (g_failCount >= kMaxFails) {
            char buf[80];
            usage::fmtErr(buf, sizeof(buf), "restart after 12 failures");
            serialLine(buf);
            ESP.restart();
        }
        g_nextPoll = nowMs() + pollInterval();
    }
}

// --- IMU gesture polling ----------------------------------------------------

// imuGestureUpdate polls the BMI270 at ~50 Hz and feeds samples to the
// double-tap detector.  On a detected gesture, toggles rotation, persists
// it, emits [GESTURE], and forces a redraw.
static void imuGestureUpdate(uint32_t now) {
    if ((int32_t)(now - g_lastImuPoll) < (int32_t)kImuPollMs) {
        return;
    }
    g_lastImuPoll = now;

    // Read raw acceleration from the BMI270 via M5Unified.
    // Units are m/s²; the detector converts to g internally.
    float ax = 0, ay = 0, az = 0;
    M5.Imu.getAccelData(&ax, &ay, &az);

    sticks3::gesture::Gesture g = g_gesture.feed(ax, ay, az, now);
    if (g == sticks3::gesture::Gesture::double_tap) {
        uint8_t rot = (uint8_t)g_gesture.rotation();
        setRotation(rot);
        saveRotation(rot);

        char buf[64];
        usage::fmtGesture(buf, sizeof(buf), rot);
        serialLine(buf);

        // Force a redraw in the new orientation.
        g_view.needsRedraw = true;
        g_lastActivity = now;
    }
}

// --- state machine steps ----------------------------------------------------

// Poll: first fetch after netUp, then periodic kPollMs.
static void pollUpdate(uint32_t now) {
    if (!netUp()) {
        g_netWasUp = false;
        return;
    }

    if (!g_netWasUp) {
        g_netWasUp = true;
        g_netUpAt = now;
    }

    // First fetch kFirstFetchDelayMs after connectivity.
    if (g_nextPoll == 0 &&
        (int32_t)(now - g_netUpAt) >= (int32_t)kFirstFetchDelayMs) {
        doFetch();
    }

    // Periodic poll.
    if (g_nextPoll != 0 && (int32_t)(now - g_nextPoll) >= 0) {
        doFetch();
    }
}

// Buttons: BtnA short-press cycles pages; BtnA long-press forces a fetch;
// BtnB short-press forces a fetch.
static void buttonsUpdate(uint32_t now) {
    // BtnA short press: cycle pages (only when we have a model).
    if (M5.BtnA.wasClicked() && g_hasModel) {
        usage::nextPage(g_view, g_model);
        g_lastActivity = now;
        char buf[64];
        usage::fmtBtn(buf, sizeof(buf), "a_click page");
        serialLine(buf);
    }

    // BtnA long press (hold threshold 600 ms in boardInit): force fetch.
    if (M5.BtnA.wasHold() && netUp()) {
        doFetch();
        g_lastActivity = now;
        char buf[64];
        usage::fmtBtn(buf, sizeof(buf), "a_hold refresh");
        serialLine(buf);
    }

    // BtnB short press: immediate fetch.
    if (M5.BtnB.wasClicked() && netUp()) {
        doFetch();
        g_lastActivity = now;
    }
}

// Brightness policy: 80 normally; 20 after 30 min without rev change or
// button press.  Any activity restores 80.  Only changes the backlight,
// never the drawn content — does not violate "redraw only on change".
static void updateBrightness(uint32_t now) {
    if ((int32_t)(now - g_lastActivity) >= (int32_t)kBrightnessIdleMs) {
        if (!g_brightnessDimmed) {
            setBrightness(kBrightnessIdle);
            g_brightnessDimmed = true;
        }
    } else if (g_brightnessDimmed) {
        setBrightness(kBrightnessActive);
        g_brightnessDimmed = false;
    }
}

// Heap watchdog: emit [HEAP] free+min every 60 s.
static void heapWatchdog(uint32_t now) {
    if ((int32_t)(now - g_lastHeapMs) < (int32_t)kHeapIntervalMs) {
        return;
    }
    g_lastHeapMs = now;
    char buf[64];
    usage::fmtHeap(buf, sizeof(buf),
                   (uint32_t)ESP.getFreeHeap(),
                   (uint32_t)ESP.getMinFreeHeap());
    serialLine(buf);
}

// --- Arduino entry points ---------------------------------------------------

void setup() {
    boardInit();
    Serial.begin(115200);

    char buf[64];
    usage::fmtBoot(buf, sizeof(buf), boardId(), psramBytes(),
                   buildId(), kFwVersion);
    serialLine(buf);

    drawBootScreen(buildId());

    // --- IMU + rotation (task 48) ------------------------------------------
    // Load persisted rotation BEFORE the first paint so a flipped device
    // never shows one upside-down frame.
    uint8_t rot = loadRotation();
    setRotation(rot);
    M5.Imu.begin(); // BMI270 at 0x68; safe to call even if IMU is disabled

    // --- power: wake cause + RTC snapshot restore ---
    WakeCause wakeCause = readWakeCause();
    uint16_t vbus = vbusMv();
    emitWake(wakeCause, vbus);

    // ORDER #27/#29: monitor for ext0 instant-wakes.  ext0 is no longer armed
    // (ORDER #29: PM1 GPIO1 conflicts with SDA), so this counter is dormant
    // — it stays for the follow-up experiment that re-tests ext0 via PM1 I2C
    // IRQ register reads.  Resets to 0 whenever VBUS >= 4000.
    if (wakeCause == WakeCause::Ext0 && vbus < 4000) {
        if (g_powerGuard.ext0InstantWakeCount < 255)
            g_powerGuard.ext0InstantWakeCount++;
    } else if (vbus >= 4000) {
        g_powerGuard.ext0InstantWakeCount = 0;
    }

    if (std::memcmp(g_wakeSnapshot.magic, "WAKE", 4) == 0) {
        // Warm boot from deep sleep: restore the cached model so a redraw
        // paints instantly without re-fetching.
        g_model = g_wakeSnapshot.model;
        g_hasModel = true;
        // ORDER #31: seed only the HTTP If-None-Match ETag cache (g_lastRev),
        // NOT g_view.lastRev.  Pre-seeding lastRev was the stuck-on-splash bug:
        // onSnapshot then saw "same rev" and skipped the paint.  Leaving lastRev
        // empty guarantees the first 200-fetch triggers exactly one redraw.
        for (size_t i = 0; i < 8; i++)
            g_lastRev[i] = g_model.rev[i];
        g_lastRev[8] = '\0';

        // ORDER #31: force one cached-snapshot paint on every warm boot,
        // regardless of wake cause (timer, ext1, or power-on after RTC magic).
        g_view.needsRedraw = true;
    }

    // ORDER #30/#31: timer wake on battery no longer sleeps immediately.
    // The device paints the cached snapshot (needsRedraw was set above),
    // then the 60 s timer backstop re-arms after the grace window expires.
    // This ensures the screen is fresh on every wake cycle.

    netBegin();

    // Initialise activity timers so the 30-min dim and 60-s heap watchdog
    // fire relative to boot, not relative to the zero millis().
    g_lastActivity = nowMs();
    g_lastHeapMs = nowMs();
    // Establish the VBUS baseline so the first loop iteration doesn't
    // misdetect a transition.  Non-RTC statics are zero (false) here, which
    // is the correct baseline for a battery wake.
    g_prevVbusPresent = vbusPresent();
}

void loop() {
    M5.update();
    uint32_t now = nowMs();
    netUpdate(now);

    // Drive OTA — may block for the duration of a chunk.  While an OTA
    // transfer is in flight, skip fetch/render/sleep entirely and paint
    // "OTA <pct>%" on screen (a transfer runs synchronously through one loop).
    otaHandle();
    if (otaInProgress()) {
        drawOtaStatus(otaPercent());
        return;
    }

    pollUpdate(now);
    buttonsUpdate(now);
    updateBrightness(now);
    heapWatchdog(now);
    pollBattery(now);

    // ORDER #30: detect USB→battery transition.  Unplugging is "activity" —
    // reset the grace anchor to now so the device stays awake 20 s after the
    // cable is pulled, letting the user see the last frame.
    bool vbusNow = vbusPresent();
    if (g_prevVbusPresent && !vbusNow) {
        g_graceActive = true;
        g_lastActivity = now;
    }
    g_prevVbusPresent = vbusNow;

    // ORDER #31: paint on warm-boot wake (needsRedraw) or the first boot
    // paint of the cached snapshot (paintedThisBoot guard).
    if (g_view.needsRedraw || (!g_paintedThisBoot && g_hasModel)) {
        redraw();
    }

    // --- IMU gesture polling (task 48) -----------------------------------
    // Poll every 20 ms while awake and no fetch/OTA is in flight.
    // The detector is pure C++ — hardware access is in the HAL.
    if (netUp() && !otaInProgress()) {
        imuGestureUpdate(now);
    }

    // ORDER #30: when grace is not yet active (pre-first-render on wake),
    // pass `now` as the anchor so powerDecide always returns StayAwake —
    // the device must not sleep before it has painted.
    uint32_t anchor = g_graceActive ? g_lastActivity : now;
    if (usage::powerDecide(vbusNow, now, anchor)
            == usage::PowerAction::SleepNow) {
        powerSleep();  // emits [SLEEP], teardown, arm wakes, never returns
    }

    delay(1);
}
