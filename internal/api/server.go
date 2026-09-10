package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"usaged/internal/advise"
	"usaged/internal/config"
	"usaged/internal/creds"
	"usaged/internal/format"
	"usaged/internal/providers"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
	"usaged/internal/stats"
	"usaged/internal/web"
)

// Server hosts the usage HTTP API backed by a scheduler.
type Server struct {
	sched          *sched.Scheduler
	cfg            config.Config
	configPath     string // path to the YAML config file (for interval persistence)
	keystore       creds.KeyStore
	getenv         func(string) string                     // defaults to os.Getenv; injectable for tests
	fetcherBuilder func(config.Config) []providers.Fetcher // rebuilds fetchers after config/key changes
	rateLimit      *rateLimiter                            // guards the /v1/keys endpoints (ORDER #52 task 57)
	pairing        *pairing                                // device pairing window + issued tokens (task 78)
	setup          *setup                                  // one-click BLE device setup (setup.go)
	provisioner    Provisioner                             // BLE central for device setup; nil disables it
	logger         *slog.Logger
	start          time.Time
	tracker        *clientTracker // per-client /v1/usage access log (ORDER #58 task 61)
}

// Option configures a Server built by New.
type Option func(*Server)

// WithKeyStore injects a KeyStore for the /v1/keys endpoints and key_state in
// GET /v1/config. A nil keystore is replaced with the macOS Keychain-backed
// store. Tests inject a FakeKeyStore.
func WithKeyStore(ks creds.KeyStore) Option {
	return func(s *Server) { s.keystore = ks }
}

// WithGetenv injects the getenv function used for env-var key resolution.
// Defaults to os.Getenv. Tests inject a fixed environment.
func WithGetenv(fn func(string) string) Option {
	return func(s *Server) { s.getenv = fn }
}

// WithFetcherBuilder injects the function that rebuilds the provider fetcher
// list from a (possibly mutated) config. When set, config and keychain changes
// rebuild the scheduler's fetchers and trigger a background poll. Defaults to
// nil, in which case runtime changes are not reflected until restart.
func WithFetcherBuilder(fn func(config.Config) []providers.Fetcher) Option {
	return func(s *Server) { s.fetcherBuilder = fn }
}

// New builds the HTTP API server around a scheduler and config. It returns an
// error if the listen address is not loopback and no DeviceToken is configured
// (never expose the LAN port without a token). configPath is the path to the
// YAML config file for persisting runtime changes (may be "").
func New(s *sched.Scheduler, cfg config.Config, configPath string, logger *slog.Logger, opts ...Option) (*http.Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// SECURITY (ORDER #43 BUG 47): never expose the LAN port without a real
	// token. The placeholder is rejected too — it ships in this repository's
	// history, so binding 0.0.0.0 with it is the same as binding with none.
	// This check lives HERE rather than in config.Load because Load is also
	// used by the read-only subcommands, which never bind a socket.
	if !isLoopbackListen(cfg.Listen) &&
		(cfg.DeviceToken == "" || cfg.DeviceToken == config.PlaceholderDeviceToken) {
		return nil, refuseStartError(cfg.Listen)
	}

	srv := &Server{
		sched:      s,
		cfg:        cfg,
		configPath: configPath,
		keystore:   creds.NewKeyStore(),
		getenv:     os.Getenv,
		rateLimit:  newRateLimiter(5, time.Minute),
		logger:     logger,
		start:      time.Now(),
		tracker:    newClientTracker(nil),
	}

	for _, opt := range opts {
		opt(srv)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", srv.handleIndex)
	mux.HandleFunc("GET /index.html", srv.handleIndex)
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /v1/advise", srv.handleAdvise)
	mux.HandleFunc("GET /v1/usage", srv.handleUsage)
	mux.HandleFunc("GET /v1/usage.txt", srv.handleUsageTxt)
	mux.HandleFunc("GET /v1/device", srv.handleDevice)
	mux.HandleFunc("GET /v1/stats", srv.handleStats)
	mux.HandleFunc("POST /v1/refresh", srv.handleRefresh)
	mux.HandleFunc("GET /v1/config", srv.handleGetConfig)
	mux.HandleFunc("PUT /v1/config", srv.handleSetConfig)
	mux.HandleFunc("PUT /v1/config/interval", srv.handleSetInterval)
	mux.HandleFunc("POST /v1/keys", srv.handleSetKey)
	mux.HandleFunc("DELETE /v1/keys", srv.handleDeleteKey)

	// Device pairing (BLE provisioning only). pairing.go backs matchToken
	// and issueDeviceToken for auth and BLE setup; the HTTP window routes
	// were removed in task 87.
	srv.pairing = newPairing(pairedDevicesPath(cfg.StatePath), time.Now, logger)

	// One-click device setup over BLE (setup.go). The Provisioner is injected
	// by cmd/usaged (internal/bleprov) through WithProvisioner; with none wired
	// the routes answer "not available" instead of 404, so the dashboard can
	// explain itself rather than showing a dead button.
	//
	// A Provisioner that implements TokenIssuerAware is handed the
	// mint-and-record function here — it must come AFTER srv.pairing exists,
	// and it is the only way the BLE central can produce a token this agent
	// will later accept. See TokenIssuerAware in setup.go.
	if issuerAware, ok := srv.provisioner.(TokenIssuerAware); ok {
		issuerAware.SetTokenIssuer(srv.pairing.issueDeviceToken)
	}
	srv.setup = newSetup(srv.provisioner, srv.providerReadiness, time.Now, logger)
	srv.setup.routes(mux)

	mux.HandleFunc("/", srv.handleNotFound)

	handler := newAuth(cfg, srv.pairing, logger).middleware(mux)

	return &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}, nil
}

