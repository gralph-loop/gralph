package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for the operational subcommands (`status` / `reset` / `validate`):
// the reserved-name loader rule, the lint's lua and graph checks, and the
// failure-counter escape hatch for manual sessions.

// ---------------------------------------------------------------------------
// Reserved names: custom commands live under `gralph do <name>`, so only the
// namespacing word itself is reserved -- built-in words are free to use.
// ---------------------------------------------------------------------------

func TestReservedCommandNames(t *testing.T) {
	t.Run("command do", func(t *testing.T) {
		pp := filepath.Join(t.TempDir(), "profile.yaml")
		if err := os.WriteFile(pp, []byte("commands:\n  - name: do\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadProfile(pp)
		if err == nil || !strings.Contains(err.Error(), "reserved command name") {
			t.Fatalf("want reserved-name error, got %v", err)
		}
	})

	t.Run("subcommand do", func(t *testing.T) {
		pp := filepath.Join(t.TempDir(), "profile.yaml")
		yaml := `commands:
  - name: p
    subcommands:
      - name: do
`
		if err := os.WriteFile(pp, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadProfile(pp)
		if err == nil || !strings.Contains(err.Error(), "reserved command name") {
			t.Fatalf("want reserved-name error, got %v", err)
		}
	})

	// Built-in words are reachable via `gralph do <name>`, so the loader
	// accepts them -- new built-ins must never invalidate existing profiles.
	for _, name := range []string{"run", "next", "status", "try"} {
		t.Run("builtin word "+name+" allowed", func(t *testing.T) {
			pp := filepath.Join(t.TempDir(), "profile.yaml")
			yaml := "commands:\n  - name: " + name + "\n"
			if err := os.WriteFile(pp, []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfile(pp); err != nil {
				t.Fatalf("built-in word must be allowed as a command name, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validate: lua existence / syntax errors and graph warnings
// ---------------------------------------------------------------------------

func TestLintLoaderErrorIsReported(t *testing.T) {
	pp := filepath.Join(t.TempDir(), "profile.yaml")
	if err := os.WriteFile(pp, []byte("commands: []"), 0o644); err != nil {
		t.Fatal(err)
	}
	errs, _ := lintProfile(pp)
	if len(errs) != 1 || !strings.Contains(errs[0], "at least one command") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestLintLuaChecks(t *testing.T) {
	t.Run("missing lua file", func(t *testing.T) {
		p := writeProfile(t, `commands:
  - name: a
    lua: ghost.lua
`, nil)
		errs, _ := lintProfile(p.Path)
		if len(errs) != 1 || !strings.Contains(errs[0], `command "a": lua script:`) {
			t.Fatalf("errs = %v", errs)
		}
	})

	t.Run("lua syntax error", func(t *testing.T) {
		p := writeProfile(t, `commands:
  - name: p
    subcommands:
      - name: s
        lua: bad.lua
`, map[string]string{"bad.lua": "if then end"})
		errs, _ := lintProfile(p.Path)
		if len(errs) != 1 || !strings.Contains(errs[0], `subcommand "s" of "p": lua syntax:`) {
			t.Fatalf("errs = %v", errs)
		}
	})

	t.Run("valid lua compiles but never runs", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "ran")
		p := writeProfile(t, `commands:
  - name: a
    lua: gate.lua
`, map[string]string{
			// Compiling must not execute: the gate would create a marker file.
			"gate.lua": `io.open("` + marker + `", "w"):close()`,
		})
		errs, warns := lintProfile(p.Path)
		if len(errs) != 0 || len(warns) != 0 {
			t.Fatalf("errs = %v, warns = %v", errs, warns)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("lint must compile lua without executing it")
		}
	})
}

func TestLintWarnsOnFlatGuidanceInvocation(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: plan
    guidance: |
      RUN: ./gralph plan --goal x
      gralph planner is not a defined name and must not match.
      gralph do plan is the canonical form and must not match.
    next: [wrap]
  - name: wrap
    guidance: close out
`, nil)
	errs, warns := lintProfile(p.Path)
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "guidance invokes `gralph plan`, which does not dispatch; write `gralph do plan`") {
		t.Fatalf("warns = %v", warns)
	}
}

func TestLintGraphWarnings(t *testing.T) {
	t.Run("unreachable node", func(t *testing.T) {
		p := writeProfile(t, `commands:
  - name: a
    next: [b]
  - name: b
  - name: orphan
`, nil)
		errs, warns := lintProfile(p.Path)
		if len(errs) != 0 {
			t.Fatalf("errs = %v", errs)
		}
		if len(warns) != 1 || !strings.Contains(warns[0], `"orphan" is unreachable`) {
			t.Fatalf("warns = %v", warns)
		}
	})

	t.Run("no reachable terminal node", func(t *testing.T) {
		// a <-> b cycle; the only terminal node is unreachable, so DONE is too.
		p := writeProfile(t, `commands:
  - name: a
    next: [b]
  - name: b
    next: [a]
  - name: stop
`, nil)
		_, warns := lintProfile(p.Path)
		if len(warns) != 2 {
			t.Fatalf("warns = %v", warns)
		}
		joined := strings.Join(warns, "\n")
		for _, want := range []string{`"stop" is unreachable`, "can never become DONE"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("warns missing %q:\n%s", want, joined)
			}
		}
	})

	t.Run("clean graph", func(t *testing.T) {
		p := writeProfile(t, `commands:
  - name: a
    next: [b]
  - name: b
`, nil)
		errs, warns := lintProfile(p.Path)
		if len(errs) != 0 || len(warns) != 0 {
			t.Fatalf("errs = %v, warns = %v", errs, warns)
		}
	})
}

// ---------------------------------------------------------------------------
// reset: --failures keeps everything but the counters; a full reset wipes
// state.json / store.json / progress.json
// ---------------------------------------------------------------------------

func TestResetFailuresOnly(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    lua: gate.lua
`, map[string]string{"gate.lua": `gralph.fail("reason: always")`})

	if _, err := resolveNext(p); err != nil { // persist cursor "a"
		t.Fatal(err)
	}
	run(t, p, "a")
	run(t, p, "a")
	if failuresOf(t, p)["a"] != 2 {
		t.Fatal("setup: expected 2 accumulated failures")
	}
	if err := os.WriteFile(storePath(p.StateDir), []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetStateDir(p.StateDir, true); err != nil {
		t.Fatal(err)
	}
	if len(failuresOf(t, p)) != 0 {
		t.Fatal("--failures must zero the counters")
	}
	if got := cursorOf(t, p); got != "a" {
		t.Fatalf("cursor = %q; --failures must not touch the cursor", got)
	}
	store, err := LoadStore(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := store.Get("k"); v != "v" {
		t.Fatal("--failures must not touch the user store")
	}
}

func TestResetAll(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    subcommands:
      - name: s
`, nil)
	run(t, p, "s") // creates state.json, progress.json
	if err := os.WriteFile(storePath(p.StateDir), []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := resetStateDir(p.StateDir, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{statePath(p.StateDir), storePath(p.StateDir), progressPath(p.StateDir)} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s must be removed by a full reset", f)
		}
	}
	// A reset dir behaves like a fresh run.
	cursor, err := resolveNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "parent" {
		t.Fatalf("cursor after reset = %q, want parent", cursor)
	}
}

// ---------------------------------------------------------------------------
// status: report assembly (cursor, failures, quota progress)
// ---------------------------------------------------------------------------

func TestStatusReport(t *testing.T) {
	p := writeProfile(t, flowProfile, nil)
	run(t, p, "sub-a", "--item", "x")

	r, err := buildStatus(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cursor != "parent" {
		t.Fatalf("cursor = %q, want parent", r.Cursor)
	}
	if len(r.Subcommands) != 2 {
		t.Fatalf("subcommands = %+v", r.Subcommands)
	}
	a, b := r.Subcommands[0], r.Subcommands[1]
	if a.Name != "sub-a" || a.Done != 1 || a.Count != 2 || len(a.Keys) != 1 || a.Keys[0] != "x" {
		t.Fatalf("sub-a status = %+v", a)
	}
	if b.Name != "sub-b" || b.Done != 0 || b.Count != 1 {
		t.Fatalf("sub-b status = %+v", b)
	}
}
