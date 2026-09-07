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
//   buttonsUpdate(now)       — BtnA(gpio11) page/double=brightness, HOLD reserved,
//                              BtnB(gpio12) refresh/stepdown/hold=flip
//   updateBrightness(now)    — dim after 30 min idle, full in brightness mode
//   heapWatchdog(now)        — [HEAP] line every 60 s
//   if (view.needsRedraw || (!paintedThisBoot && hasModel)) { redraw }
//   powerDecide(vbus, now, graceActive ? lastActivity : now) → SleepNow? powerSleep()
//   delay(1)
#include <M5Unified.h>

#include "hal/sticks3/board.h"
#include "hal/sticks3/creds.h"
#include "hal/sticks3/fetch.h"
#include "hal/sticks3/net.h"
#include "hal/sticks3/portal.h"
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
#include "usage/brightness.h"
#include "usage/provision.h"

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
static uint8_t g_currentBrightness = 0;

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

// ORDER #72 task 72: brightness controller — pure-C++17 level stepper with
// a timed adjustment mode entered via BtnA double-click.  Persisted to NVS
// via loadBrightness()/saveBrightness().
brightness::BrightnessController g_brightCtrl;

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
RTC_DATA_ATTR uint32_t g_rtcSleepStartSec = 0;    // RTC epoch sec at powerSleep entry (task 65)
RTC_DATA_ATTR bool g_justSlept = false;           // true after powerSleep(), consumed on wake

