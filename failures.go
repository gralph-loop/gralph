package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Failure memory (framework-internal, like state.json and progress.json).
//
// st.Failures only budgets retries and resets on every session rotation, so
// the REASON a node failed dies with the session that produced it -- and the
// next session starts with a fresh context, doomed to repeat the same
// mistake. failures.json keeps the most recent failure reasons per node
// label (plain commands use the command name, subcommands "name:key") across
// sessions: `next` appends them to the guidance, and a success on the node
// clears them. Writes happen under the state lock the commit already holds.
// ---------------------------------------------------------------------------

// MaxFailureRecords caps how many reasons are kept per label; the oldest is
// evicted first.
const MaxFailureRecords = 3

// FailureRecord is one persisted failure of a node label.
type FailureRecord struct {
	Reason  string `json:"reason"`  // gralph.fail reason, or the error string on SCRIPT ERROR
	Failure int    `json:"failure"` // cumulative failure number (survives session rotation)
	At      string `json:"at"`      // RFC3339
}

// Failures is the on-disk failures.json: node label -> recent failures,
// oldest first.
type Failures map[string][]FailureRecord

func failuresPath(dir string) string { return filepath.Join(dir, "failures.json") }

// LoadFailures reads the failure memory. A missing file yields an empty map.
func LoadFailures(dir string) (Failures, error) {
	f := Failures{}
	data, err := os.ReadFile(failuresPath(dir))
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read failures: %w", err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse failures: %w", err)
	}
	return f, nil
}

func (f Failures) Save(dir string) error {
	return atomicWriteJSON(failuresPath(dir), f)
}

// Record appends one failure for label. The cumulative number continues from
// the newest kept record -- unlike the in-session counter it never resets --
// and the list is trimmed to the most recent MaxFailureRecords.
func (f Failures) Record(label, reason string, at time.Time) {
	recs := f[label]
	n := 1
	if len(recs) > 0 {
		n = recs[len(recs)-1].Failure + 1
	}
	recs = append(recs, FailureRecord{
		Reason:  reason,
		Failure: n,
		At:      at.UTC().Format(time.RFC3339),
	})
	if len(recs) > MaxFailureRecords {
		recs = recs[len(recs)-MaxFailureRecords:]
	}
	f[label] = recs
}

// Clear drops the records of one exact label, reporting whether anything was
// removed so callers can skip a no-op write.
func (f Failures) Clear(label string) bool {
	if _, ok := f[label]; !ok {
		return false
	}
	delete(f, label)
	return true
}

// ClearPrefix drops every label starting with prefix -- a finalized parent
// clearing its subcommands' "name:key" records.
func (f Failures) ClearPrefix(prefix string) bool {
	changed := false
	for label := range f {
		if strings.HasPrefix(label, prefix) {
			delete(f, label)
			changed = true
		}
	}
	return changed
}

// LabelsWithPrefix lists the recorded labels starting with prefix, sorted
// for deterministic rendering.
func (f Failures) LabelsWithPrefix(prefix string) []string {
	var labels []string
	for label := range f {
		if strings.HasPrefix(label, prefix) {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	return labels
}
