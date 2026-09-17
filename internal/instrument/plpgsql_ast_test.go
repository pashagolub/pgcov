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

func TestInstrumentPlpgsql_ComplexFunction(t *testing.T) {
	// Test a complex PL/pgSQL function with multiple control structures
	sql := `CREATE OR REPLACE FUNCTION calculate_discount(total_amount NUMERIC)
RETURNS NUMERIC AS $$
DECLARE
    discount_rate NUMERIC;
BEGIN
    IF total_amount > 1000 THEN
        discount_rate := 0.20;
    ELSIF total_amount > 500 THEN
        discount_rate := 0.10;
    ELSE
        discount_rate := 0.05;
    END IF;
    
    RETURN total_amount * discount_rate;
END;
$$ LANGUAGE plpgsql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "discount.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "discount.sql",
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

	// Should have NOTIFY calls
	if !strings.Contains(instrumented.InstrumentedText, "pg_notify") {
		t.Error("Instrument() did not inject NOTIFY calls")
	}

	// Should have coverage points for all executable statements (3 assignments + 1 return)
	if len(instrumented.Locations) != 4 {
		t.Errorf("Expected 4 coverage points, got %d", len(instrumented.Locations))
	}

	// Verify coverage points are positioned at the expected executable statements
	sqlContent, _ := os.ReadFile(tmpFile)
	sqlText := string(sqlContent)

	// Expected executable statements that should have coverage points
	expectedStatements := []string{
		"discount_rate := 0.20",
		"discount_rate := 0.10",
		"discount_rate := 0.05",
		"RETURN total_amount * discount_rate",
	}

	if len(instrumented.Locations) != len(expectedStatements) {
		t.Errorf("Expected %d coverage points, got %d", len(expectedStatements), len(instrumented.Locations))
	}

	for i, cp := range instrumented.Locations {
		if i >= len(expectedStatements) {
			break
		}

		// Extract the actual code at the coverage point position
		if cp.StartPos < 0 || cp.StartPos >= len(sqlText) {
			t.Errorf("Coverage point %d: invalid start position %d", i, cp.StartPos)
			continue
		}

		// Get a reasonable chunk of text around the coverage point to verify it's correct
		endPos := min(cp.StartPos+cp.Length, len(sqlText))
		actualText := strings.TrimSpace(sqlText[cp.StartPos:endPos])

		// Check if the coverage point contains the expected statement
		if !strings.Contains(actualText, expectedStatements[i]) {
			t.Errorf("Coverage point %d: expected to contain %q, got %q", i, expectedStatements[i], actualText)
		}

		if cp.ImplicitCoverage {
			t.Errorf("Coverage point %d: should not be implicit", i)
		}
	}

	// Verify PERFORM statements are injected after the executable lines (coverage-after-execute),
	// except for terminal statements (RETURN) which keep PERFORM before.
	// Note: After instrumentation, line numbers shift due to inserted PERFORM statements
	// We just verify that PERFORM statements exist for each coverage point
	for _, cp := range instrumented.Locations {
		signalID := cp.SignalID
		if !strings.Contains(instrumented.InstrumentedText, fmt.Sprintf("PERFORM pg_notify('pgcov', '%s')", signalID)) {
			t.Errorf("Missing PERFORM pg_notify for signal %s", signalID)
		}
	}
}

func TestInstrumentPlpgsql_WithLoop(t *testing.T) {
	sql := `CREATE OR REPLACE FUNCTION sum_to_n(n INT)
RETURNS INT AS $$
DECLARE
    total INT := 0;
    i INT;
BEGIN
    FOR i IN 1..n LOOP
        total := total + i;
    END LOOP;
    
    RETURN total;
END;
$$ LANGUAGE plpgsql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "loop.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "loop.sql",
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

	// Should have coverage points for the assignment inside the loop and the return
	if len(instrumented.Locations) < 2 {
		t.Errorf("Expected at least 2 coverage points, got %d", len(instrumented.Locations))
	}

	// Verify all coverage points are non-implicit
	for i, cp := range instrumented.Locations {
		if cp.ImplicitCoverage {
			t.Errorf("Coverage point %d: should not be implicit", i)
		}
	}
}

