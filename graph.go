package main

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// `gralph graph <profile.yaml> [--state]` -- render the profile's command
// graph as a mermaid flowchart on stdout, so a profile can be reviewed (and a
// running loop located) without tracing the YAML by hand.
// ---------------------------------------------------------------------------

// renderGraph builds the mermaid flowchart. highlight, when non-empty, is the
// cursor to emphasize (a command name or DoneCursor, from --state).
func renderGraph(p *Profile, highlight string) string {
	// Stable generated ids: command names go into labels, not ids, so any
	// character the YAML allows is safe.
	ids := map[string]string{}
	for i := range p.Commands {
		ids[p.Commands[i].Name] = fmt.Sprintf("c%d", i)
	}

	var b strings.Builder
	b.WriteString("flowchart TD\n")

	// Nodes. A fork/join parent carries its subcommand quotas in the label.
	terminal := false
	for i := range p.Commands {
		c := &p.Commands[i]
		b.WriteString(fmt.Sprintf("    %s[%q]\n", ids[c.Name], nodeLabel(c)))
		if len(c.Next) == 0 {
			terminal = true
		}
	}
	if terminal {
		b.WriteString(fmt.Sprintf("    %s([%q])\n", DoneCursor, DoneCursor))
	}

	// Edges. Branching nodes (>=2 candidates) mark each edge as lua-routed.
	for i := range p.Commands {
		c := &p.Commands[i]
		if len(c.Next) == 0 {
			b.WriteString(fmt.Sprintf("    %s --> %s\n", ids[c.Name], DoneCursor))
			continue
		}
		for _, n := range c.Next {
			if len(c.Next) >= 2 {
				b.WriteString(fmt.Sprintf("    %s -->|route| %s\n", ids[c.Name], ids[n]))
			} else {
				b.WriteString(fmt.Sprintf("    %s --> %s\n", ids[c.Name], ids[n]))
			}
		}
	}

	// Cursor emphasis (--state).
	if highlight != "" {
		id := ids[highlight]
		if highlight == DoneCursor {
			id = DoneCursor
		}
		if id != "" {
			b.WriteString(fmt.Sprintf("    style %s fill:#ffd54f,stroke:#f57f17,stroke-width:3px\n", id))
		}
	}
	return b.String()
}

// nodeLabel renders a command's display label, e.g. `verify [check x3, lint]`
// for a fork/join parent.
func nodeLabel(c *CommandSpec) string {
	if len(c.Subcommands) == 0 {
		return c.Name
	}
	parts := make([]string, 0, len(c.Subcommands))
	for i := range c.Subcommands {
		s := &c.Subcommands[i]
		if s.Count > 1 {
			parts = append(parts, fmt.Sprintf("%s x%d", s.Name, s.Count))
		} else {
			parts = append(parts, s.Name)
		}
	}
	return fmt.Sprintf("%s [%s]", c.Name, strings.Join(parts, ", "))
}

// graphCursor reads the state dir's current cursor for --state. An untouched
// state means the loop would start at the first command.
func graphCursor(p *Profile) (string, error) {
	st, err := LoadState(p.StateDir)
	if err != nil {
		return "", err
	}
	if st.Cursor == "" {
		return p.FirstCommand().Name, nil
	}
	return st.Cursor, nil
}
