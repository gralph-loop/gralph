// gralph -- a ralph-loop orchestrator plus the in-session subcommands
// (`next` + YAML-defined custom commands) that the agent calls.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

const usage = `gralph - ralph loop orchestrator

Usage:
  gralph run <profile.yaml> [--max-iterations N]    run the ralph loop (orchestrator)
  gralph next [--profile <profile.yaml>]            (agent) get current task guidance
  gralph do <command> [--profile p] [--arg v ...]   (agent) run a YAML-defined custom command
  gralph status [--profile p] [--json]              show cursor, session, failures, quota progress
  gralph reset [--profile p] [--force] [--failures] reset the state dir (--failures: counters only)
  gralph validate <profile.yaml>                    lint a profile without running anything
  gralph try <command> [--profile p] [--arg v ...]  dry-run a gate: no cursor check, nothing committed
  gralph version                                    print version (from Go build info)

Inside an agent session the profile path is taken from $GRALPH_PROFILE
(set automatically by the orchestrator) unless --profile is given.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "help", "-h", "--help":
		fmt.Print(usage)

	case "version", "--version":
		fmt.Println(versionString())

	case "run":
		profilePath, maxIter, err := parseRunArgs(os.Args[2:])
		if err != nil {
			fatal(err)
		}
		p, err := LoadProfile(profilePath)
		if err != nil {
			fatal(err)
		}
		// SIGINT/SIGTERM cancel the context; the loop forwards the signal to
		// the running agent, reports the preserved cursor and exits.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := runLoop(ctx, p, maxIter); err != nil {
			fatal(err)
		}

	case "graph":
		fs := flag.NewFlagSet("graph", flag.ExitOnError)
		withState := fs.Bool("state", false, "highlight the current cursor from the state dir")
		args := os.Args[2:]
		// allow `gralph graph profile.yaml --state`
		var profilePath string
		if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
			profilePath = args[0]
			args = args[1:]
		}
		_ = fs.Parse(args)
		if profilePath == "" && fs.NArg() > 0 {
			profilePath = fs.Arg(0)
		}
		if profilePath == "" {
			fatal(fmt.Errorf("usage: gralph graph <profile.yaml> [--state]"))
		}
		p, err := LoadProfile(profilePath)
		if err != nil {
			fatal(err)
		}
		highlight := ""
		if *withState {
			if highlight, err = graphCursor(p); err != nil {
				fatal(err)
			}
		}
		fmt.Print(renderGraph(p, highlight))

	case "next":
		p, rest, err := profileFromSessionArgs(os.Args[2:])
		if err != nil {
			fatal(err)
		}
		if len(rest) > 0 {
			fatal(fmt.Errorf("next takes no arguments"))
		}
		out, err := renderNext(p)
		if err != nil {
			fatal(err)
		}
		fmt.Print(out)

	case "status":
		p, rest, err := profileFromSessionArgs(os.Args[2:])
		if err != nil {
			fatal(err)
		}
		asJSON := false
		for _, a := range rest {
			if a != "--json" {
				fatal(fmt.Errorf("unknown flag %q for status", a))
			}
			asJSON = true
		}
		if err := printStatus(p, asJSON); err != nil {
			fatal(err)
		}

	case "reset":
		p, rest, err := profileFromSessionArgs(os.Args[2:])
		if err != nil {
			fatal(err)
		}
		force, failuresOnly := false, false
		for _, a := range rest {
			switch a {
			case "--force":
				force = true
			case "--failures":
				failuresOnly = true
			default:
				fatal(fmt.Errorf("unknown flag %q for reset", a))
			}
		}
		what := "state.json, store.json and progress.json"
		if failuresOnly {
			what = "the failure counters"
		}
		if !force {
			ok, err := confirmReset(fmt.Sprintf("Reset %s in %s?", what, p.StateDir))
			if err != nil {
				fatal(err)
			}
			if !ok {
				fmt.Fprintln(os.Stderr, "gralph: reset aborted")
				os.Exit(1)
			}
		}
		if err := resetStateDir(p.StateDir, failuresOnly); err != nil {
			fatal(err)
		}
		fmt.Printf("reset %s in %s\n", what, p.StateDir)

	case "validate":
		if len(os.Args) != 3 {
			fatal(fmt.Errorf("usage: gralph validate <profile.yaml>"))
		}
		os.Exit(runValidate(os.Args[2]))

	case "try":
		if len(os.Args) < 3 || os.Args[2] == "" || os.Args[2][0] == '-' {
			fatal(fmt.Errorf("usage: gralph try <command> [--profile p] [--arg value ...]"))
		}
		name := os.Args[2]
		p, rest, err := profileFromSessionArgs(os.Args[3:])
		if err != nil {
			fatal(err)
		}
		code, err := runTry(p, name, rest, os.Stdout)
		if err != nil {
			fatal(err)
		}
		os.Exit(code)

	case "do":
		if len(os.Args) < 3 || os.Args[2] == "" || os.Args[2][0] == '-' {
			fatal(fmt.Errorf("usage: gralph do <command> [--profile p] [--arg value ...]"))
		}
		runDo(os.Args[2], os.Args[3:])

	default:
		fatal(fmt.Errorf("unknown command %q (custom commands run as `gralph do %s`)",
			os.Args[1], os.Args[1]))
	}
}

// runDo dispatches one YAML-defined custom command and exits the process.
func runDo(name string, args []string) {
	p, rest, err := profileFromSessionArgs(args)
	if err != nil {
		fatal(err)
	}
	res, err := runCustomCommand(p, name, rest)
	if err != nil {
		fatal(err)
	}
	fmt.Println(res.Message)
	os.Exit(res.ExitCode)
}

// parseRunArgs parses the arguments of `gralph run`. The profile path may
// come before or after the flags:
//
//	gralph run profile.yaml --max-iterations N
//	gralph run --max-iterations N profile.yaml
func parseRunArgs(args []string) (profilePath string, maxIterations int, err error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	maxIter := fs.Int("max-iterations", 0, "stop after N iterations (0 = unlimited)")
	// path-first form
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		profilePath = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", 0, err
	}
	// flags-first form: recover the positional argument left over after
	// flag parsing.
	rest := fs.Args()
	if profilePath == "" && len(rest) > 0 {
		profilePath, rest = rest[0], rest[1:]
	}
	if profilePath == "" || len(rest) > 0 {
		return "", 0, fmt.Errorf("usage: gralph run <profile.yaml> [--max-iterations N]")
	}
	return profilePath, *maxIter, nil
}

// profileFromSessionArgs extracts an optional leading/inline --profile flag,
// falling back to $GRALPH_PROFILE.
func profileFromSessionArgs(args []string) (*Profile, []string, error) {
	path := os.Getenv("GRALPH_PROFILE")
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--profile":
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("missing value for --profile")
			}
			i++
			path = args[i]
		case len(args[i]) > 10 && args[i][:10] == "--profile=":
			path = args[i][10:]
		default:
			rest = append(rest, args[i])
		}
	}
	if path == "" {
		return nil, nil, fmt.Errorf("no profile: set $GRALPH_PROFILE or pass --profile <profile.yaml>")
	}
	p, err := LoadProfile(path)
	if err != nil {
		return nil, nil, err
	}
	return p, rest, nil
}

// version is injected at build time via -ldflags "-X main.version=<tag>"
// (see build.sh / build.ps1). Empty when built without it.
var version string

// versionString reports the injected version, falling back to the module
// version stamped by the Go toolchain (Go 1.24+ derives it from the
// checked-out VCS tag), plus the commit the binary was built from.
func versionString() string {
	v := version
	info, ok := debug.ReadBuildInfo()
	if !ok {
		if v == "" {
			return "gralph (no build info)"
		}
		return "gralph " + v
	}
	if v == "" {
		v = info.Main.Version
	}
	out := "gralph " + v
	var rev, at string
	dirty := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
		out += " (" + rev
		if at != "" {
			out += ", " + at
		}
		if dirty {
			out += ", dirty"
		}
		out += ")"
	}
	return out
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gralph:", err)
	os.Exit(1)
}
