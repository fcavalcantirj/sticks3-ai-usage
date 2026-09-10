package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAssemblyIsSelfContained(t *testing.T) {
	html := string(IndexHTML)
	for _, marker := range []string{"/* {{styles}} */", "/* {{scripts}} */", "__PREVIEW_DATA__", "preview/adapter", "node_modules"} {
		if strings.Contains(html, marker) {
			t.Errorf("production dashboard contains unassembled or preview content: %q", marker)
		}
	}
	if strings.Count(html, "<script>") != 1 || strings.Count(html, "</script>") != 1 {
		t.Error("dashboard must ship exactly one self-contained script")
	}
	for _, name := range []string{"core.js", "settings.js", "device.js", "render.js", "app.js", "base.css", "views.css"} {
		b, err := assets.ReadFile(name)
		if err != nil || len(b) == 0 {
			t.Errorf("missing embedded asset: %s", name)
		}
		if strings.Count(string(b), "\n") > 900 {
			t.Errorf("frontend asset exceeds 900-line ceiling: %s", name)
		}
	}
}

func TestAccessibleDialogsAndPanels(t *testing.T) {
	html := string(IndexHTML)
	for _, required := range []string{`<dialog id="key-dialog"`, `id="key-value"`, `role="tabpanel"`, `aria-expanded="false"`, `prefers-reduced-motion`, `aria-label="Refresh interval"`} {
		if !strings.Contains(html, required) {
			t.Errorf("missing accessible control: %s", required)
		}
	}
	for _, banned := range []string{"prompt(", "alert(", "confirm("} {
		if strings.Contains(html, banned) {
			t.Errorf("blocking native dialog remains: %s", banned)
		}
	}
}

// TestIndexHTMLContent verifies the embedded dashboard HTML contains the
// strings the spec requires for downstream consumers (page title, API
// endpoints, auth header name) and is non-empty.
func TestIndexHTMLContent(t *testing.T) {
	if len(IndexHTML) == 0 {
		t.Fatal("embedded index.html is empty")
	}
	html := string(IndexHTML)
	for _, want := range []string{
		"<title>ai-usage</title>",
		"/v1/usage",
		"/v1/stats",
		"If-None-Match",
		"X-Device-Token",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q", want)
		}
	}
}

// TestIndexHTMLStatsIds verifies the page contains the element IDs required
// by the stats dashboard polish spec (task 45).
func TestIndexHTMLStatsIds(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		"id=\"stat-pills\"",
		"id=\"heatmap-claude_code\"",
		"id=\"heatmap-codex\"",
		"id=\"models-table\"",
		"id=\"attention\"",
		"id=\"pill-month-cost\"",
		"id=\"pill-month-ratio\"",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing element %q", want)
		}
	}
}

// TestIndexHTMLTabs verifies the tab strip (task 53 ORDER #48): four tabs with
// proper role/aria attributes so keyboard navigation works and no page scroll
// occurs at 1280x800 or 390x844.
func TestIndexHTMLTabs(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`role="tablist"`,
		`role="tab"`,
		`aria-selected="true"`,
		`id="tab-quota"`,
		`id="tab-activity"`,
		`id="tab-models"`,
		`id="tab-attention"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing tab element %q", want)
		}
	}
}

// TestIndexHTMLRefreshInHeader verifies the refresh button is in the header,
// not in the footer (ORDER #48).
func TestIndexHTMLRefreshInHeader(t *testing.T) {
	html := string(IndexHTML)
	refreshIdx := strings.Index(html, `id="refresh-btn"`)
	if refreshIdx < 0 {
		t.Fatal(`index.html missing id="refresh-btn"`)
	}
	footerIdx := strings.Index(html, `class="footer-bar"`)
	if footerIdx < 0 {
		t.Fatal(`index.html missing class="footer-bar"`)
	}
	if refreshIdx > footerIdx {
		t.Error(`refresh button appears AFTER footer-bar — it must be in the header`)
	}
}

// TestIndexHTMLIntervalSelect verifies the interval selector options match
// ORDER #48: 5/10/15/30/60 minutes (300/600/900/1800/3600 seconds).
func TestIndexHTMLIntervalSelect(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, `id="interval-select"`) {
		t.Fatal(`index.html missing id="interval-select"`)
	}
	for _, want := range []string{"300", "600", "900", "1800", "3600"} {
		if !strings.Contains(html, `value="`+want+`"`) {
			t.Errorf(`index.html missing interval option value %q`, want)
		}
	}
}

// TestIndexHTMLStatsFirstPaintFix verifies the 304-first-paint fix (ORDER #48):
// the page must not send If-None-Match on the first stats load.
func TestIndexHTMLStatsFirstPaintFix(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, "statsRendered") {
		t.Error("index.html missing statsRendered flag for first-paint fix")
	}
	// The fix: If-None-Match is only sent AFTER the first paint succeeds.
	if !strings.Contains(html, "First-paint fix") {
		t.Error("index.html missing First-paint fix comment")
	}
}

// TestIndexHTMLSettingsTab verifies the Settings tab exists with the
// config form elements (task 54). The interval control lives in the
// header (ORDER #65 task 68), so it must NOT appear in the settings form.
func TestIndexHTMLSettingsTab(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, `id="tab-settings-tab"`) {
		t.Fatal(`index.html missing id="tab-settings-tab"`)
	}
	if !strings.Contains(html, `id="tab-settings"`) {
		t.Fatal(`index.html missing id="tab-settings"`)
	}
	// Settings form elements (interval is in the header, not here).
	for _, want := range []string{
		`id="setting-openrouter-low"`,
		`id="setting-quota-warn-5h"`,
		`id="setting-quota-warn-weekly"`,
		`id="save-config-btn"`,
		`id="settings-form"`,
		`id="provider-list"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing settings element %q", want)
		}
	}
	// The settings tab must appear in the tab strip.
	settingsTabIdx := strings.Index(html, `id="tab-settings-tab"`)
	footerIdx := strings.Index(html, `class="footer-bar"`)
	if settingsTabIdx < 0 || footerIdx < 0 {
		t.Fatal("missing tab or footer markers")
	}
	if settingsTabIdx > footerIdx {
		t.Error("settings tab should appear before footer in markup")
	}

	// Task 102: Settings copy must explain that plan providers are ordered by
	// recommendation score, not by drag-to-reorder.
	for _, want := range []string{
		`Plan providers are always ordered by the current usage recommendation`,
		`Drag-to-reorder only applies to credit and free providers`,
		`Ordered by recommendation score`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing plan-ordering hint %q", want)
		}
	}
}

