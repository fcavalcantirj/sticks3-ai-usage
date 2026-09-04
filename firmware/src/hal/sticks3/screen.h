// firmware/src/hal/sticks3/screen.h — display HAL for the M5StickS3 screen.
#pragma once

#include "usage/render_plan.h"

namespace sticks3 {

// Draw the boot screen: black background, "AI USAGE" size 2 centred,
// build id size 1 at the bottom.
void drawBootScreen(const char* build);

// Full-screen redraw of the usage page.  The caller decides WHEN to call
// this (only when view.needsRedraw) — this function draws unconditionally.
// wifiOk controls the top-bar dot colour.
void drawPlan(const usage::RenderPlan& plan, bool wifiOk);

} // namespace sticks3
