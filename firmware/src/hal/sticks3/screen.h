// firmware/src/hal/sticks3/screen.h — display HAL for the M5StickS3 screen.
#pragma once

#include "usage/battery.h"
#include "usage/render_plan.h"

namespace sticks3 {

// Draw the boot screen: black background, "AI USAGE" size 2 centred,
// build id size 1 at the bottom.
void drawBootScreen(const char* build);

// Full-screen redraw of the usage page.  The caller decides WHEN to call
// this (only when view.needsRedraw) — this function draws unconditionally.
// wifiOk controls the top-bar dot colour; batt drives the battery gauge.
void drawPlan(const usage::RenderPlan& plan, bool wifiOk,
              const sticks3::battery::BatteryView& batt);

// Paint a brief "refreshing…" status centred on screen (ORDER #38).
// Called when the user presses the refresh button before the POST /v1/refresh
// returns, so the device gives visual feedback that a refresh is in flight.
void drawRefreshStatus();

// Paint a full-screen OTA status: "OTA <pct>%" centred.
// Called every loop pass while a transfer is in flight.
void drawOtaStatus(uint8_t pct);

// Draw the battery indicator at the given (gaugeX, gaugeY) position, with
// the numeric label at (gaugeX - labelW - 2, labelY).  pct is 0..100 (or -1
// for unknown), onUsb controls the '+' suffix.  Uses the PTT-style gauge:
// green >40%, yellow >20%, red <=20%.
//
// gaugeX is the LEFT edge of the 32px gauge body; the 3px nub sits at
// gaugeX + 32.  gaugeY is the TOP of the 13px body.  labelY is the text
// baseline for the numeric label.
void drawBatteryGauge(int16_t gaugeX, int16_t gaugeY, int16_t labelY,
                      int pct, bool onUsb, bool known);

// Full-screen brightness-overlay gauge (ORDER #72 task 72).
// Paints a black background, a horizontal track with green fill proportional
// to currentRaw (0–255), an amber line marking the idle dim level, percentage
// labels (5/10/25/50/75/100%) under the track, and "brightness N%" at the top.
void drawBrightnessGauge(uint8_t currentRaw, uint8_t currentPercent,
                         uint8_t idleRaw);

} // namespace sticks3
