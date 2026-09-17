package coverage

import (
	"fmt"
	"maps"
	"sync"

	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
)

// Collector aggregates coverage signals from test runs
type Collector struct {
	coverage *Coverage
	mu       sync.Mutex // Protects coverage for thread-safe parallel execution
}

// NewCollector creates a new coverage collector
func NewCollector() *Collector {
	return &Collector{
		coverage: NewCoverage(),
	}
}

// CollectFromRun processes coverage signals from a single test run
func (c *Collector) CollectFromRun(testRun *runner.TestRun) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, signal := range testRun.CoverageSigs {
		if err := c.addSignalUnsafe(signal); err != nil {
			return fmt.Errorf("failed to process signal %s: %w", signal.SignalID, err)
		}
	}
	return nil
}

// CollectFromRuns processes coverage signals from multiple test runs
func (c *Collector) CollectFromRuns(testRuns []*runner.TestRun) error {
	for _, run := range testRuns {
		if err := c.CollectFromRun(run); err != nil {
			return err
		}
	}
	return nil
}

// AddSignal adds a single coverage signal to the aggregated coverage
func (c *Collector) AddSignal(signal runner.CoverageSignal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.addSignalUnsafe(signal)
}

// addSignalUnsafe adds a signal without locking (internal use when lock is already held).
//
// Whether a position is executable or implicit is established by
// InitializeFromInstrumented, which seeds both maps; a signal simply increments
// whichever map already holds its key. A signal for an unseeded position is
// treated as executable, which is the safe default: it counts toward the
// measured percentage rather than silently inflating it.
func (c *Collector) addSignalUnsafe(signal runner.CoverageSignal) error {
	// Parse signal ID to extract file, startPos, and length
	file, startPos, length, err := instrument.ParseSignalID(signal.SignalID)
	if err != nil {
		return fmt.Errorf("invalid signal ID: %w", err)
	}

	posKey := fmt.Sprintf("%d:%d", startPos, length)

	if existingCount, exists := c.coverage.ImplicitPositions[file][posKey]; exists {
		c.coverage.AddImplicitPosition(file, startPos, length, existingCount+1)
		return nil
	}

	existingCount := c.coverage.Positions[file][posKey]
	c.coverage.AddPosition(file, startPos, length, existingCount+1)
	return nil
}

// Coverage returns a deep copy of the aggregated coverage data
func (c *Collector) Coverage() *Coverage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coverage.Clone()
}

// Reset clears all collected coverage data
func (c *Collector) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.coverage = NewCoverage()
}

// GetFilePositionCoverage returns a copy of position coverage data for a specific file
func (c *Collector) GetFilePositionCoverage(filePath string) PositionHits {
	c.mu.Lock()
	defer c.mu.Unlock()
	orig := c.coverage.Positions[filePath]
	if orig == nil {
		return nil
	}
	clone := make(PositionHits, len(orig))
	maps.Copy(clone, orig)
	return clone
}

// GetFileList returns a sorted list of all files with coverage data
func (c *Collector) GetFileList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.coverage.GetFiles()
}

// InitializeFromInstrumented seeds the coverage data with 0-hit entries for
// every CoveragePoint that has not yet been recorded, routing each to the
// executable or implicit map according to its ImplicitCoverage flag.
//
// Seeding serves two purposes. It makes unexecuted branches (ELSIF/ELSE arms
// that were never taken) appear as "not covered" rather than being absent, and
// it is what classifies each position: addSignalUnsafe afterwards just
// increments whichever map holds the key. Implicit positions are seeded too, so
// a source file that fails to load is visibly 0% instead of missing entirely -
// previously they were skipped, which meant an implicit position existed only
// if its hit count was already at least 1.
func (c *Collector) InitializeFromInstrumented(instrumented []*instrument.InstrumentedSQL) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, inst := range instrumented {
		for _, cp := range inst.Locations {
			// Only seed if not already present (do not overwrite real hit counts).
			posKey := fmt.Sprintf("%d:%d", cp.StartPos, cp.Length)
			if cp.ImplicitCoverage {
				if _, exists := c.coverage.ImplicitPositions[cp.File][posKey]; !exists {
					c.coverage.AddImplicitPosition(cp.File, cp.StartPos, cp.Length, 0)
				}
				continue
			}
			if _, exists := c.coverage.Positions[cp.File][posKey]; !exists {
				c.coverage.AddPosition(cp.File, cp.StartPos, cp.Length, 0)
			}
		}
	}
}

// TotalCoveragePercent returns the overall coverage percentage across
// executable statements. DDL/DML positions are excluded; see
// Coverage.TotalPositionCoveragePercent.
func (c *Collector) TotalCoveragePercent() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coverage.TotalPositionCoveragePercent()
}

// ExecutablePositionCounts returns covered and total counts over executable
// statements, for callers that want to show the numbers behind the percentage.
func (c *Collector) ExecutablePositionCounts() (covered int, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coverage.ExecutablePositionCounts()
}

// ImplicitPositionCounts returns covered and total counts over DDL/DML
// statements.
func (c *Collector) ImplicitPositionCounts() (covered int, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coverage.ImplicitPositionCounts()
}

// HasExecutablePositions reports whether anything measurable was instrumented.
func (c *Collector) HasExecutablePositions() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.coverage.HasExecutablePositions()
}
