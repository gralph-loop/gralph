package main

import (
	"fmt"
	"strings"
	"time"
)

// CommandResult is what gets printed back to the agent.
type CommandResult struct {
	Message    string
	EndSession bool
	ExitCode   int
}

// runCustomCommand handles `gralph <name> --arg value ...` invoked by the
// agent inside a session. <name> is either a command (graph node) or a
// subcommand of one.
//
// Contract (shared by every custom command):
//   - success  -> cursor advances immediately, store is committed, and the
//     response ALWAYS tells the agent to end the session.
//   - failure  -> the session stays alive so the agent can retry, except on
//     every n-th failure (default 5) the response also ends the session,
//     forcing a fresh session / fresh context on the next loop iteration.
//
// Commands with subcommands relax the first rule into a fork/join: while the
// cursor is on the parent, each subcommand must succeed once per distinct
// work-item key until every quota is met; only then does the parent itself
// run (as the finalize gate) and advance the cursor. A subcommand success
// still ends the (sub-)session, but the cursor stays on the parent.
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

	if cmd := p.Command(name); cmd != nil {
		// Only the instructed (= cursor) command may run in this session.
		if name != st.Cursor {
			return &CommandResult{
				Message: fmt.Sprintf(
					"`%s` is not the current command. The current command is `%s`. Run `gralph next` for instructions.",
					name, st.Cursor),
				ExitCode: 1,
			}, nil
		}
		if len(cmd.Subcommands) > 0 {
			return runParentFinalize(p, cmd, argv)
		}
		return runPlainCommand(p, cmd, argv)
	}

	if sub, parent := p.Subcommand(name); sub != nil {
		if parent.Name != st.Cursor {
			return &CommandResult{
				Message: fmt.Sprintf(
					"`%s` is a subcommand of `%s`, which is not the current command. The current command is `%s`. Run `gralph next` for instructions.",
					name, parent.Name, st.Cursor),
				ExitCode: 1,
			}, nil
		}
		return runSubcommand(p, parent, sub, argv)
	}

	return nil, fmt.Errorf("unknown command %q (run `gralph next` to see what to do)", name)
}

// runPlainCommand is the classic single-success node: validate, then advance.
func runPlainCommand(p *Profile, cmd *CommandSpec, argv []string) (*CommandResult, error) {
	args, err := parseArgs(cmd.Name, cmd.Args, argv)
	if err != nil {
		// Argument-shape mistakes are usage errors, not validation failures:
		// they don't consume the failure budget.
		return &CommandResult{Message: "usage error: " + err.Error(), ExitCode: 1}, nil
	}

	store, err := LoadStore(p.StateDir)
	if err != nil {
		return nil, err
	}

	// --- deterministic logic (outside the lock; gates may be slow) ------------
	var outcome LuaOutcome
	var gateMs int64
	if lp := p.LuaPath(cmd); lp != "" {
		gateStart := time.Now()
		outcome = runLua(lp, p.Dir, args, store, cmd.Next, nil, false, p.LuaTimeoutFor(cmd))
		gateMs = time.Since(gateStart).Milliseconds()
	}

	next := resolveSuccessor(cmd, &outcome)
	if outcome.Failed || outcome.ScriptErr != nil {
		return commitFailure(p, cmd.Name, cmd.Name, p.ThresholdFor(cmd), outcome)
	}
	return commitSuccess(p, cmd, next, store, false, 0, gateMs)
}

