// firmware/src/usage/advise_view.cpp — implementation of parseAdvise +
// buildAdviseLayout.
//
// Pure C++17, no Arduino/M5 headers.  Uses the vendored ArduinoJson single
// header (firmware/third_party/ArduinoJson.h).
#include "usage/advise_view.h"

// Disable Arduino-specific extensions before pulling in the vendored header
// (same pattern as test_smoke.cpp and model.cpp :9-13).
#define ARDUINOJSON_ENABLE_ARDUINO_STRING 0
#define ARDUINOJSON_ENABLE_ARDUINO_STREAM 0
#define ARDUINOJSON_ENABLE_STD_STREAM 0
#include <ArduinoJson.h>
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

// --- parse --------------------------------------------------------------

bool parseAdvise(const char* json, size_t len, AdvisePlan& out,
                 char* err, size_t errLen) {
    if (err == nullptr || errLen == 0)
        return false;
    err[0] = '\0';

    // ArduinoJson 7.x: JsonDocument auto-sizes on the heap (host).
    JsonDocument doc;
    DeserializationError derr = deserializeJson(doc, json, len);
    if (derr) {
        std::snprintf(err, errLen, "deserialize: %s", derr.c_str());
        return false;
    }

    // Zero-init the output so untouched fields are clean.
    std::memset(&out, 0, sizeof(out));
    out.fetchOk = true;  // parse succeeded; fetchOk reflects fetch outcome, not parse

    // winner may be null — that means no clear winner, not an error.
    JsonVariant winnerVar = doc["winner"];
    const char* winner = nullptr;
    if (!winnerVar.isNull()) {
        winner = winnerVar.as<const char*>();
    }
    if (winner != nullptr && winner[0] != '\0') {
        out.hasWinner = true;
        copyStr(out.winnerId, winner, sizeof(out.winnerId));
    }

    // recommendations array.
    JsonArray recs = doc["recommendations"].as<JsonArray>();
    size_t count = recs.size();
    if (count > kMaxRecs) {
        count = kMaxRecs;  // clamp: silently drop extras beyond screen-fit cap
    }
    out.recCount = (uint8_t)count;

    int ri = 0;
    for (JsonObject r : recs) {
        if (ri >= (int)count) break;  // respect clamp
        AdviseRec& rec = out.recs[ri];
        copyStr(rec.id, r["id"].as<const char*>(), sizeof(rec.id));
        copyStr(rec.label, r["label"].as<const char*>(), sizeof(rec.label));
        rec.score = r["score"].as<int>();
        rec.paceRatio = r["pace_ratio"].as<float>();
        rec.effectiveHeadroomPct = r["effective_headroom_pct"].as<int>();
        copyStr(rec.reason, r["reason"].as<const char*>(), sizeof(rec.reason));
        ri++;
    }

    // If there is a winner, lift its label and reason into the top-level fields
    // (matched by id against the parsed recs).  When the winner is not found in
    // the recs array, the top-level fields stay empty — the overlay still renders
    // the ranked list, just without a highlighted winner.
    if (out.hasWinner && out.winnerId[0] != '\0') {
        for (int i = 0; i < (int)out.recCount; i++) {
            if (std::strcmp(out.recs[i].id, out.winnerId) == 0) {
                copyStr(out.winnerLabel, out.recs[i].label, sizeof(out.winnerLabel));
                copyStr(out.reason, out.recs[i].reason, sizeof(out.reason));
                break;
            }
        }
    }

    return true;
}

// --- layout --------------------------------------------------------------

void buildAdviseLayout(AdvisePlan& plan) {
    // Reorder: move the winner's recommendation to the front of recs[].
    // The JSON lists recommendations in score-desc order; the winner is
    // already top-ranked, but this guarantees winner-first regardless of
    // any future server sort change.
    if (!plan.hasWinner || plan.winnerId[0] == '\0' || plan.recCount == 0)
        return;

    int widx = -1;
    for (int i = 0; i < (int)plan.recCount; i++) {
        if (std::strcmp(plan.recs[i].id, plan.winnerId) == 0) {
            widx = i;
            break;
        }
    }
    if (widx <= 0) return;  // already first, or winner not in recs

    // Rotate recs[0..widx] right by one so the winner lands at index 0.
    AdviseRec tmp = plan.recs[widx];
    for (int i = widx; i > 0; i--) {
        plan.recs[i] = plan.recs[i - 1];
    }
    plan.recs[0] = tmp;
}

// --- row fitting ---------------------------------------------------------

