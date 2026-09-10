var settingsLoaded = false;
var settingsDirty = false;
var activeFilter = 'all';
var pageNames = {
  quota: ['Quota overview', 'A little clarity for everything you create.'],
  activity: ['Activity', 'Every session adds up. See your building rhythm.'],
  models: ['Models', 'Understand the models behind your work.'],
  attention: ['Needs attention', 'A heads-up now. Fewer interruptions later.'],
  settings: ['Settings', 'Your providers, your preferences, your workspace.']
};

function showTokenInput() {
  var msg = document.getElementById('auth-error');
  msg.style.display = 'block';
  msg.textContent = '401 Unauthorized — enter your device token to connect to the local daemon.';
  document.getElementById('token-input').style.display = 'flex';
  setConnection('Authentication needed', 'pending');
}

function saveToken() {
  var input = document.getElementById('token-field');
  var token = input.value.trim();
  if (!token) return;
  localStorage.setItem('usaged_token', token);
  input.value = '';
  document.getElementById('token-input').style.display = 'none';
  document.getElementById('auth-error').style.display = 'none';
  lastRev = null;
  statsRendered = false;
  loadConfig().catch(requestFailed);
  pollUsage();
  pollStats();
  pollDevice();
  if (document.getElementById('tab-settings').classList.contains('active')) loadSettings();
}

function requestFailed(error) {
  var el = document.getElementById('request-error');
  el.hidden = false;
  el.textContent = (error && error.message ? error.message : 'Could not reach the local daemon.') + ' Your last available data is retained. Use Refresh now to retry.';
  setConnection('Disconnected', 'error');
}

function acceptSnapshot(snap) {
  if (!snap) return;
  currentSnap = snap;
  renderSnapshot(snap);
  renderAttention(snap, null);
  document.getElementById('request-error').hidden = true;
  setConnection('Connected', 'ok');
}

async function doRefresh() {
  var btn = document.getElementById('refresh-btn');
  if (btn.disabled) return;
  btn.disabled = true;
  btn.classList.add('refreshing');
  btn.innerHTML = icon('refresh') + '<span>Refreshing…</span>';
  try {
    var headers = authHeaders();
    headers['Content-Type'] = 'application/json';
    var response = await apiFetch('/v1/refresh', { method: 'POST', headers: headers });
    if (response.status === 401) {
      // A rejected early-poll does not invalidate the existing data view.
      showTokenInput();
      return;
    }
    if (!response.ok) throw new Error('Refresh failed (' + response.status + ').');
    acceptSnapshot(await response.json());
    await Promise.all([loadAdvise().then(renderAdvise), loadStats().then(function(rep) { if (rep) renderStats(rep); })]);
    notify(document.documentElement.dataset.preview ? 'Sample snapshot refreshed. No live provider calls were made.' : 'Your dashboard is up to date.');
  } catch (error) {
    renderAdvise(null);
    requestFailed(error);
  } finally {
    btn.disabled = false;
    btn.classList.remove('refreshing');
    btn.innerHTML = icon('refresh') + '<span>Refresh now</span>';
  }
}

async function saveInterval() {
  var sel = document.getElementById('interval-select');
  var sec = Number(sel.value);
  sel.disabled = true;
  try {
    var headers = authHeaders();
    headers['Content-Type'] = 'application/json';
    var response = await apiFetch('/v1/config/interval', { method: 'PUT', headers: headers, body: JSON.stringify({ interval_sec: sec }) });
    if (response.status === 401) { showTokenInput(); sel.value = String(currentInterval); return; }
    if (!response.ok) throw new Error('Could not save the refresh interval.');
    currentInterval = sec;
    reloadIntervals();
    notify('Refresh interval updated.');
  } catch (error) {
    sel.value = String(currentInterval);
    notify(error.message);
  } finally { sel.disabled = false; }
}

