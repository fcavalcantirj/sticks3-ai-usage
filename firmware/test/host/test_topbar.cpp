// firmware/test/host/test_topbar.cpp — tests for the pure top-bar packer.
// ORDER #65: the header packs freshness dot + wifi bars + battery (gauge + label),
// page indicator and title.  Version and seq live in the footer now.
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
                           int battPct, bool battOnUsb, bool battKnown,
                           bool wifiOk = true, uint8_t freshnessTier = 0) {
    Layout l;
    sticks3::topbar::compute(l, 240, page, pageCount,
                             title, battPct, battOnUsb, battKnown,
                             wifiOk, freshnessTier,
                             measureFixed);
    return l;
}

// --- vertical bounds --------------------------------------------------------

TEST(topbar_all_items_above_y21) {
    Layout l = buildLayout(0, 3, "PLANS", 87, true, true);

    // Every header item is drawn.
    ASSERT_TRUE(l.freshnessDot.drawn);
    ASSERT_TRUE(l.wifiBars.drawn);
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

// --- freshness dot to wifi bars gap is 8px, wifi bars to battery is 4px -------

TEST(topbar_freshness_wifi_battery_gap) {
    ASSERT_EQ(8, sticks3::topbar::kGapWifi);
    ASSERT_EQ(4, sticks3::topbar::kGap);

    Layout l = buildLayout(0, 3, "PLANS", 87, true, true);

    // Fresh dot right edge = W - kRightMargin = 240 - 4 = 236.
    // Dot left edge = dotCx - 3 = (236 - 3) - 3 = 230.
    // 8px gap → wifi bars right edge = 230 - 8 = 222.
    // Wifi bars are 7px wide → left edge = 222 - 7 = 215.
    // 4px gap → battery unit right edge = 215 - 4 = 211.
    int16_t dotLeft = l.freshnessDot.x - 3;       // centre - radius
    int16_t barsRight = l.wifiBars.x + l.wifiBars.w;
    int16_t battRight = l.battery.x + l.battery.w; // gauge_left + 35

    // Gap from dot left edge to bars right edge must be kGapWifi (8px).
    ASSERT_EQ(sticks3::topbar::kGapWifi, dotLeft - barsRight);
    // Gap from bars left edge to battery right edge must be kGap (4px).
    ASSERT_EQ(sticks3::topbar::kGap, l.wifiBars.x - battRight);
}

// --- no overlap between adjacent items --------------------------------------

TEST(topbar_no_overlap_full) {
    Layout l = buildLayout(1, 3, "CREDITS", 87, false, true);

    // Every header item is drawn.
    ASSERT_TRUE(l.freshnessDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.battLabel.drawn);
    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    // Right-to-left chain: freshnessDot → wifiBars → battery unit → pageInd → title.
    // 1. Battery right edge + kGapWifi <= freshness dot left edge.
    int16_t dotLeft  = l.freshnessDot.x - 3;       // centre - radius
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
    ASSERT_TRUE(l.freshnessDot.drawn);
    ASSERT_TRUE(l.wifiBars.drawn);
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
    // Battery and freshness dot are never dropped.
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.freshnessDot.drawn);
    // Title should be dropped (not enough room).
    ASSERT_FALSE(l.title.drawn);
}

// --- page indicator dropped before title when tight --------------------------

TEST(topbar_pageind_dropped_before_title) {
    // 240 px is plenty for all items; this test verifies the right-to-left
    // drop order still holds: with a huge title the page indicator is dropped
    // first, then the title.
    Layout l = buildLayout(0, 3, "ANEXTREMELYLONGTITLETHATWILLNOTFIT", 87, false, true);
    // Battery and freshness dot survive.
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.freshnessDot.drawn);
    // At least one of pageInd/title is dropped.
    ASSERT_TRUE(!l.pageInd.drawn || !l.title.drawn);
}

// --- battery label width changes don't cause overlap ------------------------

TEST(topbar_battery_label_width_no_overlap) {
    Layout l1 = buildLayout(0, 3, "PLANS", 0, false, true);
    Layout l2 = buildLayout(0, 3, "PLANS", 100, true, true);

    // Both should have all items drawn.
    ASSERT_TRUE(l1.freshnessDot.drawn);
    ASSERT_TRUE(l2.freshnessDot.drawn);
    ASSERT_TRUE(l1.battery.drawn);
    ASSERT_TRUE(l2.battery.drawn);

    // The right-edge chain must not extend past W.
    ASSERT_TRUE(l1.freshnessDot.x + 3 <= 240);
    ASSERT_TRUE(l2.freshnessDot.x + 3 <= 240);

    // No overlap in either layout.
    int16_t dotLeft1 = l1.freshnessDot.x - 3;
    int16_t battRight1 = l1.battery.x + l1.battery.w;
    ASSERT_TRUE(battRight1 + sticks3::topbar::kGapWifi <= dotLeft1);

    int16_t dotLeft2 = l2.freshnessDot.x - 3;
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
    // Verify the right-to-left chain: freshnessDot, wifiBars, battery, pageInd, title.
    Layout l = buildLayout(0, 3, "PLANS", 50, false, true);

    ASSERT_TRUE(l.freshnessDot.drawn);
    ASSERT_TRUE(l.battery.drawn);
    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    // Fresh dot is rightmost.
    ASSERT_TRUE(l.freshnessDot.x > l.battery.x);
    // battery is right of pageInd.
    ASSERT_TRUE(l.battery.x > l.pageInd.x);
    // pageInd is right of title (title is left-aligned at x=5).
    ASSERT_TRUE(l.pageInd.x > l.title.x);
}

