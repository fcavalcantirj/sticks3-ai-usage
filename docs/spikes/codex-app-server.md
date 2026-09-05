# Spike: `codex app-server` JSON-RPC

**Date**: 2026-09-05
**Codex CLI**: 0.153.3 at `~/.local/bin/codex`
**Objective**: Probe `account/rateLimits/read` via the app-server's stdio JSON-RPC transport to see if it can replace the wham/usage endpoint as the Codex rate-limit data source.

## Command

```
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"account/rateLimits/read"}' | codex -s read-only -a never app-server
```

## Verdict

| Question | Answer |
|---|---|
| Same windows as wham/usage? | **Yes** — 5-hour primary (`windowDurationMins: 300`) and 7-day secondary (`windowDurationMins: 10080`), plus `planType: "plus"` |
| Refreshes `~/.codex/auth.json` by itself? | **No** — mtime unchanged before/after |
| Latency | ~3.1 s (including initialize handshake) |
| Requires initialize handshake? | **Yes** — the bare pipe produces no output; a JSON-RPC `initialize` message must be sent first |

## Response shape

The app-server speaks JSON-RPC over stdio. An `initialize` handshake is required before any method call. The server also emits server-initiated notifications (e.g. `remoteControl/status/changed`).

### Initialize handshake (required)

```json
{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"usaged-spike","version":"1.0"}}}
```

Response:
```json
{"id":0,"result":{"userAgent":"usaged-spike/0.153.3","codexHome":"/Users/fcavalcanti/.codex","platformFamily":"unix","platformOs":"macos"}}
```

### `account/rateLimits/read`

Request:
```json
{"jsonrpc":"2.0","id":1,"method":"account/rateLimits/read"}
```

Response (truncated for brevity; full response captured at seq 260 of the Solvr room):
```json
{
  "id": 1,
  "result": {
    "rateLimits": {
      "limitId": "codex",
      "limitName": null,
      "primary": {
        "usedPercent": 0,
        "windowDurationMins": 300,
        "resetsAt": 1788605284
      },
      "secondary": {
        "usedPercent": 100,
        "windowDurationMins": 10080,
        "resetsAt": 1788969665
      },
      "credits": {
        "hasCredits": false,
        "unlimited": false,
        "balance": "0"
      },
      "individualLimit": null,
      "spendControlReached": false,
      "planType": "plus",
      "rateLimitReachedType": "rate_limit_reached"
    },
    "rateLimitsByLimitId": { "codex": { ...same as above... } },
    "rateLimitResetCredits": {
      "availableCount": 2,
      "credits": [
        {"id":"RateLimitResetCredit_…","resetType":"codexRateLimits","status":"available",
         "grantedAt":1788498845,"expiresAt":1791090845,
         "title":"Full reset (Weekly + 5 hr)"}
      ]
    },
    "accountId": "2d64a552-c83d-42c0-b69e-9789f58ab26a",
    "rateLimitUpsell": {
      "banner_type": "plus_rate_limit_reached",
      "title": "You're out of Codex and Work usage",
      "description": "Use your banked reset, or save it for later and pay $8 to reset your usage limits now",
      "ctas": [...],
      "reset_at": 1788969665
    }
  }
}
```

## Comparison with wham/usage

The existing `internal/providers/codex.go` fetches `/api/v0/wham/usage` and reads rate-limit windows from `x-ratelimit-*` headers. The app-server returns the same windowing (300 min / 10080 min) directly in the JSON body, plus `planType`, `accountId`, and reset-credit data. The fields map cleanly:

| wham/usage header | app-server field |
|---|---|
| `limit_window_seconds` → 300 min | `rateLimits.primary.windowDurationMins: 300` |
| `limit_window_seconds` → 10080 min | `rateLimits.secondary.windowDurationMins: 10080` |
| `used_percentage` | `rateLimits.primary/secondary.usedPercent` |
| reset epoch | `rateLimits.primary/secondary.resetsAt` |
| plan (from token) | `rateLimits.planType` |

## Conclusion

Viable. The app-server returns the same rate-limit windows and plan type in a structured JSON body, with no auth.json side-effects. The only operational caveat is the initialize handshake. An alternative fetcher (`USAGED_CODEX_SOURCE=cli`) can start the app-server, send the handshake, issue `account/rateLimits/read`, and normalise the result into `snapshot.Provider`.
