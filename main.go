// gralph -- a ralph-loop orchestrator plus the in-session subcommands
// (`next` + YAML-defined custom commands) that the agent calls.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `gralph - ralph loop orchestrator

Usage:
  gralph run <profile.yaml> [--max-iterations N]   run the ralph loop (orchestrator)
  gralph next [--profile <profile.yaml>]           (agent) get current task guidance
  gralph <command> [--profile p] [--arg value ...] (agent) run a YAML-defined custom command

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

	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		maxIter := fs.Int("max-iterations", 0, "stop after N iterations (0 = unlimited)")
		args := os.Args[2:]
		// allow `gralph run profile.yaml --max-iterations N`
		var profilePath string
		if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
			profilePath = args[0]
			args = args[1:]
		}
		_ = fs.Parse(args)
		if profilePath == "" {
			fatal(fmt.Errorf("usage: gralph run <profile.yaml>"))
		}
		p, err := LoadProfile(profilePath)
		if err != nil {
			fatal(err)
		}
		if err := runLoop(p, *maxIter); err != nil {
			fatal(err)
		}

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

	default: // YAML-defined custom command
		name := os.Args[1]
		p, rest, err := profileFromSessionArgs(os.Args[2:])
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "gralph:", err)
	os.Exit(1)
}
