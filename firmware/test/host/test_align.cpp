// firmware/test/host/test_align.cpp — host tests for usage::centerTextY / centerTextX.
#include "framework.h"
#include "usage/align.h"

// --- Vertical centring ---

TEST(align_center_text_y_even) {
    // 16 px rect at y=116, 8 px font: (16-8)/2 = 4 → y = 120.
    ASSERT_EQ(120, usage::centerTextY(116, 16, 8));
}

TEST(align_center_text_y_odd_leftover) {
    // 17 px rect, 8 px font: (17-8)/2 = 4 (integer division rounds down) → y = 116+4 = 120.
    // Odd leftover (1 px) goes to the top, consistently.
    ASSERT_EQ(120, usage::centerTextY(116, 17, 8));

    // 16 px rect, 7 px font: (16-7)/2 = 4 (drops the 1 px remainder) → y = 120.
    ASSERT_EQ(120, usage::centerTextY(116, 16, 7));
}

TEST(align_center_text_y_font_taller_than_rect) {
    // 8 px rect, 16 px font: slack clamps to 0 → y = rectY (never negative).
    ASSERT_EQ(116, usage::centerTextY(116, 8, 16));
    ASSERT_EQ(100, usage::centerTextY(100, 4, 40));
}

TEST(align_center_text_y_exact_fit) {
    // Font exactly fills the rect: slack = 0 → y = rectY.
    ASSERT_EQ(116, usage::centerTextY(116, 8, 8));
}

// --- Horizontal centring ---

TEST(align_center_text_x_exact) {
    // Rect 5..235 (W-10), text 40 px: (230-40)/2 = 95 → x = 5+95 = 100.
    ASSERT_EQ(100, usage::centerTextX(5, 230, 40));
}

TEST(align_center_text_x_text_wider_than_rect) {
    // 230 px rect, 300 px text: slack clamps to 0 → x = rectX (left-aligned).
    ASSERT_EQ(5, usage::centerTextX(5, 230, 300));
}

TEST(align_center_text_x_odd_leftover) {
    // 231 px rect, 100 px text: (231-100)/2 = 65 (drops remainder) → x = 5+65 = 70.
    ASSERT_EQ(70, usage::centerTextX(5, 231, 100));
}

TEST(align_center_text_x_zero_width_text) {
    // Zero-width text is centred at the middle of the rect.
    ASSERT_EQ(120, usage::centerTextX(5, 230, 0));
}
