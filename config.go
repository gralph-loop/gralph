package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// SubcommandSpec is one quota item of a parent command. While the cursor is
// on the parent, the agent (or its parallel sub-agents) must succeed this
// subcommand once per distinct work-item key, Count times in total, before
// the parent command itself becomes runnable.
type SubcommandSpec struct {
	Name string `yaml:"name"`
	// Count is the quota: how many distinct keys must succeed (default 1).
	Count int `yaml:"count"`
	// Key names the arg that identifies the work item. Required when
	// Count > 1; the named arg is forced to required. Without a key
	// (Count == 1) the subcommand name itself is the single item key.
	Key string `yaml:"key"`
	// Args the agent must provide when invoking this subcommand.
	Args []ArgSpec `yaml:"args"`
	// Lua is the per-item validation script (relative to the profile file).
	// Optional; without it any invocation with a fresh key succeeds.
	// gralph.route is not available in subcommand scripts.
	Lua string `yaml:"lua"`
	// FailThreshold overrides the parent's effective threshold. Failures are
	// counted per (subcommand, key) so one stuck worker does not recycle the
	// others.
	FailThreshold int `yaml:"fail_threshold"`
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
	// LuaTimeout is a Go duration string (e.g. "10s") limiting how long the
	// command's lua script may run. Overrides the profile-level lua_timeout.
	// Empty means "inherit"; no setting anywhere means no timeout.
	LuaTimeout string `yaml:"lua_timeout"`
	// luaTimeout is LuaTimeout parsed at load time (not from YAML).
	luaTimeout time.Duration
	// Agent optionally overrides the profile-level agent for sessions whose
	// cursor is this node (e.g. a cheaper model for verification steps).
	Agent *AgentSpec `yaml:"agent"`
	// Prompt optionally overrides the profile-level ralph prompt for
	// sessions whose cursor is this node.
	Prompt string `yaml:"prompt"`
	// Subcommands turn this node into a fork/join: every subcommand quota
	// must be met before this command itself may run (as the finalize gate).
	Subcommands []SubcommandSpec `yaml:"subcommands"`
}

// AgentSpec describes how to launch one non-interactive agent session.
type AgentSpec struct {
	// Command is argv; every element may contain the placeholder
	// {{prompt}}, replaced with the ralph prompt.
	Command []string `yaml:"command"`
	// Timeout is a Go duration string (e.g. "30m") limiting one agent
	// session. On expiry the process is killed and the iteration counts as
	// an abnormal exit (the cursor is kept, so the work is retried).
	// Empty means no timeout.
	Timeout string `yaml:"timeout"`

	// timeout is Timeout parsed at load time (not from YAML).
	timeout time.Duration
}

// Profile is the user-supplied YAML profile.
type Profile struct {
	Agent         AgentSpec     `yaml:"agent"`
	Prompt        string        `yaml:"prompt"`
	StateDir      string        `yaml:"state_dir"`
	FailThreshold int           `yaml:"fail_threshold"`
	Commands      []CommandSpec `yaml:"commands"`
	// LuaTimeout is the profile-level default lua script timeout
	// (Go duration string); commands may override it with their own
	// lua_timeout. Empty means no timeout.
	LuaTimeout string `yaml:"lua_timeout"`

	// Name is the instance name: which flow's state this process operates
	// on. The default state dir is keyed by it (".gralph/<name>"), so one
	// profile definition can drive several isolated flows. Resolved at load
	// time from --name / $GRALPH_INSTANCE_NAME, defaulting to the profile
	// filename without its extension -- never from the YAML itself.
	Name string `yaml:"-"`
	// Dir is the directory containing the profile file (not from YAML).
	Dir string `yaml:"-"`
	// Path is the absolute path of the profile file (not from YAML).
	Path string `yaml:"-"`
	// luaTimeout is LuaTimeout parsed at load time (not from YAML).
	luaTimeout time.Duration
}

// DefaultPrompt is used when the profile does not define one.
const DefaultPrompt = `You are running inside a gralph (ralph loop) session.
1. Run ` + "`gralph next`" + ` to receive the gralph command you must eventually run, along with any context about it.
2. Your job is to do whatever is necessary to be able to run that gralph command — figure out and carry out the work that running it requires. Once you've done that, run the instructed gralph command with its arguments.
3. Whenever a gralph command's response tells you to end the session, end the session immediately.`

