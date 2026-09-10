/*
 * AI Usage dashboard — vanilla JS, no build step, one file.
 *
 * Protocol: poll /v1/usage every N s (from /v1/config interval) with
 * If-None-Match; poll /v1/stats on its own interval with ETag. On 304 update
 * the "checked" time from /healthz; on 200 re-render only the changed sections.
 * Reset texts tick every 30 s.
 *
 * Token is read from ?token= (→ localStorage usaged_token), sent as
 * X-Device-Token on every /v1/* call; 401 surfaces an inline token input.
 *
 * First-paint fix (ORDER #48 latent bug): the stats etag is NEVER sent on the
 * first load — a stale etag from a previous session would make the server
 * return 304 with no body, and the page would stay at placeholder zeros forever.
 */

// ---- Pure helpers ----

function tierColor(tier) {
  var m = { ok: "var(--ok)", warn: "var(--warn)", crit: "var(--crit)", off: "var(--muted)" };
  return m[tier] || "var(--muted)";
}

function planLabel(plan) {
  if (plan === "max_20x") return "Max (20x)";
  if (plan === "plus")    return "Plus";
  return plan || "";
}

function statusBadge(status, msg) {
  if (status === "ok") return "";
  if (status === "off") return badge("off", "no key");
  if (status === "stale") return badge("stale", "stale \u00b7 " + msg);
  if (status === "auth")  return badge("auth",  msg);
  if (status === "error") return badge("error", msg);
  return "";
}

function badge(cls, text) {
  return '<span class="badge badge-status badge-' + cls + '">' + esc(text) + '</span>';
}

