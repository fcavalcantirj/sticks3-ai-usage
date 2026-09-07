#!/bin/bash
# scripts/smoke.sh — out-of-process smoke test for `usaged serve` against fixtures.
set -euo pipefail

PORT=18765
BASE="http://127.0.0.1:${PORT}"
STATE_FILE="/tmp/usaged-smoke-state.json"

# Kill any previous *test* instance on our port. Never touch
# com.fcavalcanti.usaged — Felipe's status line and dashboard depend on it.
pkill -f "usaged serve --fixtures" 2>/dev/null || true
rm -f "$STATE_FILE"

# Dummy keys so OpenRouter and Groq render as real (ok) providers in fixtures
# mode, exercising the full 5-provider snapshot.
export OPENROUTER_API_KEY=x
export OPENROUTER_API_KEY_FALLBACK=fx
export GROQ_API_KEY=gx

# Device token for mutating routes (POST/PUT/DELETE /v1/* now require a token
# even from loopback — ORDER #54 task 59).
export USAGED_DEVICE_TOKEN="smoke-test-token"

# Start the server in the background.
bin/usaged serve --fixtures testdata/fixtures --listen "127.0.0.1:${PORT}" --state "$STATE_FILE" &
SERVER_PID=$!

cleanup() {
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Wait for /healthz (20 × 0.25 s).
for _ in $(seq 1 20); do
    if curl -sf "${BASE}/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done

# /v1/usage → 200 with ETag.
USAGE_RESP=$(curl -si "${BASE}/v1/usage")
if ! echo "$USAGE_RESP" | grep -q "HTTP/1.1 200"; then
    echo "FAIL: /v1/usage did not return 200"
    echo "$USAGE_RESP"
    exit 1
fi
if ! echo "$USAGE_RESP" | grep -qi 'ETag: "'; then
    echo "FAIL: /v1/usage missing ETag header"
    echo "$USAGE_RESP"
    exit 1
fi

# With dummy keys exported, /v1/usage must include openrouter:main and groq.
USAGE_BODY=$(curl -s "${BASE}/v1/usage")
echo "$USAGE_BODY" | grep -q '"id":"openrouter:main"' || {
    echo "FAIL: /v1/usage missing openrouter:main"
    echo "$USAGE_BODY"
    exit 1
}
echo "$USAGE_BODY" | grep -q '"id":"groq"' || {
    echo "FAIL: /v1/usage missing groq"
    echo "$USAGE_BODY"
    exit 1
}

# Extract ETag and test 304: same ETag, empty body (no Content-Length assertion).
ETAG=$(echo "$USAGE_RESP" | grep -i '^ETag:' | sed 's/^ETag: //I' | tr -d '\r')
NOT_MOD_RESP=$(curl -si -H "If-None-Match: ${ETAG}" "${BASE}/v1/usage")
if ! echo "$NOT_MOD_RESP" | grep -q "HTTP/1.1 304"; then
    echo "FAIL: If-None-Match did not return 304"
    echo "$NOT_MOD_RESP"
    exit 1
fi
if ! echo "$NOT_MOD_RESP" | grep -qi "ETag:"; then
    echo "FAIL: 304 response missing ETag header"
    echo "$NOT_MOD_RESP"
    exit 1
fi
BODY_SIZE=$(curl -s -o /dev/null -w '%{size_download}' -H "If-None-Match: ${ETAG}" "${BASE}/v1/usage")
if [ "$BODY_SIZE" != "0" ]; then
    echo "FAIL: 304 response body is not empty (size=$BODY_SIZE)"
    exit 1
fi

# POST /v1/refresh → 200. Requires X-Device-Token (ORDER #54 task 59).
REFRESH_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST -H "X-Device-Token: ${USAGED_DEVICE_TOKEN}" "${BASE}/v1/refresh")
if [ "$REFRESH_CODE" != "200" ]; then
    echo "FAIL: POST /v1/refresh returned $REFRESH_CODE, want 200"
    exit 1
fi

# GET / → 200 text/html containing "AI Usage".
ROOT_COUNT=$(curl -s "${BASE}/" | grep -c 'AI Usage')
if [ "$ROOT_COUNT" -lt 1 ]; then
    echo "FAIL: GET / does not contain 'AI Usage'"
    exit 1
fi

# Kill the base server before running scenario checks.
kill "$SERVER_PID" 2>/dev/null || true
wait "$SERVER_PID" 2>/dev/null || true

# Scenario loop: start the server per scenario on port 18766 and assert the
# expected provider status string appears in /v1/usage.
SCENARIO_PORT=18767
SCENARIO_BASE="http://127.0.0.1:${SCENARIO_PORT}"

# run_scenario starts the server with the given scenario and asserts that the
# expected grep pattern appears in the /v1/usage JSON.
run_scenario() {
    local name="$1"
    local expect="$2"
    local state="/tmp/usaged-smoke-${name}.json"
    rm -f "$state"

    bin/usaged serve --fixtures testdata/fixtures --scenario "$name" \
        --listen "127.0.0.1:${SCENARIO_PORT}" --state "$state" &
    local pid=$!

    # Wait for /healthz.
    for _ in $(seq 1 20); do
        if curl -sf "${SCENARIO_BASE}/healthz" >/dev/null 2>&1; then
            break
        fi
        sleep 0.25
    done

    local body
    body=$(curl -s "${SCENARIO_BASE}/v1/usage")
    if ! echo "$body" | grep -q "$expect"; then
        echo "FAIL: scenario $name expected '$expect' in response, got:"
        echo "$body"
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        return 1
    fi

    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$state"
    echo "OK: scenario $name"
}

run_scenario "claude-401"     '"status":"auth"'
run_scenario "claude-429"     '"status":"error"'
run_scenario "codex-expired"  '"status":"auth"'
run_scenario "all-down"      '"status":"error"\|"status":"stale"'

# stats-demo: the dashboard at / must reference the stats heatmap element IDs
# when served with --scenario stats-demo.
STATS_STATE="/tmp/usaged-smoke-stats-demo.json"
rm -f "$STATS_STATE"
bin/usaged serve --fixtures testdata/fixtures --scenario stats-demo \
    --listen "127.0.0.1:${SCENARIO_PORT}" --state "$STATS_STATE" &
STATS_PID=$!
for _ in $(seq 1 20); do
    if curl -sf "${SCENARIO_BASE}/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
STATS_BODY=$(curl -s "${SCENARIO_BASE}/")
# Match in-shell rather than through a pipe. The dashboard passed 64 KiB when
# the Wi-Fi card landed (task 79), and `echo … | grep -q` then races the pipe
# buffer: grep matches at offset ~16 KB and exits, echo dies with EPIPE, and
# `set -o pipefail` reports that as a missing element. Reproduced 2 runs in 5.
if [[ "$STATS_BODY" != *heatmap-codex* ]]; then
    echo "FAIL: stats-demo dashboard missing heatmap-codex id"
    kill "$STATS_PID" 2>/dev/null || true
    wait "$STATS_PID" 2>/dev/null || true
    rm -f "$STATS_STATE"
    exit 1
fi
kill "$STATS_PID" 2>/dev/null || true
wait "$STATS_PID" 2>/dev/null || true
rm -f "$STATS_STATE"
echo "OK: scenario stats-demo"

echo "SMOKE OK"