// rateLimiter is a simple fixed-window counter: at most max events per window.
type rateLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	count  int
	start  time.Time
	now    func() time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	return &rateLimiter{max: max, window: window, now: time.Now}
}

// allow reports whether an event is permitted under the rate limit.
func (r *rateLimiter) allow() bool {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.start.IsZero() || now.Sub(r.start) > r.window {
		r.start = now
		r.count = 1
		return true
	}
	if r.count >= r.max {
		return false
	}
	r.count++
	return true
}

// isLoopbackRequest reports whether the request came from a loopback address.
func isLoopbackRequest(r *http.Request) bool {
	return isLoopbackAddr(r.RemoteAddr)
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

// handleAdvise serves the provider ranking as JSON. Unlike /v1/usage, this
// endpoint never uses an ETag or 304: the reset-inside-horizon rule means the
// answer changes as the clock crosses a boundary even with identical provider
// data, so a stale If-None-Match result would serve yesterday's winner. It is
// always 200 with Cache-Control: no-store. Auth follows the standard GET shape —
// loopback-exempt, token required from the LAN (see auth.go).
func (s *Server) handleAdvise(w http.ResponseWriter, _ *http.Request) {
	snap := s.sched.Current()
	outcome := advise.Rank(snap.Providers, time.Now(), advise.DefaultHorizon)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, outcome)
}

