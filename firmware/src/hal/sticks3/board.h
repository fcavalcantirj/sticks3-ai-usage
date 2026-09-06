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

// RTC-backed epoch seconds that survive deep sleep (gettimeofday via ESP-IDF
// RTC clock).  Use to measure real elapsed time across a sleep: call
// immediately before powerSleep() and again right after wake.
// NOTE: esp_timer_get_time() does NOT survive deep sleep on this board —
// use rtcNowSec(), not nowMs(), for the sleep-duration computation.
uint32_t rtcNowSec();

// Print a line to the serial monitor.
void serialLine(const char* s);

// Set the LCD backlight brightness (0–255).
void setBrightness(uint8_t level);

// Apply a screen rotation (1 = upright, 3 = 180-degree flip).
void setRotation(uint8_t r);

// Load the persisted rotation from NVS (namespace "usaged", key "rot").
// Returns 1 (upright) if no saved value exists.
uint8_t loadRotation();

// Persist the current rotation to NVS.
void saveRotation(uint8_t r);

// Load the persisted brightness level index (0-5) from NVS
// (namespace "usaged", key "bright").  Returns the 25% default index (2)
// if no saved value exists or the stored value is corrupt.
uint8_t loadBrightness();

// Persist the brightness level index to NVS.
void saveBrightness(uint8_t idx);

} // namespace sticks3
