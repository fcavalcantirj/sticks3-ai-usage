// firmware/src/hal/sticks3/board.h — M5StickS3 board HAL.
//
// Only this file (and firmware/src/main.cpp) include <M5Unified.h>.
// All Arduino/M5 calls are isolated here so firmware/src/usage/ stays
// pure C++17 (no M5/Arduino headers).
#pragma once

#include <cstdint>

namespace sticks3 {

// Initialise the M5StickS3: config flags, M5.begin, display rotation,
// brightness, button hold thresholds.  Does NOT call Serial.begin —
// callers do that so they can pick the baud.
void boardInit();

// Board enum value (26 = board_M5StickS3).
int boardId();

// PSRAM size in bytes.
uint32_t psramBytes();

// Build identity string (USAGED_BUILD_ID macro, or "unknown").
const char* buildId();

// Milliseconds since boot (wraps millis()).
uint32_t nowMs();

// Print a line to the serial monitor.
void serialLine(const char* s);

// Set the LCD backlight brightness (0–255).
void setBrightness(uint8_t level);

} // namespace sticks3
