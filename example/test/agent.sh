#!/usr/bin/env bash
# Fake non-interactive agent for e2e testing. One invocation = one session.
# Behavior mirrors the ralph prompt:
#   1. ask `gralph next` for guidance
#   2. "do the task" (simulated)
#   3. run the instructed command (line starting with RUN:)
#   4. obey "End the session" responses; otherwise fix & retry in-session
set -u
PROMPT="${1:-}"
# The example binary lives next to the profile (built by `go build -o
# example/gralph .`); fall back to the repo's dist/.
PATH="$PATH:$(cd "$(dirname "$0")/.." && pwd):$(cd "$(dirname "$0")/../.." && pwd)/dist"

echo "----- agent session start -----"
guidance="$(gralph next)" || { echo "agent: next failed"; exit 1; }
echo "$guidance" | sed 's/^/  [next] /'

cmdline="$(echo "$guidance" | grep '^RUN:' | head -1 | sed 's/^RUN: //')"
if [ -z "$cmdline" ]; then
  # No hand-written RUN: line -- take the generated usage line right below
  # "Command to run when done:" ({{usage}} or the auto-appended block).
  cmdline="$(echo "$guidance" | grep -A1 '^Command to run when done:' | tail -1 | sed 's/^ *//')"
fi
if [ -z "$cmdline" ]; then echo "agent: no runnable command line"; exit 1; fi

# Simulated non-deterministic work: pick a goal / write a report etc.
cmdline="${cmdline//<your-goal>/demo}"
cmdline="${cmdline//<one line>/\"all done nicely\"}"
cmdline="${cmdline//<value>/report.txt}"

run_once() {
  echo "  [agent] running: $cmdline"
  out="$(eval "$cmdline" 2>&1)"; code=$?
  echo "$out" | sed 's/^/  [cmd] /'
  return $code
}

for attempt in 1 2 3; do
  if run_once; then
    echo "----- agent session end (success response) -----"
    exit 0
  fi
  if echo "$out" | grep -q "End the session"; then
    echo "----- agent session end (forced by gralph) -----"
    exit 0
  fi
  # in-session remediation: e.g. create the missing report file, then retry
  echo "  [agent] remediating and retrying in the same session"
  touch report.txt
done
echo "----- agent session end (gave up) -----"
exit 1