// handleUsage serves the current snapshot as compact JSON with an ETag.
// Matching If-None-Match (quoted, bare, or weak) yields 304 Not Modified with
// an empty body. Go's net/http omits the body and Content-Length on 304
// responses (RFC 9110); client robustness (no 304 body, no connection reuse)
// is the firmware's responsibility.
//
// ORDER #58 task 61: every /v1/usage request is access-logged (timestamp,
// peer IP, method, 200-vs-304, If-None-Match) to slog, and the per-client
// device state is recorded. The device state is NOT included in the /v1/usage
// body — it lives at GET /v1/device (ORDER #66 task 67) so it is never trapped
// inside an ETag-cached payload that is only present on 200. The device token
// and any header carrying it are NEVER logged.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	snap := s.withNextSec(s.sched.Current())
	rev := snap.Rev
	etag := `"` + rev + `"`

	now := time.Now()
	peer := peerIP(r)
	inm := r.Header.Get("If-None-Match")
	matched := etagMatch(inm, rev)

	status := http.StatusOK
	if matched {
		status = http.StatusNotModified
	}

	// Identify whether the requester is the StickS3 device via its User-Agent
	// ("sticks3-usage/<buildId>"), not by token or address — that was the
	// ORDER #63 task 64 misclassification (a token-bearing loopback curl and a
	// browser on the LAN URL were both wrongly reported as the device).  The
	// auth middleware (auth.go) has already enforced the token above, so this
	// lookup is only for the device-state tracker.
	ua := r.Header.Get("User-Agent")

	// Record the request and log access (never logs the device token).
	s.tracker.record(peer, ua, now, status)

	// ORDER #65: the firmware sends its locally-computed effective age (seconds
	// since last successful fetch, including deep-sleep duration) as ?age_s=<n>
	// on its /v1/usage GET.  This lets the server verify the RTC sleep-duration
	// fix on the wire: a timer-wake request should carry an age ~60s larger than
	// the one before it.  It does NOT perturb the ETag/304 path — it is only
	// read and logged, never stored in the snapshot or rev hash.
	ageS := r.URL.Query().Get("age_s")
	if ageS != "" {
		// Log the server's own authoritative age for this same instant so the
		// device's claim is directly comparable. Computed identically to the
		// Age field on the 200 path below, and available on a 304 too — where
		// there is no body, so the log is the ONLY place the comparison exists.
		s.logAccessWithAge(r, status, ageS, now.Unix()-snap.CheckedAt)
	} else {
		s.logAccess(r, status)
	}

	if matched {
		writeNotModified(w, etag)
		return
	}

	// 200 response: device state is served separately at GET /v1/device
	// (ORDER #66 task 67) so it never perturbs the ETag/rev hash or the 304
	// path.  Age is the server-computed data freshness in seconds (now -
	// checked_at), outside the Snapshot struct and therefore outside the rev
	// hash: it changes every second, but rev stays stable.
	resp := usageResponse{
		Snapshot: snap,
		Age:      uint32(now.Unix() - snap.CheckedAt),
	}
	body, err := json.Marshal(resp)
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

// handleDevice returns the StickS3 device's state as JSON.  This endpoint
// carries NO ETag and is NOT cached — device state changes every second
// (seconds_since) and must never live inside the ETag-cached /v1/usage
// payload, which is only present on 200 responses (ORDER #66 task 67).
//
// When no device has ever checked in, the response is {"state":"unknown"}
// rather than an error — "waiting for first check-in" is a valid state, not
// a failure.
func (s *Server) handleDevice(w http.ResponseWriter, _ *http.Request) {
	ds := s.tracker.deviceState()
	if ds == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"state": "unknown",
		})
		return
	}
	// OtaArmed reflects whether THIS daemon provisioned the device with an OTA
	// password.  The daemon's DeviceOTAPass is the source of truth: empty means
	// the device will boot OTA-disarmed and can only be flashed over USB (task
	// 113 Part A).  The device itself does not report this back, so the daemon
	// reports its own provisioning decision.
	ds.OtaArmed = len(s.cfg.DeviceOTAPass) > 0
	writeJSON(w, http.StatusOK, ds)
}

// planParamsMap builds the plan params map from the config's effective providers
// (which merges built-in defaults with any YAML overrides), keyed by stats
// source name ("claude_code", "codex"). Providers without a plan block are
// omitted. The source-name mapping ("claude" -> "claude_code") is consistent
// with planParamsMap's original behaviour. The stats package stays free of
// config imports, so this conversion lives here.
func planParamsMap(cfg config.Config) map[string]stats.PlanParams {
	m := map[string]stats.PlanParams{}
	for _, p := range cfg.EffectiveProviders() {
		if p.Plan == nil {
			continue
		}
		srcName := p.ID
		if p.ID == "claude" {
			srcName = "claude_code"
		}
		m[srcName] = stats.PlanParams{
			Cost:       p.Plan.Cost,
			Currency:   p.Plan.Currency,
			CostUSD:    p.Plan.CostUSD,
			HasCostUSD: p.Plan.HasCostUSD,
			Label:      p.Plan.Label,
		}
	}
	return m
}

