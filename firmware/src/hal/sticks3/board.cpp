// firmware/src/hal/sticks3/board.cpp — M5StickS3 board HAL implementation.
#include "hal/sticks3/board.h"

#ifndef USAGED_BUILD_ID
#define USAGED_BUILD_ID "unknown"
#endif

#include "usage/hold_flip.h"  // kHoldThresholdMs — pure C++17, no M5/Arduino deps

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

    // ORDER #71 (task 71): enable battery charging. The M5Unified board init
    // for M5StickS3 configures GPIO0 (CHG_STAT input) but does NOT call
    // setBatteryCharge(true) — only M5PaperS3 does, so the PM1's CHG_EN bit
    // (PWR_CFG bit 0, reg 0x06) stays clear and the cell discharges even on
    // USB. setBatteryCharge writes that bit for real on the M5PM1.
    //
    // Charge current: 200 mA deliberate choice — 0.8C for the 250 mAh cell,
    // within the 0.5C-1C window ORDER #71 named. The M5PM1 has no
    // charge-current register (M5PM1_Class::setChargeCurrent is a permanent
    // stub returning false), so on this board setChargeCurrent falls through
    // to the hardware CHG_PROG resistor (~100 mA) — calling it documents
    // intent without harm. setChargeVoltage(4200) for a single-cell Li-ion,
    // also a documented-stub on M5PM1.
    //
    // isCharging() reads the CHG_STAT pin (PM1 GPIO0, low=charging) directly
    // in Power_Class for board_M5StickS3 — a real GPIO read we log on [BATT].
    // CAUTION (seq 347): GPIO0 is input-only here, never configured as output;
    // only GPIO1 conflicts with SDA (ORDER #29).
    M5.Power.setBatteryCharge(true);
    M5.Power.setChargeCurrent(200);   // 0.8C for 250 mAh — intent; hardware-set on M5PM1
    M5.Power.setChargeVoltage(4200);  // single-cell Li-ion ceiling

    M5.Display.setRotation(1);
    // ORDER #60 (task 63): brightness is set in setup() AFTER reading the wake
    // cause.  A timer wake must not raise the backlight.

    // ORDER #98 task 98: BtnA hold threshold raised from 600 ms to 1500 ms,
    // matching BtnB (board.cpp:48).  The old 600 ms was too short — the click
    // detector consumed the event before M5.BtnA.wasHold() could fire (the
    // 600 ms click window swallowed the hold).  1500 ms is hold_flip::
    // kHoldThresholdMs (HoldFlipDetector::kHoldThresholdMs), the shared click/hold boundary the pure state machine
    // uses for BtnB too.  BtnA hold is now a deliberate gesture that shows the
    // use-next advise overlay (wasHold() never also fires wasSingleClicked,
    // so single-click paging is untouched).
    M5.BtnA.setHoldThresh(sticks3::holdflip::HoldFlipDetector::kHoldThresholdMs);
    M5.BtnB.setHoldThresh(1500);  // ORDER #53 REVISED: hold-to-flip — now matches BtnA
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

// RTC-backed epoch seconds that survive deep sleep.  ESP-IDF maintains the
// system clock via the RTC across esp_deep_sleep_start() — gettimeofday()
// returns the same wall-clock epoch time before sleep and after wake.  We
// read it immediately before powerSleep() and again on wake to compute the
// real sleep duration (ORDER #65).  esp_timer_get_time() does NOT survive
// deep sleep on this board (proven: it wraps to 0 on wake, producing
// 2^32/1000ms underflow).
uint32_t rtcNowSec() {
    struct timeval tv;
    gettimeofday(&tv, nullptr);
    return (uint32_t)tv.tv_sec;
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

// --- brightness -------------------------------------------------------------

// Load persisted brightness level index (0-5) from NVS, using the same
// "usaged" namespace as rotation.  Falls back to the 25% default (index 2)
// if no saved value exists or the stored value is out of range.
uint8_t loadBrightness() {
    Preferences pref;
    pref.begin("usaged", false);
    uint8_t idx = (uint8_t)pref.getUChar("bright", 2 /* = 25% default */);
    pref.end();
    // Validate: must index into the kLevels table (0-5).
    if (idx >= 6) {
        idx = 2;
    }
    return idx;
}

// Persist the brightness level index so it survives reboot, deep sleep, and OTA.
void saveBrightness(uint8_t idx) {
    Preferences pref;
    pref.begin("usaged", false);
    pref.putUChar("bright", idx);
    pref.end();
}

} // namespace sticks3
