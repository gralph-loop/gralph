package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	lua "github.com/yuin/gopher-lua"
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
	out := runLua(script, "", nil, store, nil, nil, false, 100*time.Millisecond)
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
	out := runLua(script, "", nil, store, nil, nil, false, 0)
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

// runLuaSource writes src to a temp script and runs it through runLua,
// so tests exercise the real bridge (gralph helper included).
func runLuaSource(t *testing.T, src string, store *Store) LuaOutcome {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "test.lua")
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return runLua(script, dir, map[string]string{}, store, nil, nil, false, 0)
}

// TestGoLuaRoundTrip checks goToLua -> luaToGo is the identity for every
// JSON-representable value shape the store can hold.
func TestGoLuaRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"nil", nil},
		{"bool", true},
		{"number", 3.5},
		{"string", "hello"},
		{"empty string", ""},
		{"array", []any{1.0, "two", false}},
		{"nested map", map[string]any{
			"a": []any{1.0, 2.0},
			"b": map[string]any{"c": "d"},
		}},
		{"array of maps", []any{map[string]any{"k": "v"}, map[string]any{}}},
	}

	L := lua.NewState()
	defer L.Close()

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := luaToGo(goToLua(L, c.v))
			if err != nil {
				t.Fatalf("luaToGo: %v", err)
			}
			if !reflect.DeepEqual(got, c.v) {
				t.Errorf("round trip = %#v, want %#v", got, c.v)
			}
		})
	}
}

// TestLuaToGoTables checks the table conversion rules through the real
// bridge: lua sets a value via gralph.store.set and the test reads it back
// from the Store.
func TestLuaToGoTables(t *testing.T) {
	cases := []struct {
		name string
		expr string // lua expression passed to gralph.store.set
		want any
	}{
		{"nil scalar", "nil", nil},
		{"bool scalar", "true", true},
		{"number scalar", "42", 42.0},
		{"string scalar", `"hi"`, "hi"},
		{"consecutive array", `{"a", "b", "c"}`, []any{"a", "b", "c"}},
		{"string-keyed map", `{x = 1, y = 2}`, map[string]any{"x": 1.0, "y": 2.0}},
		{"nested table", `{a = {b = {1, 2}}}`,
			map[string]any{"a": map[string]any{"b": []any{1.0, 2.0}}}},
		// Regression for issue #2: a sparse table must not be truncated to
		// its first run; it becomes a string-keyed map with nothing lost.
		{"sparse array kept as map", `{[1] = "a", [3] = "b"}`,
			map[string]any{"1": "a", "3": "b"}},
		{"zero-based keys kept as map", `{[0] = "z", [1] = "a"}`,
			map[string]any{"0": "z", "1": "a"}},
		{"mixed keys kept as map", `{[1] = "a", x = "b"}`,
			map[string]any{"1": "a", "x": "b"}},
		{"non-integer number key kept as map", `{[1.5] = "a"}`,
			map[string]any{"1.5": "a"}},
		{"empty table is a map", `{}`, map[string]any{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &Store{values: map[string]any{}, dirtyKeys: map[string]struct{}{}}
			out := runLuaSource(t, `gralph.store.set("k", `+c.expr+`)`, store)
			if out.ScriptErr != nil {
				t.Fatalf("script error: %v", out.ScriptErr)
			}
			if out.Failed {
				t.Fatalf("unexpected gralph.fail: %s", out.FailReason)
			}
			got, ok := store.Get("k")
			if !ok {
				t.Fatal("store.set left no value")
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("store value = %#v, want %#v", got, c.want)
			}
		})
	}
}

// TestLuaProfileDir checks that the script sees the profile directory as an
// absolute path through gralph.profile_dir.
func TestLuaProfileDir(t *testing.T) {
	store := &Store{values: map[string]any{}, dirtyKeys: map[string]struct{}{}}
	dir := t.TempDir()
	script := filepath.Join(dir, "test.lua")
	src := `gralph.store.set("dir", gralph.profile_dir)`
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runLua(script, dir, map[string]string{}, store, nil, nil, false, 0)
	if out.ScriptErr != nil {
		t.Fatalf("script error: %v", out.ScriptErr)
	}
	got, _ := store.Get("dir")
	if got != dir {
		t.Errorf("gralph.profile_dir = %#v, want %#v", got, dir)
	}
}
