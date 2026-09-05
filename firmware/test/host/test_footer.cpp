// firmware/test/host/test_footer.cpp — tests for the footer measure-then-fit
// layout (ORDER #56 task 60).
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/render_plan.h"

#include <cstring>
#include <string>

using usage::FooterLayout;

// Fixed-width font simulator: 6 px per glyph (matches M5StickS3 Font 1).
static int measureFixed(const char* s) {
    if (s == nullptr) return 0;
    return (int)(std::strlen(s) * 6);
}

static FooterLayout computeFooter(int16_t W, const char* asOf,
                                   const char* buildId, const char* hint) {
    FooterLayout fl;
    usage::footerCompute(fl, W, asOf, buildId, hint, measureFixed);
    return fl;
}

// --- full fit: both strings fit unchanged -------------------------------------

TEST(footer_full_fit) {
    // "seq 5 · vtest" = 14 chars = 84 px; hint = 120 px; total = 84+4+120=208.
    // W=240: avail = 240-5-4=231.  208 <= 231 → both drawn verbatim.
    FooterLayout fl = computeFooter(240, "seq 5", "vtest",
                                    "blue: page   side: r/h");
    ASSERT_STREQ("seq 5 \xc2\xb7 vtest", fl.left);
    ASSERT_STREQ("blue: page   side: r/h", fl.hint);
    ASSERT_EQ(5, fl.leftX);
    // hintX = 240 - 120 - 4 = 116
    ASSERT_EQ(240 - measureFixed("blue: page   side: r/h") - 4, fl.hintX);
    ASSERT_TRUE(fl.hintDrawn);
}

// --- hint shortened before seq dropped -----------------------------------------

TEST(footer_hint_shortened_before_seq) {
    // Make the left very long so hint must shrink but seq is still kept.
    // "seq 99 · vverylongbuildid" = 25 chars = 150 px.
    // avail = 240 - 9 = 231.
    // If hint is "blue: page   side: r/h" (120 px): 150+4+120=274 > 231.
    // fitRight should shorten the hint; seq must still be present.
    FooterLayout fl = computeFooter(240, "seq 99", "vverylongbuildid",
                                    "blue: page   side: r/h");
    // Left must retain "seq 99 · vverylongbuildid".
    ASSERT_TRUE(std::string(fl.left).find("seq 99") != std::string::npos);
    ASSERT_TRUE(std::string(fl.left).find("vverylongbuildid") != std::string::npos);
    // Hint must be shorter than the original.
    ASSERT_TRUE(std::strlen(fl.hint) < std::strlen("blue: page   side: r/h"));
    ASSERT_TRUE(fl.hintDrawn);
}

// --- version NEVER shortened --------------------------------------------------

TEST(footer_version_never_shortened) {
    // Extremely tight screen: the version must still appear complete.
    // "seq 99 · vverylongbuildid" = 150 px; hint "blue: page r/h" = 90 px.
    // avail = 100 - 9 = 91.  Even version alone (150 px) exceeds 91, but the
    // function must still emit the full version string (untruncated).
    FooterLayout fl = computeFooter(100, "seq 99", "vverylongbuildid",
                                    "blue: page r/h");
    ASSERT_TRUE(std::string(fl.left).find("vverylongbuildid") != std::string::npos);
    // The version string must be complete, not truncated.
    ASSERT_TRUE(std::string(fl.left).find("vverylongbuildid")
                != std::string::npos);
}

// --- seq dropped, version kept ------------------------------------------------

TEST(footer_seq_dropped_version_kept) {
    // Long seq + long version + long hint: even after shortening the hint to "..",
    // the full left string still doesn't fit, so the seq prefix is dropped and
    // only the version remains.  "seq 9999999999 · vverylongbuildidthatexceedslimit"
    // = 46 chars = 276 px.  avail = 240-9 = 231.  276+4+12(min hint) = 292 > 231.
    // After dropping seq: "vverylongbuildidthatexceedslimit" = 31 chars = 186 px.
    // 186+4+12 = 202 <= 231 → fits with hint shortened to "..".
    FooterLayout fl = computeFooter(240, "seq 9999999999", "vverylongbuildidthatexceedslimit",
                                    "blue: page   side: r/h");
    // Version must be present, unshortened.
    ASSERT_STREQ("vverylongbuildidthatexceedslimit", fl.left);
    // Seq should be dropped (left must NOT contain "seq").
    ASSERT_TRUE(std::string(fl.left).find("seq") == std::string::npos);
    ASSERT_TRUE(fl.hintDrawn);
}

// --- hint dropped entirely when screen is tiny ---------------------------------

TEST(footer_hint_dropped_on_tiny_screen) {
    // W=40, avail=31. Version "vtest"=30px fits but barely.
    // Hint can't fit → dropped.
    FooterLayout fl = computeFooter(40, "seq 5", "vtest",
                                    "blue: page   side: r/h");
    ASSERT_STREQ("vtest", fl.left);
    ASSERT_FALSE(fl.hintDrawn);
    ASSERT_EQ(0, (int)std::strlen(fl.hint));
}

// --- leftX always at 5 --------------------------------------------------------

TEST(footer_left_x_always_5) {
    FooterLayout fl = computeFooter(240, "seq 1", "vabc", "r/h");
    ASSERT_EQ(5, fl.leftX);
}

// --- hintX right-aligned -------------------------------------------------------

TEST(footer_hint_x_right_aligned) {
    FooterLayout fl = computeFooter(240, "seq 5", "vtest", "r/h");
    // hint "r/h" = 18 px. hintX = 240 - 18 - 4 = 218.
    ASSERT_EQ(240 - 18 - 4, fl.hintX);
}

// --- footer version survives when seq is 3 digits ------------------------------

TEST(footer_version_survives_3digit_seq) {
    // "seq 999 · vtest" = 17 chars = 102 px, hint "r/h" = 18 px.
    // 102 + 4 + 18 = 124 <= 231 → full left drawn with seq intact.
    FooterLayout fl = computeFooter(240, "seq 999", "vtest", "r/h");
    ASSERT_TRUE(std::string(fl.left).find("seq 999") != std::string::npos);
    ASSERT_TRUE(std::string(fl.left).find("vtest") != std::string::npos);
    ASSERT_TRUE(fl.hintDrawn);
}

// --- footer never draws past the screen edge ----------------------------------

TEST(footer_never_past_screen_edge) {
    // Even with a very long hint, the fitted hint must not extend past W.
    FooterLayout fl = computeFooter(240, "seq 1", "vabc",
                                    "blue: page   side: refresh and then some more");
    if (fl.hintDrawn) {
        int16_t hintRight = fl.hintX + measureFixed(fl.hint);
        ASSERT_TRUE(hintRight <= 240);
    }
    // Left string starts at x=5 and never goes past the right edge.
    int16_t leftRight = fl.leftX + measureFixed(fl.left);
    ASSERT_TRUE(leftRight <= 240);
}

// --- hint is shortened rather than version on collision ------------------------

TEST(footer_hint_shortened_not_version) {
    // 240px, short asOf but version must survive even when hint is shortened.
    FooterLayout fl = computeFooter(240, "seq 1", "vtest",
                                    "blue: page   side: r/h");
    // The version is always complete in the left string.
    ASSERT_TRUE(std::string(fl.left).find("vtest") != std::string::npos);
    // If the hint was shortened, it should end with "..".
    if (std::strlen(fl.hint) < std::strlen("blue: page   side: r/h")) {
        std::string h(fl.hint);
        ASSERT_TRUE(h.substr(h.size() - 2) == "..");
    }
}
