package coverage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

func writeCoverageFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestStoreLoad_RejectsForeignSchemaVersion puts the guard at the single point
// every reader goes through, so `report`, `merge` and any future consumer
// inherit it rather than each remembering to check.
func TestStoreLoad_RejectsForeignSchemaVersion(t *testing.T) {
	path := writeCoverageFile(t, `{"version":"9.9","timestamp":"2026-01-01T00:00:00Z","positions":{"a.sql":{"0:10":1}}}`)

	_, err := coverage.NewStore(path).Load()
	if err == nil {
		t.Fatal("Load accepted a coverage file from an incompatible build")
	}
	msg := err.Error()
	if !strings.Contains(msg, "9.9") {
		t.Errorf("error %q should name the offending version", msg)
	}
	if !strings.Contains(msg, "regenerate") {
		t.Errorf("error %q should tell the user to regenerate with 'pgcov run'", msg)
	}
	if !strings.Contains(msg, path) {
		t.Errorf("error %q should name the file that failed to load", msg)
	}
}

// TestStoreLoad_RejectsMissingSchemaVersion covers a file with no version field.
func TestStoreLoad_RejectsMissingSchemaVersion(t *testing.T) {
	path := writeCoverageFile(t, `{"positions":{"a.sql":{"0:10":1}}}`)

	if _, err := coverage.NewStore(path).Load(); err == nil {
		t.Fatal("Load accepted a coverage file with no schema version")
	}
}

// TestStoreSaveLoad_RoundTrip confirms the guard does not reject what this
// build itself writes.
func TestStoreSaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "coverage.json")

	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 10, 3)

	store := coverage.NewStore(path)
	if err := store.Save(cov); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != coverage.SchemaVersion {
		t.Errorf("Version = %q, want %q", loaded.Version, coverage.SchemaVersion)
	}
	if got := loaded.Positions["a.sql"]["0:10"]; got != 3 {
		t.Errorf("hit count = %d, want 3", got)
	}
}
