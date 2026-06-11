package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestLoopEndToEnd drives the real orchestrator (runLoop) against a scripted
// agent, end to end: the agent shells out to the actual gralph binary (built
// here), spawns parallel background workers for the subcommand quotas,
// finalizes the parent, and the loop must reach DONE.
//
// Everything is generated into a temp dir -- example/ is documentation, not a
// test fixture.
func TestLoopEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("e2e agent script needs bash")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}

	bin := filepath.Join(t.TempDir(), "gralph")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	write("profile.yaml", `agent:
  command: ["bash", "agent.sh"]
commands:
  - name: build-all
    guidance: |
      Build parts in parallel.
      {{subprogress}}
    subcommands:
      - name: make-part
        count: 3
        key: part
        args:
          - name: part
        lua: scripts/part.lua
    lua: scripts/finalize.lua
    next: [wrap]
  - name: wrap
    guidance: close out
`)
	write("scripts/part.lua", `local f = io.open("out/" .. gralph.args.part .. ".txt", "r")
if not f then
  gralph.fail("reason: artifact missing for " .. gralph.args.part)
  return
end
f:close()
gralph.store.set("ev:" .. gralph.args.part, true)
`)
	write("scripts/finalize.lua", `if gralph.progress.count("make-part") ~= 3 then
  gralph.fail("reason: quota mismatch")
  return
end
gralph.store.set("parts", gralph.progress.keys("make-part"))
`)
	// The fake agent: one invocation = one session. On the fork/join node it
	// spawns one background worker per remaining part (real concurrent gralph
	// processes, exercising the state lock), then finalizes the parent.
	write("agent.sh", fmt.Sprintf(`#!/usr/bin/env bash
set -u
GRALPH=%q
guidance="$("$GRALPH" next)" || exit 1
case "$guidance" in
  *"Current task: build-all"*)
    mkdir -p out
    for p in alpha beta gamma; do
      if ! echo "$guidance" | grep -q "$p"; then
        ( echo data > "out/$p.txt"; "$GRALPH" make-part --part "$p" >/dev/null ) &
      fi
    done
    wait
    "$GRALPH" build-all >/dev/null
    ;;
  *"Current task: wrap"*)
    "$GRALPH" wrap >/dev/null
    ;;
esac
exit 0
`, bin))

	p, err := LoadProfile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := runLoop(p, 8); err != nil {
		t.Fatalf("runLoop: %v", err)
	}

	st, err := LoadState(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Cursor != DoneCursor {
		t.Fatalf("cursor = %q, want DONE", st.Cursor)
	}
	pr, err := LoadProgress(p.StateDir, "build-all")
	if err != nil {
		t.Fatal(err)
	}
	if pr.CountDone("make-part") != 0 {
		t.Fatal("progress must be cleared after the parent finalized")
	}
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := store.Get("parts")
	if !ok {
		t.Fatal("finalize evidence missing from store")
	}
	parts, _ := v.([]any)
	if len(parts) != 3 {
		t.Fatalf("parts = %#v, want 3 entries", v)
	}
	for _, part := range []string{"alpha", "beta", "gamma"} {
		if _, ok := store.Get("ev:" + part); !ok {
			t.Fatalf("worker evidence ev:%s lost", part)
		}
	}
}
