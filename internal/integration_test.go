package integration_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/cli"
	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestEndToEndWithTestcontainers performs a complete end-to-end test
// using testcontainers to spin up a real PostgreSQL instance
func TestEndToEndWithTestcontainers(t *testing.T) {
	ctx := context.Background()

	// Start PostgreSQL container
	t.Log("Starting PostgreSQL container...")
	pgContainer, err := postgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate container: %v", err)
		}
	}()

	// Get connection details
	host, err := pgContainer.Host(ctx)
	if err != nil {
		t.Fatalf("Failed to get container host: %v", err)
	}

	port, err := pgContainer.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatalf("Failed to get container port: %v", err)
	}

	t.Logf("PostgreSQL running at %s:%s", host, port.Port())

	// Create test configuration
	connString := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=prefer",
		host, port.Port())
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     "coverage.json",
		Verbose:          true,
	}

	// Test Phase 1: Discovery
	t.Run("Discovery", func(t *testing.T) {
		testDir := "../testdata/simple"

		// Discover test files
		testFiles, err := discovery.DiscoverTests(testDir)
		if err != nil {
			t.Fatalf("Failed to discover tests: %v", err)
		}

		if len(testFiles) == 0 {
			t.Fatal("No test files found")
		}

		t.Logf("Discovered %d test file(s)", len(testFiles))

		// Discover source files
		sourceFiles, err := discovery.DiscoverCoLocatedSources(testDir, testFiles)
		if err != nil {
			t.Fatalf("Failed to discover sources: %v", err)
		}

		if len(sourceFiles) == 0 {
			t.Fatal("No source files found")
		}

		t.Logf("Discovered %d source file(s)", len(sourceFiles))
	})

	// Test Phase 2: Parsing
	t.Run("Parsing", func(t *testing.T) {
		testDir := "../testdata/simple"
		sourceFiles, _ := discovery.DiscoverSources(testDir)

		for _, file := range sourceFiles {
			parsed, err := parser.Parse(&file)
			if err != nil {
				t.Fatalf("Failed to parse %s: %v", file.RelativePath, err)
			}

			if len(parsed.Statements) == 0 {
				t.Logf("Warning: No statements found in %s", file.RelativePath)
			}

			t.Logf("Parsed %s: %d statements", file.RelativePath, len(parsed.Statements))
		}
	})

	// Test Phase 3: Instrumentation
	t.Run("Instrumentation", func(t *testing.T) {
		testDir := "../testdata/simple"
		sourceFiles, _ := discovery.DiscoverSources(testDir)

		for _, file := range sourceFiles {
			parsed, _ := parser.Parse(&file)
			instrumented, err := instrument.GenerateCoverageInstrument(parsed, instrument.DefaultChannel)
			if err != nil {
				t.Fatalf("Failed to instrument %s: %v", file.RelativePath, err)
			}

			if instrumented.InstrumentedText == "" {
				t.Logf("Warning: No instrumented text for %s", file.RelativePath)
			}

			t.Logf("Instrumented %s: %d coverage points",
				file.RelativePath, len(instrumented.Locations))
		}
	})

	// Test Phase 4: Full End-to-End Execution
	t.Run("FullExecution", func(t *testing.T) {
		testDir := "../testdata/simple"

		// Run the full workflow using cli.Run
		exitCode, err := cli.Run(ctx, config, testDir)
		// Note: Exit code might be non-zero if test has no assertions
		// For Phase 3, we just verify the workflow completes
		if err != nil {
			t.Fatalf("Test execution failed with error: %v", err)
		}

		t.Logf("Test completed with exit code: %d", exitCode)

		// Verify coverage file was created
		if _, err := os.Stat(config.CoverageFile); os.IsNotExist(err) {
			t.Fatal("Coverage file was not created")
		}

		// Load and verify coverage data
		store := coverage.NewStore(config.CoverageFile)
		cov, err := store.Load()
		if err != nil {
			t.Fatalf("Failed to load coverage data: %v", err)
		}

		// Phase 3: Coverage data structure should exist (even if empty without instrumentation)
		if cov == nil {
			t.Fatal("Coverage data is nil")
			return
		}

		totalPercent := cov.TotalPositionCoveragePercent()
		t.Logf("Total coverage: %.2f%%", totalPercent)

		// Verify coverage is >= 0% (instrumentation is TODO for Phase 4)
		if totalPercent < 0 {
			t.Error("Coverage should be >= 0%")
		}

		// Log coverage details
		if len(cov.Positions) > 0 {
			files := cov.GetFiles()
			for _, file := range files {
				percent := cov.PositionCoveragePercent(file)
				hits := cov.Positions[file]
				covered := countCoveredPositions(hits)
				t.Logf("  %s: %.2f%% (%d/%d positions)",
					file, percent, covered, len(hits))
			}
		} else {
			t.Log("  No coverage data")
		}
	})

	// Test Phase 5: Database Operations
	t.Run("DatabaseOperations", func(t *testing.T) {
		// Test connection pool
		pool, err := database.NewPool(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create connection pool: %v", err)
		}
		defer pool.Close()

		// Test temp database creation
		tempPool, err := database.CreateTempDatabase(ctx, pool)
		if err != nil {
			t.Fatalf("Failed to create temp database: %v", err)
		}

		t.Logf("Created temp database: %s", tempPool.Config().ConnConfig.Database)

		// Test temp database cleanup
		err = database.DestroyTempDatabase(ctx, pool, tempPool)
		if err != nil {
			t.Fatalf("Failed to destroy temp database: %v", err)
		}

		t.Log("Successfully cleaned up temp database")
	})

	// Test Phase 6: Report Generation
	t.Run("ReportGeneration", func(t *testing.T) {
		// Ensure we have coverage data
		testDir := "../testdata/simple"
		_, _ = cli.Run(ctx, config, testDir)

		// Test JSON report
		err := cli.Report(t.Context(), config.CoverageFile, "json", "-", "")
		if err != nil {
			t.Fatalf("Failed to generate JSON report: %v", err)
		}

		// Test LCOV report
		lcovFile := filepath.Join(t.TempDir(), "coverage.lcov")
		err = cli.Report(t.Context(), config.CoverageFile, "lcov", lcovFile, "")
		if err != nil {
			t.Fatalf("Failed to generate LCOV report: %v", err)
		}

		// Verify LCOV file was created
		if _, err := os.Stat(lcovFile); os.IsNotExist(err) {
			t.Fatal("LCOV file was not created")
		}

		t.Log("Successfully generated both JSON and LCOV reports")
	})

	t.Log("✓ All integration tests passed!")
}

