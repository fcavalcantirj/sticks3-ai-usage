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
//   if (view.needsRedraw) { buildPlan → drawPlan → [RENDER]; needsRedraw = false }
//   powerDecide(vbus, now, lastActivity) → SleepNow? powerSleep()
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

static uint32_t g_lastActivity = 0;  // last rev change or button press
static uint32_t g_lastHeapMs = 0;    // last [HEAP] emit
static bool g_brightnessDimmed = false;

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
    drawPlan(plan, netUp());

    char buf[64];
    usage::fmtRender(buf, sizeof(buf), plan.page, plan.lineCount,
                     g_model.rev);
    serialLine(buf);

    g_view.needsRedraw = false;
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
        if (g_failCount >= kMaxFails) {
            char buf[80];
            usage::fmtErr(buf, sizeof(buf), "restart after 12 failures");
            serialLine(buf);
            ESP.restart();
        }
        g_nextPoll = nowMs() + pollInterval();
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

// Buttons: BtnA cycles pages, BtnB triggers an immediate fetch.
static void buttonsUpdate(uint32_t now) {
    // BtnA: cycle pages (only when we have a model).
    if (M5.BtnA.wasClicked() && g_hasModel) {
        usage::nextPage(g_view, g_model);
        g_lastActivity = now;
    }

    // BtnB: immediate fetch.
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

    // --- power: wake cause + RTC snapshot restore ---
    WakeCause wakeCause = readWakeCause();
    uint16_t vbus = vbusMv();
    emitWake(wakeCause, vbus);

    if (std::memcmp(g_wakeSnapshot.magic, "WAKE", 4) == 0) {
        // Warm boot from deep sleep: restore the cached model so a redraw
        // paints instantly without re-fetching.
        g_model = g_wakeSnapshot.model;
        g_hasModel = true;
        for (size_t i = 0; i < 8; i++)
            g_lastRev[i] = g_model.rev[i];
        g_lastRev[8] = '\0';
        for (size_t i = 0; i < 8; i++)
            g_view.lastRev[i] = g_model.rev[i];
        g_view.lastRev[8] = '\0';

        // Button wake: flag a redraw so the cached snapshot is painted.
        if (wakeCause == WakeCause::Ext1) {
            g_view.needsRedraw = true;
        }
    }

    // Timer wake on battery with no USB: skip the screen and go right back
    // to sleep.  A missed IRQ or button press will wake us again.
    // powerSleep() emits [SLEEP] internally and never returns.
    if (wakeCause == WakeCause::Timer && !vbusPresent()) {
        powerSleep();
    }

    netBegin();

    // Initialise activity timers so the 30-min dim and 60-s heap watchdog
    // fire relative to boot, not relative to the zero millis().
    g_lastActivity = nowMs();
    g_lastHeapMs = nowMs();
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

    // Redraw only when the view flagged it (rev change or page cycle).
    if (g_view.needsRedraw) {
        redraw();
    }

    // Power management: on battery, deep sleep after the grace window
    // (20 s since last fetch/render/button).  vbusPresent() is a cheap
    // register read; powerDecide() is pure and never sleeps on USB.
    if (usage::powerDecide(vbusPresent(), now, g_lastActivity)
            == usage::PowerAction::SleepNow) {
        powerSleep();  // emits [SLEEP], teardown, arm wakes, never returns
    }

    delay(1);
}
