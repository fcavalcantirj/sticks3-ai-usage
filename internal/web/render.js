// ---- Rendering ----

function renderSnapshot(snap) {
  var container = document.getElementById("cards");
  var loading = container.querySelector('.empty-state');
  if (loading) loading.remove();
  snap.providers.forEach(function(p) {
    var jsonStr = JSON.stringify(p);
    var cardEl = document.getElementById("card-" + p.id);
    if (lastProviderJson[p.id] !== jsonStr) {
      if (cardEl) {
        cardEl.className = "card" + (p.status === "stale" ? " stale" : "") + (p.status === 'off' ? ' provider-off' : '');
        cardEl.innerHTML = cardInner(p);
      } else {
        container.insertAdjacentHTML("beforeend", cardElHtml(p));
      }
    }
    var rendered = document.getElementById('card-' + p.id);
    rendered.dataset.kind = p.kind;
    container.appendChild(rendered);
    lastProviderJson[p.id] = jsonStr;
  });
  // Remove cards for providers no longer present.
  for (var id in lastProviderJson) {
    if (!snap.providers.some(function(p) { return p.id === id; })) {
      var old = document.getElementById("card-" + id);
      if (old) old.remove();
      delete lastProviderJson[id];
    }
  }
  lastRev = '"' + snap.rev + '"';
  document.getElementById('provider-count').textContent = snap.providers.length;
  applyProviderFilter(activeFilter);
  updateFooter(snap);
}

// renderAdvise paints the top-of-page "use this next" card from the /v1/advise
// outcome. The daemon already ranked the plans and chose the winner; this is
// pure presentation. Three states:
//  - null: endpoint unreachable (network/5xx) or 401 (token already prompted).
//    No stale winner is rendered — whatever was shown is cleared.
//  - winner set: name the winning label + its reason + the per-plan table.
//  - winner null: every plan is exhausted. No credit text (credit is out of
//    this feature by Felipe's 2026-09-08 decision).
// Provider IDs and labels are copied from the JSON — never retyped (a typo is
// silent data loss, and would ship to the 240x135 screen in task 98).
function renderAdvise(outcome) {
  var card = document.getElementById("advise-card");
  if (!card) return;
  var winnerEl = card.querySelector(".advise-winner");
  var reasonEl = card.querySelector(".advise-reason");
  var errEl    = card.querySelector(".advise-error");
  var tbody    = card.querySelector(".advise-table tbody");

  // Unreachable / 401 / non-ok: do NOT render a stale winner.
  if (!outcome) {
    currentAdvise = null;
    card.style.display = 'block';
    document.getElementById('advise-comparison').hidden = true;
    winnerEl.style.display = "none";
    reasonEl.style.display = "none";
    tbody.innerHTML = "";
    errEl.style.display = "block";
    errEl.textContent = "Could not reach /v1/advise — recommendation unavailable.";
    return;
  }

  currentAdvise = outcome;
  errEl.style.display = "none";
  card.style.display = "block";

  // Per-plan table: Provider | Pace | Headroom | Score. Every value is taken
  // verbatim from the endpoint.
  tbody.innerHTML = "";
  var recs = outcome.recommendations || [];
  document.getElementById('advise-comparison').hidden = recs.length === 0;
  recs.forEach(function(r) {
    var isWinner = r.id === outcome.winner;
    var tr = document.createElement("tr");
    tr.className = isWinner ? 'advise-row-winner' : '';
    tr.innerHTML =
      '<th scope="row"><span class="advise-provider">' + esc(r.label || r.id) +
        (isWinner ? '<span class="advise-next">Next</span>' : '') +
        (r.blocked ? '<span class="advise-blocked">Blocked \u2014 frees in ' + esc(formatShortDuration(r.blocked_for_sec)) + '</span>' : '') + '</span></th>' +
      '<td class="num">' + r.pace_ratio.toFixed(2) + 'x</td>' +
      '<td class="num">' + esc(r.effective_headroom_pct) + '%</td>' +
      '<td class="num">' + r.score.toFixed(2) + '</td>';
    tbody.appendChild(tr);
  });

  // The winner field is a provider ID. Look up its label from the
  // recommendations so we never retype an id.
  var winRec = null;
  for (var i = 0; i < recs.length; i++) {
    if (recs[i].id === outcome.winner) { winRec = recs[i]; break; }
  }
  winnerEl.style.display = "block";
  reasonEl.style.display = "block";
  if (outcome.winner && winRec) {
    winnerEl.textContent = "Use " + winRec.label + " next";
    reasonEl.textContent = winRec.reason || "";
  } else if (outcome.winner) {
    winnerEl.textContent = "Use " + outcome.winner + " next";
    reasonEl.textContent = "";
  } else {
    // winner: null — no plan has positive headroom. Show nothing about credit.
    winnerEl.textContent = "Every plan is exhausted";
    reasonEl.textContent = "";
  }
}

