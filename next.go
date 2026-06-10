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
	body, err := renderGuidance(cmd, store)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Current task: %s\n\n", cmd.Name)
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n\n")
	b.WriteString("When the task is done, run the command above exactly once and follow its response. ")
	b.WriteString("If the response tells you to end the session, end it immediately.\n")
	return b.String(), nil
}

func renderGuidance(cmd *CommandSpec, store *Store) (string, error) {
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