// --- wifi bars drawn when wifiOk, dropped when not on wifi (if no room) --------

TEST(topbar_wifi_bars_when_wifi_ok) {
    Layout l = buildLayout(0, 3, "PLANS", 87, true, true, /*wifiOk=*/true);
    ASSERT_TRUE(l.freshnessDot.drawn);
    ASSERT_TRUE(l.wifiBars.drawn);
    ASSERT_EQ(sticks3::topbar::kWifiBarsW, l.wifiBars.w);

    // Bars right edge should be exactly kGapWifi left of the dot's left edge.
    int16_t dotLeft = l.freshnessDot.x - 3;
    int16_t barsRight = l.wifiBars.x + l.wifiBars.w;
    ASSERT_EQ(sticks3::topbar::kGapWifi, dotLeft - barsRight);
}

TEST(topbar_wifi_bars_drawn_when_not_ok) {
    // Bars are drawn even when wifi is down (dimmed), as long as there is room.
    Layout l = buildLayout(0, 3, "PLANS", 87, true, true, /*wifiOk=*/false);
    ASSERT_TRUE(l.freshnessDot.drawn);
    // On a 240px screen with a short title, there is plenty of room.
    ASSERT_TRUE(l.wifiBars.drawn);
}

TEST(topbar_freshness_color_tiers) {
    ASSERT_EQ(0x07E0, sticks3::topbar::freshnessColor(0));  // green
    ASSERT_EQ(0xFD20, sticks3::topbar::freshnessColor(1));  // yellow
    ASSERT_EQ(0xF800, sticks3::topbar::freshnessColor(2));  // red
    ASSERT_EQ(0x07E0, sticks3::topbar::freshnessColor(99));  // default green
}

// --- page indicator CENTERED in the free span (ORDER #56 task 60) ---------------

TEST(topbar_pageind_centred_in_free_span) {
    // 240px screen, page 1/3, short title "PLANS" (30px).
    // Battery unit: battLabel "87%+" = 24px, gauge 32+3=35, labelLeft=161.
    // cursor = labelLeft - kGap = 157.  Title right = 5 + 30 = 35.
    // Free span: [35+4, 157] = [39, 157].  "1/3" = 18px.
    // Centred: (39 + 157 - 18) / 2 = 89.
    Layout l = buildLayout(0, 3, "PLANS", 87, true, true);

    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    int16_t pageW = measureFixed("1/3");  // 18
    // The indicator must be centred: (freeLeft + cursor - pw) / 2.
    int16_t titleRight = l.title.x + l.title.w;   // 5 + 30 = 35
    int16_t freeLeft = titleRight + sticks3::topbar::kGap;  // 39
    int16_t cursor = l.battLabel.x - sticks3::topbar::kGap;  // 161 - 4 = 157
    int16_t expectedX = (freeLeft + cursor - pageW) / 2;    // = 89

    ASSERT_EQ(expectedX, l.pageInd.x);
    ASSERT_EQ(pageW, l.pageInd.w);
}

// --- page indicator does NOT overlap title or cluster -------------------------

TEST(topbar_pageind_no_overlap_centred) {
    Layout l = buildLayout(0, 3, "CREDITS", 87, false, true);

    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    int16_t titleRight = l.title.x + l.title.w;
    int16_t pageLeft = l.pageInd.x;
    // Title right + gap must not overlap indicator.
    ASSERT_TRUE(titleRight + sticks3::topbar::kGap <= pageLeft);

    int16_t pageRight = l.pageInd.x + l.pageInd.w;
    int16_t battLabelLeft = l.battLabel.x;
    // Indicator right + gap must not overlap battery label.
    ASSERT_TRUE(pageRight + sticks3::topbar::kGap <= battLabelLeft);
}

// --- narrow bar: page indicator dropped before title --------------------------

TEST(topbar_pageind_centred_dropped_before_title_narrow) {
    // 240px with a very long title — title doesn't fit, so page indicator
    // is also dropped (it is centred in the span between title and cluster,
    // which only exists when the title fits).
    Layout l = buildLayout(0, 3, "ANEXTREMELYLONGTITLETHATWILLNOTFIT",
                              87, false, true);
    ASSERT_FALSE(l.pageInd.drawn);
    ASSERT_FALSE(l.title.drawn);
}

// --- page indicator centred with 2-digit page numbers ------------------------

TEST(topbar_pageind_centred_two_digit_pages) {
    // 12 pages: "1/12" = 24px.  Centre still works.
    Layout l = buildLayout(0, 12, "FREE", 50, false, true);

    ASSERT_TRUE(l.pageInd.drawn);
    ASSERT_TRUE(l.title.drawn);

    int16_t pageW = measureFixed("1/12");  // 24
    int16_t titleRight = l.title.x + l.title.w;  // 5 + 24 = 29
    int16_t freeLeft = titleRight + sticks3::topbar::kGap;  // 33
    int16_t cursor = l.battLabel.x - sticks3::topbar::kGap;
    int16_t expectedX = (freeLeft + cursor - pageW) / 2;

    ASSERT_EQ(expectedX, l.pageInd.x);
    ASSERT_EQ(pageW, l.pageInd.w);
}