function cardElHtml(p) {
  return '<article id="card-' + esc(p.id) + '" class="card' +
    (p.status === 'stale' ? ' stale' : '') + (p.status === 'off' ? ' provider-off' : '') + '" data-kind="' + esc(p.kind) + '">' + cardInner(p) + '</article>';
}

function cardInner(p) {
  var content = (p.rows || []).map(function(r) { return renderRow(r, p.status === 'stale'); }).join('');
  if (!content) content = '<span class="off-title">' + (p.status === 'off' ? 'Ready when you are.' : 'No usage reported yet.') + '</span><span>' + (p.status === 'off' ? 'Configure this provider to see usage here.' : esc(p.msg || 'Waiting for a provider response.')) + '</span>';
  var severity = ['ok', 'warn', 'crit'].indexOf(p.severity) >= 0 ? p.severity : 'ok';
  var footer = p.status === 'off' ? 'Not connected' : p.status !== 'ok' ? p.msg || p.status : severity === 'crit' ? 'Needs attention' : severity === 'warn' ? 'Approaching quota' : 'Usage within limits';
  return cardHeader(p) + '<div class="rows">' + content + '</div>' +
    '<div class="card-footer severity-' + severity + '"><span class="status-indicator"></span><span>' + esc(footer) + '</span><button class="card-link" data-provider-settings="' + esc(p.id) + '" aria-label="Configure ' + esc(p.label) + '">' + (p.status === 'off' ? 'Configure' : 'Manage') + icon('arrow-right') + '</button></div>';
}

function cardHeader(p) {
  var subtitle = p.kind === 'credit' ? 'Pay-as-you-go' : p.kind === 'free' ? 'API access' : 'Subscription';
  return '<div class="card-header">' + providerMark(p.id) + '<div class="card-heading-copy"><h3 class="card-title">' + esc(p.label) + '</h3><span class="provider-subtitle">' + subtitle + '</span></div>' +
    (p.status === 'off' ? statusBadge(p.status, p.msg) : '<span class="badge badge-plan">' + esc(planLabel(p.plan)) + '</span>') + '</div>';
}

function renderRow(row, isStale) {
  var hasPct = row.pct != null;
  // null-pct rows (bal/key/day): txt shows once; no countdown or duplicate txt.
  if (!hasPct) return '<div class="metric-plain"><span class="label">' + esc(row.label) + '</span><span class="usage">' + esc(row.txt || '') + '</span></div>';
  var pct = Math.max(0, Math.min(100, Number(row.pct) || 0));
  var resetTxt = resetText(row.reset_at, row.txt);
  return '<div class="quota-row' + (isStale ? ' stale' : '') + '"><div class="metric-head"><span class="label">' + esc(row.label) + '</span>' +
    '<span class="usage" style="color:' + tierColor(row.tier) + '">' + esc(row.pct) + '%<small>used</small></span></div>' +
    '<div class="bar" role="progressbar" aria-label="' + esc(row.label) + '" aria-valuemin="0" aria-valuemax="100" aria-valuenow="' + pct + '"><div class="bar-fill" style="width:' + pct + '%;background:' + tierColor(row.tier) + '"></div></div>' +
    (resetTxt ? '<div class="metric-bottom"><span class="reset" data-reset-at="' + (Number(row.reset_at) || '') + '" data-fallback="' + esc(row.txt || '') + '">' + esc(resetTxt) + '</span></div>' : '') + '</div>';
}

