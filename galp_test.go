package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary double as gralph's built-in default launcher.
// runLoop's default launcher re-invokes the gralph executable as
// `gralph __galp-subprocess ...`; under `go test` os.Executable() is this test
// binary, so it must dispatch __galp-subprocess exactly like main() does.
// Without this, every loop test that uses the default launcher would just
// re-run the suite.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__galp-subprocess" {
		os.Exit(runGALPSubprocess(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestParseGALPResultRoundTrip(t *testing.T) {
	in := []byte(`{"protocol":1,"outcome":"completed","message":"ok"}`)
	res, err := parseGALPResult(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Outcome != OutcomeCompleted || res.Message != "ok" {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestParseGALPResultProtocolMismatch(t *testing.T) {
	_, err := parseGALPResult([]byte(`{"protocol":2,"outcome":"completed"}`))
	if err == nil {
		t.Fatal("expected protocol mismatch error")
	}
	if !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseGALPResultUnknownOutcome(t *testing.T) {
	_, err := parseGALPResult([]byte(`{"protocol":1,"outcome":"weird"}`))
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("expected unknown outcome error, got %v", err)
	}
}

func TestParseGALPResultRateLimitedNeedsRetryAfter(t *testing.T) {
	_, err := parseGALPResult([]byte(`{"protocol":1,"outcome":"rate_limited"}`))
	if err == nil || !strings.Contains(err.Error(), "retry_after") {
		t.Fatalf("expected missing retry_after error, got %v", err)
	}

	rfc := "2030-01-02T03:04:05Z"
	res, err := parseGALPResult([]byte(`{"protocol":1,"outcome":"rate_limited","retry_after":"` + rfc + `"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !res.RetryAfter.Equal(mustTime(t, rfc)) {
		t.Fatalf("retry_after = %v", res.RetryAfter)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

// writeFakeLauncher writes an executable bash launcher that ignores the agent
// and deterministically writes the given result JSON body.
func writeFakeLauncher(t *testing.T, dir, name, resultBody string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/usr/bin/env bash\nset -u\ncat > \"$GALP_RESULT_FILE\" <<'EOF'\n" + resultBody + "\nEOF\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunLauncherConformance exercises every host-visible mapping of a
// launcher's behavior: each outcome value, a non-zero launcher exit, a missing
// result file, and a protocol mismatch.
func TestRunLauncherConformance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("conformance launchers need bash")
	}
	dir := t.TempDir()
	p := writeLoopProfile(t, "agent:\n  command: [\"true\"]\ncommands:\n  - name: implement\n    guidance: go\n")

	cases := []struct {
		name       string
		body       string // result JSON; "" => launcher writes nothing
		exitNonNil bool
		want       SessionOutcome
		wantErr    string // substring; "" => no host error
	}{
		{name: "completed", body: `{"protocol":1,"outcome":"completed"}`, want: OutcomeCompleted},
		{name: "crashed", body: `{"protocol":1,"outcome":"crashed","message":"boom"}`, want: OutcomeCrashed},
		{name: "timed_out", body: `{"protocol":1,"outcome":"timed_out"}`, want: OutcomeTimedOut},
		{name: "rate_limited", body: `{"protocol":1,"outcome":"rate_limited","retry_after":"2030-01-01T00:00:00Z"}`, want: OutcomeRateLimited},
		{name: "unstartable", body: `{"protocol":1,"outcome":"unstartable"}`, want: OutcomeUnstartable},
		{name: "garbage_is_crashed", body: `not json`, want: OutcomeCrashed},
		{name: "protocol_mismatch", body: `{"protocol":9,"outcome":"completed"}`, wantErr: "protocol"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lp := writeFakeLauncher(t, dir, "l-"+tc.name, tc.body)
			res, err := runLauncher(context.Background(), p, []string{lp}, []string{"true"}, "prompt", "sess-1")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want host error containing %q, got res=%+v err=%v", tc.wantErr, res, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected host error: %v", err)
			}
			if res.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q (msg %q)", res.Outcome, tc.want, res.Message)
			}
		})
	}
}

// TestRunLauncherNonZeroExitIsCrashed: a launcher that exits non-zero is a
// transport failure; its result file is ignored and the session is crashed.
func TestRunLauncherNonZeroExitIsCrashed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	dir := t.TempDir()
	p := writeLoopProfile(t, "agent:\n  command: [\"true\"]\ncommands:\n  - name: implement\n    guidance: go\n")
	path := filepath.Join(dir, "broken")
	// Writes a perfectly good "completed" result but then exits 3.
	script := "#!/usr/bin/env bash\nprintf '{\"protocol\":1,\"outcome\":\"completed\"}' > \"$GALP_RESULT_FILE\"\nexit 3\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := runLauncher(context.Background(), p, []string{path}, []string{"true"}, "", "s")
	if err != nil {
		t.Fatalf("host error: %v", err)
	}
	if res.Outcome != OutcomeCrashed {
		t.Fatalf("outcome = %q, want crashed", res.Outcome)
	}
}

// TestRunLauncherMissingBinaryIsHostError: a launcher that cannot be started is
// a host-side error (so the loop fails fast), not a crashed result.
func TestRunLauncherMissingBinaryIsHostError(t *testing.T) {
	p := writeLoopProfile(t, "agent:\n  command: [\"true\"]\ncommands:\n  - name: implement\n    guidance: go\n")
	_, err := runLauncher(context.Background(), p, []string{"no-such-launcher-xyz"}, []string{"true"}, "", "s")
	if err == nil {
		t.Fatal("expected host error for missing launcher binary")
	}
}

// TestRunLoopRateLimitedDoesNotConsumeBudget proves a rate_limited outcome
// neither increments the give-up budget nor applies the agent timeout: an
// always-rate-limited launcher (with a past retry_after, so each wait is
// instant) runs the loop right up to the iteration cap and returns cleanly,
// instead of giving up with the "times in a row" budget error.
func TestRunLoopRateLimitedDoesNotConsumeBudget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs bash")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "rl")
	body := `{"protocol":1,"outcome":"rate_limited","retry_after":"2000-01-01T00:00:00Z"}`
	writeFakeLauncherAt(t, path, body)

	// One node, no lua: the cursor never advances, so without the rate-limit
	// carve-out a crash-equivalent would give up after MaxConsecutiveAgentFailures.
	yaml := "agent:\n  command: [\"true\"]\n  launcher: [\"" + path + "\"]\ncommands:\n  - name: implement\n    guidance: go\n"
	p := writeLoopProfile(t, yaml)

	done := make(chan error, 1)
	// Cap iterations well above the budget so a wrongly-counted rate limit
	// would trip "times in a row" long before the cap is reached.
	go func() { done <- runLoop(context.Background(), p, MaxConsecutiveAgentFailures+3) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("rate_limited loop should end cleanly at the iteration cap, got: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("rate_limited loop did not terminate")
	}
}

func writeFakeLauncherAt(t *testing.T, path, resultBody string) {
	t.Helper()
	script := "#!/usr/bin/env bash\nset -u\ncat > \"$GALP_RESULT_FILE\" <<'EOF'\n" + resultBody + "\nEOF\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
