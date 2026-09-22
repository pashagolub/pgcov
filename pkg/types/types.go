package types

import (
	"fmt"
	"time"
)

// Config holds runtime configuration combining flags, environment variables, and defaults
type Config struct {
	// PostgreSQL connection
	ConnectionString string // PostgreSQL connection string (URI or key=value format)

	// Execution
	SearchPath    string        // Root path for test/source discovery
	Timeout       time.Duration // Per-test timeout
	SignalTimeout time.Duration // Grace period to wait for in-flight coverage NOTIFY signals after test SQL executes
	Parallelism   int           // Max concurrent tests (1 = sequential)

	// SetupFiles are SQL files (globs allowed) executed verbatim — without
	// instrumentation and without being treated as sources-under-test — in each
	// test's temporary database BEFORE the instrumented sources are loaded.
	// Use this to create prerequisite schema/objects that the sources under test
	// depend on but that live outside the test directory (e.g. shared "global"
	// schemas). Order is preserved.
	SetupFiles []string

	// SourceFiles, when set, replace co-located source discovery: only these
	// files (globs allowed) are instrumented and loaded, in the order given,
	// for every test.
	SourceFiles []string

	// Output
	CoverageFile string // Coverage data output path
	Verbose      bool   // Enable debug logging

	// CoverageChannel is the PostgreSQL NOTIFY channel name used for coverage
	// signals.  When empty, the CLI generates a per-run unique channel
	// ("pgcov_<8 hex chars>") so coverage NOTIFY traffic inside the temp
	// database cannot collide with user code NOTIFYing on a well-known name.
	// Channel names are restricted to an identifier-safe charset (lowercase
	// letters, digits, underscore) so they can be safely interpolated into
	// LISTEN/<channel> SQL without escaping.
	CoverageChannel string

	// FailUnder is the minimum total coverage percentage required to exit 0
	// (0 = disabled).
	FailUnder float64
}

// ConfigError represents a configuration validation error
type ConfigError struct {
	Field      string
	Value      any
	Message    string
	Suggestion string
}

func (e *ConfigError) Error() string {
	msg := fmt.Sprintf("configuration error: %s", e.Message)
	if e.Field != "" {
		msg = fmt.Sprintf("configuration error for %s: %s", e.Field, e.Message)
	}
	if e.Suggestion != "" {
		msg += fmt.Sprintf("\n\nSuggestion: %s", e.Suggestion)
	}
	return msg
}

// Validate checks configuration for errors and returns helpful error messages
func (c *Config) Validate() error {
	// Validate connection string
	if c.ConnectionString == "" {
		return &ConfigError{
			Field:      "connection",
			Message:    "PostgreSQL connection string is required",
			Suggestion: "Set via --connection flag or standard PG* environment variables (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE).",
		}
	}

	// Validate timeout
	if c.Timeout <= 0 {
		return &ConfigError{
			Field:      "timeout",
			Value:      c.Timeout,
			Message:    "timeout must be positive",
			Suggestion: "Use --timeout flag with format like '30s', '1m', '90s'. Default is 30s.",
		}
	}

	// Validate parallelism
	if c.Parallelism < 1 {
		return &ConfigError{
			Field:      "parallel",
			Value:      c.Parallelism,
			Message:    fmt.Sprintf("parallelism must be at least 1, got: %d", c.Parallelism),
			Suggestion: "Use --parallel=N where N is number of tests to run concurrently. Use 1 for sequential execution.",
		}
	}

	if c.Parallelism > 100 {
		return &ConfigError{
			Field:      "parallel",
			Value:      c.Parallelism,
			Message:    fmt.Sprintf("parallelism too high: %d", c.Parallelism),
			Suggestion: "Consider a lower value to avoid overwhelming PostgreSQL connection limits. Recommended maximum: 100.",
		}
	}

	// Validate required fields
	if c.CoverageFile == "" {
		return &ConfigError{
			Field:      "coverage-file",
			Message:    "coverage file path is required",
			Suggestion: "Set via --coverage-file flag. Default is '.pgcov/coverage.json'.",
		}
	}

	// Validate fail-under threshold (0 disables, otherwise must be 0-100)
	if c.FailUnder < 0 || c.FailUnder > 100 {
		return &ConfigError{
			Field:      "fail-under",
			Value:      c.FailUnder,
			Message:    fmt.Sprintf("fail-under must be between 0 and 100, got: %g", c.FailUnder),
			Suggestion: "Use --fail-under=N where N is a percentage in [0, 100]. Use 0 to disable the threshold.",
		}
	}

	return nil
}

// CoverageSignal represents a single coverage signal emitted via NOTIFY
type CoverageSignal struct {
	SignalID  string    // Matches CoveragePoint.SignalID
	Timestamp time.Time // When signal received
}
