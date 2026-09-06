// firmware/src/hal/sticks3/power.cpp — power management HAL for the M5StickS3.
//
// Hardware: M5PM1 PMIC (I2C 0x6E) on ESP32-S3.  USB-present = VBUS > 4000 mV.
// Screen rail = PM1 GPIO2 (L3B); PA rail = PM1 GPIO3.
//
// ORDER #29: USB-insert (ext0) wake via the PM1 IRQ line (GPIO1→GPIO13) is
// DISABLED — driving PM1 GPIO1 push-pull conflicts with the SDA line and
// hangs PMIC I2C reads after wake.  The device wakes only on ext1 (buttons)
// or the 12 h timer; on USB it never sleeps (VbusDebouncer).  See notes in
// docs/DEVICES.md under "Unit #2 power/wake errata".
//
// Deep-sleep teardown follows the ptt.ino lineage (ptt.ino:191-211) verbatim —
// every register write is a measured leak fix: ES8311 codec, PM1 PA + LCD rails,
// and now the BMI270 suspend (ptt.ino:205-207) at the 12 h backstop.
#include "hal/sticks3/power.h"

#include "hal/sticks3/board.h"   // serialLine
#include "usage/power.h"          // powerDecide, VbusDebouncer
#include "usage/serial_proto.h"   // fmtWake, fmtSleep

#include <ArduinoOTA.h>
#include <M5Unified.h>
#include <WiFi.h>
#include <esp_sleep.h>
#include <driver/rtc_io.h>
#include <cstdio>

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

int batteryLevel() {
    return M5.Power.getBatteryLevel();
}

int32_t batteryVoltageMv() {
    return M5.Power.getBatteryVoltage();
}

// ORDER #71 (task 71): report whether the cell is actively charging.
// M5.Power.isCharging() for M5StickS3 reads PM1 GPIO0 (CHG_STAT, low=charging)
// — a real I2C GPIO read, not the M5PM1_Class::isCharging() no-op stub.
bool batteryCharging() {
    return M5.Power.isCharging() == m5::Power_Class::is_charging;
}

// ORDER #26: debounce VBUS reads so a single I2C glitch (0 mV) or noise
// spike on the PM1 I2C bus can never make the device deep-sleep while on USB.
bool vbusPresent() {
    static usage::VbusDebouncer debouncer(3);  // 3 consecutive battery reads
    // ORDER #35 / BUG 40c: 0 mV is the NORMAL battery reading on this board
    // (captures: 0 mV on battery, ~5234-5280 mV on USB, ~12 mV transiently
    // just after an unplug).  Treating it as a suspect I2C glitch made the
    // device believe it was on USB forever and never sleep.  Feed every
    // reading to the debouncer; the 3-consecutive-sample rule is the glitch
    // protection.  The asymmetry matters: a false "battery" costs one
    // unnecessary sleep that a button or the 60 s timer recovers, while a
    // false "USB" costs a flat battery.
    return debouncer.sample(vbusMv());
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
//
// ORDER #29: PM1 GPIO1 IRQ output is NOT configured — it conflicts with SDA
// and causes PMIC I2C hangs after wake (see docs/DEVICES.md).  Wake sources:
//   - ext1 buttons (BtnA GPIO11, BtnB GPIO12)
//   - 12 h timer backstop (ORDER #60 — reverses ORDER #29's 60 s)

static void suspendImu() {
    // BMI270 suspend: write PWR_CFG (0x7D) = 0x00 and PMU_CMD (0x7C) = 0x03
    // at I2C address 0x68.  See ptt.ino:205-207.  internal_imu is false so
    // the sensor is never initialised, but it powers on in normal mode by
    // default (~1 mA).  At a 12 h backstop that leak is the difference
    // between weeks and days of standby — write the suspend registers
    // defensively rather than trusting the power-on default.
    M5.In_I2C.writeRegister8(0x68, 0x7D, 0x00, 400000);
    M5.In_I2C.writeRegister8(0x68, 0x7C, 0x03, 400000);
}

static void armWakeSources() {
    // No PM1 GPIO1 / IRQ register writes (ORDER #29: SDA conflict).

    // (a) Buttons: BtnA (GPIO11) + BtnB (GPIO12), any-low → per-pin OR.
    //     ESP_EXT1_WAKEUP_ANY_LOW == 0 is the per-pin OR mode.
    //     kExt1WakeMask is defined in usage/power.h for host testability.
    esp_sleep_enable_ext1_wakeup(usage::kExt1WakeMask,
                                 ESP_EXT1_WAKEUP_ANY_LOW);
    // ORDER #27: pair pullup_en...pulldown_dis on every ext wake pin.
    // Without pulldown_dis, the RTC domain's internal pulldown keeps the
    // pin LOW, causing ext1 to fire instantly on sleep entry.
    rtc_gpio_pullup_en(GPIO_NUM_11);
    rtc_gpio_pulldown_dis(GPIO_NUM_11);
    rtc_gpio_pullup_en(GPIO_NUM_12);
    rtc_gpio_pulldown_dis(GPIO_NUM_12);

    // (b) 12-hour timer backstop (ORDER #60): the button is the primary
    // wake; the timer is a true safety net so the device is not stranded
    // if a button wake is missed.  kTimerBackstopUs is defined in
    // usage/power.h for host testability.
    esp_sleep_enable_timer_wakeup(usage::kTimerBackstopUs);
}

// --- sleep -----------------------------------------------------------------

void powerSleep() {
    // [SLEEP] before teardown so the line is visible over serial.
    char buf[64];
    usage::fmtSleep(buf, sizeof(buf), "battery");
    serialLine(buf);

    // ORDER #27: instant-wake guard is monitored in main.cpp (g_powerGuard).
    // Since ext0 is no longer armed (ORDER #29), disableExt0 is always false;
    // the guard counter remains as a monitor for the follow-up experiment.

    screenOff();
    radioOff();
    suspendImu();  // ORDER #60: BMI270 suspend at 12h backstop — ~1 mA leak
    teardownCodecs();
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
