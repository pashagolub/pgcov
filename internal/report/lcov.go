package report

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// LCOVReporter formats coverage data in LCOV format
// LCOV format specification: https://github.com/linux-test-project/lcov
type LCOVReporter struct {
	// BaseDir, when non-empty, is the directory relative source paths are
	// resolved against before falling back to the process CWD. See
	// HTMLReporter.BaseDir for the same mechanism.
	BaseDir string
}

// NewLCOVReporter creates a new LCOV reporter
func NewLCOVReporter() *LCOVReporter {
	return &LCOVReporter{}
}

// SetBaseDir configures the base directory used to resolve relative source
// paths when reading files from disk. Passing an empty string clears it and
// falls back to the process CWD.
func (r *LCOVReporter) SetBaseDir(dir string) {
	r.BaseDir = dir
}

// Format formats coverage data as LCOV and writes to the writer
func (r *LCOVReporter) Format(cov *coverage.Coverage, writer io.Writer) error {
	// Only executable positions are emitted. Implicit DDL/DML is covered the
	// moment its file loads, so including it inflated the line percentage over
	// what `pgcov run` reports. Files with no executable statements are skipped.
	for _, file := range cov.GetFiles() {
		posHits := cov.Positions[file]
		if len(posHits) == 0 {
			continue
		}
		if err := r.formatFileFromPositions(file, posHits, writer); err != nil {
			return err
		}
	}

	return nil
}

// formatFileFromPositions formats a single file's coverage in LCOV format
// Converts position-based coverage to line-based for LCOV compatibility
func (r *LCOVReporter) formatFileFromPositions(path string, posHits coverage.PositionHits, writer io.Writer) error {
	// SF:<source file path>
	if _, err := fmt.Fprintf(writer, "SF:%s\n", path); err != nil {
		return err
	}

	// Read source file to convert positions to lines
	sourceText, err := r.readSourceFile(path)
	if err != nil {
		// If we can't read the file, output positions as line numbers (fallback)
		return r.formatPositionsAsLines(posHits, writer)
	}

	// Convert positions to line-based hits
	lineHits := r.convertPositionsToLines(sourceText, posHits)

	// Sort line numbers for deterministic output
	var lines []int
	for line := range lineHits {
		lines = append(lines, line)
	}
	sort.Ints(lines)

	// DA:<line number>,<hit count>
	for _, line := range lines {
		hitCount := lineHits[line]
		if _, err := fmt.Fprintf(writer, "DA:%d,%d\n", line, hitCount); err != nil {
			return err
		}
	}

	// LF:<number of instrumented lines>
	linesFound := len(lineHits)
	if _, err := fmt.Fprintf(writer, "LF:%d\n", linesFound); err != nil {
		return err
	}

	// LH:<number of lines with non-zero execution count>
	linesHit := 0
	for _, count := range lineHits {
		if count > 0 {
			linesHit++
		}
	}
	if _, err := fmt.Fprintf(writer, "LH:%d\n", linesHit); err != nil {
		return err
	}

	// end_of_record
	if _, err := fmt.Fprintf(writer, "end_of_record\n"); err != nil {
		return err
	}

	return nil
}

// convertPositionsToLines converts position-based hits to line-based hits
func (r *LCOVReporter) convertPositionsToLines(sourceText string, posHits coverage.PositionHits) map[int]int {
	lineHits := make(map[int]int)

	for posKey, hitCount := range posHits {
		startPos, _, err := coverage.ParsePositionKey(posKey)
		if err != nil {
			continue
		}

		// Convert position to line number
		line := r.positionToLine(sourceText, startPos)
		if line > 0 {
			// Accumulate hits on the same line
			lineHits[line] += hitCount
		}
	}

	return lineHits
}

// positionToLine converts a byte position to a line number (1-indexed)
func (r *LCOVReporter) positionToLine(sourceText string, pos int) int {
	if pos < 0 || pos > len(sourceText) {
		return 0
	}

	line := 1
	for i := 0; i < pos && i < len(sourceText); i++ {
		if sourceText[i] == '\n' {
			line++
		}
	}
	return line
}

// formatPositionsAsLines outputs positions directly (fallback when source not available)
func (r *LCOVReporter) formatPositionsAsLines(posHits coverage.PositionHits, writer io.Writer) error {
	// Sort by position for deterministic output
	type posEntry struct {
		pos      int
		hitCount int
	}
	var entries []posEntry

	for posKey, hitCount := range posHits {
		startPos, _, err := coverage.ParsePositionKey(posKey)
		if err != nil {
			continue
		}
		entries = append(entries, posEntry{pos: startPos, hitCount: hitCount})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].pos < entries[j].pos
	})

	// Output as if positions were line numbers (not ideal but better than nothing)
	for _, entry := range entries {
		if _, err := fmt.Fprintf(writer, "DA:%d,%d\n", entry.pos, entry.hitCount); err != nil {
			return err
		}
	}

	linesFound := len(entries)
	if _, err := fmt.Fprintf(writer, "LF:%d\n", linesFound); err != nil {
		return err
	}

	linesHit := 0
	for _, entry := range entries {
		if entry.hitCount > 0 {
			linesHit++
		}
	}
	if _, err := fmt.Fprintf(writer, "LH:%d\n", linesHit); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(writer, "end_of_record\n"); err != nil {
		return err
	}

	return nil
}

// readSourceFile reads a source file and returns its content as string.
// When BaseDir is set on the reporter, relative source paths are resolved
// against BaseDir first, then the process CWD. Absolute paths are tried as-is
// before falling back to CWD-relative resolution.
func (r *LCOVReporter) readSourceFile(filePath string) (string, error) {
	candidates := r.sourcePathCandidates(filePath)
	var lastErr error
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return string(data), nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidates generated")
	}
	return "", fmt.Errorf("cannot open file: %w", lastErr)
}

// sourcePathCandidates returns the ordered list of paths to try when reading a
// source file. Absolute paths skip base-dir resolution.
func (r *LCOVReporter) sourcePathCandidates(filePath string) []string {
	if filepath.IsAbs(filePath) {
		return []string{filePath}
	}
	var out []string
	if r.BaseDir != "" {
		out = append(out, filepath.Join(r.BaseDir, filepath.FromSlash(filePath)))
	}
	if cwd, err := os.Getwd(); err == nil {
		out = append(out, filepath.Join(cwd, filepath.FromSlash(filePath)))
	} else {
		out = append(out, filePath)
	}
	return out
}

// FormatString returns coverage data as an LCOV-formatted string
func (r *LCOVReporter) FormatString(cov *coverage.Coverage) (string, error) {
	var buf []byte
	writer := &byteWriter{data: &buf}
	if err := r.Format(cov, writer); err != nil {
		return "", err
	}
	return string(buf), nil
}

// Name returns the name of this reporter
func (r *LCOVReporter) Name() string {
	return "lcov"
}

// byteWriter is a simple io.Writer that writes to a byte slice
type byteWriter struct {
	data *[]byte
}

func (w *byteWriter) Write(p []byte) (n int, err error) {
	*w.data = append(*w.data, p...)
	return len(p), nil
}
