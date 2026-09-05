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

// Helper: build a plan for a given page with a test build ID.
static void buildTestPlan(const Model& m, uint8_t page, RenderPlan& plan) {
    usage::buildPlan(m, page, "test", plan);
}

// --- buildPlan: page 0 (PLANS, crit banner) -----------------------------------

TEST(plan_page0_plans_crit) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    buildTestPlan(m, 0, plan);

    // 2 PLAN providers (claude 3 rows + codex 3 rows = 6).  No row stealing
    // (ORDER #48 defect e) → maxLines=5.  ceil(6/5) = 2 PLAN pages.
    ASSERT_EQ(2u, plan.pageCount);
    ASSERT_EQ(1u, plan.page); // 1-indexed
    ASSERT_STREQ("PLANS", plan.title);
    ASSERT_STREQ("seq 1", plan.asOf);
    ASSERT_STREQ("vtest", plan.buildId);
    ASSERT_EQ(usage::KIND_PLAN, plan.kind);

    // Crit banner from ChatGPT (codex) — msg is empty, so the worst row
    // (GPT 5h pct=100 crit) is appended.  (ORDER #48 defect d)
    ASSERT_EQ(2u, plan.bannerTier);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", plan.banner);

    // 5 rows: CLAUDE 5h, CLAUDE 7d, FABLE 7d, GPT 5h, GPT 7d.
    ASSERT_EQ(5, (int)plan.lineCount);

    ASSERT_STREQ("CLAUDE 5h", plan.lines[0].left);
    ASSERT_EQ(19, plan.lines[0].pct);
    ASSERT_EQ(0, (int)plan.lines[0].tier);

    ASSERT_STREQ("CLAUDE 7d", plan.lines[1].left);
    ASSERT_EQ(30, plan.lines[1].pct);

    ASSERT_STREQ("FABLE 7d", plan.lines[2].left);
    ASSERT_EQ(20, plan.lines[2].pct);

    ASSERT_STREQ("GPT 5h", plan.lines[3].left);
    ASSERT_EQ(100, plan.lines[3].pct);
    ASSERT_EQ(2, (int)plan.lines[3].tier); // crit
    // ORDER #53 / task 52: reset text survives intact in the RenderPlan even at 100%.
    ASSERT_STREQ("23:13", plan.lines[3].right);

    // 5th row: GPT 7d (overflow from ChatGPT).
    ASSERT_STREQ("GPT 7d", plan.lines[4].left);
    ASSERT_EQ(31, plan.lines[4].pct);

    // Footer holds the alert text when bannerTier >= 1 (BUG 52b).
    ASSERT_STREQ("ChatGPT GPT 5h 100%", plan.footer);
}

// --- buildPlan: page 1 overflow (PLANS) ----------------------------------------

TEST(plan_page1_plans_overflow) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    buildTestPlan(m, 1, plan);

    ASSERT_EQ(2u, plan.pageCount);
    ASSERT_EQ(2u, plan.page);
    ASSERT_STREQ("PLANS", plan.title);
    ASSERT_EQ(usage::KIND_PLAN, plan.kind);

    // Banner on every page.
    ASSERT_EQ(2u, plan.bannerTier);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", plan.banner);

    // 1 remaining row: GPT cr.
    ASSERT_EQ(1, (int)plan.lineCount);
    ASSERT_STREQ("GPT cr", plan.lines[0].left);
    ASSERT_EQ(-1, plan.lines[0].pct);
}

// --- buildPlan: full example, 4 pages ------------------------------------------