function fmtTime(unixSec) {
  if (!unixSec) return "\u2014";
  var d = new Date(unixSec * 1000);
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function resetText(resetAt, fallbackTxt) {
  if (!resetAt) return fallbackTxt || "";
  var now = Math.floor(Date.now() / 1000);
  var diff = resetAt - now;
  if (diff <= 0) return fallbackTxt || "";
  if (diff > 86400) {
    var d = new Date(resetAt * 1000);
    return "Resets " + d.toLocaleDateString(undefined, { weekday: "short" }) +
           " " + d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  }
  var h = Math.floor(diff / 3600);
  var m = Math.floor((diff % 3600) / 60);
  return "Resets in " + h + "h " + m + "m";
}

function pctText(pct, txt) {
  return pct != null ? (pct + "% used") : (txt || "");
}

function esc(s) {
  return String(s).replace(/[&<>"']/g, function(c) {
    return { "&": "&#38;", "<": "&#60;", ">": "&#62;", '"': "&#34;", "'": "&#39;" }[c];
  });
}

// formatShortDuration renders seconds as a compact "XhYm" or "Xm" string,
// mirroring the Go formatShortDuration in internal/advise.
function formatShortDuration(sec) {
  if (!sec) return '';
  var num = Number(sec);
  if (!num || num <= 0) return '';
  var h = Math.floor(num / 3600);
  var m = Math.floor((num % 3600) / 60);
  if (h > 0) return h + 'h' + m + 'm';
  return m + 'm';
}

function fmtTokens(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(1) + "B";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}

function fmtCost(n) {
  return "$" + n.toFixed(2);
}

// ---- Token handling ----

function getToken() {
  return localStorage.getItem("usaged_token") || "";
}

function authHeaders() {
  var h = {};
  var t = getToken();
  if (t) h["X-Device-Token"] = t;
  return h;
}

function stripTokenFromUrl() {
  var params = new URLSearchParams(window.location.search);
  var t = params.get("token");
  if (t) {
    localStorage.setItem("usaged_token", t);
    params.delete("token");
    var u = window.location.pathname + (params.toString() ? "?" + params.toString() : "") + (window.location.hash || "");
    window.history.replaceState({}, "", u);
  }
}

// ---- State ----

var lastRev = null;       // full ETag header value, e.g. '"abcd1234"'
var lastProviderJson = {}; // id \u2192 JSON string of last-rendered provider
var lastStatsJson = null;   // JSON string of last-rendered stats report
var statsRendered = false; // true after the first successful stats paint
var currentInterval = 300; // seconds, from /v1/config
var cfg_listen_cache = "";   // listen address from last GET /v1/config
var cfg_tz_cache = "";       // tz from last GET /v1/config
var currentDeviceState = null;  // from GET /v1/device (no ETag, polled separately)
var currentSnap = null;         // latest /v1/usage snapshot (for attention re-render)
var planPresets = {};           // published plan tiers from GET /v1/config
var currentAdvise = null;       // latest /v1/advise outcome (drives the advise card)

// ---- Fetch ----

function apiFetch(url, options) {
  return fetch(url, Object.assign({ signal: AbortSignal.timeout(20000) }, options || {}));
}

function loadUsage() {
  var headers = authHeaders();
  if (lastRev) headers["If-None-Match"] = lastRev;
  return apiFetch("/v1/usage", { headers: headers }).then(function(resp) {
    if (resp.status === 401) { showTokenInput(); return null; }
    if (resp.status === 304) { setConnection("Connected", "ok"); loadHealthz(); return null; }
    if (!resp.ok) throw new Error("Usage request failed (" + resp.status + ").");
    setConnection("Connected", "ok");
    return resp.json();
  });
}

// loadDevice fetches the StickS3 device state from GET /v1/device — a dedicated
// endpoint with NO ETag, polled on its own timer (ORDER #66 task 67). Device
// state changes every second (seconds_since) and must never live inside the
// ETag-cached /v1/usage payload, which is only present on 200 responses.
function loadDevice() {
  var headers = authHeaders();
  return apiFetch("/v1/device", { headers: headers }).then(function(resp) {
    if (resp.status === 401) { showTokenInput(); return null; }
    if (resp.status === 204 || !resp.ok) return null;
    return resp.json();
  });
}

// loadAdvise fetches /v1/advise on the SAME poll cycle as /v1/usage — no second
// timer, no second fetch loop. No If-None-Match is sent: the winner can change
// as the clock crosses a reset boundary with identical provider data, so caching
// advice (a stale 304) would render a winner the endpoint no longer endorses.
function loadAdvise() {
  return apiFetch("/v1/advise", { headers: authHeaders() }).then(function(resp) {
    if (resp.status === 401) { showTokenInput(); return null; }
    if (!resp.ok) return null;
    return resp.json();
  }).catch(function() { return null; });
}

function loadStats() {
  var headers = authHeaders();
  // First-paint fix (ORDER #48 latent bug): never send If-None-Match on the
  // first load — a stale etag from a previous session would yield a 304 with
  // no body, leaving the page at placeholder zeros forever.
  if (statsRendered) {
    var prevEtag = localStorage.getItem("stats_etag");
    if (prevEtag) headers["If-None-Match"] = prevEtag;
  }
  return apiFetch("/v1/stats", { headers: headers }).then(function(resp) {
    if (resp.status === 401) { showTokenInput(); return null; }
    if (resp.status === 304)  { return null; }
    if (!resp.ok) throw new Error("Activity request failed (" + resp.status + ").");
    var etag = resp.headers.get("ETag");
    if (etag) localStorage.setItem("stats_etag", etag);
    return resp.json();
  });
}

function loadConfig() {
  var headers = authHeaders();
  return apiFetch("/v1/config", { headers: headers }).then(function(resp) {
    if (resp.status === 401) { showTokenInput(); return null; }
    if (!resp.ok) return null;
    return resp.json();
  }).then(function(cfg) {
    if (cfg && cfg.interval_sec) {
      var changed = currentInterval !== cfg.interval_sec;
      currentInterval = cfg.interval_sec;
      if (changed) reloadIntervals();
      var sel = document.getElementById("interval-select");
      if (sel) {
        for (var i = 0; i < sel.options.length; i++) {
          if (Number(sel.options[i].value) === cfg.interval_sec) {
            sel.selectedIndex = i; break;
          }
        }
      }
    }
    if (cfg) {
      cfg_listen_cache = cfg.listen || "";
      cfg_tz_cache = cfg.tz || "";
    }
    return cfg;
  });
}

function loadHealthz() {
  apiFetch("/healthz", { headers: authHeaders() })
    .then(function(r) { return r.ok ? r.json() : null; })
    .then(function(h) { if (h) updateFooterChecked(h); })
    .catch(function() { setConnection("Disconnected", "error"); });
}

function icon(name) {
  var paths = {
    grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
    activity: '<path d="M3 12h4l3-8 4 16 3-8h4"/>',
    layers: '<path d="m12 3 10 5-10 5L2 8l10-5Zm-9 9 9 5 9-5M3 16l9 5 9-5"/>',
    bell: '<path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4"/>',
    settings: '<path d="m9 3-.5 3-3 1-2-1L2 10l2.5 2L4 15l-1 2 3 3 3-1 3 2 3-2 3 1 3-3-1-3 1-3-3-2V6l-4-2-2 2-3-3Z"/><circle cx="12" cy="12" r="3"/>',
    terminal: '<path d="m6 8 4 4-4 4m7 0h5"/><rect x="2" y="3" width="20" height="18" rx="3"/>',
    device: '<rect x="6" y="2" width="12" height="20" rx="3"/><path d="M9 5h6M10 18h4"/>',
    'chevron-right': '<path d="m9 5 7 7-7 7"/>',
    'chevron-down': '<path d="m5 9 7 7 7-7"/>',
    'arrow-right': '<path d="M4 12h16m-6-6 6 6-6 6"/>',
    'arrow-up': '<path d="M12 20V4m-6 6 6-6 6 6"/>',
    'arrow-down': '<path d="M12 4v16m-6-6 6 6 6-6"/>',
    shield: '<path d="m12 3 8 3v6c0 5-8 9-8 9S4 17 4 12V6l8-3Z"/><path d="m8 12 3 3 5-6"/>',
    lock: '<rect x="5" y="10" width="14" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 5v2"/>',
    clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
    refresh: '<path d="M20 7v5h-5M4 17v-5h5M5 7a8 8 0 0 1 13-2l2 3M4 16l2 3a8 8 0 0 0 13-2"/>',
    zap: '<path d="m13 2-9 12h7l-1 8 10-12h-7l1-8Z"/>',
    coins: '<ellipse cx="10" cy="6" rx="7" ry="3"/><path d="M3 6v5c0 2 3 3 7 3s7-1 7-3V6M3 11v5c0 2 3 3 7 3m3-1c0 2 2 3 5 3s4-1 4-3v-5c0-2-2-3-4-3"/>',
    wallet: '<path d="M20 8V5a2 2 0 0 0-2-2L5 5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h15V8H5"/><path d="M20 12h-6v5h6m-3-2.5h.01"/>',
    sparkles: '<path d="m12 3 2.5 6.5L21 12l-6.5 2.5L12 21l-2.5-6.5L3 12l6.5-2.5L12 3ZM20 2v4m-2-2h4"/>',
    info: '<circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/>',
    bluetooth: '<path d="m7 7 10 10-5 5V2l5 5L7 17"/>',
    menu: '<path d="M4 6h16M4 12h16M4 18h16"/>',
    key: '<circle cx="8" cy="8" r="5"/><path d="m11.5 11.5 9 9m-3-3 3-3m-6 0 3-3"/>',
    x: '<path d="m6 6 12 12M6 18 18 6"/>',
    sun: '<path d="M12 2v20M2 12h20M5 5l14 14M5 19 19 5M8 3l8 18M3 8l18 8M3 16l18-8M8 21l8-18"/>',
    aperture: '<circle cx="12" cy="12" r="9"/><path d="m8 4 5 8m6-7-5 8m7 2H11m6 5-5-8m-6 7 5-8M3 9h10"/>',
    branch: '<path d="M3 12h7l7-7m-7 7 7 7m-4-14h5v5m-5 9h5v-5"/>',
    code: '<path d="m8 5-6 7 6 7m8-14 6 7-6 7m-3-17-2 20"/>',
    check: '<path d="m5 12 4 4L19 6"/>'
  };
  return '<svg class="icon" viewBox="0 0 24 24" aria-hidden="true">' + (paths[name] || paths.layers) + '</svg>';
}

function renderIcons() {
  document.querySelectorAll('[data-icon]').forEach(function(el) { el.innerHTML = icon(el.getAttribute('data-icon')); });
}

function providerMark(id) {
  var kind = id === 'claude' ? 'claude' : id === 'codex' ? 'codex' : id.indexOf('openrouter') === 0 ? 'router' : id === 'groq' ? 'groq' : 'code';
  var symbol = { claude: 'sun', codex: 'aperture', router: 'branch', groq: 'zap', code: 'code' }[kind];
  return '<span class="provider-mark mark-' + kind + '">' + icon(symbol) + '</span>';
}

function setConnection(label, state) {
  var el = document.getElementById('connection-status');
  el.dataset.state = state;
  document.getElementById('connection-text').textContent = document.documentElement.dataset.preview ? 'Preview mode' : label;
}

var toastTimer;
function notify(message) {
  var el = document.getElementById('toast');
  el.textContent = message;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(function() { el.hidden = true; }, 4200);
}

