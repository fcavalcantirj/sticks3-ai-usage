// Reference snapshot receiver for usaged (task 59).
// PUT /v1/usage receives the snapshot JSON; GET /v1/usage serves it with
// ETag/304. Both require Authorization: Bearer <USAGED_PUBLISH_TOKEN>.
// Deploy on any always-on box the founder chooses (NOT the Onvida fleet).
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func main() {
	addr := flag.String("addr", ":8766", "listen address")
	token := flag.String("token", os.Getenv("USAGED_PUBLISH_TOKEN"), "bearer token")
	flag.Parse()
	if *token == "" {
		slog.Error("no token (use --token or USAGED_PUBLISH_TOKEN)")
		os.Exit(2)
	}
	s := &store{}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/usage", s.handlePut(*token))
	mux.HandleFunc("GET /v1/usage", s.handleGet(*token))
	srv := &http.Server{Addr: *addr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	slog.Info("receiver listening", "addr", *addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}

type store struct {
	mu   sync.Mutex
	body []byte
	rev  string
}

func bearer(r *http.Request, token string) bool {
	const p = "Bearer "
	got := r.Header.Get("Authorization")
	return strings.HasPrefix(got, p) &&
		subtle.ConstantTimeCompare([]byte(got[len(p):]), []byte(token)) == 1
}

func (s *store) handlePut(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !bearer(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var head struct {
			Rev string `json:"rev"`
		}
		if json.Unmarshal(body, &head) != nil {
			http.Error(w, "invalid snapshot", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.body = body
		s.rev = head.Rev
		s.mu.Unlock()
		w.Header().Set("ETag", head.Rev)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *store) handleGet(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !bearer(r, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.mu.Lock()
		body, rev := s.body, s.rev
		s.mu.Unlock()
		if body == nil {
			http.Error(w, "no snapshot", http.StatusNotFound)
			return
		}
		etag := `"` + rev + `"`
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("ETag", etag)
		if etagMatch(r.Header.Get("If-None-Match"), rev) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Write(body)
	}
}

// etagMatch accepts quoted, bare, and weak forms, comma-separated.
func etagMatch(inm, rev string) bool {
	for _, p := range strings.Split(inm, ",") {
		p = strings.TrimSpace(p)
		p = strings.Trim(strings.TrimPrefix(p, `W/`), `"`)
		if p == rev {
			return true
		}
	}
	return false
}
