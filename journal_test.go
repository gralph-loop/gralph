package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// readJournal parses every journal.jsonl line, in file order.
func readJournal(t *testing.T, dir string) []JournalEvent {
	t.Helper()
	f, err := os.Open(journalPath(dir))
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	defer f.Close()
	var evs []JournalEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev JournalEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("parse journal line %q: %v", sc.Text(), err)
		}
		evs = append(evs, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return evs
}

func wantEvent(t *testing.T, ev JournalEvent, kind string) {
	t.Helper()
	if ev.Event != kind {
		t.Fatalf("event = %q, want %q (%+v)", ev.Event, kind, ev)
	}
	if ev.At == "" {
		t.Fatalf("event %q has no `at` timestamp", kind)
	}
}

// Failure, then success: the journal must hold both transitions in commit
// order, with the failure's reason and the success's next cursor.
func TestJournalRecordsFailureThenSuccess(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    args:
      - name: ok
        required: true
    lua: gate.lua
    next: [b]
  - name: b
`, map[string]string{
		"gate.lua": `if gralph.args.ok ~= "yes" then gralph.fail("reason: pass --ok yes") end`,
	})

	run(t, p, "a", "--ok", "no")
	run(t, p, "a", "--ok", "yes")
	run(t, p, "b")

	evs := readJournal(t, p.StateDir)
	if len(evs) != 3 {
		t.Fatalf("journal has %d events, want 3: %+v", len(evs), evs)
	}

	wantEvent(t, evs[0], EvCommandFailed)
	if evs[0].Command != "a" || evs[0].Failure != 1 || !strings.Contains(evs[0].Reason, "pass --ok yes") {
		t.Fatalf("failure event = %+v", evs[0])
	}

	wantEvent(t, evs[1], EvCommandSucceeded)
	if evs[1].Command != "a" || evs[1].Next != "b" || evs[1].GateMs < 0 {
		t.Fatalf("success event = %+v", evs[1])
	}

	// Last command: the journaled next cursor is DONE.
	wantEvent(t, evs[2], EvCommandSucceeded)
	if evs[2].Command != "b" || evs[2].Next != DoneCursor {
		t.Fatalf("terminal success event = %+v", evs[2])
	}
}

// A lua error() is journaled as a failure with a script-error reason.
func TestJournalRecordsScriptError(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    lua: gate.lua
`, map[string]string{
		"gate.lua": `error("boom")`,
	})

	run(t, p, "a")
	evs := readJournal(t, p.StateDir)
	if len(evs) != 1 {
		t.Fatalf("journal has %d events, want 1", len(evs))
	}
	wantEvent(t, evs[0], EvCommandFailed)
	if !strings.Contains(evs[0].Reason, "script error") || !strings.Contains(evs[0].Reason, "boom") {
		t.Fatalf("script error reason = %q", evs[0].Reason)
	}
}

// Fork/join: each recorded work item gets a subitem event, then the parent's
// finalize success follows, all in commit order.
func TestJournalRecordsSubitemsInOrder(t *testing.T) {
	p := writeProfile(t, flowProfile, nil)

	run(t, p, "sub-a", "--item", "x")
	run(t, p, "sub-a", "--item", "y")
	run(t, p, "sub-b")
	run(t, p, "parent")

	evs := readJournal(t, p.StateDir)
	if len(evs) != 4 {
		t.Fatalf("journal has %d events, want 4: %+v", len(evs), evs)
	}
	for i, want := range []struct{ sub, key string }{
		{"sub-a", "x"}, {"sub-a", "y"}, {"sub-b", "sub-b"},
	} {
		wantEvent(t, evs[i], EvSubitemRecorded)
		if evs[i].Subcommand != want.sub || evs[i].Key != want.key {
			t.Fatalf("subitem event %d = %+v, want %+v", i, evs[i], want)
		}
	}
	wantEvent(t, evs[3], EvCommandSucceeded)
	if evs[3].Command != "parent" || evs[3].Next != "wrap" {
		t.Fatalf("finalize event = %+v", evs[3])
	}

	// Budget-free rejections (duplicate key, wrong command) leave no trace.
	run(t, p, "sub-a", "--item", "z")
	if got := len(readJournal(t, p.StateDir)); got != 4 {
		t.Fatalf("rejection must not be journaled, got %d events", got)
	}
}
