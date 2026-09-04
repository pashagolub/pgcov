package cli_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/cli"
	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
)

// TestRun_RecordsRootAndSources ties the run's own output to what `report` and
// `merge` later rely on.
func TestRun_RecordsRootAndSources(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	dir := partiallyCoveredFixture(t)
	cfg := cli.NewConfig()
	cfg.ConnectionString = connString
	cfg.Timeout = 60 * time.Second
	cfg.CoverageFile = filepath.Join(t.TempDir(), "coverage.json")

	if _, err := cli.Run(context.Background(), cfg, dir); err != nil {
		t.Fatalf("Run: %v", err)
	}

	cov, err := coverage.NewStore(cfg.CoverageFile).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	wantRoot, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	if cov.Root != wantRoot {
		t.Errorf("Root = %q, want %q", cov.Root, wantRoot)
	}
	if len(cov.Sources) == 0 {
		t.Error("no source fingerprints recorded")
	}
	if _, ok := cov.Sources["calc.sql"]; !ok {
		t.Errorf("expected a fingerprint keyed %q, got %v", "calc.sql", cov.Sources)
	}
	// Verification resolves keys against the recorded root, the same way
	// `pgcov report` does when no --base-dir is given.
	if w := cov.VerifySources(cov.ResolveBaseDir("")); len(w) != 0 {
		t.Errorf("freshly written coverage must verify cleanly, got %v", w)
	}
}
