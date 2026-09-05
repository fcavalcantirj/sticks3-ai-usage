package web

import (
	"strings"
	"testing"
)

// TestIndexHTMLContent verifies the embedded dashboard HTML contains the
// strings the spec requires for downstream consumers (page title, API
// endpoints, auth header name) and is non-empty.
func TestIndexHTMLContent(t *testing.T) {
	if len(IndexHTML) == 0 {
		t.Fatal("embedded index.html is empty")
	}
	html := string(IndexHTML)
	for _, want := range []string{
		"<title>AI Usage</title>",
		"/v1/usage",
		"/v1/stats",
		"If-None-Match",
		"X-Device-Token",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// TestIndexHTMLStatsIds verifies the page contains the element IDs required
// by the stats dashboard polish spec (task 45).
func TestIndexHTMLStatsIds(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		"id=\"stat-pills\"",
		"id=\"heatmap-claude_code\"",
		"id=\"heatmap-codex\"",
		"id=\"models-table\"",
		"id=\"attention\"",
		"id=\"pill-month-cost\"",
		"id=\"pill-month-ratio\"",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing element %q", want)
		}
	}
}

// TestIndexHTMLTabs verifies the tab strip (task 53 ORDER #48): four tabs with
// proper role/aria attributes so keyboard navigation works and no page scroll
// occurs at 1280x800 or 390x844.
func TestIndexHTMLTabs(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`role="tablist"`,
		`role="tab"`,
		`aria-selected="true"`,
		`id="tab-quota"`,
		`id="tab-activity"`,
		`id="tab-models"`,
		`id="tab-attention"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing tab element %q", want)
		}
	}
}

// TestIndexHTMLRefreshInHeader verifies the refresh button is in the header,
// not in the footer (ORDER #48).
func TestIndexHTMLRefreshInHeader(t *testing.T) {
	html := string(IndexHTML)
	refreshIdx := strings.Index(html, `id="refresh-btn"`)
	if refreshIdx < 0 {
		t.Fatal(`index.html missing id="refresh-btn"`)
	}
	footerIdx := strings.Index(html, `class="footer-bar"`)
	if footerIdx < 0 {
		t.Fatal(`index.html missing class="footer-bar"`)
	}
	if refreshIdx > footerIdx {
		t.Error(`refresh button appears AFTER footer-bar — it must be in the header`)
	}
}

// TestIndexHTMLIntervalSelect verifies the interval selector options match
// ORDER #48: 5/10/15/30/60 minutes (300/600/900/1800/3600 seconds).
func TestIndexHTMLIntervalSelect(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, `id="interval-select"`) {
		t.Fatal(`index.html missing id="interval-select"`)
	}
	for _, want := range []string{"300", "600", "900", "1800", "3600"} {
		if !strings.Contains(html, `value="`+want+`"`) {
			t.Errorf(`index.html missing interval option value %q`, want)
		}
	}
}

// TestIndexHTMLStatsFirstPaintFix verifies the 304-first-paint fix (ORDER #48):
// the page must not send If-None-Match on the first stats load.
func TestIndexHTMLStatsFirstPaintFix(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, "statsRendered") {
		t.Error("index.html missing statsRendered flag for first-paint fix")
	}
	// The fix: If-None-Match is only sent AFTER the first paint succeeds.
	if !strings.Contains(html, "First-paint fix") {
		t.Error("index.html missing First-paint fix comment")
	}
}
