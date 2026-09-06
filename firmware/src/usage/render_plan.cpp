// firmware/src/usage/render_plan.cpp — implementation of buildPlan, onSnapshot,
// nextPage, tierName, tierColor565, footerCompute.
#include "usage/render_plan.h"
#include "usage/textfit.h"
#include "usage/freshness.h"

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

// --- kind helpers ----------------------------------------------------------

// kindFromStr converts a Go snapshot "kind" string to the Kind enum.
// Unknown/empty → KIND_NONE (0).
static uint8_t kindFromStr(const char* s) {
    if (s == nullptr || s[0] == '\0') return 0;
    if (std::strcmp(s, "plan") == 0) return KIND_PLAN;
    if (std::strcmp(s, "credit") == 0) return KIND_CREDIT;
    if (std::strcmp(s, "free") == 0) return KIND_FREE;
    return 0;
}

// kindTitle returns the display title for a kind.
static const char* kindTitle(uint8_t kind) {
    switch (kind) {
        case KIND_PLAN:   return "PLANS";
        case KIND_CREDIT: return "CREDITS";
        case KIND_FREE:   return "FREE";
        default:          return "AI USAGE";
    }
}

// kindColor565 returns the RGB565 accent color for a kind.
static uint16_t kindColor565(uint8_t kind) {
    switch (kind) {
        case KIND_PLAN:   return 0x3B9F; // blue
        case KIND_CREDIT: return 0x07E0; // green
        case KIND_FREE:   return 0x8410; // grey
        default:          return 0x3B9F; // blue fallback
    }
}

// --- page management ------------------------------------------------------

// providerIsDim returns true when the status warrants dimming (stale/auth/error).
static bool providerIsDim(uint8_t status) {
    return status == 1 || status == 2 || status == 3; // stale, auth, error
}

// --- banner severity scanning (ORDER #36 / task 50) -------------------------

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

// --- kind page iteration --------------------------------------------------

// KindOrder is the fixed display order for kinds.
static const uint8_t kKindOrder[] = {KIND_PLAN, KIND_CREDIT, KIND_FREE};

// kindRowCount sums all rows belonging to providers of the given kind.
static uint8_t kindRowCount(const Model& m, uint8_t kind) {
    uint8_t count = 0;
    for (uint8_t i = 0; i < m.providerCount; i++) {
        if (kindFromStr(m.providers[i].kind) != kind) continue;
        count += m.providers[i].rowCount;
    }
    return count;
}

// countPages returns the total number of kind-grouped pages.
uint8_t countPages(const Model& model) {
    // ORDER #48 defect (e): no row stealing — maxLines is always 5.
    uint8_t maxLines = 5;
    uint8_t total = 0;
    for (int ki = 0; ki < 3; ki++) {
        uint8_t rows = kindRowCount(model, kKindOrder[ki]);
        if (rows > 0) {
            total += (rows + maxLines - 1) / maxLines;
        }
    }
    return total;
}

// resolvePage maps a flat 0-indexed page to (kind, subPage, startRow) and
// fills the RenderPlan.  Returns true if the page is valid.
static void resolvePage(const Model& model, uint8_t flatPage,
                        uint8_t& outKind, uint8_t& outSubPage,
                        uint8_t& outStartRow) {
    // ORDER #48 defect (e): no row stealing — maxLines is always 5.
    uint8_t maxLines = 5;

    uint8_t remaining = flatPage;
    for (int ki = 0; ki < 3; ki++) {
        uint8_t kind = kKindOrder[ki];
        uint8_t rows = kindRowCount(model, kind);
        if (rows == 0) continue;

        uint8_t pages = (rows + maxLines - 1) / maxLines;
        if (remaining < pages) {
            outKind = kind;
            outSubPage = remaining;
            outStartRow = remaining * maxLines;
            return;
        }
        remaining -= pages;
    }

    // Page out of range — default to plan.
    outKind = KIND_PLAN;
    outSubPage = 0;
    outStartRow = 0;
}

