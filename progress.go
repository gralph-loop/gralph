package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Subcommand progress (framework-internal, like state.json).
//
// Lives in its own file because its lifecycle differs from everything else in
// state.json: failure counters reset per session and the cursor moves per
// node, but progress persists across sessions and resets only when its parent
// command succeeds. Keeping it separate means no state.json writer can clobber
// it by accident.
//
// Crash consistency relies on write ordering, not multi-file atomicity:
// on parent success the progress file is cleared FIRST, then the cursor
// advances. A crash in between leaves the cursor on the parent with empty
// progress -- conservative (the sub work is redone) but never lets a stale
// quota carry over into a later revisit of the node.
// ---------------------------------------------------------------------------

// DoneMeta records when/where one work item was verified.
type DoneMeta struct {
	At      string `json:"at"`
	Session string `json:"session"`
}

// Progress is the on-disk progress.json. Command names the parent node the
// progress belongs to; a file whose Command does not match the current cursor
// is stale and treated as empty.
type Progress struct {
	Command string                         `json:"command"`
	Done    map[string]map[string]DoneMeta `json:"done"`
}

func progressPath(dir string) string { return filepath.Join(dir, "progress.json") }

// LoadProgress reads progress for the given parent command. Missing file or a
// Command mismatch (stale file from another node or a hand-edited cursor)
// yields fresh, empty progress.
func LoadProgress(dir, command string) (*Progress, error) {
	fresh := &Progress{Command: command, Done: map[string]map[string]DoneMeta{}}
	data, err := readFileRetry(progressPath(dir))
	if os.IsNotExist(err) {
		return fresh, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read progress: %w", err)
	}
	var pr Progress
	if err := json.Unmarshal(data, &pr); err != nil {
		return nil, fmt.Errorf("parse progress: %w", err)
	}
	if pr.Command != command {
		return fresh, nil
	}
	if pr.Done == nil {
		pr.Done = map[string]map[string]DoneMeta{}
	}
	return &pr, nil
}

func (pr *Progress) Save(dir string) error {
	return atomicWriteJSON(progressPath(dir), pr)
}

// ClearProgress empties the progress file (parent success). Writing an empty
// record rather than deleting keeps the operation a single atomic rename.
func ClearProgress(dir string) error {
	return atomicWriteJSON(progressPath(dir), &Progress{Done: map[string]map[string]DoneMeta{}})
}

// CountDone is the number of distinct completed keys for one subcommand.
func (pr *Progress) CountDone(sub string) int { return len(pr.Done[sub]) }

// TotalDone is the number of completed work items across all subcommands.
// Recorded items are never removed within a visit, so this is monotonic; the
// parent finalize uses it to detect stragglers that committed while its lua
// ran outside the lock.
func (pr *Progress) TotalDone() int {
	total := 0
	for _, keys := range pr.Done {
		total += len(keys)
	}
	return total
}

// DoneKeys lists the completed keys for one subcommand, sorted.
func (pr *Progress) DoneKeys(sub string) []string {
	keys := make([]string, 0, len(pr.Done[sub]))
	for k := range pr.Done[sub] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Record marks one work item done.
func (pr *Progress) Record(sub, key string, meta DoneMeta) {
	if pr.Done == nil {
		pr.Done = map[string]map[string]DoneMeta{}
	}
	if pr.Done[sub] == nil {
		pr.Done[sub] = map[string]DoneMeta{}
	}
	pr.Done[sub][key] = meta
}

// QuotasMet reports whether every subcommand of cmd has reached its count.
// Quotas are monotonic: once met within a visit they cannot become unmet.
func (pr *Progress) QuotasMet(cmd *CommandSpec) bool {
	for i := range cmd.Subcommands {
		s := &cmd.Subcommands[i]
		if pr.CountDone(s.Name) < s.Count {
			return false
		}
	}
	return true
}

// QuotaStatus renders a one-line summary, e.g.
// "impl-feature 3/5, write-doc 1/3".
func (pr *Progress) QuotaStatus(cmd *CommandSpec) string {
	parts := make([]string, 0, len(cmd.Subcommands))
	for i := range cmd.Subcommands {
		s := &cmd.Subcommands[i]
		parts = append(parts, fmt.Sprintf("%s %d/%d", s.Name, pr.CountDone(s.Name), s.Count))
	}
	return strings.Join(parts, ", ")
}
