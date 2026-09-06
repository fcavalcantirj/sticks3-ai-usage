// firmware/test/host/test_battery.cpp — tests for the pure battery indicator.
#include "framework.h"
#include "usage/battery.h"

#include <cstdio>
#include <cstring>

using sticks3::battery::BatteryView;
using sticks3::battery::batteryLabel;
using sticks3::battery::batteryTier;
using sticks3::battery::batteryPctClamped;

// --- batteryPctClamped -------------------------------------------------------

TEST(battery_clamp_in_range) {
    ASSERT_EQ(50, batteryPctClamped(50));
    ASSERT_EQ(0, batteryPctClamped(0));
    ASSERT_EQ(100, batteryPctClamped(100));
}

TEST(battery_clamp_over_range) {
    ASSERT_EQ(100, batteryPctClamped(150));
    ASSERT_EQ(100, batteryPctClamped(999));
}

TEST(battery_clamp_negative_is_unknown) {
    ASSERT_EQ(-1, batteryPctClamped(-1));
    ASSERT_EQ(-1, batteryPctClamped(-50));
}

// --- batteryLabel ------------------------------------------------------------

TEST(battery_label_ok) {
    char buf[16];
    BatteryView v{87, false, true, false};
    batteryLabel(v, buf, sizeof(buf));
    ASSERT_STREQ("87%", buf);
}

TEST(battery_label_usb_plus) {
    char buf[16];
    BatteryView v{87, true, true, false};
    batteryLabel(v, buf, sizeof(buf));
    ASSERT_STREQ("87%+", buf);
}

TEST(battery_label_unknown) {
    char buf[16];
    BatteryView v{-1, false, false, false};
    batteryLabel(v, buf, sizeof(buf));
    ASSERT_STREQ("--", buf);
}

TEST(battery_label_zero) {
    char buf[16];
    BatteryView v{0, false, true, false};
    batteryLabel(v, buf, sizeof(buf));
    ASSERT_STREQ("0%", buf);
}

TEST(battery_label_truncation) {
    char buf[4]; // very small buffer
    BatteryView v{87, true, true, false};
    batteryLabel(v, buf, sizeof(buf));
    // "87%+" is 4 chars; buffer is 4, so 3 chars + NUL fit.
    ASSERT_STREQ("87%", buf);
}

// --- batteryTier -------------------------------------------------------------

TEST(battery_tier_ok_above_40) {
    BatteryView v{87, false, true, false};
    ASSERT_EQ(uint8_t(0), batteryTier(v));
}

TEST(battery_tier_warn_above_20) {
    // 21 pct → warn
    BatteryView v{21, false, true, false};
    ASSERT_EQ(uint8_t(1), batteryTier(v));
}

TEST(battery_tier_crit_at_20) {
    // 20 pct → crit (<=20)
    BatteryView v{20, false, true, false};
    ASSERT_EQ(uint8_t(2), batteryTier(v));
}

TEST(battery_tier_crit_at_0) {
    BatteryView v{0, false, true, false};
    ASSERT_EQ(uint8_t(2), batteryTier(v));
}

TEST(battery_tier_boundary_41) {
    // 41 pct → ok (>40)
    BatteryView v{41, false, true, false};
    ASSERT_EQ(uint8_t(0), batteryTier(v));
}

TEST(battery_tier_boundary_40) {
    // 40 pct → warn (not >40, but >20)
    BatteryView v{40, false, true, false};
    ASSERT_EQ(uint8_t(1), batteryTier(v));
}

TEST(battery_tier_unknown) {
    BatteryView v{-1, false, false, false};
    ASSERT_EQ(uint8_t(3), batteryTier(v));
}
