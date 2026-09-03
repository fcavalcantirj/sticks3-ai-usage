package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"usaged/internal/config"
	"usaged/internal/format"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
)

// Server hosts the usage HTTP API backed by a scheduler.
type Server struct {
	sched  *sched.Scheduler
	cfg    config.Config
	logger *slog.Logger
	start  time.Time
}

// New builds the HTTP API server around a scheduler and config. It returns an
// error if the listen address is not loopback and no DeviceToken is configured
// (never expose the LAN port without a token).
func New(s *sched.Scheduler, cfg config.Config, logger *slog.Logger) (*http.Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if cfg.DeviceToken == "" && !isLoopbackListen(cfg.Listen) {
		return nil, refuseStartError(cfg.Listen)
	}

	srv := &Server{
		sched:  s,
		cfg:    cfg,
		logger: logger,
		start:  time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /v1/usage", srv.handleUsage)
	mux.HandleFunc("GET /v1/usage.txt", srv.handleUsageTxt)
	mux.HandleFunc("POST /v1/refresh", srv.handleRefresh)
	mux.HandleFunc("/", srv.handleNotFound)

	handler := newAuth(cfg, logger).middleware(mux)

	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}, nil
}

// withNextSec returns a snapshot copy with next_sec set to the poll interval,
// as required by the v1 contract.
func (s *Server) withNextSec(snap snapshot.Snapshot) snapshot.Snapshot {
	snap.NextSec = int(s.cfg.Interval.Seconds())
	return snap
}

// handleHealthz returns service health without secrets.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	snap := s.sched.Current()
	resp := healthzResponse{
		OK:        true,
		Seq:       snap.Seq,
		Rev:       snap.Rev,
		CheckedAt: snap.CheckedAt,
		UptimeSec: int(time.Since(s.start).Seconds()),
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleUsage serves the current snapshot as compact JSON with an ETag.
// Matching If-None-Match (quoted, bare, or weak) yields 304 Not Modified with
// an empty body. Go's net/http omits the body and Content-Length on 304
// responses (RFC 9110); client robustness (no 304 body, no connection reuse)
// is the firmware's responsibility.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	snap := s.withNextSec(s.sched.Current())
	rev := snap.Rev
	etag := `"` + rev + `"`

	if etagMatch(r.Header.Get("If-None-Match"), rev) {
		writeNotModified(w, etag)
		return
	}

	body, err := json.Marshal(snap)
	if err != nil {
		s.logger.Error("marshal snapshot", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "internal"})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

// writeNotModified sends a standard 304 Not Modified with ETag and
// Cache-Control: no-store. Go's net/http omits the body and Content-Length;
// that is correct per RFC 9110.
func writeNotModified(w http.ResponseWriter, etag string) {
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotModified)
}

// handleUsageTxt serves the same table as `usaged once` as text/plain.
func (s *Server) handleUsageTxt(w http.ResponseWriter, _ *http.Request) {
	snap := s.withNextSec(s.sched.Current())
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	format.RenderTable(snap, w, s.cfg.TZ)
}

// handleNotFound returns a JSON 404 for unknown paths/methods.
func (s *Server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
}

// etagMatch reports whether the If-None-Match header matches the current rev.
// It accepts quoted ("<rev>"), bare (<rev>), and weak (W/<rev> or W/"<rev>")
// forms, and tolerates a comma-separated list of etags.
func etagMatch(inm, rev string) bool {
	for _, part := range strings.Split(inm, ",") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "W/") {
			p = p[2:]
		}
		if p == `"`+rev+`"` || p == rev {
			return true
		}
	}
	return false
}

type healthzResponse struct {
	OK        bool   `json:"ok"`
	Seq       int64  `json:"seq"`
	Rev       string `json:"rev"`
	CheckedAt int64  `json:"checked_at"`
	UptimeSec int    `json:"uptime_sec"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	data, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"ok":false,"error":"internal"}`)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	w.Write(data)
}
