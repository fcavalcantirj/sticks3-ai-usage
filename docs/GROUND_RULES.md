# Ground Rules

These rules are the floor for every task. Violating any one fails the task.

## Go: stdlib only, with one approved exception

No external Go dependencies, except **`tinygo.org/x/bluetooth`** and what it drags in.
Felipe approved it explicitly ("go for it") for BLE zero-config provisioning: the daemon
must drive CoreBluetooth as a central to hand a freshly-flashed device its Wi-Fi
credentials, and the stdlib has no path to CoreBluetooth at all. `muka/go-bluetooth` was
refused (Linux/BlueZ only, archived July 2024). Adding any OTHER dependency is still a
task failure.

The gate asserts the SET, not a count — a swapped dependency that kept the count the same
would sail through a count. This must print exactly these four lines and nothing else:

```
go list -deps ./... | grep '^[^/]*\.[^/]*/' | cut -d/ -f1-3 | sort -u
github.com/sirupsen/logrus
github.com/tinygo-org/cbgo
golang.org/x/sys
tinygo.org/x/bluetooth
```

`go.sum` carries 13 modules but only those four compile on darwin; the rest are the
Linux and Windows radio backends, excluded by build tags.

**Do not use the old `go list -m all | wc -l` test — it no longer runs.** It fails with
`github.com/tdakkota/win32metadata@v0.1.0: ... remote: Repository not found`, a dead
upstream repo in `saltosystems/winrt-go`'s graph (the Windows BLE backend). Nothing on
darwin reaches it: `go build`, `go vet`, `go test` and `go mod download` are all clean.
Only the whole-graph walk touches it. [REAL, 2026-09-07]

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
