# AGENTS.md — StickS3 AI-usage monitor (`usaged`)

## Purpose

`usaged` is a lean Go service that polls AI-usage APIs (Claude, Codex, OpenRouter, Groq),
persists a canonical snapshot, and serves it to a local web page and eventually an M5StickS3.

The founder owns the credentials; this repo only reads them through the Keychain and
`~/.codex/auth.json`.

## Project tree

```
cmd/usaged/          # CLI entry: serve, once, version
internal/
  config/            # env vars + flags, .env.example loading, fixtures mode
  snapshot/          # v1 types, rev hash, state persistence
  creds/             # Keychain (Claude) + auth.json (Codex) readers, injectable runner
  httpx/             # HTTP client wrapper with fixture transport, Retry-After
  format/            # timezone-aware reset text, tiers, money
  providers/         # Claude, Codex, OpenRouter, Groq fetchers
  sched/             # concurrent scheduler, last-good, cooldowns, state save
  api/               # HTTP API: /healthz, /v1/usage (ETag/304), /v1/refresh, /v1/rows
  web/               # embedded single-file dashboard
testdata/fixtures/   # captured API responses for tests (claude_usage.json, codex_usage.json, …)
scripts/             # run.sh, install.sh, uninstall.sh, smoke.sh
launchd/             # com.fcavalcanti.usaged.plist
firmware/            # PlatformIO M5StickS3 project
sticks3-ai-usage.md  # OLD research report — reference only, not a spec
```

## Build / test commands

```
make verify          # fmt + vet + staticcheck + test + build  (fast standing gate)
make verify-all      # verify + fw-test + fw-build
go test ./...        # all Go tests (no network)
go run ./cmd/usaged once --fixtures testdata/fixtures
```

## House rules (verbatim)

1. NEVER edit ~/.zshrc, ~/.zprofile, ~/.claude/settings.json, ~/.codex/*, the macOS Keychain,
   or any file named `.env` — the builder may create/edit `.env.example` only; the founder fills `.env`.
2. NEVER refresh, rotate, export or copy OAuth tokens: the Claude token is READ from the Keychain
   item "Claude Code-credentials" and the Codex token is READ from ~/.codex/auth.json; on expiry the
   UI shows a badge ("run claude" / "run codex"), nothing else. Never set CLAUDE_CODE_OAUTH_TOKEN
   (claude-code#37512: it deletes the Keychain item).
3. Tests never touch the network or real credentials: every fetcher is exercised through the fixture
   transport in testdata/fixtures; live calls only behind USAGED_LIVE=1 and only the founder runs them.
4. Before starting any server process, kill previous instances
   (`pkill -f 'usaged serve' || true`; `launchctl bootout gui/$(id -u)/com.fcavalcanti.usaged 2>/dev/null || true`).
5. Never log or print token values; log only lengths/prefixes (7 chars).
6. Device uploads (pio -t upload / espota) require the founder's explicit authorization for that
   specific upload — builds, tests and monitors never authorize one.
7. Commit only via the loop's own `git add . && git commit` at the end of a task; never push.

## Endpoint provenance

- **Claude**: `GET https://api.anthropic.com/api/oauth/usage` — UNOFFICIAL endpoint. Credential
  from Keychain item "Claude Code-credentials". Claude Code owns token refresh. Risks: 429 lockouts
  (~24 h) and the Feb-2026 credential policy restricts to read-only single-user.
- **Codex**: `GET https://chatgpt.com/backend-api/wham/usage` — UNOFFICIAL endpoint. Credential from
  `~/.codex/auth.json`. Codex CLI owns token refresh (JWT rotated on each use) — this repo NEVER
  refreshes. Risks: refresh-token rotation invalidates any manually-copied token.
- **Admin/Usage APIs** (Anthropic, OpenAI): OUT OF SCOPE — no admin keys; founder decision.

## Loop rules

One task at a time, in `spec.json` ledger order. Each task ends by running its own Verify steps.
`passes` is flipped to true only on real evidence. `progress.txt` is append-only.
