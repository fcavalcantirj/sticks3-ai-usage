// firmware/src/hal/sticks3/board.cpp — M5StickS3 board HAL implementation.
#include "hal/sticks3/board.h"

#ifndef USAGED_BUILD_ID
#define USAGED_BUILD_ID "unknown"
#endif

#include <M5Unified.h>
#include <Preferences.h>

namespace sticks3 {

void boardInit() {
    auto cfg = M5.config();
    cfg.internal_imu = false;  // ORDER #53 REVISED: no IMU polling
    cfg.internal_spk = false;
    cfg.internal_mic = false;
    cfg.output_power = false;
    M5.begin(cfg);

    M5.Display.setRotation(1);
    // ORDER #60 (task 63): brightness is set in setup() AFTER reading the wake
    // cause.  A timer wake must not raise the backlight.

    M5.BtnA.setHoldThresh(600);
    M5.BtnB.setHoldThresh(1500);  // ORDER #53 REVISED: hold-to-flip
}

int boardId() {
    return (int)M5.getBoard();
}

uint32_t psramBytes() {
    return (uint32_t)ESP.getPsramSize();
}

const char* buildId() {
    return USAGED_BUILD_ID;
}

uint32_t nowMs() {
    return (uint32_t)millis();
}

void serialLine(const char* s) {
    Serial.println(s);
}

void setBrightness(uint8_t level) {
    M5.Display.setBrightness(level);
}

// --- IMU + rotation ----------------------------------------------------------

// Apply a screen rotation value (1 = upright, 3 = 180-degree flip).
void setRotation(uint8_t r) {
    M5.Display.setRotation(r);
}

// Load persisted rotation from NVS.  Returns 1 (upright) if unset.
uint8_t loadRotation() {
    Preferences pref;
    pref.begin("usaged", false);
    uint8_t rot = (uint8_t)pref.getUChar("rot", 1);
    pref.end();
    // Validate: only 1 and 3 are valid for the StickS3.
    if (rot != 1 && rot != 3) {
        rot = 1;
    }
    return rot;
}

// Persist rotation to NVS so it survives reboot, deep sleep, and OTA.
void saveRotation(uint8_t r) {
    Preferences pref;
    pref.begin("usaged", false);
    pref.putUChar("rot", r);
    pref.end();
}

} // namespace sticks3
