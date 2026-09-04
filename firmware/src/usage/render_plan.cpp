// firmware/src/usage/render_plan.cpp — implementation of buildPlan, onSnapshot,
// nextPage, tierName, tierColor565.
#include "usage/render_plan.h"

#include <cstdio>
#include <cstring>

namespace usage {

// --- bounded string copy (local; same pattern as model.cpp) ---------------

static void copyStr(char* dst, const char* src, size_t n) {
    if (n == 0) return;
    if (src == nullptr) { dst[0] = '\0'; return; }
    size_t i;
    for (i = 0; i < n - 1 && src[i] != '\0'; i++)
        dst[i] = src[i];
    dst[i] = '\0';
}

// --- tier helpers ---------------------------------------------------------

const char* tierName(uint8_t tier) {
    switch (tier) {
        case 1: return "warn";
        case 2: return "crit";
        case 3: return "off";
        default: return "ok";
    }
}

uint16_t tierColor565(uint8_t tier, bool dim) {
    uint16_t c;
    switch (tier) {
        case 1: c = 0xFD20; break; // warn: amber
        case 2: c = 0xF800; break; // crit: red
        case 3: c = 0x8410; break; // off: grey
        default: c = 0x3B9F; break; // ok: blue
    }
    if (dim) {
        // Halve each RGB565 channel.
        uint8_t r = (c >> 11) & 0x1F;
        uint8_t g = (c >> 5) & 0x3F;
        uint8_t b = c & 0x1F;
        c = (uint16_t)((r / 2 << 11) | (g / 2 << 5) | (b / 2));
    }
    return c;
}

// --- page management ------------------------------------------------------

// hasPage2Providers returns true if the snapshot has any provider beyond
// codex (i.e. openrouter or groq), which means content spills onto page 2.
static bool hasPage2Providers(const Model& m) {
    for (uint8_t i = 0; i < m.providerCount; i++) {
        const char* id = m.providers[i].id;
        if (std::strcmp(id, "openrouter:main") == 0 ||
            std::strcmp(id, "openrouter:fallback") == 0 ||
            std::strcmp(id, "groq") == 0) {
            return true;
        }
    }
    return false;
}

// providerIsPage1 returns true for the page-1 providers (claude, codex).
static bool providerIsPage1(const char* id) {
    return std::strcmp(id, "claude") == 0 || std::strcmp(id, "codex") == 0;
}

// providerIsDim returns true when the status warrants dimming (stale/auth/error).
static bool providerIsDim(uint8_t status) {
    return status == 1 || status == 2 || status == 3; // stale, auth, error
}

// --- banner severity scanning (ORDER #36 / task 50) --------------------------

// Returns the worst severity across ALL providers (0 ok, 1 warn, 2 crit, 3 off).
// The banner decision is global: every page shows the same banner.
static uint8_t worstSeverity(const Model& m) {
    uint8_t worst = 0;
    for (uint8_t i = 0; i < m.providerCount; i++) {
        if (m.providers[i].severity > worst)
            worst = m.providers[i].severity;
    }
    return worst;
}

// Returns the first provider with severity == sev, or nullptr.
static const Provider* firstProviderWithSeverity(const Model& m, uint8_t sev) {
    for (uint8_t i = 0; i < m.providerCount; i++) {
        if (m.providers[i].severity == sev)
            return &m.providers[i];
    }
    return nullptr;
}

// Appends '!' to a row label, truncating to fit within n bytes (incl. NUL).
// e.g. "ORmain bal" (10 chars) → "ORmain ba!" (10 chars) in a 11-byte buffer.
static void appendWarnBang(char* label, size_t n) {
    if (n == 0) return;
    size_t len = std::strlen(label);
    if (len + 1 >= n) { // no room for '!' + NUL
        len = n - 2;    // leave 2 bytes for '!' + NUL
        label[len] = '!';
        label[len + 1] = '\0';
    } else {
        label[len] = '!';
        label[len + 1] = '\0';
    }
}

void buildPlan(const Model& model, uint8_t page, RenderPlan& out) {
    std::memset(&out, 0, sizeof(out));
    copyStr(out.title, "AI USAGE", sizeof(out.title));
    out.page = (uint8_t)(page + 1); // 1-indexed for display
    out.pageCount = (uint8_t)(hasPage2Providers(model) ? 2 : 1);

    // asOf shows "seq N" (the device has no real-time clock).
    char asOfBuf[12];
    std::snprintf(asOfBuf, sizeof(asOfBuf), "seq %u", model.seq);
    copyStr(out.asOf, asOfBuf, sizeof(out.asOf));

    // --- alert banner (ORDER #36 / task 50) ---
    // When ANY provider is crit, show a red banner on EVERY page (including
    // page 2) and shrink the row area to 4 lines.  When the worst is warn,
    // append '!' to the warn provider's rows and tint them amber — no banner.
    uint8_t worst = worstSeverity(model);
    if (worst >= 2) {
        out.bannerTier = 2;
        const Provider* cp = firstProviderWithSeverity(model, 2);
        if (cp) {
            char combined[25];
            if (cp->msg[0] != '\0') {
                std::snprintf(combined, sizeof(combined), "%s %s",
                              cp->label, cp->msg);
            } else {
                copyStr(combined, cp->label, sizeof(combined));
            }
            copyStr(out.banner, combined, sizeof(out.banner));
        }
    }

    uint8_t maxLines = (worst >= 2) ? 4 : 5;

    uint8_t lineIdx = 0;
    const char* footerMsg = nullptr;

    for (uint8_t i = 0; i < model.providerCount; i++) {
        const Provider& prov = model.providers[i];
        bool onThisPage = (page == 0) ? providerIsPage1(prov.id)
                                      : !providerIsPage1(prov.id);
        if (!onThisPage) continue;

        // Track the first non-ok provider's msg for the footer.
        if (prov.status != 0 && footerMsg == nullptr) {
            footerMsg = prov.msg;
        }

        for (uint8_t r = 0; r < prov.rowCount && lineIdx < maxLines; r++) {
            const Row& row = prov.rows[r];
            // bal rows never go on page 1.
            if (page == 0 && std::strcmp(row.k, "bal") == 0) {
                continue;
            }
            Line& line = out.lines[lineIdx];
            copyStr(line.left, row.label, sizeof(line.left));
            line.pct = row.pct;
            copyStr(line.right, row.txt, sizeof(line.right));
            line.tier = row.tier;
            line.dim = (uint8_t)(providerIsDim(prov.status) ? 1 : 0);

            // Warn: append '!' and flag for amber tint — only when warn is
            // the global worst (no crit provider present).
            if (worst == 1 && prov.severity == 1) {
                appendWarnBang(line.left, sizeof(line.left));
                line.warn = 1;
            }

            lineIdx++;
        }
    }

    out.lineCount = lineIdx;
    copyStr(out.footer, footerMsg, sizeof(out.footer));
}

void onSnapshot(View& view, const Model& model) {
    if (std::strcmp(view.lastRev, model.rev) != 0) {
        view.needsRedraw = true;
        copyStr(view.lastRev, model.rev, sizeof(view.lastRev));
    } else {
        view.needsRedraw = false;
    }
}

void nextPage(View& view, const Model& model) {
    uint8_t total = hasPage2Providers(model) ? 2 : 1;
    view.page = (view.page + 1) % total;
    view.needsRedraw = true;
}

} // namespace usage
