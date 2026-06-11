package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Framework-internal state (NOT user-accessible territory):
//   cursor, session id, per-command failure counts.
// Failure counts are session-scoped: the orchestrator resets them whenever it
// rotates the session id at the start of each loop iteration.
// ---------------------------------------------------------------------------

type State struct {
	Cursor    string         `json:"cursor"`
	SessionID string         `json:"session_id"`
	Failures  map[string]int `json:"failures"`
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }
func storePath(dir string) string { return filepath.Join(dir, "store.json") }

func LoadState(dir string) (*State, error) {
	s := &State{Failures: map[string]int{}}
	data, err := os.ReadFile(statePath(dir))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	if s.Failures == nil {
		s.Failures = map[string]int{}
	}
	return s, nil
}

func (s *State) Save(dir string) error {
	return atomicWriteJSON(statePath(dir), s)
}

// ---------------------------------------------------------------------------
// User store (lua-only KV). The framework never touches the contents; lua
// writes values derived during deterministic logic, and `next` reads them
// when rendering guidance templates.
// ---------------------------------------------------------------------------

type Store struct {
	values    map[string]any
	dirtyKeys map[string]struct{}
}

func LoadStore(dir string) (*Store, error) {
	st := &Store{values: map[string]any{}, dirtyKeys: map[string]struct{}{}}
	data, err := os.ReadFile(storePath(dir))
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read store: %w", err)
	}
	if err := json.Unmarshal(data, &st.values); err != nil {
		return nil, fmt.Errorf("parse store: %w", err)
	}
	return st, nil
}

func (st *Store) Get(key string) (any, bool) {
	v, ok := st.values[key]
	return v, ok
}

func (st *Store) Set(key string, v any) {
	st.values[key] = v
	st.dirtyKeys[key] = struct{}{}
}

// Commit persists the store. Called only after a command succeeds, so a
// failed validation never leaves half-written values behind.
//
// Only the keys written by this run are merged into a fresh read of the
// file, so parallel subcommand gates (which see a snapshot from their own
// load) don't clobber each other's evidence -- conflicts narrow to the key
// level. Callers must hold the state lock.
func (st *Store) Commit(dir string) error {
	if len(st.dirtyKeys) == 0 {
		return nil
	}
	onDisk := map[string]any{}
	data, err := os.ReadFile(storePath(dir))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read store: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &onDisk); err != nil {
			return fmt.Errorf("parse store: %w", err)
		}
	}
	for k := range st.dirtyKeys {
		onDisk[k] = st.values[k]
	}
	if err := atomicWriteJSON(storePath(dir), onDisk); err != nil {
		return err
	}
	st.dirtyKeys = map[string]struct{}{}
	return nil
}

// ---------------------------------------------------------------------------

func atomicWriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
