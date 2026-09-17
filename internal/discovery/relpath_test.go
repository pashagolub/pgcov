package discovery_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
)

// inDir runs fn with the process working directory set to dir.
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	fn()
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"alpha", "beta"} {
		dir := filepath.Join(root, d)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "functions.sql"), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatalf("write source: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, d+"_test.sql"), []byte("SELECT 1;"), 0o644); err != nil {
			t.Fatalf("write test: %v", err)
		}
	}
	return root
}

func relPaths(files []discovery.DiscoveredFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.RelativePath)
	}
	return out
}

// TestDiscoverRelativePathIsIndependentOfCWD is the regression test for the
// defect: RelativePath was computed with filepath.Rel(cwd, path), so the same
// source file keyed differently depending on the directory pgcov ran from.
// Two CI shards invoking `pgcov run` from different directories produced
// disjoint keys for one file, and `pgcov merge` unioned rather than summed them.
func TestDiscoverRelativePathIsIndependentOfCWD(t *testing.T) {
	root := fixture(t)
	sub := filepath.Join(root, "alpha")

	var fromRoot, fromSub []string
	inDir(t, root, func() {
		files, err := discovery.Discover(root)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		fromRoot = relPaths(files)
	})
	inDir(t, sub, func() {
		files, err := discovery.Discover(root)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		fromSub = relPaths(files)
	})

	if len(fromRoot) != len(fromSub) {
		t.Fatalf("different file counts: %v vs %v", fromRoot, fromSub)
	}
	for i := range fromRoot {
		if fromRoot[i] != fromSub[i] {
			t.Errorf("key %d differs by working directory: %q vs %q - coverage keys "+
				"must depend only on the discovery root", i, fromRoot[i], fromSub[i])
		}
	}
}

// TestDiscoverRelativePathUsesForwardSlashes keeps coverage data portable
// across a mixed-OS CI matrix: keys carried OS-native separators, so a file
// keyed "sql\a.sql" on Windows could not be resolved by a reporter on Linux.
func TestDiscoverRelativePathUsesForwardSlashes(t *testing.T) {
	files, err := discovery.Discover(fixture(t))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no files discovered")
	}
	for _, p := range relPaths(files) {
		if strings.Contains(p, `\`) {
			t.Errorf("RelativePath %q contains a backslash; keys must be slash-normalised", p)
		}
		if filepath.IsAbs(p) {
			t.Errorf("RelativePath %q is absolute; keys must be relative to the discovery root", p)
		}
	}
}

// TestDiscoverRelativePathIsRootRelative pins the actual shape of the key
// against the documented contract ("Path relative to search root").
func TestDiscoverRelativePathIsRootRelative(t *testing.T) {
	root := fixture(t)

	files, err := discovery.Discover(root)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	want := map[string]bool{
		"alpha/functions.sql":  true,
		"alpha/alpha_test.sql": true,
		"beta/functions.sql":   true,
		"beta/beta_test.sql":   true,
	}
	for _, p := range relPaths(files) {
		if !want[p] {
			t.Errorf("unexpected key %q", p)
		}
		delete(want, p)
	}
	for p := range want {
		t.Errorf("missing key %q", p)
	}
}

// TestDiscoverCoLocatedSourcesKeysAgainstTheRun is the reason the root is
// threaded into DiscoverCoLocatedSources: it scans each test directory
// individually, so keying against the scanned directory would reduce both of
// these files to the bare name "functions.sql" and collapse two distinct
// sources onto one coverage key.
func TestDiscoverCoLocatedSourcesKeysAgainstTheRun(t *testing.T) {
	root := fixture(t)

	testFiles, err := discovery.DiscoverTests(root)
	if err != nil {
		t.Fatalf("discover tests: %v", err)
	}

	sources, err := discovery.DiscoverCoLocatedSources(root, testFiles)
	if err != nil {
		t.Fatalf("discover sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d: %v", len(sources), relPaths(sources))
	}

	seen := map[string]bool{}
	for _, p := range relPaths(sources) {
		if seen[p] {
			t.Errorf("duplicate coverage key %q - sources from different directories collapsed", p)
		}
		seen[p] = true
		if !strings.Contains(p, "/") {
			t.Errorf("key %q is a bare filename; it must be relative to the run's discovery root", p)
		}
	}
}

// TestDiscoverCoLocatedSourcesMatchesDiscover pins the two entry points to the
// same key format, since a run mixes both: DiscoverTests for tests and
// DiscoverCoLocatedSources for the sources beside them.
func TestDiscoverCoLocatedSourcesMatchesDiscover(t *testing.T) {
	root := fixture(t)

	all, err := discovery.DiscoverSources(root)
	if err != nil {
		t.Fatalf("discover sources: %v", err)
	}
	byPath := map[string]string{}
	for _, f := range all {
		byPath[f.Path] = f.RelativePath
	}

	testFiles, err := discovery.DiscoverTests(root)
	if err != nil {
		t.Fatalf("discover tests: %v", err)
	}
	coLocated, err := discovery.DiscoverCoLocatedSources(root, testFiles)
	if err != nil {
		t.Fatalf("discover co-located: %v", err)
	}

	for _, f := range coLocated {
		want, ok := byPath[f.Path]
		if !ok {
			t.Errorf("%s not found by DiscoverSources", f.Path)
			continue
		}
		if f.RelativePath != want {
			t.Errorf("%s: co-located key %q != Discover key %q", f.Path, f.RelativePath, want)
		}
	}
}
