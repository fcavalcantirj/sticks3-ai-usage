// firmware/src/hal/sticks3/screen.h — display HAL for the M5StickS3 screen.
#pragma once

#include "hal/sticks3/bleprov.h"
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
// Paints a black background, a horizontal track with segmented green fill
// (evenly spaced by level index, not by raw PWM), a pip marking the current
// level, an amber pip at the idle dim position, percentage labels (5/10/25/50/
// 75/100%) under the track, and "brightness N%" at the top.
// ORDER #74 task 74: tick positions are evenly spaced by index, not by raw.
void drawBrightnessGauge(uint8_t currentRaw, uint8_t currentPercent,
                         uint8_t idleRaw, uint8_t levelIdx);

// Full-screen BLE zero-config setup status.
//
// hal/sticks3/bleprov.* deliberately DOES NOT DRAW — it returns state, the
// passkey, a fixed message and a transfer count, and the screen is decided
// here, the same split every other screen in this file uses.
//
// THE PASSKEY SCREEN CARRIES NOTHING BUT THE SIX DIGITS.  At size 6 the
// default font is 36x48 px per glyph, so six digits are exactly 216x48 on a
// 240x135 panel: x=(240-216)/2=12, y=(135-48)/2=44 (43.5 rounded up).  That
// is a deliberate decision, not a layout accident — the owner is reading
// those digits off a 1.14" screen and typing them into a macOS dialog, and
// anything else on screen is something to misread them against.  The digits
// are drawn HERE and never printed to the serial line, exactly as the
// portal's AP passphrase is.
//
// Every other state gets an ordinary informational screen.  `received` and
// `declared` are progress only, never content.
void drawBleSetup(BleProvState state, const char* name, const char* passkey,
                  const char* message, uint16_t received, uint16_t declared);

} // namespace sticks3
