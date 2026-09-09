// ---- Settings rendering ----

function renderSettings(cfg) {
  var form = document.getElementById("settings-form");
  if (!form) return;
  form.style.display = "block";
  document.getElementById('settings-loading').hidden = true;
  settingsLoaded = true;

  // Published list prices, shipped with the binary — same for every install.
  planPresets = cfg.plan_presets || {};

  // The interval select lives in the header (single control — ORDER #65 task 68).
  // Keep the header select in sync with the persisted value.
  var intervalVal = cfg.interval_sec || 900;
  var headerSel = document.getElementById("interval-select");
  if (headerSel) {
    for (var j = 0; j < headerSel.options.length; j++) {
      if (Number(headerSel.options[j].value) === intervalVal) {
        headerSel.selectedIndex = j; break;
      }
    }
  }

  // Populate alerts.
  if (cfg.alerts) {
    if (cfg.alerts.openrouter_low_usd != null) {
      document.getElementById("setting-openrouter-low").value = cfg.alerts.openrouter_low_usd;
    }
    if (cfg.alerts.quota_warn_5h_pct != null) {
      document.getElementById("setting-quota-warn-5h").value = cfg.alerts.quota_warn_5h_pct;
    }
    if (cfg.alerts.quota_warn_weekly_pct != null) {
      document.getElementById("setting-quota-warn-weekly").value = cfg.alerts.quota_warn_weekly_pct;
    }
  }

  // Render provider cards.
  var list = document.getElementById("provider-list");
  list.innerHTML = "";
  var errEl = document.getElementById("settings-error");
  errEl.style.display = "none";
  errEl.textContent = "";

  if (!cfg.providers || cfg.providers.length === 0) {
    list.innerHTML = '<div class="key-state" style="color:var(--muted);">No providers configured</div>';
    return;
  }

  // The server already returns providers in the configured order, but be
  // explicit: if provider_order is present, sort by it so a stale response
  // never renders in the wrong position.
  var order = cfg.provider_order || [];
  var provs = cfg.providers || [];
  if (order.length > 0) {
    var rank = {};
    for (var oi = 0; oi < order.length; oi++) rank[order[oi]] = oi;
    provs = provs.slice().sort(function(a, b) {
      var ra = rank[a.id] !== undefined ? rank[a.id] : order.length;
      var rb = rank[b.id] !== undefined ? rank[b.id] : order.length;
      return ra - rb;
    });
  }
  provs.forEach(function(p) {
    list.appendChild(createProviderCard(p));
  });
}

// moveCardUp moves a provider card up by one position in the DOM.
function moveCardUp(card) {
  var prev = card.previousElementSibling;
  if (prev && prev.classList.contains("provider-card")) {
    card.parentNode.insertBefore(card, prev);
    markSettingsDirty();
  }
}

// moveCardDown moves a provider card down by one position in the DOM.
function moveCardDown(card) {
  var next = card.nextElementSibling;
  if (next && next.classList.contains("provider-card")) {
    card.parentNode.insertBefore(next, card);
    markSettingsDirty();
  }
}

