# AGENTS.md — StickS3 AI-usage monitor (`usaged`)

## Purpose

`usaged` is a lean Go service that polls AI-usage APIs (Claude, Codex, OpenRouter,
Groq), persists a canonical snapshot, and serves it to a local web page and an
M5StickS3 display. The founder owns the credentials; this repo only reads them
through the Keychain and `~/.codex/auth.json`.

## Project tree

```
cmd/usaged/            CLI: serve, once, version
internal/
  config/              env vars + flags, .env.example loading, fixtures mode
  snapshot/            v1 types, rev hash, state persistence
  creds/               Keychain (Claude) + auth.json (Codex) readers, injectable runner
  httpx/               HTTP client wrapper with fixture transport, Retry-After
  format/              timezone-aware reset text, tiers, money, table rendering
  providers/           Claude, Codex, OpenRouter, Groq fetchers
  sched/               concurrent scheduler, last-good, cooldowns, state save
  api/                 HTTP routes: /healthz /v1/usage /v1/usage.txt /v1/refresh /
  web/                 embedded single-file dashboard (go:embed)
  stats/               local transcript scanner for /v1/stats (tasks 44+)
testdata/              fixtures (claude/codex/openrouter/groq JSON), scenarios
scripts/               run.sh, install.sh, uninstall.sh, smoke.sh, lint.sh
launchd/               com.fcavalcanti.usaged.plist
firmware/              PlatformIO M5StickS3 project (board.h/screen.h/net.h/fetch.h/power.h)
  src/usage/           pure C++17 core (model, power, render_plan, serial_proto, textfit)
  src/hal/sticks3/     M5/Arduino HAL (board, screen, net, fetch, power)
  test/host/           CMake host-test harness + framework.h
docs/                  DEVICES.md, GROUND_RULES.md, SOAK.md, SOURCES.md
README.md              user-facing docs
sticks3-ai-usage.md    OLD research report — reference only, not a spec
```

## Build / test commands

```
make verify       # fmt + vet + staticcheck + test + build
make verify-all   # verify + fw-test + fw-build
make build        # go build → bin/usaged
make test         # go test ./... (no network)
make smoke        # build + scripts/smoke.sh (live HTTP checks)
make fw-test      # cmake host tests (firmware/test/host)
make fw-build     # pio run (firmware)
make fw-ota       # OTA upload to unit #2 (MAC-guarded)
```
Fresh-clone gate: `git clone . /tmp/c && make verify-all` from `/tmp/c`.

## Serial protocol (firmware)

One bracketed tag per line. See `firmware/README.md` for the full table.

```
[BOOT]  board=26 psram=8386231 build=abc123 fw=1.0.0
[WAKE]  cause=power_on vbus=5282          # after deep-sleep wake
[NET]   state=connected ip=192.168.0.136   # state transitions
[FETCH] code=200 rev=abcd1234 seq=1 ms=310  # new data → [RENDER]
[FETCH] code=304 rev=abcd1234 ms=120       # unchanged, no redraw
[RENDER] page=1 lines=5 rev=abcd1234      # after redraw
[HEAP]  free=281168 min=275316            # 60 s watchdog
[SLEEP] reason=battery                    # before deep sleep
[OTA]   start / pct=50 / end / err=<code>  # OTA progress
[ERR]   <what>                            # parse or fatal errors
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

- **Claude**: `GET https://api.anthropic.com/api/oauth/usage` — UNOFFICIAL.
  Credential from Keychain item "Claude Code-credentials". Claude Code owns token refresh.
- **Codex**: `GET https://chatgpt.com/backend-api/wham/usage` — UNOFFICIAL.
  Credential from `~/.codex/auth.json`. Codex CLI owns token refresh.
- **OpenRouter**: `GET /api/v1/credits` + `GET /api/v1/key` — Official (static API keys in `.env`).
- **Groq**: `GET /openai/v1/models` proves key validity; rate limits from `x-ratelimit-*` on POST.
- **Admin/Usage APIs** (Anthropic, OpenAI): OUT OF SCOPE — no admin keys; founder decision.

## Loop rules

One task at a time, in `spec.json` ledger order. Each task ends by running its own Verify steps.
`passes` is flipped to true only on real evidence. `progress.txt` is append-only.
Every firmware task: fresh-clone build check (`git clone . /tmp/c && make verify-all`).
Every task: read the last 20 room messages before starting.

## Status

Ledger: 51 tasks, 41 passed. Optional v3 tasks (48-51) not started. Task 40 UAT-pending (source
committed, hardware unplug/replug awaiting founder). See `progress.txt` Reconciliation block.
