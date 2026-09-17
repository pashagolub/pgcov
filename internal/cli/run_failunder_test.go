package cli_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/cli"
	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
)

// partiallyCoveredFixture builds a tree whose single plpgsql function has two
// branches, only one of which the test exercises. Coverage lands strictly
// between 0 and 100 so a threshold can sit on either side of it.
func partiallyCoveredFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("calc.sql", `
CREATE OR REPLACE FUNCTION classify(n INT) RETURNS TEXT AS $$
BEGIN
    IF n > 0 THEN
        RETURN 'positive';
    ELSIF n < 0 THEN
        RETURN 'negative';
    ELSE
        RETURN 'zero';
    END IF;
END;
$$ LANGUAGE plpgsql;
`)
	write("calc_test.sql", `
DO $$
BEGIN
    ASSERT classify(1) = 'positive', 'classify(1)';
END $$;
`)
	return dir
}

// ddlOnlyFixture has a passing test but nothing executable to instrument.
func ddlOnlyFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "schema.sql"),
		[]byte("CREATE TABLE t (id int);\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema_test.sql"),
		[]byte("INSERT INTO t(id) VALUES (1);\n"), 0o644); err != nil {
		t.Fatalf("write test: %v", err)
	}
	return dir
}

// failingFixture has a test that raises, so the run reports a test failure.
func failingFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "calc.sql"), []byte(`
CREATE OR REPLACE FUNCTION always_true() RETURNS BOOLEAN AS $$
BEGIN
    RETURN true;
END;
$$ LANGUAGE plpgsql;
`), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calc_test.sql"), []byte(`
DO $$
BEGIN
    PERFORM always_true();
    RAISE EXCEPTION 'deliberate failure';
END $$;
`), 0o644); err != nil {
		t.Fatalf("write test: %v", err)
	}
	return dir
}

// TestRun_FailUnderExitCodes covers the threshold's effect on the exit code,
// including the precedence rule: a test failure outranks the threshold, so a
// broken suite never reports a coverage problem instead of a broken test.
//
// It also pins what the threshold now measures. Since implicit DDL/DML no
// longer counts toward the percentage, a threshold that used to be satisfied by
// schema alone is not any more.
func TestRun_FailUnderExitCodes(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	newConfig := func(t *testing.T) *cli.Config {
		t.Helper()
		cfg := cli.NewConfig()
		cfg.ConnectionString = connString
		cfg.Timeout = 60 * time.Second
		cfg.CoverageFile = filepath.Join(t.TempDir(), "coverage.json")
		return cfg
	}

	t.Run("threshold met exits 0", func(t *testing.T) {
		cfg := newConfig(t)
		cfg.FailUnder = 10 // well below the partial coverage this fixture yields
		code, err := cli.Run(context.Background(), cfg, partiallyCoveredFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("threshold missed exits 1", func(t *testing.T) {
		cfg := newConfig(t)
		cfg.FailUnder = 99.9
		code, err := cli.Run(context.Background(), cfg, partiallyCoveredFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	})

	t.Run("threshold disabled ignores low coverage", func(t *testing.T) {
		cfg := newConfig(t)
		cfg.FailUnder = 0
		code, err := cli.Run(context.Background(), cfg, partiallyCoveredFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0 (threshold disabled)", code)
		}
	})

	t.Run("test failure outranks the threshold", func(t *testing.T) {
		cfg := newConfig(t)
		cfg.FailUnder = 1 // trivially satisfied; the failure must decide
		code, err := cli.Run(context.Background(), cfg, failingFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}

		cov, err := coverage.NewStore(cfg.CoverageFile).Load()
		if err != nil {
			t.Fatalf("load coverage: %v", err)
		}
		if cov.Version != coverage.SchemaVersion {
			t.Errorf("coverage version = %q, want %q", cov.Version, coverage.SchemaVersion)
		}
	})

	// A DDL-only project has nothing a threshold can measure. Before implicit
	// positions were split out it scored 100% and cleared any threshold; it must
	// now fail rather than pass on an empty measurement.
	t.Run("no executable statements cannot satisfy a threshold", func(t *testing.T) {
		cfg := newConfig(t)
		cfg.FailUnder = 80
		code, err := cli.Run(context.Background(), cfg, ddlOnlyFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 1 {
			t.Errorf("exit code = %d, want 1 (a threshold cannot be met with nothing to measure)", code)
		}

		cov, err := coverage.NewStore(cfg.CoverageFile).Load()
		if err != nil {
			t.Fatalf("load coverage: %v", err)
		}
		if _, total := cov.ExecutablePositionCounts(); total != 0 {
			t.Errorf("expected no executable positions in a DDL-only project, got %d", total)
		}
		if _, total := cov.ImplicitPositionCounts(); total == 0 {
			t.Error("expected the DDL statements to be recorded as implicit positions")
		}
	})

	t.Run("no threshold on a DDL-only project still exits 0", func(t *testing.T) {
		cfg := newConfig(t)
		code, err := cli.Run(context.Background(), cfg, ddlOnlyFixture(t))
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})
}