function createProviderCard(p) {
  var card = document.createElement("div");
  card.className = "provider-card";
  card.setAttribute("data-pid", p.id);

  // Header — always visible, acts as the collapse toggle.
  var header = document.createElement("div");
  header.className = "provider-card-header";
  header.innerHTML = providerMark(p.id);

  // Enabled toggle
  var toggleWrap = document.createElement("label");
  toggleWrap.className = "toggle-label";
  toggleWrap.innerHTML =
    '<span class="toggle"><input type="checkbox" data-field="enabled" ' +
    (p.enabled ? "checked" : "") + '><span class="toggle-slider"></span></span>' +
    '<span class="toggle-caption">Enable ' + esc(p.label || p.id) + '</span>';
  toggleWrap.querySelector('input').setAttribute('aria-label', 'Enable ' + (p.label || p.id));
  // Header click toggles the card (not the checkbox).
  toggleWrap.onclick = function(e) { e.stopPropagation(); };
  header.appendChild(toggleWrap);

  // Label input
  var labelInput = document.createElement("input");
  labelInput.type = "text";
  labelInput.className = "provider-label";
  labelInput.setAttribute("data-field", "label");
  labelInput.value = p.label || p.id;
  labelInput.setAttribute('aria-label', (p.label || p.id) + ' display name');
  labelInput.required = true;
  header.appendChild(labelInput);

  // Key state summary (uses key_source for the real source label, ORDER #66 task 67c)
  var keySummary = document.createElement("span");
  keySummary.className = "key-state";
  keySummary.innerHTML = keyStateLabel(p);
  header.appendChild(keySummary);

  // Collapse chevron
  var chevron = document.createElement('button');
  chevron.className = 'card-chevron';
  chevron.innerHTML = icon('chevron-down');
  chevron.setAttribute('aria-label', 'Configure ' + (p.label || p.id));
  chevron.setAttribute('aria-expanded', 'false');
  chevron.setAttribute('aria-controls', 'provider-settings-' + p.id);
  chevron.onclick = function() { toggleProviderCard(card); };
  header.appendChild(chevron);

  // Up/down buttons for reordering provider display order
  var moveUpBtn = document.createElement("button");
  moveUpBtn.className = "btn-small";
  moveUpBtn.innerHTML = icon('arrow-up');
  moveUpBtn.setAttribute('aria-label', 'Move ' + (p.label || p.id) + ' up');
  moveUpBtn.setAttribute("title", "Move up");
  moveUpBtn.onclick = function(e) { e.stopPropagation(); moveCardUp(card); };
  // Task 102: plan providers are ranked by the daemon, not by user drag. Disable
  // the up/down buttons on plan cards so the UI never offers a no-op control.
  if (p.plan) { moveUpBtn.disabled = true; moveUpBtn.title = "Ordered by recommendation score"; }
  header.appendChild(moveUpBtn);

  var moveDownBtn = document.createElement("button");
  moveDownBtn.className = "btn-small";
  moveDownBtn.innerHTML = icon('arrow-down');
  moveDownBtn.setAttribute('aria-label', 'Move ' + (p.label || p.id) + ' down');
  moveDownBtn.setAttribute("title", "Move down");
  moveDownBtn.onclick = function(e) { e.stopPropagation(); moveCardDown(card); };
  if (p.plan) { moveDownBtn.disabled = true; moveDownBtn.title = "Ordered by recommendation score"; }
  header.appendChild(moveDownBtn);

  card.appendChild(header);

  // Collapsible body — starts hidden (ORDER #66 task 67b: no page scroll).
  var body = document.createElement("div");
  body.className = "provider-card-body";
  body.id = 'provider-settings-' + p.id;
  var sourceDescription = document.createElement('p');
  sourceDescription.className = 'provider-source';
  sourceDescription.innerHTML = keyStateLabel(p);
  body.appendChild(sourceDescription);

  // Key state row with Set/Remove buttons (only for providers with key_env)
  var keyEnv = p.key_env || "";
  if (p.key_state === "set" || keyEnv) {
    var keyRow = document.createElement("div");
    keyRow.className = "key-state-row";

    if (keyEnv) {
      var setBtn = document.createElement("button");
      setBtn.className = "btn-small";
      setBtn.textContent = "Set key";
      setBtn.setAttribute("data-pid", p.id);
      setBtn.setAttribute("data-action", "set-key");
      setBtn.onclick = function(e) { e.stopPropagation(); setKeyPrompt(p.id); };
      keyRow.appendChild(setBtn);

      var rmBtn = document.createElement("button");
      rmBtn.className = "btn-small";
      rmBtn.textContent = "Remove key";
      rmBtn.setAttribute("data-action", "remove-key");
      rmBtn.onclick = function(e) { e.stopPropagation(); removeKey(p.id); };
      keyRow.appendChild(rmBtn);
    }
    body.appendChild(keyRow);
  }

  // Probe toggle: gated on provider capability (can_probe), not parse-time flag.
  if (p.can_probe) {
    var probeRow = document.createElement("div");
    probeRow.className = "key-state-row";
    var probeLabel = document.createElement("label");
    probeLabel.className = "toggle-label";
    probeLabel.innerHTML =
      '<span class="toggle"><input type="checkbox" data-field="probe" ' +
      (p.probe ? "checked" : "") + '><span class="toggle-slider"></span></span>' +
      '<span style="font-size:14px;">Groq probe</span>';
    probeLabel.onclick = function(e) { e.stopPropagation(); };
    probeRow.appendChild(probeLabel);
    body.appendChild(probeRow);
  }

  // Plan block for claude/codex
  if (p.plan) {
    var planBlock = document.createElement("div");
    planBlock.className = "plan-block";

    var planTitle = document.createElement("div");
    planTitle.className = "card-title";
    planTitle.textContent = "Subscription plan";
    planTitle.style.fontSize = "14px";
    planTitle.style.marginBottom = "8px";
    planBlock.appendChild(planTitle);

    // The published tiers for this provider, so nobody has to type their own
    // bill. Picking one fills the four fields below; they stay editable for
    // legacy pricing, an enterprise agreement, or a currency we do not list.
    var presets = (planPresets && planPresets[p.id]) || [];
    if (presets.length) {
      planBlock.appendChild(makePlanPicker(presets, p.plan));
    }

    planBlock.appendChild(makePlanRow("cost", "Cost", "number", p.plan.cost || "", "60px"));
    planBlock.appendChild(makePlanRow("currency", "Currency", "text", p.plan.currency || "USD", "70px"));
    planBlock.appendChild(makePlanRow("label", "Label", "text", p.plan.label || "", "80px"));
    planBlock.appendChild(makePlanRow("cost_usd", "USD equiv", "number", p.plan.cost_usd || "", "90px"));

    body.appendChild(planBlock);
  }

  card.appendChild(body);
  return card;
}

