package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the behavior that predates subcommands: the loader's classic
// validation rules, arg parsing, the single-success command contract,
// routing, commit-on-success, and guidance rendering. The repo shipped
// without tests, so these pin the legacy contract alongside the new feature.

// ---------------------------------------------------------------------------
// Loader validation (the classic rules)
// ---------------------------------------------------------------------------

func TestValidateCoreRules(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{
			"zero commands",
			`commands: []`,
			"at least one command",
		},
		{
			"command without name",
			`commands:
  - guidance: g`,
			"has no name",
		},
		{
			"reserved name DONE",
			`commands:
  - name: DONE`,
			"reserved",
		},
		{
			"duplicate command name",
			`commands:
  - name: a
  - name: a`,
			"duplicate command name",
		},
		{
			"unknown successor",
			`commands:
  - name: a
    next: [ghost]`,
			`unknown successor "ghost"`,
		},
		{
			"multiple successors without lua",
			`commands:
  - name: a
    next: [b, c]
  - name: b
  - name: c`,
			"multiple successors but no lua",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pp := filepath.Join(t.TempDir(), "profile.yaml")
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

func TestProfileDefaults(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
`, nil)
	if p.FailThreshold != DefaultFailThreshold {
		t.Fatalf("fail_threshold default = %d, want %d", p.FailThreshold, DefaultFailThreshold)
	}
	if p.Prompt != DefaultPrompt {
		t.Fatal("prompt default not applied")
	}
	// writeProfile writes "profile.yaml", so the derived name is "profile"
	// and the default state dir is keyed by it: ".gralph/profile".
	if p.Name != "profile" {
		t.Fatalf("name default = %q, want %q", p.Name, "profile")
	}
	want := filepath.Join(".gralph", "profile")
	if !filepath.IsAbs(p.StateDir) || !strings.HasSuffix(p.StateDir, want) {
		t.Fatalf("state_dir default = %q, want suffix %q", p.StateDir, want)
	}
}

// The instance name keys the default state dir, so one profile definition
// can drive several isolated flows, and two profiles sharing a workspace
// never collide -- all without anyone setting state_dir.
func TestInstanceNameKeysStateDir(t *testing.T) {
	dir := t.TempDir()
	write := func(file string) string {
		t.Helper()
		pp := filepath.Join(dir, file)
		if err := os.WriteFile(pp, []byte("commands:\n  - name: a\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return pp
	}
	load := func(pp, instance string) *Profile {
		t.Helper()
		p, err := LoadProfileAs(pp, instance)
		if err != nil {
			t.Fatalf("LoadProfileAs(%s, %q): %v", pp, instance, err)
		}
		return p
	}

	build := write("build.yaml")
	review := write("review.yaml")
	if got := load(build, "").StateDir; got != filepath.Join(dir, ".gralph", "build") {
		t.Fatalf("derived-instance state dir = %q", got)
	}
	if got := load(review, "").StateDir; got != filepath.Join(dir, ".gralph", "review") {
		t.Fatalf("second profile's state dir = %q", got)
	}
	// One definition, two instances: --name picks the flow.
	if got := load(build, "feat-a").StateDir; got != filepath.Join(dir, ".gralph", "feat-a") {
		t.Fatalf("explicit-instance state dir = %q", got)
	}

	// An explicit state_dir stays authoritative regardless of the instance.
	pp := filepath.Join(dir, "third.yaml")
	if err := os.WriteFile(pp, []byte("state_dir: custom-state\ncommands:\n  - name: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := load(pp, "feat-a").StateDir; got != filepath.Join(dir, "custom-state") {
		t.Fatalf("explicit state_dir = %q", got)
	}
}

func TestInstanceNameMustBePathComponent(t *testing.T) {
	pp := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(pp, []byte("commands:\n  - name: a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a/b", `a\b`, ".", ".."} {
		if _, err := LoadProfileAs(pp, name); err == nil || !strings.Contains(err.Error(), "not usable as a directory name") {
			t.Fatalf("instance %q: want directory-name error, got %v", name, err)
		}
	}
}

// --profile and --name are stripped by the CLI before a custom command sees
// its args, so the loader must reject them as declared arg names.
func TestReservedArgNames(t *testing.T) {
	for _, yaml := range []string{
		"commands:\n  - name: a\n    args:\n      - { name: name }\n",
		"commands:\n  - name: a\n    args:\n      - { name: profile }\n",
		"commands:\n  - name: p\n    subcommands:\n      - name: s\n        args:\n          - { name: name }\n",
	} {
		pp := filepath.Join(t.TempDir(), "profile.yaml")
		if err := os.WriteFile(pp, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProfile(pp); err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("want reserved-arg error, got %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// Argument parsing
// ---------------------------------------------------------------------------

func TestParseArgsForms(t *testing.T) {
	specs := []ArgSpec{{Name: "report", Required: true}, {Name: "note"}}

	// Both --name value and --name=value are accepted.
	got, err := parseArgs("cmd", specs, []string{"--report", "r.json", "--note=hi there"})
	if err != nil {
		t.Fatal(err)
	}
	if got["report"] != "r.json" || got["note"] != "hi there" {
		t.Fatalf("parsed %v", got)
	}

	for _, tc := range []struct {
		argv    []string
		wantErr string
	}{
		{[]string{"report"}, "unexpected token"},
		{[]string{"--report"}, "missing value for --report"},
		{[]string{"--report", "r", "--bogus", "x"}, "unknown argument --bogus"},
		{[]string{"--note", "hi"}, "missing required argument --report"},
	} {
		if _, err := parseArgs("cmd", specs, tc.argv); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Fatalf("argv %v: want error containing %q, got %v", tc.argv, tc.wantErr, err)
		}
	}
}

func TestUsageErrorConsumesNoBudget(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: report
        required: true
`, nil)
	res := run(t, p, "a", "--bogus", "x")
	wantContains(t, res, "usage error")
	if res.ExitCode != 1 || res.EndSession {
		t.Fatalf("usage error must be exit 1 / keep session, got %+v", res)
	}
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("usage errors must not consume the failure budget")
	}
}

