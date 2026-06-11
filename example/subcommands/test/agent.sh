#!/usr/bin/env bash
# Fake non-interactive agent with parallel sub-agent support.
# One invocation = one session:
#   1. ask `gralph next` for guidance
#   2. spawn one background "sub-agent" per remaining work item; each does the
#      item's work and runs its subcommand (concurrently -- this exercises the
#      state-dir flock for real)
#   3. once quotas are met, run the parent finalize command
set -u
PROMPT="${1:-}"
# The example binary lives next to the profile (built by `go build -o
# example/subcommands/gralph .`); fall back to the repo's dist/.
PATH="$PATH:$(cd "$(dirname "$0")/.." && pwd):$(cd "$(dirname "$0")/../../.." && pwd)/dist"

echo "----- agent session start -----"
guidance="$(gralph next)" || { echo "agent: next failed"; exit 1; }
echo "$guidance" | sed 's/^/  [next] /'

if echo "$guidance" | grep -q "Current task: build-all"; then
  mkdir -p out
  # One parallel sub-agent per part not yet listed as done.
  for p in alpha beta gamma; do
    if ! echo "$guidance" | grep -q "$p"; then
      (
        echo "part $p content" > "out/$p.txt"
        out="$(gralph make-part --part "$p" 2>&1)"
        echo "$out" | sed "s/^/  [sub-agent $p] /"
      ) &
    fi
  done
  if echo "$guidance" | grep -q "write-doc: 0/1"; then
    (
      echo "# manual" > out/manual.md
      out="$(gralph write-doc 2>&1)"
      echo "$out" | sed 's/^/  [sub-agent doc] /'
    ) &
  fi
  wait
  out="$(gralph build-all 2>&1)"; code=$?
  echo "$out" | sed 's/^/  [cmd] /'
  echo "----- agent session end -----"
  exit 0
fi

# Any other node: run the RUN: line from the guidance.
cmdline="$(echo "$guidance" | grep '^RUN:' | head -1 | sed 's/^RUN: //')"
if [ -z "$cmdline" ]; then echo "agent: no RUN line"; exit 1; fi
echo "  [agent] running: $cmdline"
out="$(eval "$cmdline" 2>&1)"
echo "$out" | sed 's/^/  [cmd] /'
echo "----- agent session end -----"
exit 0
