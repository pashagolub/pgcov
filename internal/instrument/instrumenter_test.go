package instrument

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
)

// getCoveragePointBySignal finds a coverage point by its signal ID. Used only in tests.
func getCoveragePointBySignal(instrumented *InstrumentedSQL, signalID string) *CoveragePoint {
	for i := range instrumented.Locations {
		if instrumented.Locations[i].SignalID == signalID {
			return &instrumented.Locations[i]
		}
	}
	return nil
}

func TestInstrumentWithLexer(t *testing.T) {
	sql := `CREATE OR REPLACE FUNCTION get_grade(score INT)
RETURNS TEXT AS $$
BEGIN
    IF score >= 90 THEN
        RETURN 'A';
    ELSIF score >= 80 THEN
        RETURN 'B';
    ELSIF score >= 70 THEN
        RETURN 'C';
    ELSE
        RETURN 'F';
    END IF;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}
	stmt := stmts[0]

	instrumentedSQL, coveragePoints := instrumentBody(stmt, "test.sql", true, false, DefaultChannel)
	if instrumentedSQL == "" {
		t.Error("instrumentWithLexer() returned empty instrumented SQL")
	}
	if len(coveragePoints) == 0 {
		t.Error("instrumentWithLexer() returned no coverage points")
	}

	// Should have NOTIFY calls injected
	if !strings.Contains(instrumentedSQL, "pg_notify") {
		t.Error("instrumentWithLexer() did not inject NOTIFY calls")
	}
	t.Log(instrumentedSQL)
}

func TestInstrument_BasicSQL(t *testing.T) {
	sql := "SELECT 1;"

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "test.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	if instrumented == nil {
		t.Fatal("Instrument() returned nil")
	}

	if len(instrumented.Locations) == 0 {
		t.Error("Instrument() produced no coverage points")
	}

	// Verify original is preserved
	if instrumented.Original != parsed {
		t.Error("Instrument() did not preserve original parsed SQL")
	}
}

func TestInstrument_PLpgSQLFunction(t *testing.T) {
	sql := `CREATE OR REPLACE FUNCTION add_numbers(a INT, b INT)
RETURNS INT AS $$
BEGIN
    RETURN a + b;
END;
$$ LANGUAGE plpgsql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "func.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "func.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Should have NOTIFY calls injected
	if !strings.Contains(instrumented.InstrumentedText, "pg_notify") {
		t.Error("Instrument() did not inject NOTIFY calls for PL/pgSQL function")
	}

	// Should have coverage points
	if len(instrumented.Locations) == 0 {
		t.Error("Instrument() produced no coverage points for PL/pgSQL function")
	}
}

func TestInstrument_SQLFunction_UsesLeadingNotify(t *testing.T) {
	sql := `CREATE OR REPLACE FUNCTION double_val(x INT)
RETURNS INT AS $$
    SELECT x * 2;
$$ LANGUAGE sql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sqlfunc.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "sqlfunc.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Should have coverage points
	if len(instrumented.Locations) == 0 {
		t.Fatal("Instrument() produced no coverage points for SQL function")
	}

	// SQL functions must be instrumented with a leading standalone
	// `SELECT pg_notify(...);` statement.
	//
	// This previously asserted the opposite - that the signal is a CTE prefix,
	// `WITH _pgcov_signal AS (SELECT pg_notify(...)) <stmt>`. That form
	// preserves the function's return type but never executes: the CTE is not
	// referenced by the main query and is not data-modifying, so the planner
	// prunes it and pg_notify never runs. SQL-language functions reported no
	// coverage at all.
	if !strings.Contains(instrumented.InstrumentedText, "SELECT pg_notify(") {
		t.Error("SQL function should be instrumented with a standalone SELECT pg_notify statement")
	}
	if strings.Contains(instrumented.InstrumentedText, "WITH _pgcov_signal AS") {
		t.Error("SQL function must not use the CTE form: an unreferenced CTE is pruned and never fires")
	}

	// The signal must come BEFORE the statement it marks. In a SQL function the
	// last statement determines the return type, so a trailing pg_notify (which
	// returns void) breaks the function - that was the original bug.
	text := instrumented.InstrumentedText
	notifyIdx := strings.Index(text, "SELECT pg_notify(")
	stmtIdx := strings.Index(text, "SELECT x * 2")
	if notifyIdx < 0 || stmtIdx < 0 {
		t.Fatalf("could not locate both the signal and the statement in:\n%s", text)
	}
	if notifyIdx > stmtIdx {
		t.Error("the coverage signal must precede the statement, or it becomes the " +
			"function's last statement and dictates the return type")
	}

	// Should NOT use the PL/pgSQL signal form
	if strings.Contains(instrumented.InstrumentedText, "EXECUTE 'SELECT pg_notify") {
		t.Error("SQL function should not use EXECUTE")
	}

	t.Logf("Instrumented SQL:\n%s", instrumented.InstrumentedText)
}

func TestInstrument_SQLFunction_MultipleStatements(t *testing.T) {
	// SQL functions can have multiple statements; the last one determines the return value
	sql := `CREATE OR REPLACE FUNCTION insert_and_count()
RETURNS BIGINT AS $$
    INSERT INTO log(msg) VALUES ('hello');
    SELECT count(*) FROM log;
$$ LANGUAGE sql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sqlfunc_multi.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "sqlfunc_multi.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Each statement gets its own leading `SELECT pg_notify(...);`. The CTE
	// form this replaced was never executed: an unreferenced, non-data-
	// modifying CTE is pruned by the planner, so the signal never fired.
	notifyCount := strings.Count(instrumented.InstrumentedText, "SELECT pg_notify(")
	if notifyCount < 2 {
		t.Errorf("Expected at least 2 pg_notify statements for multi-statement SQL function, got %d", notifyCount)
	}
	if strings.Contains(instrumented.InstrumentedText, "WITH _pgcov_signal AS") {
		t.Error("SQL functions must not use the CTE form: an unreferenced CTE is pruned and never fires")
	}

	t.Logf("Instrumented SQL:\n%s", instrumented.InstrumentedText)
}