// handleStats serves the local transcript stats report as JSON with an ETag
// based on the report's GeneratedAt timestamp.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	report := s.sched.CurrentStats()
	if report == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"ok": "false", "error": "stats not ready"})
		return
	}

	// Augment with subscription plan value (API-equiv ratio) from the config.
	stats.ApplyPlanValues(report, planParamsMap(s.cfg))

	// Attach the server-side plan-cost summary (sum of stated plan prices +
	// API-equivalent disclosure) computed from all plan providers, not just
	// the scanned sources.
	report.PlanCost = stats.ComputePlanCost(planParamsMap(s.cfg), report)

	etag := `"` + fmt.Sprintf("%x", report.GeneratedAt) + `"`
	if etagMatch(r.Header.Get("If-None-Match"), fmt.Sprintf("%x", report.GeneratedAt)) {
		writeNotModified(w, etag)
		return
	}

	body, err := json.Marshal(report)
	if err != nil {
		s.logger.Error("marshal stats report", "err", err)
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

// handleGetConfig returns the current editable runtime config as JSON.
// Secrets are redacted: API keys and the device token appear only as
// set(len=N). The response always includes the full effective provider set —
// the five built-in providers in canonical order, with file overrides layered
// on top (ORDER #52a) — so the settings page never renders an empty section
// when no config.yaml exists.
func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	providers := make([]map[string]any, 0, len(config.DefaultProviderOrder))
	for _, p := range s.cfg.EffectiveProviders() {
		entry := map[string]any{
			"id":         p.ID,
			"enabled":    p.Enabled,
			"label":      p.Label,
			"key_state":  s.keyState(p.ID, p.KeyEnv),
			"key_source": s.keySource(p.ID, p.KeyEnv),
		}
		if p.KeyEnv != "" {
			entry["key_env"] = p.KeyEnv
		}
		entry["probe"] = p.Probe
		entry["has_probe"] = p.HasProbe
		entry["can_probe"] = p.CanProbe // ORDER #54 task 59: provider capability, not parse-time flag
		if p.Plan != nil {
			plan := map[string]any{
				"cost":     p.Plan.Cost,
				"currency": p.Plan.Currency,
				"label":    p.Plan.Label,
			}
			if p.Plan.HasCostUSD {
				plan["cost_usd"] = p.Plan.CostUSD
			}
			entry["plan"] = plan
		}
		providers = append(providers, entry)
	}

	// The published tiers, so the settings page can offer them instead of
	// asking the owner to type their own bill. Same for every install, so they
	// ship with the binary rather than living in anyone's config file.
	presets := map[string][]map[string]any{}
	for id, list := range config.PlanPresets() {
		for _, pl := range list {
			entry := map[string]any{
				"cost":     pl.Cost,
				"currency": pl.Currency,
				"label":    pl.Label,
			}
			if pl.HasCostUSD {
				entry["cost_usd"] = pl.CostUSD
			}
			presets[id] = append(presets[id], entry)
		}
	}

	resp := map[string]any{
		"interval_sec":   int(s.cfg.Interval.Seconds()),
		"listen":         s.cfg.Listen,
		"plan_presets":   presets,
		"tz":             s.cfg.TZ.String(),
		"provider_order": s.cfg.ProviderOrder,
		"alerts": map[string]any{
			"openrouter_low_usd":    s.cfg.AlertOpenRouterLowUSD,
			"quota_warn_5h_pct":     s.cfg.AlertQuotaWarn5hPct,
			"quota_warn_weekly_pct": s.cfg.AlertQuotaWarnWeeklyPct,
		},
		"providers": providers,
	}
	writeJSON(w, http.StatusOK, resp)
}

