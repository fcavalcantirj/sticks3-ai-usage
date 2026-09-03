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
		"If-None-Match",
		"X-Device-Token",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}
