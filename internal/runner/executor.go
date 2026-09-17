package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
)

// Executor orchestrates test execution with coverage tracking
type Executor struct {
	pool            *database.Pool
	timeout         time.Duration
	signalTimeout   time.Duration
	verbose         bool
	coverageChannel string

	// setupSQL holds prerequisite SQL scripts (in order) executed verbatim in
	// each temp database before the instrumented sources are loaded. They are
	// NOT instrumented and do NOT contribute to coverage.
	setupSQL []SetupScript
}

// SetupScript is a named chunk of prerequisite SQL run before sources load.
// Name is used only for diagnostics (typically the file path).
type SetupScript struct {
	Name string
	SQL  string
}

// DefaultSignalTimeout is the grace period for in-flight NOTIFY signals used
// when NewExecutor receives a non-positive signalTimeout (e.g. a Config built
// without the CLI defaults).
const DefaultSignalTimeout = 100 * time.Millisecond

// NewExecutor creates a new test executor.  coverageChannel is the PostgreSQL
// NOTIFY channel name used both for the instrumented sources (pg_notify calls)
// and for the LISTEN side; both must agree or no signals will be received.
// signalTimeout is the grace period for in-flight NOTIFY signals after test SQL
// finishes executing; a non-positive value falls back to DefaultSignalTimeout.
func NewExecutor(pool *database.Pool, timeout time.Duration, signalTimeout time.Duration, verbose bool, coverageChannel string) *Executor {
	if signalTimeout <= 0 {
		signalTimeout = DefaultSignalTimeout
	}
	return &Executor{
		pool:            pool,
		timeout:         timeout,
		signalTimeout:   signalTimeout,
		verbose:         verbose,
		coverageChannel: coverageChannel,
	}
}

// SetSetupScripts registers prerequisite SQL scripts to run (in order) in every
// test's temp database before the instrumented sources are loaded.
func (e *Executor) SetSetupScripts(scripts []SetupScript) {
	e.setupSQL = scripts
}

// Execute runs a single test file and collects coverage.
//
// It never fails at the Go level: every problem encountered while running the
// test - temp-database creation, source loading, the test SQL itself - is
// recorded on the returned TestRun as Status/Error rather than returned
// separately. TestRun.Error is therefore the single channel through which a
// caller learns why a test did not pass.
//
// sourceFiles may contain sources from any directory; Execute narrows them to
// the ones co-located with testFile before loading anything. Doing this here
// rather than in the callers is deliberate - it is the single choke point both
// ExecuteBatch and WorkerPool.worker funnel through, so sequential and parallel
// runs cannot diverge in which sources they load.
func (e *Executor) Execute(ctx context.Context, testFile *discovery.DiscoveredFile, sourceFiles []*instrument.InstrumentedSQL) *TestRun {
	testRun := &TestRun{
		Test:      testFile,
		StartTime: time.Now(),
		Status:    TestPending,
	}

	// Narrow the sources to those co-located with this test.
	sourceFiles = filterSourcesByDirectory(sourceFiles, filepath.Dir(testFile.Path))

	// Create context with timeout
	testCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	// Execute the per-test workflow
	err := e.executeTestWorkflow(testCtx, testRun, sourceFiles)
	if err != nil {
		testRun.Status = TestFailed
		testRun.Error = err
		if e.verbose {
			fmt.Printf("[ERROR] Test failed: %v\n", err)
		}
	} else {
		testRun.Status = TestPassed
	}

	testRun.EndTime = time.Now()

	return testRun
}

// ExecuteBatch runs multiple tests sequentially
func (e *Executor) ExecuteBatch(ctx context.Context, testFiles []discovery.DiscoveredFile, sourceFiles []*instrument.InstrumentedSQL) ([]*TestRun, error) {
	var runs []*TestRun

	for i := range testFiles {
		if e.verbose {
			fmt.Printf("Running test: %s\n", testFiles[i].RelativePath)
		}

		run := e.Execute(ctx, &testFiles[i], sourceFiles)
		runs = append(runs, run)

		// Check if context was cancelled
		if ctx.Err() != nil {
			break
		}
	}

	return runs, nil
}

// filterSourcesByDirectory returns only source files from the specified directory
func filterSourcesByDirectory(sources []*instrument.InstrumentedSQL, testDir string) []*instrument.InstrumentedSQL {
	var filtered []*instrument.InstrumentedSQL
	for _, src := range sources {
		sourceDir := filepath.Dir(src.Original.File.Path)
		if sourceDir == testDir {
			filtered = append(filtered, src)
		}
	}
	return filtered
}

// SummarizeRuns creates a summary of test execution results
func SummarizeRuns(runs []*TestRun) *TestSummary {
	summary := &TestSummary{
		TotalTests: len(runs),
	}

	var totalDuration time.Duration

	for _, run := range runs {
		totalDuration += run.Duration()

		switch run.Status {
		case TestPassed:
			summary.PassedTests++
		case TestFailed:
			summary.FailedTests++
		case TestTimeout:
			summary.TimedOutTests++
		}
	}

	summary.TotalDuration = totalDuration

	return summary
}