func TestInstrumentPlpgsql_DOBlock(t *testing.T) {
	// Test DO blocks which are also PL/pgSQL but not functions
	sql := `DO $$
BEGIN
    RAISE NOTICE 'Hello World';
END $$;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "do_block.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "do_block.sql",
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

	// DO blocks may not instrument with AST-only approach
	// This is acceptable as we only support properly formatted functions
	t.Logf("Coverage points: %d", len(instrumented.Locations))
}

func TestInstrumentPlpgsql_FallbackOnParseError(t *testing.T) {
	// Test that if PL/pgSQL parsing fails, we return without instrumentation
	// This is a malformed function that might not parse correctly
	sql := `CREATE FUNCTION bad_func() RETURNS void AS $$
BEGIN
    SELECT 1;
END;
$$ LANGUAGE plpgsql;`

	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "bad.sql")
	if err := os.WriteFile(tmpFile, []byte(sql), 0644); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}

	file := &discovery.DiscoveredFile{
		Path:         tmpFile,
		RelativePath: "bad.sql",
		Type:         discovery.FileTypeSource,
	}

	parsed, err := parser.Parse(file)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	// Should not fail even if PL/pgSQL parsing has issues
	instrumented, err := GenerateCoverageInstrument(parsed, DefaultChannel)
	if err != nil {
		t.Fatalf("Instrument() should not fail on parse errors, got: %v", err)
	}

	// With AST-only approach, may return no instrumentation for malformed SQL
	if instrumented == nil {
		t.Fatal("Instrument() returned nil")
	}
	t.Logf("Coverage points: %d (may be 0 for malformed SQL)", len(instrumented.Locations))
}

func TestFindTerminalPos_StartingTerminals(t *testing.T) {
	// Segments that start with a terminal should return pos 0.
	// Segments without any terminal should return -1.
	tests := []struct {
		name      string
		segment   string
		wantFound bool
	}{
		{"RETURN value", "RETURN a + b", true},
		{"RETURN bare", "RETURN", true},
		{"RAISE EXCEPTION", "RAISE EXCEPTION 'error'", true},
		{"RAISE with string (default EXCEPTION)", "RAISE 'something went wrong'", true},
		{"RAISE bare re-raise", "RAISE", true},
		{"RAISE NOTICE", "RAISE NOTICE 'hello'", false},
		{"RAISE WARNING", "RAISE WARNING 'warn'", false},
		{"RAISE INFO", "RAISE INFO 'info'", false},
		{"RAISE LOG", "RAISE LOG 'log'", false},
		{"RAISE DEBUG", "RAISE DEBUG 'debug'", false},
		{"assignment", "discount_rate := 0.20", false},
		{"IF block", "IF x > 0 THEN\n    y := 1", false},
		{"PERFORM", "PERFORM some_function()", false},
		{"SELECT", "SELECT 1 INTO result", false},
		{"comment then RETURN", "-- comment\nRETURN 42", true},
		{"comment then assignment", "-- comment\nx := 1", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findTerminalPos(tt.segment)
			if tt.wantFound && got < 0 {
				t.Errorf("findTerminalPos(%q) = -1, want >= 0", tt.segment)
			} else if !tt.wantFound && got >= 0 {
				t.Errorf("findTerminalPos(%q) = %d, want -1", tt.segment, got)
			}
		})
	}
}

func TestInstrumentBody_CoverageAfterExecute(t *testing.T) {
	// Verify that for non-terminal statements, NOTIFY comes after the statement,
	// and for RETURN, NOTIFY comes before.
	sql := `CREATE OR REPLACE FUNCTION example(x INT)
RETURNS INT AS $$
BEGIN
    x := x + 1;
    RETURN x;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}

	instrumentedSQL, coveragePoints := instrumentBody(stmts[0], "test.sql", true, false, DefaultChannel)
	if len(coveragePoints) != 2 {
		t.Fatalf("expected 2 coverage points, got %d", len(coveragePoints))
	}

	// The assignment "x := x + 1" should have NOTIFY *after* it.
	assignSignal := coveragePoints[0].SignalID
	assignNotify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", assignSignal)
	assignIdx := strings.Index(instrumentedSQL, assignNotify)
	assignStmtIdx := strings.Index(instrumentedSQL, "x := x + 1")
	if assignIdx < 0 || assignStmtIdx < 0 {
		t.Fatal("could not find assignment or its notify in instrumented SQL")
	}
	if assignIdx <= assignStmtIdx {
		t.Errorf("assignment: NOTIFY at %d should come after statement at %d (coverage-after-execute)", assignIdx, assignStmtIdx)
	}

	// The RETURN should have NOTIFY *before* it (terminal statement).
	returnSignal := coveragePoints[1].SignalID
	returnNotify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", returnSignal)
	returnIdx := strings.Index(instrumentedSQL, returnNotify)
	returnStmtIdx := strings.Index(instrumentedSQL, "RETURN x")
	if returnIdx < 0 || returnStmtIdx < 0 {
		t.Fatal("could not find RETURN or its notify in instrumented SQL")
	}
	if returnIdx >= returnStmtIdx {
		t.Errorf("RETURN: NOTIFY at %d should come before statement at %d (terminal statement)", returnIdx, returnStmtIdx)
	}

	t.Log(instrumentedSQL)
}

