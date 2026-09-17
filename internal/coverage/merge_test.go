package coverage

import (
	"strings"
	"testing"
)

// mustMerge calls Merge and fails the test on error, keeping the existing
// assertions focused on the summing behaviour rather than error plumbing.
func mustMerge(t *testing.T, coverages ...*Coverage) *Coverage {
	t.Helper()
	merged, err := Merge(coverages...)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	return merged
}

func TestMerge_OverlappingAndDisjoint(t *testing.T) {
	a := NewCoverage()
	a.AddPosition("a.sql", 100, 50, 2)
	a.AddPosition("a.sql", 200, 60, 1)
	a.AddPosition("shared.sql", 10, 5, 3)

	b := NewCoverage()
	b.AddPosition("a.sql", 100, 50, 5)    // overlaps a.sql:100:50 -> 2+5 = 7
	b.AddPosition("b.sql", 300, 70, 4)    // disjoint
	b.AddPosition("shared.sql", 10, 5, 1) // overlaps shared.sql:10:5 -> 3+1 = 4

	merged := mustMerge(t, a, b)

	if merged.Positions["a.sql"]["100:50"] != 7 {
		t.Errorf("a.sql:100:50 = %d, want 7", merged.Positions["a.sql"]["100:50"])
	}
	if merged.Positions["a.sql"]["200:60"] != 1 {
		t.Errorf("a.sql:200:60 = %d, want 1", merged.Positions["a.sql"]["200:60"])
	}
	if merged.Positions["b.sql"]["300:70"] != 4 {
		t.Errorf("b.sql:300:70 = %d, want 4", merged.Positions["b.sql"]["300:70"])
	}
	if merged.Positions["shared.sql"]["10:5"] != 4 {
		t.Errorf("shared.sql:10:5 = %d, want 4", merged.Positions["shared.sql"]["10:5"])
	}
}

func TestMerge_SumsHitCounts(t *testing.T) {
	a := NewCoverage()
	a.AddPosition("f.sql", 0, 10, 1)

	b := NewCoverage()
	b.AddPosition("f.sql", 0, 10, 1)

	c := NewCoverage()
	c.AddPosition("f.sql", 0, 10, 1)

	merged := mustMerge(t, a, b, c)

	got := merged.Positions["f.sql"]["0:10"]
	if got != 3 {
		t.Errorf("f.sql:0:10 = %d, want 3", got)
	}
}

func TestMerge_Empty(t *testing.T) {
	merged := mustMerge(t)
	if merged == nil {
		t.Fatal("Merge() returned nil")
	}
	if len(merged.Positions) != 0 {
		t.Errorf("empty merge produced positions: %v", merged.Positions)
	}
	if merged.Version != NewCoverage().Version {
		t.Errorf("empty merge Version = %q, want %q", merged.Version, NewCoverage().Version)
	}
}

func TestMerge_NilInputs(t *testing.T) {
	a := NewCoverage()
	a.AddPosition("f.sql", 0, 1, 1)

	merged := mustMerge(t, nil, a, nil)
	if merged.Positions["f.sql"]["0:1"] != 1 {
		t.Errorf("nil-aware merge lost a's hit count: got %d, want 1",
			merged.Positions["f.sql"]["0:1"])
	}
}

func TestMerge_DoesNotMutateInputs(t *testing.T) {
	a := NewCoverage()
	a.AddPosition("f.sql", 0, 1, 2)
	b := NewCoverage()
	b.AddPosition("f.sql", 0, 1, 3)

	_ = mustMerge(t, a, b)

	if a.Positions["f.sql"]["0:1"] != 2 {
		t.Errorf("Merge mutated a: got %d, want 2", a.Positions["f.sql"]["0:1"])
	}
	if b.Positions["f.sql"]["0:1"] != 3 {
		t.Errorf("Merge mutated b: got %d, want 3", b.Positions["f.sql"]["0:1"])
	}
}

func TestMerge_VersionAndTimestamp(t *testing.T) {
	c := NewCoverage()
	c.AddPosition("f.sql", 0, 1, 1)

	merged := mustMerge(t, c)
	if merged.Version != SchemaVersion {
		t.Errorf("merged.Version = %q, want %q", merged.Version, SchemaVersion)
	}
	if merged.Timestamp.IsZero() {
		t.Errorf("merged.Timestamp is zero; expected time.Now()")
	}
}

// TestMerge_RejectsForeignSchemaVersion is the point of this change: Merge is
// the one operation that consumes files it did not write, so summing hit counts
// across schema versions would produce plausible-looking wrong output. It used
// to accept any Version and stamp the result with the current one.
func TestMerge_RejectsForeignSchemaVersion(t *testing.T) {
	good := NewCoverage()
	good.AddPosition("f.sql", 0, 1, 1)

	foreign := NewCoverage()
	foreign.Version = "9.9"
	foreign.AddPosition("f.sql", 0, 1, 1)

	_, err := Merge(good, foreign)
	if err == nil {
		t.Fatal("Merge accepted a foreign schema version; mismatched inputs must be refused")
	}

	msg := err.Error()
	// The offending input is identified by position so the user knows which
	// file to regenerate, and the remedy is stated.
	if !strings.Contains(msg, "input 2") {
		t.Errorf("error %q should name which input was rejected", msg)
	}
	if !strings.Contains(msg, "9.9") {
		t.Errorf("error %q should name the offending version", msg)
	}
	if !strings.Contains(msg, "regenerate") {
		t.Errorf("error %q should tell the user to regenerate, not to downgrade", msg)
	}
}

// TestMerge_RejectsMissingSchemaVersion covers a hand-written or truncated file
// with no version field at all.
func TestMerge_RejectsMissingSchemaVersion(t *testing.T) {
	bare := &Coverage{Positions: map[string]PositionHits{"f.sql": {"0:1": 1}}}

	if _, err := Merge(bare); err == nil {
		t.Fatal("Merge accepted coverage with no schema version")
	}
}

// TestValidateVersion covers the shared guard directly.
func TestValidateVersion(t *testing.T) {
	if err := NewCoverage().ValidateVersion(); err != nil {
		t.Errorf("current schema version rejected: %v", err)
	}
	if err := (&Coverage{Version: "0.9"}).ValidateVersion(); err == nil {
		t.Error("older schema version accepted")
	}
	if err := (&Coverage{}).ValidateVersion(); err == nil {
		t.Error("empty schema version accepted")
	}
	var nilCov *Coverage
	if err := nilCov.ValidateVersion(); err == nil {
		t.Error("nil coverage accepted")
	}
}
