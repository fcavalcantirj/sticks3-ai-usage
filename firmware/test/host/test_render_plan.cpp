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
    ASSERT_EQ(5, (int)plan.lineCount);
    ASSERT_EQ(1u, plan.page); // 1-indexed

    // Expected order: CLAUDE 5h, CLAUDE 7d, FABLE 7d, GPT 5h, GPT 7d
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

    ASSERT_STREQ("GPT 7d", plan.lines[4].left);
    ASSERT_EQ(31, plan.lines[4].pct);

    // GPT bal is excluded from page 1.
    for (int i = 0; i < 5; i++) {
        ASSERT_TRUE(std::strcmp(plan.lines[i].left, "GPT bal") != 0);
    }
    // Footer empty (all providers ok).
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

    // Page 2 begins with openrouter:main's bal row.
    ASSERT_STREQ("ORmain bal", plan.lines[0].left);
    ASSERT_EQ(99, plan.lines[0].pct);
    ASSERT_EQ(2, (int)plan.lines[0].tier);

    // Fallback account row label is distinct.
    ASSERT_STREQ("ORfbk bal", plan.lines[2].left);
    ASSERT_STREQ("ORfbk day", plan.lines[3].left);
    ASSERT_STREQ("GROQ key", plan.lines[4].left);
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
