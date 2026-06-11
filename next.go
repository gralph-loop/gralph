package main

import (
	"fmt"
	"strings"
	"text/template"
)

// resolveNext returns the current cursor, initializing it to the first
// command on a fresh run. The orchestrator calls this directly as a function
// at the top of every loop iteration (never via the CLI); the agent reaches
// the same logic through `gralph next`.
func resolveNext(p *Profile) (string, error) {
	st, err := LoadState(p.StateDir)
	if err != nil {
		return "", err
	}
	if st.Cursor == "" {
		st.Cursor = p.FirstCommand().Name
		if err := st.Save(p.StateDir); err != nil {
			return "", err
		}
	}
	return st.Cursor, nil
}

// renderNext produces the agent-facing guidance for the current cursor node.
// Pure rendering: only values from gralph.store are pulled in -- no lua runs.
func renderNext(p *Profile) (string, error) {
	cursor, err := resolveNext(p)
	if err != nil {
		return "", err
	}
	if cursor == DoneCursor {
		// The agent should never see this (the orchestrator stops looping
		// before launching a session), but degrade gracefully.
		return "All work is complete. End the session now.", nil
	}
	cmd := p.Command(cursor)
	if cmd == nil {
		return "", fmt.Errorf("state cursor %q does not match any command in the profile", cursor)
	}
	store, err := LoadStore(p.StateDir)
	if err != nil {
		return "", err
	}
	var prog *Progress
	if len(cmd.Subcommands) > 0 {
		prog, err = LoadProgress(p.StateDir, cmd.Name)
		if err != nil {
			return "", err
		}
	}
	body, err := renderGuidance(cmd, store, prog)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Current task: %s\n\n", cmd.Name)
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n")
	if prog != nil {
		// Always show live quota state, so a fresh session can resume
		// without the guidance author having to remember {{subprogress}}.
		b.WriteString("Subcommand progress:\n")
		b.WriteString(formatSubprogress(cmd, prog))
		b.WriteString("\n")
		b.WriteString("This task has subcommand quotas. Run each subcommand exactly once per distinct work item ")
		b.WriteString("(spawn parallel sub-agents for them if your environment supports it). ")
		b.WriteString("When every quota is met, run the command above exactly once and follow its response. ")
	} else {
		b.WriteString("When the task is done, run the command above exactly once and follow its response. ")
	}
	b.WriteString("If the response tells you to end the session, end it immediately.\n")
	return b.String(), nil
}

// formatSubprogress renders the multi-line quota view, e.g.
//
//	impl-feature: 3/5 done (auth, billing, search)
//	write-doc: 0/3 done
func formatSubprogress(cmd *CommandSpec, prog *Progress) string {
	var b strings.Builder
	for i := range cmd.Subcommands {
		s := &cmd.Subcommands[i]
		fmt.Fprintf(&b, "%s: %d/%d done", s.Name, prog.CountDone(s.Name), s.Count)
		if keys := prog.DoneKeys(s.Name); len(keys) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(keys, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderGuidance(cmd *CommandSpec, store *Store, prog *Progress) (string, error) {
	funcs := template.FuncMap{
		// {{store "key"}} -> value from the user store ("" if absent)
		"store": func(key string) any {
			v, ok := store.Get(key)
			if !ok {
				return ""
			}
			return v
		},
	}
	if prog != nil {
		// {{subprogress}} -> the same multi-line quota view shown after the
		// guidance; {{subdone "sub"}} / {{subcount "sub"}} for finer templates.
		funcs["subprogress"] = func() string { return strings.TrimRight(formatSubprogress(cmd, prog), "\n") }
		funcs["subdone"] = func(sub string) []string { return prog.DoneKeys(sub) }
		funcs["subcount"] = func(sub string) int { return prog.CountDone(sub) }
	}
	tpl, err := template.New(cmd.Name).Funcs(funcs).Parse(cmd.Guidance)
	if err != nil {
		return "", fmt.Errorf("guidance template of %q: %w", cmd.Name, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, map[string]any{"Cursor": cmd.Name}); err != nil {
		return "", fmt.Errorf("render guidance of %q: %w", cmd.Name, err)
	}
	return b.String(), nil
}
