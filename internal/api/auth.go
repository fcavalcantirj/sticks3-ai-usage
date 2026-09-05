package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"usaged/internal/config"
)

// Auth is the device-token authentication middleware.
//
// Public paths (/, /healthz) are never challenged.
//
// GET /v1/* routes: loopback clients bypass the token check (the dashboard
// works unauthenticated on localhost). Non-loopback clients must present a
// device token.
//
// Mutating /v1/* routes (POST/PUT/DELETE): the device token is required
// REGARDLESS of whether the request is from loopback — ORDER #54 task 59.
// The token is checked before the handler reads or parses the body, so an
// unauthenticated caller learns nothing about the payload shape. Requests
// with a body must have Content-Type: application/json.
type Auth struct {
	cfg    config.Config
	logger *slog.Logger
}

// newAuth creates an Auth middleware from config.
func newAuth(cfg config.Config, logger *slog.Logger) *Auth {
	if logger == nil {
		logger = slog.Default()
	}
	return &Auth{cfg: cfg, logger: logger}
}

// middleware wraps next with device-token enforcement. Public paths are never
// challenged. For GET /v1/*, loopback senders bypass the token check. For
// mutating /v1/* (POST/PUT/DELETE), the token is always required — even from
// loopback — and the Content-Type must be application/json (for routes with a
// body). The token is validated before the body is parsed.
func (a *Auth) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresToken(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// Mutating methods require the token even on loopback (ORDER #54).
		isMutating := r.Method == http.MethodPost ||
			r.Method == http.MethodPut ||
			r.Method == http.MethodDelete

		// GET routes keep the loopback exemption.
		if !isMutating && isLoopbackAddr(r.RemoteAddr) {
			next.ServeHTTP(w, r)
			return
		}

		// Require a valid token for non-loopback GETs and ALL mutating requests.
		token := r.Header.Get("X-Device-Token")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if !validToken(token, a.cfg.DeviceToken) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"ok": "false", "error": "unauthorized"})
			return
		}

		// Mutating routes with a body must be application/json.
		if isMutating && r.ContentLength > 0 && !isJSONContentType(r.Header.Get("Content-Type")) {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{
				"ok": "false", "error": "Content-Type must be application/json",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// requiresToken reports whether path requires a device token. / and /healthz
// are exempt; everything under /v1/ requires it.
func requiresToken(path string) bool {
	return strings.HasPrefix(path, "/v1/")
}

// isLoopbackAddr reports whether the host portion of addr is a loopback IP.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isJSONContentType reports whether the Content-Type header value is
// application/json (with optional parameters like charset).
func isJSONContentType(ct string) bool {
	if ct == "" {
		return false
	}
	mediaType := strings.TrimSpace(strings.Split(ct, ";")[0])
	return mediaType == "application/json"
}

// validToken compares the provided token against the expected one using
// constant-time comparison. An empty configured token (want == "") always
// fails — a server with no token configured must not authenticate anyone.
func validToken(got, want string) bool {
	if want == "" {
		return false
	}
	if len(got) != len(want) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// isLoopbackListen reports whether the listen address binds to a loopback IP.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// handleRefresh triggers an immediate scheduler poll and returns the new
// snapshot. If a poll is already running (coalesced), the current snapshot is
// returned with 202 Accepted. Otherwise the poll runs synchronously and 200 OK
// is returned.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	ran := s.sched.Refresh()

	snap := s.withNextSec(s.sched.Current())
	body, err := json.Marshal(snap)
	if err != nil {
		s.logger.Error("marshal snapshot", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "internal"})
		return
	}

	status := http.StatusOK
	if !ran {
		status = http.StatusAccepted // 202: poll still running
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", `"`+snap.Rev+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write(body)
}

// refuseStartError returns the error used when the server would bind to a
// non-loopback address without a device token configured.
func refuseStartError(listen string) error {
	return fmt.Errorf(
		"usaged: refusing to start on non-loopback %q with empty USAGED_DEVICE_TOKEN; "+
			"set USAGED_DEVICE_TOKEN in .env or listen on 127.0.0.1 only",
		listen,
	)
}
