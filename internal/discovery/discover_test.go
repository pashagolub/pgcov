package discovery_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
)

func touch(t *testing.T, dir, name string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestDiscover_ErrorCases(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		_, err := discovery.Discover(filepath.Join(t.TempDir(), "nope"))
		if err == nil {
			t.Fatal("a missing directory must be an error")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error %q should say the directory was not found", err.Error())
		}
	})

	t.Run("path is a file", func(t *testing.T) {
		dir := t.TempDir()
		touch(t, dir, "a.sql")
		_, err := discovery.Discover(filepath.Join(dir, "a.sql"))
		if err == nil {
			t.Fatal("a file path must be rejected")
		}
		if !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("error %q should say the path is not a directory", err.Error())
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		files, err := discovery.Discover(t.TempDir())
		if err != nil {
			t.Fatalf("an empty directory is not an error: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("expected no files, got %v", files)
		}
	})
}

// TestDiscover_OnlySQLFilesAndRecursion pins the two traversal rules the
// runner depends on: non-.sql files are ignored, and nesting is followed.
func TestDiscover_OnlySQLFilesAndRecursion(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "top.sql")
	touch(t, dir, "README.md")
	touch(t, dir, "notes.txt")
	touch(t, dir, filepath.Join("nested", "deep", "inner.sql"))
	touch(t, dir, filepath.Join("nested", "UPPER.SQL"))

	files, err := discovery.Discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	got := map[string]bool{}
	for _, f := range files {
		got[f.RelativePath] = true
	}

	for _, want := range []string{"top.sql", "nested/deep/inner.sql", "nested/UPPER.SQL"} {
		if !got[want] {
			t.Errorf("missing %q from %v", want, got)
		}
	}
	for _, unwanted := range []string{"README.md", "notes.txt"} {
		if got[unwanted] {
			t.Errorf("non-SQL file %q was discovered", unwanted)
		}
	}
}

func TestDiscoverTestsAndSources_Partition(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "math.sql")
	touch(t, dir, "math_test.sql")
	touch(t, dir, filepath.Join("sub", "util.sql"))
	touch(t, dir, filepath.Join("sub", "util_test.sql"))

	tests, err := discovery.DiscoverTests(dir)
	if err != nil {
		t.Fatalf("DiscoverTests: %v", err)
	}
	sources, err := discovery.DiscoverSources(dir)
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}

	if len(tests) != 2 {
		t.Errorf("expected 2 test files, got %d", len(tests))
	}
	if len(sources) != 2 {
		t.Errorf("expected 2 source files, got %d", len(sources))
	}
	for _, f := range tests {
		if !strings.HasSuffix(f.RelativePath, "_test.sql") {
			t.Errorf("%q classified as a test", f.RelativePath)
		}
	}
	for _, f := range sources {
		if strings.HasSuffix(f.RelativePath, "_test.sql") {
			t.Errorf("%q classified as a source", f.RelativePath)
		}
	}
}

// TestDiscoverCoLocatedSources_IgnoresDirectoriesWithoutTests is the point of
// co-location: sources living where no test does are not deployed.
func TestDiscoverCoLocatedSources_IgnoresDirectoriesWithoutTests(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, filepath.Join("tested", "a.sql"))
	touch(t, dir, filepath.Join("tested", "a_test.sql"))
	touch(t, dir, filepath.Join("untested", "b.sql"))

	tests, err := discovery.DiscoverTests(dir)
	if err != nil {
		t.Fatalf("DiscoverTests: %v", err)
	}
	sources, err := discovery.DiscoverCoLocatedSources(dir, tests)
	if err != nil {
		t.Fatalf("DiscoverCoLocatedSources: %v", err)
	}

	if len(sources) != 1 {
		t.Fatalf("expected 1 co-located source, got %d", len(sources))
	}
	if sources[0].RelativePath != "tested/a.sql" {
		t.Errorf("got %q, want %q", sources[0].RelativePath, "tested/a.sql")
	}
}

func TestDiscoverCoLocatedSources_NoTests(t *testing.T) {
	sources, err := discovery.DiscoverCoLocatedSources(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("DiscoverCoLocatedSources: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("expected no sources, got %v", sources)
	}
}

// TestDiscoverCoLocatedSources_DeduplicatesAcrossTests covers two tests sharing
// a directory: their common source must be deployed once, not twice.
func TestDiscoverCoLocatedSources_DeduplicatesAcrossTests(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "shared.sql")
	touch(t, dir, "one_test.sql")
	touch(t, dir, "two_test.sql")

	tests, err := discovery.DiscoverTests(dir)
	if err != nil {
		t.Fatalf("DiscoverTests: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("expected 2 tests, got %d", len(tests))
	}

	sources, err := discovery.DiscoverCoLocatedSources(dir, tests)
	if err != nil {
		t.Fatalf("DiscoverCoLocatedSources: %v", err)
	}
	if len(sources) != 1 {
		t.Errorf("expected the shared source once, got %d entries", len(sources))
	}
}