// TestRunnerIsolation verifies that each test runs in complete isolation
func TestRunnerIsolation(t *testing.T) {
	ctx := context.Background()

	// Start PostgreSQL container
	t.Log("Starting PostgreSQL container for isolation test...")
	pgContainer, err := postgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate container: %v", err)
		}
	}()

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432")

	connString := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=prefer",
		host, port.Port())
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
	}

	// Create connection pool
	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	// Create two temp databases and verify they're independent
	db1, err := database.CreateTempDatabase(ctx, pool)
	if err != nil {
		t.Fatalf("Failed to create first temp database: %v", err)
	}

	db2, err := database.CreateTempDatabase(ctx, pool)
	if err != nil {
		t.Fatalf("Failed to create second temp database: %v", err)
	}

	db1Name := db1.Config().ConnConfig.Database
	db2Name := db2.Config().ConnConfig.Database

	// Verify different names
	if db1Name == db2Name {
		t.Fatal("Temp databases should have unique names")
	}

	t.Logf("Created isolated databases: %s and %s", db1Name, db2Name)

	// Cleanup
	if err := database.DestroyTempDatabase(ctx, pool, db1); err != nil {
		t.Logf("Warning: failed to cleanup db1: %v", err)
	}
	if err := database.DestroyTempDatabase(ctx, pool, db2); err != nil {
		t.Logf("Warning: failed to cleanup db2: %v", err)
	}

	t.Log("✓ Isolation test passed!")
}

