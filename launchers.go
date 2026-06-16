package main

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// launcherTemplates bundles the editable official GALP launchers so `gralph
// launchers init` can materialize them without any network access. They follow
// the exact same contract as the built-in default launcher -- "even the
// default is editable".
//
//go:embed launchers
var launcherTemplates embed.FS

// runLaunchers implements `gralph launchers <subcommand>`. Currently the only
// subcommand is `init`, which copies one or more launcher templates into
// .gralph/launchers/<name> (never overwriting without --force).
func runLaunchers(args []string) int {
	if len(args) == 0 || args[0] != "init" {
		fmt.Fprintln(os.Stderr, "usage: gralph launchers init [name ...] [--force]")
		fmt.Fprintln(os.Stderr, "       templates: subprocess, tmux, ratelimit (default: all)")
		return 2
	}

	force := false
	var names []string
	for _, a := range args[1:] {
		switch {
		case a == "--force":
			force = true
		case len(a) > 0 && a[0] == '-':
			fmt.Fprintf(os.Stderr, "launchers: unknown flag %q\n", a)
			return 2
		default:
			names = append(names, a)
		}
	}

	entries, err := launcherTemplates.ReadDir("launchers")
	if err != nil {
		fmt.Fprintf(os.Stderr, "launchers: %v\n", err)
		return 1
	}
	available := map[string]bool{}
	var allNames []string
	for _, e := range entries {
		available[e.Name()] = true
		allNames = append(allNames, e.Name())
	}
	sort.Strings(allNames)
	if len(names) == 0 {
		names = allNames
	}

	destDir := filepath.Join(".gralph", "launchers")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "launchers: %v\n", err)
		return 1
	}

	rc := 0
	for _, n := range names {
		if !available[n] {
			fmt.Fprintf(os.Stderr, "launchers: unknown template %q (available: %v)\n", n, allNames)
			rc = 1
			continue
		}
		data, err := launcherTemplates.ReadFile("launchers/" + n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "launchers: %v\n", err)
			rc = 1
			continue
		}
		dest := filepath.Join(destDir, n)
		if _, err := os.Stat(dest); err == nil && !force {
			fmt.Fprintf(os.Stderr, "launchers: %s already exists (use --force to overwrite)\n", dest)
			continue
		}
		if err := os.WriteFile(dest, data, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "launchers: %v\n", err)
			rc = 1
			continue
		}
		fmt.Printf("wrote %s\n", dest)
	}
	return rc
}