// keyState reports whether a provider's key is available. Resolution order
// (ORDER #52 task 57): env var first (Felipe's .env keeps working), then the
// macOS Keychain. For providers without an env var (claude, codex) the
// keychain is the only fallback; their OAuth/auth status is reflected by the
// snapshot's provider status.
func (s *Server) keyState(id, keyEnv string) string {
	if keyEnv != "" && s.getenv(keyEnv) != "" {
		return "set"
	}
	if k, ok, _ := s.keystore.Get(context.Background(), id); ok && k != "" {
		return "set"
	}
	if keyEnv == "" {
		// OAuth providers: fall back to the last-known fetch status. An "ok"
		// or "stale" status means credentials are present; "auth" means the
		// user must sign in.
		for _, p := range s.sched.Current().Providers {
			if p.ID == id {
				if p.Status == "ok" || p.Status == "stale" {
					return "set"
				}
				break
			}
		}
	}
	return "not_set"
}

// keySource reports WHERE a provider's credential actually lives, so the
// settings page can label it correctly (ORDER #66 task 67c). Resolution:
// env var first, then the provider's own external source (Claude Code's
// Keychain entry, Codex's auth.json file), then usaged's own Keychain as
// last resort. Returns "none" when no credential is available.
func (s *Server) keySource(id, keyEnv string) string {
	// Env var takes precedence (ORDER #47 task 52).
	if keyEnv != "" && s.getenv(keyEnv) != "" {
		return "env:" + keyEnv
	}
	// OAuth/self-hosted providers: credentials live outside usaged.
	if keyEnv == "" {
		switch id {
		case config.ProviderClaude:
			// Credential source: Claude Code's own Keychain entry.
			if s.hasOAuthCreds(id) {
				return "claude-code"
			}
			return "none"
		case config.ProviderCodex:
			// Credential source: ~/.codex/auth.json (a file, not Keychain).
			if s.hasOAuthCreds(id) {
				return "codex"
			}
			return "none"
		}
	}
	// Usaged's own Keychain (for env-var providers whose env var is unset
	// but which have a key stored via the SETTINGS page).
	if k, ok, _ := s.keystore.Get(context.Background(), id); ok && k != "" {
		return "keychain"
	}
	return "none"
}

// hasOAuthCreds reports whether an OAuth/self-hosted provider (claude, codex)
// currently has credentials, based on the last-known fetch status. This is
// the same check keyState uses for these providers.
func (s *Server) hasOAuthCreds(id string) bool {
	for _, p := range s.sched.Current().Providers {
		if p.ID == id {
			return p.Status == "ok" || p.Status == "stale"
		}
	}
	return false
}

