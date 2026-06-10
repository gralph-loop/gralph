package main

import (
	"fmt"
	"strings"
)

// CommandResult is what gets printed back to the agent.
type CommandResult struct {
	Message    string
	EndSession bool
	ExitCode   int
}

// runCustomCommand handles `gralph <name> --arg value ...` invoked by the
// agent inside a session.
//
// Contract (shared by every custom command):
//   - success  -> cursor advances immediately, store is committed, and the
//     response ALWAYS tells the agent to end the session.
//   - failure  -> the session stays alive so the agent can retry, except on
//     every n-th failure (default 5) the response also ends the session,
//     forcing a fresh session / fresh context on the next loop iteration.
func runCustomCommand(p *Profile, name string, argv []string) (*CommandResult, error) {
	st, err := LoadState(p.StateDir)
	if err != nil {
		return nil, err
	}
	if st.Cursor == "" {
		st.Cursor = p.FirstCommand().Name
	}

	if st.Cursor == DoneCursor {
		return &CommandResult{
			Message:    "All work is already complete. End the session now.",
			EndSession: true,
		}, nil
	}

	cmd := p.Command(name)
	if cmd == nil {
		return nil, fmt.Errorf("unknown command %q (run `gralph next` to see what to do)", name)
	}

	// Only the instructed (= cursor) command may run in this session.
	if name != st.Cursor {
		return &CommandResult{
			Message: fmt.Sprintf(
				"`%s` is not the current command. The current command is `%s`. Run `gralph next` for instructions.",
				name, st.Cursor),
			ExitCode: 1,
		}, nil
	}

	args, err := parseArgs(cmd, argv)
	if err != nil {
		// Argument-shape mistakes are usage errors, not validation failures:
		// they don't consume the failure budget.
		return &CommandResult{
			Message:  "usage error: " + err.Error(),
			ExitCode: 1,
		}, nil
	}

	store, err := LoadStore(p.StateDir)
	if err != nil {
		return nil, err
	}

	// --- deterministic logic -------------------------------------------------
	var outcome LuaOutcome
	if lp := p.LuaPath(cmd); lp != "" {
		outcome = runLua(lp, args, store, cmd.Next)
	}

	// Routing resolution on (tentative) success.
	next := ""
	if !outcome.Failed && outcome.ScriptErr == nil {
		switch len(cmd.Next) {
		case 0:
			next = DoneCursor // last command
		case 1:
			next = cmd.Next[0] // unconditional move
		default:
			if outcome.Route == "" {
				outcome.ScriptErr = fmt.Errorf(
					"lua finished without gralph.route() but %q has %d successor candidates %v",
					cmd.Name, len(cmd.Next), cmd.Next)
			} else {
				next = outcome.Route
			}
		}
	}

	// --- failure path ---------------------------------------------------------
	if outcome.Failed || outcome.ScriptErr != nil {
		st.Failures[name]++
		count := st.Failures[name]
		if err := st.Save(p.StateDir); err != nil {
			return nil, err
		}
		// Store is intentionally NOT committed on failure.

		threshold := p.ThresholdFor(cmd)
		end := count%threshold == 0

		var b strings.Builder
		if outcome.ScriptErr != nil {
			fmt.Fprintf(&b, "SCRIPT ERROR in `%s` (failure %d): %v\n", name, count, outcome.ScriptErr)
		} else {
			fmt.Fprintf(&b, "FAILED `%s` (failure %d): %s\n", name, count, outcome.FailReason)
		}
		if end {
			b.WriteString("Too many failures in this session. End the session now.")
		} else {
			b.WriteString("Fix the issue and run the command again in this session.")
		}
		return &CommandResult{Message: b.String(), EndSession: end, ExitCode: 1}, nil
	}

	// --- success path -----------------------------------------------------------
	// Cursor advances immediately; the next loop's `next` renders the new
	// node's guidance from the (committed) store.
	if err := store.Commit(p.StateDir); err != nil {
		return nil, err
	}
	st.Cursor = next
	if err := st.Save(p.StateDir); err != nil {
		return nil, err
	}

	msg := fmt.Sprintf("OK: `%s` succeeded.", name)
	if next == DoneCursor {
		msg += " All work is complete."
	}
	msg += " End the session now."
	return &CommandResult{Message: msg, EndSession: true}, nil
}

// parseArgs accepts `--name value` (or `--name=value`) pairs and checks them
// against the command's YAML arg spec.
func parseArgs(cmd *CommandSpec, argv []string) (map[string]string, error) {
	declared := map[string]*ArgSpec{}
	for i := range cmd.Args {
		declared[cmd.Args[i].Name] = &cmd.Args[i]
	}

	got := map[string]string{}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if !strings.HasPrefix(a, "--") {
			return nil, fmt.Errorf("unexpected token %q (arguments must be --name value)", a)
		}
		a = strings.TrimPrefix(a, "--")
		var key, val string
		if eq := strings.IndexByte(a, '='); eq >= 0 {
			key, val = a[:eq], a[eq+1:]
		} else {
			key = a
			if i+1 >= len(argv) {
				return nil, fmt.Errorf("missing value for --%s", key)
			}
			i++
			val = argv[i]
		}
		if _, ok := declared[key]; !ok {
			return nil, fmt.Errorf("unknown argument --%s for command %q", key, cmd.Name)
		}
		got[key] = val
	}
	for name, spec := range declared {
		if spec.Required {
			if _, ok := got[name]; !ok {
				return nil, fmt.Errorf("missing required argument --%s", name)
			}
		}
	}
	return got, nil
}