// TestIndexHTMLNoSettingsIntervalSelect verifies the duplicate interval
// control was removed from Settings (ORDER #65 task 68). The settings
// markup must contain NO interval select; the header must still have one.
func TestIndexHTMLNoSettingsIntervalSelect(t *testing.T) {
	html := string(IndexHTML)
	// setting-interval must be entirely absent from the markup.
	if strings.Contains(html, `setting-interval`) {
		t.Error(`index.html still contains id="setting-interval" — the duplicate interval control must be removed from Settings`)
	}
	// The header interval select must still exist — it is the single control.
	if !strings.Contains(html, `id="interval-select"`) {
		t.Error(`index.html missing id="interval-select" in header — the single interval control must remain`)
	}
}

// TestIndexHTMLProviderCards verifies the settings page renders a card per
// provider with enabled toggle, label, key state, and (for claude/codex) a
// plan block with cost fields.
func TestIndexHTMLProviderCards(t *testing.T) {
	html := string(IndexHTML)
	// The renderer builds provider cards dynamically in createProviderCard(),
	// so we check for the template structure and data-field attributes it emits.
	for _, want := range []string{
		`data-field="enabled"`,
		`data-field="label"`,
		`data-field="plan.cost"`,
		`data-field="plan.currency"`,
		`data-field="plan.label"`,
		`data-field="plan.cost_usd"`,
		`/v1/keys`,
		`createProviderCard`,
		`provider-card`,
		`Set key`,
		`Remove key`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing provider card element %q", want)
		}
	}
}

// TestIndexHTMLSettingsDarkTheme verifies the settings inputs use the dark
// palette classes (no default browser chrome).
func TestIndexHTMLSettingsDarkTheme(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`var(--bg)`,
		`var(--border)`,
		`var(--card)`,
		`var(--text)`,
		`var(--muted)`,
		`var(--ok)`,
		`var(--warn)`,
		`var(--crit)`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html settings CSS missing palette variable %q", want)
		}
	}
}

// TestIndexHTMLKeyManagement verifies the key endpoints and Keychain-related
// JS are present and the key never appears in a config GET response.
func TestIndexHTMLKeyManagement(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`POST`, `/v1/keys`,
		`DELETE`, `/v1/keys`,
		`setKeyPrompt`,
		`removeKey`,
		`Keychain`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing key management element %q", want)
		}
	}
}

// TestIndexHTMLSettingsValidation verifies inline validation that names the
// bad field and save feedback states.
func TestIndexHTMLSettingsValidation(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`settings-error`,
		`showSettingsStatus`,
		`Invalid field`,
		`Save settings`,
		`Saving…`,
		`Saved`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing validation element %q", want)
		}
	}
}

