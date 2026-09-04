package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
)

// loadSetupScripts expands the given file patterns (globs allowed) and reads
// each matched .sql file into a SetupScript. Patterns are processed in the
// order given; files matched by a single glob are sorted for determinism.
func loadSetupScripts(patterns []string) ([]runner.SetupScript, error) {
	var scripts []runner.SetupScript
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid setup pattern %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			// Treat a non-glob path that doesn't exist as an explicit error so
			// typos surface immediately rather than silently skipping setup.
			return nil, fmt.Errorf("setup file/pattern matched nothing: %q", pattern)
		}
		sort.Strings(matches)
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve setup file %q: %w", m, err)
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true

			data, err := os.ReadFile(m)
			if err != nil {
				return nil, fmt.Errorf("failed to read setup file %q: %w", m, err)
			}
			scripts = append(scripts, runner.SetupScript{Name: m, SQL: string(data)})
		}
	}

	return scripts, nil
}

// generateCoverageChannel returns a per-run unique NOTIFY channel name.  The
// returned string uses only identifier-safe characters (lowercase letters,
// digits, underscore) so it can be safely interpolated into LISTEN/<channel>
// and pg_notify('<channel>', ...) SQL without escaping.
func generateCoverageChannel() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("failed to read random bytes for coverage channel: %w", err)
	}
	return "pgcov_" + hex.EncodeToString(b[:]), nil
}

// Run executes the test runner workflow
func Run(ctx context.Context, config *Config, searchPath string) (int, error) {
	startTime := time.Now()

	// Generate a per-run unique NOTIFY channel when the caller did not
	// supply one.  This avoids collisions with user code that NOTIFYs on
	// the well-known "pgcov" name inside the temp database.
	if config.CoverageChannel == "" {
		ch, err := generateCoverageChannel()
		if err != nil {
			return 1, err
		}
		config.CoverageChannel = ch
	}

	if config.Verbose {
		fmt.Printf("pgcov: discovering tests in %s\n", searchPath)
		fmt.Printf("pgcov: NOTIFY channel = %s\n", config.CoverageChannel)
	}

	// Step 1: Discover test files
	testFiles, err := discovery.DiscoverTests(searchPath)
	if err != nil {
		return 1, fmt.Errorf("failed to discover tests: %w", err)
	}

	if len(testFiles) == 0 {
		fmt.Println("No test files found (*_test.sql)")
		return 0, nil
	}

	if config.Verbose {
		fmt.Printf("Found %d test file(s)\n", len(testFiles))
	}

	// Step 2: Discover source files (co-located with tests)
	sourceFiles, err := discovery.DiscoverCoLocatedSources(searchPath, testFiles)
	if err != nil {
		return 1, fmt.Errorf("failed to discover source files: %w", err)
	}

	if config.Verbose {
		fmt.Printf("Found %d source file(s)\n", len(sourceFiles))
	}

	// Step 3: Parse source files
	var parsedSources []*parser.ParsedSQL
	for i := range sourceFiles {
		parsed, err := parser.Parse(&sourceFiles[i])
		if err != nil {
			return 1, fmt.Errorf("failed to parse %s: %w", sourceFiles[i].RelativePath, err)
		}
		parsedSources = append(parsedSources, parsed)
	}

	// Step 4: Instrument source files
	instrumentedSources, err := instrument.GenerateCoverageInstruments(parsedSources, config.CoverageChannel)
	if err != nil {
		return 1, fmt.Errorf("failed to instrument sources: %w", err)
	}

	// Step 5: Connect to PostgreSQL
	pool, err := database.NewPool(ctx, config)
	if err != nil {
		return 1, fmt.Errorf("database connection failed: %w", err)
	}
	defer pool.Close()

	if config.Verbose {
		fmt.Println("Connected to PostgreSQL")
	}

	// Step 6: Execute tests (parallel or sequential based on config)
	executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, config.CoverageChannel)

	// Load any prerequisite setup scripts (globs expanded, order preserved) so
	// they run in each test's temp database before the instrumented sources.
	if len(config.SetupFiles) > 0 {
		scripts, err := loadSetupScripts(config.SetupFiles)
		if err != nil {
			return 1, err
		}
		if config.Verbose {
			fmt.Printf("Loaded %d setup script(s)\n", len(scripts))
		}
		executor.SetSetupScripts(scripts)
	}

	var testRuns []*runner.TestRun
	if config.Parallelism > 1 {
		// Use parallel execution
		if config.Verbose {
			fmt.Printf("Executing tests in parallel (workers: %d)\n", config.Parallelism)
		}
		workerPool := runner.NewWorkerPool(executor, config.Parallelism, config.Verbose)
		testRuns, err = workerPool.ExecuteParallel(ctx, testFiles, instrumentedSources)
	} else {
		// Use sequential execution
		if config.Verbose {
			fmt.Println("Executing tests sequentially")
		}
		testRuns, err = executor.ExecuteBatch(ctx, testFiles, instrumentedSources)
	}

	if err != nil {
		return 1, fmt.Errorf("test execution failed: %w", err)
	}

	// Step 7: Collect coverage
	collector := coverage.NewCollector()

	// Seed all instrumented positions with 0 hits so that unexecuted branches
	// (e.g. ELSIF/ELSE arms) appear as "not covered" in reports.
	collector.InitializeFromInstrumented(instrumentedSources)

	if err := collector.CollectFromRuns(testRuns); err != nil {
		return 1, fmt.Errorf("coverage collection failed: %w", err)
	}

	// Step 8: Save coverage data
	store := coverage.NewStore(config.CoverageFile)
	if err := store.Save(collector.Coverage()); err != nil {
		return 1, fmt.Errorf("failed to save coverage: %w", err)
	}

	// Step 9: Display summary
	summary := runner.SummarizeRuns(testRuns)
	coveragePercent := collector.TotalCoveragePercent()

	// Surface per-test failure messages so users do not have to re-run with
	// --verbose to see why a test failed. Each line is prefixed with "FAILED "
	// to match the run-level status badge printed earlier in --verbose mode.
	for _, line := range runner.FormatFailedTests(testRuns) {
		fmt.Println(line)
	}
	fmt.Printf("\n")
	// Timed-out tests are reported separately from failures so a suite that
	// is merely too slow is distinguishable from one that is broken. The
	// segment is omitted entirely when nothing timed out, keeping the
	// common-case output unchanged.
	if summary.TimedOutTests > 0 {
		fmt.Printf("Tests:    %d passed, %d failed, %d timed out, %d total\n",
			summary.PassedTests, summary.FailedTests, summary.TimedOutTests, summary.TotalTests)
	} else {
		fmt.Printf("Tests:    %d passed, %d failed, %d total\n",
			summary.PassedTests, summary.FailedTests, summary.TotalTests)
	}
	fmt.Printf("Coverage: %.2f%%\n", coveragePercent)
	fmt.Printf("Time:     %v\n", time.Since(startTime).Round(time.Millisecond))
	fmt.Printf("\n")
	fmt.Printf("Coverage data written to %s\n", config.CoverageFile)

	// Determine exit code: test failures take precedence; otherwise check threshold.
	exitCode := summary.ExitCode()
	if exitCode == 0 && config.FailUnder > 0 && coveragePercent < config.FailUnder {
		fmt.Printf("Coverage %.2f%% is below threshold %.2f%%\n", coveragePercent, config.FailUnder)
		return 1, nil
	}

	return exitCode, nil
}
