#!/usr/bin/env bash
# Scripted fake agent for the tdd-loop example. One invocation = one session.
# It mirrors the ralph prompt: ask `gralph next`, do the (simulated) work the
# instructed command needs, run that command, obey "End the session".
#
# The simulated work is deliberately imperfect on the first implementation so
# the verify gate (which RUNS ./check.sh) routes to `fix`; the fix step then
# completes the impl so the next verify routes to `release`.
set -u

# Make the freshly built gralph binary discoverable. Adjust if yours is elsewhere.
SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
export PATH="$PATH:$SELF_DIR:$SELF_DIR/../..:$SELF_DIR/../../dist"

echo "----- agent session start -----"
guidance="$(gralph next)" || { echo "agent: next failed"; exit 1; }
echo "$guidance" | sed 's/^/  [next] /'

run_line="$(printf '%s\n' "$guidance" | grep '^RUN:' | head -1 | sed 's/^RUN: //')"
[ -n "$run_line" ] || { echo "agent: no RUN line"; exit 1; }

# Identify which command we're closing (token after 'gralph').
cmd="$(printf '%s' "$run_line" | awk '{print $2}')"

# --- simulated, command-specific preparation ----------------------------------
case "$cmd" in
  spec)
    run_line="${run_line//<name>/adder}"
    ;;
  implement)
    mkdir -p src
    # First implementation is intentionally incomplete (no DONE marker).
    echo "def add(a, b): return a + b" > src/impl.txt
    ;;
  fix)
    # Real remediation: complete the implementation so ./check.sh will pass.
    echo "DONE" >> src/impl.txt
    run_line="${run_line//<one short line>/\"completed the implementation\"}"
    ;;
  release)
    run_line="${run_line//v1.0.0/v1.0.0}"
    ;;
esac

run_once() {
  echo "  [agent] running: $run_line"
  out="$(eval "$run_line" 2>&1)"; code=$?
  printf '%s\n' "$out" | sed 's/^/  [cmd] /'
  return $code
}

for attempt in 1 2 3 4; do
  if run_once; then
    echo "----- agent session end (success) -----"; exit 0
  fi
  if printf '%s' "$out" | grep -q "End the session"; then
    echo "----- agent session end (forced by gralph) -----"; exit 0
  fi
  # Generic in-session remediation for missing-file failures.
  echo "  [agent] remediating and retrying in the same session"
  [ "$cmd" = "implement" ] && { mkdir -p src; echo "x" > src/impl.txt; }
done
echo "----- agent session end (gave up) -----"; exit 1
