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
  assert.equal(node('page-title').textContent, 'Activity');
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
