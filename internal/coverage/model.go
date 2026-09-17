package coverage

import (
	"fmt"
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Coverage represents aggregated coverage data across all tests
// Uses position-based coverage only (byte offsets)
type Coverage struct {
	Version   string    `json:"version"`   // Schema version (see SchemaVersion)
	Timestamp time.Time `json:"timestamp"` // When coverage collected

	// Positions holds *executable* statements only - the PL/pgSQL and SQL
	// function bodies instrumented with pg_notify. These are the positions a
	// test can leave uncovered, so they are the ones the coverage percentage
	// is computed over.
	Positions map[string]PositionHits `json:"positions"` // Key: root-relative file path, Value: position key -> hit count

	// ImplicitPositions holds DDL/DML statements, which are marked covered the
	// moment their source file loads successfully. They are kept separate
	// because they can never be uncovered: including them in the percentage
	// made every CREATE TABLE a permanently-100%-covered denominator entry and
	// inflated the headline number. Reporters still render them so those lines
	// stay highlighted.
	ImplicitPositions map[string]PositionHits `json:"implicit_positions,omitempty"`

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
		Version:           SchemaVersion,
		Timestamp:         time.Now(),
		Positions:         make(map[string]PositionHits),
		ImplicitPositions: make(map[string]PositionHits),
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

// AddImplicitPosition adds or updates coverage for a DDL/DML statement. These
// are tracked separately from executable positions; see Coverage.
func (c *Coverage) AddImplicitPosition(file string, startPos int, length int, hitCount int) {
	if c.ImplicitPositions == nil {
		c.ImplicitPositions = make(map[string]PositionHits)
	}
	if c.ImplicitPositions[file] == nil {
		c.ImplicitPositions[file] = make(PositionHits)
	}
	c.ImplicitPositions[file][formatPositionKey(startPos, length)] = hitCount
}

// AllPositions returns the union of executable and implicit positions for a
// file. Reporters use this for rendering so DDL/DML lines stay highlighted;
// percentages deliberately do not, because implicit positions can never be
// uncovered. The returned map is a fresh copy and may be nil when the file has
// no positions at all.
func (c *Coverage) AllPositions(file string) PositionHits {
	exec, implicit := c.Positions[file], c.ImplicitPositions[file]
	if len(exec) == 0 && len(implicit) == 0 {
		return nil
	}
	all := make(PositionHits, len(exec)+len(implicit))
	maps.Copy(all, implicit)
	maps.Copy(all, exec) // executable wins on the (impossible) key collision
	return all
}

// countHits returns how many of the given files' positions were hit at least
// once, and how many there are in total.
func countHits(byFile map[string]PositionHits) (covered int, total int) {
	for _, posHits := range byFile {
		for _, count := range posHits {
			total++
			if count > 0 {
				covered++
			}
		}
	}
	return covered, total
}

// ExecutablePositionCounts returns covered and total counts over executable
// statements - the numbers behind TotalPositionCoveragePercent.
func (c *Coverage) ExecutablePositionCounts() (covered int, total int) {
	return countHits(c.Positions)
}

// ImplicitPositionCounts returns covered and total counts over DDL/DML
// statements. covered equals total whenever every source file loaded.
func (c *Coverage) ImplicitPositionCounts() (covered int, total int) {
	return countHits(c.ImplicitPositions)
}

// HasExecutablePositions reports whether anything measurable was instrumented.
// When false the coverage percentage is not meaningful - there is nothing a
// test could have covered - and callers should say so rather than print 0%.
func (c *Coverage) HasExecutablePositions() bool {
	_, total := c.ExecutablePositionCounts()
	return total > 0
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

// TotalPositionCoveragePercent calculates overall coverage across *executable*
// statements. DDL/DML positions are excluded: they are marked covered as soon
// as their file loads, so counting them only inflated the result - a source
// file that is 90% DDL scored about 90% before a single assertion ran.
//
// Returns 0 when nothing executable was instrumented; callers that distinguish
// "nothing to measure" from "measured nothing" should check
// HasExecutablePositions first.
func (c *Coverage) TotalPositionCoveragePercent() float64 {
	covered, total := c.ExecutablePositionCounts()
	if total == 0 {
		return 0.0
	}
	return float64(covered) / float64(total) * 100.0
}

// TotalImplicitCoveragePercent calculates coverage across DDL/DML statements.
// This is 100% whenever every source file loaded successfully, and is reported
// alongside - never folded into - the executable number.
func (c *Coverage) TotalImplicitCoveragePercent() float64 {
	covered, total := c.ImplicitPositionCounts()
	if total == 0 {
		return 0.0
	}
	return float64(covered) / float64(total) * 100.0
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

// GetFiles returns a sorted list of all files with coverage data, executable or
// implicit. Reporters iterate this, so a file containing only DDL still gets a
// section rather than disappearing from the report.
func (c *Coverage) GetFiles() []string {
	seen := make(map[string]struct{}, len(c.Positions)+len(c.ImplicitPositions))
	for file := range c.Positions {
		seen[file] = struct{}{}
	}
	for file := range c.ImplicitPositions {
		seen[file] = struct{}{}
	}

	files := make([]string, 0, len(seen))
	for file := range seen {
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

		mergeInto(result.Positions, c.Positions)
		mergeInto(result.ImplicitPositions, c.ImplicitPositions)
	}
	return result, nil
}

// mergeInto adds src's per-file hit counts into dst.
func mergeInto(dst, src map[string]PositionHits) {
	for file, posHits := range src {
		if posHits == nil {
			continue
		}
		if dst[file] == nil {
			dst[file] = make(PositionHits)
		}
		for posKey, hits := range posHits {
			dst[file][posKey] += hits
		}
	}
}

// Clone returns a deep copy of the Coverage struct
func (c *Coverage) Clone() *Coverage {
	return &Coverage{
		Version:           c.Version,
		Timestamp:         c.Timestamp,
		Root:              c.Root,
		Positions:         clonePositions(c.Positions),
		ImplicitPositions: clonePositions(c.ImplicitPositions),
	}
}

// clonePositions deep-copies a per-file position map.
func clonePositions(src map[string]PositionHits) map[string]PositionHits {
	dst := make(map[string]PositionHits, len(src))
	for file, posHits := range src {
		clonedHits := make(PositionHits, len(posHits))
		maps.Copy(clonedHits, posHits)
		dst[file] = clonedHits
	}
	return dst
}
