import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import vm from 'node:vm';

const root = new URL('../internal/web/', import.meta.url);
const files = ['core.js', 'settings.js', 'device.js', 'render.js', 'app.js'];
const html = readFileSync(new URL('index.html', root), 'utf8');
const source = existsSync(new URL('core.js', root))
  ? files.map(f => readFileSync(new URL(f, root), 'utf8')).join('\n')
  : html.match(/<script>([\s\S]*?)<\/script>/)[1];

function harness() {
  const nodes = new Map();
  const storage = new Map();
  const timers = new Map();
  let timerId = 0;
  const node = id => {
    if (!nodes.has(id)) nodes.set(id, {
      id, style: {}, dataset: {}, textContent: '', innerHTML: '', hidden: false,
      value: '', options: [], selectedIndex: 0, children: [],
      classList: { add() {}, remove() {}, toggle() {}, contains() { return false; } },
      setAttribute(k, v) { this[k] = v; }, getAttribute(k) { return this[k]; },
      appendChild(child) { this.children.push(child); },
      querySelector(sel) { return node(id + sel); },
      querySelectorAll() { return []; }, addEventListener() {},
    });
    return nodes.get(id);
  };
  const context = vm.createContext({
    console, URLSearchParams, URL, Date, Math, Number, String, JSON, Promise, Response,
    AbortSignal, Intl, Map, Set, clearTimeout: id => timers.delete(id),
    setTimeout: (fn, ms) => { timers.set(++timerId, { fn, ms }); return timerId; },
    setInterval: (fn, ms) => { timers.set(++timerId, { fn, ms }); return timerId; },
    clearInterval: id => timers.delete(id),
    localStorage: { getItem: k => storage.get(k) || null, setItem: (k,v) => storage.set(k,v), removeItem: k => storage.delete(k) },
    document: { addEventListener() {}, getElementById: node, querySelector: node,
      querySelectorAll: () => [], createElement: tag => node('created-' + tag + Math.random()),
      documentElement: { dataset: {} }, body: { classList: { remove() {}, toggle() {} } } },
    window: { location: { search: '', pathname: '/', hash: '' }, history: { replaceState() {} }, addEventListener() {} },
    fetch: async () => new Response('{}', { status: 200 }),
  });
  vm.runInContext(source, context);
  return { context, node, storage, timers };
}

