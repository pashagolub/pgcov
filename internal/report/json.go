package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/cybertec-postgresql/pgcov/internal/coverage"
)

// JSONReporter formats coverage data as JSON
type JSONReporter struct{}

// NewJSONReporter creates a new JSON reporter
func NewJSONReporter() *JSONReporter {
	return &JSONReporter{}
}

// Format formats coverage data as JSON and writes to the writer
func (r *JSONReporter) Format(cov *coverage.Coverage, writer io.Writer) error {
	// Convert coverage to JSON format
	data, err := json.MarshalIndent(cov, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal coverage to JSON: %w", err)
	}

	// Write JSON to writer
	_, err = writer.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write JSON output: %w", err)
	}

	// Add newline
	_, err = writer.Write([]byte("\n"))
	return err
}

// FormatString returns coverage data as a JSON string
func (r *JSONReporter) FormatString(cov *coverage.Coverage) (string, error) {
	data, err := json.MarshalIndent(cov, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal coverage to JSON: %w", err)
	}
	return string(data), nil
}

// FormatSummary formats a summary view of coverage as JSON
func (r *JSONReporter) FormatSummary(cov *coverage.Coverage) (string, error) {
	execCovered, execTotal := cov.ExecutablePositionCounts()
	implicitCovered, implicitTotal := cov.ImplicitPositionCounts()

	summary := make(map[string]any)
	summary["version"] = cov.Version
	summary["timestamp"] = cov.Timestamp
	// total_coverage_percent counts executable statements only. DDL/DML is
	// reported alongside because it is covered the moment its file loads and
	// so cannot discriminate between a tested and an untested suite.
	summary["total_coverage_percent"] = cov.TotalPositionCoveragePercent()
	summary["executable_positions_covered"] = execCovered
	summary["executable_positions_total"] = execTotal
	summary["implicit_positions_covered"] = implicitCovered
	summary["implicit_positions_total"] = implicitTotal

	files := make(map[string]any)
	for _, path := range cov.GetFiles() {
		posHits := cov.Positions[path]
		covered := 0
		for _, count := range posHits {
			if count > 0 {
				covered++
			}
		}
		implicit := cov.ImplicitPositions[path]
		files[path] = map[string]any{
			"positions_covered":        covered,
			"positions_total":          len(posHits),
			"coverage_percent":         cov.PositionCoveragePercent(path),
			"implicit_positions_total": len(implicit),
		}
	}
	summary["files"] = files

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal summary to JSON: %w", err)
	}

	return string(data), nil
}

// Name returns the name of this reporter
func (r *JSONReporter) Name() string {
	return "json"
}
