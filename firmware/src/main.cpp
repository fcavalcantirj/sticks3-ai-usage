// firmware/src/main.cpp — Arduino entry points for the StickS3 usage monitor.
//
// Includes <M5Unified.h> (allowed for main.cpp) and delegates all M5 calls to
// the HAL layer; usage/ formatters stay pure C++17.
#include <M5Unified.h>

#include "hal/sticks3/board.h"
#include "hal/sticks3/fetch.h"
#include "hal/sticks3/net.h"
#include "hal/sticks3/screen.h"
#include "usage/model.h"
#include "usage/render_plan.h"
#include "usage/serial_proto.h"

#include <cstdio>

using namespace sticks3;

// --- poll configuration -----------------------------------------------------

static const uint32_t kPollMs = 300000;        // 5 minutes
static const uint32_t kFirstFetchDelayMs = 3000;  // 3 s after netUp
static const uint32_t kBackoffBaseMs = 30000;   // 30 s
static const uint32_t kMaxFails = 12;

// --- poll state -------------------------------------------------------------

static usage::View g_view;
static usage::Model g_model;
static char g_lastRev[9] = "";
static bool g_hasModel = false;
static uint32_t g_netUpAt = 0;
static bool g_netWasUp = false;
static uint32_t g_nextPoll = 0;
static uint32_t g_failCount = 0;

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
                g_model = model;
                g_hasModel = true;
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

void setup() {
    boardInit();
    Serial.begin(115200);

    char buf[64];
    usage::fmtBoot(buf, sizeof(buf), boardId(), psramBytes(), buildId());
    serialLine(buf);

    drawBootScreen(buildId());
    netBegin();
}

void loop() {
    M5.update();
    uint32_t now = nowMs();
    netUpdate(now);

    if (netUp()) {
        if (!g_netWasUp) {
            g_netWasUp = true;
            g_netUpAt = now;
        }

        // First fetch 3 s after netUp becomes true.
        if (g_nextPoll == 0 &&
            (int32_t)(now - g_netUpAt) >= (int32_t)kFirstFetchDelayMs) {
            doFetch();
        }

        // Periodic poll.
        if (g_nextPoll != 0 && (int32_t)(now - g_nextPoll) >= 0) {
            doFetch();
        }

        // Immediate fetch on BtnB click.
        if (M5.BtnB.wasClicked()) {
            doFetch();
        }
    } else {
        g_netWasUp = false;
    }

    // Redraw on data change (set by onSnapshot) or button press.
    if (g_hasModel && g_view.needsRedraw) {
        redraw();
    }

    if (M5.BtnA.wasClicked() && g_hasModel) {
        usage::nextPage(g_view, g_model);
        redraw();
    }

    delay(1);
}
