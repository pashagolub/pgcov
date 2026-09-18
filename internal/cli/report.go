package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
	"github.com/cybertec-postgresql/pgcov/internal/report"
)

// Report generates a coverage report from saved coverage data.
// baseDir, when non-empty, is forwarded to the formatter so relative source
// paths are resolved against it instead of the process CWD. This lets
// `pgcov report` find sources when invoked from a directory other than the
// one `pgcov run` was executed from.
func Report(_ context.Context, coverageFile string, format string, outputPath string, baseDir string) error {

	// Step 1: Load coverage data
	store := coverage.NewStore(coverageFile)
	if !store.Exists() {
		return fmt.Errorf("coverage file not found: %s (run 'pgcov run' first)", coverageFile)
	}

	cov, err := store.Load()
	if err != nil {
		return fmt.Errorf("failed to load coverage data: %w", err)
	}

	// Step 2: Validate format
	if !report.ValidFormat(format) {
		return fmt.Errorf("unsupported format: %s (supported: %v)", format, report.SupportedFormats())
	}

	// Step 3: Get formatter
	formatter, err := report.GetFormatter(report.FormatType(format))
	if err != nil {
		return err
	}

	// Fall back to the discovery root recorded by the run. Coverage keys are
	// relative to that root, so resolving them against the working directory
	// only works when the two happen to coincide.
	baseDir = cov.ResolveBaseDir(baseDir)

	// Step 3a: Forward baseDir to formatters that support source-resolution.
	// The JSON reporter embeds no source and ignores BaseDir, so the
	// type-assertion fall-through is intentional.
	if baseDir != "" {
		if bd, ok := formatter.(interface{ SetBaseDir(string) }); ok {
			bd.SetBaseDir(baseDir)
		}
	}


	// Step 4: Format and output
	var writer *os.File
	if outputPath == "-" || outputPath == "" {
		// Write to stdout
		writer = os.Stdout
	} else {
		// Write to file
		writer, err = os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create output file: %w", err)
		}
		defer writer.Close()
	}

	// Format coverage data
	if err := formatter.Format(cov, writer); err != nil {
		return fmt.Errorf("failed to format coverage data: %w", err)
	}

	// Print success message to stderr (so it doesn't interfere with stdout output)
	if outputPath != "-" && outputPath != "" {
		fmt.Fprintf(os.Stderr, "Report written to %s\n", outputPath)
	}

	return nil
}


