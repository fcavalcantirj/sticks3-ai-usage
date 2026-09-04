// firmware/src/hal/sticks3/screen.h — display HAL for the M5StickS3 screen.
#pragma once

namespace sticks3 {

// Draw the boot screen: black background, "AI USAGE" size 2 centred,
// build id size 1 at the bottom.
void drawBootScreen(const char* build);

} // namespace sticks3
