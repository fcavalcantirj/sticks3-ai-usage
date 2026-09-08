#!/bin/bash
set -e

# ─────────────────────────────────────────────────────────────────────────────
# ralph.sh — bounded per-task build loop for ai-usage (engine-agnostic core).
#
# Runs a headless coding agent N times. Each run: pick the first spec.json
# task with passes=false, do ONLY that task (tests-first where sensible), run
# the task's own "Verify:" steps, flip passes=true, journal to progress.txt,
# commit, then STOP. Statelessness means every run re-reads the JSON and picks
# up the next undone task.
#
# Engines (pick via wrapper script or ENGINE env var):
#   ./ralph-opencode.sh 3      # OpenCode Zen
#   ./ralph-claude.sh 1        # Claude Code CLI (reports tokens + cost)
#   ./ralph-codex.sh 1         # Codex CLI, gpt-5.6-sol
#
# Knobs (env or git-ignored .env.ralph.local):
#   ENGINE=claude|codex|opencode   MODEL=<claude pin>
#   CODEX_MODEL=gpt-5.6-sol   CODEX_EFFORT=max
#   OPENCODE_MODEL=opencode/muse-spark-1.3-contributor-free   RALPH_PUSH=0
#   PRD_FILE=spec.json   VERIFY_CMD=<host verify command, e.g. "npm run verify">
# ─────────────────────────────────────────────────────────────────────────────

# Load git-ignored local config if present (provider keys, RALPH_PUSH, MODEL…)
if [ -f .env.ralph.local ]; then set -a; . ./.env.ralph.local; set +a; fi

ENGINE="${ENGINE:-opencode}"   # all three wrappers are rendered; ENGINE= still overrides
PRD_FILE="${PRD_FILE:-spec.json}"
RALPH_PUSH="${RALPH_PUSH:-0}"          # 1 = git push after each task (needs a remote)
MODEL="${MODEL:-}"                     # optional Claude model pin
CODEX_MODEL="${CODEX_MODEL:-gpt-5.6-sol}"
CODEX_EFFORT="${CODEX_EFFORT:-max}"
CUSTOM_MODEL="${CUSTOM_MODEL:-opencode/muse-spark-1.3-contributor-free}"   # --custom-engine--
CUSTOM_EFFORT="${CUSTOM_EFFORT:-}"     # --custom-engine-- empty = don't send reasoning effort
VERIFY_CMD="${VERIFY_CMD:-}"           # host-side batch verify (empty = off); runs OUTSIDE the engine sandbox

case "$ENGINE" in
  claude|codex|opencode) ;;
  *) echo "Unknown ENGINE '$ENGINE' (expected claude, codex, or opencode)"; exit 1 ;;
esac

# opencode guard: credentials live in opencode's own auth store (`opencode auth`),
# not in an env var, so probe the CLI instead of a key. OPENCODE_MODEL is
# overridable from the environment so a rate-limited free model can be swapped
# without editing this script.
OPENCODE_MODEL="${OPENCODE_MODEL:-$CUSTOM_MODEL}"
if [ "$ENGINE" = "opencode" ]; then
  if ! command -v opencode >/dev/null 2>&1; then
    echo "ENGINE=opencode requires the opencode CLI on PATH"
    exit 1
  fi
  if ! opencode models >/dev/null 2>&1; then
    echo "opencode is installed but not authenticated — run: opencode auth login"
    exit 1
  fi
fi

MODEL_FLAG=""
if [ -n "$MODEL" ]; then
  MODEL_FLAG="--model $MODEL"
fi

# Colors
CYAN='\033[0;36m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'
MAGENTA='\033[0;35m'; BLUE='\033[0;34m'; RED='\033[0;31m'
BOLD='\033[1m'; DIM='\033[2m'; NC='\033[0m'

# Push instruction injected into the prompt based on RALPH_PUSH.
if [ "$RALPH_PUSH" = "1" ]; then
  PUSH_STEP="8. PUSH: run 'git push' to publish the commit (only if a git remote exists)."
else
  PUSH_STEP="8. Do NOT push — leave the commit local (RALPH_PUSH=0)."
fi

format_time() {
  local secs=$1
  printf "%02d:%02d:%02d" $((secs/3600)) $((secs%3600/60)) $((secs%60))
}