// keyStateLabel renders the key state using the REAL source (ORDER #66 task 67c).
// env-var providers show "via env VAR", OAuth providers show their actual
// credential location (Claude Code keyring, ~/.codex/auth.json), and only
// usaged's own Keychain entry says "in Keychain".
function keyStateLabel(p) {
  var ks = p.key_state || "not_set";
  var src = p.key_source || "none";
  var keyEnv = p.key_env || "";

  if (ks === "set") {
    if (src && src.indexOf("env:") === 0) {
      return '<span class="key-state-set">Key set</span> via env <span class="env-name">' + esc(src.substring(4)) + '</span>';
    }
    if (src === "claude-code") {
      return '<span class="key-state-set">Key set</span> via Claude Code';
    }
    if (src === "codex") {
      return '<span class="key-state-set">Key set</span> via ~/.codex/auth.json';
    }
    if (src === "keychain") {
      return '<span class="key-state-set">Key set</span> in Keychain';
    }
    // Fallback for any other "set" source
    return '<span class="key-state-set">Key set</span>';
  }

  // key_state === "not_set"
  if (keyEnv) {
    return 'Key in env <span class="env-name">' + esc(keyEnv) + '</span> (not set)';
  }
  if (src === "claude-code" || src === "codex") {
    return '<span style="color:var(--warn);">No key set</span>';
  }
  return '<span style="color:var(--warn);">No key set</span>';
}

// toggleProviderCard expands/collapses a provider card body. Only one card
// is open at a time (ORDER #66 task 67b).
function toggleProviderCard(card) {
  var isOpen = card.classList.contains("expanded");
  // Close all cards.
  var cards = document.querySelectorAll(".provider-card");
  cards.forEach(function(c) {
    c.classList.remove('expanded');
    c.querySelector('.card-chevron').setAttribute('aria-expanded', 'false');
  });
  if (!isOpen) {
    card.classList.add('expanded');
    card.querySelector('.card-chevron').setAttribute('aria-expanded', 'true');
  }
}

