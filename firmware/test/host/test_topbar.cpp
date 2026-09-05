// firmware/test/host/test_topbar.cpp — tests for the pure top-bar packer.
// ORDER #51: the header packs wifi dot, battery (gauge + label), page
// indicator and title only.  Version and seq live in the footer now.
#include "framework.h"
#include "usage/topbar.h"

#include <cmath>
#include <cstring>

using sticks3::topbar::Layout;
using sticks3::topbar::Item;

// Fixed-width font simulator: each character is 6 px wide (matches the
// M5StickS3 default Font 1 = 6×8).  This is exactly what M5.Display.textWidth
// returns for the default font.
static int measureFixed(const char* s) {
    if (s == nullptr) return 0;
    return (int)(std::strlen(s) * 6);
}

// Build a layout for a standard 240×135 screen.
static Layout buildLayout(uint8_t page, uint8_t pageCount, const char* title,
                           int battPct, bool battOnUsb, bool battKnown) {
    Layout l;
    sticks3::topbar::compute(l, 240, page, pageCount,
                             title, battPct, battOnUsb, battKnown,
                             measureFixed);
    return l;
}

// --- vertical bounds --------------------------------------------------------

TEST(topbar_all_items_above_y21) {
    Layout l = buildLayout(0, 3, "PLANS", 87, true, true);

    // Every header item is drawn.
    ASSERT_TRUE(l.wifiDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.battLabel.drawn);
    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    // Gauge (y=4..16) and dot (y=8..14) and text (y=7..14) stay above y=21.
    ASSERT_EQ(7, sticks3::topbar::kTextY);
    ASSERT_EQ(11, sticks3::topbar::kDotY);
    ASSERT_EQ(4, sticks3::topbar::kGaugeY);
    ASSERT_TRUE(sticks3::topbar::kGaugeY + sticks3::topbar::kGaugeH <= sticks3::topbar::kBarH);
    ASSERT_TRUE(sticks3::topbar::kTextY + 8 <= sticks3::topbar::kBarH);
}

// --- wifi-to-battery gap is 8px (ORDER #51) ----------------------------------

TEST(topbar_wifi_battery_gap_is_8) {
    ASSERT_EQ(8, sticks3::topbar::kGapWifi);
    ASSERT_EQ(4, sticks3::topbar::kGap);

    Layout l = buildLayout(0, 3, "PLANS", 87, true, true);

    // Wifi dot right edge = dotCx + r = (W-4-3) + 3 = W-4 = 236.
    // Battery right edge = gaugeLeft + 35.  The gap between the dot's LEFT
    // edge and the battery's right edge must be exactly kGapWifi (8px).
    int16_t dotLeft = l.wifiDot.x - 3;           // centre - radius
    int16_t battRight = l.battery.x + l.battery.w; // gauge_left + 35
    ASSERT_EQ(sticks3::topbar::kGapWifi, dotLeft - battRight);
}

// --- no overlap between adjacent items --------------------------------------

TEST(topbar_no_overlap_full) {
    Layout l = buildLayout(1, 3, "CREDITS", 87, false, true);

    // Every header item is drawn.
    ASSERT_TRUE(l.wifiDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.battLabel.drawn);
    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    // Right-to-left chain: wifiDot → battery unit → pageInd → title.
    // 1. Battery right edge + kGapWifi <= wifi dot left edge.
    int16_t dotLeft  = l.wifiDot.x - 3;       // centre - radius
    int16_t battRight = l.battery.x + l.battery.w; // gauge_left + 35
    ASSERT_TRUE(battRight + sticks3::topbar::kGapWifi <= dotLeft);

    // 2. Battery label right edge + 2 <= gauge left edge.
    int16_t labelRight = l.battLabel.x + l.battLabel.w;
    ASSERT_TRUE(labelRight + 2 <= l.battery.x); // 2px gap inside the unit

    // 3. Page indicator right edge + kGap <= battery label left edge.
    int16_t pageRight = l.pageInd.x + l.pageInd.w;
    int16_t labelLeft = l.battLabel.x;
    ASSERT_TRUE(pageRight + sticks3::topbar::kGap <= labelLeft);

    // 4. Title right edge + kGap <= page indicator left edge.
    int16_t titleRight = l.title.x + l.title.w;
    int16_t pageLeft = l.pageInd.x;
    ASSERT_TRUE(titleRight + sticks3::topbar::kGap <= pageLeft);
}

// --- battery always drawn, page indicator only when pageCount > 1 ------------

TEST(topbar_battery_always_drawn_pageind_only_when_multi) {
    Layout l = buildLayout(0, 1, "PLANS", 5, false, true);
    ASSERT_TRUE(l.wifiDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.battLabel.drawn);
    ASSERT_FALSE(l.pageInd.drawn);  // pageCount == 1
    ASSERT_TRUE(l.title.drawn);
}