function updateFooter(snap) {
  var asOf = fmtTime(snap.generated_at);
  var el = document.getElementById("footer-text");
  var parts = ["Updated " + asOf, "checked " + fmtTime(snap.checked_at || snap.generated_at),
               "rev " + (snap.rev || "\u2014"),
               "seq " + (snap.seq != null ? snap.seq : "\u2014")];
  // ORDER #65: server-computed age (seconds since checked_at) rides on 200
  // responses.  A 304 has no body so age is not refreshed — the web page keeps
  // the last known value, which is correct (it was fresh at the last check).
  if (snap.age != null && snap.age >= 0) {
    var tier = snap.age <= snap.next_sec * 2 ? "fresh"
             : snap.age <= snap.next_sec * 4 ? "stale" : "very stale";
    parts.push("age " + snap.age + "s (" + tier + ")");
  }
  el.textContent = parts.join(" \u00b7 ");
}

function updateFooterChecked(h) {
  var el = document.getElementById("footer-text");
  var parts = el.textContent.split(" \u00b7 ");
  if (parts.length >= 4) {
    parts[1] = "checked " + fmtTime(h.checked_at);
    if (h.rev) parts[2] = "rev " + h.rev;
    if (h.seq != null) parts[3] = "seq " + h.seq;
    el.textContent = parts.join(" \u00b7 ");
  }
}

// ---- Stats rendering ----

function tokensTotal(t) {
  if (!t) return 0;
  return (t.input || 0) + (t.output || 0) + (t.cache_read || 0) + (t.cache_write || 0);
}

// fmtCurrency formats a cost value in the given currency. BRL renders as
// "R$1265" (whole units); USD as "$230.00" to match the existing fmtCost style.
function fmtCurrency(n, currency) {
  if (currency === "BRL") return "R$" + Math.round(n).toLocaleString("pt-BR");
  if (currency === "USD") return "$" + n.toFixed(2);
  return currency + " " + n.toFixed(2);
}

// formatApiEquiv renders the equivalent-API disclosure line. When the source
// is partial (some models unpriced), the unpriced names are listed.
function formatApiEquiv(cost, partial, unpriced) {
  if (!cost) return "";
  var parts = ["API equiv: " + fmtCost(cost)];
  if (partial) {
    var names = unpriced && unpriced.length ? unpriced.join(", ") : "";
    parts.push("(partial" + (names ? ": " + names : "") + ")");
  }
  return parts.join(" ");
}

