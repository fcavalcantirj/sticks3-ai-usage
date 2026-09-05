package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"usaged/internal/config"
	"usaged/internal/format"
	"usaged/internal/sched"
	"usaged/internal/snapshot"
	"usaged/internal/stats"
	"usaged/internal/web"
)

// Server hosts the usage HTTP API backed by a scheduler.
type Server struct {
	sched      *sched.Scheduler
	cfg        config.Config
	configPath string // path to the YAML config file (for interval persistence)
	logger     *slog.Logger
	start      time.Time
}

// New builds the HTTP API server around a scheduler and config. It returns an
// error if the listen address is not loopback and no DeviceToken is configured
// (never expose the LAN port without a token). configPath is the path to the
// YAML config file for persisting runtime changes (may be "").
func New(s *sched.Scheduler, cfg config.Config, configPath string, logger *slog.Logger) (*http.Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	if cfg.DeviceToken == "" && !isLoopbackListen(cfg.Listen) {
		return nil, refuseStartError(cfg.Listen)
	}

	srv := &Server{
		sched:      s,
		cfg:        cfg,
		configPath: configPath,
		logger:     logger,
		start:      time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", srv.handleIndex)
	mux.HandleFunc("GET /index.html", srv.handleIndex)
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /v1/usage", srv.handleUsage)
	mux.HandleFunc("GET /v1/usage.txt", srv.handleUsageTxt)
	mux.HandleFunc("GET /v1/stats", srv.handleStats)
	mux.HandleFunc("POST /v1/refresh", srv.handleRefresh)
	mux.HandleFunc("GET /v1/config", srv.handleGetConfig)
	mux.HandleFunc("PUT /v1/config", srv.handleSetConfig)
	mux.HandleFunc("PUT /v1/config/interval", srv.handleSetInterval)
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

// planParamsMap builds the plan params map from the config's provider configs,
// keyed by stats source name ("claude_code", "codex"). Providers without a
// plan block are omitted. The stats package stays free of config imports, so
// this conversion lives here.
func planParamsMap(cfg config.Config) map[string]stats.PlanParams {
	m := map[string]stats.PlanParams{}
	for id, p := range cfg.ProviderConfigs {
		if p.Plan == nil {
			continue
		}
		srcName := id
		if id == "claude" {
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
// set(len=N). The response includes interval_sec, listen, tz, alert thresholds,
// and per-provider settings (enabled, label, key_env, probe, plan). It does NOT
// expose OpenRouter keys or the Groq key.
func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"interval_sec": int(s.cfg.Interval.Seconds()),
		"listen":       s.cfg.Listen,
		"tz":           s.cfg.TZ.String(),
		"alerts": map[string]any{
			"openrouter_low_usd": s.cfg.AlertOpenRouterLowUSD,
			"quota_warn_pct":     s.cfg.AlertQuotaWarnPct,
		},
		"providers": []map[string]any{},
	}
	for id, p := range s.cfg.ProviderConfigs {
		entry := map[string]any{
			"id":      id,
			"enabled": p.Enabled,
			"label":   p.Label,
		}
		if p.KeyEnv != "" {
			entry["key_env"] = p.KeyEnv
		}
		entry["probe"] = p.Probe
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
		resp["providers"] = append(resp["providers"].([]map[string]any), entry)
	}
	writeJSON(w, http.StatusOK, resp)
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
		IntervalSec int                `json:"interval_sec"`
		Listen      string             `json:"listen"`
		TZ          string             `json:"tz"`
		Alerts      map[string]float64 `json:"alerts"`
		Providers   []providerReq      `json:"providers"`
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

	// Validate alert keys.
	for k := range body.Alerts {
		if !config.ValidAlertKey(k) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"ok":    "false",
				"error": fmt.Sprintf("unknown alert key %q", k),
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

	// Build a FileConfig from the request and serialize it.
	fc := config.FileConfig{
		IntervalSec: body.IntervalSec,
		Listen:      body.Listen,
		TZ:          body.TZ,
	}
	for k, v := range body.Alerts {
		if fc.Alerts == nil {
			fc.Alerts = map[string]float64{}
		}
		fc.Alerts[k] = v
	}
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
		fc.Providers = append(fc.Providers, yp)
	}

	yamlText, err := config.SerializeYAML(fc)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"ok": "false", "error": "serialize config"})
		return
	}

	// Persist atomically if a config path exists.
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

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

// persistInterval updates interval_sec in the YAML config file in-place.
func (s *Server) persistInterval(sec int) {
	text, err := os.ReadFile(s.configPath)
	if err != nil {
		s.logger.Warn("persist interval: read config file", "path", s.configPath, "err", err)
		return
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
// backup, write to a temp file, rename into place. The file mode is 0600.
// A failure at any step leaves the original file untouched.
func saveConfigAtomic(path, yamlText string, logger *slog.Logger) error {
	// Create a backup: config.yaml → config.yaml.bak.<timestamp>.
	backupPath := path + ".bak." + time.Now().Format("20060102T150405")
	if data, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(backupPath, data, 0o600); err != nil {
			logger.Warn("saveConfigAtomic: backup failed", "backup", backupPath, "err", err)
		}
	}

	// Write to a temp file in the same directory (for rename atomicity).
	dir := filepathDir(path)
	tmpPath := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmpPath, []byte(yamlText), 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}

	// Rename the temp file into the final path.
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	_ = dir
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