// handleSetConfig accepts a PUT with the full editable config and writes it
// atomically to the YAML config file (if one exists). It validates the input,
// applies safe changes in-memory, and rejects any key-shaped values. An
// invalid payload leaves the file and the running config unchanged.
func (s *Server) handleSetConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "invalid JSON body"})
		return
	}

	// Reject key-shaped values anywhere in the payload (ORDER #43 / task 55).
	if err := rejectKeyValues(raw); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"ok":    "false",
			"error": "keys live in environment variables, not in the config"},
		)
		return
	}

	type planReq struct {
		Cost     float64  `json:"cost"`
		Currency string   `json:"currency"`
		Label    string   `json:"label"`
		CostUSD  *float64 `json:"cost_usd,omitempty"`
	}
	type providerReq struct {
		ID      string   `json:"id"`
		Enabled *bool    `json:"enabled"`
		Label   string   `json:"label"`
		KeyEnv  string   `json:"key_env"`
		Probe   *bool    `json:"probe"`
		Plan    *planReq `json:"plan,omitempty"`
	}
	type configReq struct {
		IntervalSec   int                `json:"interval_sec"`
		Listen        string             `json:"listen"`
		TZ            string             `json:"tz"`
		Alerts        map[string]float64 `json:"alerts"`
		Providers     []providerReq      `json:"providers"`
		ProviderOrder []string           `json:"provider_order"`
	}

	var body configReq
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "invalid JSON body"})
		return
	}

	// Validate interval.
	if body.IntervalSec != 0 && body.IntervalSec < config.MinIntervalSec {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"ok":    "false",
			"error": fmt.Sprintf("interval_sec must be >= %d", config.MinIntervalSec),
		})
		return
	}

	// Validate alert keys and their values.
	for k := range body.Alerts {
		if !config.ValidAlertKey(k) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"ok":    "false",
				"error": fmt.Sprintf("unknown alert key %q", k),
			})
			return
		}
	}
	if v, ok := body.Alerts["quota_warn_5h_pct"]; ok {
		if v < 50 || v > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"ok":    "false",
				"error": fmt.Sprintf("invalid field: alerts.quota_warn_5h_pct must be 50-100, got %v", v),
			})
			return
		}
	}
	if v, ok := body.Alerts["quota_warn_weekly_pct"]; ok {
		if v < 50 || v > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"ok":    "false",
				"error": fmt.Sprintf("invalid field: alerts.quota_warn_weekly_pct must be 50-100, got %v", v),
			})
			return
		}
	}
	if v, ok := body.Alerts["openrouter_low_usd"]; ok {
		if v < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"ok":    "false",
				"error": fmt.Sprintf("invalid field: alerts.openrouter_low_usd must be >= 0, got %v", v),
			})
			return
		}
	}

	// Validate providers: check required id.
	for _, p := range body.Providers {
		if p.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "provider missing id"})
			return
		}
	}

	// Build a FileConfig from the request and update in-memory state.
	fc := config.FileConfig{
		IntervalSec:   body.IntervalSec,
		Listen:        body.Listen,
		TZ:            body.TZ,
		ProviderOrder: body.ProviderOrder,
	}
	for k, v := range body.Alerts {
		if fc.Alerts == nil {
			fc.Alerts = map[string]float64{}
		}
		fc.Alerts[k] = v
	}
	// Track the full provider set in-memory so GET /v1/config reflects changes
	// immediately; persist only the overrides (providers differing from their
	// built-in defaults) so the file stays minimal (ORDER #52a).
	provs := make(map[string]config.YamlProvider, len(body.Providers))
	for _, p := range body.Providers {
		yp := config.YamlProvider{
			ID:      p.ID,
			Enabled: true,
			Label:   p.Label,
			KeyEnv:  p.KeyEnv,
		}
		if p.Enabled != nil {
			yp.Enabled = *p.Enabled
		}
		if p.Probe != nil {
			yp.Probe = *p.Probe
			yp.HasProbe = true
		}
		if p.Plan != nil {
			yp.Plan = &config.PlanConfig{
				Cost:     p.Plan.Cost,
				Currency: p.Plan.Currency,
				Label:    p.Plan.Label,
			}
			if p.Plan.CostUSD != nil {
				yp.Plan.CostUSD = *p.Plan.CostUSD
				yp.Plan.HasCostUSD = true
			}
		}
		provs[p.ID] = yp
	}
	// Persist only providers that differ from the built-in defaults.
	fc.Providers = config.OverridesOnly(buildYamlProviders(provs))

	yamlText, err := config.SerializeYAML(fc)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "serialize config"})
		return
	}

	// Persist atomically if a config path exists (creating it on first save).
	if s.configPath != "" {
		if err := saveConfigAtomic(s.configPath, yamlText, s.logger); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{
				"ok":    "false",
				"error": "failed to write config file"},
			)
			return
		}
	}

	// Apply in-memory changes.
	if body.IntervalSec != 0 {
		s.sched.SetInterval(time.Duration(body.IntervalSec) * time.Second)
		s.cfg.Interval = time.Duration(body.IntervalSec) * time.Second
	}
	if body.Listen != "" {
		s.cfg.Listen = body.Listen
	}
	if body.TZ != "" {
		if loc, err := time.LoadLocation(body.TZ); err == nil {
			s.cfg.TZ = loc
		}
	}
	s.cfg.ProviderConfigs = provs
	if len(body.ProviderOrder) > 0 {
		s.cfg.ProviderOrder = body.ProviderOrder
	}
	// Apply alert thresholds in-memory (migration-aware: legacy
	// quota_warn_pct seeds both new keys; explicit new keys take precedence).
	if v, ok := fc.Alerts["quota_warn_pct"]; ok {
		s.cfg.AlertQuotaWarn5hPct = int(v)
		s.cfg.AlertQuotaWarnWeeklyPct = int(v)
	}
	if v, ok := fc.Alerts["quota_warn_5h_pct"]; ok {
		s.cfg.AlertQuotaWarn5hPct = int(v)
	}
	if v, ok := fc.Alerts["quota_warn_weekly_pct"]; ok {
		s.cfg.AlertQuotaWarnWeeklyPct = int(v)
	}
	if v, ok := fc.Alerts["openrouter_low_usd"]; ok {
		s.cfg.AlertOpenRouterLowUSD = v
	}
	s.rebuildFetchers()

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// buildYamlProviders converts a provider-id map to an ordered slice matching the
// canonical provider order, omitting ids that are not in DefaultProviderOrder.
func buildYamlProviders(m map[string]config.YamlProvider) []config.YamlProvider {
	out := make([]config.YamlProvider, 0, len(m))
	for _, id := range config.DefaultProviderOrder {
		if p, ok := m[id]; ok {
			out = append(out, p)
		}
	}
	return out
}

