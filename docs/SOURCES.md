# Sources & Endpoint Provenance

All endpoints verified live on 2026-09-02 with the founder's real accounts.
Every fetcher is exercised through the fixture transport in `testdata/fixtures`;
live calls only happen behind `USAGED_LIVE=1` and only the founder runs them.

| Provider | Endpoint | Official? | Credential source | Refresh owner | Known risks |
|---|---|---|---|---|---|
| Claude | `GET https://api.anthropic.com/api/oauth/usage` (headers: `Authorization: Bearer <token>`, `anthropic-beta: oauth-2025-04-20`, `Accept: application/json`) | Unofficial | macOS Keychain item "Claude Code-credentials" (read via `security find-generic-password -s "Claude Code-credentials" -a <username> -w`), JSON `claudeAiOauth` block | Claude Code rotates the access token (~every 8 h) | 429 lockouts (~24 h) if polled too often; poll at most every 300 s, honor Retry-After; Feb-2026 credential policy requires `anthropic-beta: oauth-2025-04-20`; read-only, single-user only |
| Codex | `GET https://chatgpt.com/backend-api/wham/usage` (headers: `Authorization: Bearer <token>`, `ChatGPT-Account-Id: <account_id>`, `Accept: application/json`, `User-Agent: codex-cli`) | Unofficial | `~/.codex/auth.json` (JSON: `auth_mode`, `tokens.access_token`, `tokens.account_id`), access token is a 10-day-life JWT with `exp` claim | Codex CLI rotates the refresh token on each use | refresh-token rotation invalidates any manually-copied token → never refresh; windows identified by `limit_window_seconds` (18000 = 5h, 604800 = 7d), never by primary/secondary position; `used_percent` is USED; never POST to /wham/rate-limit-reset-credits/consume |
| OpenRouter | `GET https://openrouter.ai/api/v1/credits` → `data.total_credits` / `data.total_usage` and `GET https://openrouter.ai/api/v1/key` → `data.usage_daily`/`weekly`/`monthly`, `data.limit`, `data.limit_remaining` | Official (200 on regular keys despite docs saying management key) | `OPENROUTER_API_KEY` (main) + `OPENROUTER_API_KEY_FALLBACK` (fallback), in `.env` | n/a (static API keys) | /credits may return 401/403 → fall back to /key figures only; balance computed as `total_credits - total_usage` |
| Groq | No usage/billing API exists (7 candidate endpoints → 404). Only `x-ratelimit-*` headers on inference POSTs (`x-ratelimit-limit-requests`, `x-ratelimit-remaining-requests`, etc.); `GET https://api.groq.com/openai/v1/models` proves the key is valid | Official | `GROQ_API_KEY` in `.env` | n/a (static API key) | key-valid check is the ceiling; rate-limit headroom probe optional behind `USAGED_GROQ_PROBE=1`; duration format parsing (`172ms`, `2m59.56s`, `7.66s`) |
| Anthropic/OpenAI Admin | Admin usage/cost APIs | OUT OF SCOPE | n/a | n/a | No admin keys available; founder decision not to pursue. |

## Policy

Both quota endpoints (Claude `oauth/usage`, Codex `wham/usage`) are **unofficial**
and **read-only**. This project is **single-user**: each founder runs their own
`usaged` behind their own Keychain/Codex credentials. No token ever leaves the
Mac — the service reads from the Keychain (`Claude Code-credentials`) and
`~/.codex/auth.json` and writes only token lengths/prefixes (7 chars) to logs.
**No token refresh, rotation, export, or copy** is ever performed: Claude Code
and the Codex CLI own token lifecycle. Anthropic/OpenAI Admin usage APIs are
out of scope (no admin keys, founder decision). The service polls at 900 s and
honors `Retry-After` to avoid 429 lockouts.

## Attribution

Idioms and patterns lifted from:

- **sheepxux/Taskhub-for-StickS3** (MIT) — serial protocol line format,
  deep-sleep wake-source configuration, PM1 GPIO rail-cut teardown sequence,
  RTC_DATA_ATTR snapshot persistence pattern, `powerDecide` state-machine shape.
- **Colibrino/home-automations** (founder Felipe's own, MIT) — `ptt.ino:191-211`
  teardown sequence (ES8311 0x18:0x0D/0x0E/0x00, BMI270 0x68:0x7D/0x7C, PM1 GPIO3/GPIO2
  rail cut, ext0 GPIO13 IRQ, ext1 GPIO11/12, timer backstop, `esp_deep_sleep_start`
  lineage, `M5.Power.getVBUSVoltage() > 4000` VBUS test, re-arm IRQ on every boot).
- **ArduinoJson** (MIT) — `firmware/third_party/ArduinoJson.h` vendored single-header
  v7.4.2 for snapshot parsing on the ESP32-S3.
