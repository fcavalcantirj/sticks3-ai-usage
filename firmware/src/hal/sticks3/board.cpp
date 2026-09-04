// firmware/src/hal/sticks3/board.cpp — M5StickS3 board HAL implementation.
#include "hal/sticks3/board.h"

#ifndef USAGED_BUILD_ID
#define USAGED_BUILD_ID "unknown"
#endif

#include <M5Unified.h>

namespace sticks3 {

void boardInit() {
    auto cfg = M5.config();
    cfg.internal_imu = false;
    cfg.internal_spk = false;
    cfg.internal_mic = false;
    cfg.output_power = false;
    M5.begin(cfg);

    M5.Display.setRotation(1);
    M5.Display.setBrightness(80);

    M5.BtnA.setHoldThresh(600);
    M5.BtnB.setHoldThresh(600);
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

} // namespace sticks3