# Host-side verification (runs OUTSIDE the engine sandbox, once per batch).
# The engine's sandbox can silently skip checks it cannot run (port binds,
# browsers, git) — this is the ground truth. On failure: journal the tail to
# progress.txt and inject ONE URGENT ledger task (deduped against open tasks).
run_host_verify() {
  [ -n "$VERIFY_CMD" ] || return 0
  echo -e "${CYAN}🔍 Host verify: ${VERIFY_CMD}${NC}"
  local vout
  vout=$(mktemp)
  if bash -c "$VERIFY_CMD" > "$vout" 2>&1; then
    echo -e "${GREEN}✅ Host verify passed${NC}"
    rm -f "$vout"
    return 0
  fi
  echo -e "${RED}${BOLD}❌ Host verify FAILED — output tail:${NC}"
  tail -20 "$vout"
  {
    echo ""
    echo "$(date '+%Y-%m-%d %H:%M'): HOST VERIFY FAILED — \`$VERIFY_CMD\` exited nonzero. Tail:"
    tail -20 "$vout" | sed 's/^/    /'
  } >> progress.txt
  if command -v jq >/dev/null 2>&1; then
    local desc="URGENT: host verification failed — run '$VERIFY_CMD' on the host, fix every failure, re-run until it exits 0"
    if ! jq -e --arg d "$desc" 'any(.[]; .description == $d and .passes == false)' "$PRD_FILE" >/dev/null 2>&1; then
      local tprd
      tprd=$(mktemp)
      if jq --arg d "$desc" --arg cmd "$VERIFY_CMD" \
        '[{category: "infra", description: $d,
           steps: [("Run on the host: " + $cmd + " and read every failure"),
                   "Fix the root causes — do NOT weaken, skip, or sandbox-attest the checks",
                   ("Re-run " + $cmd + " until it exits 0")],
           passes: false}] + .' "$PRD_FILE" > "$tprd"; then
        mv "$tprd" "$PRD_FILE"
        echo -e "${YELLOW}⚠️  Injected URGENT task at top of ${PRD_FILE}${NC}"
      else
        rm -f "$tprd"
      fi
    fi
  else
    echo -e "${YELLOW}⚠️  jq not found — cannot inject URGENT task; see progress.txt${NC}"
  fi
  rm -f "$vout"
  return 1
}

if [ -z "${1:-}" ] || ! [ "$1" -ge 1 ] 2>/dev/null; then
  echo "Usage: $0 <iterations>"
  exit 1
fi

# Host git backstop: some engine sandboxes cannot create .git (the fuguFaces
# overnight run finished 45 tasks with zero commits). Sits after the usage
# check so a bare ./ralph.sh stays side-effect-free.
if [ ! -d .git ] && command -v git >/dev/null 2>&1; then
  git init -b main >/dev/null 2>&1 || git init >/dev/null 2>&1
  echo -e "${DIM}Initialized git repo (host backstop).${NC}"
fi

case "$ENGINE" in
  claude) ENGINE_DESC="claude (${MODEL:-CLI default})"; ENGINE_CMD="claude" ;;
  codex)  ENGINE_DESC="codex (${CODEX_MODEL}, effort ${CODEX_EFFORT})"; ENGINE_CMD="codex" ;;
  opencode) ENGINE_DESC="opencode (${OPENCODE_MODEL})"; ENGINE_CMD="opencode" ;;
esac

echo -e "${DIM}PRD: ${PRD_FILE}   engine: ${ENGINE_DESC}   push: ${RALPH_PUSH}${NC}"
# Show only the current engine's agent processes (claude -> claude, codex-based -> codex).
running_pids=$(pgrep -il "$ENGINE_CMD" 2>/dev/null || true)
if [ -n "$running_pids" ]; then
  echo -e "${DIM}Running ${ENGINE_CMD} processes:${NC}"
  echo "$running_pids" | awk '{print "  PID: " $1}'
else
  echo -e "${DIM}No ${ENGINE_CMD} processes running.${NC}"
fi
echo ""