function setupTabs() {
  var tabs = Array.from(document.querySelectorAll('.tab-strip button[role="tab"]'));
  tabs.forEach(function(btn, i) {
    btn.addEventListener('click', function() { showTab(btn.getAttribute('aria-controls')); });
    btn.addEventListener('keydown', function(event) {
      var next = i;
      if (event.key === 'ArrowDown' || event.key === 'ArrowRight') next = (i + 1) % tabs.length;
      else if (event.key === 'ArrowUp' || event.key === 'ArrowLeft') next = (i - 1 + tabs.length) % tabs.length;
      else if (event.key === 'Home') next = 0;
      else if (event.key === 'End') next = tabs.length - 1;
      else return;
      event.preventDefault();
      tabs[next].focus();
      showTab(tabs[next].getAttribute('aria-controls'));
    });
  });
  var saved = window.location.hash.slice(1) || (localStorage.getItem('usaged_tab') || '').replace('tab-', '');
  showTab('tab-' + (pageNames[saved] ? saved : 'quota'));
  window.addEventListener('hashchange', function() {
    var view = window.location.hash.slice(1);
    if (pageNames[view]) showTab('tab-' + view);
  });
}

function showTab(panelId) {
  var view = panelId.replace('tab-', '');
  if (!pageNames[view]) return;
  document.querySelectorAll('.tab-panel').forEach(function(panel) {
    var selected = panel.id === panelId;
    panel.hidden = !selected;
    panel.classList.toggle('active', selected);
  });
  document.querySelectorAll('.tab-strip button[role="tab"]').forEach(function(btn) {
    var selected = btn.getAttribute('aria-controls') === panelId;
    btn.setAttribute('aria-selected', String(selected));
    btn.tabIndex = selected ? 0 : -1;
  });
  document.getElementById('breadcrumb-view').textContent = pageNames[view][0];
  document.getElementById('advise-card').hidden = view !== 'quota';
  localStorage.setItem('usaged_tab', panelId);
  window.history.replaceState({}, '', window.location.pathname + window.location.search + '#' + view);
  closeNavigation();
  window.scrollTo({ top: 0, left: 0, behavior: 'instant' });
  if (view === 'settings') loadSettings();
}

function closeNavigation() {
  var opened = document.body.classList.contains('nav-open');
  if (opened && document.getElementById('sidebar').contains(document.activeElement)) document.getElementById('menu-toggle').focus({ preventScroll: true });
  document.body.classList.remove('nav-open');
  document.getElementById('nav-backdrop').hidden = true;
  document.getElementById('menu-toggle').setAttribute('aria-expanded', 'false');
}

function applyProviderFilter(filter) {
  activeFilter = filter;
  var count = 0;
  document.querySelectorAll('#cards > .card').forEach(function(card) {
    card.hidden = filter !== 'all' && card.dataset.kind !== filter;
    if (!card.hidden) count++;
  });
  document.querySelectorAll('[data-filter]').forEach(function(btn) {
    var selected = btn.dataset.filter === filter;
    btn.classList.toggle('active', selected);
    btn.setAttribute('aria-pressed', String(selected));
  });
  document.getElementById('quota-empty').hidden = count > 0;
}

function markSettingsDirty() {
  settingsDirty = true;
  document.getElementById('settings-status').textContent = 'Unsaved changes';
  document.getElementById('settings-status').style.color = 'var(--muted)';
}

// Polling intervals are derived from currentInterval. Each recurring timer
// replaces its own handle, including stats, which previously stopped after one run.
var pollUsageTO = 0, pollStatsTO = 0, pollDeviceTO = 0, tickResetsTO = 0;
function reloadIntervals() {
  clearTimeout(pollUsageTO); clearTimeout(pollStatsTO); clearTimeout(pollDeviceTO); clearInterval(tickResetsTO);
  pollUsageTO = setTimeout(pollLoop, currentInterval * 1000);
  pollStatsTO = setTimeout(pollStats, Math.max(currentInterval * 1000, 300000));
  pollDeviceTO = setTimeout(pollDevice, 5000);
  tickResetsTO = setInterval(tickResets, 30000);
}
async function pollLoop() {
  await pollUsage();
  clearTimeout(pollUsageTO);
  pollUsageTO = setTimeout(pollLoop, currentInterval * 1000);
}
function pollUsage() {
  return Promise.all([
    loadUsage().then(acceptSnapshot).catch(requestFailed),
    loadAdvise().then(renderAdvise)
  ]);
}

