// firmware/test/host/test_render_plan.cpp — tests for buildPlan, onSnapshot,
// nextPage, tierName, tierColor565.
#include "framework.h"
#include "usage/render_plan.h"
#include "usage/model.h"

#include <cstring>

#include "fixtures/fixtures.h"

using usage::Model;
using usage::Provider;
using usage::RenderPlan;
using usage::View;
using usage::Line;

// --- buildPlan: page 1 ordering -------------------------------------------

TEST(plan_page1_order) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 0, plan);
    ASSERT_EQ(1u, plan.pageCount);
    ASSERT_EQ(1u, plan.page); // 1-indexed

    // Codex has severity "crit" → red banner on every page, 4 rows instead of 5.
    ASSERT_EQ(2u, plan.bannerTier);
    ASSERT_STREQ("ChatGPT", plan.banner);
    ASSERT_EQ(4, (int)plan.lineCount);

    // Expected order: CLAUDE 5h, CLAUDE 7d, FABLE 7d, GPT 5h (GPT 7d cut by banner).
    ASSERT_STREQ("CLAUDE 5h", plan.lines[0].left);
    ASSERT_EQ(19, plan.lines[0].pct);

    ASSERT_STREQ("CLAUDE 7d", plan.lines[1].left);
    ASSERT_EQ(30, plan.lines[1].pct);

    ASSERT_STREQ("FABLE 7d", plan.lines[2].left);
    ASSERT_EQ(20, plan.lines[2].pct);
    ASSERT_EQ(0, (int)plan.lines[2].tier);

    ASSERT_STREQ("GPT 5h", plan.lines[3].left);
    ASSERT_EQ(100, plan.lines[3].pct);
    ASSERT_EQ(2, (int)plan.lines[3].tier);

    // GPT bal is excluded from page 1.
    for (int i = 0; i < 4; i++) {
        ASSERT_TRUE(std::strcmp(plan.lines[i].left, "GPT bal") != 0);
    }
    // Footer empty (all providers ok status).
    ASSERT_STREQ("", plan.footer);
}

// --- onSnapshot: no redraw when rev unchanged ------------------------------

TEST(plan_no_redraw_same_rev) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    View view;
    std::memset(&view, 0, sizeof(view));
    view.page = 0;

    // First snapshot: needsRedraw should be set.
    view.needsRedraw = false;
    usage::onSnapshot(view, m);
    ASSERT_TRUE(view.needsRedraw);
    ASSERT_STREQ(m.rev, view.lastRev);

    // Second snapshot, same rev: no redraw.
    view.needsRedraw = true;
    usage::onSnapshot(view, m);
    ASSERT_TRUE(!view.needsRedraw);
}

// --- buildPlan: page 2 with full providers --------------------------------

TEST(plan_page2_full) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExampleFull,
                                   strlen(kSnapshotExampleFull),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 1, plan);
    ASSERT_EQ(2u, plan.pageCount);
    ASSERT_EQ(2u, plan.page); // 1-indexed

    // Crit provider (codex) makes the banner appear on EVERY page.
    ASSERT_EQ(2u, plan.bannerTier);
    ASSERT_STREQ("ChatGPT", plan.banner);
    ASSERT_EQ(4, (int)plan.lineCount); // 4 rows (banner steals one slot)

    // Page 2 begins with openrouter:main's bal row.
    ASSERT_STREQ("ORmain bal", plan.lines[0].left);
    ASSERT_EQ(99, plan.lines[0].pct);
    ASSERT_EQ(2, (int)plan.lines[0].tier);

    // Fallback account row label is distinct.
    ASSERT_STREQ("ORfbk bal", plan.lines[2].left);
    ASSERT_STREQ("ORfbk day", plan.lines[3].left);
    // GROQ key is cut (4 rows only with banner).
}

// --- ORDER #31: warm-boot wake produces exactly one render ------------------

// On warm boot from deep sleep, g_view.lastRev is NOT pre-seeded (empty).
// The first onSnapshot after restoring the cached model must see the rev
// change (empty vs model.rev) and set needsRedraw — exactly one render to
// paint the cached snapshot.  The next identical fetch (304 or 200-same-rev)
// must produce no additional render.
TEST(plan_wake_one_render_same_rev) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    // Simulate warm boot: lastRev is zeroed (not pre-seeded), needsRedraw
    // was cleared by the cached paint that already ran in redraw().
    View view;
    std::memset(&view, 0, sizeof(view));
    view.page = 0;

    // First onSnapshot after wake: lastRev empty vs model.rev → one render.
    usage::onSnapshot(view, m);
    ASSERT_TRUE(view.needsRedraw);
    ASSERT_STREQ(m.rev, view.lastRev);

    // Second identical fetch: revs match → no render.
    usage::onSnapshot(view, m);
    ASSERT_TRUE(!view.needsRedraw);
}

// --- nextPage wraps --------------------------------------------------------

TEST(plan_next_page_wraps) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExampleFull,
                                   strlen(kSnapshotExampleFull),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    View view;
    std::memset(&view, 0, sizeof(view));
    view.page = 0;

    usage::nextPage(view, m);
    ASSERT_EQ(1u, view.page);
    ASSERT_TRUE(view.needsRedraw);

    // Wrap back to page 0.
    usage::nextPage(view, m);
    ASSERT_EQ(0u, view.page);
}

// --- stale provider: dim + footer ------------------------------------------

