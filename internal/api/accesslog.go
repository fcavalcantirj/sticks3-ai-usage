package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"
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
	addr       string // client IP address (without port)
	userAgent  string // last User-Agent seen from this client
	isDevice   bool   // re-evaluated from userAgent on every request (ORDER #65)
	otaArmed   bool   // device-reported OTA armed state (from ?ota_armed= query param, task 115)
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
// the given HTTP status (200 or 304).  userAgent is the request's User-Agent
// header value, and otaArmed is the device-reported OTA state parsed from the
// ?ota_armed= query parameter (task 115) — true means the device has an OTA
// password stored and ArduinoOTA.begin() was called.  The client is marked as a
// device (isDevice) when its User-Agent starts with "sticks3-usage/" — the
// firmware identifies itself that way, while a browser or loopback curl never
// does (ORDER #63 task 64).
func (t *clientTracker) record(clientKey, userAgent string, now time.Time, status int, otaArmed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entry, ok := t.clients[clientKey]
	if !ok {
		entry = &clientEntry{
			firstSeen:  now,
			lastSeen:   now,
			prevSeen:   now,
			lastStatus: status,
			addr:       clientKey,
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
	entry.addr = clientKey
	entry.userAgent = userAgent
	// Re-evaluate isDevice from the CURRENT User-Agent on every request —
	// do NOT latch it.  A prior device-UA request must not keep the flag if a
	// later request from the same IP uses a different UA (ORDER #65).
	entry.isDevice = isDeviceUserAgent(userAgent)
	entry.otaArmed = otaArmed
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
	return t.entryToState(entry)
}

// deviceState returns the state of the StickS3 device itself — the most
// recently active tracked client identified as a device by its User-Agent
// ("sticks3-usage/") — rather than the client making the current request.
// This is the fix for ORDER #63 task 64: a token-bearing loopback curl or a
// browser on the LAN URL must not be reported as the device.  Returns nil if
// no device client has been seen yet, so device_state is ABSENT (never absent
// a fallback to the requester).
func (t *clientTracker) deviceState() *deviceState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var dev *clientEntry
	for _, e := range t.clients {
		if !e.isDevice {
			continue
		}
		if dev == nil || e.lastSeen.After(dev.lastSeen) {
			dev = e
		}
	}
	if dev == nil {
		return nil
	}
	return t.entryToState(dev)
}

// entryToState converts a clientEntry to a deviceState snapshot.
func (t *clientTracker) entryToState(e *clientEntry) *deviceState {
	now := t.now()
	return &deviceState{
		LastSeen:     e.lastSeen.Unix(),
		SecondsSince: int64(now.Sub(e.lastSeen).Seconds()),
		IntervalSec:  int64(e.lastSeen.Sub(e.prevSeen).Seconds()),
		Count200:     e.count200,
		Count304:     e.count304,
		LastStatus:   e.lastStatus,
		State:        computeDeviceState(int64(e.lastSeen.Sub(e.prevSeen).Seconds()), int64(now.Sub(e.lastSeen).Seconds())),
		ClientAddr:   e.addr,
		OtaArmed:     e.otaArmed,
	}
}

// deviceState is the per-client device state exposed on /v1/usage. It does NOT
// participate in the ETag/rev hash and never appears in a 304 body.
type deviceState struct {
	LastSeen     int64  `json:"last_seen"`      // unix s of the latest request
	SecondsSince int64  `json:"seconds_since"`  // seconds elapsed since lastSeen
	IntervalSec  int64  `json:"interval_sec"`   // seconds between the last two requests
	Count200     int    `json:"count_200"`      // 200 OK responses observed
	Count304     int    `json:"count_304"`      // 304 Not Modified responses observed
	LastStatus   int    `json:"last_status"`    // 200 or 304
	State        string `json:"state"`          // "connected", "absent", or "unknown"
	ClientAddr   string `json:"addr,omitempty"` // IP of the client this state describes
	OtaArmed     bool   `json:"ota_armed"`      // whether the daemon provisioned with an OTA password
}

// computeDeviceState returns an explicit presence state from the observed
// polling cadence rather than leaving the caller to interpret a raw number.
//
// On USB the device polls every 300 s; on battery it sleeps, so a gap far
// longer than the observed interval IS deep sleep. "absent" is reported when
// seconds_since exceeds 3× the expected interval (observed interval if
// available, else 600 s) — enough slack to ride out jitter without hiding a
// real disappearance. "unknown" covers a client that has never made a
// request.
func computeDeviceState(intervalSec, secondsSince int64) string {
	if secondsSince == 0 && intervalSec == 0 {
		return "connected"
	}
	expected := intervalSec
	if expected == 0 {
		expected = 600 // seconds — fallback when no interval has been observed yet
	}
	if secondsSince > 3*expected {
		return "absent"
	}
	return "connected"
}

// usageResponse wraps the Snapshot with the data-freshness Age field.
// Device state is NO LONGER included here — it has its own endpoint
// (GET /v1/device, ORDER #66 task 67) so it is never trapped inside an
// ETag-cached payload that is only present on 200 responses.
//
// Age is the server-computed data freshness in seconds (now - checked_at),
// added so the firmware — which has no clock — can track staleness as
// age + elapsed millis since the last fetch.  It is outside the Snapshot
// struct and therefore outside the rev hash: it changes every second, but
// rev stays stable and a 304 still returns no body.  The firmware's JSON
// parser reads it via model.parseSnapshot.
type usageResponse struct {
	snapshot.Snapshot
	Age uint32 `json:"age,omitempty"`
}

// peerIP extracts the client IP from r.RemoteAddr, stripping the port.
func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isDeviceUserAgent reports whether the User-Agent string identifies the
// StickS3 firmware client.  The firmware sends "sticks3-usage/<buildId>"
// (see firmware/src/hal/sticks3/fetch.cpp).  A browser, curl, or any other
// client sends a different UA and is never classified as the device.
func isDeviceUserAgent(ua string) bool {
	return strings.HasPrefix(ua, "sticks3-usage/")
}

// logAccess logs a /v1/usage access entry to slog. It logs timestamp (via
// slog's built-in time field), peer IP, method, User-Agent, status, and the
// If-None-Match header value — but NEVER the device token or any header
// carrying it.  The User-Agent is logged so isDevice classification can be
// debugged on the wire (ORDER #65: addHeader("User-Agent") is silently
// dropped by the Arduino HTTPClient core, so the log is the only proof).
func (s *Server) logAccess(r *http.Request, status int) {
	s.logger.Info("access",
		"path", "/v1/usage",
		"method", r.Method,
		"peer", peerIP(r),
		"user_agent", r.Header.Get("User-Agent"),
		"status", status,
		"if_none_match", r.Header.Get("If-None-Match"),
		"ota_armed", r.URL.Query().Get("ota_armed") == "1",
	)
}

// logAccessWithAge is like logAccess but also logs the firmware-reported
// effective age (ORDER #65: the device sends ?age_s=<n> = seconds of data
// staleness it computed locally, including deep-sleep duration), ALONGSIDE the
// server's own authoritative age for the same instant.
//
// Logging both is the point. The device's age_s is only meaningful against the
// server's now-minus-checked_at, and reconstructing that baseline by hand is
// error-prone: it produced two false "offset" defects during the 2026-09-06
// hardware sessions, because the age a device reports immediately after a
// reboot is its own pre-fetch estimate, not evidence about the server. With
// both numbers and their difference on one line, that misreading is not
// available — drift_s near zero means the freshness pipeline is correct, and a
// large drift is a real defect rather than an artefact of the reader.
func (s *Server) logAccessWithAge(r *http.Request, status int, ageS string, serverAgeSec int64) {
	attrs := []any{
		"path", "/v1/usage",
		"method", r.Method,
		"peer", peerIP(r),
		"user_agent", r.Header.Get("User-Agent"),
		"status", status,
		"if_none_match", r.Header.Get("If-None-Match"),
		"age_s", ageS,
		"server_age_s", serverAgeSec,
		"ota_armed", r.URL.Query().Get("ota_armed") == "1",
	}
	// drift_s = what the device believes minus what the server knows. Only
	// computable when the device sent a parseable number; a malformed value is
	// logged verbatim above and simply carries no drift.
	if n, err := strconv.ParseInt(ageS, 10, 64); err == nil {
		attrs = append(attrs, "drift_s", n-serverAgeSec)
	}
	s.logger.Info("access", attrs...)
}