// LoadProfile reads, defaults and validates a profile under the default
// instance name (the profile filename without its extension).
func LoadProfile(path string) (*Profile, error) { return LoadProfileAs(path, "") }

// LoadProfileAs is LoadProfile for an explicit instance name; empty means
// the default.
func LoadProfileAs(path, instance string) (*Profile, error) {
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
	p.Name = instance
	if p.Name == "" {
		base := filepath.Base(abs)
		p.Name = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if err := validateInstanceName(p.Name); err != nil {
		return nil, err
	}
	defaultedStateDir := p.StateDir == ""
	if defaultedStateDir {
		p.StateDir = filepath.Join(".gralph", p.Name)
	}
	if !filepath.IsAbs(p.StateDir) {
		p.StateDir = filepath.Join(p.Dir, p.StateDir)
	}
	if defaultedStateDir {
		if err := checkLegacyStateDir(&p); err != nil {
			return nil, err
		}
	}
	if p.FailThreshold <= 0 {
		p.FailThreshold = DefaultFailThreshold
	}
	if p.Prompt == "" {
		p.Prompt = DefaultPrompt
	}
	if p.Agent.timeout, err = parseTimeout("agent.timeout", p.Agent.Timeout); err != nil {
		return nil, err
	}
	if p.luaTimeout, err = parseTimeout("lua_timeout", p.LuaTimeout); err != nil {
		return nil, err
	}
	for i := range p.Commands {
		c := &p.Commands[i]
		field := fmt.Sprintf("command %q: lua_timeout", c.Name)
		if c.luaTimeout, err = parseTimeout(field, c.LuaTimeout); err != nil {
			return nil, err
		}
	}
	if err := p.validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// validateInstanceName guards the instance name's use as a state-dir path
// component: it must stay a single, non-special component. Derived names
// (filename stems) normally pass; an odd one is reported with the fix.
func validateInstanceName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("instance name %q is not usable as a directory name; pass a valid --name", name)
	}
	return nil
}

// checkLegacyStateDir refuses to run when a flow's state still lives in the
// pre-name default ".gralph-state": silently starting an empty ".gralph/<name>"
// would restart the graph from its entry node. Only consulted when state_dir
// was defaulted -- an explicit state_dir is always authoritative -- and only
// until state exists at the new location.
func checkLegacyStateDir(p *Profile) error {
	legacy := filepath.Join(p.Dir, ".gralph-state")
	if _, err := os.Stat(statePath(legacy)); err != nil {
		return nil // no legacy state to lose
	}
	if _, err := os.Stat(statePath(p.StateDir)); err == nil {
		return nil // already migrated; the leftover legacy dir is inert
	}
	return fmt.Errorf(`profile: found legacy state in %s while the default state dir is now %s
migrate it:    mv %s %s
or keep it:    set "state_dir: .gralph-state" in the profile
or discard it: rm -rf %s`,
		legacy, p.StateDir, legacy, p.StateDir, legacy)
}

// parseTimeout parses an optional Go duration string from the profile.
// Empty means "no timeout" (zero duration).
func parseTimeout(field, s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("profile: %s: %w", field, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("profile: %s must be positive, got %q", field, s)
	}
	return d, nil
}

// reservedCommandNames are the names custom commands (and subcommands -- they
// share the CLI namespace) may not use. Since custom commands are invoked as
// `gralph do <name>`, built-in words no longer collide and future built-ins
// can be added freely; only `do` itself stays reserved so the namespacing
// word is never ambiguous.
var reservedCommandNames = map[string]bool{
	"do": true,
}

// reservedArgNames are arg names the CLI consumes for itself before a custom
// command ever sees them (profileFromSessionArgs strips --profile / --name),
// so declaring them would silently swallow the agent's value.
var reservedArgNames = map[string]bool{
	"profile": true,
	"name":    true,
}