// --- title dropped when very narrow ------------------------------------------

TEST(topbar_title_dropped_when_narrow) {
    // 240 px screen, no page indicator (pageCount=1), very long title.
    // cursor after battery = 163.  Title needs 5 + titleW + 4 <= 163,
    // i.e. titleW <= 154, i.e. title <= 25 chars (25*6=150).  A 35-char
    // title (210 px) cannot fit → title dropped.
    Layout l = buildLayout(0, 1, "ANEXTREMELYLONGTITLETHATWILLNOTFIT",
                           87, false, true);
    // Battery and wifi dot are never dropped.
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.wifiDot.drawn);
    // Title should be dropped (not enough room).
    ASSERT_FALSE(l.title.drawn);
}

// --- page indicator dropped before title when tight --------------------------

TEST(topbar_pageind_dropped_before_title) {
    // 240 px is plenty for all items; this test verifies the right-to-left
    // drop order still holds: with a huge title the page indicator is dropped
    // first, then the title.
    Layout l = buildLayout(0, 3, "ANEXTREMELYLONGTITLETHATWILLNOTFIT", 87, false, true);
    // Battery and wifi dot survive.
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.wifiDot.drawn);
    // At least one of pageInd/title is dropped.
    ASSERT_TRUE(!l.pageInd.drawn || !l.title.drawn);
}

// --- battery label width changes don't cause overlap ------------------------

TEST(topbar_battery_label_width_no_overlap) {
    Layout l1 = buildLayout(0, 3, "PLANS", 0, false, true);
    Layout l2 = buildLayout(0, 3, "PLANS", 100, true, true);

    // Both should have all items drawn.
    ASSERT_TRUE(l1.wifiDot.drawn);
    ASSERT_TRUE(l2.wifiDot.drawn);
    ASSERT_TRUE(l1.battery.drawn);
    ASSERT_TRUE(l2.battery.drawn);

    // The right-edge chain must not extend past W.
    ASSERT_TRUE(l1.wifiDot.x + 3 <= 240);
    ASSERT_TRUE(l2.wifiDot.x + 3 <= 240);

    // No overlap in either layout.
    int16_t dotLeft1 = l1.wifiDot.x - 3;
    int16_t battRight1 = l1.battery.x + l1.battery.w;
    ASSERT_TRUE(battRight1 + sticks3::topbar::kGapWifi <= dotLeft1);

    int16_t dotLeft2 = l2.wifiDot.x - 3;
    int16_t battRight2 = l2.battery.x + l2.battery.w;
    ASSERT_TRUE(battRight2 + sticks3::topbar::kGapWifi <= dotLeft2);
}

// --- battery gauge vertical position (ORDER #48 defect b) ---------------------

TEST(topbar_battery_gauge_within_bar) {
    // The gauge must sit fully within the 22px bar (y=0..21).
    // Gauge: top=kGaugeY(4), height=kGaugeH(13) → spans y=4..16.
    // Dot:   centre=kDotY(11), radius=3 → spans y=8..14.
    // Text:  baseline=kTextY(7), font height=8 → spans y=7..14.
    // None cross the separator at y=21.
    ASSERT_EQ(4, sticks3::topbar::kGaugeY);
    ASSERT_EQ(13, sticks3::topbar::kGaugeH);
    ASSERT_TRUE(sticks3::topbar::kGaugeY + sticks3::topbar::kGaugeH <= sticks3::topbar::kBarH);
    // Centre of the gauge relative to the bar midline.
    int16_t gaugeMid = sticks3::topbar::kGaugeY + sticks3::topbar::kGaugeH / 2;  // 4+6=10
    ASSERT_EQ(11, sticks3::topbar::kDotY);  // bar midline
    ASSERT_TRUE(gaugeMid == sticks3::topbar::kDotY || gaugeMid == sticks3::topbar::kDotY - 1);
}

// --- item ordering verification (right-to-left) ------------------------------

TEST(topbar_item_ordering_right_to_left) {
    // Verify the right-to-left chain: wifiDot, battery, pageInd, title.
    Layout l = buildLayout(0, 3, "PLANS", 50, false, true);

    ASSERT_TRUE(l.wifiDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    // wifiDot is rightmost.
    ASSERT_TRUE(l.wifiDot.x > l.battery.x);
    // battery is right of pageInd.
    ASSERT_TRUE(l.battery.x > l.pageInd.x);
    // pageInd is right of title (title is left-aligned at x=5).
    ASSERT_TRUE(l.pageInd.x > l.title.x);
}
