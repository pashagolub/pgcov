package runner_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// TestExecuteMarksSlowTestAsTimedOut covers the status that was previously
// unreachable: nothing in the production code ever assigned TestTimeout, so a
// test that blew through --timeout was indistinguishable from a broken one and
// TestSummary.TimedOutTests was permanently zero.
//
// A test file that sleeps well past a deliberately tiny per-test timeout must
// now come back as TestTimeout, and must be counted as such in the summary.
func TestExecuteMarksSlowTestAsTimedOut(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	dir := t.TempDir()
	testPath := filepath.Join(dir, "slow_test.sql")
	if err := os.WriteFile(testPath, []byte("SELECT pg_sleep(30);\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	ctx := context.Background()
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          2 * time.Second, // far shorter than the pg_sleep above
		Parallelism:      1,
		Verbose:          testing.Verbose(),
	}

	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	testFiles, err := discovery.DiscoverTests(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(testFiles) != 1 {
		t.Fatalf("expected 1 test file, got %d", len(testFiles))
	}

	executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	runs, err := executor.ExecuteBatch(ctx, testFiles, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	if got := runs[0].Status; got != runner.TestTimeout {
		t.Errorf("status = %v, want %v (per-test deadline must be reported as a timeout, not a plain failure)",
			got, runner.TestTimeout)
	}
	if runs[0].Error == nil {
		t.Error("timed-out run must still carry the underlying error")
	}

	summary := runner.SummarizeRuns(runs)
	if summary.TimedOutTests != 1 {
		t.Errorf("summary.TimedOutTests = %d, want 1", summary.TimedOutTests)
	}
	if summary.FailedTests != 0 {
		t.Errorf("summary.FailedTests = %d, want 0 (a timeout is not a failure)", summary.FailedTests)
	}
	if summary.AllPassed() {
		t.Error("AllPassed() must be false when a test timed out")
	}
	if code := summary.ExitCode(); code != 1 {
		t.Errorf("ExitCode() = %d, want 1", code)
	}

	lines := runner.FormatFailedTests(runs)
	if len(lines) != 1 {
		t.Fatalf("expected 1 summary line for the timed-out run, got %d: %v", len(lines), lines)
	}
	if want := "TIMEOUT "; len(lines[0]) < len(want) || lines[0][:len(want)] != want {
		t.Errorf("summary line %q must use the TIMEOUT prefix", lines[0])
	}
}

// TestParentCancellationIsNotATimeout pins the distinction the status check
// relies on: testCtx is derived from the caller's context, so a cancelled
// parent also surfaces as testCtx.Err(). That is not a per-test timeout and
// must not be reported as one.
func TestParentCancellationIsNotATimeout(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "slow_test.sql"), []byte("SELECT pg_sleep(30);\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	config := &types.Config{
		ConnectionString: connString,
		Timeout:          60 * time.Second, // generous: the parent gives up first
		Parallelism:      1,
		Verbose:          testing.Verbose(),
	}

	pool, err := database.NewPool(context.Background(), config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	testFiles, err := discovery.DiscoverTests(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(2 * time.Second)
		cancel()
	}()
	defer cancel()

	executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	runs, err := executor.ExecuteBatch(ctx, testFiles, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(runs))
	}

	if got := runs[0].Status; got != runner.TestFailed {
		t.Errorf("status = %v, want %v (parent cancellation is not a per-test timeout)",
			got, runner.TestFailed)
	}
}
