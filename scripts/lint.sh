#!/bin/bash
# scripts/lint.sh — run staticcheck with graceful degradation.
# - absent binary: informational message, exit 0
# - built with older Go (version mismatch): skip, exit 0
# - real findings: print output, exit non-zero (turns make verify red)
SC="$HOME/go/bin/staticcheck"

if [ ! -x "$SC" ]; then
	echo "staticcheck: not installed (go install honnef.co/go/tools/cmd/staticcheck@latest)"
	exit 0
fi

output=$("$SC" ./... 2>&1)
rc=$?

if [ "$rc" -eq 0 ]; then
	[ -n "$output" ] && echo "$output"
	exit 0
fi

if echo "$output" | grep -q "but Staticcheck was built with"; then
	echo "staticcheck: too old for go1.26, skipped (reinstall to enable)"
	exit 0
fi

echo "$output"
exit "$rc"
