package api

import (
	"net/http/httptest"
	"testing"
)

func TestDashboardResponseHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).handleIndex(w, httptest.NewRequest("GET", "/", nil))
	for key, want := range map[string]string{
		"Content-Type":           "text/html; charset=utf-8",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := w.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
