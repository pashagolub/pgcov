package coverage_test

import (
	"path/filepath"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// TestResolveBaseDir covers the precedence that makes `pgcov report` work with
// no flags. Coverage keys are relative to the run's discovery root, so
// resolving them against the working directory only happens to work when the
// two coincide -- which is why the recorded root is consulted.
func TestResolveBaseDir(t *testing.T) {
	realRoot := t.TempDir()
	explicit := t.TempDir()

	tests := []struct {
		name     string
		root     string
		explicit string
		want     string
	}{
		{
			name:     "explicit base dir always wins",
			root:     realRoot,
			explicit: explicit,
			want:     explicit,
		},
		{
			name: "recorded root is used when it still exists",
			root: realRoot,
			want: realRoot,
		},
		{
			name: "no recorded root falls back to the working directory",
			want: "",
		},
		{
			name: "a root that no longer exists falls back to the working directory",
			root: filepath.Join(realRoot, "moved-away"),
			want: "",
		},
		{
			name:     "explicit wins even when the recorded root is stale",
			root:     filepath.Join(realRoot, "moved-away"),
			explicit: explicit,
			want:     explicit,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cov := coverage.NewCoverage()
			cov.Root = tc.root
			if got := cov.ResolveBaseDir(tc.explicit); got != tc.want {
				t.Errorf("ResolveBaseDir(%q) with Root=%q = %q, want %q",
					tc.explicit, tc.root, got, tc.want)
			}
		})
	}
}

// TestResolveBaseDir_RootPointingAtAFile guards against a recorded root that
// resolves to something that is not a directory.
func TestResolveBaseDir_RootPointingAtAFile(t *testing.T) {
	dir := t.TempDir()
	file := writeSource(t, dir, "a.sql", "SELECT 1;\n")

	cov := coverage.NewCoverage()
	cov.Root = file

	if got := cov.ResolveBaseDir(""); got != "" {
		t.Errorf("ResolveBaseDir = %q; a non-directory root must be ignored", got)
	}
}

// TestRootSurvivesRoundTrip keeps the hint usable after save/load, which is the
// only way report ever sees it.
func TestRootSurvivesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	cov := coverage.NewCoverage()
	cov.Root = root
	cov.AddPosition("a.sql", 0, 9, 1)

	path := filepath.Join(dir, "coverage.json")
	if err := coverage.NewStore(path).Save(cov); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := coverage.NewStore(path).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Root != root {
		t.Errorf("Root = %q, want %q", loaded.Root, root)
	}
}

// TestMergeKeepsFirstRoot: shards run from the same tree share a root, and a
// merged file should still carry one so report keeps working.
func TestMergeKeepsFirstRoot(t *testing.T) {
	root := t.TempDir()

	a := coverage.NewCoverage()
	a.Root = root
	a.AddPosition("a.sql", 0, 9, 1)

	b := coverage.NewCoverage()
	b.Root = root
	b.AddPosition("a.sql", 0, 9, 2)

	merged, err := coverage.Merge(a, b)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.Root != root {
		t.Errorf("merged Root = %q, want %q", merged.Root, root)
	}
}
