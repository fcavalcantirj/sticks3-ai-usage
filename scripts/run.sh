#!/bin/bash
# scripts/run.sh — LaunchAgent entry point for ai-usage.
# Sources the repo .env (if present) to populate USAGED_* env vars and API keys,
# then execs the built binary in the foreground so launchd can supervise it.
set -euo pipefail

cd "$(dirname "$0")/.."

if [ -f .env ]; then
    set -a
    source .env
    set +a
else
    echo "run.sh: .env not found; using defaults (edit .env.example for secrets)" >&2
fi

exec ./bin/ai-usage serve
