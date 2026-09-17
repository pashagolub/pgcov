package cli_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/cli"
	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// writeCoverage persists a coverage file with the given per-position hits and
// returns its path.
func writeCoverage(t *testing.T, dir, name string, hits map[string]int) string {
	t.Helper()
	cov := coverage.NewCoverage()
	for key, count := range hits {
		startPos, length, err := coverage.ParsePositionKey(key)
		if err != nil {
			t.Fatalf("bad position key %q: %v", key, err)
		}
		cov.AddPosition("a.sql", startPos, length, count)
	}
	path := filepath.Join(dir, name)
	if err := coverage.NewStore(path).Save(cov); err != nil {
		t.Fatalf("save %s: %v", name, err)
	}
	return path
}

func TestMerge_RequiresAtLeastTwoInputs(t *testing.T) {
	dir := t.TempDir()
	one := writeCoverage(t, dir, "one.json", map[string]int{"0:10": 1})

	for _, inputs := range [][]string{nil, {one}} {
		err := cli.Merge(context.Background(), inputs, filepath.Join(dir, "out.json"))
		if err == nil {
			t.Errorf("Merge(%d inputs) must fail", len(inputs))
			continue
		}
		if !strings.Contains(err.Error(), "at least 2") {
			t.Errorf("error %q should explain the minimum", err.Error())
		}
	}
}

func TestMerge_WritesSummedOutputFile(t *testing.T) {
	dir := t.TempDir()
	a := writeCoverage(t, dir, "a.json", map[string]int{"0:10": 2, "20:5": 0})
	b := writeCoverage(t, dir, "b.json", map[string]int{"0:10": 3, "20:5": 1})
	out := filepath.Join(dir, "merged.json")

	if err := cli.Merge(context.Background(), []string{a, b}, out); err != nil {
		t.Fatalf("Merge: %v", err)
	}

	merged, err := coverage.NewStore(out).Load()
	if err != nil {
		t.Fatalf("load merged: %v", err)
	}
	if got := merged.Positions["a.sql"]["0:10"]; got != 5 {
		t.Errorf("0:10 = %d, want 5", got)
	}
	if got := merged.Positions["a.sql"]["20:5"]; got != 1 {
		t.Errorf("20:5 = %d, want 1", got)
	}
	if merged.Version != coverage.SchemaVersion {
		t.Errorf("merged version = %q, want %q", merged.Version, coverage.SchemaVersion)
	}
}

func TestMerge_MissingInputIsReported(t *testing.T) {
	dir := t.TempDir()
	a := writeCoverage(t, dir, "a.json", map[string]int{"0:10": 1})
	missing := filepath.Join(dir, "gone.json")

	err := cli.Merge(context.Background(), []string{a, missing}, filepath.Join(dir, "out.json"))
	if err == nil {
		t.Fatal("Merge must fail when an input does not exist")
	}
	if !strings.Contains(err.Error(), "gone.json") {
		t.Errorf("error %q should name the missing input", err.Error())
	}
}

func TestReport_RejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	cov := writeCoverage(t, dir, "coverage.json", map[string]int{"0:10": 1})

	err := cli.Report(context.Background(), cov, "yaml", filepath.Join(dir, "out"), "")
	if err == nil {
		t.Fatal("an unknown format must be rejected")
	}
	if !strings.Contains(err.Error(), "yaml") {
		t.Errorf("error %q should name the rejected format", err.Error())
	}
}

func TestReport_MissingCoverageFile(t *testing.T) {
	dir := t.TempDir()

	err := cli.Report(context.Background(), filepath.Join(dir, "nope.json"), "json", "-", "")
	if err == nil {
		t.Fatal("a missing coverage file must be reported")
	}
	if !strings.Contains(err.Error(), "pgcov run") {
		t.Errorf("error %q should point the user at 'pgcov run'", err.Error())
	}
}

func TestReport_WritesEachFormatToAFile(t *testing.T) {
	dir := t.TempDir()

	// A real source so the HTML and LCOV reporters have something to annotate.
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.sql"), []byte("SELECT 1;\nSELECT 2;\n"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 9, 1)
	cov.AddPosition("a.sql", 10, 9, 0)
	covPath := filepath.Join(dir, "coverage.json")
	if err := coverage.NewStore(covPath).Save(cov); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, format := range []string{"json", "lcov", "html"} {
		t.Run(format, func(t *testing.T) {
			out := filepath.Join(dir, "report."+format)
			if err := cli.Report(context.Background(), covPath, format, out, srcDir); err != nil {
				t.Fatalf("Report(%s): %v", format, err)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatalf("read output: %v", err)
			}
			if len(data) == 0 {
				t.Fatalf("%s report is empty", format)
			}
			if format == "json" {
				var parsed map[string]any
				if err := json.Unmarshal(data, &parsed); err != nil {
					t.Errorf("json report is not valid JSON: %v", err)
				}
			}
		})
	}
}

// TestReport_BaseDirResolvesSourcesFromAnotherCWD covers the --base-dir
// plumbing end to end: the reporter must find the source even though the
// process is running somewhere else entirely.
func TestReport_BaseDirResolvesSourcesFromAnotherCWD(t *testing.T) {
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const body = "SELECT 111;\nSELECT 222;\n"
	if err := os.WriteFile(filepath.Join(srcDir, "a.sql"), []byte(body), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cov := coverage.NewCoverage()
	cov.AddPosition("a.sql", 0, 11, 1)
	covPath := filepath.Join(root, "coverage.json")
	if err := coverage.NewStore(covPath).Save(cov); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Run from a directory that contains no sources at all.
	elsewhere := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(old); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()

	withBase := filepath.Join(root, "with-base.html")
	if err := cli.Report(context.Background(), covPath, "html", withBase, srcDir); err != nil {
		t.Fatalf("Report with base-dir: %v", err)
	}
	data, err := os.ReadFile(withBase)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "SELECT 111") {
		t.Error("--base-dir did not resolve the source: annotated text is missing from the HTML report")
	}

	withoutBase := filepath.Join(root, "no-base.html")
	if err := cli.Report(context.Background(), covPath, "html", withoutBase, ""); err != nil {
		t.Fatalf("Report without base-dir: %v", err)
	}
	data, err = os.ReadFile(withoutBase)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "SELECT 111") {
		t.Error("source resolved without --base-dir from an unrelated CWD; the test is not " +
			"actually exercising base-dir resolution")
	}
}
