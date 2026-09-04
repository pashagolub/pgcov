package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/cybertec-postgresql/pgcov/internal/database"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/runner"
	"github.com/cybertec-postgresql/pgcov/internal/testutil"
	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// TestSQLFunctionReturnShapes exercises SQL-language function return types the
// main fixture does not cover, against a real server.
//
// Instrumenting a SQL function means adding a statement to its body, and in a
// SQL function the LAST statement determines the return type. Emitting the
// coverage signal after the statement broke that (the original B6 bug);
// replacing it with an unreferenced CTE preserved the type but never fired,
// because the planner prunes such a CTE. Emitting it before is the only form
// that both fires and leaves the return type alone - these cases pin that for
// the shapes most likely to break.
func TestSQLFunctionReturnShapes(t *testing.T) {
	connString, cleanup := testutil.SetupPostgresContainer(t)
	defer cleanup()

	ctx := context.Background()
	config := &types.Config{
		ConnectionString: connString,
		Timeout:          60 * time.Second,
		Parallelism:      1,
	}

	pool, err := database.NewPool(ctx, config)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	cases := []struct {
		name   string
		source string
		test   string
	}{
		{
			name: "returns void",
			source: `
CREATE TABLE audit (note text);

CREATE OR REPLACE FUNCTION record_note(note TEXT) RETURNS void AS $$
    INSERT INTO audit(note) VALUES (note);
$$ LANGUAGE sql;
`,
			test: `
SELECT record_note('hello');
DO $$
BEGIN
    ASSERT (SELECT count(*) FROM audit) = 1, 'record_note() should insert one row';
END $$;
`,
		},
		{
			name: "returns table",
			source: `
CREATE OR REPLACE FUNCTION pairs() RETURNS TABLE(k INT, v TEXT) AS $$
    SELECT g, 'v' || g FROM generate_series(1, 3) AS g;
$$ LANGUAGE sql;
`,
			test: `
DO $$
BEGIN
    ASSERT (SELECT count(*) FROM pairs()) = 3, 'pairs() should return 3 rows';
    ASSERT (SELECT v FROM pairs() WHERE k = 2) = 'v2', 'pairs() values should match';
END $$;
`,
		},
		{
			name: "returns setof",
			source: `
CREATE OR REPLACE FUNCTION nums() RETURNS SETOF INT AS $$
    SELECT generate_series(1, 4);
$$ LANGUAGE sql;
`,
			test: `
DO $$
BEGIN
    ASSERT (SELECT count(*) FROM nums()) = 4, 'nums() should return 4 rows';
END $$;
`,
		},
		{
			name: "multi statement returning composite",
			source: `
CREATE TABLE items (id serial primary key, label text);

CREATE OR REPLACE FUNCTION add_item(label TEXT) RETURNS items AS $$
    INSERT INTO items(label) VALUES (label);
    SELECT * FROM items ORDER BY id DESC LIMIT 1;
$$ LANGUAGE sql;
`,
			test: `
DO $$
DECLARE
    row items;
BEGIN
    row := add_item('widget');
    ASSERT row.label = 'widget', 'add_item() should return the inserted row';
END $$;
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "src.sql", tc.source)
			writeFile(t, root, "src_test.sql", tc.test)

			testFiles, instrumented := instrumentTreeN(t, root, 1, 1)

			executor := runner.NewExecutor(pool, config.Timeout, config.SignalTimeout,
				testing.Verbose(), instrument.DefaultChannel)
			runs, err := executor.ExecuteBatch(ctx, testFiles, instrumented)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(runs) != 1 {
				t.Fatalf("expected 1 run, got %d", len(runs))
			}
			if runs[0].Status != runner.TestPassed {
				t.Fatalf("test failed: %v (instrumentation must not change the "+
					"function's return type)", runs[0].Error)
			}

			// Every executable position in the SQL function must have fired.
			var executable, fired int
			seen := map[string]bool{}
			for _, sig := range runs[0].CoverageSigs {
				seen[sig.SignalID] = true
			}
			for _, src := range instrumented {
				for _, cp := range src.Locations {
					if cp.ImplicitCoverage {
						continue
					}
					executable++
					if seen[cp.SignalID] {
						fired++
					}
				}
			}
			if executable == 0 {
				t.Fatal("no executable positions were instrumented")
			}
			if fired != executable {
				t.Errorf("%d/%d SQL-function signals fired; every statement in a SQL "+
					"function body runs when the function is called, so all must fire",
					fired, executable)
			}
		})
	}
}
