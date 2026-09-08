#!/bin/bash
# ralph-codex.sh — Ralph loop on the Codex CLI engine.
#   ./ralph-codex.sh 3        # up to three tasks
cd "$(dirname "$0")" || exit 1
ENGINE=codex exec ./ralph.sh "$@"