// ---------------------------------------------------------------------------
// The command contract: wrong command, unknown command, DONE
// ---------------------------------------------------------------------------

func TestWrongAndUnknownCommand(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: first
    next: [second]
  - name: second
`, nil)

	// Only the cursor command may run; rejection costs no budget.
	res := run(t, p, "second")
	wantContains(t, res, "`second` is not the current command")
	wantContains(t, res, "The current command is `first`")
	if res.ExitCode != 1 || len(failuresOf(t, p)) != 0 {
		t.Fatalf("wrong command must be a budget-free rejection, got %+v", res)
	}

	// A name in no profile at all is a hard error, not a CommandResult.
	if _, err := runCustomCommand(p, "ghost", nil); err == nil ||
		!strings.Contains(err.Error(), `unknown command "ghost"`) {
		t.Fatalf("want unknown-command error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func TestRoutingContract(t *testing.T) {
	const yaml = `commands:
  - name: verify
    args:
      - name: to
    lua: route.lua
    next: [fix, finish]
  - name: fix
  - name: finish
`
	// The gate routes wherever --to says; the contract under test is the
	// bridge, not the gate's wisdom.
	const luaSrc = `if gralph.args.to ~= nil then gralph.route(gralph.args.to) end`

	t.Run("route to a valid candidate", func(t *testing.T) {
		p := writeProfile(t, yaml, map[string]string{"route.lua": luaSrc})
		res := run(t, p, "verify", "--to", "finish")
		wantContains(t, res, "OK: `verify` succeeded")
		if got := cursorOf(t, p); got != "finish" {
			t.Fatalf("cursor = %q, want finish", got)
		}
	})

	t.Run("route to a non-candidate is a script error", func(t *testing.T) {
		p := writeProfile(t, yaml, map[string]string{"route.lua": luaSrc})
		res := run(t, p, "verify", "--to", "nowhere")
		wantContains(t, res, "SCRIPT ERROR")
		wantContains(t, res, "not a successor candidate")
		if failuresOf(t, p)["verify"] != 1 {
			t.Fatal("a script error must count toward the failure threshold")
		}
	})

	t.Run("finishing without route is a script error", func(t *testing.T) {
		p := writeProfile(t, yaml, map[string]string{"route.lua": luaSrc})
		res := run(t, p, "verify") // --to absent -> lua never routes
		wantContains(t, res, "SCRIPT ERROR")
		wantContains(t, res, "without gralph.route()")
		if got := cursorOf(t, p); got == "fix" || got == "finish" {
			t.Fatalf("cursor must not advance on a routing error, got %q", got)
		}
	})
}

// ---------------------------------------------------------------------------
// Store: commit-on-success only, value round-trip, guidance feed-forward
// ---------------------------------------------------------------------------

func TestStoreCommitOnSuccessOnly(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: ok
        required: true
    lua: gate.lua
    next: [b]
  - name: b
    guidance: 'goal is {{store "goal"}} on {{.Cursor}}'
`, map[string]string{
		// The gate writes BEFORE deciding -- the attempts gotcha: a value set
		// on a run that then fails must not be persisted.
		"gate.lua": `gralph.store.set("goal", "demo")
gralph.store.set("nested", {tags = {"x", "y"}, n = 2})
if gralph.args.ok ~= "yes" then gralph.fail("reason: pass --ok yes") end`,
	})

	run(t, p, "a", "--ok", "no")
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("goal"); ok {
		t.Fatal("store must not be committed on a failed validation")
	}

	run(t, p, "a", "--ok", "yes")
	store, err = LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Get("goal"); v != "demo" {
		t.Fatalf("goal = %#v, want demo", v)
	}
	// Nested lua table round-trips through the JSON store.
	nested, _ := store.Get("nested")
	m, ok := nested.(map[string]any)
	if !ok || m["n"] != float64(2) {
		t.Fatalf("nested = %#v", nested)
	}
	tags, ok := m["tags"].([]any)
	if !ok || len(tags) != 2 || tags[0] != "x" {
		t.Fatalf("nested.tags = %#v", m["tags"])
	}

	// The committed value feeds the next node's guidance template.
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Current task: b",
		"goal is demo on b", // {{store "goal"}} and {{.Cursor}}
		"run the command above exactly once",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderNext missing %q:\n%s", want, out)
		}
	}
}

