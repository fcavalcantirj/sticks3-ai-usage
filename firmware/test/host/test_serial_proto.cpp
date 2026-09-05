// firmware/test/host/test_serial_proto.cpp — tests for the serial protocol
// formatters (fmtBoot, fmtNet, fmtFetch, fmtRender, fmtErr).
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/serial_proto.h"

#include <cstdio>
#include <cstring>

// --- fmtBoot ---------------------------------------------------------------

TEST(serial_boot_basic) {
    char buf[64];
    int r = usage::fmtBoot(buf, sizeof(buf), 26, 8388608, "abc123", "1.0.0");
    ASSERT_STREQ("[BOOT] board=26 psram=8388608 build=abc123 fw=1.0.0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_boot_null_build) {
    char buf[64];
    int r = usage::fmtBoot(buf, sizeof(buf), 26, 8388608, nullptr, nullptr);
    ASSERT_STREQ("[BOOT] board=26 psram=8388608 build= fw=", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtNet ----------------------------------------------------------------

TEST(serial_net_connected) {
    char buf[64];
    int r = usage::fmtNet(buf, sizeof(buf), "connected", "192.168.0.77");
    ASSERT_STREQ("[NET] state=connected ip=192.168.0.77", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_net_connecting) {
    char buf[64];
    int r = usage::fmtNet(buf, sizeof(buf), "connecting", nullptr);
    ASSERT_STREQ("[NET] state=connecting", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_net_empty_ip) {
    char buf[64];
    int r = usage::fmtNet(buf, sizeof(buf), "lost", "");
    ASSERT_STREQ("[NET] state=lost", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtFetch --------------------------------------------------------------

TEST(serial_fetch_304) {
    char buf[64];
    int r = usage::fmtFetch(buf, sizeof(buf), 304, "abcd1234", 0, 120);
    ASSERT_STREQ("[FETCH] code=304 rev=abcd1234 ms=120", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_fetch_200) {
    char buf[64];
    int r = usage::fmtFetch(buf, sizeof(buf), 200, "abcd1234", 43, 310);
    ASSERT_STREQ("[FETCH] code=200 rev=abcd1234 seq=43 ms=310", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_fetch_error) {
    char buf[64];
    int r = usage::fmtFetch(buf, sizeof(buf), -1, "timeout", 0, 8000);
    ASSERT_STREQ("[FETCH] code=-1 err=timeout ms=8000", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtRender -------------------------------------------------------------

TEST(serial_render_basic) {
    char buf[64];
    int r = usage::fmtRender(buf, sizeof(buf), 1, 5, "abcd1234");
    ASSERT_STREQ("[RENDER] page=1 lines=5 rev=abcd1234", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtErr ----------------------------------------------------------------

TEST(serial_err_basic) {
    char buf[64];
    int r = usage::fmtErr(buf, sizeof(buf), "oom");
    ASSERT_STREQ("[ERR] oom", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- truncation ------------------------------------------------------------

TEST(serial_trunc_4byte) {
    // A 4-byte buffer: snprintf writes 3 chars + NUL and returns the full
    // would-be length.
    char buf[4];
    int r = usage::fmtBoot(buf, sizeof(buf), 26, 8388608, "abc123", "1.0.0");
    ASSERT_STREQ("[BO", buf);             // only 3 chars fit (4th byte is NUL)
    ASSERT_EQ(3, (int)std::strlen(buf));
    ASSERT_TRUE(r > 3);                    // return value is the full length
}

// --- fmtHeap -----------------------------------------------------------------

TEST(serial_heap_basic) {
    char buf[64];
    int r = usage::fmtHeap(buf, sizeof(buf), 123456, 65432);
    ASSERT_STREQ("[HEAP] free=123456 min=65432", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_heap_zeros) {
    char buf[64];
    int r = usage::fmtHeap(buf, sizeof(buf), 0, 0);
    ASSERT_STREQ("[HEAP] free=0 min=0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtWake ---------------------------------------------------------------

TEST(serial_wake_power_on) {
    char buf[64];
    int r = usage::fmtWake(buf, sizeof(buf), "power_on", 0);
    ASSERT_STREQ("[WAKE] cause=power_on vbus=0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_wake_ext0) {
    char buf[64];
    int r = usage::fmtWake(buf, sizeof(buf), "ext0", 4904);
    ASSERT_STREQ("[WAKE] cause=ext0 vbus=4904", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_wake_unknown) {
    char buf[64];
    int r = usage::fmtWake(buf, sizeof(buf), nullptr, 0);
    ASSERT_STREQ("[WAKE] cause=unknown vbus=0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtSleep --------------------------------------------------------------

TEST(serial_sleep_battery) {
    char buf[64];
    int r = usage::fmtSleep(buf, sizeof(buf), "battery");
    ASSERT_STREQ("[SLEEP] reason=battery", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtOta ----------------------------------------------------------------

TEST(serial_ota_start) {
    char buf[64];
    int r = usage::fmtOta(buf, sizeof(buf), "start", 0);
    ASSERT_STREQ("[OTA] start", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_ota_pct) {
    char buf[64];
    int r = usage::fmtOta(buf, sizeof(buf), "pct", 47);
    ASSERT_STREQ("[OTA] pct=47", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_ota_end) {
    char buf[64];
    int r = usage::fmtOta(buf, sizeof(buf), "end", 0);
    ASSERT_STREQ("[OTA] end", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_ota_err) {
    char buf[64];
    int r = usage::fmtOta(buf, sizeof(buf), "err", 2);
    ASSERT_STREQ("[OTA] err=2", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_ota_unknown_kind) {
    char buf[64];
    int r = usage::fmtOta(buf, sizeof(buf), "bogus", 9);
    ASSERT_STREQ("[OTA] err=0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtGesture ----------------------------------------------------------------

TEST(serial_gesture_flip) {
    char buf[64];
    int r = usage::fmtGesture(buf, sizeof(buf), 3);
    ASSERT_STREQ("[GESTURE] flip rot=3", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtBtn (ORDER #49: GPIO-based) --------------------------------------------------

TEST(serial_btn_gpio11_click_page) {
    char buf[64];
    int r = usage::fmtBtn(buf, sizeof(buf), 11, "click page");
    ASSERT_STREQ("[BTN] gpio=11 click page", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_btn_gpio11_hold_refresh) {
    char buf[64];
    int r = usage::fmtBtn(buf, sizeof(buf), 11, "hold refresh");
    ASSERT_STREQ("[BTN] gpio=11 hold refresh", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_btn_gpio12_click_refresh) {
    char buf[64];
    int r = usage::fmtBtn(buf, sizeof(buf), 12, "click refresh");
    ASSERT_STREQ("[BTN] gpio=12 click refresh", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtRefresh (ORDER #38) ---------------------------------------------------------

TEST(serial_refresh_200) {
    char buf[64];
    int r = usage::fmtRefresh(buf, sizeof(buf), 200, 310);
    ASSERT_STREQ("[REFRESH] code=200 ms=310", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_refresh_202) {
    char buf[64];
    int r = usage::fmtRefresh(buf, sizeof(buf), 202, 50);
    ASSERT_STREQ("[REFRESH] code=202 retry", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_refresh_error) {
    char buf[64];
    int r = usage::fmtRefresh(buf, sizeof(buf), -1, 8000);
    ASSERT_STREQ("[REFRESH] err=timeout ms=8000", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

// --- fmtBatt -----------------------------------------------------------------

TEST(serial_batt_known_usb) {
    char buf[64];
    int r = usage::fmtBatt(buf, sizeof(buf), 87, 1);
    ASSERT_STREQ("[BATT] pct=87 usb=1", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}

TEST(serial_batt_known_battery) {
    char buf[64];
    int r = usage::fmtBatt(buf, sizeof(buf), 27, 0);
    ASSERT_STREQ("[BATT] pct=27 usb=0", buf);
    ASSERT_EQ(r, (int)std::strlen(buf));
}