# Shared prompt: project golden rules + one-task workflow.
# NB: assigned via `read`, not $(cat <<heredoc) — macOS bash 3.2 cannot parse a
# heredoc inside $() when the body contains an apostrophe.
read -r -d '' PROMPT <<EOF || true
=== GOLDEN RULES (MUST FOLLOW) ===
- PROJECT: ai-usage — a Go daemon on Felipe's Mac that polls Claude / ChatGPT-Codex / OpenRouter / Groq quotas and serves a dashboard on 127.0.0.1:8765, plus M5StickS3 firmware that renders the same snapshot. Go 1.26.2, stdlib only, ZERO external Go dependencies. Read \`AGENTS.md\` (house rules) and \`GOLDEN_RULES.md\` (invariants) before your first edit.
- THE LEDGER IS \`spec.json\`, 93 tasks, APPEND-ONLY TRUTH: never edit, reorder, delete, renumber or add tasks. The ONLY field you may change is \`passes\`, on the ONE task you did this iteration, after that task's own Verify steps pass. Tasks 1-84 are completed history — never touch them, and never "fix" a task's prose because you disagree with it.
- TASK SELECTION: take the FIRST task with \`passes: false\` whose description does NOT begin with \`[WITHDRAWN\`, \`[DEFERRED\` or \`[BLOCKED\`. Those three are not yours to run — task 77 needs Felipe's hardware UAT, 83 is parked by decision, 78 and 79 were cancelled and nothing was built. Your queue is 85, 86, 87, 88, 89, 90, 91, 92.
- TASK 93 IS THE EXCEPTION AND IT DOES NOT SELF-CERTIFY. When 85-92 are all true, run 93's checks, append a \`UAT:\` line to progress.txt naming what Felipe must confirm, and STOP WITHOUT FLIPPING \`passes\`. Its Verify step names him, and this project's rule is that such a task waits for his word. Do not flip it, do not loop on it — journal, say so, and exit.
- A GREEN TEST PROVES NOTHING ACROSS A PROCESS BOUNDARY. Nine bugs shipped on 2026-09-07 and every one had a passing test faking the exact thing that was broken. \`internal/web/\` is embedded with go:embed, so nothing you change there is live until you rebuild AND restart. THE LANE, after every Go change: \`make verify\`, then \`cp bin/ai-usage ~/.local/bin/ai-usage\`, then \`codesign --force --sign - ~/.local/bin/ai-usage\` (make build output is UNSIGNED and an unsigned copy gets the daemon killed with OS_REASON_CODESIGNING), then \`launchctl kickstart -k gui/\$(id -u)/com.fcavalcanti.ai-usage\`, then \`lsof -nP -iTCP:8765 -sTCP:LISTEN\` and confirm the PID is launchd's.
- NEVER HAND-START THE DAEMON ON PORT 8765. It prevents launchd from binding, nothing survives a reboot, and every conclusion drawn from the log afterwards is worthless. This produced an entirely fictional defect once and cost an afternoon.
- NEVER FLASH OR ERASE THE DEVICE. No \`pio run -t upload\`, no \`-t erase\`, no \`esptool.py write_flash\`, no \`make fw-ota\`. Flashing is Felipe's action and needs his explicit go-ahead for that specific upload. Build, then append \`FLASH REQUEST: sha=<short sha> reason=<why> — owner action required\` to progress.txt and MOVE ON. \`pio device monitor -b 115200\` is read-only and always allowed.
- NEVER edit \`.env\` or \`firmware/include/secrets.h\`. \`secrets.h\` holds the only copy of the board's OTA password. NEVER log, render, return or commit a credential value — lengths or set/unset only.
- PURE/HAL SPLIT: logic lives in \`firmware/src/usage/\` as pure C++ with NO M5 or Arduino headers, host-tested by \`make fw-test\` (359 tests, Failed 0 today). ONLY \`firmware/src/hal/sticks3/*\` and \`firmware/src/main.cpp\` may touch M5. \`make fw-build\` must stay SUCCESS with 0 warnings from firmware/src/, and \`make fw-publish-check\` must still print PUBLISHABLE.
- NO SECOND SPELLING of any path, name or constant. Two of 2026-09-07's nine bugs were exactly that: a constant written in two places that drifted. If a task tells you to extract one shared helper, extract it — do not leave two copies in sync by hand.
- LABEL EVERY CLAIM \`[REAL]\` (verified against the running daemon or the hardware, with the command and its real output), \`[TEST]\` (passed in \`make verify\`/\`make fw-test\` only) or \`[UNVERIFIED]\` (reasoned but not checked). "It should work now" is not a status.
- TASKS 85 AND 86 ARE PRE-AUTHORIZED to toggle Felipe's real config and keys for their live proof, on one condition: capture the current value FIRST, restore it before the task ends, and re-read it to confirm the restore landed. Do not stall waiting for him — he already said yes.
- WHEN STUCK: journal the exact command and its exact error to progress.txt, leave the task open (do NOT flip passes), and stop the iteration so a human can look. Never guess an API, never invent one, never go researching on the web.
=== WORKFLOW ===
1. Read $PRD_FILE (the task ledger) and progress.txt (the build journal) before anything else.
2. In $PRD_FILE, find the FIRST task (top-to-bottom order = priority; do any task whose description starts with the URGENT marker before others) where passes is false. Work ONLY on that one task. Honor its 'DEPENDS ON:' / 'PREREQUISITE:' notes.
3. Follow that task's 'steps' exactly. Write tests first where it makes sense.
4. Validate by running that task's own 'Verify:' steps. Do NOT mark the task done until its Verify steps pass. If a Verify step is inherently visual/human-only, run every headless check you can and append a 'UAT:' line to progress.txt naming what a human must confirm — then STILL set passes=true. Never skip a task (later tasks depend on it).
5. APPEND a dated entry to progress.txt describing what you did. progress.txt is APPEND-ONLY: use >> and NEVER > — do not overwrite, truncate, rewrite or reformat it, and never delete lines you did not add. Its history is the only memory the next iteration has.
6. In $PRD_FILE, set that task's "passes" to true.
7. COMMIT: if .git exists, run 'git add .' to stage ALL files (including new ones), then 'git commit -m "<task description>"'. If the repo is not git-initialized, note that in progress.txt and skip committing this once.
$PUSH_STEP
9. If, and ONLY IF, every task in $PRD_FILE now has passes=true, output the exact line: <promise>COMPLETE</promise>

CRITICAL:
- ONE TASK ONLY, then STOP. Do NOT continue to another task.
- Always 'git add .' (include NEW files) before committing.
- After commit, you are DONE. Exit immediately.
- HARNESS IS INFRASTRUCTURE, NOT DELIVERABLE: never create, modify, or replace ralph.sh, ralph-*.sh, progress.sh, .env.ralph.local, or the ledger schema unless the current task explicitly names them.
- Keep files focused (~500 lines max).
EOF

# Claude attaches the ledger/journal; codex engines are told to read them first.
CLAUDE_INPUT="@$PRD_FILE @progress.txt $PROMPT"
CODEX_INPUT="FIRST: read ./$PRD_FILE and ./progress.txt in this repository — they are the task ledger and build journal.

$PROMPT"

# opencode takes the same "read them first" preamble as codex, for the -f reason
# documented at the invocation below.
OPENCODE_INPUT="$CODEX_INPUT"

tmpfile=$(mktemp)
errfile=$(mktemp)
cleanup() { rm -f "$tmpfile" "$errfile"; }
trap cleanup EXIT

overall_start=$(date +%s)
total_iteration_time=0
completed_iterations=0
total_cost=0
total_input_tokens=0
total_output_tokens=0

for ((i=1; i<=$1; i++)); do
  echo ""
  echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════${NC}"
  echo -e "${CYAN}${BOLD}  Iteration $i of $1 — ${ENGINE_DESC}${NC}"
  echo -e "${CYAN}${BOLD}═══════════════════════════════════════════════════════════${NC}"
  echo ""
  iter_start=$(date +%s)
  engine_exit=0

  # Journal backstop: progress.txt is append-only, but an engine may rewrite it
  # (muse-spark did on task 1, dropping the seed header). Snapshot it so the
  # history can be restored if the engine truncates instead of appending.
  progbak=""
  if [ -f progress.txt ]; then
    progbak=$(mktemp)
    cp progress.txt "$progbak"
  fi

  if [ "$ENGINE" = "claude" ]; then
    # Headless Claude Code on ONE task; JSON output carries result + usage/cost.
    claude $MODEL_FLAG --dangerously-skip-permissions --no-session-persistence \
      -p --output-format json "$CLAUDE_INPUT" > "$tmpfile" 2>&1 || engine_exit=$?
  elif [ "$ENGINE" = "opencode" ]; then
    # opencode run: headless one-shot. --auto auto-approves permissions (the loop
    # cannot answer a prompt).
    # Do NOT pass the ledger with -f: --file is declared [array] in opencode's
    # yargs parser, and an array option greedily swallows the positional that
    # follows it, so `-f progress.txt "$PROMPT"` parsed the ENTIRE prompt as a
    # second filename and exited 1. The prompt tells it to read the files itself,
    # exactly like the codex lane. Keep the message the ONLY positional.
    opencode run --auto -m "$OPENCODE_MODEL" \
      "$OPENCODE_INPUT" > "$tmpfile" 2> "$errfile" || engine_exit=$?
  else
    # codex exec: one task then exit; final answer -> stdout, progress -> stderr.
    codex_args=(exec --skip-git-repo-check --sandbox workspace-write
      -c 'sandbox_workspace_write.network_access=true'
      -m "$CODEX_MODEL" -c "model_reasoning_effort=\"$CODEX_EFFORT\"")
    codex "${codex_args[@]}" "$CODEX_INPUT" > "$tmpfile" 2> "$errfile" || engine_exit=$?
  fi

  # Restore the journal if the engine overwrote rather than appended: the last
  # non-empty line of the snapshot must still be present in the new file.
  if [ -n "$progbak" ] && [ -s "$progbak" ] && [ -f progress.txt ]; then
    last_prev=$(grep -v '^[[:space:]]*$' "$progbak" | tail -1)
    if [ -n "$last_prev" ] && ! grep -qF "$last_prev" progress.txt; then
      { cat "$progbak"; echo ""; cat progress.txt; } > progress.txt.restored \
        && mv progress.txt.restored progress.txt
      echo -e "${YELLOW}⚠️  progress.txt was overwritten, not appended — history restored by host${NC}"
    fi
  fi
  [ -n "$progbak" ] && rm -f "$progbak"

  iter_end=$(date +%s)
  iter_time=$((iter_end - iter_start))
  total_iteration_time=$((total_iteration_time + iter_time))
  completed_iterations=$((completed_iterations + 1))

  if [ "$ENGINE" = "claude" ]; then
    if jq -e . "$tmpfile" > /dev/null 2>&1; then
      result_text=$(jq -r '.result // "No result"' "$tmpfile")
      cost=$(jq -r '.total_cost_usd // 0' "$tmpfile")
      input_tokens=$(jq -r '.usage.input_tokens // 0' "$tmpfile")
      cache_read=$(jq -r '.usage.cache_read_input_tokens // 0' "$tmpfile")
      cache_create=$(jq -r '.usage.cache_creation_input_tokens // 0' "$tmpfile")
      output_tokens=$(jq -r '.usage.output_tokens // 0' "$tmpfile")
      iter_context=$((input_tokens + cache_read + cache_create))

      total_cost=$(echo "$total_cost $cost" | awk '{printf "%.4f", $1 + $2}')
      total_input_tokens=$((total_input_tokens + iter_context))
      total_output_tokens=$((total_output_tokens + output_tokens))

      echo "$result_text"
      echo ""
      echo -e "${BLUE}───────────────────────────────────────────────────────────${NC}"
      echo -e "${BLUE}  🔢 CONTEXT: ${BOLD}${iter_context}${NC}${BLUE} tokens (in=${input_tokens} cache_read=${cache_read} cache_create=${cache_create})${NC}"
      echo -e "${BLUE}  📤 OUTPUT:  ${BOLD}${output_tokens}${NC}${BLUE} tokens${NC}"
      echo -e "${BLUE}  💰 COST:    ${BOLD}\$${cost}${NC}"
      echo -e "${BLUE}───────────────────────────────────────────────────────────${NC}"
    else
      echo -e "${YELLOW}Warning: Could not parse JSON output${NC}"
      cat "$tmpfile"
    fi
  else
    # Codex engines: stdout IS the final answer; no usage JSON in text mode.
    cat "$tmpfile"
    if [ "$engine_exit" -ne 0 ]; then
      echo ""
      echo -e "${RED}${BOLD}  🚨 ${ENGINE} exited with code ${engine_exit} — stderr tail:${NC}"
      tail -20 "$errfile"
      echo -e "${GREEN}📊 $(./progress.sh)${NC}"
      exit 1   # let ralph-continuous.sh back off
    fi
    echo ""
    if [ "$ENGINE" = "opencode" ]; then
      echo -e "${DIM}  💰 usage/cost: n/a inline — run 'opencode stats' for token totals${NC}"
    else
      echo -e "${DIM}  💰 usage/cost: n/a (${ENGINE} engine — codex exec text mode reports no usage)${NC}"
    fi
  fi

  # Host commit backstop: if the engine's sandbox couldn't commit, do it here.
  if [ -d .git ] && command -v git >/dev/null 2>&1 && [ -n "$(git status --porcelain 2>/dev/null)" ]; then
    commit_msg=$(grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}' progress.txt 2>/dev/null | tail -1 | head -c 72)
    git add -A >/dev/null 2>&1 || true
    git commit -m "${commit_msg:-ralph: host auto-commit after iteration $i}" >/dev/null 2>&1 \
      && echo -e "${DIM}📦 Host auto-commit: ${commit_msg:-iteration $i}${NC}" || true
  fi

  echo ""
  echo -e "${YELLOW}⏱  Iteration $i took ${BOLD}$(format_time $iter_time)${NC}"
  echo -e "${GREEN}📊 $(./progress.sh)${NC}"

  if grep -q "<promise>COMPLETE</promise>" "$tmpfile"; then
    # The engine's claim of completeness only stands if the HOST agrees.
    if ! run_host_verify; then
      echo ""
      echo -e "${RED}${BOLD}  🚫 Engine claims COMPLETE but host verify FAILED — banner withheld.${NC}"
      echo -e "${RED}  An URGENT task was injected; the next batch will pick it up first.${NC}"
      echo -e "${GREEN}📊 $(./progress.sh)${NC}"
      exit 1
    fi
    overall_end=$(date +%s)
    overall_time=$((overall_end - overall_start))
    avg_time=$((total_iteration_time / completed_iterations))
    echo ""
    echo -e "${MAGENTA}${BOLD}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${MAGENTA}${BOLD}  🎉 PRD COMPLETE after $i iterations!${NC}"
    echo -e "${MAGENTA}${BOLD}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${MAGENTA}  ⏱  Overall time: ${BOLD}$(format_time $overall_time)${NC}"
    echo -e "${MAGENTA}  ⏱  Average per iteration: ${BOLD}$(format_time $avg_time)${NC}"
    echo -e "${BLUE}  🔢 Total context: ${BOLD}${total_input_tokens}${NC}${BLUE} tokens (claude iterations only)${NC}"
    echo -e "${BLUE}  📤 Total output: ${BOLD}${total_output_tokens}${NC}${BLUE} tokens (claude iterations only)${NC}"
    echo -e "${BLUE}  💰 Total cost: ${BOLD}\$${total_cost}${NC}"
    echo -e "${GREEN}  📊 $(./progress.sh)${NC}"
    exit 0
  fi
done

# Batch ended without COMPLETE: verify anyway so drift is caught (and an URGENT
# task injected) as early as possible. Informational — the batch itself succeeded.
run_host_verify || true

overall_end=$(date +%s)
overall_time=$((overall_end - overall_start))
avg_time=$((total_iteration_time / completed_iterations))

echo ""
echo -e "${MAGENTA}${BOLD}═══════════════════════════════════════════════════════════${NC}"
echo -e "${MAGENTA}${BOLD}  Completed $1 iterations${NC}"
echo -e "${MAGENTA}${BOLD}═══════════════════════════════════════════════════════════${NC}"
echo -e "${MAGENTA}  ⏱  Overall time: ${BOLD}$(format_time $overall_time)${NC}"
echo -e "${MAGENTA}  ⏱  Average per iteration: ${BOLD}$(format_time $avg_time)${NC}"
echo -e "${BLUE}  🔢 Total context: ${BOLD}${total_input_tokens}${NC}${BLUE} tokens (claude iterations only)${NC}"
echo -e "${BLUE}  📤 Total output: ${BOLD}${total_output_tokens}${NC}${BLUE} tokens (claude iterations only)${NC}"
echo -e "${BLUE}  💰 Total cost: ${BOLD}\$${total_cost}${NC}"
echo -e "${GREEN}  📊 $(./progress.sh)${NC}"