func TestInstrument_MultipleStatements(t *testing.T) {
	sql := `SELECT 1;
SELECT 2;
SELECT 3;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "multi.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "multi.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Should have multiple coverage points (one per statement line)
	if len(instrumented.Locations) < 3 {
		t.Errorf("Instrument() got %d coverage points, want at least 3", len(instrumented.Locations))
	}
}

func TestInstrument_EmptyFile(t *testing.T) {
	sql := ""

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "empty.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "empty.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	if len(instrumented.Locations) != 0 {
		t.Errorf("Instrument() got %d coverage points for empty file, want 0", len(instrumented.Locations))
	}
}

func TestInstrument_CommentsOnly(t *testing.T) {
	sql := `-- Comment line 1
-- Comment line 2`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "comments.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "comments.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Comments should not produce coverage points
	if len(instrumented.Locations) != 0 {
		t.Errorf("Instrument() got %d coverage points for comments, want 0", len(instrumented.Locations))
	}
}

func TestInstrument_SignalIDFormat(t *testing.T) {
	sql := "SELECT 1;"

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "test.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	// Verify signal ID format (should be file:line or file:line:branch)
	for _, loc := range instrumented.Locations {
		if loc.SignalID == "" {
			t.Error("Instrument() produced empty SignalID")
		}

		// Signal ID should contain file and line
		if !strings.Contains(loc.SignalID, ":") {
			t.Errorf("Instrument() SignalID %q doesn't contain separator", loc.SignalID)
		}
	}
}

func TestInstrumentBatch(t *testing.T) {
	files := []string{
		"SELECT 1;",
		"SELECT 2;",
		"SELECT 3;",
	}

	tmpDir := t.TempDir()
	var parsedFiles []*parser.ParsedSQL

	for i, content := range files {
		tmpFile := filepath.Join(tmpDir, string(rune('a'+i))+".sql")
		if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write temp file: %v", err)
		}

		file := &discovery.DiscoveredFile{
			Path:         tmpFile,
			RelativePath: string(rune('a'+i)) + ".sql",
			Type:         discovery.FileTypeSource,
		}

		parsed, err := parser.Parse(file)
		if err != nil {
			t.Fatalf("Parse() error = %v", err)
		}
		parsedFiles = append(parsedFiles, parsed)
	}

	instrumented, err := GenerateCoverageInstruments(parsedFiles, DefaultChannel)
	if err != nil {
		t.Fatalf("InstrumentBatch() error = %v", err)
	}

	if len(instrumented) != len(files) {
		t.Errorf("InstrumentBatch() got %d results, want %d", len(instrumented), len(files))
	}
}

func TestGetCoveragePointBySignal(t *testing.T) {
	sql := "SELECT 1;"

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "test.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "test.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	if len(instrumented.Locations) == 0 {
		t.Fatal("No coverage points generated")
	}

	// Test finding a coverage point by signal
	signal := instrumented.Locations[0].SignalID
	cp := getCoveragePointBySignal(instrumented, signal)
	if cp == nil {
		t.Errorf("GetCoveragePointBySignal() returned nil for signal %q", signal)
	} else if cp.SignalID != signal {
		t.Errorf("GetCoveragePointBySignal() got signal %q, want %q", cp.SignalID, signal)
	}

	// Test non-existent signal
	cp = getCoveragePointBySignal(instrumented, "nonexistent:signal")
	if cp != nil {
		t.Errorf("GetCoveragePointBySignal() expected nil for nonexistent signal, got %v", cp)
	}

	// Test returned pointer refers to actual slice element, not a loop-copy
	cp = getCoveragePointBySignal(instrumented, signal)
	if cp != &instrumented.Locations[0] {
		t.Error("GetCoveragePointBySignal() returned pointer to copy, not to original slice element")
	}
}

func TestInstrument_NilInput(t *testing.T) {
	_, err := GenerateCoverageInstrument(nil, DefaultChannel)
	if err == nil {
		t.Error("Instrument() expected error for nil input, got nil")
	}
}

func TestInstrument_DifferentStatementTypes(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		wantLoc bool
	}{
		{
			name:    "SELECT",
			sql:     "SELECT * FROM users;",
			wantLoc: true,
		},
		{
			name:    "INSERT",
			sql:     "INSERT INTO users VALUES (1, 'Alice');",
			wantLoc: true,
		},
		{
			name:    "UPDATE",
			sql:     "UPDATE users SET name = 'Bob';",
			wantLoc: true,
		},
		{
			name:    "DELETE",
			sql:     "DELETE FROM users WHERE id = 1;",
			wantLoc: true,
		},
		{
			name:    "CREATE TABLE",
			sql:     "CREATE TABLE users (id INT);",
			wantLoc: true,
		},
		{
			name:    "DROP TABLE",
			sql:     "DROP TABLE users;",
			wantLoc: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			tmpFile := filepath.Join(tmpDir, "test.sql")
			if err := os.WriteFile(tmpFile, []byte(tt.sql), 0644); err != nil {
				t.Fatalf("failed to write temp file: %v", err)
			}

			file := &discovery.DiscoveredFile{
				Path:         tmpFile,
				RelativePath: "test.sql",
				Type:         discovery.FileTypeSource,
			}

			parsed, err := parser.Parse(file)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
			if err != nil {
				t.Fatalf("Instrument() error = %v", err)
			}

			hasLocations := len(instrumented.Locations) > 0
			if hasLocations != tt.wantLoc {
				t.Errorf("Instrument() hasLocations = %v, want %v", hasLocations, tt.wantLoc)
			}
		})
	}
}


// TestInstrument_CustomChannel_PLpgSQL verifies that the channel name passed to
// GenerateCoverageInstrument (and threaded through instrumentStatement /
// instrumentBody) appears verbatim in the injected pg_notify PERFORM calls.
func TestInstrument_CustomChannel_PLpgSQL(t *testing.T) {
	const ch = "pgcov_abcdef01"
	sql := `CREATE OR REPLACE FUNCTION add_numbers(a INT, b INT)
RETURNS INT AS $$
BEGIN
	RETURN a + b;
END;
$$ LANGUAGE plpgsql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "func.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "func.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, ch)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	want := fmt.Sprintf("EXECUTE 'SELECT pg_notify(''%s'',", ch)
	if !strings.Contains(instrumented.InstrumentedText, want) {
		t.Errorf("expected instrumented SQL to use custom channel %q, got:\n%s", ch, instrumented.InstrumentedText)
	}
	if strings.Contains(instrumented.InstrumentedText, "pg_notify('pgcov',") {
		t.Errorf("custom channel was ignored -- legacy 'pgcov' channel found in output")
	}
}

// TestInstrument_CustomChannel_SQLFunction verifies that the channel argument
// is used in the CTE form (WITH _pgcov_signal AS (SELECT pg_notify(<channel>, ...)))
// for SQL-language functions.
func TestInstrument_CustomChannel_SQLFunction(t *testing.T) {
	const ch = "pgcov_12345678"
	sql := `CREATE OR REPLACE FUNCTION double_val(x INT)
RETURNS INT AS $$
	SELECT x * 2;
$$ LANGUAGE sql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sqlfunc.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "sqlfunc.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	instrumented, err := GenerateCoverageInstrument(parsed, ch)
	if err != nil {
		t.Fatalf("Instrument() error = %v", err)
	}

	want := fmt.Sprintf("SELECT pg_notify('%s',", ch)
	if !strings.Contains(instrumented.InstrumentedText, want) {
		t.Errorf("expected leading pg_notify statement %q in instrumented SQL, got:\n%s", want, instrumented.InstrumentedText)
	}
	if strings.Contains(instrumented.InstrumentedText, "pg_notify('pgcov',") {
		t.Errorf("custom channel was ignored -- legacy 'pgcov' channel found in output")
	}
}