// executeTestWorkflow implements the per-test workflow:
// 1. Create temp database
// 2. Load instrumented source code
// 3. Start LISTEN for coverage signals
// 4. Run test
// 5. Collect coverage signals
// 6. Destroy temp database
func (e *Executor) executeTestWorkflow(ctx context.Context, testRun *TestRun, sourceFiles []*instrument.InstrumentedSQL) error {
	if e.verbose {
		fmt.Println("[DEBUG] Step 1: Creating temp database...")
	}
	// Step 1: Create temporary database
	tempPool, err := database.CreateTempDatabase(ctx, e.pool)
	if err != nil {
		return fmt.Errorf("failed to create temp database: %w", err)
	}
	testRun.Database = tempPool.Config().ConnConfig.Database
	if e.verbose {
		fmt.Printf("[DEBUG] Created temp database: %s\n", testRun.Database)
	}

	// Ensure cleanup
	defer func() {
		if e.verbose {
			fmt.Println("[DEBUG] Cleaning up temp database...")
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = database.DestroyTempDatabase(cleanupCtx, e.pool, tempPool)
	}()

	if e.verbose {
		fmt.Println("[DEBUG] Step 2: Connecting to temp database...")
		fmt.Println("[DEBUG] Connected to temp database")
	}

	if e.verbose {
		fmt.Println("[DEBUG] Step 3: Starting LISTEN for coverage signals...")
	}
	// Step 3: Start LISTEN for coverage signals
	listener, err := database.NewListener(ctx, tempPool, e.coverageChannel)
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	defer listener.Close(ctx)
	if e.verbose {
		fmt.Println("[DEBUG] Listener started")
	}

	// Step 3b: Run prerequisite setup scripts verbatim (uninstrumented). These
	// create schema/objects the sources under test depend on but that live
	// outside the test directory. They run before sources load and do not
	// contribute to coverage.
	if len(e.setupSQL) > 0 {
		if e.verbose {
			fmt.Printf("[DEBUG] Step 3b: Running %d setup script(s)...\n", len(e.setupSQL))
		}
		setupConn, err := tempPool.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("failed to acquire connection for setup: %w", err)
		}
		for _, s := range e.setupSQL {
			if e.verbose {
				fmt.Printf("[DEBUG] Running setup script: %s\n", s.Name)
			}
			if _, err := setupConn.Exec(ctx, s.SQL); err != nil {
				setupConn.Release()
				return fmt.Errorf("failed to run setup script %s: %w", s.Name, err)
			}
		}
		setupConn.Release()
	}

	if e.verbose {
		fmt.Println("[DEBUG] Step 4: Loading instrumented source code...")
	}
	// Step 4: Load instrumented source code
	conn, err := tempPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}

	for _, source := range sourceFiles {
		if e.verbose {
			fmt.Printf("[DEBUG] Loading source: %s\n", source.Original.File.RelativePath)
		}
		_, err := conn.Exec(ctx, source.InstrumentedText)
		if err != nil {
			conn.Release()
			if e.verbose {
				fmt.Printf("[DEBUG] Failed to load source: %v\n", err)
				fmt.Printf("[DEBUG] Instrumented SQL was:\n%s\n", source.InstrumentedText)
			}
			return fmt.Errorf("failed to load source %s: %w", source.Original.File.RelativePath, err)
		}

		// For successfully loaded source files, mark DDL/DML locations as implicitly covered
		// (PL/pgSQL code coverage is tracked via NOTIFY signals during execution)
		for _, loc := range source.Locations {
			if loc.ImplicitCoverage {
				testRun.CoverageSigs = append(testRun.CoverageSigs, CoverageSignal{
					SignalID:  loc.SignalID,
					Timestamp: time.Now(),
				})
			}
		}
	}
	conn.Release()
	if e.verbose {
		fmt.Println("[DEBUG] All sources loaded")
		fmt.Printf("[DEBUG] Added %d implicit coverage signals from DDL/DML\n", len(testRun.CoverageSigs))
	}

	if e.verbose {
		fmt.Println("[DEBUG] Step 5: Reading test file...")
	}
	// Step 5: Run test file
	testContent, err := os.ReadFile(testRun.Test.Path)
	if err != nil {
		return fmt.Errorf("failed to read test file: %w", err)
	}
	if e.verbose {
		fmt.Printf("[DEBUG] Test file read: %d bytes\n", len(testContent))
	}

	testRun.Status = TestRunning

	if e.verbose {
		fmt.Println("[DEBUG] Step 6: Executing test SQL...")
	}
	conn, err = tempPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection for test: %w", err)
	}
	defer conn.Release()

	// Execute test SQL
	_, err = conn.Exec(ctx, string(testContent))
	if err != nil {
		return fmt.Errorf("test execution failed: %w", err)
	}
	if e.verbose {
		fmt.Println("[DEBUG] Test SQL executed successfully")
	}

	if e.verbose {
		fmt.Println("[DEBUG] Step 7: Collecting coverage signals...")
	}
	// Give a short time for any remaining signals to arrive
	signals, err := listener.CollectSignals(ctx, e.signalTimeout)
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		return fmt.Errorf("failed to collect signals: %w", err)
	}
	if e.verbose {
		fmt.Printf("[DEBUG] Collected %d signals\n", len(signals))
	}

	// Check for dropped signals — warns about incomplete coverage data
	if dropped := listener.DroppedSignals(); dropped > 0 {
		fmt.Printf("[WARNING] %d coverage signal(s) dropped for %s (buffer full) — coverage data is incomplete\n",
			dropped, testRun.Test.RelativePath)
	}

	// Append NOTIFY signals to the implicit coverage signals
	testRun.CoverageSigs = append(testRun.CoverageSigs, signals...)

	return nil
}
