package coverage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

func writeSource(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	a := writeSource(t, dir, "a.sql", "SELECT 1;\n")
	b := writeSource(t, dir, "b.sql", "SELECT 1;\n")
	c := writeSource(t, dir, "c.sql", "SELECT 2;\n")

	ia, err := coverage.HashFile(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	ib, _ := coverage.HashFile(b)
	ic, _ := coverage.HashFile(c)

	if ia.SHA256 != ib.SHA256 {
		t.Error("identical contents must hash identically")
	}
	if ia.SHA256 == ic.SHA256 {
		t.Error("different contents must hash differently")
	}
	if ia.Size != int64(len("SELECT 1;\n")) {
		t.Errorf("size = %d, want %d", ia.Size, len("SELECT 1;\n"))
	}

	if _, err := coverage.HashFile(filepath.Join(dir, "missing.sql")); err == nil {
		t.Error("hashing a missing file must fail")
	}
}

// TestVerifySources_DetectsEditedSource is the point of the feature: positions
// are byte offsets, so editing a source between `run` and `report` shifts every
// offset past the edit and the report annotates the wrong spans.
func TestVerifySources_DetectsEditedSource(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.sql", "SELECT 1;\n")

	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 9, 1)
	info, err := coverage.HashFile(filepath.Join(dir, "a.sql"))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	cov.AddSource("a.sql", info)

	if w := cov.VerifySources(dir); len(w) != 0 {
		t.Fatalf("unmodified source produced warnings: %v", w)
	}

	// Simulate an edit between run and report.
	writeSource(t, dir, "a.sql", "-- a new leading comment\nSELECT 1;\n")

	warnings := cov.VerifySources(dir)
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning for the edited source, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "a.sql") {
		t.Errorf("warning %q should name the file", warnings[0])
	}
	if !strings.Contains(warnings[0], "pgcov run") {
		t.Errorf("warning %q should tell the user how to fix it", warnings[0])
	}
}

func TestVerifySources_ReportsMissingSource(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.sql", "SELECT 1;\n")
	info, _ := coverage.HashFile(filepath.Join(dir, "a.sql"))

	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 9, 1)
	cov.AddSource("a.sql", info)

	if err := os.Remove(filepath.Join(dir, "a.sql")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	warnings := cov.VerifySources(dir)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "not found") {
		t.Errorf("expected a not-found warning, got %v", warnings)
	}
}

// TestVerifySources_NoFingerprintsIsQuiet keeps coverage written by an older
// build usable: verification is skipped rather than reported as a failure.
func TestVerifySources_NoFingerprintsIsQuiet(t *testing.T) {
	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 9, 1)

	if w := cov.VerifySources(t.TempDir()); len(w) != 0 {
		t.Errorf("coverage without fingerprints must verify quietly, got %v", w)
	}
}

// TestMerge_RefusesConflictingSourceRevisions covers the other consumer:
// summing hit counts across two revisions of a file yields an internally
// consistent, meaningless result.
func TestMerge_RefusesConflictingSourceRevisions(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.sql", "SELECT 1;\n")
	revA, _ := coverage.HashFile(filepath.Join(dir, "a.sql"))
	writeSource(t, dir, "a.sql", "SELECT 2;\n")
	revB, _ := coverage.HashFile(filepath.Join(dir, "a.sql"))

	shardA := coverage.NewCoverage()
	shardA.AddPosition("a.sql", 0, 9, 1)
	shardA.AddSource("a.sql", revA)

	shardB := coverage.NewCoverage()
	shardB.AddPosition("a.sql", 0, 9, 1)
	shardB.AddSource("a.sql", revB)

	_, err := coverage.Merge(shardA, shardB)
	if err == nil {
		t.Fatal("Merge combined coverage from two revisions of the same source")
	}
	if !strings.Contains(err.Error(), "a.sql") {
		t.Errorf("error %q should name the conflicting file", err.Error())
	}
}

// TestMerge_AllowsMatchingSourceRevisions is the ordinary CI-shard case.
func TestMerge_AllowsMatchingSourceRevisions(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.sql", "SELECT 1;\n")
	rev, _ := coverage.HashFile(filepath.Join(dir, "a.sql"))

	shardA := coverage.NewCoverage()
	shardA.AddPosition("a.sql", 0, 9, 2)
	shardA.AddSource("a.sql", rev)

	shardB := coverage.NewCoverage()
	shardB.AddPosition("a.sql", 0, 9, 3)
	shardB.AddSource("a.sql", rev)

	merged, err := coverage.Merge(shardA, shardB)
	if err != nil {
		t.Fatalf("merge of matching revisions failed: %v", err)
	}
	if got := merged.Positions["a.sql"]["0:9"]; got != 5 {
		t.Errorf("hits = %d, want 5", got)
	}
	if merged.Sources["a.sql"].SHA256 != rev.SHA256 {
		t.Error("merged coverage lost the source fingerprint")
	}
}

// TestMerge_SourcesFromOnlyOneInputAreKept covers shards that touched
// different files.
func TestMerge_SourcesFromOnlyOneInputAreKept(t *testing.T) {
	dir := t.TempDir()
	writeSource(t, dir, "a.sql", "SELECT 1;\n")
	writeSource(t, dir, "b.sql", "SELECT 2;\n")
	revA, _ := coverage.HashFile(filepath.Join(dir, "a.sql"))
	revB, _ := coverage.HashFile(filepath.Join(dir, "b.sql"))

	shardA := coverage.NewCoverage()
	shardA.AddPosition("a.sql", 0, 9, 1)
	shardA.AddSource("a.sql", revA)

	shardB := coverage.NewCoverage()
	shardB.AddPosition("b.sql", 0, 9, 1)
	shardB.AddSource("b.sql", revB)

	merged, err := coverage.Merge(shardA, shardB)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(merged.Sources) != 2 {
		t.Errorf("expected 2 fingerprints, got %d", len(merged.Sources))
	}
}

// TestCloneCopiesSources guards the deep copy behind Collector.Coverage().
func TestCloneCopiesSources(t *testing.T) {
	orig := coverage.NewCoverage()
	orig.AddSource("a.sql", coverage.SourceInfo{SHA256: "abc", Size: 1})

	clone := orig.Clone()
	clone.AddSource("a.sql", coverage.SourceInfo{SHA256: "zzz", Size: 9})

	if orig.Sources["a.sql"].SHA256 != "abc" {
		t.Errorf("clone mutated the original fingerprint: %v", orig.Sources["a.sql"])
	}
}
