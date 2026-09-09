// ---- One-click setup over Bluetooth ----
//
// The card is a thin renderer over GET /v1/setup. Every sentence the owner
// reads — the headline, the step text, the failure reason and what to do next
// — is CHOSEN BY THE AGENT, not composed here, so no implementation string can
// reach the page. That is the same rule the setup card above follows, and it is why
// this code never builds an error message out of a response field it did not
// expect.
//
// It polls only while something is happening. An idle card costs one request
// when the Settings tab is opened and nothing after that.

var setupOfferAddr = null;   // the device the button would set up
var setupTimer = null;       // poll handle; non-null means polling
var setupPrevRun = null;     // previous run.state, to catch the edge into done

function setupPolling(on) {
  if (on && !setupTimer) {
    setupTimer = setInterval(refreshSetup, 2000);
  } else if (!on && setupTimer) {
    clearInterval(setupTimer);
    setupTimer = null;
  }
}

function refreshSetup() {
  apiFetch("/v1/setup", { headers: authHeaders() })
    .then(function(r) { if (r.status === 401) { showTokenInput(); return null; } return r.ok ? r.json() : null; })
    .then(function(js) { if (js) renderSetup(js); else setupUnavailable(); })
    .catch(setupUnavailable);
}

function renderSetup(js) {
  var status = document.getElementById("setup-status");
  var scanBtn = document.getElementById("setup-scan-btn");
  var provBtn = document.getElementById("setup-provision-btn");
  var stopBtn = document.getElementById("setup-stop-btn");

  if (!js.available) {
    // No BLE central in this build, or none on this OS. Say so and stop:
    // a card that offers a button which always fails is worse than one that
    // explains itself.
    status.textContent = js.headline || "Bluetooth setup is not available.";
    status.style.color = "var(--muted)";
    scanBtn.disabled = true;
    provBtn.disabled = true;
    stopBtn.style.display = 'none';
    setupOfferAddr = null;
    document.getElementById('setup-found').textContent = '';
    document.getElementById('setup-steps').textContent = '';
    setupPolling(false);
    return;
  }

  status.textContent = js.headline || "";
  status.style.color = "var(--muted)";

  var run = js.run;
  var running = run && run.state === "running";
  var scanning = js.state === "scanning";

  scanBtn.disabled = running || scanning;
  scanBtn.textContent = scanning ? "Looking\u2026" : "Look for a device";
  stopBtn.style.display = running ? "" : "none";

  // The offer is the agent's own pick of what to set up. The page never
  // chooses for itself, so a device the agent did not offer cannot be
  // provisioned by clicking.
  setupOfferAddr = js.offer ? js.offer.addr : null;
  provBtn.disabled = running || !setupOfferAddr;
  provBtn.textContent = js.offer ? "Set up " + js.offer.name : "Set it up";

  renderSetupFound(js.scan);
  renderSetupSteps(run);

  // Poll while anything is in flight, and for one pass after it ends so the
  // final message lands.
  var active = running || scanning;
  setupPolling(active);
  if (!active && setupPrevRun === "running") {
    refreshSetup();
  }
  setupPrevRun = run ? run.state : null;
}

function renderSetupFound(scan) {
  var el = document.getElementById("setup-found");
  if (!scan) { el.textContent = ""; return; }
  if (scan.error) {
    // Agent-chosen sentence, plus the agent's own "what to do next".
    el.textContent = scan.error + (scan.next ? " " + scan.next : "");
    el.style.color = "var(--warn)";
    return;
  }
  el.style.color = "var(--muted)";
  if (!scan.found || !scan.found.length) {
    el.textContent = "";
    return;
  }
  var parts = scan.found.map(function(d) {
    // rssi is the only number worth showing: it tells the owner WHICH stick on
    // the desk this is. Nothing else here identifies anything secret.
    return esc(d.name || d.addr) + (d.provisioned ? " (already set up)" : "") +
           " \u00b7 " + d.rssi + " dBm";
  });
  el.innerHTML = "Found: " + parts.join(" \u00b7 ");
}

function renderSetupSteps(run) {
  var el = document.getElementById("setup-steps");
  if (!run) { el.textContent = ""; return; }

  if (run.state === "failed") {
    el.textContent = (run.error || "Setup did not finish.") + (run.next ? " " + run.next : "");
    el.style.color = "var(--crit)";
    return;
  }
  if (run.state === "applied") {
    el.textContent = run.message || "Done.";
    el.style.color = "var(--ok)";
    return;
  }
  el.style.color = "var(--muted)";
  // While running, show the step list the agent built. Each entry's text is a
  // fixed sentence from the agent's own vocabulary.
  var steps = (run.steps || []).map(function(s) { return esc(s.text); });
  el.innerHTML = steps.length ? steps.join("<br>") : esc(run.message || "");
}

function startSetupScan() {
  apiFetch("/v1/setup/scan", { method: "POST", headers: authHeaders() })
    .then(function(r) { if (r.status === 401) { showTokenInput(); return null; } return r.ok ? r.json() : null; })
    .then(function(js) { if (js) renderSetup(js); else setupUnavailable(); })
    .catch(setupUnavailable);
}

function startSetupProvision() {
  if (!setupOfferAddr) return;
  var headers = authHeaders();
  headers["Content-Type"] = "application/json";
  apiFetch("/v1/setup/provision", {
    method: "POST",
    headers: headers,
    body: JSON.stringify({ addr: setupOfferAddr })
  })
    .then(function(r) { if (r.status === 401) { showTokenInput(); return null; } return r.ok ? r.json() : null; })
    .then(function(js) { if (js) renderSetup(js); else setupUnavailable(); })
    .catch(setupUnavailable);
}

function stopSetup() {
  apiFetch("/v1/setup", { method: "DELETE", headers: authHeaders() })
    .then(function(r) { return r.ok ? r.json() : null; })
    .then(function(js) { if (js) renderSetup(js); else setupUnavailable(); })
    .catch(setupUnavailable);
}

function setupUnavailable() {
  setupPolling(false);
  setupOfferAddr = null;
  document.getElementById('setup-status').textContent = 'Could not reach device setup. Reopen Settings to retry.';
  document.getElementById('setup-scan-btn').disabled = true;
  document.getElementById('setup-provision-btn').disabled = true;
  document.getElementById('setup-stop-btn').style.display = 'none';
}

// ---- One-click setup over Bluetooth ends ----

