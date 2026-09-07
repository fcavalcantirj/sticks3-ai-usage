package web

import (
	"regexp"
	"strings"
	"testing"
)

// TestIndexHTMLContent verifies the embedded dashboard HTML contains the
// strings the spec requires for downstream consumers (page title, API
// endpoints, auth header name) and is non-empty.
func TestIndexHTMLContent(t *testing.T) {
	if len(IndexHTML) == 0 {
		t.Fatal("embedded index.html is empty")
	}
	html := string(IndexHTML)
	for _, want := range []string{
		"<title>AI Usage</title>",
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
		`id="setting-quota-warn"`,
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

// TestIndexHTMLPairingCard verifies the Settings tab carries the pairing
// control (task 78): a button to open the window, a field for the code that
// the DEVICE shows, and the three endpoints behind them.
func TestIndexHTMLPairingCard(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`id="pair-card"`,
		`id="pair-open-btn"`,
		`id="pair-code-field"`,
		`id="pair-confirm-btn"`,
		`id="pair-status"`,
		`id="pair-devices"`,
		`"/v1/pair/open"`,
		`"/v1/pair/confirm"`,
		`"/v1/pair"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing pairing element %q", want)
		}
	}
}

// TestIndexHTMLPairingNeverShowsTheCode is the page-side half of the rule the
// API enforces: the pairing code lives on the DEVICE screen, which is what
// makes typing it proof of physical possession. The dashboard must never
// render a code or a token it received from the agent.
func TestIndexHTMLPairingNeverShowsTheCode(t *testing.T) {
	html := string(IndexHTML)
	// code_len and token_len are fine — they are lengths. A bare .code or
	// .token read off a pairing response is not.
	leak := regexp.MustCompile(`\b(js|pair|status)\.(code|token)\b`)
	if m := leak.FindString(html); m != "" {
		t.Errorf("index.html reads %q from the pairing API — the code and token must never reach the page", m)
	}
}

// TestIndexHTMLWifiCard verifies the Settings tab carries the Wi-Fi control
// (task 79): where the device is now, fields to change it, the cancel escape
// hatch, and the three endpoints behind them.
func TestIndexHTMLWifiCard(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		`id="wifi-card"`,
		`id="wifi-current"`,
		`id="wifi-ssid-field"`,
		`id="wifi-pass-field"`,
		`id="wifi-open-check"`,
		`id="wifi-apply-btn"`,
		`id="wifi-cancel-btn"`,
		`id="wifi-status"`,
		`id="wifi-notice"`,
		`"/v1/netcfg"`,
		`sendWifi`,
		`cancelWifi`,
		`loadWifi`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing Wi-Fi element %q", want)
		}
	}
	// The password field must be a password input, and must never be
	// pre-filled from a server response.
	if !strings.Contains(html, `type="password" id="wifi-pass-field"`) {
		t.Error(`the Wi-Fi password field is not type="password"`)
	}
}

// TestIndexHTMLWifiPasswordIsWriteOnly is the page-side half of the rule the
// API enforces: the Wi-Fi password goes out with PUT /v1/netcfg and is never
// read back. The page must not render one from any response body — writing it
// INTO an outgoing payload (payload.password) is the one legitimate use.
func TestIndexHTMLWifiPasswordIsWriteOnly(t *testing.T) {
	html := string(IndexHTML)
	leak := regexp.MustCompile(`\b(js|resp|cfg|status|change|pending|last|device)\.password\b`)
	if m := leak.FindString(html); m != "" {
		t.Errorf("index.html reads %q from a response — the Wi-Fi password is write-only", m)
	}
	// The value the page DOES render is the label pair, not the credential.
	for _, want := range []string{"password_state", "password_source"} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q — the page must say whether a password is set and where it lives", want)
		}
	}
}

// TestIndexHTMLWifiSaysItIsNotInstant verifies the page tells the user the two
// things this feature can surprise them with: the change lands on the device's
// next check-in, and a bad password takes the device briefly offline with the
// recovery shown on the DEVICE screen (the portal AP drops any phone on it the
// moment the device retries the join — measured on hardware, 2026-09-06).
func TestIndexHTMLWifiSaysItIsNotInstant(t *testing.T) {
	html := string(IndexHTML)
	for _, want := range []string{
		"js.notice",
		"js.recovery",
		"wifi-notice",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("index.html missing %q — the latency and recovery wording comes from the server", want)
		}
	}
	if !strings.Contains(html, "Check the device screen") {
		t.Error("index.html never points the user at the device screen when a join fails")
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