// ---------------------------------------------------------------------------
// Failure reporting: gralph.fail vs lua error(), threshold override
// ---------------------------------------------------------------------------

func TestFailVsScriptError(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: mode
        required: true
    lua: gate.lua
`, map[string]string{
		"gate.lua": `if gralph.args.mode == "fail" then
  gralph.fail("reason: deliberate")
else
  error("boom")
end`,
	})

	res := run(t, p, "a", "--mode", "fail")
	wantContains(t, res, "FAILED `a` (failure 1): reason: deliberate")
	res = run(t, p, "a", "--mode", "crash")
	wantContains(t, res, "SCRIPT ERROR in `a` (failure 2)")
	if failuresOf(t, p)["a"] != 2 {
		t.Fatal("both kinds must share one failure counter")
	}
}

func TestPerCommandThresholdOverride(t *testing.T) {
	p := writeProfile(t, `fail_threshold: 5
commands:
  - name: a
    fail_threshold: 2
    lua: gate.lua
`, map[string]string{
		"gate.lua": `gralph.fail("reason: always")`,
	})
	if run(t, p, "a").EndSession {
		t.Fatal("failure 1 of override threshold 2 must keep the session")
	}
	if !run(t, p, "a").EndSession {
		t.Fatal("failure 2 must end the session (per-command override, not profile's 5)")
	}
	// The n-th failure recycles every n-th time, not only once.
	run(t, p, "a")
	if !run(t, p, "a").EndSession {
		t.Fatal("failure 4 must end the session again (k %% threshold == 0)")
	}
}

// ---------------------------------------------------------------------------
// resolveNext: cursor initialization and DONE rendering
// ---------------------------------------------------------------------------

func TestResolveNextInitializesCursor(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: entry
`, nil)
	cursor, err := resolveNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "entry" {
		t.Fatalf("cursor = %q, want entry", cursor)
	}
	// The initialization is persisted.
	if got := cursorOf(t, p); got != "entry" {
		t.Fatalf("persisted cursor = %q, want entry", got)
	}

	run(t, p, "entry")
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, &CommandResult{Message: out}, "All work is complete")
}
