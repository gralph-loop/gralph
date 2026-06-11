package main

import (
	"encoding/json"
	"fmt"
	"io"
)

// runTry is `gralph try <name>`: a dry-run of one node's (or subcommand's)
// gate. It skips the cursor check entirely and commits nothing -- store reads
// hit the real file, writes stay in memory, and the failure counters,
// progress and cursor are never touched. The returned int is the exit code
// (0 on SUCCESS, 1 otherwise).
func runTry(p *Profile, name string, argv []string, w io.Writer) (int, error) {
	var (
		kind        string
		specArgs    []ArgSpec
		luaPath     string
		cmd         *CommandSpec // nil for subcommands
		timeoutNode *CommandSpec // node whose lua_timeout applies (parent for subcommands)
		prog        *Progress    // non-nil for parent finalize gates
		isSub       bool
		quotaWarn   string
	)
	switch c := p.Command(name); {
	case c != nil:
		kind, cmd, specArgs, luaPath = "command", c, c.Args, p.LuaPath(c)
		timeoutNode = c
		if len(c.Subcommands) > 0 {
			// A finalize gate reads the live progress so gralph.progress.*
			// works exactly as in a real run. An unmet quota only warns:
			// try-ing the aggregate gate early is the whole point.
			kind = "finalize command"
			pr, err := LoadProgress(p.StateDir, c.Name)
			if err != nil {
				return 0, err
			}
			prog = pr
			if !pr.QuotasMet(c) {
				quotaWarn = fmt.Sprintf("warning: subcommand quotas not met (%s); a real run would be rejected", pr.QuotaStatus(c))
			}
		}
	default:
		s, parent := p.Subcommand(name)
		if s == nil {
			return 0, fmt.Errorf("unknown command %q (not a command or subcommand of the profile)", name)
		}
		kind = fmt.Sprintf("subcommand of %q", parent.Name)
		specArgs, luaPath, isSub = s.Args, p.SubLuaPath(s), true
		timeoutNode = parent
	}

	args, err := parseArgs(name, specArgs, argv)
	if err != nil {
		fmt.Fprintln(w, "usage error:", err)
		return 1, nil
	}

	store, err := LoadStore(p.StateDir)
	if err != nil {
		return 0, err
	}

	fmt.Fprintf(w, "try: %s (%s)\n", name, kind)
	if quotaWarn != "" {
		fmt.Fprintln(w, quotaWarn)
	}
	if luaPath == "" {
		fmt.Fprintln(w, "lua: (none -- always succeeds)")
		fmt.Fprintln(w, "result: SUCCESS")
		return 0, nil
	}
	fmt.Fprintln(w, "lua:", luaPath)

	var candidates []string
	if cmd != nil {
		candidates = cmd.Next
	}
	outcome := runLua(luaPath, p.Dir, args, store, candidates, prog, isSub, p.LuaTimeoutFor(timeoutNode))
	if cmd != nil {
		// Mirror the real successor rules: a branching node whose lua never
		// called gralph.route surfaces as the same SCRIPT ERROR here.
		resolveSuccessor(cmd, &outcome)
	}

	exit := 0
	switch {
	case outcome.ScriptErr != nil:
		fmt.Fprintf(w, "result: SCRIPT ERROR: %v\n", outcome.ScriptErr)
		exit = 1
	case outcome.Failed:
		fmt.Fprintf(w, "result: FAILED: %s\n", outcome.FailReason)
		exit = 1
	default:
		fmt.Fprintln(w, "result: SUCCESS")
	}
	if outcome.Route != "" {
		fmt.Fprintf(w, "route: %s\n", outcome.Route)
	}

	dirty := store.DirtyKeys()
	if len(dirty) == 0 {
		fmt.Fprintln(w, "store writes: (none)")
	} else {
		fmt.Fprintln(w, "store writes (not committed):")
		for _, k := range dirty {
			v, _ := store.Get(k)
			data, err := json.Marshal(v)
			if err != nil {
				data = []byte(fmt.Sprintf("%#v", v))
			}
			fmt.Fprintf(w, "  %s = %s\n", k, data)
		}
	}
	return exit, nil
}