void buildPlan(const Model& model, uint8_t page, const char* buildId,
               RenderPlan& out) {
    std::memset(&out, 0, sizeof(out));

    // Version string: "v<buildId>" truncated to a 7-char git sha, with
    // a "+dirty" suffix collapsed to a trailing "*".  e.g. "v3ca0869+d1r"
    // → "v3ca086*".  Fits in buildId[13]: 'v' + 7 + '*' + NUL.  (ORDER #48 defect a)
    char verBuf[13];
    if (buildId != nullptr && buildId[0] != '\0') {
        char shortSha[8];
        std::snprintf(shortSha, sizeof(shortSha), "%.7s", buildId);
        // Detect dirty suffix: anything after '+'.
        if (std::strchr(buildId, '+') != nullptr) {
            std::snprintf(verBuf, sizeof(verBuf), "v%s*", shortSha);
        } else {
            std::snprintf(verBuf, sizeof(verBuf), "v%s", shortSha);
        }
    } else {
        copyStr(verBuf, "v?", sizeof(verBuf));
    }
    copyStr(out.buildId, verBuf, sizeof(out.buildId));

    uint8_t totalPages = countPages(model);
    out.pageCount = totalPages;
    out.page = (uint8_t)(page + 1); // 1-indexed for display

    // ORDER #65: freshness tier from the server-reported age at fetch time.
    // main.cpp overrides this with the accumulated age after buildPlan returns.
    out.freshnessTier = usage::freshnessTier(model.age, model.nextSec);

    if (totalPages == 0) return;

    // Resolve which kind this flat page belongs to.
    uint8_t kind = 0, subPage = 0, startRow = 0;
    resolvePage(model, page, kind, subPage, startRow);
    out.kind = kind;
    out.kindColor = kindColor565(kind);
    copyStr(out.title, kindTitle(kind), sizeof(out.title));

    // asOf shows "seq N" (the device has no real-time clock).
    char asOfBuf[12];
    std::snprintf(asOfBuf, sizeof(asOfBuf), "seq %u", model.seq);
    copyStr(out.asOf, asOfBuf, sizeof(out.asOf));

    // --- alert banner (ORDER #36 / task 50, ORDER #48 defect d) ---
    // The banner text says WHAT is wrong, not just whose.  When the provider
    // has a msg (e.g. "429 until 21:40") use "label msg".  When msg is empty,
    // find the worst row of the worst provider and append its label + pct.
    // e.g. "ChatGPT GPT 5h 100%".  (ORDER #48 defect d)
    uint8_t worst = worstSeverity(model);
    out.bannerTier = (worst >= 2) ? 2 : 0;
    if (worst >= 2) {
        const Provider* cp = firstProviderWithSeverity(model, 2);
        if (cp) {
            char combined[25];
            if (cp->msg[0] != '\0') {
                std::snprintf(combined, sizeof(combined), "%s %s",
                              cp->label, cp->msg);
            } else {
                // No msg — describe the worst row.
                int worstTier = 0;
                int worstRowIdx = -1;
                for (uint8_t r = 0; r < cp->rowCount; r++) {
                    uint8_t rt = cp->rows[r].tier;
                    if (rt > worstTier) {
                        worstTier = rt;
                        worstRowIdx = r;
                    }
                }
                if (worstRowIdx >= 0 && cp->rows[worstRowIdx].pct >= 0) {
                    std::snprintf(combined, sizeof(combined), "%s %s %d%%",
                                  cp->label, cp->rows[worstRowIdx].label,
                                  (int)cp->rows[worstRowIdx].pct);
                } else if (worstRowIdx >= 0) {
                    std::snprintf(combined, sizeof(combined), "%s %s",
                                  cp->label, cp->rows[worstRowIdx].label);
                } else {
                    copyStr(combined, cp->label, sizeof(combined));
                }
            }
            copyStr(out.banner, combined, sizeof(out.banner));
        }
    }

    // ORDER #48 defect (e): the banner no longer steals a card row.
    // maxLines is always 5; alerts live in the footer (drawPlan),
    // not in the card area.
    uint8_t maxLines = 5;

    uint8_t lineIdx = 0;
    uint8_t currentRow = 0;  // row counter within the current kind

    for (uint8_t i = 0; i < model.providerCount && lineIdx < maxLines; i++) {
        const Provider& prov = model.providers[i];
        if (kindFromStr(prov.kind) != kind) continue;

        for (uint8_t r = 0; r < prov.rowCount; r++) {
            if (currentRow < startRow) {
                currentRow++;
                continue;
            }
            if (lineIdx >= maxLines) break;

            const Row& row = prov.rows[r];
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
            currentRow++;
        }

        // BUG 52b: provider messages (msg field) belong to the severity
        // banner, NOT the footer.  The footer shows the device's own state
        // ("no hub" / button hint) or the alert text.  Provider failures are
        // communicated through the row tint + banner only.
    }

    out.lineCount = lineIdx;
    // ORDER #51 + ORDER #57 (task 60 correction): the footer carries ONLY the
    // version string (e.g. "v3c639cd") when there is no crit banner — seq was
    // dropped entirely (it is developer noise, already on the serial line and
    // in the snapshot).  screen.cpp left-aligns the footer text and
    // right-aligns the hint.  When crit, the footer carries the banner text.
    if (out.bannerTier >= 2 && out.banner[0] != '\0') {
        copyStr(out.footer, out.banner, sizeof(out.footer));
    } else {
        copyStr(out.footer, out.buildId, sizeof(out.footer));
    }
}