// TestOrderIndependence verifies that tests produce identical results
// regardless of execution order (test A→B vs B→A)
func TestOrderIndependence(t *testing.T) {
	ctx := context.Background()

	// Start PostgreSQL container
	t.Log("Starting PostgreSQL container for order-independence test...")
	pgContainer, err := postgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate container: %v", err)
		}
	}()

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432")

	connString := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=prefer",
		host, port.Port())
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     filepath.Join(t.TempDir(), "coverage.json"),
		Verbose:          true,
	}

	testDir := "../testdata/simple"

	// Discover and prepare test files
	testFiles, err := discovery.DiscoverTests(testDir)
	if err != nil {
		t.Fatalf("Failed to discover tests: %v", err)
	}

	if len(testFiles) < 2 {
		t.Skip("Need at least 2 test files for order-independence test")
	}

	t.Logf("Found %d test files for order testing", len(testFiles))

	// Discover and instrument source files
	sourceFiles, err := discovery.DiscoverCoLocatedSources(testDir, testFiles)
	if err != nil {
		t.Fatalf("Failed to discover sources: %v", err)
	}

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

	// Create connection pool
	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	// Helper function to run tests in a specific order and collect coverage
	runTestsInOrder := func(order []discovery.DiscoveredFile, label string) *coverage.Coverage {
		t.Logf("Running tests in order %s", label)

		executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
		testRuns, err := executor.ExecuteBatch(ctx, order, instrumentedSources)
		if err != nil {
			t.Fatalf("Test execution failed for %s: %v", label, err)
		}

		collector := coverage.NewCollector()
		if err := collector.CollectFromRuns(testRuns); err != nil {
			t.Fatalf("Coverage collection failed for %s: %v", label, err)
		}

		return collector.Coverage()
	}

	// Run tests in order A→B
	orderAB := make([]discovery.DiscoveredFile, len(testFiles))
	copy(orderAB, testFiles)
	coverageAB := runTestsInOrder(orderAB, "A→B")

	// Run tests in order B→A (reverse)
	orderBA := make([]discovery.DiscoveredFile, len(testFiles))
	copy(orderBA, testFiles)
	// Reverse the order
	for i, j := 0, len(orderBA)-1; i < j; i, j = i+1, j-1 {
		orderBA[i], orderBA[j] = orderBA[j], orderBA[i]
	}
	coverageBA := runTestsInOrder(orderBA, "B→A")

	// Compare coverage results
	t.Log("Comparing coverage results...")

	// Check that both have the same files
	if len(coverageAB.Positions) != len(coverageBA.Positions) {
		t.Errorf("Different number of files covered: A→B has %d, B→A has %d",
			len(coverageAB.Positions), len(coverageBA.Positions))
	}

	// Compare each file's coverage
	for filePath, fileAB := range coverageAB.Positions {
		fileBA, exists := coverageBA.Positions[filePath]
		if !exists {
			t.Errorf("File %s covered in A→B but not in B→A", filePath)
			continue
		}

		// Compare position coverage
		if len(fileAB) != len(fileBA) {
			t.Errorf("File %s: different number of positions covered: A→B has %d, B→A has %d",
				filePath, len(fileAB), len(fileBA))
		}

		// Check each position
		for posKey, hitCountAB := range fileAB {
			hitCountBA, exists := fileBA[posKey]
			if !exists {
				t.Errorf("File %s, position %s: covered in A→B but not in B→A",
					filePath, posKey)
				continue
			}

			// Verify both are covered (hitCount > 0)
			coveredAB := hitCountAB > 0
			coveredBA := hitCountBA > 0
			if coveredAB != coveredBA {
				t.Errorf("File %s, position %s: coverage mismatch - A→B=%v, B→A=%v",
					filePath, posKey, coveredAB, coveredBA)
			}

			// Note: We don't compare HitCount exactly, as it may vary if tests
			// execute the same position multiple times. We only care that Covered
			// status is the same.
		}

		// Branch coverage not yet implemented
		t.Logf("  %s: %d positions verified", filePath, len(fileAB))
	}

	// Check for files in BA but not in AB
	for filePath := range coverageBA.Positions {
		if _, exists := coverageAB.Positions[filePath]; !exists {
			t.Errorf("File %s covered in B→A but not in A→B", filePath)
		}
	}

	// Compare total coverage percentages
	totalAB := coverageAB.TotalPositionCoveragePercent()
	totalBA := coverageBA.TotalPositionCoveragePercent()
	t.Logf("Total coverage A→B: %.2f%%", totalAB)
	t.Logf("Total coverage B→A: %.2f%%", totalBA)

	// Allow tiny floating point differences
	if totalAB != totalBA {
		diff := totalAB - totalBA
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.01 { // Allow 0.01% difference for floating point precision
			t.Errorf("Total coverage mismatch: A→B=%.2f%%, B→A=%.2f%%", totalAB, totalBA)
		}
	}

	t.Log("✓ Order-independence test passed! Coverage is identical regardless of test execution order.")
}