TEST(plan_full_four_pages) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExampleFull,
                                   strlen(kSnapshotExampleFull),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

    ASSERT_EQ(5, (int)m.providerCount);

    // Page 0: PLANS, 5 rows (no row stealing — maxLines=5).
    RenderPlan p0;
    buildTestPlan(m, 0, p0);
    ASSERT_EQ(4u, p0.pageCount);
    ASSERT_EQ(1u, p0.page);
    ASSERT_STREQ("PLANS", p0.title);
    ASSERT_EQ(usage::KIND_PLAN, p0.kind);
    ASSERT_EQ(0x3B9F, p0.kindColor); // blue
    ASSERT_EQ(2u, p0.bannerTier);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", p0.banner);
    ASSERT_EQ(5, (int)p0.lineCount);
    ASSERT_STREQ("CLAUDE 5h", p0.lines[0].left);
    ASSERT_EQ(19, p0.lines[0].pct);
    ASSERT_STREQ("CLAUDE 7d", p0.lines[1].left);
    ASSERT_STREQ("FABLE 7d", p0.lines[2].left);
    ASSERT_STREQ("GPT 5h", p0.lines[3].left);
    ASSERT_EQ(100, p0.lines[3].pct);
    ASSERT_EQ(2, (int)p0.lines[3].tier); // crit row, not dim
    ASSERT_STREQ("23:13", p0.lines[3].right);
    ASSERT_STREQ("GPT 7d", p0.lines[4].left);
    ASSERT_EQ(31, p0.lines[4].pct);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", p0.footer);

    // Page 1: PLANS overflow, 1 row (GPT cr).
    RenderPlan p1;
    buildTestPlan(m, 1, p1);
    ASSERT_EQ(2u, p1.page);
    ASSERT_STREQ("PLANS", p1.title);
    ASSERT_EQ(1, (int)p1.lineCount);
    ASSERT_STREQ("GPT cr", p1.lines[0].left);
    ASSERT_EQ(-1, p1.lines[0].pct);

    // Page 2: CREDITS, 4 rows (ORmain bal, ORmain day, ORfbk bal, ORfbk day).
    RenderPlan p2;
    buildTestPlan(m, 2, p2);
    ASSERT_EQ(3u, p2.page);
    ASSERT_STREQ("CREDITS", p2.title);
    ASSERT_EQ(usage::KIND_CREDIT, p2.kind);
    ASSERT_EQ(0x07E0, p2.kindColor); // green
    ASSERT_EQ(4, (int)p2.lineCount);
    ASSERT_STREQ("ORmain bal", p2.lines[0].left);
    ASSERT_EQ(99, p2.lines[0].pct);
    ASSERT_EQ(2, (int)p2.lines[0].tier); // crit
    ASSERT_STREQ("ORmain day", p2.lines[1].left);
    ASSERT_STREQ("ORfbk bal", p2.lines[2].left);
    ASSERT_STREQ("ORfbk day", p2.lines[3].left);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", p2.footer);

    // Page 3: FREE, 1 row (GROQ key).
    RenderPlan p3;
    buildTestPlan(m, 3, p3);
    ASSERT_EQ(4u, p3.page);
    ASSERT_STREQ("FREE", p3.title);
    ASSERT_EQ(usage::KIND_FREE, p3.kind);
    ASSERT_EQ(0x8410, p3.kindColor); // grey
    ASSERT_EQ(1, (int)p3.lineCount);
    ASSERT_STREQ("GROQ key", p3.lines[0].left);
    ASSERT_EQ(-1, p3.lines[0].pct);
    // Banner persists across all pages (global severity).
    ASSERT_EQ(2u, p3.bannerTier);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", p3.banner);
    ASSERT_STREQ("ChatGPT GPT 5h 100%", p3.footer);
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

// --- ORDER #31: warm-boot wake produces exactly one render -------------------

TEST(plan_wake_one_render_same_rev) {
    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(kSnapshotExample, strlen(kSnapshotExample),
                                   m, err, sizeof(err));
    ASSERT_TRUE(ok);

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

// --- nextPage wraps across all kind-pages -----------------------------------

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

    // 4 pages total; cycling through all and wrapping.
    usage::nextPage(view, m);
    ASSERT_EQ(1u, view.page);
    ASSERT_TRUE(view.needsRedraw);

    usage::nextPage(view, m);
    ASSERT_EQ(2u, view.page);

    usage::nextPage(view, m);
    ASSERT_EQ(3u, view.page);

    // Wrap back to page 0.
    usage::nextPage(view, m);
    ASSERT_EQ(0u, view.page);
}

// --- stale provider: dim + footer -------------------------------------------

TEST(plan_stale_dim_and_footer) {
    // Hand-built JSON with a stale claude provider.
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"deadbeef\","
        "\"generated_at\":0,\"next_sec\":900,"
        "\"providers\":[{\"id\":\"claude\",\"label\":\"Claude\","
        "\"plan\":\"max_20x\",\"kind\":\"plan\",\"status\":\"stale\",\"msg\":\"stale check\","
        "\"rows\":[{\"k\":\"5h\",\"label\":\"CLAUDE 5h\",\"pct\":19,"
        "\"txt\":\"05:09\",\"tier\":\"ok\",\"reset_at\":0}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    buildTestPlan(m, 0, plan);
    ASSERT_EQ(1, (int)plan.lineCount);
    ASSERT_EQ(1, (int)plan.lines[0].dim);
    // No crit banner (severity defaults to ok) → footer is empty.
    // The stale msg is not shown in the footer (BUG 52b: footer is device state).
    ASSERT_STREQ("", plan.footer);
}

