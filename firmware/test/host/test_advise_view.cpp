// firmware/test/host/test_advise_view.cpp — tests for parseAdvise +
// buildAdviseLayout (the /v1/advise overlay parsing).
//
// This file does NOT define TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the
// entry point.
#include "framework.h"
#include "usage/advise_view.h"

#include <cstdio>
#include <cstring>

using usage::AdvisePlan;
using usage::AdviseRec;
using usage::kMaxRecs;

// --- parseAdvise: error cases --------------------------------------------------

// Empty string is invalid JSON — parseAdvise must return false.
TEST(advise_parse_empty_string) {
    AdvisePlan plan;
    char err[256];
    bool ok = usage::parseAdvise("", 0, plan, err, sizeof(err));
    ASSERT_FALSE(ok);
    ASSERT_TRUE(err[0] != '\0');
}

// Malformed JSON must return false with a non-empty error.
TEST(advise_parse_malformed) {
    AdvisePlan plan;
    char err[256];
    const char* json = "not json at all {{{}}}";
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_FALSE(ok);
    ASSERT_TRUE(err[0] != '\0');
}

// Truncated JSON (missing closing brace) is malformed.
TEST(advise_parse_truncated) {
    AdvisePlan plan;
    char err[256];
    const char* json = "{\"winner\":\"codex\",\"recommendations\":[";
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_FALSE(ok);
    ASSERT_TRUE(err[0] != '\0');
}

// --- parseAdvise: success cases ------------------------------------------------

// A well-formed response with a winner and two recommendations.
TEST(advise_parse_valid_with_winner) {
    AdvisePlan plan;
    char err[256];
    const char* json =
        "{\"winner\":\"codex\","
        "\"recommendations\":["
        "{\"id\":\"codex\",\"label\":\"ChatGPT\",\"score\":100,"
        "\"pace_ratio\":0.0,\"effective_headroom_pct\":100,"
        "\"reason\":\"binding 7d resets in 126h\"},"
        "{\"id\":\"claude\",\"label\":\"Claude\",\"score\":19,"
        "\"pace_ratio\":2.3,\"effective_headroom_pct\":37,"
        "\"reason\":\"5h at 25% resets soon\"}]}"
        ;
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_TRUE(plan.fetchOk);
    ASSERT_TRUE(plan.hasWinner);
    ASSERT_STREQ("codex", plan.winnerId);
    ASSERT_EQ(2, (int)plan.recCount);

    // Winner's label and reason lifted to top-level (matched by id).
    ASSERT_STREQ("ChatGPT", plan.winnerLabel);
    ASSERT_STREQ("binding 7d resets in 126h", plan.reason);

    // First recommendation is the winner.
    ASSERT_STREQ("codex", plan.recs[0].id);
    ASSERT_STREQ("ChatGPT", plan.recs[0].label);
    ASSERT_EQ(100, plan.recs[0].score);
    ASSERT_EQ(0.0f, plan.recs[0].paceRatio);
    ASSERT_EQ(100, plan.recs[0].effectiveHeadroomPct);

    // Second recommendation.
    ASSERT_STREQ("claude", plan.recs[1].id);
    ASSERT_STREQ("Claude", plan.recs[1].label);
    ASSERT_EQ(19, plan.recs[1].score);
}

// Null winner means no clear winner — hasWinner is false, top-level fields empty.
TEST(advise_parse_null_winner) {
    AdvisePlan plan;
    char err[256];
    const char* json =
        "{\"winner\":null,"
        "\"recommendations\":["
        "{\"id\":\"codex\",\"label\":\"ChatGPT\",\"score\":100,"
        "\"pace_ratio\":0.0,\"effective_headroom_pct\":100,\"reason\":\"ok\"}]}"
        ;
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_TRUE(plan.fetchOk);
    ASSERT_FALSE(plan.hasWinner);
    ASSERT_STREQ("", plan.winnerId);
    ASSERT_STREQ("", plan.winnerLabel);
    ASSERT_STREQ("", plan.reason);
    ASSERT_EQ(1, (int)plan.recCount);
    ASSERT_STREQ("codex", plan.recs[0].id);
}