function makePlanPicker(presets, current) {
  var row = document.createElement("div");
  row.className = "plan-row";
  var lbl = document.createElement("span");
  lbl.className = "plan-label";
  lbl.textContent = "Plan";
  lbl.style.minWidth = "60px";

  var sel = document.createElement("select");
  sel.className = "plan-input";
  sel.setAttribute('aria-label', 'Subscription plan preset');

  // "Custom" is first and is what a plan we do not publish selects to. It is
  // never auto-applied: choosing it changes nothing, it just says the fields
  // below are the owner's own numbers.
  var custom = document.createElement("option");
  custom.value = "";
  custom.textContent = "Custom";
  sel.appendChild(custom);

  presets.forEach(function(pl, i) {
    var o = document.createElement("option");
    o.value = String(i);
    o.textContent = pl.label + " \u2014 " + fmtPlanPrice(pl);
    sel.appendChild(o);
    // Match on the numbers, not the label: a plan whose cost and currency
    // equal a published tier IS that tier, whatever it has been renamed to.
    if (current && Number(current.cost) === Number(pl.cost) &&
        (current.currency || "") === pl.currency) {
      sel.value = String(i);
    }
  });

  sel.addEventListener("change", function() {
    if (sel.value === "") return;
    var pl = presets[parseInt(sel.value, 10)];
    if (!pl) return;
    var block = sel.closest(".plan-block") || sel.parentNode.parentNode;
    setPlanField(block, "cost", pl.cost);
    setPlanField(block, "currency", pl.currency);
    setPlanField(block, "label", pl.label);
    setPlanField(block, "cost_usd", pl.cost_usd === undefined ? "" : pl.cost_usd);
  });

  row.appendChild(lbl);
  row.appendChild(sel);
  return row;
}

function setPlanField(block, field, val) {
  var el = block.querySelector('[data-field="plan.' + field + '"]');
  if (el) el.value = val;
}

function fmtPlanPrice(pl) {
  var sym = pl.currency === "BRL" ? "R$" : (pl.currency === "USD" ? "$" : pl.currency + " ");
  var s = sym + pl.cost;
  if (pl.cost_usd !== undefined && pl.currency !== "USD") s += " (~$" + pl.cost_usd + ")";
  return s + "/mo";
}

function makePlanRow(field, labelText, type, val, labelW) {
  var row = document.createElement("div");
  row.className = "plan-row";
  var lbl = document.createElement("span");
  lbl.className = "plan-label";
  lbl.textContent = labelText;
  lbl.style.minWidth = labelW;
  var inp = document.createElement("input");
  inp.className = "plan-input";
  inp.type = type;
  inp.setAttribute("data-field", "plan." + field);
  inp.value = val;
  inp.setAttribute('aria-label', labelText);
  if (type === "number") {
    inp.setAttribute("step", "0.01");
    inp.setAttribute("min", "0");
  } else {
    inp.setAttribute("minlength", "3");
  }
  row.appendChild(lbl);
  row.appendChild(inp);
  return row;
}

