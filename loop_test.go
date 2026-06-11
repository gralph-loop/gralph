package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
	if err := runLoop(context.Background(), p, 8); err != nil {
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

// writeLoopProfile writes a profile YAML into a temp dir and loads it.
func writeLoopProfile(t *testing.T, yaml string) *Profile {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runLoopWithDeadline guards against the regression where runLoop spins
// forever on a permanently failing agent.
func runLoopWithDeadline(t *testing.T, p *Profile, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- runLoop(context.Background(), p, 0) }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("runLoop did not return within %s (hot loop?)", timeout)
		return nil
	}
}

// Regression for issue #4: an agent.command pointing at a nonexistent binary
// must make runLoop return an error instead of hot-looping forever.
func TestRunLoopAgentBinaryNotFound(t *testing.T) {
	p := writeLoopProfile(t, `
agent:
  command: ["gralph-no-such-binary-xyz"]
commands:
  - name: implement
    guidance: do the thing
`)
	err := runLoopWithDeadline(t, p, 10*time.Second)
	if err == nil {
		t.Fatal("expected an error for a nonexistent agent binary, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be started") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Issue #4: an agent that keeps exiting abnormally without cursor progress
// must stop after MaxConsecutiveAgentFailures attempts (with backoff).
func TestRunLoopConsecutiveAgentFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on sh")
	}
	oldBase, oldMax := agentBackoffBase, agentBackoffMax
	agentBackoffBase, agentBackoffMax = time.Millisecond, 8*time.Millisecond
	defer func() { agentBackoffBase, agentBackoffMax = oldBase, oldMax }()

	p := writeLoopProfile(t, `
agent:
  command: ["sh", "-c", "exit 1"]
commands:
  - name: implement
    guidance: do the thing
`)
	err := runLoopWithDeadline(t, p, 10*time.Second)
	if err == nil {
		t.Fatal("expected an error after repeated abnormal agent exits, got nil")
	}
	if !strings.Contains(err.Error(), "times in a row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Issue #6: agent.timeout kills a hung agent; the iteration counts as an
// abnormal exit, so a permanently hanging agent also exhausts the budget.
func TestRunLoopAgentTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on sh")
	}
	oldBase, oldMax := agentBackoffBase, agentBackoffMax
	agentBackoffBase, agentBackoffMax = time.Millisecond, 8*time.Millisecond
	defer func() { agentBackoffBase, agentBackoffMax = oldBase, oldMax }()

	p := writeLoopProfile(t, `
agent:
  command: ["sh", "-c", "exec sleep 60"]
  timeout: 100ms
commands:
  - name: implement
    guidance: do the thing
`)
	err := runLoopWithDeadline(t, p, 30*time.Second)
	if err == nil {
		t.Fatal("expected an error after repeated agent timeouts, got nil")
	}
	if !strings.Contains(err.Error(), "times in a row") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// Issue #7: a cancelled context stops the loop, keeps the cursor on disk and
// reports the interruption.
func TestRunLoopInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on sh")
	}
	p := writeLoopProfile(t, `
agent:
  command: ["sh", "-c", "exec sleep 60"]
commands:
  - name: implement
    guidance: do the thing
`)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runLoop(ctx, p, 0) }()
	time.Sleep(200 * time.Millisecond) // let the agent start
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("expected interrupted error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runLoop did not stop after context cancellation")
	}
	// Cursor must survive the interruption so `gralph run` can resume.
	st, err := LoadState(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Cursor != "implement" {
		t.Fatalf("cursor not preserved, got %q", st.Cursor)
	}
}

func TestAgentBackoff(t *testing.T) {
	want := []time.Duration{
		2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		if got := agentBackoff(i + 1); got != w {
			t.Errorf("agentBackoff(%d) = %s, want %s", i+1, got, w)
		}
	}
}