// --- footer layout (ORDER #56 task 60) ---------------------------------------

// footerCompute implements the measure-then-fit discipline for the footer:
// a left-aligned "seq N · v<sha>" and a right-aligned hint.  On collision the
// hint is shortened first (via fitRight), then the seq prefix is dropped
// (keeping only the version), but the version is NEVER shortened.
void footerCompute(FooterLayout& out, int16_t W,
                   const char* asOf, const char* buildId,
                   const char* hint,
                   int (*measure)(const char*)) {
    std::memset(&out, 0, sizeof(out));
    out.leftX = 5;

    const char* seq = (asOf != nullptr) ? asOf : "";
    const char* ver = (buildId != nullptr) ? buildId : "";

    // Full left string: "seq N · v<sha>".
    char fullLeft[48];
    if (seq[0] != '\0') {
        std::snprintf(fullLeft, sizeof(fullLeft), "%s %s %s", seq, "\xc2\xb7", ver);
    } else {
        copyStr(fullLeft, ver, sizeof(fullLeft));
    }

    int gap = 4;        // gap between left and hint
    int rightMarg = 4;  // right margin
    int avail = W - 5 - rightMarg;  // total space for left + gap + hint

    // Step 1: try full left string + shortened hint.
    char hintFitted[64];
    int maxHintW = avail - measure(fullLeft) - gap;
    if (maxHintW > 0) {
        fitRight(hint, hintFitted, sizeof(hintFitted), maxHintW, measure);
    } else {
        hintFitted[0] = '\0';
    }
    int hintW = measure(hintFitted);

    // Step 2: if still doesn't fit, drop seq — keep only the version (never
    // shortened).
    if (measure(fullLeft) + gap + hintW > avail) {
        int verW = measure(ver);
        maxHintW = avail - verW - gap;
        if (maxHintW > 0) {
            fitRight(hint, hintFitted, sizeof(hintFitted), maxHintW, measure);
        } else {
            hintFitted[0] = '\0';
        }
        hintW = measure(hintFitted);

        copyStr(out.left, ver, sizeof(out.left));
    } else {
        copyStr(out.left, fullLeft, sizeof(out.left));
    }

    out.hintX = W - hintW - rightMarg;
    out.hintDrawn = (hintW > 0);
    copyStr(out.hint, hintFitted, sizeof(out.hint));
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
    uint8_t total = countPages(model);
    if (total > 0) {
        view.page = (view.page + 1) % total;
    }
    view.needsRedraw = true;
}

} // namespace usage
