#!/bin/bash
# scripts/smoke.sh — out-of-process smoke test for `usaged serve` against fixtures.
set -euo pipefail

PORT=18765
BASE="http://127.0.0.1:${PORT}"
STATE_FILE="/tmp/usaged-smoke-state.json"

# Kill any previous instance.
pkill -f 'usaged serve' || true
rm -f "$STATE_FILE"

# Start the server in the background.
bin/usaged serve --fixtures testdata/fixtures --listen 127.0.0.1:${PORT} --state "$STATE_FILE" &
SERVER_PID=$!

cleanup() {
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

# Wait for /healthz (20 × 0.25 s).
for i in $(seq 1 20); do
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

# POST /v1/refresh → 200.
REFRESH_CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${BASE}/v1/refresh")
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
SCENARIO_PORT=18766
SCENARIO_BASE="http://127.0.0.1:${SCENARIO_PORT}"

run_scenario() {
    local name="$1"
    local expect="$2"
    local state="/tmp/usaged-smoke-${name}.json"
    rm -f "$state"

    bin/usaged serve --fixtures testdata/fixtures --scenario "$name" \
        --listen 127.0.0.1:${SCENARIO_PORT} --state "$state" &
    local pid=$!

    # Wait for /healthz.
    for i in $(seq 1 20); do
        if curl -sf "${SCENARIO_BASE}/healthz" >/dev/null 2>&1; then
            break
        fi
        sleep 0.25
    done

    local body
    body=$(curl -s "${SCENARIO_BASE}/v1/usage")
    if ! echo "$body" | grep -q "\"status\":\"$expect\""; then
        echo "FAIL: scenario $name expected status \"$expect\", got:"
        echo "$body"
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        return 1
    fi

    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
    rm -f "$state"
    echo "OK: scenario $name ($expect)"
}

# run_scenario starts the server with the given scenario and asserts that the
# expected status string appears in the /v1/usage JSON.
run_scenario() {
    local name="$1"
    local expect="$2"
    local state="/tmp/usaged-smoke-${name}.json"
    rm -f "$state"

    bin/usaged serve --fixtures testdata/fixtures --scenario "$name" \
        --listen 127.0.0.1:${SCENARIO_PORT} --state "$state" &
    local pid=$!

    # Wait for /healthz.
    for i in $(seq 1 20); do
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

echo "SMOKE OK"
