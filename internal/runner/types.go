package runner

import (
	"fmt"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// CoverageSignal is an alias for the shared type
type CoverageSignal = types.CoverageSignal

// TestRun represents a single test execution
type TestRun struct {
	Test         *discovery.DiscoveredFile
	Database     string // name of the temp database used for this test run
	StartTime    time.Time
	EndTime      time.Time
	Status       TestStatus
	Error        error            // Non-nil if test failed
	CoverageSigs []CoverageSignal // Signals collected during test
}

// TestStatus represents the current state of a test execution
type TestStatus int

const (
	TestPending TestStatus = iota
	TestRunning
	TestPassed
	TestFailed
	TestTimeout
)

// String returns a string representation of TestStatus
func (ts TestStatus) String() string {
	switch ts {
	case TestPending:
		return "pending"
	case TestRunning:
		return "running"
	case TestPassed:
		return "passed"
	case TestFailed:
		return "failed"
	case TestTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// Note: CoverageSignal and TempDatabase moved to pkg/types to avoid import cycles

// Duration returns the test execution duration
func (tr *TestRun) Duration() time.Duration {
	if tr.EndTime.IsZero() {
		return time.Since(tr.StartTime)
	}
	return tr.EndTime.Sub(tr.StartTime)
}

// TestSummary summarizes all test executions
type TestSummary struct {
	TotalTests    int
	PassedTests   int
	FailedTests   int
	TimedOutTests int
	TotalDuration time.Duration
}

// AllPassed returns true if all tests passed
func (s *TestSummary) AllPassed() bool {
	return s.FailedTests == 0 && s.TimedOutTests == 0
}

// ExitCode returns the appropriate exit code based on test results
func (s *TestSummary) ExitCode() int {
	if s.AllPassed() {
		return 0
	}
	return 1
}

// FormatFailedTests renders one human-readable line per test run that did not
// pass, so users do not have to re-run with --verbose to see why. Failures are
// prefixed "FAILED " and per-test timeouts "TIMEOUT ", keeping the two apart in
// the summary block; runs that passed, are still pending, or carry no Error are
// skipped. The returned slice is nil when there is nothing to report, so callers
// can print it directly with no extra guarding. Output is built (not printed)
// here so it stays unit-testable without a live database.
func FormatFailedTests(runs []*TestRun) []string {
	var lines []string
	for _, run := range runs {
		if run == nil || run.Error == nil {
			continue
		}
		var prefix string
		switch run.Status {
		case TestFailed:
			prefix = "FAILED"
		case TestTimeout:
			prefix = "TIMEOUT"
		default:
			continue
		}
		relPath := ""
		if run.Test != nil {
			relPath = run.Test.RelativePath
		}
		lines = append(lines, fmt.Sprintf("%s %s: %v", prefix, relPath, run.Error))
	}
	return lines
}
