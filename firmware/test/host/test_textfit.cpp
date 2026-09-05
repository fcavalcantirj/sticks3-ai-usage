// firmware/test/host/test_textfit.cpp — host tests for usage::fitRight.
#include "framework.h"
#include "usage/textfit.h"

#include <cstring>

// Measure callback for tests: 6 pixels per character (fixed-width).
static int measure6(const char* s) {
    return 6 * (int)std::strlen(s);
}

TEST(textfit_shortens) {
    char out[64];
    size_t n;

    // Short string fits unchanged in 156 px.
    n = usage::fitRight("100% $178.10", out, sizeof(out), 156, measure6);
    ASSERT_STREQ("100% $178.10", out);
    ASSERT_EQ(12, (int)n);

    // 30-char string: 30*6=180 > 156, so shorten to 24+2=26 chars (26*6=156).
    char in30[31];
    std::memset(in30, 'A', 30);
    in30[30] = '\0';
    n = usage::fitRight(in30, out, sizeof(out), 156, measure6);
    ASSERT_EQ(26, (int)n);        // 24 chars + ".."
    ASSERT_STREQ("..", out + n - 2);  // ends with ".."
}

TEST(textfit_exact_fit) {
    char out[64];
    // 26 chars * 6 px = 156 px — exactly fits.
    const char* in = "ABCDEFGHIJKLMNOPQRSTUVWXYZ";  // 26 chars
    size_t n = usage::fitRight(in, out, sizeof(out), 156, measure6);
    ASSERT_STREQ(in, out);
    ASSERT_EQ(26, (int)n);
}

TEST(textfit_trunc_4byte) {
    // Buffer too small to hold ".." — returns 0.
    char out[4];
    size_t n = usage::fitRight("hello", out, sizeof(out), 2, measure6);
    ASSERT_EQ(0, (int)n);
    ASSERT_STREQ("", out);
}

TEST(textfit_pct_100_omits_reset) {
    char out[64];
    size_t n;

    // At 100% the bar leaves a narrow column (maxPx=54 ≈ real screen budget).
    // The full string "100% 21:59" (60 px) does not fit and truncates to
    // "100% 21.." — the defect ORDER #53 fixes.
    n = usage::fitRight("100% 21:59", out, sizeof(out), 54, measure6);
    ASSERT_STREQ("100% 21..", out);
    ASSERT_EQ(9, (int)n);

    // Showing just "100%" (24 px) fits cleanly — no truncation.
    n = usage::fitRight("100%", out, sizeof(out), 54, measure6);
    ASSERT_STREQ("100%", out);
    ASSERT_EQ(4, (int)n);
}
