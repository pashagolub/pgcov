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
	"github.com/cybertec-postgresql/pgcov/internal/parser"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// writeFile writes content to dir/name, failing the test on error.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// buildCrossDirFixture creates two sibling test directories whose sources are
// mutually exclusive: both define a table named "shared", with different
// columns. Loading both into one database is impossible - the second CREATE
// TABLE fails with "relation already exists".
//
// That makes the fixture a direct probe for per-test source filtering: a run
// that correctly loads only the sources co-located with each test passes, and a
// run that loads every discovered source into every temp database fails.
func buildCrossDirFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	dirA := filepath.Join(root, "alpha")
	writeFile(t, dirA, "alpha.sql", `
CREATE TABLE shared (id integer);

CREATE OR REPLACE FUNCTION alpha_count() RETURNS integer AS $$
BEGIN
    RETURN (SELECT count(*) FROM shared);
END;
$$ LANGUAGE plpgsql;
`)
	writeFile(t, dirA, "alpha_test.sql", `
INSERT INTO shared (id) VALUES (1);
DO $$
BEGIN
    IF alpha_count() <> 1 THEN
        RAISE EXCEPTION 'alpha_count() returned %', alpha_count();
    END IF;
END;
$$;
`)

	dirB := filepath.Join(root, "beta")
	writeFile(t, dirB, "beta.sql", `
CREATE TABLE shared (name text);

CREATE OR REPLACE FUNCTION beta_first() RETURNS text AS $$
BEGIN
    RETURN (SELECT name FROM shared ORDER BY name LIMIT 1);
END;
$$ LANGUAGE plpgsql;
`)
	writeFile(t, dirB, "beta_test.sql", `
INSERT INTO shared (name) VALUES ('x');
DO $$
BEGIN
    IF beta_first() <> 'x' THEN
        RAISE EXCEPTION 'beta_first() returned %', beta_first();
    END IF;
END;
$$;
`)

	return root
}

// instrumentTreeN discovers, parses and instruments every source co-located
// with a test under root, mirroring what cli.Run does before execution. The
// expected counts guard against a fixture that silently stopped matching.
func instrumentTreeN(t *testing.T, root string, wantTests, wantSources int) ([]discovery.DiscoveredFile, []*instrument.InstrumentedSQL) {
	t.Helper()

	testFiles, err := discovery.DiscoverTests(root)
	if err != nil {
		t.Fatalf("discover tests: %v", err)
	}
	if len(testFiles) != wantTests {
		t.Fatalf("expected %d test files, got %d", wantTests, len(testFiles))
	}

	sourceFiles, err := discovery.DiscoverCoLocatedSources(root, testFiles)
	if err != nil {
		t.Fatalf("discover sources: %v", err)
	}
	if len(sourceFiles) != wantSources {
		t.Fatalf("expected %d source files, got %d", wantSources, len(sourceFiles))
	}

	var parsed []*parser.ParsedSQL
	for i := range sourceFiles {
		ps, err := parser.Parse(&sourceFiles[i])
		if err != nil {
			t.Fatalf("parse %s: %v", sourceFiles[i].RelativePath, err)
		}
		parsed = append(parsed, ps)
	}

	instrumented, err := instrument.GenerateCoverageInstruments(parsed, instrument.DefaultChannel)
	if err != nil {
		t.Fatalf("instrument: %v", err)
	}
	return testFiles, instrumented
}

// TestParallelFiltersSourcesPerTestDirectory is the regression test for the
// defect where WorkerPool.worker passed the full, unfiltered source slice to
// Executor.Execute while ExecuteBatch filtered per test directory.
//
// It needs both >= 2 workers and >= 2 tests, because ExecuteParallel delegates
// to ExecuteBatch (which always filtered) when either count is 1 - that
// fallback is exactly why the bug stayed hidden.
//
// Before the fix, both tests fail here with `relation "shared" already exists`
// while passing under --parallel 1.
func TestParallelFiltersSourcesPerTestDirectory(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          60 * time.Second,
		Parallelism:      2,
		Verbose:          testing.Verbose(),
	}

	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	root := buildCrossDirFixture(t)
	testFiles, instrumented := instrumentTreeN(t, root, 2, 2)

	executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	workerPool := runner.NewWorkerPool(executor, config.Parallelism, config.Verbose)

	runs, err := workerPool.ExecuteParallel(ctx, testFiles, instrumented)
	if err != nil {
		t.Fatalf("parallel execution: %v", err)
	}
	if len(runs) != len(testFiles) {
		t.Fatalf("expected %d runs, got %d", len(testFiles), len(runs))
	}

	for _, run := range runs {
		if run.Status != runner.TestPassed {
			t.Errorf("parallel run %s: status=%v err=%v (sources from a sibling "+
				"directory were loaded into this test's temp database)",
				run.Test.RelativePath, run.Status, run.Error)
		}
	}
}

// TestSequentialAndParallelLoadTheSameSources pins the two execution paths to
// the same behaviour on the fixture above: whatever one does, the other must do
// too. A divergence here means the filtering choke point has moved back out
// into the callers.
func TestSequentialAndParallelLoadTheSameSources(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          60 * time.Second,
		Parallelism:      2,
		Verbose:          testing.Verbose(),
	}

	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	root := buildCrossDirFixture(t)
	testFiles, instrumented := instrumentTreeN(t, root, 2, 2)

	seqExec := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	seqRuns, err := seqExec.ExecuteBatch(ctx, testFiles, instrumented)
	if err != nil {
		t.Fatalf("sequential execution: %v", err)
	}

	parExec := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	parRuns, err := runner.NewWorkerPool(parExec, 2, config.Verbose).ExecuteParallel(ctx, testFiles, instrumented)
	if err != nil {
		t.Fatalf("parallel execution: %v", err)
	}

	statusByTest := func(runs []*runner.TestRun) map[string]runner.TestStatus {
		m := make(map[string]runner.TestStatus, len(runs))
		for _, r := range runs {
			m[r.Test.RelativePath] = r.Status
		}
		return m
	}

	seq, par := statusByTest(seqRuns), statusByTest(parRuns)
	for name, seqStatus := range seq {
		parStatus, ok := par[name]
		if !ok {
			t.Errorf("%s present in sequential results but missing from parallel", name)
			continue
		}
		if seqStatus != parStatus {
			t.Errorf("%s: sequential=%v parallel=%v - execution paths diverged",
				name, seqStatus, parStatus)
		}
	}
}
