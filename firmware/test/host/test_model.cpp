// firmware/test/host/test_model.cpp — host tests for usage::parseSnapshot.
//
// Exercises parseSnapshot against the real Go golden fixtures (embedded via
// fixtures.h) and a few hand-written edge cases.  This file does NOT define
// TEST_FRAMEWORK_MAIN — test_smoke.cpp owns the entry point.
#include "framework.h"
#include "usage/model.h"

#include <ArduinoJson.h>
#include <cstring>

// The JSON fixtures live in test/host/fixtures/; the CMake include path adds
// test/host/fixtures so we can #include it without a directory prefix.
#include "fixtures/fixtures.h"

using usage::Model;
using usage::Provider;
using usage::Row;

// --- golden fixtures ----------------------------------------------------

TEST(model_parses_example) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_TRUE(err[0] == '\0'); // err empty on success

    // Top-level fields.
    ASSERT_EQ(1, (int)m.v);
    ASSERT_STREQ("a1b2c3d4", m.rev);
    ASSERT_EQ(1u, m.seq);
    ASSERT_EQ(1788414949u, m.generatedAt);
    ASSERT_EQ(900u, m.nextSec);

    // Two providers: claude, codex.
    ASSERT_EQ(2, (int)m.providerCount);

    const Provider& claude = m.providers[0];
    ASSERT_STREQ("claude", claude.id);
    ASSERT_STREQ("Claude", claude.label);
    ASSERT_EQ(0, (int)claude.status);  // ok
    ASSERT_EQ(3, (int)claude.rowCount);

    // CLAUDE 5h: pct 19, tier 0 (ok)
    ASSERT_STREQ("CLAUDE 5h", claude.rows[0].label);
    ASSERT_EQ(19, claude.rows[0].pct);
    ASSERT_EQ(0, (int)claude.rows[0].tier);

    // FABLE 7d: pct 20, tier 0
    const Row& fable = claude.rows[2];
    ASSERT_STREQ("7d:Fable", fable.k);
    ASSERT_STREQ("FABLE 7d", fable.label);
    ASSERT_EQ(20, fable.pct);
    ASSERT_EQ(0, (int)fable.tier);

    // Codex provider.
    const Provider& codex = m.providers[1];
    ASSERT_STREQ("codex", codex.id);
    ASSERT_EQ(3, (int)codex.rowCount);

    // GPT 5h: pct 100, tier 2 (crit)
    const Row& gpt5h = codex.rows[0];
    ASSERT_STREQ("GPT 5h", gpt5h.label);
    ASSERT_EQ(100, gpt5h.pct);
    ASSERT_EQ(2, (int)gpt5h.tier);

    // GPT bal: pct null (-1)
    const Row& gptBal = codex.rows[2];
    ASSERT_STREQ("GPT bal", gptBal.label);
    ASSERT_EQ(-1, gptBal.pct);
    ASSERT_STREQ("$178.10", gptBal.txt);
}

TEST(model_parses_full) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExampleFull,
                                   strlen(kSnapshotExampleFull),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_EQ(5, (int)m.providerCount);

    // Canonical provider order: claude, codex, openrouter:main, fallback, groq.
    ASSERT_STREQ("claude", m.providers[0].id);
    ASSERT_STREQ("codex", m.providers[1].id);
    ASSERT_STREQ("openrouter:main", m.providers[2].id);
    ASSERT_STREQ("OpenRouter main", m.providers[2].label);
    ASSERT_STREQ("openrouter:fallback", m.providers[3].id);
    ASSERT_STREQ("OpenRouter fallback", m.providers[3].label);
    ASSERT_STREQ("groq", m.providers[4].id);

    // OpenRouter main: bal pct 99, tier 2 (crit)
    const Row& orBal = m.providers[2].rows[0];
    ASSERT_STREQ("ORmain bal", orBal.label);
    ASSERT_EQ(99, orBal.pct);
    ASSERT_EQ(2, (int)orBal.tier);

    // OpenRouter main: day pct null (-1), txt $0.00
    const Row& orDay = m.providers[2].rows[1];
    ASSERT_STREQ("ORmain day", orDay.label);
    ASSERT_EQ(-1, orDay.pct);
    ASSERT_STREQ("$0.00", orDay.txt);
    ASSERT_EQ(0, (int)orDay.tier);

    // OpenRouter fallback: distinct row labels (ORfbk*)
    const Row& fbkBal = m.providers[3].rows[0];
    ASSERT_STREQ("ORfbk bal", fbkBal.label);
    ASSERT_STREQ("ORfbk day", m.providers[3].rows[1].label);

    // Groq: single row, pct null
    const Row& groqKey = m.providers[4].rows[0];
    ASSERT_STREQ("GROQ key", groqKey.label);
    ASSERT_EQ(-1, groqKey.pct);
    ASSERT_STREQ("ok", groqKey.txt);
}

// --- edge cases ---------------------------------------------------------

TEST(model_rejects_bad_version) {
    Model m;
    char err[256];
    const char* bad =
        "{\"v\":2,\"seq\":1,\"rev\":\"a1b2c3d4\","
        "\"generated_at\":0,\"next_sec\":900,\"providers\":[]}";
    bool ok = usage::parseSnapshot(bad, strlen(bad), m, err, sizeof(err));
    ASSERT_TRUE(!ok);
    ASSERT_TRUE(err[0] != '\0'); // err populated
}

TEST(model_rejects_malformed) {
    Model m;
    char err[256];
    const char* bad = "{not valid json,,,}";
    bool ok = usage::parseSnapshot(bad, strlen(bad), m, err, sizeof(err));
    ASSERT_TRUE(!ok);
    ASSERT_TRUE(err[0] != '\0');
}

TEST(model_truncates_label) {
    // A 12-char label must be truncated to 10 chars (char label[11]).
    // ASan will catch any overflow.
    Model m;
    char err[256];
    const char* json =
        "{\"v\":1,\"seq\":1,\"rev\":\"a1b2c3d4\","
        "\"generated_at\":0,\"next_sec\":900,"
        "\"providers\":[{\"id\":\"test\",\"label\":\"Test\",\"plan\":"
        "\"free\",\"status\":\"ok\",\"msg\":\"\",\"rows\":[{\"k\":\""
        "x\",\"label\":\"ABCDEFGHIJKL\",\"pct\":null,\"txt\":\"X\",\""
        "tier\":\"ok\",\"reset_at\":0}]}]}";
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);
    ASSERT_EQ(10, (int)std::strlen(m.providers[0].rows[0].label));
    ASSERT_STREQ("ABCDEFGHIJ", m.providers[0].rows[0].label);
}

TEST(model_rejects_missing_rev) {
    Model m;
    char err[256];
    const char* json =
        "{\"v\":1,\"seq\":1,\"generated_at\":0,"
        "\"next_sec\":900,\"providers\":[]}";
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(!ok);
    ASSERT_TRUE(err[0] != '\0');
}
