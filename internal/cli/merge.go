package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// Merge loads two or more coverage JSON files, merges their per-position hit
// counts, and writes the result to outputPath. A "-" or empty outputPath
// writes the merged JSON to stdout; any other path is persisted via
// coverage.NewStore. Save.
func Merge(_ context.Context, inputs []string, outputPath string) error {
	if len(inputs) < 2 {
		return fmt.Errorf("merge requires at least 2 input coverage files (got %d)", len(inputs))
	}

	coverages := make([]*coverage.Coverage, 0, len(inputs))
	for _, path := range inputs {
		store := coverage.NewStore(path)
		cov, err := store.Load()
		if err != nil {
			return fmt.Errorf("failed to load %s: %w", path, err)
		}
		coverages = append(coverages, cov)
	}

	merged, err := coverage.Merge(coverages...)
	if err != nil {
		return fmt.Errorf("cannot merge coverage files: %w", err)
	}

	if outputPath == "" || outputPath == "-" {
		data, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal merged coverage: %w", err)
		}
		data = append(data, '\n')
		if _, err := os.Stdout.Write(data); err != nil {
			return fmt.Errorf("failed to write merged coverage to stdout: %w", err)
		}
		return nil
	}

	store := coverage.NewStore(outputPath)
	if err := store.Save(merged); err != nil {
		return fmt.Errorf("failed to save merged coverage: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Merged %d coverage files into %s\n", len(inputs), outputPath)
	return nil
}
