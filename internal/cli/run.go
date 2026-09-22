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

// expandPatterns resolves file patterns (globs allowed) to absolute paths.
// Patterns are processed in the order given; files matched by a single glob
// are sorted for determinism, and duplicates are dropped.
func expandPatterns(kind string, patterns []string) ([]string, error) {
	var files []string
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid %s pattern %q: %w", kind, pattern, err)
		}
		if len(matches) == 0 {
			// Treat a non-glob path that doesn't exist as an explicit error so
			// typos surface immediately rather than silently skipping it.
			return nil, fmt.Errorf("%s file/pattern matched nothing: %q", kind, pattern)
		}
		sort.Strings(matches)
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve %s file %q: %w", kind, m, err)
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true
			files = append(files, abs)
		}
	}

	return files, nil
}

// loadSetupScripts reads each file matched by patterns into a SetupScript.
func loadSetupScripts(patterns []string) ([]runner.SetupScript, error) {
	files, err := expandPatterns("setup", patterns)
	if err != nil {
		return nil, err
	}
	var scripts []runner.SetupScript
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("failed to read setup file %q: %w", f, err)
		}
		scripts = append(scripts, runner.SetupScript{Name: f, SQL: string(data)})
	}
	return scripts, nil
}

// explicitSources builds the source list from --source patterns. Files are
// keyed relative to searchPath, like discovered sources, so coverage reports
// resolve them the same way.
func explicitSources(searchPath string, patterns []string) ([]discovery.DiscoveredFile, error) {
	files, err := expandPatterns("source", patterns)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(searchPath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}
	var sources []discovery.DiscoveredFile
	for _, f := range files {
		if discovery.ClassifyFile(filepath.Base(f)) != discovery.FileTypeSource {
			return nil, fmt.Errorf("source file %q is not a source (*_test.sql files are tests)", f)
		}
		info, err := os.Stat(f)
		if err != nil {
			return nil, fmt.Errorf("failed to stat source file %q: %w", f, err)
		}
		rel, err := filepath.Rel(absRoot, f)
		if err != nil {
			return nil, fmt.Errorf("failed to get relative path: %w", err)
		}
		sources = append(sources, discovery.DiscoveredFile{
			Path:         f,
			RelativePath: filepath.ToSlash(rel),
			Type:         discovery.FileTypeSource,
			ModTime:      info.ModTime(),
		})
	}
	return sources, nil
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

	// Step 2: Discover source files (co-located with tests) unless the caller
	// listed them explicitly.
	var sourceFiles []discovery.DiscoveredFile
	if len(config.SourceFiles) > 0 {
		sourceFiles, err = explicitSources(searchPath, config.SourceFiles)
	} else {
		sourceFiles, err = discovery.DiscoverCoLocatedSources(searchPath, testFiles)
	}
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
	if len(config.SourceFiles) > 0 {
		executor.UseExplicitSources()
	}

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

	// Step 8: Save coverage data. The discovery root is recorded alongside it
	// so `pgcov report` can resolve the keys without being told where they came
	// from: they are relative to this directory, not to the working directory.
	cov := collector.Coverage()
	if absRoot, err := filepath.Abs(searchPath); err == nil {
		cov.Root = absRoot
	}

	store := coverage.NewStore(config.CoverageFile)
	if err := store.Save(cov); err != nil {
		return 1, fmt.Errorf("failed to save coverage: %w", err)
	}

	// Step 9: Display summary
	summary := runner.SummarizeRuns(testRuns)
	coveragePercent := collector.TotalCoveragePercent()
	hasExecutable := collector.HasExecutablePositions()

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
	execCovered, execTotal := collector.ExecutablePositionCounts()
	implicitCovered, implicitTotal := collector.ImplicitPositionCounts()

	// The headline number counts executable statements only. DDL/DML is marked
	// covered the moment its file loads, so folding it in made every CREATE
	// TABLE a permanently-100%-covered denominator entry; it is reported on its
	// own line instead of inflating the percentage.
	if hasExecutable {
		fmt.Printf("Coverage: %.2f%% executable (%d/%d statements)\n",
			coveragePercent, execCovered, execTotal)
	} else {
		fmt.Printf("Coverage: n/a - no executable statements were instrumented\n")
	}
	if implicitTotal > 0 {
		fmt.Printf("          %d/%d DDL/DML statements loaded\n", implicitCovered, implicitTotal)
	}
	fmt.Printf("Time:     %v\n", time.Since(startTime).Round(time.Millisecond))
	fmt.Printf("\n")
	fmt.Printf("Coverage data written to %s\n", config.CoverageFile)

	// Determine exit code: test failures take precedence; otherwise check the
	// threshold, which applies to the executable percentage. Measuring it
	// against the old inflated number meant a suite with no real PL/pgSQL
	// coverage could clear a high threshold on DDL alone.
	exitCode := summary.ExitCode()
	if exitCode == 0 && config.FailUnder > 0 {
		if !hasExecutable {
			// Nothing measurable was instrumented, so no threshold can be
			// satisfied. Fail loudly rather than passing on an empty measurement.
			fmt.Printf("Coverage threshold %.2f%% cannot be met: no executable statements were instrumented\n",
				config.FailUnder)
			return 1, nil
		}
		if coveragePercent < config.FailUnder {
			fmt.Printf("Coverage %.2f%% is below threshold %.2f%%\n", coveragePercent, config.FailUnder)
			return 1, nil
		}
	}

	return exitCode, nil
}