test('every frontend script parses without a framework', () => {
  assert.doesNotThrow(() => new vm.Script(source));
  assert.doesNotMatch(source, /from ['"]react|ReactDOM|createRoot/);
});

test('stats first paint does not send a persisted ETag', async () => {
  const { context: c, storage } = harness();
  storage.set('stats_etag', '"old"');
  let sent;
  c.fetch = async (_, opts) => { sent = opts.headers; return new Response('{"sources":{}}', { headers: { ETag: '"fresh"' } }); };
  await c.loadStats();
  assert.equal(sent['If-None-Match'], undefined);
  c.statsRendered = true;
  await c.loadStats();
  assert.equal(sent['If-None-Match'], '"fresh"');
});

test('usage 304 preserves its snapshot and polls health', async () => {
  const { context: c } = harness();
  c.lastRev = '"abc"';
  const calls = [];
  c.fetch = async (url, opts) => {
    calls.push([url, opts]);
    return url === '/v1/usage' ? new Response(null, { status: 304 }) : new Response('{}');
  };
  assert.equal(await c.loadUsage(), null);
  assert.equal(c.lastRev, '"abc"');
  assert.equal(calls[0][1].headers['If-None-Match'], '"abc"');
  assert.ok(calls.some(([url]) => url === '/healthz'));
});

test('stats polling schedules the following cycle', async () => {
  const { context: c, timers } = harness();
  c.loadStats = async () => null;
  await c.pollStats();
  assert.ok([...timers.values()].some(t => t.fn === c.pollStats), 'stats must not stop after the first scheduled request');
});

test('reloadIntervals replaces rather than duplicates its four timers', () => {
  const { context: c, timers } = harness();
  c.currentInterval = 900;
  c.reloadIntervals(); c.reloadIntervals();
  assert.equal(timers.size, 4);
  assert.ok([...timers.values()].some(t => t.ms === 900000));
});

test('initial persisted refresh interval updates scheduled timers', async () => {
  const { context: c, timers } = harness();
  c.fetch = async () => new Response('{"interval_sec":1800}');
  c.reloadIntervals();
  await c.loadConfig();
  assert.equal(c.currentInterval, 1800);
  assert.ok([...timers.values()].some(t => t.ms === 1800000));
});

test('unavailable advice clears both displayed and retained winner', () => {
  const { context: c, node } = harness();
  c.currentAdvise = { winner: 'claude' };
  c.renderAdvise(null);
  assert.equal(c.currentAdvise, null);
  assert.equal(node('advise-card.advise-table tbody').innerHTML, '');
  assert.match(node('advise-card.advise-error').textContent, /unavailable/);
});

test('null-percentage credit values appear exactly once without a bar', () => {
  const { context: c } = harness();
  const row = c.renderRow({ k: 'day', label: 'Spent today', pct: null, txt: '$0.55', tier: 'ok', reset_at: null }, false);
  assert.equal(row.split('$0.55').length - 1, 1);
  assert.doesNotMatch(row, /role="progressbar"|class="bar-fill"/);
});

test('401 recovery shows auth controls', async () => {
  const { context: c, node } = harness();
  c.fetch = async () => new Response(null, { status: 401 });
  assert.equal(await c.loadUsage(), null);
  assert.equal(node('token-input').style.display, 'flex');
});

test('server failures never masquerade as valid snapshots', async () => {
  const { context: c } = harness();
  c.fetch = async () => new Response('{"error":"unavailable"}', { status: 503 });
  await assert.rejects(() => c.loadUsage());
});

test('text escaping neutralizes markup in provider data', () => {
  const { context: c } = harness();
  assert.doesNotMatch(c.esc('<img src=x onerror="alert(1)">'), /<|>/);
});

test('recommendations never send a usage ETag', async () => {
  const { context: c } = harness();
  c.lastRev = '"cached"';
  let sent;
  c.fetch = async (_, opts) => { sent = opts; return new Response('{"winner":null,"recommendations":[]}'); };
  await c.loadAdvise();
  assert.equal(sent.headers['If-None-Match'], undefined);
  assert.ok(sent.signal instanceof AbortSignal);
});

test('exhausted advice has no fabricated alternative', () => {
  const { context: c, node } = harness();
  c.renderAdvise({ winner: null, recommendations: [] });
  assert.equal(node('advise-card.advise-winner').textContent, 'Every plan is exhausted');
});

test('blocked provider gets a blocked badge with time to free', () => {
  const { context: c, node } = harness();
  c.renderAdvise({
    winner: 'claude',
    recommendations: [
      { id: 'claude', label: 'Claude', pace_ratio: 1.93, effective_headroom_pct: 33, score: 17.10, blocked: false, blocked_for_sec: 0, reason: 'headroom 33%, pace 1.93x, binding 7d — ChatGPT is out for 2h30m, then it is the stronger pick' },
      { id: 'codex', label: 'ChatGPT', pace_ratio: 1.0, effective_headroom_pct: 38, score: 38.0, blocked: true, blocked_for_sec: 9000, reason: 'headroom 38%, pace 1.00x, binding 5h — BLOCKED: 5h at 100%, frees in 2h30m' },
    ]
  });
  // Winner is the non-blocked provider.
  assert.equal(node('advise-card.advise-winner').textContent, 'Use Claude next');
  // Winner's reason mentions the blocked top-scorer.
  const winReason = node('advise-card.advise-reason').textContent;
  assert.match(winReason, /ChatGPT is out for 2h30m/);
  assert.match(winReason, /stronger pick/);
  // Table has two rows.
  const tbody = node('advise-card.advise-table tbody');
  assert.equal(tbody.children.length, 2);
  // Codex row (second) has the blocked badge.
  const codexRow = tbody.children[1];
  assert.match(codexRow.innerHTML, /advise-blocked/);
  assert.match(codexRow.innerHTML, /Blocked/);
  assert.match(codexRow.innerHTML, /2h30m/);
  // Claude row (first) does NOT have the blocked badge.
  assert.doesNotMatch(tbody.children[0].innerHTML, /advise-blocked/);
});

test('failed stats request retains the last valid stats ETag', async () => {
  const { context: c, storage } = harness();
  storage.set('stats_etag', '"good"');
  c.statsRendered = true;
  c.fetch = async () => new Response('{"error":"no"}', { status: 500, headers: { ETag: '"bad"' } });
  await assert.rejects(() => c.loadStats());
  assert.equal(storage.get('stats_etag'), '"good"');
});

test('stats polling continues after a network failure', async () => {
  const { context: c, timers } = harness();
  c.loadStats = async () => { throw new Error('offline'); };
  await c.pollStats();
  assert.ok([...timers.values()].some(t => t.fn === c.pollStats));
});

test('failed interval update restores the persisted selection', async () => {
  const { context: c, node } = harness();
  c.currentInterval = 900;
  node('interval-select').value = '1800';
  c.fetch = async () => new Response(null, { status: 503 });
  await c.saveInterval();
  assert.equal(c.currentInterval, 900);
  assert.equal(node('interval-select').value, '900');
  assert.equal(node('interval-select').disabled, false);
});

test('device setup unavailable clears its old offered device', () => {
  const { context: c, node, timers } = harness();
  c.setupOfferAddr = 'stale-device';
  c.setupPolling(true);
  c.renderSetup({ available: false, headline: 'Not available on this OS' });
  assert.equal(c.setupOfferAddr, null);
  assert.equal(node('setup-provision-btn').disabled, true);
  assert.equal(node('setup-scan-btn').disabled, true);
  assert.equal(timers.size, 0);
});

test('settings navigation does not reload over unsaved edits', async () => {
  const { context: c } = harness();
  c.settingsLoaded = true;
  c.settingsDirty = true;
  c.refreshSetup = () => {};
  let calls = 0;
  c.fetch = async () => { calls++; return new Response('{}'); };
  await c.loadSettings();
  assert.equal(calls, 0);
  assert.equal(c.settingsDirty, true);
});

test('refresh failure unlocks its button and retains last good snapshot', async () => {
  const { context: c, node } = harness();
  c.currentSnap = { rev: 'last-good' };
  c.fetch = async () => new Response(null, { status: 503 });
  await c.doRefresh();
  assert.equal(c.currentSnap.rev, 'last-good');
  assert.equal(node('refresh-btn').disabled, false);
  assert.equal(node('request-error').hidden, false);
});

test('rejected refresh preserves the last snapshot and unlocks auth recovery', async () => {
  const { context: c, node } = harness();
  c.currentSnap = { rev: 'last-good' };
  c.lastRev = '"last-good"';
  node('request-error').hidden = true;
  c.fetch = async () => new Response(null, { status: 401 });
  await c.doRefresh();
  assert.equal(c.currentSnap.rev, 'last-good');
  assert.equal(c.lastRev, '"last-good"');
  assert.equal(node('token-input').style.display, 'flex');
  assert.equal(node('request-error').hidden, true);
  assert.equal(node('refresh-btn').disabled, false);
});

test('concurrent settings shortcuts share a single request', async () => {
  const { context: c } = harness();
  c.refreshSetup = () => {};
  let calls = 0;
  let resolveSettings;
  c.fetchSettings = () => {
    calls++;
    return new Promise(resolve => { resolveSettings = resolve; });
  };
  const first = c.loadSettings();
  const second = c.loadSettings();
  assert.equal(first, second);
  assert.equal(calls, 1);
  resolveSettings();
  await first;
  assert.equal(c.settingsRequest, null);
});

test('tab navigation preserves its destination and resets document scroll', () => {
  const { context: c, node, storage } = harness();
  c.document.body.classList.contains = () => false;
  let scroll;
  let path;
  c.window.scrollTo = position => { scroll = position; };
  c.window.history.replaceState = (_, __, value) => { path = value; };
  c.showTab('tab-activity');
  assert.equal(node('breadcrumb-view').textContent, 'Activity');
  assert.equal(storage.get('usaged_tab'), 'tab-activity');
  assert.equal(path, '/#activity');
  assert.equal(scroll.top, 0);
  assert.equal(scroll.left, 0);
});

test('query token is persisted and removed from visible URL', () => {
  const { context: c, storage } = harness();
  c.window.location.search = '?token=test-only-token&view=quota';
  let replaced;
  c.window.history.replaceState = (_, __, path) => { replaced = path; };
  c.stripTokenFromUrl();
  assert.equal(storage.get('usaged_token'), 'test-only-token');
  assert.equal(replaced, '/?view=quota');
  assert.equal(c.authHeaders()['X-Device-Token'], 'test-only-token');
});

test('production assembly includes no preview adapter or unresolved markers', async () => {
  const { assemble } = await import('./assemble-preview.mjs');
  const production = await assemble({ preview: false });
  assert.doesNotMatch(production, /__PREVIEW_DATA__|\{\{styles\}\}|\{\{scripts\}\}/);
  assert.equal((production.match(/<script>/g) || []).length, 1);
  assert.doesNotThrow(() => new vm.Script(production.match(/<script>([\s\S]*?)<\/script>/)[1]));
});

// NOTE: the "hosted preview" assembly test is omitted — preview/ is not ported
// to this repo (preview/data.js and preview/adapter.js are v0 scaffolding).
// The production-assembly test above covers assemble({ preview: false }),
// which is the only path this repo uses.

test('production source does not import or activate sample adapters', () => {
  assert.doesNotMatch(source, /preview\/adapter|__PREVIEW_DATA__/);
  assert.doesNotMatch(html, /src=["']https:\/\//);
});

// TASK 116. GET /v1/device answers exactly {"state":"unknown"} until the first
// check-in — the state of every fresh install, and of every daemon restart
// until the stick next polls (up to 12 h on battery). That object is truthy, so
// it sailed past the `if (!ds)` guard and the card rendered "undefineds ago",
// "200:undefined 304:undefined", and a confident "OTA: disarmed (USB only)"
// about a device the daemon had never heard from. The payload below is the
// VERBATIM body a live daemon returns in that state.
test('device card claims nothing about a device it has never heard from', () => {
  const { context } = harness();
  const html = context.deviceCard(JSON.parse('{"state":"unknown"}'));

  assert.match(html, /Waiting for first check-in/);
  assert.doesNotMatch(html, /undefined/);
  assert.doesNotMatch(html, /disarmed/);
});

// The device reports its own OTA state on every fetch, so false IS a
// measurement — but undefined is not one, and must not be rendered as disarmed.
test('OTA state is a tri-state, not a falsy check', () => {
  const { context } = harness();
  const base = { last_seen: 1789087018, seconds_since: 12, interval_sec: 300,
                 count_200: 4, count_304: 0, state: 'connected', addr: '192.168.0.194' };

  const armed = context.deviceCard({ ...base, ota_armed: true });
  assert.doesNotMatch(armed, /disarmed|not reported/);

  const disarmed = context.deviceCard({ ...base, ota_armed: false });
  assert.match(disarmed, /OTA: disarmed \(USB only\)/);

  // Never reported: a browser, or firmware older than task 115.
  const silent = context.deviceCard(base);
  assert.match(silent, /OTA: not reported/);
  assert.doesNotMatch(silent, /disarmed/);
});
