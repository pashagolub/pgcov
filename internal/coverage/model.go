package coverage

import (
	"fmt"
	"os"
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

	// Root is the absolute discovery root the run used, i.e. the directory the
	// keys in Positions are relative to. It is a hint for resolving sources at
	// report time, not a requirement: --base-dir overrides it, and it is ignored
	// when the directory no longer exists (a different machine, a moved
	// checkout). Without it, `pgcov report` cannot find sources even when run
	// from the same directory as `pgcov run`, because the keys are relative to
	// the discovery root while resolution defaults to the working directory.
	Root string `json:"root,omitempty"`
}

// PositionHits represents position hit counts for a single file
type PositionHits map[string]int // Key: "startPos:length", Value: hit count

// SchemaVersion is the coverage-file schema this build reads and writes. It is
// the single source of truth for the Version field: Store.Load and Merge both
// refuse data stamped with anything else, so a file produced by an incompatible
// build fails loudly instead of being silently misinterpreted.
const SchemaVersion = "2.0"

// ValidateVersion reports whether c carries a schema version this build
// understands. The error names the offending version and tells the user how to
// fix it, because the remedy is to regenerate the data rather than to downgrade.
func (c *Coverage) ValidateVersion() error {
	if c == nil {
		return fmt.Errorf("coverage data is nil")
	}
	if c.Version == SchemaVersion {
		return nil
	}
	if c.Version == "" {
		return fmt.Errorf("coverage data has no schema version (expected %q); regenerate it with 'pgcov run'", SchemaVersion)
	}
	return fmt.Errorf("unsupported coverage schema version %q (this build reads %q); regenerate it with 'pgcov run'",
		c.Version, SchemaVersion)
}

// NewCoverage creates a new Coverage instance
func NewCoverage() *Coverage {
	return &Coverage{
		Version:   SchemaVersion,
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

// ResolveBaseDir decides which directory relative coverage keys are resolved
// against, in order of precedence:
//
//  1. an explicit base dir (the --base-dir flag), which always wins;
//  2. the discovery root recorded by the run, when it still exists;
//  3. "", meaning the process working directory.
//
// Step 2 is what makes `pgcov report` work with no flags: keys are relative to
// the run's discovery root, so resolving them against the working directory
// only happens to work when the two coincide.
func (c *Coverage) ResolveBaseDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if c.Root == "" {
		return ""
	}
	if info, err := os.Stat(c.Root); err == nil && info.IsDir() {
		return c.Root
	}
	// Recorded on another machine or in a moved checkout; fall back to the CWD.
	return ""
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
// per-position hit counts for each file. The result's Version is SchemaVersion
// and Timestamp is set to the current time. Input coverages are not mutated;
// the returned Coverage owns its position maps. Nil entries are skipped; an
// all-nil or empty input returns a freshly initialized Coverage with no
// positions.
//
// Every non-nil input must carry a schema version this build understands.
// Merge is the one operation that consumes files it did not write, so combining
// mismatched schemas would produce plausible-looking wrong output -- summed hit
// counts over keys that mean different things. It refuses instead.
func Merge(coverages ...*Coverage) (*Coverage, error) {
	result := NewCoverage()
	for i, c := range coverages {
		if c == nil {
			continue
		}
		if err := c.ValidateVersion(); err != nil {
			return nil, fmt.Errorf("coverage input %d: %w", i+1, err)
		}
		// Keep the first root seen so a merged file still resolves its
		// sources at report time. Shards of one run share a tree; if they
		// somehow do not, --base-dir remains the override.
		if result.Root == "" {
			result.Root = c.Root
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
	return result, nil
}

// Clone returns a deep copy of the Coverage struct
func (c *Coverage) Clone() *Coverage {
	clone := &Coverage{
		Version:   c.Version,
		Timestamp: c.Timestamp,
		Root:      c.Root,
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