// rejectKeyValues scans the raw JSON body for any object containing a "key"
// field whose value looks like an API key (starts with sk- or gsk_).
func rejectKeyValues(raw []byte) error {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if k, ok := top["key"].(string); ok {
		if strings.HasPrefix(k, "sk-") || strings.HasPrefix(k, "gsk_") {
			return fmt.Errorf("keys live in environment variables, not in the config")
		}
	}
	if provs, ok := top["providers"].([]any); ok {
		for _, p := range provs {
			if pm, ok := p.(map[string]any); ok {
				if k, ok := pm["key"].(string); ok {
					if strings.HasPrefix(k, "sk-") || strings.HasPrefix(k, "gsk_") {
						return fmt.Errorf("keys live in environment variables, not in the config")
					}
				}
			}
		}
	}
	return nil
}

// handleSetInterval accepts a PUT to change the poll interval at runtime.
// The new value is applied immediately (without restart) and persisted to
// the YAML config file if one exists. Validates >= MinIntervalSec (300).
func (s *Server) handleSetInterval(w http.ResponseWriter, r *http.Request) {
	type req struct {
		IntervalSec int `json:"interval_sec"`
	}
	var body req
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "invalid JSON body"})
		return
	}
	if body.IntervalSec < config.MinIntervalSec {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"ok":    "false",
			"error": fmt.Sprintf("interval_sec must be >= %d", config.MinIntervalSec),
		})
		return
	}

	newInterval := time.Duration(body.IntervalSec) * time.Second
	s.sched.SetInterval(newInterval)
	s.cfg.Interval = newInterval

	if s.configPath != "" {
		s.persistInterval(body.IntervalSec)
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "interval_sec": body.IntervalSec})
}

// --- Key management (ORDER #52 task 57) ---
//
// POST /v1/keys  { "id": <provider>, "value": <key> }  → store in keychain
// DELETE /v1/keys?id=<provider>                       → remove from keychain
//
// Keys live ONLY in the macOS Keychain (service "usaged"), never in YAML,
// never in GET /v1/config, never logged. Both endpoints are loopback-only,
// require a device token, and are rate-limited.

var validKeyProviderIDs = map[string]bool{
	config.ProviderClaude:         true,
	config.ProviderCodex:          true,
	config.ProviderOpenRouterMain: true,
	config.ProviderOpenRouterFbk:  true,
	config.ProviderGroq:           true,
	config.ProviderOpenCodeGo:     true,
}

// rebuildFetchers rebuilds the scheduler's fetcher list from the current
// in-memory config and kicks a background poll so the change is reflected
// without a restart. It is a no-op when no fetcher builder was injected.
func (s *Server) rebuildFetchers() {
	if s.fetcherBuilder == nil {
		return
	}
	fetchers := s.fetcherBuilder(s.cfg)
	s.sched.SetFetchers(fetchers)
	s.sched.SetProviderOrder(s.cfg.ProviderOrder)
	go s.sched.Refresh()
}

