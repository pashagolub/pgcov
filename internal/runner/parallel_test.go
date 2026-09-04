package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// TestParallelExecution tests that parallel execution produces correct results
func TestParallelExecution(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()

	// Setup config
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      4, // Use 4 workers
		Verbose:          testing.Verbose(),
	}

	// Connect to database
	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("Cannot connect to PostgreSQL: %v", err)
	}
	defer pool.Close()

	// Discover test files in parallel test fixtures
	testFiles, err := discovery.DiscoverTests("../../testdata/parallel")
	if err != nil {
		t.Fatalf("Failed to discover tests: %v", err)
	}

	if len(testFiles) < 4 {
		t.Fatalf("Expected at least 4 test files, got %d", len(testFiles))
	}

	// Discover source files
	sourceFiles, err := discovery.DiscoverCoLocatedSources("../../testdata/parallel", testFiles)
	if err != nil {
		t.Fatalf("Failed to discover sources: %v", err)
	}

	// Parse and instrument sources
	var parsedSources []*parser.ParsedSQL
	for i := range sourceFiles {
		parsed, err := parser.Parse(&sourceFiles[i])
		if err != nil {
			t.Fatalf("Failed to parse %s: %v", sourceFiles[i].RelativePath, err)
		}
		parsedSources = append(parsedSources, parsed)
	}

	instrumentedSources, err := instrument.GenerateCoverageInstruments(parsedSources, instrument.DefaultChannel)
	if err != nil {
		t.Fatalf("Failed to instrument sources: %v", err)
	}

	// Execute tests in parallel
	executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	workerPool := runner.NewWorkerPool(executor, config.Parallelism, config.Verbose)

	startTime := time.Now()
	testRuns, err := workerPool.ExecuteParallel(ctx, testFiles, instrumentedSources)
	parallelDuration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Parallel execution failed: %v", err)
	}

	// Verify all tests completed
	if len(testRuns) != len(testFiles) {
		t.Fatalf("Expected %d test runs, got %d", len(testFiles), len(testRuns))
	}

	// Verify all tests passed
	for _, run := range testRuns {
		if run.Status != runner.TestPassed {
			t.Errorf("Test %s failed: %v", run.Test.RelativePath, run.Error)
		}
	}

	// Execute tests sequentially for comparison
	executor2 := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
	startTime = time.Now()
	testRuns2, err := executor2.ExecuteBatch(ctx, testFiles, instrumentedSources)
	sequentialDuration := time.Since(startTime)

	if err != nil {
		t.Fatalf("Sequential execution failed: %v", err)
	}

	// Verify sequential tests passed
	for _, run := range testRuns2 {
		if run.Status != runner.TestPassed {
			t.Errorf("Sequential test %s failed: %v", run.Test.RelativePath, run.Error)
		}
	}

	// Collect coverage from both runs
	collector1 := coverage.NewCollector()
	if err := collector1.CollectFromRuns(testRuns); err != nil {
		t.Fatalf("Failed to collect parallel coverage: %v", err)
	}

	collector2 := coverage.NewCollector()
	if err := collector2.CollectFromRuns(testRuns2); err != nil {
		t.Fatalf("Failed to collect sequential coverage: %v", err)
	}

	// Verify coverage is identical
	cov1 := collector1.Coverage()
	cov2 := collector2.Coverage()

	if len(cov1.Positions) != len(cov2.Positions) {
		t.Errorf("Coverage file count mismatch: parallel=%d, sequential=%d",
			len(cov1.Positions), len(cov2.Positions))
	}

	for file := range cov1.Positions {
		if _, exists := cov2.Positions[file]; !exists {
			t.Errorf("File %s present in parallel coverage but not sequential", file)
		}
	}

	// Log timing comparison
	t.Logf("Parallel execution: %v", parallelDuration)
	t.Logf("Sequential execution: %v", sequentialDuration)
	if len(testFiles) >= 4 && parallelDuration < sequentialDuration {
		t.Logf("✓ Parallel execution was faster")
	}
}

// TestParallelExecutionAccuracy verifies coverage accuracy with parallel execution
func TestParallelExecutionAccuracy(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()

	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      2,
		Verbose:          testing.Verbose(),
	}

	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("Cannot connect to PostgreSQL: %v", err)
	}
	defer pool.Close()

	// Use simple test fixtures
	testFiles, err := discovery.DiscoverTests("../../testdata/simple")
	if err != nil {
		t.Fatalf("Failed to discover tests: %v", err)
	}

	sourceFiles, err := discovery.DiscoverCoLocatedSources("../../testdata/parallel", testFiles)
	if err != nil {
		t.Fatalf("Failed to discover sources: %v", err)
	}

	var parsedSources []*parser.ParsedSQL
	for i := range sourceFiles {
		parsed, err := parser.Parse(&sourceFiles[i])
		if err != nil {
			t.Fatalf("Failed to parse: %v", err)
		}
		parsedSources = append(parsedSources, parsed)
	}

	instrumentedSources, err := instrument.GenerateCoverageInstruments(parsedSources, instrument.DefaultChannel)
	if err != nil {
		t.Fatalf("Failed to instrument: %v", err)
	}

	// Run multiple times with parallel execution
	const runs = 3
	var coveragePercentages []float64

	for i := range runs {
		executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
		workerPool := runner.NewWorkerPool(executor, config.Parallelism, config.Verbose)
		testRuns, err := workerPool.ExecuteParallel(ctx, testFiles, instrumentedSources)
		if err != nil {
			t.Fatalf("Run %d failed: %v", i+1, err)
		}

		collector := coverage.NewCollector()
		if err := collector.CollectFromRuns(testRuns); err != nil {
			t.Fatalf("Run %d coverage collection failed: %v", i+1, err)
		}

		coveragePercentages = append(coveragePercentages, collector.TotalCoveragePercent())
	}

	// Verify all runs produced the same coverage
	for i := 1; i < runs; i++ {
		if coveragePercentages[i] != coveragePercentages[0] {
			t.Errorf("Coverage inconsistent: run 1=%.2f%%, run %d=%.2f%%",
				coveragePercentages[0], i+1, coveragePercentages[i])
		}
	}

	t.Logf("✓ Coverage consistent across %d parallel runs: %.2f%%", runs, coveragePercentages[0])
}