// --- tier helpers -----------------------------------------------------------

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
    buildTestPlan(m, 0, plan);
    ASSERT_EQ(2u, plan.bannerTier);
    // msg is empty → banner describes the worst row: "label row_label pct%".
    ASSERT_STREQ("ChatGPT GPT 5h 100%", plan.banner);
    // No row stealing — maxLines is always 5.
    ASSERT_EQ(5, (int)plan.lineCount);
}

// --- banner: warn (no crit banner row, per-row ! tint) -------------------------

TEST(plan_warn_label_no_banner) {
    // Hand-built JSON: openrouter:main is warn, no crit provider anywhere.
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"deadbeef\",\"generated_at\":0,"
        "\"next_sec\":900,"
        "\"providers\":["
        "{\"id\":\"openrouter:main\",\"label\":\"OpenRouter main\","
        "\"plan\":\"paid\",\"kind\":\"credit\",\"severity\":\"warn\",\"status\":\"ok\","
        "\"msg\":\"low $0.07\","
        "\"rows\":[{\"k\":\"bal\",\"label\":\"ORmain bal\",\"pct\":99,"
        "\"txt\":\"$0.07\",\"tier\":\"crit\",\"reset_at\":null},"
        "{\"k\":\"day\",\"label\":\"ORmain day\",\"pct\":null,"
        "\"txt\":\"$0.00\",\"tier\":\"ok\",\"reset_at\":null}]}, "
        "{\"id\":\"groq\",\"label\":\"Groq\",\"plan\":\"on_demand\","
        "\"kind\":\"free\",\"severity\":\"ok\",\"status\":\"ok\",\"msg\":\"\","
        "\"rows\":[{\"k\":\"key\",\"label\":\"GROQ key\",\"pct\":null,"
        "\"txt\":\"ok\",\"tier\":\"ok\",\"reset_at\":null}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    // 2 pages: CREDITS (page 0), FREE (page 1). warn worst → maxLines=5.
    RenderPlan plan;
    buildTestPlan(m, 0, plan);

    // Worst is warn → no crit banner (bannerTier stays 0).
    ASSERT_EQ(0u, plan.bannerTier);
    ASSERT_STREQ("", plan.banner);
    ASSERT_STREQ("CREDITS", plan.title);
    ASSERT_EQ(usage::KIND_CREDIT, plan.kind);
    ASSERT_EQ(2, (int)plan.lineCount);

    // Warn provider rows get "!" appended (truncated to fit 10-char label).
    ASSERT_STREQ("ORmain ba!", plan.lines[0].left);
    ASSERT_EQ(1, (int)plan.lines[0].warn);

    ASSERT_STREQ("ORmain da!", plan.lines[1].left);
    ASSERT_EQ(1, (int)plan.lines[1].warn);
}

// --- all-ok, no banner -----------------------------------------------------

TEST(plan_all_ok_no_banner) {
    const char* json =
        "{\"v\":1,\"seq\":5,\"rev\":\"abcd1234\",\"generated_at\":0,"
        "\"next_sec\":900,"
        "\"providers\":["
        "{\"id\":\"claude\",\"label\":\"Claude\",\"plan\":\"max_20x\","
        "\"kind\":\"plan\",\"severity\":\"ok\",\"status\":\"ok\",\"msg\":\"\","
        "\"rows\":[{\"k\":\"5h\",\"label\":\"CLAUDE 5h\",\"pct\":50,"
        "\"txt\":\"05:09\",\"tier\":\"ok\",\"reset_at\":0},"
        "{\"k\":\"7d\",\"label\":\"CLAUDE 7d\",\"pct\":60,"
        "\"txt\":\"Mon\",\"tier\":\"ok\",\"reset_at\":0}]}]}";

    Model m;
    char err[256];
    bool ok = usage::parseSnapshot(json, strlen(json), m, err, sizeof(err));
    ASSERT_TRUE(ok);

    RenderPlan plan;
    buildTestPlan(m, 0, plan);

    // All-ok: no banner, 5-row layout exactly as today.
    ASSERT_EQ(0u, plan.bannerTier);
    ASSERT_STREQ("", plan.banner);
    ASSERT_EQ(2, (int)plan.lineCount);
    ASSERT_STREQ("CLAUDE 5h", plan.lines[0].left);
    ASSERT_EQ(0, (int)plan.lines[0].warn);
    ASSERT_STREQ("CLAUDE 7d", plan.lines[1].left);
    ASSERT_EQ(0, (int)plan.lines[1].warn);
}