func validateArgSpecs(owner string, args []ArgSpec) error {
	for _, a := range args {
		if reservedArgNames[a.Name] {
			return fmt.Errorf("profile: %s: arg %q is reserved (consumed by the gralph CLI itself)", owner, a.Name)
		}
	}
	return nil
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
		if reservedCommandNames[c.Name] {
			return fmt.Errorf("profile: %q is a reserved command name (the `gralph do` namespacing word)", c.Name)
		}
		if _, dup := byName[c.Name]; dup {
			return fmt.Errorf("profile: duplicate command name %q", c.Name)
		}
		if err := validateArgSpecs(fmt.Sprintf("command %q", c.Name), c.Args); err != nil {
			return err
		}
		byName[c.Name] = c
	}
	// Subcommand names share the CLI namespace with commands, so they must be
	// globally unique across both.
	seenSub := map[string]string{} // sub name -> parent name
	for i := range p.Commands {
		c := &p.Commands[i]
		for j := range c.Subcommands {
			s := &c.Subcommands[j]
			if s.Name == "" {
				return fmt.Errorf("profile: command %q subcommand #%d has no name", c.Name, j+1)
			}
			if s.Name == DoneCursor {
				return fmt.Errorf("profile: %q is a reserved command name", DoneCursor)
			}
			if reservedCommandNames[s.Name] {
				return fmt.Errorf("profile: %q is a reserved command name (the `gralph do` namespacing word)", s.Name)
			}
			if _, clash := byName[s.Name]; clash {
				return fmt.Errorf("profile: subcommand %q of %q clashes with a command name", s.Name, c.Name)
			}
			if parent, dup := seenSub[s.Name]; dup {
				return fmt.Errorf("profile: duplicate subcommand name %q (in %q and %q)", s.Name, parent, c.Name)
			}
			seenSub[s.Name] = c.Name
			if err := validateArgSpecs(fmt.Sprintf("subcommand %q of %q", s.Name, c.Name), s.Args); err != nil {
				return err
			}
			if s.Count <= 0 {
				s.Count = 1
			}
			if s.Count > 1 && s.Key == "" {
				return fmt.Errorf("profile: subcommand %q of %q has count %d but no key to distinguish work items", s.Name, c.Name, s.Count)
			}
			if s.Key != "" {
				found := false
				for k := range s.Args {
					if s.Args[k].Name == s.Key {
						s.Args[k].Required = true // the key always identifies the item
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("profile: subcommand %q of %q: key %q is not a declared arg", s.Name, c.Name, s.Key)
				}
			}
		}
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
		if c.Agent != nil && len(c.Agent.Command) == 0 {
			return fmt.Errorf("profile: command %q declares an agent override with an empty command", c.Name)
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

// Subcommand returns the spec for name plus its parent command, or nils.
func (p *Profile) Subcommand(name string) (*SubcommandSpec, *CommandSpec) {
	for i := range p.Commands {
		c := &p.Commands[i]
		for j := range c.Subcommands {
			if c.Subcommands[j].Name == name {
				return &c.Subcommands[j], c
			}
		}
	}
	return nil, nil
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

// ThresholdForSub resolves the effective fail threshold for a subcommand:
// its own override, else the parent's effective threshold.
func (p *Profile) ThresholdForSub(parent *CommandSpec, s *SubcommandSpec) int {
	if s.FailThreshold > 0 {
		return s.FailThreshold
	}
	return p.ThresholdFor(parent)
}

// LuaTimeoutFor resolves the effective lua timeout for a command:
// the command-level lua_timeout, falling back to the profile-level default.
// Zero means no timeout.
func (p *Profile) LuaTimeoutFor(c *CommandSpec) time.Duration {
	if c.luaTimeout > 0 {
		return c.luaTimeout
	}
	return p.luaTimeout
}

// AgentCommandFor resolves the effective agent command for a node: the
// node's override when present, otherwise the profile-level command.
func (p *Profile) AgentCommandFor(c *CommandSpec) []string {
	if c != nil && c.Agent != nil && len(c.Agent.Command) > 0 {
		return c.Agent.Command
	}
	return p.Agent.Command
}

// PromptFor resolves the effective ralph prompt for a node: the node's
// override when present, otherwise the profile-level prompt.
func (p *Profile) PromptFor(c *CommandSpec) string {
	if c != nil && c.Prompt != "" {
		return c.Prompt
	}
	return p.Prompt
}

// LuaPath resolves a command's lua script relative to the profile dir.
func (p *Profile) LuaPath(c *CommandSpec) string { return p.resolvePath(c.Lua) }

// SubLuaPath resolves a subcommand's lua script relative to the profile dir.
func (p *Profile) SubLuaPath(s *SubcommandSpec) string { return p.resolvePath(s.Lua) }

func (p *Profile) resolvePath(rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(p.Dir, rel)
}
