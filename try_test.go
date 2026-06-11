package main

import (
	"bytes"
	"strings"
	"testing"
)

// Tests for `gralph try`: the dry-run gate runner. Its contract is that
// nothing it does is observable afterwards -- no store commit, no failure
// budget, no progress, no cursor movement -- while the lua sees the real
// store (and, for finalize gates, the real progress).

func tryOut(t *testing.T, p *Profile, name string, argv ...string) (string, int) {
	t.Helper()
	var buf bytes.Buffer
	code, err := runTry(p, name, argv, &buf)
	if err != nil {
		t.Fatalf("runTry(%s %v): %v", name, argv, err)
	}
	return buf.String(), code
}

func TestTryDoesNotCommit(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: ok
        required: true
    lua: gate.lua
    next: [b]
  - name: b
`, map[string]string{
		"gate.lua": `gralph.store.set("goal", "demo")
if gralph.args.ok ~= "yes" then gralph.fail("reason: pass --ok yes") end`,
	})

	out, code := tryOut(t, p, "a", "--ok", "yes")
	if code != 0 {
		t.Fatalf("exit = %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"try: a (command)",
		"result: SUCCESS",
		"store writes (not committed):",
		`goal = "demo"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	// Nothing was committed or moved.
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("goal"); ok {
		t.Fatal("try must never commit store writes")
	}
	if got := cursorOf(t, p); got != "" {
		t.Fatalf("cursor = %q; try must not move the cursor", got)
	}

	// A failing try consumes no failure budget either.
	out, code = tryOut(t, p, "a", "--ok", "no")
	if code != 1 || !strings.Contains(out, "result: FAILED: reason: pass --ok yes") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("try must not consume the failure budget")
	}
}

func TestTrySkipsCursorCheck(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: first
    next: [second]
  - name: second
`, nil)
	if _, err := resolveNext(p); err != nil { // cursor := first
		t.Fatal(err)
	}

	// A real run of `second` would be rejected; try runs it anyway.
	out, code := tryOut(t, p, "second")
	if code != 0 || !strings.Contains(out, "result: SUCCESS") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
	if !strings.Contains(out, "lua: (none -- always succeeds)") {
		t.Fatalf("output:\n%s", out)
	}
	if got := cursorOf(t, p); got != "first" {
		t.Fatalf("cursor = %q, want first", got)
	}
}

func TestTryShowsRouteAndMirrorsRoutingErrors(t *testing.T) {
	const yaml = `commands:
  - name: verify
    args:
      - name: to
    lua: route.lua
    next: [fix, finish]
  - name: fix
  - name: finish
`
	const luaSrc = `if gralph.args.to ~= nil then gralph.route(gralph.args.to) end`

	p := writeProfile(t, yaml, map[string]string{"route.lua": luaSrc})
	out, code := tryOut(t, p, "verify", "--to", "finish")
	if code != 0 || !strings.Contains(out, "route: finish") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}

	// Missing route on a branching node is the same SCRIPT ERROR as a real run.
	out, code = tryOut(t, p, "verify")
	if code != 1 || !strings.Contains(out, "result: SCRIPT ERROR") ||
		!strings.Contains(out, "without gralph.route()") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("try must not consume the failure budget")
	}
}

func TestTryFinalizeReadsProgress(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: s
        count: 2
        key: k
        args:
          - name: k
    lua: fin.lua
    next: [wrap]
  - name: wrap
`, map[string]string{
		"fin.lua": `if gralph.progress.count("s") ~= 2 then
  gralph.fail("reason: expected 2 items, got " .. gralph.progress.count("s"))
end`,
	})

	run(t, p, "s", "--k", "k1") // real progress: 1/2

	out, code := tryOut(t, p, "parent")
	if code != 1 {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
	for _, want := range []string{
		"warning: subcommand quotas not met (s 1/2); a real run would be rejected",
		"try: parent (finalize command)",
		"result: FAILED: reason: expected 2 items, got 1", // gralph.progress.* saw the live file
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	// The try left progress and cursor untouched.
	pr, err := LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if pr.CountDone("s") != 1 {
		t.Fatal("try must not modify progress")
	}
	if got := cursorOf(t, p); got != "parent" {
		t.Fatalf("cursor = %q, want parent", got)
	}

	// With the quota met the warning disappears and the gate passes.
	run(t, p, "s", "--k", "k2")
	out, code = tryOut(t, p, "parent")
	if code != 0 || strings.Contains(out, "warning:") || !strings.Contains(out, "result: SUCCESS") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
	if got := cursorOf(t, p); got != "parent" {
		t.Fatalf("cursor = %q; a successful try must not advance the cursor", got)
	}
}

func TestTryUnknownAndUsage(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: report
        required: true
`, nil)

	if _, err := runTry(p, "ghost", nil, &bytes.Buffer{}); err == nil ||
		!strings.Contains(err.Error(), `unknown command "ghost"`) {
		t.Fatalf("want unknown-command error, got %v", err)
	}

	out, code := tryOut(t, p, "a") // missing --report
	if code != 1 || !strings.Contains(out, "usage error") {
		t.Fatalf("exit = %d, output:\n%s", code, out)
	}
}
