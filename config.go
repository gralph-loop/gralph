package main

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// DoneCursor is the sentinel cursor value meaning the graph is finished.
const DoneCursor = "DONE"

// DefaultFailThreshold: every n-th failure forces a session restart.
const DefaultFailThreshold = 5

// ArgSpec describes one named argument of a custom command,
// passed by the agent as `--<name> <value>`.
type ArgSpec struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
	Desc     string `yaml:"desc"`
}

// CommandSpec is one node of the graph.
type CommandSpec struct {
	Name string `yaml:"name"`
	// Guidance is the text returned by `gralph next` while the cursor is on
	// this node. Rendered with text/template; only {{store "key"}} (and
	// {{.Cursor}}) are available -- no lua is involved in rendering.
	Guidance string `yaml:"guidance"`
	// Args the agent must provide when invoking this command.
	Args []ArgSpec `yaml:"args"`
	// Lua is a path (relative to the profile file) to the validation /
	// routing script. Optional; without it the command always succeeds.
	Lua string `yaml:"lua"`
	// Next lists candidate successor command names.
	//   0  -> this is the last command; success sets cursor := DONE
	//   1  -> unconditional move
	//   >1 -> lua must call gralph.route("name")
	Next []string `yaml:"next"`
	// FailThreshold overrides the profile-level threshold for this command.
	FailThreshold int `yaml:"fail_threshold"`
}

// AgentSpec describes how to launch one non-interactive agent session.
type AgentSpec struct {
	// Command is argv; every element may contain the placeholder
	// {{prompt}}, replaced with the ralph prompt.
	Command []string `yaml:"command"`
}

// Profile is the user-supplied YAML profile.
type Profile struct {
	Agent         AgentSpec     `yaml:"agent"`
	Prompt        string        `yaml:"prompt"`
	StateDir      string        `yaml:"state_dir"`
	FailThreshold int           `yaml:"fail_threshold"`
	Commands      []CommandSpec `yaml:"commands"`

	// Dir is the directory containing the profile file (not from YAML).
	Dir string `yaml:"-"`
	// Path is the absolute path of the profile file (not from YAML).
	Path string `yaml:"-"`
}

// DefaultPrompt is used when the profile does not define one.
const DefaultPrompt = `You are running inside a gralph (ralph loop) session.
1. Run ` + "`gralph next`" + ` to receive your current task and the exact gralph command to run when the task is done.
2. Perform the task, then run the instructed gralph command with its arguments.
3. Whenever a gralph command's response tells you to end the session, end the session immediately.`

// LoadProfile reads, defaults and validates a profile.
func LoadProfile(path string) (*Profile, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read profile: %w", err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}
	p.Path = abs
	p.Dir = filepath.Dir(abs)
	if p.StateDir == "" {
		p.StateDir = ".gralph"
	}
	if !filepath.IsAbs(p.StateDir) {
		p.StateDir = filepath.Join(p.Dir, p.StateDir)
	}
	if p.FailThreshold <= 0 {
		p.FailThreshold = DefaultFailThreshold
	}
	if p.Prompt == "" {
		p.Prompt = DefaultPrompt
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) validate() error {
	if len(p.Commands) == 0 {
		return fmt.Errorf("profile: at least one command is required")
	}
	byName := map[string]*CommandSpec{}
	for i := range p.Commands {
		c := &p.Commands[i]
		if c.Name == "" {
			return fmt.Errorf("profile: command #%d has no name", i+1)
		}
		if c.Name == DoneCursor {
			return fmt.Errorf("profile: %q is a reserved command name", DoneCursor)
		}
		if _, dup := byName[c.Name]; dup {
			return fmt.Errorf("profile: duplicate command name %q", c.Name)
		}
		byName[c.Name] = c
	}
	for i := range p.Commands {
		c := &p.Commands[i]
		for _, n := range c.Next {
			if _, ok := byName[n]; !ok {
				return fmt.Errorf("profile: command %q lists unknown successor %q", c.Name, n)
			}
		}
		if len(c.Next) > 1 && c.Lua == "" {
			return fmt.Errorf("profile: command %q has multiple successors but no lua to route them", c.Name)
		}
	}
	return nil
}

// Command returns the spec for name, or nil.
func (p *Profile) Command(name string) *CommandSpec {
	for i := range p.Commands {
		if p.Commands[i].Name == name {
			return &p.Commands[i]
		}
	}
	return nil
}

// FirstCommand is the entry node of the graph.
func (p *Profile) FirstCommand() *CommandSpec { return &p.Commands[0] }

// ThresholdFor resolves the effective fail threshold for a command.
func (p *Profile) ThresholdFor(c *CommandSpec) int {
	if c.FailThreshold > 0 {
		return c.FailThreshold
	}
	return p.FailThreshold
}

// LuaPath resolves a command's lua script relative to the profile dir.
func (p *Profile) LuaPath(c *CommandSpec) string {
	if c.Lua == "" {
		return ""
	}
	if filepath.IsAbs(c.Lua) {
		return c.Lua
	}
	return filepath.Join(p.Dir, c.Lua)
}