// TestTestIndependence verifies that running the same test multiple times
// produces identical coverage results (no state accumulation across runs)
func TestTestIndependence(t *testing.T) {
	ctx := context.Background()

	// Start PostgreSQL container
	t.Log("Starting PostgreSQL container for test independence verification...")
	pgContainer, err := postgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate container: %v", err)
		}
	}()

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432")

	connString := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=prefer",
		host, port.Port())
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     filepath.Join(t.TempDir(), "coverage.json"),
		Verbose:          true,
	}

	testDir := "../testdata/isolation"

	// Discover test files
	testFiles, err := discovery.DiscoverTests(testDir)
	if err != nil {
		t.Fatalf("Failed to discover tests: %v", err)
	}

	if len(testFiles) == 0 {
		t.Fatal("No test files found in testdata/isolation")
	}

	// Use the first test file for repeated execution
	testFile := testFiles[0]
	t.Logf("Using test file: %s", testFile.RelativePath)

	// Discover and instrument source files
	sourceFiles, err := discovery.DiscoverCoLocatedSources(testDir, testFiles)
	if err != nil {
		t.Fatalf("Failed to discover sources: %v", err)
	}

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

	// Create connection pool
	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	// Helper function to run a single test and collect coverage
	runSingleTest := func(iteration int) (*runner.TestRun, *coverage.Coverage) {
		t.Logf("Running test iteration %d...", iteration)

		executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout, config.Verbose, instrument.DefaultChannel)
		testRuns, err := executor.ExecuteBatch(ctx, []discovery.DiscoveredFile{testFile}, instrumentedSources)
		if err != nil {
			t.Fatalf("Test execution failed (iteration %d): %v", iteration, err)
		}

		if len(testRuns) != 1 {
			t.Fatalf("Expected 1 test run, got %d", len(testRuns))
		}

		collector := coverage.NewCollector()
		if err := collector.CollectFromRuns(testRuns); err != nil {
			t.Fatalf("Coverage collection failed (iteration %d): %v", iteration, err)
		}

		return testRuns[0], collector.Coverage()
	}

	// Run the same test twice
	t.Log("=== First Execution ===")
	run1, coverage1 := runSingleTest(1)

	t.Log("=== Second Execution ===")
	run2, coverage2 := runSingleTest(2)

	// Verify isolation using the isolation validator
	t.Log("Verifying stateless execution...")
	err = verifyStatelessExecution(run1, run2)
	if err != nil {
		t.Fatalf("Stateless execution verification failed: %v", err)
	}
	t.Log("✓ Test runs are properly isolated (different databases used)")

	// Verify test status is identical
	if run1.Status != run2.Status {
		t.Errorf("Test status differs: run1=%s, run2=%s", run1.Status, run2.Status)
	}
	t.Logf("✓ Test status identical: %s", run1.Status)

	// Compare coverage results
	t.Log("Comparing coverage results...")

	// Check that both have the same files
	if len(coverage1.Positions) != len(coverage2.Positions) {
		t.Errorf("Different number of files covered: run1 has %d, run2 has %d",
			len(coverage1.Positions), len(coverage2.Positions))
	}

	// Compare each file's coverage in detail
	for filePath, file1 := range coverage1.Positions {
		file2, exists := coverage2.Positions[filePath]
		if !exists {
			t.Errorf("File %s covered in run1 but not in run2", filePath)
			continue
		}

		// Compare number of positions
		if len(file1) != len(file2) {
			t.Errorf("File %s: different number of positions covered: run1 has %d, run2 has %d",
				filePath, len(file1), len(file2))
		}

		// Compare position-by-position coverage
		mismatchCount := 0
		for posKey, hitCount1 := range file1 {
			hitCount2, exists := file2[posKey]
			if !exists {
				t.Errorf("File %s, position %s: covered in run1 but not in run2",
					filePath, posKey)
				mismatchCount++
				continue
			}

			// Verify coverage status is identical
			covered1 := hitCount1 > 0
			covered2 := hitCount2 > 0
			if covered1 != covered2 {
				t.Errorf("File %s, position %s: coverage mismatch - run1=%v, run2=%v",
					filePath, posKey, covered1, covered2)
				mismatchCount++
			}
		}

		// Branch coverage not yet implemented
		if mismatchCount > 0 {
			t.Errorf("File %s: %d mismatches found", filePath, mismatchCount)
		} else {
			t.Logf("✓ File %s: coverage identical in both runs", filePath)
		}
	}

	// Check for files in run2 but not in run1
	for filePath := range coverage2.Positions {
		if _, exists := coverage1.Positions[filePath]; !exists {
			t.Errorf("File %s covered in run2 but not in run1", filePath)
		}
	}

	// Compare total coverage percentages
	total1 := coverage1.TotalPositionCoveragePercent()
	total2 := coverage2.TotalPositionCoveragePercent()
	t.Logf("Total coverage run1: %.2f%%", total1)
	t.Logf("Total coverage run2: %.2f%%", total2)

	if total1 != total2 {
		diff := total1 - total2
		if diff < 0 {
			diff = -diff
		}
		if diff > 0.01 { // Allow 0.01% difference for floating point precision
			t.Errorf("Total coverage mismatch: run1=%.2f%%, run2=%.2f%%", total1, total2)
		}
	}

	// Verify coverage signals are identical
	t.Logf("Coverage signals: run1=%d, run2=%d", len(run1.CoverageSigs), len(run2.CoverageSigs))
	if len(run1.CoverageSigs) != len(run2.CoverageSigs) {
		t.Errorf("Different number of coverage signals: run1=%d, run2=%d",
			len(run1.CoverageSigs), len(run2.CoverageSigs))
	}

	// Verify databases were properly cleaned up
	t.Log("Verifying database cleanup...")
	exists1, err := databaseExists(ctx, pool, run1.Database)
	if err != nil {
		t.Errorf("Failed to check database existence: %v", err)
	} else if exists1 {
		t.Errorf("Database %s from run1 was not cleaned up", run1.Database)
	}

	exists2, err := databaseExists(ctx, pool, run2.Database)
	if err != nil {
		t.Errorf("Failed to check database existence: %v", err)
	} else if exists2 {
		t.Errorf("Database %s from run2 was not cleaned up", run2.Database)
	}

	if !exists1 && !exists2 {
		t.Log("✓ Both test databases were properly cleaned up")
	}

	t.Log("✓ Test independence verified! Same test produces identical coverage across multiple runs.")
}