// Device status is independently polled, never coupled to the usage ETag.
async function pollDevice() {
  try { currentDeviceState = await loadDevice(); }
  catch (_) { currentDeviceState = null; }
  renderAttention(currentSnap, null);
  var ds = currentDeviceState;
  document.getElementById('sidebar-device-status').textContent = ds && ds.state === 'connected' ? 'Connected to daemon' : ds && ds.state === 'absent' ? 'Device absent' : 'Waiting for device';
  clearTimeout(pollDeviceTO);
  pollDeviceTO = setTimeout(pollDevice, 5000);
}
async function pollStats() {
  try {
    var report = await loadStats();
    if (report) renderStats(report);
  } catch (error) { requestFailed(error); }
  finally {
    clearTimeout(pollStatsTO);
    pollStatsTO = setTimeout(pollStats, Math.max(currentInterval * 1000, 300000));
  }
}
function tickResets() {
  document.querySelectorAll('.reset').forEach(function(el) {
    var resetAt = parseInt(el.getAttribute('data-reset-at') || '0', 10);
    el.textContent = resetText(resetAt || null, el.getAttribute('data-fallback') || '');
  });
}

function init() {
  window.history.scrollRestoration = 'manual';
  stripTokenFromUrl();
  renderIcons();
  setupTabs();
  loadConfig().catch(requestFailed);
  pollUsage();
  pollStats();
  pollDevice();
  document.getElementById('refresh-btn').addEventListener('click', doRefresh);
  document.getElementById('token-save').addEventListener('click', saveToken);
  document.getElementById('token-field').addEventListener('keydown', function(event) {
    if (event.isComposing || event.keyCode === 229) return;
    if (event.key === 'Enter') saveToken();
  });
  document.getElementById('interval-select').addEventListener('change', saveInterval);
  document.getElementById('save-config-btn').addEventListener('click', saveSettings);
  document.getElementById('setup-scan-btn').addEventListener('click', startSetupScan);
  document.getElementById('setup-provision-btn').addEventListener('click', startSetupProvision);
  document.getElementById('setup-stop-btn').addEventListener('click', stopSetup);
  document.getElementById('settings-form').addEventListener('input', markSettingsDirty);
  document.getElementById('settings-form').addEventListener('change', markSettingsDirty);
  document.querySelectorAll('[data-filter]').forEach(function(btn) { btn.addEventListener('click', function() { applyProviderFilter(btn.dataset.filter); }); });
  document.querySelectorAll('[data-view]').forEach(function(btn) { btn.addEventListener('click', function() { showTab('tab-' + btn.dataset.view); }); });
  document.getElementById('menu-toggle').addEventListener('click', function() {
    var opened = document.body.classList.toggle('nav-open');
    this.setAttribute('aria-expanded', String(opened));
    document.getElementById('nav-backdrop').hidden = !opened;
  });
  document.getElementById('nav-backdrop').addEventListener('click', closeNavigation);
  document.addEventListener('keydown', function(event) { if (event.key === 'Escape') closeNavigation(); });
  document.getElementById('cards').addEventListener('click', async function(event) {
    var btn = event.target.closest('[data-provider-settings]');
    if (!btn) return;
    showTab('tab-settings');
    await loadSettings();
    var card = Array.from(document.querySelectorAll('#provider-list .provider-card')).find(function(c) { return c.dataset.pid === btn.dataset.providerSettings; });
    if (card) {
      if (!card.classList.contains('expanded')) toggleProviderCard(card);
      card.scrollIntoView({ block: 'center', behavior: 'smooth' });
      card.querySelector('.card-chevron').focus({ preventScroll: true });
    }
  });
  document.getElementById('activity').addEventListener('click', function(event) {
    var cell = event.target.closest('.heat-cell');
    if (cell) document.getElementById('day-detail').textContent = cell.getAttribute('aria-label');
  });
  document.getElementById('activity').addEventListener('keydown', function(event) {
    var cell = event.target.closest('.heat-cell');
    if (!cell) return;
    var offsets = { ArrowRight: 7, ArrowLeft: -7, ArrowDown: 1, ArrowUp: -1 };
    if (!(event.key in offsets)) return;
    var cells = Array.from(cell.closest('.heatmap').querySelectorAll('button'));
    var next = cells[cells.indexOf(cell) + offsets[event.key]];
    if (next) { event.preventDefault(); cell.tabIndex = -1; next.tabIndex = 0; next.focus(); }
  });
  setupKeyDialog();
  document.getElementById('month-label').textContent = new Date().toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
  reloadIntervals();
  window.addEventListener('beforeunload', function(event) {
    if (!settingsDirty) return;
    event.preventDefault();
    event.returnValue = '';
  });
}
document.addEventListener('DOMContentLoaded', init);
