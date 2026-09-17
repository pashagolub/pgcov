package runner

import (
	"path/filepath"
	"testing"

	"github.com/cybertec-postgresql/pgcov/internal/discovery"
	"github.com/cybertec-postgresql/pgcov/internal/instrument"
	"github.com/cybertec-postgresql/pgcov/internal/parser"
)

// src builds a minimal InstrumentedSQL whose originating file lives at path.
func src(path string) *instrument.InstrumentedSQL {
	return &instrument.InstrumentedSQL{
		Original: &parser.ParsedSQL{
			File: &discovery.DiscoveredFile{
				Path:         path,
				RelativePath: filepath.Base(path),
				Type:         discovery.FileTypeSource,
			},
		},
	}
}

func paths(sources []*instrument.InstrumentedSQL) []string {
	out := make([]string, 0, len(sources))
	for _, s := range sources {
		out = append(out, s.Original.File.Path)
	}
	return out
}

func TestFilterSourcesByDirectory(t *testing.T) {
	authDir := filepath.Join("proj", "auth")
	billDir := filepath.Join("proj", "billing")

	auth := src(filepath.Join(authDir, "authenticate.sql"))
	authHelpers := src(filepath.Join(authDir, "helpers.sql"))
	billing := src(filepath.Join(billDir, "invoice.sql"))

	all := []*instrument.InstrumentedSQL{auth, billing, authHelpers}

	tests := []struct {
		name string
		dir  string
		want []string
	}{
		{
			name: "keeps only co-located sources",
			dir:  authDir,
			want: []string{auth.Original.File.Path, authHelpers.Original.File.Path},
		},
		{
			name: "other directory",
			dir:  billDir,
			want: []string{billing.Original.File.Path},
		},
		{
			name: "directory with no sources yields nothing",
			dir:  filepath.Join("proj", "reporting"),
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paths(filterSourcesByDirectory(all, tt.dir))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// TestFilterSourcesByDirectory_EmptyInput guards the nil-slice contract that
// Execute relies on: no sources at all must not panic and must stay empty.
func TestFilterSourcesByDirectory_EmptyInput(t *testing.T) {
	if got := filterSourcesByDirectory(nil, "anywhere"); got != nil {
		t.Fatalf("expected nil for empty input, got %v", paths(got))
	}
}