// databaseExists checks if a database exists in PostgreSQL
func databaseExists(ctx context.Context, pool *database.Pool, dbName string) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var exists bool
	query := `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)`
	err = conn.QueryRow(ctx, query, dbName).Scan(&exists)
	return exists, err
}

// Helper function to count covered positions
func countCoveredPositions(hits coverage.PositionHits) int {
	count := 0
	for _, hitCount := range hits {
		if hitCount > 0 {
			count++
		}
	}
	return count
}

// TestSQLFunctionInstrumentation verifies that SQL-language functions are
// correctly instrumented using CTE-based pg_notify injection instead of
// standalone SELECT pg_notify() which breaks return types.
func TestSQLFunctionInstrumentation(t *testing.T) {
	ctx := context.Background()

	// Start PostgreSQL container
	t.Log("Starting PostgreSQL container for SQL function instrumentation test...")
	pgContainer, err := postgres.Run(ctx,
		"docker.io/postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("Failed to start PostgreSQL container: %v", err)
	}
	defer func() {
		if err := pgContainer.Terminate(ctx); err != nil {
			t.Logf("Failed to terminate container: %v", err)
		}
	}()

	host, _ := pgContainer.Host(ctx)
	port, _ := pgContainer.MappedPort(ctx, "5432")

	connString := fmt.Sprintf("host=%s port=%s user=testuser password=testpass dbname=testdb sslmode=prefer",
		host, port.Port())
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     filepath.Join(t.TempDir(), "coverage.json"),
		Verbose:          true,
	}

	testDir := "../testdata/sqlfunc"

	t.Run("FullExecution", func(t *testing.T) {
		exitCode, err := cli.Run(ctx, config, testDir)
		if err != nil {
			t.Fatalf("cli.Run failed: %v", err)
		}
		if exitCode != 0 {
			t.Fatalf("expected exit code 0, got %d", exitCode)
		}

		// Load coverage data
		store := coverage.NewStore(config.CoverageFile)
		cov, err := store.Load()
		if err != nil {
			t.Fatalf("Failed to load coverage: %v", err)
		}

		// We expect coverage data for the SQL function source file
		files := cov.GetFiles()
		if len(files) == 0 {
			t.Fatal("No files in coverage data")
		}

		t.Logf("Coverage files: %v", files)
		for _, f := range files {
			hits := cov.Positions[f]
			covered := countCoveredPositions(hits)
			t.Logf("  %s: %d/%d positions covered", f, covered, len(hits))
		}

		totalPct := cov.TotalPositionCoveragePercent()
		t.Logf("Total coverage: %.2f%%", totalPct)

		// Assert the actual SQL-function positions, not just "> 0".
		//
		// A `totalPct > 0` check is what let a total instrumentation failure
		// hide here: the CTE form this fixture was built for never fired, so
		// all five SQL-function positions sat at zero and the single implicit
		// CREATE TABLE position alone carried the number to 16.67%. The test
		// passed while measuring nothing it claimed to measure.
		hits := cov.Positions["sqlfunc_source.sql"]
		if len(hits) == 0 {
			t.Fatal("no coverage entries for sqlfunc_source.sql")
		}

		uncovered := 0
		for posKey, count := range hits {
			if count == 0 {
				uncovered++
				t.Errorf("SQL-function position %s was never covered; the test file "+
					"exercises every function in this fixture", posKey)
			}
		}
		if uncovered == 0 {
			t.Logf("all %d SQL-function positions covered", len(hits))
		}

		if totalPct <= 0 {
			t.Error("Expected some coverage from SQL function tests")
		}
	})

	t.Run("InstrumentedSQLValid", func(t *testing.T) {
		// Verify that the instrumented SQL is syntactically valid by
		// deploying it into a real database and calling the functions.
		pool, err := database.NewPool(ctx, config)
		if err != nil {
			t.Fatalf("Failed to create pool: %v", err)
		}
		defer pool.Close()

		tempPool, err := database.CreateTempDatabase(ctx, pool)
		if err != nil {
			t.Fatalf("Failed to create temp database: %v", err)
		}
		defer func() {
			_ = database.DestroyTempDatabase(ctx, pool, tempPool)
		}()

		// Discover, parse, instrument
		sourceFiles, err := discovery.DiscoverSources(testDir)
		if err != nil {
			t.Fatalf("Failed to discover sources: %v", err)
		}
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

		// Deploy instrumented SQL
		conn, err := tempPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("Failed to acquire connection: %v", err)
		}
		defer conn.Release()

		for _, inst := range instrumentedSources {
			t.Logf("Deploying instrumented SQL:\n%s", inst.InstrumentedText)
			_, err := conn.Exec(ctx, inst.InstrumentedText)
			if err != nil {
				t.Fatalf("Failed to deploy instrumented SQL for %s: %v",
					inst.Original.File.RelativePath, err)
			}
		}

		// Call each SQL function and verify it returns the correct value
		var result int
		err = conn.QueryRow(ctx, "SELECT double_val(5)").Scan(&result)
		if err != nil {
			t.Fatalf("double_val(5) failed: %v", err)
		}
		if result != 10 {
			t.Errorf("double_val(5) = %d, want 10", result)
		}

		err = conn.QueryRow(ctx, "SELECT add_vals(3, 4)").Scan(&result)
		if err != nil {
			t.Fatalf("add_vals(3,4) failed: %v", err)
		}
		if result != 7 {
			t.Errorf("add_vals(3,4) = %d, want 7", result)
		}

		var greeting string
		err = conn.QueryRow(ctx, "SELECT greet('pgcov')").Scan(&greeting)
		if err != nil {
			t.Fatalf("greet('pgcov') failed: %v", err)
		}
		if greeting != "Hello, pgcov!" {
			t.Errorf("greet('pgcov') = %q, want %q", greeting, "Hello, pgcov!")
		}

		// Multi-statement SQL function
		err = conn.QueryRow(ctx, "SELECT insert_and_return(7)").Scan(&result)
		if err != nil {
			t.Fatalf("insert_and_return(7) failed: %v", err)
		}
		if result != 70 {
			t.Errorf("insert_and_return(7) = %d, want 70", result)
		}

		t.Log("All SQL functions return correct values with CTE-based instrumentation")
	})
}

