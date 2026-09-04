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
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing element %q", want)
		}
	}
}
