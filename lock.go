package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// withStateLock serializes read-modify-write access to the state dir's files
// (state.json, store.json, progress.json) across processes. Parallel
// sub-agents each run their own `gralph <subcommand>` process, so in-memory
// locking is not enough; flock works cross-platform via gofrs/flock.
//
// Hold it only around the commit phase, never around lua execution -- gates
// may run builds or test suites and must stay parallel.
func withStateLock(stateDir string, fn func() error) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	fl := flock.New(filepath.Join(stateDir, "lock"))
	if err := fl.Lock(); err != nil {
		return fmt.Errorf("acquire state lock: %w", err)
	}
	defer fl.Unlock()
	return fn()
}
