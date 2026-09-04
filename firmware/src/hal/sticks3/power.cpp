// firmware/src/hal/sticks3/power.cpp — power management HAL for the M5StickS3.
//
// Hardware: M5PM1 PMIC (I2C 0x6E) on ESP32-S3.  USB-present = VBUS > 4000 mV.
// Screen rail = PM1 GPIO2 (L3B); PA rail = PM1 GPIO3.  The PM1 IRQ line
// (GPIO1 as push-pull output) is wired to ESP32-S3 GPIO13 for ext0 wake.
//
// Deep-sleep teardown follows the ptt.ino lineage (ptt.ino:191-211) verbatim —
// every register write is a measured leak fix: ES8311 codec, BMI270 IMU,
// PM1 PA + LCD rails.  Do NOT thin this teardown.
#include "hal/sticks3/power.h"

#include "hal/sticks3/board.h"   // serialLine
#include "usage/power.h"          // powerDecide
#include "usage/serial_proto.h"   // fmtWake, fmtSleep

#include <ArduinoOTA.h>
#include <M5Unified.h>
#include <WiFi.h>
#include <esp_sleep.h>
#include <driver/rtc_io.h>

namespace sticks3 {

// --- wake cause / vbus -----------------------------------------------------

const char* wakeCauseStr(WakeCause c) {
    switch (c) {
        case WakeCause::PowerOn: return "power_on";
        case WakeCause::Ext0:    return "ext0";
        case WakeCause::Ext1:    return "ext1";
        case WakeCause::Timer:   return "timer";
        default:                 return "unknown";
    }
}

uint16_t vbusMv() {
    return M5.Power.getVBUSVoltage();
}

bool vbusPresent() {
    return vbusMv() > 4000;
}

// --- screen off ------------------------------------------------------------

void screenOff() {
    M5.Display.sleep();
    M5.Display.waitDisplay();
    // Cut the LCD panel rail (PM1 GPIO2 = L3B).  M5GFX re-asserts it
    // on the next boot, so this is safe across sleep/wake cycles.
    M5.Power.M5pm1.setGPIOOutput(m5::M5PM1_Class::gpio2, false);
}

// --- teardown (ptt.ino:191-211, DO NOT THIN) -------------------------------

static void teardownCodecs() {
    M5.Speaker.end();
    M5.Mic.end();
    // ES8311 full powerdown — the lib's mic-disable alone leaves the codec
    // powered.  Three registers: 0x0D, 0x0E, 0x00.
    M5.In_I2C.writeRegister8(0x18, 0x0D, 0xFC, 400000);
    M5.In_I2C.writeRegister8(0x18, 0x0E, 0x6A, 400000);
    M5.In_I2C.writeRegister8(0x18, 0x00, 0x00, 400000);
}

static void teardownImu() {
    // BMI270 suspend — ~1 mA leak eats the µA win otherwise.
    M5.In_I2C.writeRegister8(0x68, 0x7D, 0x00, 400000);
    M5.In_I2C.writeRegister8(0x68, 0x7C, 0x03, 400000);
}

static void teardownPowerRails() {
    // PA off (PM1 GPIO3) + LCD rail off (PM1 GPIO2).
    M5.Power.M5pm1.setGPIOOutput(m5::M5PM1_Class::gpio3, false);
    M5.Power.M5pm1.setGPIOOutput(m5::M5PM1_Class::gpio2, false);
}

static void radioOff() {
    if (WiFi.status() == WL_CONNECTED) {
        ArduinoOTA.end();
    }
    WiFi.disconnect(true);
    WiFi.mode(WIFI_OFF);
}

// --- wake sources ----------------------------------------------------------

static void armWakeSources() {
    // PM1 GPIO1 as push-pull IRQ output for 5VIN-insert detection.
    M5.Power.M5pm1.setGPIOMode(m5::M5PM1_Class::gpio1,
                               m5::M5PM1_Class::output);
    M5.Power.M5pm1.setGPIOFunction(m5::M5PM1_Class::gpio1,
                                   m5::M5PM1_Class::irq);
    M5.Power.M5pm1.setGPIODrive(m5::M5PM1_Class::gpio1,
                                m5::M5PM1_Class::push_pull);
    // Clear IRQ status so the line is released before sleep.
    M5.Power.M5pm1.clearIRQStatus();
    // Unmask only 5VIN-inserted (system IRQ bit 0); disable all others.
    // setSystemIRQMaskBits: bit=1 DISABLES.  0x3E = 0b111110 → bit0 enabled.
    M5.Power.M5pm1.setSystemIRQMaskBits(0x3E);

    // (a) USB insert: ext0 on GPIO13 (PM1 IRQ line), trigger on low.
    esp_sleep_enable_ext0_wakeup(GPIO_NUM_13, 0);
    rtc_gpio_pullup_en(GPIO_NUM_13);

    // (b) Buttons: BtnA (GPIO11) + BtnB (GPIO12), any-low → per-pin OR.
    //     ESP_EXT1_WAKEUP_ANY_LOW == 0 is the per-pin OR mode.
    esp_sleep_enable_ext1_wakeup((1ULL << 11) | (1ULL << 12),
                                 ESP_EXT1_WAKEUP_ANY_LOW);

    // (c) 1-hour timer backstop: a missed IRQ can never strand the device.
    esp_sleep_enable_timer_wakeup(3600ULL * 1000000ULL);
}

// --- sleep -----------------------------------------------------------------

void powerSleep() {
    // [SLEEP] before teardown so the line is visible over serial.
    char buf[64];
    usage::fmtSleep(buf, sizeof(buf), "battery");
    serialLine(buf);

    screenOff();
    radioOff();
    teardownCodecs();
    teardownImu();
    teardownPowerRails();
    armWakeSources();

    esp_deep_sleep_start();
}

// --- wake ------------------------------------------------------------------

WakeCause readWakeCause() {
    // On a cold boot / reset, esp_sleep_get_wakeup_cause() returns
    // ESP_SLEEP_WAKEUP_UNDEFINED (there is no POWERON in ESP-IDF 4.4.7).
    switch (esp_sleep_get_wakeup_cause()) {
        case ESP_SLEEP_WAKEUP_EXT0:       return WakeCause::Ext0;
        case ESP_SLEEP_WAKEUP_EXT1:       return WakeCause::Ext1;
        case ESP_SLEEP_WAKEUP_TIMER:      return WakeCause::Timer;
        case ESP_SLEEP_WAKEUP_UNDEFINED:  return WakeCause::PowerOn;
        default:                          return WakeCause::Unknown;
    }
}

void emitWake(WakeCause cause, uint16_t vbus) {
    char buf[64];
    usage::fmtWake(buf, sizeof(buf), wakeCauseStr(cause), vbus);
    serialLine(buf);
}

void emitSleep(const char* reason) {
    char buf[64];
    usage::fmtSleep(buf, sizeof(buf), reason);
    serialLine(buf);
}

} // namespace sticks3
