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
//   buttonsUpdate(now)       — BtnA(gpio11) page/hold-refresh, BtnB(gpio12) refresh
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
#include "usage/refresh.h"
#include "usage/serial_proto.h"
#include "usage/hold_flip.h"
#include "usage/battery.h"
#include "usage/freshness.h"

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
static uint32_t g_lastPoll = 0;
static uint32_t g_failCount = 0;

// ORDER #38: last device-initiated refresh (POST /v1/refresh) timestamp,
// for the 10 s throttle.  Zero = never refreshed.
static uint32_t g_lastRefreshMs = 0;

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

// --- hold-to-flip state (ORDER #53 REVISED, task 58) --------------------------
// BtnB hold-to-flip state machine replaces the IMU double-tap detector.
static sticks3::holdflip::HoldFlipDetector g_flipDetector;
static bool g_holdHintActive = false;  // true while a flip hint is on screen

// --- battery polling (task 49) ------------------------------------------------
// PTT-style gauge on the top bar; polled every 30 s, redrawn on change.
static const uint32_t kBatteryPollMs = 30000;  // 30 seconds
static battery::BatteryView g_batt;
static battery::BatteryView g_prevBatt;
static bool g_battEverPolled = false;
static uint32_t g_lastBatteryPoll = 0;

// ORDER #65 / #64: data-freshness tracking across fetches and deep sleeps.
// The device has no RTC, so it cannot compute "how old is this data".
// The server sends `age` (seconds since checked_at) on each 200 response.
// The device stores that age at fetch time and adds elapsed millis to get
// the effective age: effectiveAge = serverAgeAtFetch + (nowMs - lastFetchMs)/1000.
//
// ORDER #64 correction: the sleep duration must NOT be discarded on warm boot.
// g_lastFetchMs is a plain static — millis() resets on ESP32 deep-sleep wake —
// so before sleeping we compute and store the full effective age in RTC memory.
// On wake we restore that age (which already includes the sleep duration)
// and reset g_lastFetchMs to nowMs(), so the post-wake effective age starts
// from the correct stale value and grows from there.  A successful fetch
// after wake resets everything to the server's fresh age.
static uint32_t g_dataAgeAtFetch = 0;   // server-reported age at last 200 fetch
static uint32_t g_lastFetchMs = 0;      // millis() at last 200 fetch
RTC_DATA_ATTR uint32_t g_effectiveAgeAtSleep = 0; // full effective age at last powerSleep()
RTC_DATA_ATTR uint32_t g_rtcSleepStartMs = 0;     // RTC ms at powerSleep entry (task 65)
RTC_DATA_ATTR bool g_justSlept = false;           // true after powerSleep(), consumed on wake

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
    usage::buildPlan(g_model, g_view.page, buildId(), plan);

    // ORDER #65: override the fetch-time tier with the accumulated effective age.
    // effectiveAge = serverAgeAtFetch + elapsed ms since that fetch / 1000.
    uint32_t effectiveAge = usage::accumulateAge(g_dataAgeAtFetch,
                                                 nowMs() - g_lastFetchMs);
    plan.freshnessTier = usage::freshnessTier(effectiveAge, g_model.nextSec);

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
    int32_t mv = batteryVoltageMv();
    bool onUsb = vbusMv() > 4000;
    battery::BatteryView current{pct, onUsb, pct >= 0};

    bool changed = !g_battEverPolled ||
                   current.pct != g_prevBatt.pct ||
                   current.onUsb != g_prevBatt.onUsb;
    if (changed) {
        char buf[80];
        usage::fmtBatt(buf, sizeof(buf), current.pct,
                       (int)mv, current.onUsb ? 1 : 0);
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
                // ORDER #65: record the server-reported age at fetch time and
                // the millis() baseline for the accumulation.
                g_dataAgeAtFetch = model.age;
                g_lastFetchMs = nowMs();
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

// doDeviceRefresh implements ORDER #38/#49: POST /v1/refresh to trigger an
// immediate server-side poll, retry once on 202 after ~2 s, then fall back to
// the conditional GET (doFetch).  Throttled via the pure-core refreshThrottleOk.
// Both BtnA hold and BtnB click trigger this.
static void doDeviceRefresh(uint32_t now) {
    if (!usage::refreshThrottleOk(now, g_lastRefreshMs)) {
        return;  // within 10 s throttle window
    }
    g_lastRefreshMs = now;

    // ORDER #38: paint a brief "refreshing…" state for visual feedback.
    drawRefreshStatus();

    FetchResult refreshResult;
    if (refreshUpstream(refreshResult)) {
        // 202 = server poll still running; retry once after ~2 s.
        if (usage::shouldRefreshRetry(refreshResult.code)) {
            delay(2000);
            refreshUpstream(refreshResult);
        }
        // ORDER #65 fix: POST /v1/refresh forced a server-side re-poll, so the
        // data IS fresh at this instant even before the GET returns.  Reset the
        // age accumulator so a 304 on the conditional GET below shows green, not
        // red from the old age.  If the GET returns 200, doFetch() overwrites
        // g_dataAgeAtFetch with the server's fresh age anyway.
        g_dataAgeAtFetch = 0;
        g_lastFetchMs = nowMs();
    }
    // Regardless of POST outcome, do the conditional GET to pick up data.
    doFetch();
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

// drawFlipHint paints a brief "flip…" indicator in the footer area without a
// full screen wipe — just enough feedback that the hold gesture is active.
static void drawFlipHint();

// Buttons: BtnA (GPIO 11) short-press cycles pages; BtnA long-press refreshes.
// BtnB (GPIO 12) short-press refreshes; BtnB hold (>= 1500 ms) flips the
// screen 180°.  ORDER #53 REVISED: one threshold governs click and hold on
// BtnB — a short click still refreshes exactly as today.
// ORDER #49: emit [BTN] gpio=N <action> for physical-button clarity.
static void buttonsUpdate(uint32_t now) {
    // BtnA short press: cycle pages (only when we have a model).
    if (M5.BtnA.wasClicked() && g_hasModel) {
        usage::nextPage(g_view, g_model);
        g_lastActivity = now;
        char buf[64];
        usage::fmtBtn(buf, sizeof(buf), 11, "click page");
        serialLine(buf);
    }

    // BtnA long press: POST /v1/refresh + retry + conditional GET.
    if (M5.BtnA.wasHold() && netUp()) {
        doDeviceRefresh(now);
        g_lastActivity = now;
        char buf[64];
        usage::fmtBtn(buf, sizeof(buf), 11, "hold refresh");
        serialLine(buf);
    }

    // BtnB: click = refresh, hold (>= 1500 ms) = flip.  The hold state machine
    // in usage/hold_flip.h disambiguates click vs hold using a single threshold.
    bool btnBPressed = M5.BtnB.isPressed();
    sticks3::holdflip::FlipAction action = g_flipDetector.feed(btnBPressed, now);

    switch (action) {
        case sticks3::holdflip::FlipAction::click:
            // BtnB short press: POST /v1/refresh + retry + conditional GET.
            if (netUp()) {
                doDeviceRefresh(now);
            }
            g_lastActivity = now;
            {
                char buf[64];
                usage::fmtBtn(buf, sizeof(buf), 12, "click refresh");
                serialLine(buf);
            }
            break;

        case sticks3::holdflip::FlipAction::hint:
            // Hold reached 500 ms — show a brief hint without a full repaint.
            g_holdHintActive = true;
            drawFlipHint();
            break;

        case sticks3::holdflip::FlipAction::flip: {
            // Hold reached 1500 ms — flip 180°, persist, force redraw.
            g_holdHintActive = false;
            // The screen rotates regardless of whether data has been fetched.
            uint8_t rot = (uint8_t)g_flipDetector.rotation();
            setRotation(rot);
            saveRotation(rot);
            {
                char buf[64];
                usage::fmtGesture(buf, sizeof(buf), rot);
                serialLine(buf);
            }
            g_view.needsRedraw = true;
            g_lastActivity = now;
            break;
        }

        default:
            // If the button is not held and a hint was active, clear it.
            if (!btnBPressed && g_holdHintActive) {
                g_holdHintActive = false;
                // The next redraw() will paint normally.
                if (g_view.needsRedraw || (!g_paintedThisBoot && g_hasModel)) {
                    redraw();
                }
            }
            break;
    }
}

// drawFlipHint paints a brief "flip…" indicator in the footer area without a
// full screen wipe — just enough feedback that the hold gesture is active.
static void drawFlipHint() {
    M5.Display.setTextColor(0xFD20);  // amber
    const char* hint = "hold to flip 180°";
    int16_t w = M5.Display.width();
    int16_t tw = M5.Display.textWidth(hint);
    M5.Display.setCursor(w - tw - 4, 120);
    M5.Display.println(hint);
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

    // --- power: wake cause + VBUS (read BEFORE any screen draw) ----------------
    // ORDER #60 (task 63d): a timer wake on battery must NOT raise the
    // backlight or paint.  Only ext1 (button — someone is there) or VBUS
    // present should light the screen, because drawBootScreen and the warm-
    // boot paint below would otherwise flash the panel in a dark room for
    // an audience of nobody.
    WakeCause wakeCause = readWakeCause();
    uint16_t vbus = vbusMv();
    emitWake(wakeCause, vbus);

    // True when the screen should be lit + painted: any non-timer wake, or a
    // timer wake that found USB plugged in.
    bool lightScreen = (wakeCause != WakeCause::Timer) || (vbus >= 4000);
    if (lightScreen) {
        setBrightness(kBrightnessActive);
        drawBootScreen(buildId());
    }

    // --- rotation (ORDER #53 REVISED, task 58) -------------------------------
    // Load persisted rotation BEFORE the first paint so a flipped device
    // never shows one upside-down frame.
    uint8_t rot = loadRotation();
    setRotation(rot);
    // No IMU init — the BMI270 is not polled.  Wake is via ext1 buttons + timer.

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

        // On warm boot, restore the effective age that was computed and stored
        // BEFORE the sleep (see the powerSleep call-site below).  Before sleeping
        // we read the RTC clock (esp_timer_get_time, which survives deep sleep)
        // and compute the full effective age — server-reported age at last fetch
        // + elapsed time since that fetch, INCLUDING the sleep duration we are
        // about to incur — into g_effectiveAgeAtSleep.  On wake we read the RTC
        // clock again and ADD the real sleep duration to the effective age, so
        // even if the pre-sleep computation was slightly stale the lamp reflects
        // the true gap.  No special-casing of the wake cause: a timer wake and a
        // button wake both restore the same accumulated age.  g_lastFetchMs is
        // reset to nowMs() so post-wake elapsed time accumulates from the wake
        // instant onward.
        if (g_justSlept) {
            uint32_t rtcEndMs = rtcNowMs();
            uint32_t sleepMs = rtcEndMs - g_rtcSleepStartMs;
            g_dataAgeAtFetch = usage::accumulateAge(g_effectiveAgeAtSleep, sleepMs);
            g_justSlept = false;  // consume so a cold boot doesn't use stale data
        } else {
            g_dataAgeAtFetch = g_effectiveAgeAtSleep;
        }
        g_lastFetchMs = nowMs();

        // ORDER #60 (task 63d): ORDER #31 forces one paint on warm boot, BUT
        // only when the screen is lit.  A timer wake on battery skips the
        // paint — the device fetches, refreshes the cached snapshot, and
        // returns to sleep dark.  ext1 (button) and VBUS both light the screen.
        g_view.needsRedraw = lightScreen;
    }

    // On a timer wake that does not light the screen (lightScreen == false),
    // the device fetches via pollUpdate in loop() and returns to sleep dark.
    // The 12 h timer backstop (ORDER #60) re-arms after the grace window.

    // ORDER #30/#31: timer wake on battery no longer sleeps immediately — it
    // fetches, refreshes the cached snapshot if stale, then re-sleeps after the
    // 20 s grace window. The 12 h backstop (ORDER #60) re-arms after grace.

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

    // ORDER #30: when grace is not yet active (pre-first-render on wake),
    // pass `now` as the anchor so powerDecide always returns StayAwake —
    // the device must not sleep before it has painted.
    uint32_t anchor = g_graceActive ? g_lastActivity : now;
    if (usage::powerDecide(vbusNow, now, anchor)
            == usage::PowerAction::SleepNow) {
        // Task 65: read the RTC clock immediately before powerSleep() so we can
        // compute the real sleep duration on wake (esp_timer_get_time survives
        // deep sleep; millis() resets).  Store the full effective age — server-
        // reported age at last fetch + elapsed since that fetch, INCLUDING the
        // sleep we are about to incur — into RTC memory.
        g_rtcSleepStartMs = rtcNowMs();
        g_justSlept = true;
        g_effectiveAgeAtSleep = usage::accumulateAge(g_dataAgeAtFetch,
                                                     now - g_lastFetchMs);
        powerSleep();  // emits [SLEEP], teardown, arm wakes, never returns
    }

    delay(1);
}