// handleSetKey stores an API key for a provider in the keychain. The provider id
// must be one of the five known providers and the value must be non-empty.
func (s *Server) handleSetKey(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"ok": "false", "error": "keys must be set from localhost"})
		return
	}
	if !s.rateLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"ok": "false", "error": "rate limit exceeded"})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "invalid JSON body"})
		return
	}

	var body struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "invalid JSON body"})
		return
	}
	if !validKeyProviderIDs[body.ID] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"ok":    "false",
			"error": fmt.Sprintf("unknown provider id %q", body.ID),
		})
		return
	}
	if strings.TrimSpace(body.Value) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"ok": "false", "error": "value must not be empty"})
		return
	}

	if err := s.keystore.Set(r.Context(), body.ID, body.Value); err != nil {
		s.logger.Error("keychain set failed", "id", body.ID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "keychain write failed"})
		return
	}

	// Never echo the key value.
	s.logger.Info("key stored", "id", body.ID, "key_len", len(body.Value))
	s.rebuildFetchers()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": body.ID})
}

// handleDeleteKey removes an API key for a provider from the keychain.
func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"ok": "false", "error": "keys must be managed from localhost"})
		return
	}
	if !s.rateLimit.allow() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"ok": "false", "error": "rate limit exceeded"})
		return
	}

	id := r.URL.Query().Get("id")
	if !validKeyProviderIDs[id] {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"ok":    "false",
			"error": fmt.Sprintf("unknown provider id %q", id),
		})
		return
	}

	if err := s.keystore.Delete(r.Context(), id); err != nil {
		s.logger.Error("keychain delete failed", "id", id, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "keychain delete failed"})
		return
	}

	s.rebuildFetchers()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// persistInterval updates interval_sec in the YAML config file in-place.
func (s *Server) persistInterval(sec int) {
	if s.configPath == "" {
		s.logger.Warn("persist interval: no config path; the change will not survive a restart")
		return
	}
	// A MISSING FILE IS THE NORMAL FIRST CASE, NOT AN ERROR. This used to read
	// before writing and give up when the read failed, so on any install that
	// had never written a config — which is every fresh install — the interval
	// was accepted, answered ok, and silently lost on the next restart.
	text, err := os.ReadFile(s.configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.Warn("persist interval: read config file", "path", s.configPath, "err", err)
			return
		}
		text = nil
		if mkErr := os.MkdirAll(filepathDir(s.configPath), 0o700); mkErr != nil {
			s.logger.Warn("persist interval: create config dir", "path", s.configPath, "err", mkErr)
			return
		}
	}
	re := regexp.MustCompile(`(?m)^interval_sec:\s*\d+\s*$`)
	newLine := fmt.Sprintf("interval_sec: %d", sec)
	if re.Match(text) {
		text = re.ReplaceAll(text, []byte(newLine))
	} else {
		text = append(text, '\n')
		text = append(text, []byte(newLine)...)
	}
	if err := os.WriteFile(s.configPath, text, 0o600); err != nil {
		s.logger.Warn("persist interval: write config file", "path", s.configPath, "err", err)
	}
}

// saveConfigAtomic writes the YAML text to path atomically: create a timestamped
// backup, write to a temp file, rename into place. Creates the parent directory
// (0700) on first save when it does not yet exist. The file mode is 0600.
// A failure at any step leaves the original file untouched.
func saveConfigAtomic(path, yamlText string, logger *slog.Logger) error {
	// On first save, create the parent directory (0700) and the file (0600).
	dir := filepathDir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}

	// Create a backup: config.yaml → config.yaml.bak.<timestamp>.
	backupPath := path + ".bak." + time.Now().Format("20060102T150405")
	if data, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(backupPath, data, 0o600); err != nil {
			logger.Warn("saveConfigAtomic: backup failed", "backup", backupPath, "err", err)
		}
	}

	// Write to a temp file in the same directory (for rename atomicity).
	tmpPath := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmpPath, []byte(yamlText), 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	// Rename the temp file into the final path.
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// filepathDir returns the directory portion of path.
func filepathDir(path string) string {
	idx := strings.LastIndex(path, "/")
	if idx >= 0 {
		return path[:idx]
	}
	return "."
}

// handleIndex serves the embedded dashboard at / and /index.html.
func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Write(web.IndexHTML)
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
		p = strings.TrimPrefix(p, "W/")
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
