#!/usr/bin/env bash
# Scripted fake agent for the release-notes example. One invocation = one session.
# Mirrors the ralph prompt; the simulated "work" produces real files the gates
# inspect. To exercise the citation cross-check, the first draft cites a
# fabricated PR (#999); remediation rewrites it to a valid one.
set -u

SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
export PATH="$PATH:$SELF_DIR:$SELF_DIR/../..:$SELF_DIR/../../dist"

echo "----- agent session start -----"
guidance="$(gralph next)" || { echo "agent: next failed"; exit 1; }
echo "$guidance" | sed 's/^/  [next] /'

run_line="$(printf '%s\n' "$guidance" | grep '^RUN:' | head -1 | sed 's/^RUN: //')"
[ -n "$run_line" ] || { echo "agent: no RUN line"; exit 1; }
cmd="$(printf '%s' "$run_line" | awk '{print $2}')"

write_good_notes() {
  cat > NOTES.md <<'MD'
# Release notes

- Faster startup (#101)
- Fixed crash on empty input (#102)
- New export format (#103)
MD
}

case "$cmd" in
  gather)
    cat > changes.txt <<'TXT'
#101 speed up startup
#102 fix crash on empty input
#103 add export format
TXT
    ;;
  draft)
    # First draft cites a fabricated PR (#999) to trip verify_citations.
    cat > NOTES.md <<'MD'
# Release notes

- Faster startup (#101)
- Fixed crash on empty input (#102)
- Mysterious improvement (#999)
MD
    ;;
  publish)
    : # version already valid in the RUN line
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
  echo "  [agent] remediating and retrying in the same session"
  # Fix the fabricated citation by rewriting the notes correctly.
  [ "$cmd" = "verify_citations" ] || [ "$cmd" = "draft" ] && write_good_notes
done
echo "----- agent session end (gave up) -----"; exit 1