function saveSettings() {
  var errEl = document.getElementById("settings-error");
  errEl.style.display = "none";
  errEl.textContent = "";

  var invalid = Array.from(document.querySelectorAll('#settings-form input')).find(function(input) { return !input.checkValidity(); });
  if (invalid) {
    var owner = invalid.closest('.provider-card');
    if (owner && !owner.classList.contains('expanded')) toggleProviderCard(owner);
    invalid.reportValidity();
    errEl.textContent = 'Invalid field: ' + (invalid.getAttribute('aria-label') || invalid.id);
    errEl.style.display = 'block';
    return;
  }
  var interval = parseInt(document.getElementById("interval-select").value, 10);
  if (isNaN(interval) || interval < 300) {
    showSettingsStatus("Interval must be at least 5 min (300 s)", false);
    return;
  }

  var alerts = {
    openrouter_low_usd: parseFloat(document.getElementById("setting-openrouter-low").value) || 0,
    quota_warn_5h_pct: parseInt(document.getElementById("setting-quota-warn-5h").value, 10) || 70,
    quota_warn_weekly_pct: parseInt(document.getElementById("setting-quota-warn-weekly").value, 10) || 60
  };

  // Gather provider data from each card.
  var providers = [];
  var provider_order = [];
  var cards = document.querySelectorAll("#provider-list .provider-card");
  cards.forEach(function(card) {
    var pid = card.getAttribute("data-pid");
    var enabled = card.querySelector('input[data-field="enabled"]').checked;
    var label = card.querySelector('input[data-field="label"]').value.trim();

    // Collect the DOM order as the user-chosen provider_order.
    provider_order.push(pid);

    var providerObj = { id: pid, enabled: enabled, label: label };

    // Probe (if present)
    var probeCb = card.querySelector('input[data-field="probe"]');
    if (probeCb) {
      providerObj.probe = probeCb.checked;
    }

    // Plan (if present)
    var costInp = card.querySelector('input[data-field="plan.cost"]');
    if (costInp) {
      providerObj.plan = {
        cost: parseFloat(costInp.value) || 0,
        currency: card.querySelector('input[data-field="plan.currency"]').value.trim(),
        label: card.querySelector('input[data-field="plan.label"]').value.trim(),
        cost_usd: parseFloat(card.querySelector('input[data-field="plan.cost_usd"]').value) || 0
      };
    }

    providers.push(providerObj);
  });

  // Inline validation: name the bad field.
  var badField = null;
  if (isNaN(interval)) badField = "interval_sec";
  providers.forEach(function(p) {
    if (!p.label) badField = badField || "providers." + p.id + ".label";
    if (p.plan) {
      if (p.plan.cost < 0) badField = "providers." + p.id + ".plan.cost";
      if (p.plan.cost_usd !== 0 && p.plan.cost_usd < 0) badField = "providers." + p.id + ".plan.cost_usd";
    }
  });

  if (badField) {
    errEl.textContent = "Invalid field: " + badField;
    errEl.style.display = "block";
    return;
  }

  var payload = JSON.stringify({
    interval_sec: interval,
    listen: cfg_listen_cache,
    tz: cfg_tz_cache,
    alerts: alerts,
    providers: providers,
    provider_order: provider_order
  });
  var headers = authHeaders();
  headers["Content-Type"] = "application/json";

  var btn = document.getElementById("save-config-btn");
  btn.disabled = true;
  btn.textContent = "Saving…";

  apiFetch("/v1/config", { method: "PUT", headers: headers, body: payload })
    .then(function(r) {
      if (r.status === 401) { showTokenInput(); return; }
      if (!r.ok) {
        return r.json().then(function(e) {
          showSettingsStatus("Save failed: " + (e.error || r.statusText), false);
        }).catch(function() {
          showSettingsStatus("Save failed: " + r.statusText, false);
        });
      }
      settingsDirty = false;
      currentInterval = interval;
      reloadIntervals();
      lastRev = null;
      pollUsage();
      showSettingsStatus(document.documentElement.dataset.preview ? 'Saved in this browser · preview only' : 'Saved', true);
      // Sync the header interval select.
      var sel = document.getElementById("interval-select");
      if (sel) {
        for (var i = 0; i < sel.options.length; i++) {
          if (Number(sel.options[i].value) === interval) {
            sel.selectedIndex = i; break;
          }
        }
      }
    })
    .catch(function(err) {
      showSettingsStatus("Save failed: " + err.message, false);
    })
    .finally(function() {
      btn.disabled = false;
      btn.textContent = "Save settings";
    });
}

function showSettingsStatus(msg, ok) {
  var el = document.getElementById("settings-status");
  el.textContent = msg;
  el.style.color = ok ? "var(--ok)" : "var(--crit)";
}

var settingsRequest = null;
function loadSettings() {
  refreshSetup();
  if (settingsLoaded) return Promise.resolve();
  if (!settingsRequest) settingsRequest = fetchSettings().finally(function() { settingsRequest = null; });
  return settingsRequest;
}

async function fetchSettings() {
  var loading = document.getElementById('settings-loading');
  loading.hidden = false;
  loading.textContent = 'Loading settings…';
  try {
    var response = await apiFetch('/v1/config', { headers: authHeaders() });
    if (response.status === 401) { showTokenInput(); loading.textContent = 'Connect to load settings.'; return; }
    if (!response.ok) throw new Error('Could not load settings. Open Settings again to retry.');
    var cfg = await response.json();
    cfg_listen_cache = cfg.listen || '';
    cfg_tz_cache = cfg.tz || '';
    renderSettings(cfg);
  } catch (error) { loading.textContent = error.message; }
}