func TestInstrumentBody_RaiseExceptionBeforeNotify(t *testing.T) {
	// Use standalone RAISE statements so each is its own segment.
	sql := `CREATE OR REPLACE FUNCTION check_positive(x INT)
RETURNS VOID AS $$
BEGIN
    RAISE NOTICE 'checking value: %', x;
    RAISE EXCEPTION 'negative value: %', x;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}

	instrumentedSQL, coveragePoints := instrumentBody(stmts[0], "test.sql", true, false, DefaultChannel)
	if len(coveragePoints) != 2 {
		t.Fatalf("expected 2 coverage points, got %d", len(coveragePoints))
	}

	// RAISE NOTICE is non-terminal — NOTIFY should come after it.
	raiseNoticeSignal := coveragePoints[0].SignalID
	raiseNoticeNotify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", raiseNoticeSignal)
	raiseNoticeIdx := strings.Index(instrumentedSQL, raiseNoticeNotify)
	raiseNoticeStmtIdx := strings.Index(instrumentedSQL, "RAISE NOTICE")
	if raiseNoticeIdx < 0 || raiseNoticeStmtIdx < 0 {
		t.Fatal("could not find RAISE NOTICE or its notify")
	}
	if raiseNoticeIdx <= raiseNoticeStmtIdx {
		t.Errorf("RAISE NOTICE: NOTIFY at %d should come after statement at %d", raiseNoticeIdx, raiseNoticeStmtIdx)
	}

	// RAISE EXCEPTION is terminal — NOTIFY should come before it.
	raiseExcSignal := coveragePoints[1].SignalID
	raiseExcNotify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", raiseExcSignal)
	raiseExcIdx := strings.Index(instrumentedSQL, raiseExcNotify)
	raiseExcStmtIdx := strings.Index(instrumentedSQL, "RAISE EXCEPTION")
	if raiseExcIdx < 0 || raiseExcStmtIdx < 0 {
		t.Fatal("could not find RAISE EXCEPTION or its notify")
	}
	if raiseExcIdx >= raiseExcStmtIdx {
		t.Errorf("RAISE EXCEPTION: NOTIFY at %d should come before statement at %d", raiseExcIdx, raiseExcStmtIdx)
	}

	t.Log(instrumentedSQL)
}

func TestFindTerminalPos(t *testing.T) {
	tests := []struct {
		name      string
		segment   string
		wantFound bool   // whether a terminal is expected
		wantText  string // expected text at the found position (prefix check)
	}{
		{"bare RETURN", "RETURN x", true, "RETURN"},
		{"IF with RETURN", "IF x > 0 THEN\n        RETURN 1", true, "RETURN"},
		{"ELSIF with RETURN", "\n    ELSIF x > 5 THEN\n        RETURN 2", true, "RETURN"},
		{"ELSE with RETURN", "\n    ELSE\n        RETURN 3", true, "RETURN"},
		{"no terminal", "x := x + 1", false, ""},
		{"RAISE NOTICE", "RAISE NOTICE 'hello'", false, ""},
		{"IF with RAISE EXCEPTION", "IF x < 0 THEN\n        RAISE EXCEPTION 'bad'", true, "RAISE"},
		{"IF with non-terminal", "IF x > 0 THEN\n        x := 1", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findTerminalPos(tt.segment)
			if tt.wantFound {
				if got < 0 {
					t.Fatalf("findTerminalPos(%q) = -1, want >= 0", tt.segment)
				}
				if !strings.HasPrefix(tt.segment[got:], tt.wantText) {
					t.Errorf("findTerminalPos(%q) at %d: got %q, want prefix %q",
						tt.segment, got, tt.segment[got:], tt.wantText)
				}
			} else {
				if got >= 0 {
					t.Errorf("findTerminalPos(%q) = %d, want -1", tt.segment, got)
				}
			}
		})
	}
}

func TestInstrumentBody_ReturnInBranches(t *testing.T) {
	// IF/ELSIF/ELSE with RETURN in each branch.
	// All signals must be reachable (placed before the RETURN inside each branch).
	sql := `CREATE OR REPLACE FUNCTION check_stock(v_stock INT)
RETURNS TEXT AS $$
BEGIN
    IF v_stock = 0 THEN
        RETURN 'out_of_stock';
    ELSIF v_stock <= 10 THEN
        RETURN 'low_stock';
    ELSE
        RETURN 'in_stock';
    END IF;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}

	instrumentedSQL, coveragePoints := instrumentBody(stmts[0], "test.sql", true, false, DefaultChannel)
	if len(coveragePoints) != 3 {
		t.Fatalf("expected 3 coverage points, got %d", len(coveragePoints))
	}

	// For each coverage point, verify the NOTIFY comes BEFORE the RETURN
	// inside the branch—not after it (which would be unreachable).
	returns := []string{"RETURN 'out_of_stock'", "RETURN 'low_stock'", "RETURN 'in_stock'"}
	for i, cp := range coveragePoints {
		notify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", cp.SignalID)
		notifyIdx := strings.Index(instrumentedSQL, notify)
		returnIdx := strings.Index(instrumentedSQL, returns[i])
		if notifyIdx < 0 || returnIdx < 0 {
			t.Fatalf("cp %d: could not find notify or %s", i, returns[i])
		}
		if notifyIdx > returnIdx {
			t.Errorf("cp %d (%s): NOTIFY at %d after RETURN at %d — signal is unreachable",
				i, returns[i], notifyIdx, returnIdx)
		}
	}

	// Also verify the instrumented SQL does NOT have a PERFORM between
	// a RETURN and the next ELSIF/ELSE/END (that would be unreachable code).
	for _, kw := range []string{"ELSIF", "ELSE", "END IF"} {
		kwIdx := strings.Index(instrumentedSQL, kw)
		if kwIdx < 0 {
			continue
		}
		// Check a narrow window before the keyword for a rogue PERFORM.
		before := instrumentedSQL[max(0, kwIdx-80):kwIdx]
		// There should be a RETURN between the PERFORM and the keyword boundary.
		lastPerform := strings.LastIndex(before, "PERFORM pg_notify")
		lastReturn := strings.LastIndex(before, "RETURN")
		if lastPerform >= 0 && lastReturn >= 0 && lastPerform > lastReturn {
			t.Errorf("unreachable PERFORM found between RETURN and %s", kw)
		}
	}

	t.Log(instrumentedSQL)
}