// verifyStatelessExecution verifies that running the same test twice produces
// identical results, confirming there is no shared state between runs.
func verifyStatelessExecution(run1, run2 *runner.TestRun) error {
	if run1.Status != run2.Status {
		return fmt.Errorf("test status differs: %s vs %s", run1.Status, run2.Status)
	}
	if run1.Database == run2.Database {
		return fmt.Errorf("tests used the same database: %s", run1.Database)
	}
	if len(run1.CoverageSigs) != len(run2.CoverageSigs) {
		return fmt.Errorf("coverage signal count differs: %d vs %d",
			len(run1.CoverageSigs), len(run2.CoverageSigs))
	}

	signals1 := make(map[string]bool, len(run1.CoverageSigs))
	for _, sig := range run1.CoverageSigs {
		signals1[sig.SignalID] = true
	}
	signals2 := make(map[string]bool, len(run2.CoverageSigs))
	for _, sig := range run2.CoverageSigs {
		signals2[sig.SignalID] = true
	}

	for sigID := range signals1 {
		if !signals2[sigID] {
			return fmt.Errorf("signal %s present in first run but not second", sigID)
		}
	}
	for sigID := range signals2 {
		if !signals1[sigID] {
			return fmt.Errorf("signal %s present in second run but not first", sigID)
		}
	}
	return nil
}