// truncateLabel copies at most budget characters of label into out, appending
// ".." when the label was longer than budget (and budget >= 3).  Always
// NUL-terminates within outSize.  Returns the number of chars placed
// (excluding NUL).  When budget < 3, only ".." or empty is written.
static size_t truncateLabel(const char* label, char* out, size_t outSize, size_t budget) {
    if (outSize == 0) return 0;
    if (label == nullptr) label = "";
    size_t labelLen = std::strlen(label);

    if (labelLen <= budget) {
        size_t i;
        for (i = 0; i < outSize - 1 && i < labelLen; i++)
            out[i] = label[i];
        out[i] = '\0';
        return i;
    }

    // Need room for at least ".." (2 chars + NUL).
    if (budget < 3 || outSize < 3) {
        if (outSize >= 3) {
            out[0] = '.';
            out[1] = '.';
            out[2] = '\0';
            return 2;
        }
        out[0] = '\0';
        return 0;
    }

    // Copy (budget - 2) chars of label + ".." + NUL = budget + 1 bytes total.
    size_t copyLen = budget - 2;
    if (copyLen > outSize - 3) copyLen = outSize - 3;
    size_t i;
    for (i = 0; i < copyLen && label[i] != '\0'; i++)
        out[i] = label[i];
    out[i++] = '.';
    out[i++] = '.';
    out[i] = '\0';
    return i;
}

size_t fitAdviseRow(const char* label, int headroomPct, float paceRatio,
                    char* out, size_t outSize, size_t maxChars) {
    if (outSize == 0 || maxChars == 0) {
        if (outSize > 0) out[0] = '\0';
        return 0;
    }

    // Build the fixed suffix: "  <headroom>%  <pace>.0x"
    // The numbers are never the part that gets cut.
    char suffix[32];
    int slen = std::snprintf(suffix, sizeof(suffix), "  %d%%  %.1fx",
                              headroomPct, (double)paceRatio);
    if (slen < 0 || (size_t)slen >= sizeof(suffix)) {
        out[0] = '\0';
        return 0;
    }
    size_t suffixLen = (size_t)slen;

    // If the numbers alone exceed the budget, the row cannot be shown.
    if (suffixLen >= maxChars) {
        out[0] = '\0';
        return 0;
    }

    size_t labelBudget = maxChars - suffixLen;
    size_t labelLen = (label != nullptr) ? std::strlen(label) : 0;

    if (labelLen <= labelBudget) {
        // Full label fits — build the complete row.
        size_t total = (size_t)std::snprintf(out, outSize, "%s%s",
            (label != nullptr) ? label : "", suffix);
        return (total < outSize) ? total : 0;
    }

    // Label overflows — truncate it, keeping the ".." inside the label budget.
    // The suffix (numbers) is always intact.
    if (labelBudget < 2) {
        // Not enough room even for ".." in the label slot — just show "..suffix".
        size_t total = (size_t)std::snprintf(out, outSize, "..%s", suffix);
        return (total < outSize) ? total : 0;
    }

    // Truncate label to labelBudget with ".." suffix into out, then append suffix.
    char* p = out;
    size_t i;
    // Copy as many label chars as fit (leaving room for ".." and NUL).
    size_t maxCopy = (labelBudget >= 3) ? labelBudget - 2 : 0;
    for (i = 0; i < maxCopy && label[i] != '\0'; i++)
        p[i] = label[i];
    if (maxCopy == 0 && labelBudget >= 2) {
        // Budget is 2 — just the dots, no label chars.
        p[0] = '.';
        p[1] = '.';
        i = 2;
    } else {
        p[i++] = '.';
        p[i++] = '.';
    }
    // Append suffix.
    for (size_t j = 0; j < suffixLen && i < outSize - 1; j++)
        p[i++] = suffix[j];
    p[i] = '\0';
    return i;
}

size_t fitWinnerLine(const char* label, char* out, size_t outSize, size_t maxChars) {
    if (outSize == 0 || maxChars == 0) {
        if (outSize > 0) out[0] = '\0';
        return 0;
    }

    const char* prefix = "use: ";
    size_t prefixLen = 5;  // "use: "
    size_t labelLen = (label != nullptr) ? std::strlen(label) : 0;

    if (prefixLen + labelLen <= maxChars) {
        // Full line fits.
        size_t total = (size_t)std::snprintf(out, outSize, "%s%s",
            prefix, (label != nullptr) ? label : "");
        return (total < outSize) ? total : 0;
    }

    // Truncate label to fit, with ".." suffix.
    if (maxChars <= prefixLen) {
        // Can't even fit the prefix — truncate prefix too.
        size_t total = (size_t)std::snprintf(out, outSize, "..");
        return (total < outSize) ? total : 0;
    }

    size_t labelBudget = maxChars - prefixLen;
    // Build into a temp buffer, then assemble via snprintf (overflow-safe).
    char truncated[32];
    truncateLabel(label, truncated, sizeof(truncated), labelBudget);
    size_t total = (size_t)std::snprintf(out, outSize, "%s%s", prefix, truncated);
    return (total < outSize) ? total : 0;
}

} // namespace usage