// runSubcommand validates one work item of a parent's quota and records it in
// the progress file. The cursor does not move.
func runSubcommand(p *Profile, parent *CommandSpec, sub *SubcommandSpec, argv []string) (*CommandResult, error) {
	args, err := parseArgs(sub.Name, sub.Args, argv)
	if err != nil {
		return &CommandResult{Message: "usage error: " + err.Error(), ExitCode: 1}, nil
	}

	itemKey := sub.Name // single-slot subcommand (count 1, no key arg)
	if sub.Key != "" {
		itemKey = strings.TrimSpace(args[sub.Key])
		if itemKey == "" {
			return &CommandResult{
				Message:  fmt.Sprintf("usage error: --%s (the work-item key) must not be empty", sub.Key),
				ExitCode: 1,
			}, nil
		}
	}

	// Early duplicate check: fast feedback only. The authoritative check runs
	// again under the lock at commit time, because two workers may pass the
	// gate for the same key concurrently.
	pr, err := LoadProgress(p.StateDir, parent.Name)
	if err != nil {
		return nil, err
	}
	if _, dup := pr.Done[sub.Name][itemKey]; dup {
		return duplicateResult(parent, sub, itemKey, pr), nil
	}

	store, err := LoadStore(p.StateDir)
	if err != nil {
		return nil, err
	}

	// --- deterministic logic (outside the lock; gates may be slow) ------------
	var outcome LuaOutcome
	if lp := p.SubLuaPath(sub); lp != "" {
		outcome = runLua(lp, p.Dir, args, store, nil, nil, true, p.LuaTimeoutFor(parent))
	}

	if outcome.Failed || outcome.ScriptErr != nil {
		// Failures are budgeted per (subcommand, key) so one stuck worker
		// doesn't recycle its siblings.
		label := fmt.Sprintf("%s (%s)", sub.Name, itemKey)
		return commitFailure(p, label, sub.Name+":"+itemKey, p.ThresholdForSub(parent, sub), outcome)
	}

	// --- locked commit ---------------------------------------------------------
	var res *CommandResult
	err = withStateLock(p.StateDir, func() error {
		st, err := LoadState(p.StateDir)
		if err != nil {
			return err
		}
		// The cursor may have moved while the gate ran (a straggler worker
		// after the parent finalized). Recording then would poison a future
		// revisit of the node, so re-check.
		cur := st.Cursor
		if cur == "" {
			cur = p.FirstCommand().Name
		}
		if cur != parent.Name {
			res = &CommandResult{
				Message: fmt.Sprintf(
					"`%s` is no longer runnable: the current command is `%s`. Run `gralph next` for instructions.",
					sub.Name, st.Cursor),
				ExitCode: 1,
			}
			return nil
		}
		pr, err := LoadProgress(p.StateDir, parent.Name)
		if err != nil {
			return err
		}
		if _, dup := pr.Done[sub.Name][itemKey]; dup {
			// Another worker committed this key while our gate ran. The item
			// is done either way; reject without consuming the budget.
			res = duplicateResult(parent, sub, itemKey, pr)
			return nil
		}
		if err := store.Commit(p.StateDir); err != nil {
			return err
		}
		pr.Record(sub.Name, itemKey, DoneMeta{
			At:      time.Now().UTC().Format(time.RFC3339),
			Session: st.SessionID,
		})
		if err := pr.Save(p.StateDir); err != nil {
			return err
		}
		// This work item is done; drop its failure memory.
		fr, err := LoadFailures(p.StateDir)
		if err != nil {
			return err
		}
		if fr.Clear(sub.Name + ":" + itemKey) {
			if err := fr.Save(p.StateDir); err != nil {
				return err
			}
		}
		appendJournal(p.StateDir, JournalEvent{
			Event:      EvSubitemRecorded,
			Session:    st.SessionID,
			Subcommand: sub.Name,
			Key:        itemKey,
		})
		if st.Cursor == "" {
			// First run before any resolveNext: persist the implicit cursor
			// so on-disk state matches what just got recorded.
			st.Cursor = parent.Name
			if err := st.Save(p.StateDir); err != nil {
				return err
			}
		}
		msg := fmt.Sprintf("OK: `%s` (%s) recorded. Progress: %s.", sub.Name, itemKey, pr.QuotaStatus(parent))
		if pr.QuotasMet(parent) {
			msg += fmt.Sprintf(" All subcommand quotas met; `%s` is now runnable.", parent.Name)
		}
		msg += " End the session now."
		res = &CommandResult{Message: msg, EndSession: true}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// runParentFinalize runs a subcommand-bearing node as its own finalize gate:
// rejected until every quota is met, then validated (with read access to the
// completed work items) and, on success, the progress is cleared and the
// cursor advances.
func runParentFinalize(p *Profile, cmd *CommandSpec, argv []string) (*CommandResult, error) {
	args, err := parseArgs(cmd.Name, cmd.Args, argv)
	if err != nil {
		return &CommandResult{Message: "usage error: " + err.Error(), ExitCode: 1}, nil
	}

	// Sequencing error, like running the wrong command: no budget consumed.
	// Quotas are monotonic, so they cannot become unmet at commit time -- but
	// the commit still re-checks for items recorded AFTER this load (see
	// seenDone below).
	pr, err := LoadProgress(p.StateDir, cmd.Name)
	if err != nil {
		return nil, err
	}
	if !pr.QuotasMet(cmd) {
		return &CommandResult{
			Message: fmt.Sprintf(
				"`%s` is not runnable yet: subcommand quotas not met (%s). Run the remaining subcommands first.",
				cmd.Name, pr.QuotaStatus(cmd)),
			ExitCode: 1,
		}, nil
	}
	// Snapshot how many work items this finalize run gets to validate. A
	// straggler worker may commit another key while the lua runs outside the
	// lock; that worker was told "OK: recorded", so the commit must not
	// silently clear a record this run never saw.
	seenDone := pr.TotalDone()

	store, err := LoadStore(p.StateDir)
	if err != nil {
		return nil, err
	}

	// --- deterministic logic (outside the lock; gates may be slow) ------------
	var outcome LuaOutcome
	var gateMs int64
	if lp := p.LuaPath(cmd); lp != "" {
		gateStart := time.Now()
		outcome = runLua(lp, p.Dir, args, store, cmd.Next, pr, false, p.LuaTimeoutFor(cmd))
		gateMs = time.Since(gateStart).Milliseconds()
	}

	next := resolveSuccessor(cmd, &outcome)
	if outcome.Failed || outcome.ScriptErr != nil {
		return commitFailure(p, cmd.Name, cmd.Name, p.ThresholdFor(cmd), outcome)
	}
	return commitSuccess(p, cmd, next, store, true, seenDone, gateMs)
}

// resolveSuccessor decides the next cursor on (tentative) success, turning a
// missing gralph.route into a script error when the node branches.
func resolveSuccessor(cmd *CommandSpec, outcome *LuaOutcome) string {
	if outcome.Failed || outcome.ScriptErr != nil {
		return ""
	}
	switch len(cmd.Next) {
	case 0:
		return DoneCursor // last command
	case 1:
		return cmd.Next[0] // unconditional move
	default:
		if outcome.Route == "" {
			outcome.ScriptErr = fmt.Errorf(
				"lua finished without gralph.route() but %q has %d successor candidates %v",
				cmd.Name, len(cmd.Next), cmd.Next)
			return ""
		}
		return outcome.Route
	}
}

// commitFailure increments the failure counter under the state lock and
// builds the retry / end-session response. counterKey is the st.Failures key
// and the failure-memory label (subcommands use "name:key"); label is what
// the agent sees.
func commitFailure(p *Profile, label, counterKey string, threshold int, outcome LuaOutcome) (*CommandResult, error) {
	var res *CommandResult
	err := withStateLock(p.StateDir, func() error {
		st, err := LoadState(p.StateDir)
		if err != nil {
			return err
		}
		st.Failures[counterKey]++
		count := st.Failures[counterKey]
		if err := st.Save(p.StateDir); err != nil {
			return err
		}
		// Store is intentionally NOT committed on failure.

		// Persist the reason so the NEXT session's agent sees what went
		// wrong: st.Failures resets on rotation, failures.json does not.
		fr, err := LoadFailures(p.StateDir)
		if err != nil {
			return err
		}
		reason := outcome.FailReason
		if outcome.ScriptErr != nil {
			reason = outcome.ScriptErr.Error()
		}
		fr.Record(counterKey, reason, time.Now())
		if err := fr.Save(p.StateDir); err != nil {
			return err
		}

		jreason := reason
		if outcome.ScriptErr != nil {
			jreason = "script error: " + reason
		}
		appendJournal(p.StateDir, JournalEvent{
			Event:   EvCommandFailed,
			Session: st.SessionID,
			Command: label,
			Failure: count,
			Reason:  jreason,
		})

		end := count%threshold == 0
		var b strings.Builder
		if outcome.ScriptErr != nil {
			fmt.Fprintf(&b, "SCRIPT ERROR in `%s` (failure %d): %v\n", label, count, outcome.ScriptErr)
		} else {
			fmt.Fprintf(&b, "FAILED `%s` (failure %d): %s\n", label, count, outcome.FailReason)
		}
		if end {
			b.WriteString("Too many failures in this session. End the session now.")
		} else {
			b.WriteString("Fix the issue and run the command again in this session.")
		}
		res = &CommandResult{Message: b.String(), EndSession: end, ExitCode: 1}
		return nil
	})
	return res, err
}

// commitSuccess advances the cursor under the state lock. For a parent
// finalize (clearProgress) the write order is load-bearing: clear the
// progress file BEFORE advancing the cursor, so a crash in between leaves
// the cursor on the parent with empty progress -- the sub work is redone,
// but a stale quota can never carry over into a later revisit of the node.
//
// seenDone is meaningful only with clearProgress: the total number of work
// items the finalize's lua run got to see. If a straggler worker recorded
// more items while the lua ran outside the lock, clearing now would silently
// discard work that was acknowledged with "OK: recorded" -- so the commit is
// refused (no budget consumed) and the agent re-runs the finalize. Quotas
// are monotonic, so the re-run passes the quota check and the lua simply
// validates the now-complete progress.
// gateMs is the measured lua gate duration, journaled for post-hoc analysis.
func commitSuccess(p *Profile, cmd *CommandSpec, next string, store *Store, clearProgress bool, seenDone int, gateMs int64) (*CommandResult, error) {
	var res *CommandResult
	err := withStateLock(p.StateDir, func() error {
		st, err := LoadState(p.StateDir)
		if err != nil {
			return err
		}
		cur := st.Cursor
		if cur == "" {
			cur = p.FirstCommand().Name
		}
		if cur != cmd.Name {
			res = &CommandResult{
				Message: fmt.Sprintf(
					"`%s` is no longer the current command. The current command is `%s`. Run `gralph next` for instructions.",
					cmd.Name, st.Cursor),
				ExitCode: 1,
			}
			return nil
		}
		if clearProgress {
			pr, err := LoadProgress(p.StateDir, cmd.Name)
			if err != nil {
				return err
			}
			if got := pr.TotalDone(); got > seenDone {
				res = &CommandResult{
					Message: fmt.Sprintf(
						"`%s` was not committed: new work items were recorded while it ran (validated %d, progress now has %d). Run `%s` again so it validates the latest progress.",
						cmd.Name, seenDone, got, cmd.Name),
					ExitCode: 1,
				}
				return nil
			}
			if err := ClearProgress(p.StateDir); err != nil {
				return err
			}
		}
		// A success closes the node's failure memory: the recorded reasons
		// are advice for redoing THIS task, useless once it is done.
		fr, err := LoadFailures(p.StateDir)
		if err != nil {
			return err
		}
		cleared := fr.Clear(cmd.Name)
		if clearProgress {
			for i := range cmd.Subcommands {
				if fr.ClearPrefix(cmd.Subcommands[i].Name + ":") {
					cleared = true
				}
			}
		}
		if cleared {
			if err := fr.Save(p.StateDir); err != nil {
				return err
			}
		}
		if err := store.Commit(p.StateDir); err != nil {
			return err
		}
		st.Cursor = next
		if err := st.Save(p.StateDir); err != nil {
			return err
		}
		appendJournal(p.StateDir, JournalEvent{
			Event:   EvCommandSucceeded,
			Session: st.SessionID,
			Command: cmd.Name,
			Next:    next,
			GateMs:  gateMs,
		})

		msg := fmt.Sprintf("OK: `%s` succeeded.", cmd.Name)
		if next == DoneCursor {
			msg += " All work is complete."
		}
		msg += " End the session now."
		res = &CommandResult{Message: msg, EndSession: true}
		return nil
	})
	return res, err
}

func duplicateResult(parent *CommandSpec, sub *SubcommandSpec, key string, pr *Progress) *CommandResult {
	return &CommandResult{
		Message: fmt.Sprintf(
			"`%s` (%s) is already completed. Progress: %s. Pick a remaining work item.",
			sub.Name, key, pr.QuotaStatus(parent)),
		ExitCode: 1,
	}
}

// parseArgs accepts `--name value` (or `--name=value`) pairs and checks them
// against the YAML arg spec.
func parseArgs(cmdName string, specs []ArgSpec, argv []string) (map[string]string, error) {
	declared := map[string]*ArgSpec{}
	for i := range specs {
		declared[specs[i].Name] = &specs[i]
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
			return nil, fmt.Errorf("unknown argument --%s for command %q", key, cmdName)
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