func TestInstrumentBody_RaiseExceptionInBranch(t *testing.T) {
	// Segment: IF ... THEN RAISE EXCEPTION ... — terminal inside control structure.
	sql := `CREATE OR REPLACE FUNCTION validate(x INT)
RETURNS VOID AS $$
BEGIN
    IF x < 0 THEN
        RAISE EXCEPTION 'negative: %', x;
    ELSIF x = 0 THEN
        RAISE EXCEPTION 'zero';
    END IF;
    RAISE NOTICE 'ok: %', x;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}

	instrumentedSQL, coveragePoints := instrumentBody(stmts[0], "test.sql", true, false, DefaultChannel)
	if len(coveragePoints) != 3 {
		t.Fatalf("expected 3 coverage points, got %d", len(coveragePoints))
	}

	// First two are in IF/ELSIF branches with RAISE EXCEPTION (terminal).
	// Signals must appear before the RAISE EXCEPTION.
	for i, target := range []string{"RAISE EXCEPTION 'negative", "RAISE EXCEPTION 'zero"} {
		notify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", coveragePoints[i].SignalID)
		notifyIdx := strings.Index(instrumentedSQL, notify)
		stmtIdx := strings.Index(instrumentedSQL, target)
		if notifyIdx < 0 || stmtIdx < 0 {
			t.Fatalf("cp %d: could not find notify or %q", i, target)
		}
		if notifyIdx > stmtIdx {
			t.Errorf("cp %d: NOTIFY at %d after %q at %d — unreachable", i, notifyIdx, target, stmtIdx)
		}
	}

	// Third is RAISE NOTICE (non-terminal, standalone). Signal should come after.
	notify2 := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", coveragePoints[2].SignalID)
	notifyIdx2 := strings.Index(instrumentedSQL, notify2)
	noticeIdx := strings.Index(instrumentedSQL, "RAISE NOTICE")
	if notifyIdx2 < 0 || noticeIdx < 0 {
		t.Fatal("could not find RAISE NOTICE or its notify")
	}
	if notifyIdx2 <= noticeIdx {
		t.Errorf("RAISE NOTICE: NOTIFY at %d should come after statement at %d", notifyIdx2, noticeIdx)
	}

	t.Log(instrumentedSQL)
}

func TestInstrumentBody_MixedTerminalNonTerminalBranches(t *testing.T) {
	// Branch with RETURN vs branch with assignment — signal placement differs.
	sql := `CREATE OR REPLACE FUNCTION classify(x INT)
RETURNS TEXT AS $$
DECLARE
    result TEXT;
BEGIN
    IF x > 0 THEN
        result := 'positive';
    ELSE
        RETURN 'non-positive';
    END IF;
    RETURN result;
END;
$$ LANGUAGE plpgsql;`

	stmts := parser.ParseStatements(sql)
	if len(stmts) == 0 {
		t.Fatal("ParseStatements() returned no statements")
	}

	instrumentedSQL, coveragePoints := instrumentBody(stmts[0], "test.sql", true, false, DefaultChannel)
	if len(coveragePoints) != 3 {
		t.Fatalf("expected 3 coverage points, got %d", len(coveragePoints))
	}

	// cp0: IF ... result := 'positive' — no terminal, signal after.
	assign := "result := 'positive'"
	assignNotify := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", coveragePoints[0].SignalID)
	if strings.Index(instrumentedSQL, assignNotify) < strings.Index(instrumentedSQL, assign) {
		t.Error("assignment branch: NOTIFY should come AFTER the assignment")
	}

	// cp1: ELSE RETURN 'non-positive' — terminal inside branch, signal before.
	ret1 := "RETURN 'non-positive'"
	retNotify1 := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", coveragePoints[1].SignalID)
	if strings.Index(instrumentedSQL, retNotify1) > strings.Index(instrumentedSQL, ret1) {
		t.Error("ELSE RETURN branch: NOTIFY should come BEFORE the RETURN")
	}

	// cp2: standalone RETURN result — terminal at start, signal before.
	ret2 := "RETURN result"
	retNotify2 := fmt.Sprintf("PERFORM pg_notify('pgcov', '%s');", coveragePoints[2].SignalID)
	if strings.Index(instrumentedSQL, retNotify2) > strings.Index(instrumentedSQL, ret2) {
		t.Error("standalone RETURN: NOTIFY should come BEFORE the RETURN")
	}

	t.Log(instrumentedSQL)
}