// --- provisioning (tasks 76/77) ----------------------------------------------
// The single place the portal-vs-join decision is made.  The rule lives in the
// pure core (usage/provision.h) and is host-tested, so a device that is half
// configured raises the setup portal instead of retrying a configuration that
// cannot work — the failure that would otherwise need a USB cable to escape.
static usage::provision::Machine g_provision;

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
    // ORDER #72 task 72: in brightness adjustment mode, draw the gauge overlay
    // instead of the usage plan.
    if (g_brightCtrl.inMode()) {
        drawBrightnessGauge(g_brightCtrl.rawLevel(),
                           g_brightCtrl.percent(),
                           g_brightCtrl.idleRaw(),
                           g_brightCtrl.levelIdx());
        char buf[64];
        std::snprintf(buf, sizeof(buf),
                      "[RENDER] brightness %d%%", (int)g_brightCtrl.percent());
        serialLine(buf);

        g_view.needsRedraw = false;
        g_lastActivity = nowMs();
        g_graceActive = true;
        return;
    }

    usage::RenderPlan plan;
    usage::buildPlan(g_model, g_view.page, buildId(), plan);

    // ORDER #65: override the fetch-time tier with the accumulated effective age.
    // effectiveAge = serverAgeAtFetch + elapsed ms since that fetch / 1000.
    // If the sleep duration was unknowable (clock wrap/failure), g_dataAgeAtFetch
    // is kSleepUnknown (MAX_UINT32) and we propagate it so the lamp shows RED.
    uint32_t effectiveAge;
    if (g_dataAgeAtFetch == usage::kSleepUnknown) {
        effectiveAge = usage::kSleepUnknown;
    } else {
        effectiveAge = usage::accumulateAge(g_dataAgeAtFetch,
                                             nowMs() - g_lastFetchMs);
    }
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
    bool charging = batteryCharging();  // ORDER #71 (task 71)
    battery::BatteryView current{pct, onUsb, pct >= 0, charging};

    bool changed = !g_battEverPolled ||
                   current.pct != g_prevBatt.pct ||
                   current.onUsb != g_prevBatt.onUsb ||
                   current.charging != g_prevBatt.charging;  // ORDER #71
    if (changed) {
        char buf[80];
        usage::fmtBatt(buf, sizeof(buf), current.pct,
                       (int)mv, current.onUsb ? 1 : 0,
                       current.charging ? 1 : 0);  // ORDER #71
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
    // ORDER #65: compute the effective data age at this moment and send it as
    // ?age_s=<n> so the server can verify the sleep-duration fix on the wire.
    // age_s = serverAgeAtLastFetch + elapsed_ms_since_last_fetch / 1000.
    // If the sleep duration was unknowable, send a sentinel that the server
    // logs distinctly.
    uint32_t ageS;
    if (g_dataAgeAtFetch == usage::kSleepUnknown) {
        ageS = usage::kSleepUnknown;
    } else {
        ageS = usage::accumulateAge(g_dataAgeAtFetch, nowMs() - g_lastFetchMs);
    }

    FetchResult result;
    if (fetchUsage(g_lastRev, result, ageS)) {
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

// Buttons: BtnA (GPIO 11) single-click cycles pages, double-click enters
// brightness mode.  BtnB (GPIO 12) click refreshes (or steps brightness down
// in mode), hold (>= 1500 ms) flips the screen.
// ORDER #53 REVISED: one threshold governs click and hold on BtnB.
// ORDER #72 task 72: brightness mode entered via double-click, exited via
// 5 s inactivity timeout.  Inside mode, BtnA click = step up, BtnB click = step down.
// ORDER #72: BtnA HOLD is RESERVED for a future AI-agent action.  It is not
// bound to any handler here.  Felipe reported blue-hold "does nothing" — the
// root cause is a 600 ms hold threshold (board.cpp:47), too short for a
// deliberate hold: the click detector fires first and consumes the event
// before wasHold() can.  A 1500 ms threshold (matching BtnB) would fix it,
// but the hold now belongs to the agent, so the branch is simply removed.
// ORDER #49: emit [BTN] gpio=N <action> for physical-button clarity.
// Step 8 fix: [BTN] line emitted outside netUp() gate so button activity is
// always logged.
static void buttonsUpdate(uint32_t now) {
    bool inBrightMode = g_brightCtrl.inMode();

    // --- BtnA: behavior depends on brightness mode ---
    if (inBrightMode) {
        // In brightness mode: wasSingleClicked = stepUp.
        if (M5.BtnA.wasSingleClicked()) {
            g_brightCtrl.stepUp(now);
            g_view.needsRedraw = true;
            g_lastActivity = now;
            char buf[64];
            usage::fmtBtn(buf, sizeof(buf), 11, "step up");
            serialLine(buf);
        }
        // BtnA HOLD is reserved (ORDER #72) — no handler here.
    } else {
        // Normal mode: wasSingleClicked = page cycle (disambiguates from
        // double-click for brightness entry — ~250 ms latency tradeoff).
        if (M5.BtnA.wasSingleClicked() && g_hasModel) {
            usage::nextPage(g_view, g_model);
            g_lastActivity = now;
            char buf[64];
            usage::fmtBtn(buf, sizeof(buf), 11, "click page");
            serialLine(buf);
        }
        // Double-click enters brightness mode.
        if (M5.BtnA.wasDoubleClicked()) {
            g_brightCtrl.enterMode(now);
            g_view.needsRedraw = true;
            g_lastActivity = now;
            setBrightness(255);  // full brightness for gauge visibility
            g_currentBrightness = 255;
            char buf[64];
            usage::fmtBtn(buf, sizeof(buf), 11, "brightness enter");
            serialLine(buf);
        }
        // BtnA HOLD is reserved (ORDER #72) — no handler here;
        // refresh is triggered by BtnB click or POST /v1/refresh on the web page.
    }

    // --- BtnB: hold_flip detector disambiguates click vs hold ---
    // Click = stepDown (in brightness mode) or refresh (normal mode).
    // Hold (>= 1500 ms) = flip screen (unlocks in both modes).
    bool btnBPressed = M5.BtnB.isPressed();
    sticks3::holdflip::FlipAction action = g_flipDetector.feed(btnBPressed, now);

    switch (action) {
        case sticks3::holdflip::FlipAction::click:
            if (inBrightMode) {
                g_brightCtrl.stepDown(now);
                g_view.needsRedraw = true;
                g_lastActivity = now;
                {
                    char buf[64];
                    usage::fmtBtn(buf, sizeof(buf), 12, "step down");
                    serialLine(buf);
                }
            } else {
                if (netUp()) {
                    doDeviceRefresh(now);
                }
                g_lastActivity = now;
                {
                    char buf[64];
                    usage::fmtBtn(buf, sizeof(buf), 12, "click refresh");
                    serialLine(buf);
                }
            }
            // Hint is also dismissed on click.
            g_holdHintActive = false;
            break;

        case sticks3::holdflip::FlipAction::hint:
            g_holdHintActive = true;
            drawFlipHint();
            break;

        case sticks3::holdflip::FlipAction::flip: {
            g_holdHintActive = false;
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
            // Button released with no hold → dismiss hint, clear hint line.
            if (!btnBPressed && g_holdHintActive) {
                g_holdHintActive = false;
                if (g_view.needsRedraw || (!g_paintedThisBoot && g_hasModel)) {
                    redraw();
                }
            }
            break;
    }

    // ORDER #72 task 72: brightness mode timeout.
    if (inBrightMode && g_brightCtrl.shouldExit(now)) {
        g_brightCtrl.exitMode();
        sticks3::saveBrightness(g_brightCtrl.levelIdx());
        g_view.needsRedraw = true;
        g_lastActivity = now;
        char buf[64];
        usage::fmtBtn(buf, sizeof(buf), 11, "brightness exit");
        serialLine(buf);
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

// Brightness policy (ORDER #72 task 72):
//   - In brightness mode: full backlight (255) so the gauge is visible.
//   - Idle (30 min no activity): idleRaw() = active / 4, clamped >= 1.
//   - Active: rawLevel() from the persisted level.
// Only calls setBrightness() on change to avoid unnecessary I2C writes.
static void updateBrightness(uint32_t now) {
    bool idle = (int32_t)(now - g_lastActivity) >= (int32_t)kBrightnessIdleMs;
    uint8_t target;
    if (g_brightCtrl.inMode()) {
        target = 255;  // full brightness during adjustment
    } else if (idle) {
        target = g_brightCtrl.idleRaw();
    } else {
        target = g_brightCtrl.rawLevel();
    }
    if (target != g_currentBrightness) {
        setBrightness(target);
        g_currentBrightness = target;
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
        // ORDER #72 task 72: load persisted brightness level into the controller,
        // then apply the raw value to the backlight.
        g_brightCtrl.setLevel(sticks3::loadBrightness());
        setBrightness(g_brightCtrl.rawLevel());
        g_currentBrightness = g_brightCtrl.rawLevel();
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
        // we read the RTC clock (gettimeofday, maintained across deep sleep by
        // ESP-IDF's RTC) and compute the full effective age — server-reported
        // age at last fetch + elapsed time since that fetch, INCLUDING the sleep
        // duration we are about to incur — into g_effectiveAgeAtSleep.  On wake
        // we read the RTC clock again and ADD the real sleep duration (computed
        // by sleepDuration() in the pure core, which clamps wraps and absurd
        // deltas to UNKNOWN) to the effective age.
        //
        // ORDER #65 defect fix: RTC_DATA_ATTR survives an OTA software-reset
        // reboot, not only a deep-sleep wake.  If g_justSlept was set before a
        // reboot, g_rtcSleepStartSec is stale and computing a sleep duration
        // would invent phantom elapsed time (the observed ~400s offset).
        // Gate on the actual wake cause: only Ext1 (button) and Timer (backstop)
        // are real deep-sleep wakes; PowerOn (cold boot / OTA reboot) is not.
        // shouldApplySleepDuration() is pure-C++17 and host-tested.
        bool fromDeepSleepWake = (wakeCause == WakeCause::Ext1
                                  || wakeCause == WakeCause::Timer);
        if (usage::shouldApplySleepDuration(g_justSlept, fromDeepSleepWake)) {
            uint32_t rtcEndSec = rtcNowSec();
            uint32_t sleepSec = usage::sleepDuration(rtcEndSec, g_rtcSleepStartSec);
            if (sleepSec == usage::kSleepUnknown) {
                // Clock did not survive or produced an absurd delta — treat
                // age as unknown (RED) rather than inventing a number.
                g_dataAgeAtFetch = usage::kSleepUnknown;
            } else {
                g_dataAgeAtFetch = usage::accumulateAge(g_effectiveAgeAtSleep, sleepSec * 1000);
            }
            g_justSlept = false;  // consume so a cold boot doesn't use stale data
        } else {
            // Cold boot, software reset (OTA), or ext0: RTC_DATA_ATTR survives
            // these, so g_justSlept may be stale from a previous sleep.  Use the
            // effective age from the last sleep without adding a phantom duration,
            // and consume g_justSlept so stale state does not persist.
            g_dataAgeAtFetch = g_effectiveAgeAtSleep;
            g_justSlept = false;
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

    // --- credentials (task 76) ----------------------------------------------
    // Credentials come from NVS, never from the compiled binary.  secrets.h was
    // a compile-time header, so the SSID, the Wi-Fi password, the agent address,
    // the device token and the OTA password were all plaintext strings inside
    // firmware.bin — verified with `strings` — which is why the binary could not
    // be published anywhere.
    //
    // When secrets.h IS present (a developer build) it is used ONCE, as a
    // first-boot seed for an empty store, and never as a runtime source.  The
    // production build compiles with no secrets.h at all and starts unprovisioned.
    usage::provision::Record creds;
    bool provisioned = credsLoad(creds);
    if (!provisioned && credsSeedFromSecretsIfEmpty()) {
        provisioned = credsLoad(creds);
        serialLine("[CREDS] seeded from secrets.h (developer build)");
    }

    // Log the SHAPE of the record, never its contents.
    std::snprintf(buf, sizeof(buf),
                  "[CREDS] provisioned=%d ssid=%d pass=%d host=%d token=%d ota=%d port=%u",
                  provisioned ? 1 : 0, (int)std::strlen(creds.ssid),
                  (int)std::strlen(creds.pass), (int)std::strlen(creds.host),
                  (int)std::strlen(creds.token), (int)std::strlen(creds.otaPass),
                  (unsigned)creds.port);
    serialLine(buf);

    // The machine, not this function, decides between joining and the portal —
    // the same edge that will send a device back to the portal after N failed
    // joins has to agree with the one a cold boot takes.
    if (g_provision.onBoot(provisioned) == usage::provision::State::Joining) {
        fetchConfigure(creds);
        netBegin(creds);
    } else {
        // Task 77: no usable record, so the device raises its own access point
        // and asks.  A portal on a dark screen is useless and a timer wake on
        // battery deliberately leaves the backlight off (ORDER #60) — but an
        // unprovisioned device has nothing to save power for, so light it.
        if (!lightScreen) {
            g_brightCtrl.setLevel(sticks3::loadBrightness());
            setBrightness(g_brightCtrl.rawLevel());
            g_currentBrightness = g_brightCtrl.rawLevel();
        }
        serialLine("[CREDS] unprovisioned — raising the setup portal");
        if (!portalBegin(creds)) {
            char err[64];
            usage::fmtErr(err, sizeof(err), "portal failed to start");
            serialLine(err);
        }
    }

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

    // Task 77: while the portal is up it owns the radio and the whole pass —
    // the same shape the OTA branch below uses.  It must come BEFORE
    // netUpdate(), which would otherwise re-issue WiFi.begin() with the empty
    // credentials every 30 s and tear the access point out from under the
    // phone; and it must skip the sleep decision, because a device being set
    // up by hand cannot deep-sleep mid-conversation.
    if (portalActive()) {
        portalUpdate(now);
        if (portalOutcome() == PortalOutcome::Joined) {
            // DECISION (task 77 asks for it explicitly): the AP-to-station
            // transition is a REBOOT, not a live mode switch.  Both work — the
            // spike measured WiFi.mode(WIFI_STA) from AP_STA returning true
            // with the station connection untouched — but a restart lands the
            // device on the ordinary provisioned boot path (credsLoad,
            // fetchConfigure, netBegin, OTA armed) with no second wiring to
            // keep correct, and cold boot to fully operational was measured at
            // 3 s.  The NVS write happened before the join, and a write
            // immediately before a restart was measured to survive it.
            serialLine("[PORTAL] provisioned — restarting");
            portalEnd();
            ESP.restart();
        }
        heapWatchdog(now);
        delay(1);
        return;
    }

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
    // ORDER #72: in brightness mode, only paint on needsRedraw (the gauge
    // doesn't need the g_paintedThisBoot boot paint).
    bool needsPaint = g_view.needsRedraw;
    if (!g_brightCtrl.inMode()) {
        needsPaint = needsPaint || (!g_paintedThisBoot && g_hasModel);
    }
    if (needsPaint) {
        redraw();
    }

    // ORDER #30: when grace is not yet active (pre-first-render on wake),
    // pass `now` as the anchor so powerDecide always returns StayAwake —
    // the device must not sleep before it has painted.
    uint32_t anchor = g_graceActive ? g_lastActivity : now;
    if (usage::powerDecide(vbusNow, now, anchor)
            == usage::PowerAction::SleepNow) {
        // Task 65: read the RTC clock (gettimeofday, maintained across deep
        // sleep by ESP-IDF's RTC) immediately before powerSleep() so we can
        // compute the real sleep duration on wake.  Store the full effective
        // age — server-reported age at last fetch + elapsed since that fetch,
        // INCLUDING the sleep we are about to incur — into RTC memory.
        // NOTE: esp_timer_get_time()/millis() do NOT survive deep sleep on
        // this board — gettimeofday is the correct primitive.
        g_rtcSleepStartSec = rtcNowSec();
        g_justSlept = true;
        g_effectiveAgeAtSleep = usage::accumulateAge(g_dataAgeAtFetch,
                                                     now - g_lastFetchMs);
        powerSleep();  // emits [SLEEP], teardown, arm wakes, never returns
    }

    delay(1);
}
