# Ground Rules

These rules are the floor for every task. Violating any one fails the task.

## Go: stdlib only

No external Go dependencies. `go list -m all | wc -l` must print 1 (just `usaged` itself).
`staticcheck` (at ~/go/bin/staticcheck) is the linter; it has zero false-positive tolerance here.

## One HTTP client

Every outbound HTTP call goes through `internal/httpx.Client`. Tests swap the transport
to serve fixtures. No bare `http.Get` / `http.DefaultClient` in production paths.

## Credentials via interfaces

Every credential read goes through an injectable function or interface so tests can
substitute fixtures. No direct `os.ReadFile` of `~/.codex/auth.json` or `security`
calls inside provider logic.

## No goroutine leaks

Every background loop (scheduler, HTTP server, etc.) takes a `context.Context`.
`go test -race ./...` must be green.

## No secrets in logs

Token values are never logged. At most, log the length or a 7-char prefix.
`slog` with a custom `Redacted()` config map is the pattern.

## Snapshot v1 contract is immutable

JSON field names (`v`, `seq`, `rev`, `generated_at`, `checked_at`, `next_sec`,
`providers`, `id`, `label`, `plan`, `status`, `msg`, `rows`, `k`, `pct`, `txt`,
`tier`, `reset_at`, `fetched_at`) must not change without bumping `v`.

## TDD discipline

Each task writes a failing test first (red), then the minimum code to pass (green),
then refactors with tests still green. Coverage floor: 80% per package.
