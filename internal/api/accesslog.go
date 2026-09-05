package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"usaged/internal/snapshot"
)

// clientEntry records the latest /v1/usage request from a single client.
type clientEntry struct {
	firstSeen  time.Time
	lastSeen   time.Time
	prevSeen   time.Time // previous to lastSeen, for interval computation
	lastStatus int       // 200 or 304
	count200   int
	count304   int
}

// clientTracker records per-client /v1/usage request metadata so the system
// can prove whether the device is sleeping — deep sleep manifests as a long,
// regular interval between requests. Keyed by client IP (from RemoteAddr).
//
// The device state rides on the /v1/usage request: it does NOT perturb the
// ETag/If-None-Match path, does NOT change rev, and does NOT cause a redraw.
// On a 304 there is no body, so no device_state is sent — the web page caches
// the last one from a 200 response. A 304 stays a 304.
type clientTracker struct {
	mu      sync.RWMutex
	clients map[string]*clientEntry
	now     func() time.Time
}

func newClientTracker(nowFn func() time.Time) *clientTracker {
	if nowFn == nil {
		nowFn = time.Now
	}
	return &clientTracker{
		clients: make(map[string]*clientEntry),
		now:     nowFn,
	}
}

// record stores the latest /v1/usage request from clientKey at time now with
// the given HTTP status (200 or 304). It updates the 200/304 counters and
// recomputes the observed interval between consecutive requests.
func (t *clientTracker) record(clientKey string, now time.Time, status int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.clients[clientKey]
	if !ok {
		entry = &clientEntry{
			firstSeen:  now,
			lastSeen:   now,
			prevSeen:   now,
			lastStatus: status,
		}
		t.clients[clientKey] = entry
	}

	// If this is not the first request, prevSeen captures the previous request
	// time so the interval can be computed. On the first request prevSeen ==
	// lastSeen, so the interval reads 0.
	if !entry.lastSeen.IsZero() && !now.Equal(entry.lastSeen) {
		entry.prevSeen = entry.lastSeen
	}
	entry.lastSeen = now
	entry.lastStatus = status
	if status == http.StatusOK {
		entry.count200++
	} else if status == http.StatusNotModified {
		entry.count304++
	}
}

// state returns the device state for the given client, or nil if no
// /v1/usage request has ever been recorded for that client.
func (t *clientTracker) state(clientKey string) *deviceState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	entry, ok := t.clients[clientKey]
	if !ok {
		return nil
	}

	now := t.now()
	return &deviceState{
		LastSeen:     entry.lastSeen.Unix(),
		SecondsSince: int64(now.Sub(entry.lastSeen).Seconds()),
		IntervalSec:  int64(entry.lastSeen.Sub(entry.prevSeen).Seconds()),
		Count200:     entry.count200,
		Count304:     entry.count304,
		LastStatus:   entry.lastStatus,
	}
}

// deviceState is the per-client device state exposed on /v1/usage. It does NOT
// participate in the ETag/rev hash and never appears in a 304 body.
type deviceState struct {
	LastSeen     int64 `json:"last_seen"`     // unix s of the latest request
	SecondsSince int64 `json:"seconds_since"` // seconds elapsed since lastSeen
	IntervalSec  int64 `json:"interval_sec"`  // seconds between the last two requests
	Count200     int   `json:"count_200"`     // 200 OK responses observed
	Count304     int   `json:"count_304"`     // 304 Not Modified responses observed
	LastStatus   int   `json:"last_status"`   // 200 or 304
}

// usageResponse wraps the snapshot with optional device state for the client
// that made the request. DeviceState is NOT part of the ETag/rev hash and
// does not affect the firmware's 304 behaviour — on 304 there is no body.
// The firmware's JSON parser ignores the extra field.
type usageResponse struct {
	snapshot.Snapshot
	DeviceState *deviceState `json:"device_state,omitempty"`
}

// peerIP extracts the client IP from r.RemoteAddr, stripping the port.
func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// logAccess logs a /v1/usage access entry to slog. It logs timestamp (via
// slog's built-in time field), peer IP, method, status, and the If-None-Match
// header value — but NEVER the device token or any header carrying it.
func (s *Server) logAccess(r *http.Request, status int) {
	s.logger.Info("access",
		"path", "/v1/usage",
		"method", r.Method,
		"peer", peerIP(r),
		"status", status,
		"if_none_match", r.Header.Get("If-None-Match"),
	)
}
