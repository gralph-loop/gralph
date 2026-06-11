package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeProfile materializes a profile plus auxiliary files in a temp dir and
// loads it, so tests exercise the real loader (defaults + validation).
func writeProfile(t *testing.T, yaml string, files map[string]string) *Profile {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pp := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(pp, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfile(pp)
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	return p
}

func run(t *testing.T, p *Profile, name string, argv ...string) *CommandResult {
	t.Helper()
	res, err := runCustomCommand(p, name, argv)
	if err != nil {
		t.Fatalf("runCustomCommand(%s %v): %v", name, argv, err)
	}
	return res
}

func wantContains(t *testing.T, res *CommandResult, substr string) {
	t.Helper()
	if !strings.Contains(res.Message, substr) {
		t.Fatalf("message %q does not contain %q", res.Message, substr)
	}
}

func cursorOf(t *testing.T, p *Profile) string {
	t.Helper()
	st, err := LoadState(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Cursor
}

func failuresOf(t *testing.T, p *Profile) map[string]int {
	t.Helper()
	st, err := LoadState(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	return st.Failures
}

// ---------------------------------------------------------------------------
// Loader validation
// ---------------------------------------------------------------------------

func TestValidateSubcommands(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{
			"count without key",
			`commands:
  - name: p
    subcommands:
      - name: s
        count: 3`,
			"no key to distinguish",
		},
		{
			"key not a declared arg",
			`commands:
  - name: p
    subcommands:
      - name: s
        count: 2
        key: item`,
			`key "item" is not a declared arg`,
		},
		{
			"subcommand clashes with command name",
			`commands:
  - name: p
    subcommands:
      - name: q
  - name: q`,
			"clashes with a command name",
		},
		{
			"duplicate subcommand across parents",
			`commands:
  - name: p
    next: [q]
    subcommands:
      - name: s
  - name: q
    subcommands:
      - name: s`,
			"duplicate subcommand name",
		},
		{
			"reserved name",
			`commands:
  - name: p
    subcommands:
      - name: DONE`,
			"reserved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pp := filepath.Join(dir, "profile.yaml")
			if err := os.WriteFile(pp, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadProfile(pp)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestKeyArgForcedRequired(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: p
    subcommands:
      - name: s
        count: 2
        key: item
        args:
          - name: item
`, nil)
	res := run(t, p, "s") // no --item
	wantContains(t, res, "missing required argument --item")
	if res.ExitCode != 1 {
		t.Fatalf("want exit 1, got %d", res.ExitCode)
	}
}

// ---------------------------------------------------------------------------
// Fork/join flow
// ---------------------------------------------------------------------------

const flowProfile = `commands:
  - name: parent
    guidance: "work items: {{subprogress}}"
    subcommands:
      - name: sub-a
        count: 2
        key: item
        args:
          - name: item
      - name: sub-b
    next: [wrap]
  - name: wrap
    guidance: done
`

func TestSubcommandQuotaFlow(t *testing.T) {
	p := writeProfile(t, flowProfile, nil)

	// Parent before quotas: rejected, no budget.
	res := run(t, p, "parent")
	wantContains(t, res, "not runnable yet")
	wantContains(t, res, "sub-a 0/2, sub-b 0/1")
	if res.ExitCode != 1 || res.EndSession {
		t.Fatalf("want exit 1 / keep session, got %+v", res)
	}
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("quota rejection must not consume the failure budget")
	}

	// Another node's command: rejected.
	res = run(t, p, "wrap")
	wantContains(t, res, "not the current command")

	// First item.
	res = run(t, p, "sub-a", "--item", "x")
	wantContains(t, res, "OK: `sub-a` (x) recorded")
	wantContains(t, res, "sub-a 1/2")
	if !res.EndSession {
		t.Fatal("subcommand success must end the (sub-)session")
	}
	if cursorOf(t, p) != "parent" {
		t.Fatal("cursor must stay on the parent")
	}

	// Duplicate key: rejected, no budget.
	res = run(t, p, "sub-a", "--item", "x")
	wantContains(t, res, "already completed")
	if res.ExitCode != 1 || len(failuresOf(t, p)) != 0 {
		t.Fatalf("duplicate must be a budget-free rejection, got %+v", res)
	}

	// Empty key: usage error.
	res = run(t, p, "sub-a", "--item", "  ")
	wantContains(t, res, "must not be empty")

	// Fill the quotas.
	run(t, p, "sub-a", "--item", "y")
	res = run(t, p, "sub-b") // count 1, no key -> single slot
	wantContains(t, res, "All subcommand quotas met")

	// Finalize: cursor advances, progress cleared.
	res = run(t, p, "parent")
	wantContains(t, res, "OK: `parent` succeeded")
	if !res.EndSession {
		t.Fatal("parent success must end the session")
	}
	if got := cursorOf(t, p); got != "wrap" {
		t.Fatalf("cursor = %q, want wrap", got)
	}
	pr, err := LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if pr.CountDone("sub-a") != 0 || pr.CountDone("sub-b") != 0 {
		t.Fatal("progress must be cleared after parent success")
	}

	// Straggler after the cursor moved: rejected.
	res = run(t, p, "sub-a", "--item", "z")
	wantContains(t, res, "is a subcommand of `parent`, which is not the current command")
}

func TestStaleProgressInvalidated(t *testing.T) {
	p := writeProfile(t, flowProfile, nil)
	stale := &Progress{Command: "other", Done: map[string]map[string]DoneMeta{
		"sub-a": {"x": {}, "y": {}}, "sub-b": {"sub-b": {}},
	}}
	if err := stale.Save(p.StateDir); err != nil {
		t.Fatal(err)
	}
	res := run(t, p, "parent")
	wantContains(t, res, "not runnable yet") // stale quotas must not count
}

// ---------------------------------------------------------------------------
// Gates and budgets
// ---------------------------------------------------------------------------

func TestSubcommandGatePerKeyBudget(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: check
        count: 2
        key: k
        fail_threshold: 2
        args:
          - name: k
          - name: ok
        lua: check.lua
`, map[string]string{
		"check.lua": `if gralph.args.ok ~= "yes" then gralph.fail("reason: not ok; pass --ok yes") end`,
	})

	res := run(t, p, "check", "--k", "a", "--ok", "no")
	wantContains(t, res, "FAILED `check (a)` (failure 1)")
	if res.EndSession {
		t.Fatal("failure 1 of threshold 2 must keep the session")
	}
	if failuresOf(t, p)["check:a"] != 1 {
		t.Fatal("failures must be counted per (subcommand, key)")
	}

	// A different key has its own budget.
	res = run(t, p, "check", "--k", "b", "--ok", "no")
	if res.EndSession {
		t.Fatal("key b's first failure must not inherit key a's budget")
	}

	// Second failure of the same key hits the threshold.
	res = run(t, p, "check", "--k", "a", "--ok", "no")
	if !res.EndSession {
		t.Fatal("failure 2 of threshold 2 must end the session")
	}

	res = run(t, p, "check", "--k", "a", "--ok", "yes")
	wantContains(t, res, "recorded")
}

func TestRouteForbiddenInSubcommandGate(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: s
        lua: route.lua
    next: [other]
  - name: other
`, map[string]string{
		"route.lua": `gralph.route("other")`,
	})
	res := run(t, p, "s")
	wantContains(t, res, "SCRIPT ERROR")
	wantContains(t, res, "not available in subcommand gates")
}

func TestFinalizeGateSeesProgressAndRoutes(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: s
        count: 2
        key: k
        args:
          - name: k
    lua: fin.lua
    next: [a, b]
  - name: a
  - name: b
`, map[string]string{
		"fin.lua": `
if gralph.progress.count("s") ~= 2 then
  gralph.fail("reason: expected 2 items, got " .. gralph.progress.count("s"))
end
gralph.store.set("done_keys", gralph.progress.keys("s"))
gralph.route("b")
`,
	})

	run(t, p, "s", "--k", "k1")
	run(t, p, "s", "--k", "k2")
	res := run(t, p, "parent")
	wantContains(t, res, "OK: `parent` succeeded")
	if got := cursorOf(t, p); got != "b" {
		t.Fatalf("cursor = %q, want b", got)
	}
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := store.Get("done_keys")
	if !ok {
		t.Fatal("finalize store write missing")
	}
	keys, ok := v.([]any)
	if !ok || len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Fatalf("done_keys = %#v, want [k1 k2]", v)
	}
}

// A straggler worker that commits a new key between the finalize's progress
// load and its locked commit must not have its record silently cleared: the
// commit is refused, and the re-run finalize sees the straggler.
func TestFinalizeRejectsStragglerCommit(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: work
        count: 1
        key: k
        args:
          - name: k
    lua: fin.lua
    next: [wrap]
  - name: wrap
`, map[string]string{
		// Records how many items this finalize run saw, so the test can prove
		// the re-run validated the straggler too.
		"fin.lua": `gralph.store.set("validated", tostring(gralph.progress.count("work")))`,
	})

	// Meet the quota, then snapshot what a finalize starting now would read
	// (= the progress its lua run gets to see).
	run(t, p, "work", "--k", "k1")
	pr, err := LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	seen := pr.TotalDone()

	// Simulate the race: while the (conceptual) finalize lua is still
	// running outside the lock, a parallel worker commits a fresh key and is
	// told "OK: recorded".
	res := run(t, p, "work", "--k", "straggler")
	wantContains(t, res, "OK: `work` (straggler) recorded")

	// The finalize commit, carrying the stale snapshot, must be refused
	// without ending the session or consuming the failure budget.
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	res, err = commitSuccess(p, p.Command("parent"), "wrap", store, true, seen, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 1 || res.EndSession {
		t.Fatalf("stale finalize must be a keep-session rejection, got %+v", res)
	}
	wantContains(t, res, "new work items were recorded while it ran")
	wantContains(t, res, "Run `parent` again")
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("stale finalize rejection must not consume the failure budget")
	}
	if got := cursorOf(t, p); got != "parent" {
		t.Fatalf("cursor = %q, want parent (no advance on rejection)", got)
	}
	pr, err = LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if pr.CountDone("work") != 2 {
		t.Fatal("the straggler's record must survive the rejected finalize")
	}

	// Re-run: quotas are monotonic so the gate passes, the lua now sees the
	// straggler, and the commit goes through.
	res = run(t, p, "parent")
	wantContains(t, res, "OK: `parent` succeeded")
	if got := cursorOf(t, p); got != "wrap" {
		t.Fatalf("cursor = %q, want wrap", got)
	}
	store, err = LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Get("validated"); v != "2" {
		t.Fatalf("re-run finalize validated %v items, want 2 (straggler visible to lua)", v)
	}
	pr, err = LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if pr.TotalDone() != 0 {
		t.Fatal("progress must be cleared after the successful re-run")
	}
}

// ---------------------------------------------------------------------------
// Concurrency: parallel sub-agents are the feature's reason to exist.
// ---------------------------------------------------------------------------

func TestConcurrentSubcommands(t *testing.T) {
	const n = 16
	p := writeProfile(t, fmt.Sprintf(`commands:
  - name: parent
    subcommands:
      - name: work
        count: %d
        key: k
        args:
          - name: k
        lua: work.lua
`, n+1), map[string]string{
		// Each gate writes its own namespaced evidence key, per the store
		// convention for parallel workers.
		"work.lua": `gralph.store.set("ev:" .. gralph.args.k, gralph.args.k)`,
	})

	var wg sync.WaitGroup
	results := make([]*CommandResult, n)
	dupResults := make([]*CommandResult, 4)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runCustomCommand(p, "work", []string{"--k", fmt.Sprintf("item%02d", i)})
			if err != nil {
				t.Errorf("worker %d: %v", i, err)
				return
			}
			results[i] = res
		}(i)
	}
	// Several workers race on the SAME key: exactly one may record it.
	for i := range dupResults {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := runCustomCommand(p, "work", []string{"--k", "shared"})
			if err != nil {
				t.Errorf("dup worker %d: %v", i, err)
				return
			}
			dupResults[i] = res
		}(i)
	}
	wg.Wait()

	for i, res := range results {
		if res == nil || res.ExitCode != 0 {
			t.Fatalf("distinct-key worker %d should succeed, got %+v", i, res)
		}
	}
	wins := 0
	for _, res := range dupResults {
		if res == nil {
			t.Fatal("missing dup result")
		}
		if res.ExitCode == 0 {
			wins++
		} else {
			wantContains(t, res, "already completed")
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one same-key worker must win, got %d", wins)
	}

	pr, err := LoadProgress(p.StateDir, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if got := pr.CountDone("work"); got != n+1 {
		t.Fatalf("progress has %d items, want %d (lost update?)", got, n+1)
	}

	// No store evidence may be lost to a concurrent commit.
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("ev:item%02d", i)
		if _, ok := store.Get(key); !ok {
			t.Fatalf("store evidence %s lost to a concurrent commit", key)
		}
	}
	if _, ok := store.Get("ev:shared"); !ok {
		t.Fatal("store evidence ev:shared missing")
	}
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func TestNextRendersSubprogress(t *testing.T) {
	p := writeProfile(t, flowProfile, nil)
	run(t, p, "sub-a", "--item", "x")

	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Current task: parent",
		"work items: sub-a: 1/2 done (x)", // {{subprogress}} in the guidance
		"Subcommand progress:",            // auto-appended block
		"sub-b: 0/1 done",
		"subcommand quotas",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderNext output missing %q:\n%s", want, out)
		}
	}
}

// Plain single-success nodes must behave exactly as before.
func TestPlainCommandUnchanged(t *testing.T) {
	p := writeProfile(t, `fail_threshold: 2
commands:
  - name: a
    lua: gate.lua
    args:
      - name: ok
        required: true
    next: [b]
  - name: b
`, map[string]string{
		"gate.lua": `if gralph.args.ok ~= "yes" then gralph.fail("reason: pass --ok yes") end
gralph.store.set("a_done", true)`,
	})

	res := run(t, p, "a", "--ok", "no")
	wantContains(t, res, "FAILED `a` (failure 1)")
	if res.EndSession {
		t.Fatal("failure 1 of threshold 2 must keep the session")
	}
	res = run(t, p, "a", "--ok", "no")
	if !res.EndSession {
		t.Fatal("failure 2 of threshold 2 must end the session")
	}
	res = run(t, p, "a", "--ok", "yes")
	wantContains(t, res, "OK: `a` succeeded")
	if got := cursorOf(t, p); got != "b" {
		t.Fatalf("cursor = %q, want b", got)
	}
	res = run(t, p, "b")
	wantContains(t, res, "All work is complete")
	if got := cursorOf(t, p); got != DoneCursor {
		t.Fatalf("cursor = %q, want DONE", got)
	}
	res = run(t, p, "b")
	wantContains(t, res, "already complete")
}
