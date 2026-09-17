package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadSetupScripts_OrderAndContent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "01_schema.sql", "CREATE SCHEMA app;")
	write(t, dir, "02_types.sql", "CREATE DOMAIN d AS int;")

	scripts, err := loadSetupScripts([]string{
		filepath.Join(dir, "01_schema.sql"),
		filepath.Join(dir, "02_types.sql"),
	})
	if err != nil {
		t.Fatalf("loadSetupScripts: %v", err)
	}
	if len(scripts) != 2 {
		t.Fatalf("expected 2 scripts, got %d", len(scripts))
	}
	if !strings.Contains(scripts[0].SQL, "CREATE SCHEMA") {
		t.Errorf("first script content = %q", scripts[0].SQL)
	}
	if filepath.Base(scripts[0].Name) != "01_schema.sql" {
		t.Errorf("explicit argument order not preserved: %v", scripts[0].Name)
	}
}

// TestLoadSetupScripts_GlobIsSorted pins determinism: a single glob must expand
// in a stable order, because setup scripts routinely depend on each other.
func TestLoadSetupScripts_GlobIsSorted(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"03_c.sql", "01_a.sql", "02_b.sql"} {
		write(t, dir, n, "SELECT 1;")
	}

	scripts, err := loadSetupScripts([]string{filepath.Join(dir, "*.sql")})
	if err != nil {
		t.Fatalf("loadSetupScripts: %v", err)
	}
	want := []string{"01_a.sql", "02_b.sql", "03_c.sql"}
	if len(scripts) != len(want) {
		t.Fatalf("expected %d scripts, got %d", len(want), len(scripts))
	}
	for i, w := range want {
		if got := filepath.Base(scripts[i].Name); got != w {
			t.Errorf("position %d = %q, want %q", i, got, w)
		}
	}
}

// TestLoadSetupScripts_Deduplicates covers overlapping patterns, e.g. a glob
// plus an explicit path naming the same file.
func TestLoadSetupScripts_Deduplicates(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.sql", "SELECT 1;")

	scripts, err := loadSetupScripts([]string{
		filepath.Join(dir, "*.sql"),
		filepath.Join(dir, "a.sql"),
	})
	if err != nil {
		t.Fatalf("loadSetupScripts: %v", err)
	}
	if len(scripts) != 1 {
		t.Fatalf("expected the duplicate to be dropped, got %d scripts", len(scripts))
	}
}

// TestLoadSetupScripts_MissingPatternIsAnError keeps a typo from silently
// skipping prerequisite schema, which would surface much later as a confusing
// load failure inside a temp database.
func TestLoadSetupScripts_MissingPatternIsAnError(t *testing.T) {
	dir := t.TempDir()

	_, err := loadSetupScripts([]string{filepath.Join(dir, "nope.sql")})
	if err == nil {
		t.Fatal("a pattern matching nothing must be an error")
	}
	if !strings.Contains(err.Error(), "nope.sql") {
		t.Errorf("error %q should name the offending pattern", err.Error())
	}
}

func TestLoadSetupScripts_UnreadableFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A directory matches the glob but cannot be read as a file.
	if _, err := loadSetupScripts([]string{filepath.Join(dir, "*")}); err == nil {
		t.Error("expected an error when a match cannot be read as a file")
	}
}

func TestLoadSetupScripts_Empty(t *testing.T) {
	scripts, err := loadSetupScripts(nil)
	if err != nil {
		t.Fatalf("loadSetupScripts(nil): %v", err)
	}
	if len(scripts) != 0 {
		t.Errorf("expected no scripts, got %d", len(scripts))
	}
}

// TestGenerateCoverageChannel_IsIdentifierSafe pins the contract the
// instrumenter relies on: the channel name is interpolated straight into
// LISTEN/pg_notify SQL without escaping, so it must stay within a
// lowercase/digit/underscore charset.
func TestGenerateCoverageChannel_IsIdentifierSafe(t *testing.T) {
	seen := make(map[string]bool)

	for i := 0; i < 100; i++ {
		ch, err := generateCoverageChannel()
		if err != nil {
			t.Fatalf("generateCoverageChannel: %v", err)
		}
		if !strings.HasPrefix(ch, "pgcov_") {
			t.Errorf("channel %q should carry the pgcov_ prefix", ch)
		}
		for _, r := range ch {
			isLower := r >= 'a' && r <= 'z'
			isDigit := r >= '0' && r <= '9'
			if !isLower && !isDigit && r != '_' {
				t.Fatalf("channel %q contains %q, which is not identifier-safe", ch, r)
			}
		}
		seen[ch] = true
	}

	// Per-run uniqueness is the point of the generator; 100 draws from 32 bits
	// of randomness colliding would indicate it is not random at all.
	if len(seen) < 99 {
		t.Errorf("expected ~100 distinct channels, got %d", len(seen))
	}
}
