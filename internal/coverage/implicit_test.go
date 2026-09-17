package coverage_test

import (
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// instrumentedWith builds an InstrumentedSQL carrying the given points, as
// GenerateCoverageInstrument would.
func instrumentedWith(file string, points ...instrument.CoveragePoint) *instrument.InstrumentedSQL {
	for i := range points {
		points[i].File = file
		points[i].SignalID = instrument.FormatSignalID(file, points[i].StartPos, points[i].Length)
	}
	return &instrument.InstrumentedSQL{Original: &parser.ParsedSQL{}, Locations: points}
}

// loadRun mimics what the executor appends when a source file loads
// successfully: one signal per implicit (DDL/DML) location.
func loadRun(sources ...*instrument.InstrumentedSQL) *runner.TestRun {
	run := &runner.TestRun{}
	for _, src := range sources {
		for _, cp := range src.Locations {
			if cp.ImplicitCoverage {
				run.CoverageSigs = append(run.CoverageSigs, types.CoverageSignal{SignalID: cp.SignalID})
			}
		}
	}
	return run
}

// TestImplicitPositionsDoNotInflateCoverage is the regression test for the
// headline defect. Three DDL statements plus one never-executed PL/pgSQL
// statement used to report 75% coverage where the real executable coverage was
// 0%: implicit positions entered the map only via the load-time signal, so they
// existed if and only if their hit count was >= 1, yet were counted in the
// denominator. Every CREATE TABLE was a permanently-100%-covered entry.
func TestImplicitPositionsDoNotInflateCoverage(t *testing.T) {
	src := instrumentedWith("a.sql",
		instrument.CoveragePoint{StartPos: 0, Length: 10, ImplicitCoverage: true},
		instrument.CoveragePoint{StartPos: 10, Length: 10, ImplicitCoverage: true},
		instrument.CoveragePoint{StartPos: 20, Length: 10, ImplicitCoverage: true},
		instrument.CoveragePoint{StartPos: 30, Length: 10, ImplicitCoverage: false},
	)

	c := coverage.NewCollector()
	c.InitializeFromInstrumented([]*instrument.InstrumentedSQL{src})
	if err := c.CollectFromRuns([]*runner.TestRun{loadRun(src)}); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if got := c.TotalCoveragePercent(); got != 0.0 {
		t.Errorf("coverage = %.2f%%, want 0.00%% - the single executable statement was "+
			"never executed, so DDL must not lift the number", got)
	}

	covered, total := c.ExecutablePositionCounts()
	if covered != 0 || total != 1 {
		t.Errorf("executable counts = %d/%d, want 0/1", covered, total)
	}

	implicitCovered, implicitTotal := c.ImplicitPositionCounts()
	if implicitCovered != 3 || implicitTotal != 3 {
		t.Errorf("implicit counts = %d/%d, want 3/3", implicitCovered, implicitTotal)
	}
}

// TestExecutableCoverageIsMeasuredNormally confirms the split did not break the
// ordinary case: executing the statement moves the number to 100%.
func TestExecutableCoverageIsMeasuredNormally(t *testing.T) {
	src := instrumentedWith("a.sql",
		instrument.CoveragePoint{StartPos: 0, Length: 10, ImplicitCoverage: true},
		instrument.CoveragePoint{StartPos: 30, Length: 10, ImplicitCoverage: false},
	)

	c := coverage.NewCollector()
	c.InitializeFromInstrumented([]*instrument.InstrumentedSQL{src})

	run := loadRun(src)
	run.CoverageSigs = append(run.CoverageSigs,
		types.CoverageSignal{SignalID: instrument.FormatSignalID("a.sql", 30, 10)})

	if err := c.CollectFromRuns([]*runner.TestRun{run}); err != nil {
		t.Fatalf("collect: %v", err)
	}

	if got := c.TotalCoveragePercent(); got != 100.0 {
		t.Errorf("coverage = %.2f%%, want 100.00%%", got)
	}
}

// TestPartialExecutableCoverage pins an ordinary mixed result and, critically,
// that a large pile of DDL does not drag it upward.
func TestPartialExecutableCoverage(t *testing.T) {
	points := []instrument.CoveragePoint{
		{StartPos: 0, Length: 5, ImplicitCoverage: false},
		{StartPos: 5, Length: 5, ImplicitCoverage: false},
		{StartPos: 10, Length: 5, ImplicitCoverage: false},
		{StartPos: 15, Length: 5, ImplicitCoverage: false},
	}
	for i := 0; i < 20; i++ {
		points = append(points, instrument.CoveragePoint{
			StartPos: 100 + i*10, Length: 10, ImplicitCoverage: true,
		})
	}
	src := instrumentedWith("a.sql", points...)

	c := coverage.NewCollector()
	c.InitializeFromInstrumented([]*instrument.InstrumentedSQL{src})

	run := loadRun(src)
	run.CoverageSigs = append(run.CoverageSigs,
		types.CoverageSignal{SignalID: instrument.FormatSignalID("a.sql", 0, 5)})
	if err := c.CollectFromRuns([]*runner.TestRun{run}); err != nil {
		t.Fatalf("collect: %v", err)
	}

	// 1 of 4 executable statements covered. With 20 DDL statements folded in,
	// the old computation would have reported 21/24 = 87.5%.
	if got := c.TotalCoveragePercent(); got != 25.0 {
		t.Errorf("coverage = %.2f%%, want 25.00%%", got)
	}
}

// TestImplicitPositionsAreSeededSoAFailedLoadShowsZero covers the other half of
// the seeding change: implicit points used to be skipped by
// InitializeFromInstrumented, so a source file that failed to load was absent
// from the report rather than visibly uncovered.
func TestImplicitPositionsAreSeededSoAFailedLoadShowsZero(t *testing.T) {
	src := instrumentedWith("a.sql",
		instrument.CoveragePoint{StartPos: 0, Length: 10, ImplicitCoverage: true},
		instrument.CoveragePoint{StartPos: 10, Length: 10, ImplicitCoverage: true},
	)

	c := coverage.NewCollector()
	c.InitializeFromInstrumented([]*instrument.InstrumentedSQL{src})
	// No run at all: the file never loaded.

	covered, total := c.ImplicitPositionCounts()
	if covered != 0 || total != 2 {
		t.Errorf("implicit counts = %d/%d, want 0/2 - a file that never loaded must be "+
			"visible as uncovered, not absent", covered, total)
	}

	cov := c.Coverage()
	if got := cov.TotalImplicitCoveragePercent(); got != 0.0 {
		t.Errorf("implicit coverage = %.2f%%, want 0.00%%", got)
	}
}

// TestAllPositionsKeepsDDLHighlighted guards the reporter contract: splitting
// the maps must not stop DDL/DML lines being rendered, only stop them counting
// toward the percentage.
func TestAllPositionsKeepsDDLHighlighted(t *testing.T) {
	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 30, 10, 1)
	cov.AddImplicitPosition("a.sql", 0, 10, 1)
	cov.AddImplicitPosition("a.sql", 10, 10, 1)

	all := cov.AllPositions("a.sql")
	if len(all) != 3 {
		t.Errorf("AllPositions returned %d entries, want 3 (reporters must still render DDL)", len(all))
	}
	for _, key := range []string{"0:10", "10:10", "30:10"} {
		if _, ok := all[key]; !ok {
			t.Errorf("AllPositions missing %q", key)
		}
	}

	// A file with only DDL must still appear in the report's file list.
	ddlOnly := coverage.NewCoverage()
	ddlOnly.AddImplicitPosition("schema.sql", 0, 10, 1)
	files := ddlOnly.GetFiles()
	if len(files) != 1 || files[0] != "schema.sql" {
		t.Errorf("GetFiles() = %v, want [schema.sql]", files)
	}
}

