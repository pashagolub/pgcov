package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
)

// WorkerPool manages parallel test execution
type WorkerPool struct {
	executor   *Executor
	maxWorkers int
	verbose    bool
}

// NewWorkerPool creates a new worker pool for parallel test execution
func NewWorkerPool(executor *Executor, maxWorkers int, verbose bool) *WorkerPool {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	return &WorkerPool{
		executor:   executor,
		maxWorkers: maxWorkers,
		verbose:    verbose,
	}
}

// ExecuteParallel runs multiple tests in parallel with the configured concurrency limit
func (wp *WorkerPool) ExecuteParallel(ctx context.Context, testFiles []discovery.DiscoveredFile, sourceFiles []*instrument.InstrumentedSQL) ([]*TestRun, error) {
	numTests := len(testFiles)
	if numTests == 0 {
		return nil, nil
	}

	// If only one worker or one test, fall back to sequential execution
	if wp.maxWorkers == 1 || numTests == 1 {
		return wp.executor.ExecuteBatch(ctx, testFiles, sourceFiles)
	}

	if wp.verbose {
		fmt.Printf("Starting parallel execution with %d workers for %d tests\n", wp.maxWorkers, numTests)
	}

	// Create buffered channels for job distribution and result collection
	jobs := make(chan *testJob, numTests)
	results := make(chan *testResult, numTests)

	// Start worker goroutines
	var wg sync.WaitGroup
	for i := 0; i < wp.maxWorkers; i++ {
		wg.Add(1)
		go wp.worker(ctx, i, jobs, results, &wg, sourceFiles)
	}

	// Send all test jobs to the jobs channel
	for i := range testFiles {
		jobs <- &testJob{
			testFile: &testFiles[i],
			index:    i,
		}
	}
	close(jobs)

	// Wait for all workers to complete in a separate goroutine
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results from the results channel
	testRuns := make([]*TestRun, numTests)
	for result := range results {
		testRuns[result.index] = result.run
		if wp.verbose {
			var status string
			switch result.run.Status {
			case TestFailed:
				status = "FAIL"
			case TestTimeout:
				status = "TIMEOUT"
			default:
				status = "PASS"
			}
			fmt.Printf("[%s] %s (worker %d)\n", status, result.run.Test.RelativePath, result.workerID)
		}
	}

	return testRuns, nil
}

// testJob represents a single test to execute
type testJob struct {
	testFile *discovery.DiscoveredFile
	index    int
}

// testResult represents the result of a test execution
type testResult struct {
	run      *TestRun
	index    int
	workerID int
}

// worker is the goroutine that processes test jobs
func (wp *WorkerPool) worker(ctx context.Context, workerID int, jobs <-chan *testJob, results chan<- *testResult, wg *sync.WaitGroup, sourceFiles []*instrument.InstrumentedSQL) {
	defer wg.Done()

	for job := range jobs {
		// Check if context was cancelled before starting the test
		if ctx.Err() != nil {
			// Create a failed test run for cancelled tests
			testRun := &TestRun{
				Test:      job.testFile,
				StartTime: time.Now(),
				EndTime:   time.Now(),
				Status:    TestFailed,
				Error:     ctx.Err(),
			}
			results <- &testResult{
				run:      testRun,
				index:    job.index,
				workerID: workerID,
			}
			continue
		}

		if wp.verbose {
			fmt.Printf("Worker %d: Running test %s\n", workerID, job.testFile.RelativePath)
		}

		// Execute the test. Execute always returns a non-nil TestRun; a
		// failure is reported through run.Status/run.Error, not a Go error.
		run := wp.executor.Execute(ctx, job.testFile, sourceFiles)

		results <- &testResult{
			run:      run,
			index:    job.index,
			workerID: workerID,
		}
	}
}
