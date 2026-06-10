package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// runLoop is the orchestrator. Each iteration is a fresh session / fresh
// context: it rotates the session id (which resets the session-scoped
// failure counters), then launches the non-interactive agent command from
// the YAML profile with the ralph prompt.
//
// Termination: at the top of every iteration the loop calls resolveNext()
// directly (a function call, not the CLI); if the cursor is DONE it breaks.
// The agent never observes the loop's stop signal.
func runLoop(p *Profile, maxIterations int) error {
	if len(p.Agent.Command) == 0 {
		return fmt.Errorf("profile: agent.command is required to run the loop")
	}

	for i := 1; ; i++ {
		if maxIterations > 0 && i > maxIterations {
			fmt.Fprintf(os.Stderr, "[gralph] reached max iterations (%d); stopping\n", maxIterations)
			return nil
		}

		cursor, err := resolveNext(p)
		if err != nil {
			return err
		}
		if cursor == DoneCursor {
			fmt.Fprintf(os.Stderr, "[gralph] cursor is DONE; loop finished after %d iteration(s)\n", i-1)
			return nil
		}

		// New session: rotate id, reset per-command failure counters.
		st, err := LoadState(p.StateDir)
		if err != nil {
			return err
		}
		st.SessionID = newSessionID()
		st.Failures = map[string]int{}
		if err := st.Save(p.StateDir); err != nil {
			return err
		}

		fmt.Fprintf(os.Stderr, "[gralph] iteration %d | session %s | cursor %s\n", i, st.SessionID, cursor)

		if err := launchAgent(p); err != nil {
			// An agent process dying is not a graph failure; report and keep
			// looping (the cursor did not move, so the work will be retried
			// in a fresh session).
			fmt.Fprintf(os.Stderr, "[gralph] agent exited with error: %v\n", err)
		}
	}
}

func launchAgent(p *Profile) error {
	argv := make([]string, len(p.Agent.Command))
	for i, a := range p.Agent.Command {
		argv[i] = strings.ReplaceAll(a, "{{prompt}}", p.Prompt)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = p.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"GRALPH_PROFILE="+p.Path, // lets `gralph next` / custom commands find the profile
	)
	return cmd.Run()
}

func newSessionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}
