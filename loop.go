package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// MaxConsecutiveAgentFailures: the loop gives up after this many abnormal
// agent exits in a row without any cursor progress (a crash-looping agent
// would otherwise spin forever).
const MaxConsecutiveAgentFailures = 5

// Backoff between consecutive abnormal agent exits: 2s, 4s, 8s, ... capped.
// Vars (not consts) so tests can shrink them.
var (
	agentBackoffBase = 2 * time.Second
	agentBackoffMax  = 30 * time.Second
)

// agentKillGrace is how long a cancelled agent process (signal or timeout)
// gets to exit after SIGTERM before it is hard-killed.
const agentKillGrace = 5 * time.Second

// runLoop is the orchestrator. Each iteration is a fresh session / fresh
// context: it rotates the session id (which resets the session-scoped
// failure counters), then launches the non-interactive agent command from
// the YAML profile with the ralph prompt.
//
// Termination: at the top of every iteration the loop calls resolveNext()
// directly (a function call, not the CLI); if the cursor is DONE it breaks.
// The agent never observes the loop's stop signal.
//
// The loop also stops with an error when:
//   - ctx is cancelled (SIGINT/SIGTERM); the cursor is preserved on disk,
//   - the agent binary cannot be started at all (retrying is pointless),
//   - the agent exits abnormally MaxConsecutiveAgentFailures times in a row
//     without the cursor moving (with exponential backoff between retries).
func runLoop(ctx context.Context, p *Profile, maxIterations int) error {
	if len(p.Agent.Command) == 0 {
		return fmt.Errorf("profile: agent.command is required to run the loop")
	}

	consecutiveFailures := 0
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
			appendJournal(p.StateDir, JournalEvent{Event: EvLoopDone, Iteration: i - 1})
			return nil
		}
		if ctx.Err() != nil {
			return interrupted(i, cursor)
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
		appendJournal(p.StateDir, JournalEvent{
			Event:     EvSessionStart,
			Session:   st.SessionID,
			Cursor:    cursor,
			Iteration: i,
		})

		// Per-node overrides: the cursor's command may carry its own agent
		// command and/or ralph prompt; otherwise the profile-level ones apply.
		node := p.Command(cursor)
		agentErr := launchAgent(ctx, p, p.AgentCommandFor(node), p.PromptFor(node))
		if ctx.Err() != nil {
			return interrupted(i, cursor)
		}
		if agentErr != nil {
			// Launching the binary itself is impossible: retrying cannot help.
			if errors.Is(agentErr, exec.ErrNotFound) || errors.Is(agentErr, fs.ErrNotExist) {
				return fmt.Errorf("agent command %q cannot be started: %w", p.AgentCommandFor(node)[0], agentErr)
			}
			// Otherwise an agent process dying is not a graph failure; report
			// and keep looping (the cursor did not move, so the work will be
			// retried in a fresh session).
			fmt.Fprintf(os.Stderr, "[gralph] agent exited with error: %v\n", agentErr)
		}

		after, err := resolveNext(p)
		if err != nil {
			return err
		}
		if after != cursor {
			// Cursor progressed; the agent is alive enough.
			consecutiveFailures = 0
		} else if agentErr != nil {
			consecutiveFailures++
			if consecutiveFailures >= MaxConsecutiveAgentFailures {
				return fmt.Errorf("agent exited abnormally %d times in a row without cursor progress (cursor: %s); giving up",
					consecutiveFailures, cursor)
			}
			// Exponential backoff before the next attempt.
			d := agentBackoff(consecutiveFailures)
			fmt.Fprintf(os.Stderr, "[gralph] backing off %s before retry (%d/%d consecutive abnormal exits)\n",
				d, consecutiveFailures, MaxConsecutiveAgentFailures)
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return interrupted(i, cursor)
			}
		}
	}
}

// interrupted reports a SIGINT/SIGTERM stop. The cursor is already preserved
// on disk, so a plain `gralph run` resumes where the loop left off.
func interrupted(iteration int, cursor string) error {
	fmt.Fprintf(os.Stderr, "[gralph] interrupted at iteration %d (cursor: %s)\n", iteration, cursor)
	fmt.Fprintf(os.Stderr, "[gralph] state is preserved; rerun `gralph run` to resume from this cursor\n")
	return fmt.Errorf("interrupted")
}

// agentBackoff returns the wait before retry n (1-based): 2s, 4s, 8s, ...
// capped at agentBackoffMax.
func agentBackoff(n int) time.Duration {
	d := agentBackoffBase
	for ; n > 1 && d < agentBackoffMax; n-- {
		d *= 2
	}
	if d > agentBackoffMax {
		d = agentBackoffMax
	}
	return d
}

// launchAgent runs one agent session with the given argv (each element may
// contain {{prompt}}) and ralph prompt. ctx cancellation (signal) and the
// optional agent.timeout both terminate the process: SIGTERM first, then a
// hard kill after agentKillGrace.
func launchAgent(ctx context.Context, p *Profile, command []string, prompt string) error {
	if t := p.Agent.timeout; t > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}
	argv := make([]string, len(command))
	for i, a := range command {
		argv[i] = strings.ReplaceAll(a, "{{prompt}}", prompt)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = p.Dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"GRALPH_PROFILE="+p.Path, // lets `gralph next` / custom commands find the profile
	)
	cmd.Cancel = func() error {
		// Graceful first; WaitDelay hard-kills if the process lingers.
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = agentKillGrace
	err := cmd.Run()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("agent timed out after %s: %w", p.Agent.timeout, err)
	}
	return err
}

func newSessionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}
