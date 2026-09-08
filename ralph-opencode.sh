#!/bin/bash
# ralph-opencode.sh — Ralph loop on the OpenCode Zen engine.
#   ./ralph-opencode.sh 3        # up to three tasks
cd "$(dirname "$0")" || exit 1
ENGINE=opencode exec ./ralph.sh "$@"