function renderStats(report) {
  // Stat pills
  var todayTok = 0, monthTok = 0;
  for (var srcName in report.sources) {
    var src = report.sources[srcName];
    todayTok += tokensTotal(src.today.tokens);
    monthTok += tokensTotal(src.month.tokens);
  }
  document.getElementById("pill-today-tok").textContent = fmtTokens(todayTok);
  document.getElementById("pill-month-tok").textContent = fmtTokens(monthTok);

  // Cost pills — plan sum comes from the server (report.plan_cost), never
  // summed from src.cost in JS. Both "today" and "month" show the same fixed
  // plan total (subscription is monthly, not daily accruing).
  var pc = report.plan_cost;
  var todayCostEl = document.getElementById("pill-today-cost");
  var monthCostEl = document.getElementById("pill-month-cost");
  var todaySecEl = document.getElementById("pill-today-cost-secondary");
  var monthSecEl = document.getElementById("pill-month-cost-secondary");
  var todayApiEl = document.getElementById("pill-today-api");
  var monthApiEl = document.getElementById("pill-month-api");

  if (pc) {
    todayCostEl.textContent = fmtCurrency(pc.primary_total, pc.primary_currency);
    monthCostEl.textContent = fmtCurrency(pc.primary_total, pc.primary_currency);

    if (pc.secondary_currency) {
      todaySecEl.textContent = fmtCurrency(pc.secondary_total, pc.secondary_currency);
      monthSecEl.textContent = fmtCurrency(pc.secondary_total, pc.secondary_currency);
      todaySecEl.style.display = "block";
      monthSecEl.style.display = "block";
    } else {
      todaySecEl.style.display = "none";
      monthSecEl.style.display = "none";
    }

    // Equivalent-API disclosure (old estimate from scanned sources), with
    // partial flag surface when applicable.
    todayApiEl.textContent = formatApiEquiv(pc.api_cost_today, pc.api_partial, pc.api_unpriced_models);
    monthApiEl.textContent = formatApiEquiv(pc.api_cost_month, pc.api_partial, pc.api_unpriced_models);
  } else {
    todayCostEl.textContent = "$—";
    monthCostEl.textContent = "$—";
    todaySecEl.style.display = "none";
    monthSecEl.style.display = "none";
    todayApiEl.textContent = "";
    monthApiEl.textContent = "";
  }

  // Plan-value ratio
  var ratioEl = document.getElementById("pill-month-ratio");
  ratioEl.textContent = "";
  ratioEl.className = "ratio";
  var hasRatio = false;
  for (var sn in report.sources) {
    var s = report.sources[sn];
    if (s.plan_value && s.plan_value.has_ratio) {
      hasRatio = true;
      var r = s.plan_value.ratio;
      var label = s.plan_value.label || sn;
      ratioEl.textContent = label + " " + r.toFixed(1) + "x your $" +
        s.plan_value.cost.toFixed(0) + "/mo plan";
      ratioEl.className = "ratio " + (s.plan_value.warn ? "warn" : "ok");
      break;
    }
  }
  if (!hasRatio) {
    for (var sn2 in report.sources) {
      var s2 = report.sources[sn2];
      if (s2.plan_value && !s2.plan_value.has_ratio) {
        ratioEl.textContent = s2.plan_value.label || sn2;
        ratioEl.className = "ratio";
        break;
      }
    }
  }

  // Heatmap cards
  var srcNames = Object.keys(report.sources);
  srcNames.forEach(function(name) {
    var src = report.sources[name];
    var cardEl = document.getElementById("heatmap-" + name);
    if (!cardEl) return;
    var label = name === "claude_code" ? "Claude Code" : "Codex";
    cardEl.className = "card";
    cardEl.style.display = "block";
    cardEl.innerHTML = '<div class="card-header">' + providerMark(name === 'claude_code' ? 'claude' : 'codex') + '<div><h3 class="card-title">' + label + '</h3><span class="muted">Token activity</span></div><div class="activity-total font-mono">' + fmtTokens(tokensTotal(src.month.tokens)) + '<span>tokens this month</span></div></div>' + heatmapHtml(src, name);
  });
  ["claude_code", "codex"].forEach(function(name) {
    var el = document.getElementById("heatmap-" + name);
    if (el && srcNames.indexOf(name) === -1) el.style.display = "none";
  });

  // Models table — server returns models already sorted by month tokens desc.
  var tbody = document.querySelector("#models-table tbody");
  tbody.innerHTML = "";
  var models = [];
  for (var sn in report.sources) {
    var s = report.sources[sn];
    s.models.forEach(function(m) {
      models.push({ name: m.model, dayTok: tokensTotal(m.tokens_today), monthTok: tokensTotal(m.tokens_month), cost: m.cost_month, reqs: m.requests });
    });
  }
  // No client-side sort: the server sorts by month tokens desc. Reordering
  // here would silently disagree with the table's month-cost ranking.
  models.forEach(function(m) {
    var tr = document.createElement("tr");
    tr.innerHTML = '<td class="model-name">' + esc(m.name) + '</td>' +
      '<td>' + fmtTokens(m.dayTok) + '</td>' +
      '<td>' + fmtTokens(m.monthTok) + '</td>' +
      '<td>' + fmtCost(m.cost) + '</td>' +
      // Requests column is lifetime (header names no window), not a lie.
      '<td>' + m.reqs + '</td>';
    tbody.appendChild(tr);
  });

  if (!models.length) tbody.innerHTML = '<tr><td colspan="5" class="muted">No model usage recorded yet.</td></tr>';
  document.getElementById('activity-empty').hidden = srcNames.length > 0;
  document.getElementById('month-cost-note').hidden = !!ratioEl.textContent;
  statsRendered = true;
}

