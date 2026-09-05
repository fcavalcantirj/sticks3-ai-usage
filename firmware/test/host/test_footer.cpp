// firmware/test/host/test_footer.cpp — tests for the footer measure-then-fit
// layout (ORDER #56/57 task 60).
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

// The production hint (ORDER #57: whole words, no single-char cipher).
static const char* kHint = "blue: page   side: click hold";

// --- full fit: version + full hint fit unchanged (seq dropped) ------------------

TEST(footer_full_fit) {
    // With seq dropped, left = "vtest" (5 chars = 30 px), hint = 29 chars = 174 px.
    // avail = 240 - 5 - 4 = 231.  30 + 4 + 174 = 208 <= 231 → both verbatim.
    FooterLayout fl = computeFooter(240, "", "vtest", kHint);
    ASSERT_STREQ("vtest", fl.left);
    ASSERT_STREQ(kHint, fl.hint);
    ASSERT_EQ(5, fl.leftX);
    ASSERT_EQ(240 - measureFixed(kHint) - 4, fl.hintX);
    ASSERT_TRUE(fl.hintDrawn);
}

// --- hint shortened before seq dropped (seq still present) ---------------------

TEST(footer_hint_shortened_before_seq) {
    // Long left string with seq: "seq 99 · vverylongbuildid" = 25 chars = 150 px.
    // avail = 231.  150 + 4 + 174 = 328 > 231 → hint must shrink, seq kept.
    FooterLayout fl = computeFooter(240, "seq 99", "vverylongbuildid", kHint);
    ASSERT_TRUE(std::string(fl.left).find("seq 99") != std::string::npos);
    ASSERT_TRUE(std::string(fl.left).find("vverylongbuildid") != std::string::npos);
    ASSERT_TRUE(std::strlen(fl.hint) < std::strlen(kHint));
    ASSERT_TRUE(fl.hintDrawn);
}

// --- version NEVER shortened --------------------------------------------------

TEST(footer_version_never_shortened) {
    // W=100, avail=91.  "seq 99 · vverylongbuildidthatexceeds91" is way over.
    // Even version alone (vverylongbuildidthatexceeds91 = 38 chars = 228 px)
    // exceeds 91, but the version is emitted complete (not truncated).
    FooterLayout fl = computeFooter(100, "seq 99", "vverylongbuildidthatexceeds91", kHint);
    ASSERT_TRUE(std::string(fl.left).find("vverylongbuildidthatexceeds91")
                != std::string::npos);
}

// --- seq dropped, version kept (future use) -----------------------------------

TEST(footer_seq_dropped_version_kept) {
    // Long seq + long version + long hint: even after shortening the hint to "..",
    // the full left still doesn't fit, so the seq prefix is dropped.
    FooterLayout fl = computeFooter(240, "seq 9999999999", "vverylongbuildidthatexceedslimit", kHint);
    ASSERT_STREQ("vverylongbuildidthatexceedslimit", fl.left);
    ASSERT_TRUE(std::string(fl.left).find("seq") == std::string::npos);
    ASSERT_TRUE(fl.hintDrawn);
}

// --- hint dropped entirely when screen is tiny --------------------------------

TEST(footer_hint_dropped_on_tiny_screen) {
    // W=40, avail=31. Version "vtest"=30px fits but barely.
    // Hint can't fit → dropped.
    FooterLayout fl = computeFooter(40, "", "vtest", kHint);
    ASSERT_STREQ("vtest", fl.left);
    ASSERT_FALSE(fl.hintDrawn);
    ASSERT_EQ(0, (int)std::strlen(fl.hint));
}

// --- leftX always at 5 --------------------------------------------------------

TEST(footer_left_x_always_5) {
    FooterLayout fl = computeFooter(240, "", "vabc", "click hold");
    ASSERT_EQ(5, fl.leftX);
}

// --- hintX right-aligned ------------------------------------------------------

TEST(footer_hint_x_right_aligned) {
    FooterLayout fl = computeFooter(240, "", "vtest", "click hold");
    // hint "click hold" = 11 chars = 66 px. hintX = 240 - 66 - 4 = 170.
    ASSERT_EQ(240 - measureFixed("click hold") - 4, fl.hintX);
}

// --- no single-character tokens between separators (ORDER #57) -----------------

TEST(footer_no_single_char_cipher) {
    // The hint must not contain a single-character token between separators
    // or end-of-string — "r/h" is a cipher, "click hold" is not.
    FooterLayout fl = computeFooter(240, "", "vtest", kHint);
    std::string h(fl.hint);
    // Split on spaces and '/' — every token must be >= 2 chars.
    // Replace '/' with ' ' so we can split on whitespace.
    std::string normalized = h;
    for (auto& c : normalized) {
        if (c == '/') c = ' ';
    }
    std::string token;
    bool bad = false;
    for (size_t i = 0; i <= normalized.size(); i++) {
        if (i == normalized.size() || normalized[i] == ' ') {
            if (token.size() == 1) {
                bad = true;
                break;
            }
            token.clear();
        } else {
            token += normalized[i];
        }
    }
    ASSERT_FALSE(bad);
}

// --- footer never draws past the screen edge ----------------------------------

TEST(footer_never_past_screen_edge) {
    FooterLayout fl = computeFooter(240, "", "vabc",
                                    "blue: page   side: refresh and then some more text");
    if (fl.hintDrawn) {
        int16_t hintRight = fl.hintX + measureFixed(fl.hint);
        ASSERT_TRUE(hintRight <= 240);
    }
    int16_t leftRight = fl.leftX + measureFixed(fl.left);
    ASSERT_TRUE(leftRight <= 240);
}

// --- hint shortened (not version) when both collide --------------------------

TEST(footer_hint_shortened_not_version) {
    FooterLayout fl = computeFooter(240, "", "vtest", kHint);
    // Version is always complete in the left string.
    ASSERT_TRUE(std::string(fl.left).find("vtest") != std::string::npos);
}
