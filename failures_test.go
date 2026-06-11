package main

import (
	"strings"
	"testing"
	"time"
)

// Tests for the failure memory: reasons recorded in failures.json survive
// session rotation, surface in `gralph next`, and are cleared on success.

const memoryProfile = `commands:
  - name: a
    guidance: do the thing
    args:
      - name: ok
        required: true
      - name: why
    lua: gate.lua
    next: [b]
  - name: b
    guidance: done
`

const memoryGate = `if gralph.args.ok ~= "yes" then
  gralph.fail(gralph.args.why or "reason: pass --ok yes")
end`

// rotateSession mimics the orchestrator's per-iteration reset: new session
// id, session-scoped failure counters wiped. failures.json must survive it.
func rotateSession(t *testing.T, p *Profile) {
	t.Helper()
	st, err := LoadState(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	st.SessionID = newSessionID()
	st.Failures = map[string]int{}
	if err := st.Save(p.StateDir); err != nil {
		t.Fatal(err)
	}
}

func TestFailureMemoryRenderedAcrossSessions(t *testing.T) {
	p := writeProfile(t, memoryProfile, map[string]string{"gate.lua": memoryGate})

	run(t, p, "a", "--ok", "no", "--why", "reason: report file 'report.json' does not exist")
	rotateSession(t, p) // the counter resets, the memory must not

	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Previous attempts on this task failed:",
		"- (failure 1) reason: report file 'report.json' does not exist",
		"Avoid repeating the same mistakes.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderNext missing %q:\n%s", want, out)
		}
	}

	// The cumulative number keeps counting in the new session even though
	// the in-session budget restarted at 1.
	res := run(t, p, "a", "--ok", "no", "--why", "reason: still broken")
	wantContains(t, res, "(failure 1)") // session-scoped counter
	out, err = renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "- (failure 2) reason: still broken") {
		t.Fatalf("cumulative failure number must survive rotation:\n%s", out)
	}

	// Records carry an RFC3339 timestamp.
	fr, err := LoadFailures(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr["a"]) != 2 || fr["a"][0].At == "" {
		t.Fatalf("failures.json records = %+v", fr["a"])
	}
}

func TestFailureMemoryRecordsScriptError(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: a
    lua: gate.lua
`, map[string]string{"gate.lua": `error("boom")`})

	run(t, p, "a")
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	// A script crash persists the error string as the reason.
	if !strings.Contains(out, "Previous attempts on this task failed:") || !strings.Contains(out, "boom") {
		t.Fatalf("script error reason missing from renderNext:\n%s", out)
	}
}

func TestFailureMemoryClearedOnSuccess(t *testing.T) {
	p := writeProfile(t, memoryProfile, map[string]string{"gate.lua": memoryGate})

	run(t, p, "a", "--ok", "no")
	run(t, p, "a", "--ok", "yes")

	fr, err := LoadFailures(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr["a"]) != 0 {
		t.Fatalf("success must clear the node's failure memory, got %+v", fr["a"])
	}
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Previous attempts") {
		t.Fatalf("cleared memory must not render:\n%s", out)
	}
}

func TestFailureMemoryKeepsLastThree(t *testing.T) {
	p := writeProfile(t, memoryProfile, map[string]string{"gate.lua": memoryGate})

	for _, why := range []string{"reason: r1", "reason: r2", "reason: r3", "reason: r4"} {
		run(t, p, "a", "--ok", "no", "--why", why)
	}

	fr, err := LoadFailures(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	recs := fr["a"]
	if len(recs) != MaxFailureRecords {
		t.Fatalf("kept %d records, want %d", len(recs), MaxFailureRecords)
	}
	// Oldest evicted first; cumulative numbers are preserved.
	for i, want := range []struct {
		reason  string
		failure int
	}{{"reason: r2", 2}, {"reason: r3", 3}, {"reason: r4", 4}} {
		if recs[i].Reason != want.reason || recs[i].Failure != want.failure {
			t.Fatalf("record %d = %+v, want %+v", i, recs[i], want)
		}
	}
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "reason: r1") || !strings.Contains(out, "- (failure 4) reason: r4") {
		t.Fatalf("renderNext must show only the kept records:\n%s", out)
	}
}

func TestFailureMemorySubcommandLabels(t *testing.T) {
	p := writeProfile(t, `commands:
  - name: parent
    guidance: fan out
    subcommands:
      - name: check
        count: 2
        key: k
        args:
          - name: k
          - name: ok
        lua: check.lua
    next: [wrap]
  - name: wrap
`, map[string]string{
		"check.lua": `if gralph.args.ok ~= "yes" then gralph.fail("reason: item broken") end`,
	})

	// Failures are recorded per (subcommand, key) label and rendered with it.
	run(t, p, "check", "--k", "a", "--ok", "no")
	run(t, p, "check", "--k", "b", "--ok", "no")
	out, err := renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"- (failure 1) [check:a] reason: item broken",
		"- (failure 1) [check:b] reason: item broken",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderNext missing %q:\n%s", want, out)
		}
	}

	// A key's success clears only that label.
	run(t, p, "check", "--k", "a", "--ok", "yes")
	out, err = renderNext(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[check:a]") || !strings.Contains(out, "[check:b]") {
		t.Fatalf("only the succeeded key's records may be cleared:\n%s", out)
	}

	// Parent finalize success wipes all remaining subcommand records: key b
	// succeeded under its own label, but a straggler label from a key that
	// never re-ran (here: none left after b succeeds) must not linger either.
	run(t, p, "check", "--k", "b", "--ok", "yes")
	fr, err := LoadFailures(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	fr.Record("check:zombie", "reason: stale", time.Now())
	if err := fr.Save(p.StateDir); err != nil {
		t.Fatal(err)
	}
	run(t, p, "parent")
	fr, err = LoadFailures(p.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(fr) != 0 {
		t.Fatalf("parent finalize must clear all of its subcommand records, got %+v", fr)
	}
}
