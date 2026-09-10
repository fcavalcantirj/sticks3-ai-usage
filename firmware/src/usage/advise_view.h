// firmware/src/usage/advise_view.h — parsed /v1/advise response layout.
//
// Pure C++17, NO Arduino/M5 headers (ArduinoJson single header allowed — it is
// platform-independent).  All fields are bounded POD: no heap allocation, no
// std::string, suitable for an ESP32-S3 fetch/render path.
//
// The /v1/advise endpoint is Cache-Control: no-store, emits no ETag, and is
// never 304.  Its JSON shape:
//   {"winner":"codex"|null,"recommendations":[{id,label,score,pace_ratio,
//                                              effective_headroom_pct,reason}]}
// Only "plan" kind providers participate (~3: codex/claude/opencode:go).  The
// device fetches this on a BtnA hold and renders a full-screen overlay.
#pragma once

#include <cstddef>
#include <cstdint>

namespace usage {

// Screen-fit cap for the ranked recommendation list (ORDER #98 task 98).
// The 240x135 overlay reserves rows for: title, winner line, reason, hint —
// leaving room for 5 recommendation lines at textSize 1 (≈10 px each).
static const uint8_t kMaxRecs = 5;

// AdviseRec: one ranked provider recommendation within the /v1/advise response.
struct AdviseRec {
    char id[24];               // provider id (e.g. "codex")
    char label[24];            // provider label (e.g. "ChatGPT")
    int score;                 // ranking score (truncated from float; not rendered)
    float paceRatio;           // usage pace ratio (1.0 = break-even)
    int effectiveHeadroomPct;  // percent of the binding window remaining (0..100)
    char reason[64];           // one-line explanation
};

// AdvisePlan: the parsed /v1/advise response, laid out for the 240x135 screen.
// The winner's recommendation is reordered to the front of recs[] (winner-first)
// by buildAdviseLayout after parseAdvise populates the raw recommendation order.
struct AdvisePlan {
    bool fetchOk;              // false when fetch/parse failed (unreachable on a configured net)
    bool hasWinner;            // true when winner is not null
    char winnerId[24];         // winner's id ("" when no winner)
    char winnerLabel[24];      // winner's label ("" when no winner)
    char reason[96];           // winner's reason text ("" when no winner)
    uint8_t recCount;          // number of recommendations actually stored
    AdviseRec recs[kMaxRecs];  // capped to screen-fit max; winner-first after layout
};

// parseAdvise parses a /v1/advise JSON response into a bounded AdvisePlan.
// Returns true on success.  On failure returns false and writes a
// human-readable reason into err (up to errLen bytes, NUL-terminated).
// Uses ArduinoJson (heap-allocated JsonDocument on the host; acceptable at
// fetch time on the device — NOT in the render loop).
bool parseAdvise(const char* json, size_t len, AdvisePlan& out,
                 char* err, size_t errLen);

// buildAdviseLayout reorders recs[] so the winner's recommendation is first
// (winner-first ordering) and re-truncates text fields into their fixed
// buffers as a defensive guarantee.  Called after parseAdvise to finalize
// the layout for rendering.
void buildAdviseLayout(AdvisePlan& plan);

// kAdviseRowMaxChars is the character budget for a recommendation row at
// textSize(2) on the 240x135 panel: (240 - 5) / 12 ≈ 19 chars.  Passed from
// screen.cpp to fitAdviseRow so the HAL stays dumb and the pure core owns
// the fitting contract.
static const size_t kAdviseRowMaxChars = 19;

// fitAdviseRow builds a single recommendation row string fitting within
// maxChars.  Format: "label  pct%  pace×"
//
// At textSize(2) each character is ~12 px; a row like
// "OpenCode Go  1%  1.8x" is ~228 px of a 240 px panel — it fits barely,
// and a longer provider label would run off the edge silently.  This
// function truncates the LABEL (with ".." suffix) when the row would
// overflow, but the numbers (headroom% and pace×) are never cut — they
// are the point.
//
// Returns the fitted string length (excluding NUL), or 0 if the numbers
// alone exceed maxChars (row cannot be shown meaningfully).
size_t fitAdviseRow(const char* label, int headroomPct, float paceRatio,
                    char* out, size_t outSize, size_t maxChars);

// fitWinnerLine builds "use: <label>" truncated to maxChars with ".."
// suffix when needed.  Winner labels are typically short, but a 23-char
// buffer could overflow at textSize(2).  Returns the string length.
size_t fitWinnerLine(const char* label, char* out, size_t outSize, size_t maxChars);

} // namespace usage