// Missing "winner" key entirely — treated as null (no winner).
TEST(advise_parse_missing_winner_key) {
    AdvisePlan plan;
    char err[256];
    const char* json =
        "{\"recommendations\":["
        "{\"id\":\"codex\",\"label\":\"ChatGPT\",\"score\":50,"
        "\"pace_ratio\":0.5,\"effective_headroom_pct\":50,\"reason\":\"ok\"}]}"
        ;
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_FALSE(plan.hasWinner);
    ASSERT_EQ(1, (int)plan.recCount);
}

// Empty recommendations with a winner — hasWinner is true but recs are empty,
// so winnerLabel and reason stay empty (winner not found in recs).
TEST(advise_parse_winner_but_no_recs) {
    AdvisePlan plan;
    char err[256];
    const char* json = "{\"winner\":\"codex\",\"recommendations\":[]}";
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_TRUE(plan.fetchOk);
    ASSERT_TRUE(plan.hasWinner);
    ASSERT_STREQ("codex", plan.winnerId);
    ASSERT_STREQ("", plan.winnerLabel);
    ASSERT_STREQ("", plan.reason);
    ASSERT_EQ(0, (int)plan.recCount);
}

// Empty recommendations array with correct JSON syntax.
TEST(advise_parse_empty_recs_valid) {
    AdvisePlan plan;
    char err[256];
    const char* json = "{\"winner\":null,\"recommendations\":[]}";
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_FALSE(plan.hasWinner);
    ASSERT_EQ(0, (int)plan.recCount);
}

// Winner id not present in recs — top-level fields stay empty, recs still parsed.
TEST(advise_parse_winner_not_in_recs) {
    AdvisePlan plan;
    char err[256];
    const char* json =
        "{\"winner\":\"opencode\","
        "\"recommendations\":["
        "{\"id\":\"codex\",\"label\":\"ChatGPT\",\"score\":100,"
        "\"pace_ratio\":0.0,\"effective_headroom_pct\":100,\"reason\":\"ok\"},"
        "{\"id\":\"claude\",\"label\":\"Claude\",\"score\":19,"
        "\"pace_ratio\":2.3,\"effective_headroom_pct\":37,\"reason\":\"ok\"}]}"
        ;
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_TRUE(plan.hasWinner);
    ASSERT_STREQ("opencode", plan.winnerId);
    // Winner not found in recs → winnerLabel and reason stay empty.
    ASSERT_STREQ("", plan.winnerLabel);
    ASSERT_STREQ("", plan.reason);
    ASSERT_EQ(2, (int)plan.recCount);
}

// Recommendation list longer than kMaxRecs (5) is silently clamped.
TEST(advise_parse_clamps_to_kmaxrecs) {
    AdvisePlan plan;
    char err[256];
    const char* json =
        "{\"winner\":null,"
        "\"recommendations\":["
        "{\"id\":\"a\",\"label\":\"A\",\"score\":1,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":100,\"reason\":\"r1\"},"
        "{\"id\":\"b\",\"label\":\"B\",\"score\":2,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":90,\"reason\":\"r2\"},"
        "{\"id\":\"c\",\"label\":\"C\",\"score\":3,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":80,\"reason\":\"r3\"},"
        "{\"id\":\"d\",\"label\":\"D\",\"score\":4,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":70,\"reason\":\"r4\"},"
        "{\"id\":\"e\",\"label\":\"E\",\"score\":5,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":60,\"reason\":\"r5\"},"
        "{\"id\":\"f\",\"label\":\"F\",\"score\":6,\"pace_ratio\":1.0,"
        "\"effective_headroom_pct\":50,\"reason\":\"r6\"}]}";
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_EQ(kMaxRecs, plan.recCount);  // clamped to 5
    ASSERT_STREQ("a", plan.recs[0].id);
    ASSERT_STREQ("e", plan.recs[4].id);
}