// TestMergeSumsBothPositionKinds keeps `pgcov merge` consistent with the split.
func TestMergeSumsBothPositionKinds(t *testing.T) {
	a := coverage.NewCoverage()
	a.AddPosition("a.sql", 0, 5, 2)
	a.AddImplicitPosition("a.sql", 100, 10, 1)

	b := coverage.NewCoverage()
	b.AddPosition("a.sql", 0, 5, 3)
	b.AddImplicitPosition("a.sql", 100, 10, 1)

	merged, err := coverage.Merge(a, b)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if got := merged.Positions["a.sql"]["0:5"]; got != 5 {
		t.Errorf("executable hits = %d, want 5", got)
	}
	if got := merged.ImplicitPositions["a.sql"]["100:10"]; got != 2 {
		t.Errorf("implicit hits = %d, want 2", got)
	}
	if got := merged.TotalPositionCoveragePercent(); got != 100.0 {
		t.Errorf("merged coverage = %.2f%%, want 100.00%%", got)
	}
}

// TestCloneCopiesBothPositionKinds guards the deep copy behind Collector.Coverage().
func TestCloneCopiesBothPositionKinds(t *testing.T) {
	orig := coverage.NewCoverage()
	orig.AddPosition("a.sql", 0, 5, 1)
	orig.AddImplicitPosition("a.sql", 100, 10, 1)

	clone := orig.Clone()
	clone.AddPosition("a.sql", 0, 5, 99)
	clone.AddImplicitPosition("a.sql", 100, 10, 99)

	if got := orig.Positions["a.sql"]["0:5"]; got != 1 {
		t.Errorf("clone mutated original executable hits: %d", got)
	}
	if got := orig.ImplicitPositions["a.sql"]["100:10"]; got != 1 {
		t.Errorf("clone mutated original implicit hits: %d", got)
	}
}
