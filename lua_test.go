package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #6: lua_timeout aborts a runaway script; the abort is classified as
// a SCRIPT ERROR (ScriptErr), not a validation failure.
func TestRunLuaTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "spin.lua")
	if err := os.WriteFile(script, []byte("while true do end\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &Store{values: map[string]any{}, dirtyKeys: map[string]struct{}{}}

	start := time.Now()
	out := runLua(script, nil, store, nil, nil, false, 100*time.Millisecond)
	if time.Since(start) > 5*time.Second {
		t.Fatal("runLua did not honor the timeout")
	}
	if out.ScriptErr == nil {
		t.Fatal("expected ScriptErr on lua timeout, got success")
	}
	if out.Failed {
		t.Fatal("timeout must not be reported as gralph.fail")
	}
}

// Without a timeout configured the previous behavior is kept: the script
// runs to completion.
func TestRunLuaNoTimeout(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ok.lua")
	if err := os.WriteFile(script, []byte("-- noop\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &Store{values: map[string]any{}, dirtyKeys: map[string]struct{}{}}
	out := runLua(script, nil, store, nil, nil, false, 0)
	if out.ScriptErr != nil || out.Failed {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

// Command-level lua_timeout overrides the profile default.
func TestLuaTimeoutResolution(t *testing.T) {
	p := writeProfile(t, `
agent:
  command: ["true"]
lua_timeout: 10s
commands:
  - name: a
    guidance: g
    lua: a.lua
    lua_timeout: 2s
    next: [b]
  - name: b
    guidance: g
`, nil)
	if got := p.LuaTimeoutFor(p.Command("a")); got != 2*time.Second {
		t.Fatalf("command override: got %s, want 2s", got)
	}
	if got := p.LuaTimeoutFor(p.Command("b")); got != 10*time.Second {
		t.Fatalf("profile default: got %s, want 10s", got)
	}
}

// Bad duration strings are rejected at load time.
func TestTimeoutValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yaml")
	yaml := `
agent:
  command: ["true"]
  timeout: not-a-duration
commands:
  - name: a
    guidance: g
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadProfile(path)
	if err == nil || !strings.Contains(err.Error(), "agent.timeout") {
		t.Fatalf("expected agent.timeout parse error, got %v", err)
	}
}
