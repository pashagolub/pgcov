package coverage

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Coverage represents aggregated coverage data across all tests
// Uses position-based coverage only (byte offsets)
type Coverage struct {
	Version   string                  `json:"version"`   // Schema version (e.g., "1.0")
	Timestamp time.Time               `json:"timestamp"` // When coverage collected
	Positions map[string]PositionHits `json:"positions"` // Key: relative file path, Value: map of position keys to hit counts
}

// PositionHits represents position hit counts for a single file
type PositionHits map[string]int // Key: "startPos:length", Value: hit count

// NewCoverage creates a new Coverage instance
func NewCoverage() *Coverage {
	return &Coverage{
		Version:   "1.0",
		Timestamp: time.Now(),
		Positions: make(map[string]PositionHits),
	}
}

// AddPosition adds or updates position-based coverage data
func (c *Coverage) AddPosition(file string, startPos int, length int, hitCount int) {
	if c.Positions == nil {
		c.Positions = make(map[string]PositionHits)
	}
	if c.Positions[file] == nil {
		c.Positions[file] = make(PositionHits)
	}
	posKey := formatPositionKey(startPos, length)
	c.Positions[file][posKey] = hitCount
}

// PositionCoveragePercent calculates position coverage percentage for a file
func (c *Coverage) PositionCoveragePercent(file string) float64 {
	posHits := c.Positions[file]
	if len(posHits) == 0 {
		return 0.0
	}

	covered := 0
	for _, count := range posHits {
		if count > 0 {
			covered++
		}
	}

	return float64(covered) / float64(len(posHits)) * 100.0
}

// TotalPositionCoveragePercent calculates overall position coverage percentage
func (c *Coverage) TotalPositionCoveragePercent() float64 {
	totalPositions := 0
	coveredPositions := 0

	for _, posHits := range c.Positions {
		for _, count := range posHits {
			totalPositions++
			if count > 0 {
				coveredPositions++
			}
		}
	}

	if totalPositions == 0 {
		return 0.0
	}

	return float64(coveredPositions) / float64(totalPositions) * 100.0
}

// formatPositionKey creates a string key from startPos and length
func formatPositionKey(startPos int, length int) string {
	return fmt.Sprintf("%d:%d", startPos, length)
}

// ParsePositionKey parses a "startPos:length" key back into its two numbers.
//
// Both halves must be complete decimal integers. fmt.Sscanf("%d:%d") was used
// here previously and accepted trailing garbage - "10:20junk" parsed as
// (10, 20) with a nil error - which let a hand-edited or third-party coverage
// file reach the reporters as plausible-looking nonsense.
func ParsePositionKey(posKey string) (startPos int, length int, err error) {
	startStr, lenStr, found := strings.Cut(posKey, ":")
	if !found {
		return 0, 0, fmt.Errorf("invalid position key %q: expected \"startPos:length\"", posKey)
	}

	startPos, err = strconv.Atoi(startStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start position in position key %q: %w", posKey, err)
	}

	length, err = strconv.Atoi(lenStr)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid length in position key %q: %w", posKey, err)
	}

	if startPos < 0 {
		return 0, 0, fmt.Errorf("start position must be non-negative in position key %q", posKey)
	}
	if length < 0 {
		return 0, 0, fmt.Errorf("length must be non-negative in position key %q", posKey)
	}

	return startPos, length, nil
}

// GetFiles returns a sorted list of all files with coverage data
func (c *Coverage) GetFiles() []string {
	var files []string
	for file := range c.Positions {
		files = append(files, file)
	}
	sort.Strings(files)
	return files
}

// Merge combines multiple Coverage objects into a single Coverage by summing
// per-position hit counts for each file. The result's Version mirrors
// NewCoverage's "1.0" schema identifier and Timestamp is set to the current
// time. Input coverages are not mutated; the returned Coverage owns its
// position maps. Nil entries are skipped; an all-nil or empty input returns a
// freshly initialized Coverage with no positions.
func Merge(coverages ...*Coverage) *Coverage {
	result := NewCoverage()
	for _, c := range coverages {
		if c == nil {
			continue
		}
		for file, posHits := range c.Positions {
			if posHits == nil {
				continue
			}
			if result.Positions[file] == nil {
				result.Positions[file] = make(PositionHits)
			}
			for posKey, hits := range posHits {
				result.Positions[file][posKey] += hits
			}
		}
	}
	return result
}

// Clone returns a deep copy of the Coverage struct
func (c *Coverage) Clone() *Coverage {
	clone := &Coverage{
		Version:   c.Version,
		Timestamp: c.Timestamp,
		Positions: make(map[string]PositionHits, len(c.Positions)),
	}
	for file, posHits := range c.Positions {
		clonedHits := make(PositionHits, len(posHits))
		for k, v := range posHits {
			clonedHits[k] = v
		}
		clone.Positions[file] = clonedHits
	}
	return clone
}