TEST(plan_stale_dim_and_footer) {
    // Hand-built JSON with a stale claude provider.
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"deadbeef\","
        "\"generated_at\":0,\"next_sec\":900,"
        "\"providers\":[{\"id\":\"claude\",\"label\":\"Claude\",\""
        "plan\":\"max_20x\",\"status\":\"stale\",\"msg\":\"stale check\","
        "\"rows\":[{\"k\":\"5h\",\"label\":\"CLAUDE 5h\",\"pct\":19,"
        "\"txt\":\"05:09\",\"tier\":\"ok\",\"reset_at\":0}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 0, plan);
    ASSERT_EQ(1, (int)plan.lineCount);
    ASSERT_EQ(1, (int)plan.lines[0].dim);
    ASSERT_STREQ("stale check", plan.footer);
}

// --- tier helpers ---------------------------------------------------------

TEST(plan_tier_names) {
    ASSERT_STREQ("ok", usage::tierName(0));
    ASSERT_STREQ("warn", usage::tierName(1));
    ASSERT_STREQ("crit", usage::tierName(2));
    ASSERT_STREQ("off", usage::tierName(3));
    ASSERT_STREQ("ok", usage::tierName(99));
}

// --- banner: crit (ORDER #36 / task 50) -------------------------------------

TEST(plan_banner_crit) {
    // kSnapshotExample has codex with severity "crit", msg "".
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 0, plan);
    ASSERT_EQ(2u, plan.bannerTier);
    ASSERT_STREQ("ChatGPT", plan.banner);
    // Crit banner steals one row slot → 4 usage rows, not 5.
    ASSERT_EQ(4, (int)plan.lineCount);
}

// --- banner: warn label "!" (no banner row) ---------------------------------

TEST(plan_warn_label_no_banner) {
    // Hand-built JSON: openrouter:main is warn, no crit provider anywhere.
    // turn_context lines use the real top-level type (BUG 48 format).
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"deadbeef\",\"generated_at\":0,"
        "\"next_sec\":900,"
        "\"providers\":["
        "{\"id\":\"openrouter:main\",\"label\":\"OpenRouter main\","
        "\"plan\":\"paid\",\"severity\":\"warn\",\"status\":\"ok\","
        "\"msg\":\"low $0.07\","
        "\"rows\":[{\"k\":\"bal\",\"label\":\"ORmain bal\",\"pct\":99,"
        "\"txt\":\"$0.07\",\"tier\":\"crit\",\"reset_at\":null},"
        "{\"k\":\"day\",\"label\":\"ORmain day\",\"pct\":null,"
        "\"txt\":\"$0.00\",\"tier\":\"ok\",\"reset_at\":null}]}, "
        "{\"id\":\"groq\",\"label\":\"Groq\",\"plan\":\"on_demand\","
        "\"severity\":\"ok\",\"status\":\"ok\",\"msg\":\"\","
        "\"rows\":[{\"k\":\"key\",\"label\":\"GROQ key\",\"pct\":null,"
        "\"txt\":\"ok\",\"tier\":\"ok\",\"reset_at\":null}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 1, plan);  // page 2 (openrouter + groq)

    // Worst is warn → no banner, bannerTier stays 0.
    ASSERT_EQ(0u, plan.bannerTier);
    ASSERT_STREQ("", plan.banner);
    // All 5 row slots are available.
    ASSERT_EQ(3, (int)plan.lineCount);

    // Warn provider rows get "!" appended (truncated to fit 10-char label).
    ASSERT_STREQ("ORmain ba!", plan.lines[0].left);
    ASSERT_EQ(1, (int)plan.lines[0].warn);

    ASSERT_STREQ("ORmain da!", plan.lines[1].left);
    ASSERT_EQ(1, (int)plan.lines[1].warn);

    // Ok provider rows do NOT get "!" or the warn flag.
    ASSERT_STREQ("GROQ key", plan.lines[2].left);
    ASSERT_EQ(0, (int)plan.lines[2].warn);
}

// --- banner: all-ok yields today's exact layout ----------------------------

TEST(plan_all_ok_no_banner) {
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"abcd1234\",\"generated_at\":0,"
        "\"next_sec\":900,"
        "\"providers\":["
        "{\"id\":\"claude\",\"label\":\"Claude\",\"plan\":\"max_20x\","
        "\"severity\":\"ok\",\"status\":\"ok\",\"msg\":\"\","
        "\"rows\":[{\"k\":\"5h\",\"label\":\"CLAUDE 5h\",\"pct\":50,"
        "\"txt\":\"05:09\",\"tier\":\"ok\",\"reset_at\":0},"
        "{\"k\":\"7d\",\"label\":\"CLAUDE 7d\",\"pct\":60,"
        "\"txt\":\"Mon\",\"tier\":\"ok\",\"reset_at\":0}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    usage::buildPlan(m, 0, plan);

    // All-ok: no banner, 5-row layout exactly as today.
    ASSERT_EQ(0u, plan.bannerTier);
    ASSERT_STREQ("", plan.banner);
    ASSERT_EQ(2, (int)plan.lineCount);
    ASSERT_STREQ("CLAUDE 5h", plan.lines[0].left);
    ASSERT_EQ(0, (int)plan.lines[0].warn);
    ASSERT_STREQ("CLAUDE 7d", plan.lines[1].left);
    ASSERT_EQ(0, (int)plan.lines[1].warn);
}
