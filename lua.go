package main

import (
	"fmt"

	lua "github.com/yuin/gopher-lua"
)

// LuaOutcome distinguishes a validation failure (gralph.fail) from a script
// crash (lua error()). Both count toward the failure threshold, but they are
// reported differently to the agent.
type LuaOutcome struct {
	Failed     bool   // gralph.fail was called
	FailReason string // reason passed to gralph.fail
	ScriptErr  error  // lua raised an error / bridge misuse
	Route      string // gralph.route choice ("" if not called)
}

// runLua executes the command's external lua script with the YAML-declared
// arguments and the user store exposed through the `gralph` helper object.
func runLua(script string, args map[string]string, store *Store, candidates []string) LuaOutcome {
	out := LuaOutcome{}

	L := lua.NewState()
	defer L.Close()

	g := L.NewTable()

	// gralph.args.<name>
	argsT := L.NewTable()
	for k, v := range args {
		argsT.RawSetString(k, lua.LString(v))
	}
	g.RawSetString("args", argsT)

	// gralph.store.get / gralph.store.set
	storeT := L.NewTable()
	storeT.RawSetString("get", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		v, ok := store.Get(key)
		if !ok {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(goToLua(L, v))
		return 1
	}))
	storeT.RawSetString("set", L.NewFunction(func(L *lua.LState) int {
		key := L.CheckString(1)
		val := L.CheckAny(2)
		gv, err := luaToGo(val)
		if err != nil {
			L.RaiseError("gralph.store.set(%q): %s", key, err.Error())
			return 0
		}
		store.Set(key, gv)
		return 0
	}))
	g.RawSetString("store", storeT)

	// gralph.route("name") -- only meaningful with multiple candidates;
	// a name outside the candidate list is a runtime error.
	g.RawSetString("route", L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		for _, c := range candidates {
			if c == name {
				out.Route = name
				return 0
			}
		}
		L.RaiseError("gralph.route(%q): not a successor candidate %v", name, candidates)
		return 0
	}))

	// gralph.fail("reason: ...") -- marks validation failure; the script may
	// keep running. If never called (and no error), the run is a success.
	g.RawSetString("fail", L.NewFunction(func(L *lua.LState) int {
		reason := L.OptString(1, "(no reason given)")
		if !out.Failed {
			out.Failed = true
			out.FailReason = reason
		}
		return 0
	}))

	L.SetGlobal("gralph", g)

	if err := L.DoFile(script); err != nil {
		// Distinguish "script died" from a deliberate gralph.fail.
		out.ScriptErr = err
	}
	return out
}

// ---------------------------------------------------------------------------
// Value conversion between Lua and the JSON-backed store.
// Scalars plus (nested) tables are supported; tables with consecutive
// integer keys become arrays, otherwise string-keyed maps.
// ---------------------------------------------------------------------------

func goToLua(L *lua.LState, v any) lua.LValue {
	switch t := v.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(t)
	case float64:
		return lua.LNumber(t)
	case int:
		return lua.LNumber(t)
	case string:
		return lua.LString(t)
	case []any:
		tbl := L.NewTable()
		for i, e := range t {
			tbl.RawSetInt(i+1, goToLua(L, e))
		}
		return tbl
	case map[string]any:
		tbl := L.NewTable()
		for k, e := range t {
			tbl.RawSetString(k, goToLua(L, e))
		}
		return tbl
	default:
		return lua.LString(fmt.Sprintf("%v", t))
	}
}

func luaToGo(v lua.LValue) (any, error) {
	switch t := v.(type) {
	case *lua.LNilType:
		return nil, nil
	case lua.LBool:
		return bool(t), nil
	case lua.LNumber:
		return float64(t), nil
	case lua.LString:
		return string(t), nil
	case *lua.LTable:
		// Array if it has only consecutive integer keys starting at 1.
		n := t.Len()
		isArray := n > 0
		t.ForEach(func(k, _ lua.LValue) {
			if _, ok := k.(lua.LNumber); !ok {
				isArray = false
			}
		})
		if isArray {
			arr := make([]any, 0, n)
			var convErr error
			for i := 1; i <= n; i++ {
				gv, err := luaToGo(t.RawGetInt(i))
				if err != nil {
					convErr = err
					break
				}
				arr = append(arr, gv)
			}
			return arr, convErr
		}
		m := map[string]any{}
		var convErr error
		t.ForEach(func(k, val lua.LValue) {
			gv, err := luaToGo(val)
			if err != nil {
				convErr = err
				return
			}
			m[fmt.Sprintf("%v", k)] = gv
		})
		return m, convErr
	default:
		return nil, fmt.Errorf("unsupported lua type %s", v.Type())
	}
}