// TestIndexHTMLCanProbe verifies the settings page gates the probe toggle on
// the provider capability field can_probe (ORDER #54 task 59), not the
// parse-time has_probe flag.
func TestIndexHTMLCanProbe(t *testing.T) {
	html := string(IndexHTML)
	if !strings.Contains(html, "can_probe") {
		t.Error(`index.html missing can_probe field reference for probe toggle gating`)
	}
	if strings.Contains(html, "p.has_probe") {
		t.Error(`index.html still uses p.has_probe for probe toggle gating, should use p.can_probe`)
	}
	if !strings.Contains(html, "p.can_probe") {
		t.Error(`index.html missing p.can_probe reference`)
	}
}

// TestIndexHTMLSetupCard verifies the Settings tab carries the one-click BLE
// setup control: a scan button, a place for what the scan found, a set-up
// button, and the live step list — plus the three endpoints behind them.
//
// This is the zero-typing path. The owner clicks once and the daemon sends the
// SSID, the Wi-Fi password, its own address, its port and a freshly minted
// token over a bonded BLE link. Nothing on this card is a credential field.
func TestIndexHTMLSetupCard(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`id="setup-card"`,
		`id="setup-scan-btn"`,
		`id="setup-status"`,
		`id="setup-found"`,
		`id="setup-provision-btn"`,
		`id="setup-steps"`,
		`"/v1/setup/scan"`,
		`"/v1/setup/provision"`,
		`"/v1/setup"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing setup element %q", want)
		}
	}
}

// TestIndexHTMLSetupNeverRendersACredential is the page-side half of the rule
// the whole design rests on: the daemon holds the Wi-Fi password and the device
// token, and NEITHER is ever shown, typed or echoed here. The six passkey
// digits live on the DEVICE screen — that is what makes typing them into the
// macOS dialog proof of physical possession — so the card must not render a
// passkey either.
//
// A previous agent left a credential input in this page and the work had to be
// reverted rather than committed. This test is why that cannot recur silently.
func TestIndexHTMLSetupNeverRendersACredential(t *testing.T) {
	html := string(IndexHTML)
	// The setup card must contribute no password/token input of its own.
	for _, banned := range []string{
		`id="setup-pass-field"`,
		`id="setup-token-field"`,
		`id="setup-ssid-field"`,
		`id="setup-passkey"`,
		`setupJs.password`,
		`run.password`,
		`run.token`,
		`run.passkey`,
		`scan.password`,
		`d.token`,
	} {
		if strings.Contains(html, banned) {
			t.Errorf("index.html setup card renders or accepts a credential: %q", banned)
		}
	}
}

// TestIndexHTMLJavaScriptParses actually PARSES the page's script instead of
// grepping it for strings.
//
// Every other test in this file checks that some id or endpoint appears
// somewhere in the file. None of them would notice that the script does not
// run at all — and on 2026-09-07 one shipped that way: a regex-driven removal
// of a dead card left an orphaned block behind, and the whole dashboard died on
//
//	Uncaught SyntaxError: Unexpected token ')'
//
// with every string assertion still passing. Presence is not validity.
//
// The check is skipped where node is unavailable rather than failing, so this
// never blocks a machine without it; the point is that it fails loudly where it
// can run, which includes here.
func TestIndexHTMLJavaScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; cannot parse-check the dashboard script")
	}

	html := string(IndexHTML)
	start := strings.Index(html, "<script>")
	end := strings.LastIndex(html, "</script>")
	if start < 0 || end < 0 || end <= start {
		t.Fatal("index.html has no <script> block to check")
	}
	js := html[start+len("<script>") : end]

	dir := t.TempDir()
	path := filepath.Join(dir, "dashboard.js")
	if err := os.WriteFile(path, []byte(js), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	out, err := exec.Command(node, "--check", path).CombinedOutput()
	if err != nil {
		t.Fatalf("the dashboard script does not parse — the page would be dead:\n%s", out)
	}
}

// TestIndexHTMLAdviseCard verifies the dashboard "use this next" card renders
// the /v1/advise winner and the per-plan table (task 97). The daemon is the
// single source of truth — the card only names what the endpoint returned.
func TestIndexHTMLAdviseCard(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`id="advise-card"`,
		`/v1/advise`,
		`loadAdvise`,
		`renderAdvise`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing advise element %q", want)
		}
	}
	// The card must sit above the provider grid it annotates.
	cardIdx := strings.Index(html, `id="advise-card"`)
	cardsIdx := strings.Index(html, `id="cards"`)
	if cardIdx < 0 || cardsIdx < 0 {
		t.Fatal("missing advise-card or #cards marker")
	}
	if cardIdx > cardsIdx {
		t.Error(`advise-card renders after #cards — it must be above the provider grid`)
	}
	// Guard: a provider-id typo ("opencache" for "opencode") is silent data loss on
	// a 240x135 screen — room message 36 flagged one. The card must only ever print
	// an id the endpoint named, so the typo is banned from the markup entirely.
	if strings.Contains(html, "opencache") {
		t.Error(`index.html contains "opencache" — provider ids must come from the endpoint, never be retyped`)
	}
}
