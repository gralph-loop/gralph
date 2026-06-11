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
	body, usedUsage, err := renderGuidance(cmd, store, prog)
	if err != nil {
		return "", err
	}
	fail, err := LoadFailures(p.StateDir)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Current task: %s\n\n", cmd.Name)
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n")
	if !usedUsage {
		// The guidance did not place {{usage}} itself, so append the
		// generated usage block here -- every node always shows the exact
		// invocation derived from its arg spec, with no hand-written drift.
		b.WriteString(formatUsage(cmd))
		b.WriteString("\n\n")
	}
	if block := formatFailureMemory(cmd, fail); block != "" {
		// Failures recorded on this node by earlier sessions: without this a
		// fresh session would re-attempt blind and repeat the same mistakes.
		b.WriteString(block)
		b.WriteString("\n")
	}
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

// formatFailureMemory renders the persisted failures of every label that
// belongs to cmd: the command's own name plus each subcommand's "name:key"
// labels (in spec order, keys sorted). Subcommand entries carry their label
// so the agent can tell which work item failed. Empty string when the node
// has no recorded failures.
func formatFailureMemory(cmd *CommandSpec, fail Failures) string {
	type entry struct {
		label string // "" for the node's own records
		rec   FailureRecord
	}
	var entries []entry
	for _, r := range fail[cmd.Name] {
		entries = append(entries, entry{"", r})
	}
	for i := range cmd.Subcommands {
		for _, label := range fail.LabelsWithPrefix(cmd.Subcommands[i].Name + ":") {
			for _, r := range fail[label] {
				entries = append(entries, entry{label, r})
			}
		}
	}
	if len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Previous attempts on this task failed:\n")
	for _, e := range entries {
		if e.label == "" {
			fmt.Fprintf(&b, "- (failure %d) %s\n", e.rec.Failure, e.rec.Reason)
		} else {
			fmt.Fprintf(&b, "- (failure %d) [%s] %s\n", e.rec.Failure, e.label, e.rec.Reason)
		}
	}
	b.WriteString("Avoid repeating the same mistakes.\n")
	return b.String()
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

// formatUsage builds the usage block for one node from its arg specs, so the
// invocation line is generated rather than hand-written into the guidance
// (which would drift from the spec). For a fork/join node it covers each
// subcommand -- same format plus a one-line quota note -- before the parent
// finalize command.
func formatUsage(cmd *CommandSpec) string {
	var b strings.Builder
	for i := range cmd.Subcommands {
		s := &cmd.Subcommands[i]
		b.WriteString("Subcommand to run per work item:\n")
		fmt.Fprintf(&b, "  %s\n", invocationLine(s.Name, s.Args))
		fmt.Fprintf(&b, "  (%s)\n", quotaNote(s))
		b.WriteString(argumentsBlock(s.Args))
		b.WriteString("\n")
	}
	if len(cmd.Subcommands) > 0 {
		b.WriteString("Command to run when every quota is met:\n")
	} else {
		b.WriteString("Command to run when done:\n")
	}
	fmt.Fprintf(&b, "  %s\n", invocationLine(cmd.Name, cmd.Args))
	b.WriteString(argumentsBlock(cmd.Args))
	return strings.TrimRight(b.String(), "\n")
}

// invocationLine is the exact `gralph <name> ...` line the agent must run:
// required args as `--name <value>`, optional ones bracketed.
func invocationLine(name string, args []ArgSpec) string {
	parts := []string{"gralph", name}
	for i := range args {
		a := &args[i]
		if a.Required {
			parts = append(parts, fmt.Sprintf("--%s <value>", a.Name))
		} else {
			parts = append(parts, fmt.Sprintf("[--%s <value>]", a.Name))
		}
	}
	return strings.Join(parts, " ")
}

// argumentsBlock renders the aligned "Arguments:" section ("" when the
// command takes none). Desc is shown only when the spec provides one.
func argumentsBlock(args []ArgSpec) string {
	if len(args) == 0 {
		return ""
	}
	width := 0
	for i := range args {
		if n := len(args[i].Name); n > width {
			width = n
		}
	}
	var b strings.Builder
	b.WriteString("\nArguments:\n")
	for i := range args {
		a := &args[i]
		req := "(optional)"
		if a.Required {
			req = "(required)"
		}
		fmt.Fprintf(&b, "  --%-*s  %s", width, a.Name, req)
		if a.Desc != "" {
			b.WriteString("  " + a.Desc)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// quotaNote is the one-line count/key summary attached to a subcommand's
// usage, e.g. "run once per distinct --part, 3 items total".
func quotaNote(s *SubcommandSpec) string {
	if s.Key == "" {
		return "run once"
	}
	items := "items"
	if s.Count == 1 {
		items = "item"
	}
	return fmt.Sprintf("run once per distinct --%s, %d %s total", s.Key, s.Count, items)
}

// renderGuidance executes the guidance template. usedUsage reports whether
// the template called {{usage}}, so renderNext knows to skip the automatic
// append (tracked as a flag rather than by comparing rendered output).
func renderGuidance(cmd *CommandSpec, store *Store, prog *Progress) (body string, usedUsage bool, err error) {
	funcs := template.FuncMap{
		// {{store "key"}} -> value from the user store ("" if absent)
		"store": func(key string) any {
			v, ok := store.Get(key)
			if !ok {
				return ""
			}
			return v
		},
		// {{usage}} -> the generated usage block, letting the author choose
		// where it appears instead of getting it appended after the guidance.
		"usage": func() string {
			usedUsage = true
			return formatUsage(cmd)
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
		return "", false, fmt.Errorf("guidance template of %q: %w", cmd.Name, err)
	}
	var b strings.Builder
	if err := tpl.Execute(&b, map[string]any{"Cursor": cmd.Name}); err != nil {
		return "", false, fmt.Errorf("render guidance of %q: %w", cmd.Name, err)
	}
	return b.String(), usedUsage, nil
}