function heatmapHtml(src, srcName) {
  var days = src.days || [];
  if (!days.length) return '<div class="empty-state">No daily activity recorded yet.</div>';
  var maxTok = Math.max(1, ...days.map(function(day) { return tokensTotal(day.tokens); }));
  var today = new Date().toISOString().slice(0, 10);
  var first = new Date(days[0].date + 'T00:00:00Z');
  var offset = first.getUTCDay();
  var monthNames = [];
  var total = 0;
  var cells = days.map(function(day, index) {
    var tok = tokensTotal(day.tokens);
    total += tok;
    var relative = tok / maxTok;
    var level = tok === 0 ? 0 : relative < .25 ? 1 : relative < .5 ? 2 : relative < .75 ? 3 : 4;
    var date = new Date(day.date + 'T00:00:00Z');
    var month = date.toLocaleDateString(undefined, { month: 'short', timeZone: 'UTC' });
    if (monthNames[monthNames.length - 1] !== month) monthNames.push(month);
    var column = Math.floor(((date - first) / 86400000 + offset) / 7) + 1;
    var row = date.getUTCDay() + 1;
    var title = (srcName === 'claude_code' ? 'Claude Code' : 'Codex') + ' · ' + day.date + ' · ' + fmtTokens(tok) + ' tokens';
    return '<button class="heat-cell heat-' + level + (day.date === today ? ' heat-today' : '') + '" style="grid-column:' + column + ';grid-row:' + row + '" tabindex="' + (index === days.length - 1 ? '0' : '-1') + '" aria-label="' + esc(title) + '" title="' + esc(title) + '"></button>';
  }).join('');
  var scale = '<div class="heatmap-scale"><span>Less</span>' + [0,1,2,3,4].map(function(n) { return '<i class="swatch heat-' + n + '"></i>'; }).join('') + '<span>More</span></div>';
  var summary = fmtTokens(total) + ' tokens · ' + src.active_days + ' active days';
  var peak = src.peak ? ' · Peak ' + fmtTokens(src.peak.tokens) + ' on ' + src.peak.date : '';
  return '<div class="heatmap-container"><div class="heat-scroll"><div class="heat-months">' + monthNames.map(function(m) { return '<span>' + esc(m) + '</span>'; }).join('') + '</div><div class="heat-layout"><div class="heat-weekdays"><span>Mon</span><span>Wed</span><span>Fri</span></div><div class="heatmap" role="group" aria-label="' + esc(srcName) + ' daily token activity">' + cells + '</div></div></div><div class="heatmap-meta"><span title="' + esc(summary + peak) + '">' + esc(summary) + '</span>' + scale + '</div></div>';
}

// ---- Attention rendering ----