// Long field values are truncated to fit the bounded buffers.
TEST(advise_parse_truncates_long_fields) {
    AdvisePlan plan;
    char err[256];
    // id is 24 bytes, label is 24 bytes, reason is 64 bytes.
    const char* json =
        "{\"winner\":null,"
        "\"recommendations\":["
        "{\"id\":\"this_is_a_very_long_provider_id\","
        "\"label\":\"This Is A Very Long Label That Exceeds Buffer\","
        "\"score\":75,\"pace_ratio\":1.5,"
        "\"effective_headroom_pct\":50,"
        "\"reason\":\"This is a very long reason that definitely "
        "exceeds the 64-byte reason buffer in the AdviseRec struct\"}]}"
        ;
    bool ok = usage::parseAdvise(json, std::strlen(json), plan, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_EQ(1, (int)plan.recCount);
    // id truncated to 23 chars + NUL.
    ASSERT_EQ(23, (int)std::strlen(plan.recs[0].id));
    ASSERT_EQ(23, (int)sizeof(plan.recs[0].id) - 1);
    // label truncated to 23 chars + NUL.
    ASSERT_EQ(23, (int)std::strlen(plan.recs[0].label));
}

// --- buildAdviseLayout ---------------------------------------------------------

// Winner not in first position is rotated to index 0.
TEST(advise_layout_winner_rotated_to_front) {
    AdvisePlan plan;
    // Manually build a plan where winner is at index 1 (not first).
    plan.hasWinner = true;
    std::strcpy(plan.winnerId, "claude");
    plan.recCount = 2;
    std::strcpy(plan.recs[0].id, "codex");
    std::strcpy(plan.recs[1].id, "claude");

    usage::buildAdviseLayout(plan);

    // Winner (claude) should now be at index 0, codex shifted to index 1.
    ASSERT_STREQ("claude", plan.recs[0].id);
    ASSERT_STREQ("codex", plan.recs[1].id);
}

// No winner → layout is a no-op (order unchanged).
TEST(advise_layout_no_winner_noop) {
    AdvisePlan plan;
    plan.hasWinner = false;
    plan.recCount = 2;
    std::strcpy(plan.recs[0].id, "codex");
    std::strcpy(plan.recs[1].id, "claude");

    usage::buildAdviseLayout(plan);

    ASSERT_STREQ("codex", plan.recs[0].id);
    ASSERT_STREQ("claude", plan.recs[1].id);
}

// Winner already at index 0 → no change.
TEST(advise_layout_winner_already_first) {
    AdvisePlan plan;
    plan.hasWinner = true;
    std::strcpy(plan.winnerId, "codex");
    plan.recCount = 2;
    std::strcpy(plan.recs[0].id, "codex");
    std::strcpy(plan.recs[1].id, "claude");

    usage::buildAdviseLayout(plan);

    ASSERT_STREQ("codex", plan.recs[0].id);
    ASSERT_STREQ("claude", plan.recs[1].id);
}

// Empty recCount → no-op.
TEST(advise_layout_empty_recs) {
    AdvisePlan plan;
    plan.hasWinner = true;
    std::strcpy(plan.winnerId, "codex");
    plan.recCount = 0;

    usage::buildAdviseLayout(plan);

    ASSERT_EQ(0, (int)plan.recCount);
}

// Winner id empty → no reordering (treated as no winner for layout purposes).
TEST(advise_layout_empty_winner_id) {
    AdvisePlan plan;
    plan.hasWinner = true;
    plan.winnerId[0] = '\0';  // non-null winner field but empty id
    plan.recCount = 2;
    std::strcpy(plan.recs[0].id, "codex");
    std::strcpy(plan.recs[1].id, "claude");

    usage::buildAdviseLayout(plan);

    // No reordering because winnerId is empty.
    ASSERT_STREQ("codex", plan.recs[0].id);
    ASSERT_STREQ("claude", plan.recs[1].id);
}