// ---- Key management ----

var keyAction = null;
var keyReturnFocus = null;
function setKeyPrompt(pid) { openKeyDialog(pid, 'POST'); }
function removeKey(pid) { openKeyDialog(pid, 'DELETE'); }

function openKeyDialog(pid, method) {
  var preview = !!document.documentElement.dataset.preview;
  keyAction = { pid: pid, method: method };
  keyReturnFocus = document.activeElement;
  document.getElementById('key-dialog-title').textContent = preview ? 'Credentials stay on your Mac' : (method === 'DELETE' ? 'Remove API key?' : 'Set API key');
  document.getElementById('key-dialog-description').textContent = preview ? 'Key management is unavailable in the hosted preview. Open this dashboard through your local Go daemon to manage ' + pid + '.' : method === 'DELETE' ? 'Remove the managed Keychain entry for ' + pid + '? Environment variables and CLI-owned credentials are not removed.' : 'Set the key for ' + pid + '. It is stored in the macOS Keychain and never written to the config file.';
  document.getElementById('key-input-label').hidden = preview || method === 'DELETE';
  document.getElementById('key-value').required = !preview && method === 'POST';
  document.getElementById('key-value').value = '';
  document.getElementById('key-error').textContent = '';
  var confirm = document.getElementById('key-confirm');
  confirm.hidden = preview;
  confirm.disabled = false;
  confirm.textContent = method === 'DELETE' ? 'Remove key' : 'Save key';
  document.getElementById('key-cancel').textContent = preview ? 'Got it' : 'Cancel';
  document.getElementById('key-dialog').showModal();
  (preview || method === 'DELETE' ? document.getElementById('key-cancel') : document.getElementById('key-value')).focus();
}

function closeKeyDialog() { document.getElementById('key-dialog').close(); }
function setupKeyDialog() {
  document.getElementById('key-cancel').addEventListener('click', closeKeyDialog);
  document.getElementById('key-dialog-close').addEventListener('click', closeKeyDialog);
  document.getElementById('key-dialog').addEventListener('close', function() {
    document.getElementById('key-value').value = '';
    keyAction = null;
    if (keyReturnFocus && keyReturnFocus.isConnected) keyReturnFocus.focus();
  });
  document.getElementById('key-form').addEventListener('submit', async function(event) {
    event.preventDefault();
    if (!keyAction || document.documentElement.dataset.preview) return;
    var action = keyAction;
    var btn = document.getElementById('key-confirm');
    if (btn.disabled) return;
    btn.disabled = true;
    btn.textContent = 'Saving…';
    try {
      var headers = authHeaders();
      headers['Content-Type'] = 'application/json';
      var options = { method: action.method, headers: headers };
      if (action.method === 'POST') options.body = JSON.stringify({ id: action.pid, value: document.getElementById('key-value').value.trim() });
      var response = await apiFetch('/v1/keys' + (action.method === 'DELETE' ? '?id=' + encodeURIComponent(action.pid) : ''), options);
      if (response.status === 401) { closeKeyDialog(); showTokenInput(); return; }
      if (!response.ok) throw new Error('The key could not be updated (' + response.status + '). Check your local Keychain and retry.');
      closeKeyDialog();
      notify(action.method === 'DELETE' ? 'Managed key removed.' : 'Key stored in Keychain.');
      var configResponse = await apiFetch('/v1/config', { headers: authHeaders() });
      if (configResponse.ok) {
        var cfg = await configResponse.json();
        (cfg.providers || []).forEach(function(p) {
          var card = Array.from(document.querySelectorAll('#provider-list .provider-card')).find(function(c) { return c.dataset.pid === p.id; });
          if (card) card.querySelectorAll('.key-state, .provider-source').forEach(function(el) { el.innerHTML = keyStateLabel(p); });
        });
      }
    } catch (error) { document.getElementById('key-error').textContent = error.message; }
    finally { btn.disabled = false; btn.textContent = action.method === 'DELETE' ? 'Remove key' : 'Save key'; }
  });
}