// deviceCard renders the device-status card at the top of the Attention tab.
// It shows last seen, seconds since, observed poll interval, and the 200/304
// split — enough to prove whether deep sleep is occurring (ORDER #58 task 61).
// Device state comes from GET /v1/device (ORDER #66 task 67), polled on its
// own timer, NOT from the /v1/usage body (which is only present on 200, never
// on 304). When no device has checked in, the card shows a neutral "no data"
// state rather than an error — "waiting" is a valid state, not a failure.
function deviceCard(ds) {
  if (!ds) {
    return '<div class="device-accent">' +
      attentionItem("Device", "No data", "chip-stale",
        "Waiting for first check-in from StickS3") +
      '</div>';
  }

  // ORDER #61 task 64: this is the DEVICE's state (the StickS3 on the LAN),
  // not the browser's.  Name the address so there is never ambiguity about
  // whose state is on screen.
  var title = ds.addr ? ("Device " + ds.addr) : "Device";

  var since = ds.seconds_since;

  var chipClass, chipText;
  if (ds.state === "absent") {
    chipClass = "chip-critical";
    chipText = "Absent";
  } else if (ds.state === "connected") {
    chipClass = "chip-ok";
    chipText = "Awake";
  } else if (ds.state === "unknown") {
    chipClass = "chip-stale";
    chipText = "Unknown";
  } else {
    chipClass = "chip-stale";
    chipText = "Offline";
  }

  var parts = [];
  parts.push("last seen " + fmtTime(ds.last_seen));
  parts.push(since + "s ago");
  if (ds.interval_sec > 0) parts.push("~" + ds.interval_sec + "s poll");
  parts.push("200:" + ds.count_200 + " 304:" + ds.count_304);
  parts.push("state: " + (ds.state || "unknown"));
  if (!ds.ota_armed) parts.push("OTA: disarmed (USB only)");

  return '<div class="device-accent">' +
    attentionItem(title, chipText, chipClass, parts.join(" \u00b7 ")) +
    '</div>';
}

function renderAttention(snap, statsReport) {
  var container = document.getElementById("attention");
  container.innerHTML = "";

  // Device status card (ORDER #66 task 67): from GET /v1/device, polled on its
  // own timer (see pollDevice). Device state changes every second and must
  // never sit inside the ETag-cached /v1/usage body (only present on 200,
  // absent on 304). currentDeviceState is null until the first /v1/device
  // fetch completes.
  container.insertAdjacentHTML("beforeend", deviceCard(currentDeviceState));

  if (!snap) return;
  var count = snap.providers.filter(function(p) { return p.severity === 'warn' || p.severity === 'crit' || p.status === 'stale'; }).length;
  var counter = document.getElementById('attention-count');
  counter.textContent = count;
  counter.hidden = count === 0;
  if (!count) container.insertAdjacentHTML('beforeend', '<div class="empty-state">All clear. No provider warnings right now.</div>');

  snap.providers.forEach(function(p) {
    var maxPct = 0;
    for (var i = 0; i < (p.rows || []).length; i++) {
      if (p.rows[i].pct != null && p.rows[i].pct > maxPct) maxPct = p.rows[i].pct;
    }

    // Render from p.severity (the single threshold-driven source of truth
    // computed in the Go daemon). The page no longer re-derives 80/95
    // constants — see PROD-READY 2/10. pct stays as display text only.
    if (p.severity === "crit") {
      var detail = (p.status === "auth" || p.status === "error") ? "auth or error" : pctText(maxPct, "");
      container.insertAdjacentHTML("beforeend", attentionItem(p.label, "Critical", "chip-critical", detail));
    } else if (p.severity === "warn") {
      container.insertAdjacentHTML("beforeend", attentionItem(p.label, "Watch", "chip-watch",
        pctText(maxPct, "")));
    }
    if (p.status === "stale") {
      container.insertAdjacentHTML("beforeend", attentionItem(p.label, "Stale", "chip-stale",
        p.msg || ""));
    }
  });
}

function attentionItem(label, chipText, chipClass, detail) {
  return '<div class="attention-item">' + icon(label.indexOf('Device') === 0 ? 'device' : 'bell') +
    '<div class="attention-copy"><strong>' + esc(label) + '</strong><span class="attention-detail">' + esc(detail) + '</span></div>' +
    '<span class="chip ' + esc(chipClass) + '">' + esc(chipText) + '</span></div>';
}

