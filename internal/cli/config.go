package cli

import (
	"time"

	"github.com/cybertec-postgresql/pgcov/pkg/types"
)

// Config is an alias for the shared Config type
type Config = types.Config

// ConfigError is an alias for the shared ConfigError type
type ConfigError = types.ConfigError

// DefaultConfig provides default configuration values.
//
// Treat this as a read-only template: copy it (`cfg := cli.DefaultConfig`)
// rather than taking its address. Mutating it through a pointer rewrites the
// defaults for the rest of the process, which leaks between commands in any
// caller that runs more than one.
var DefaultConfig = Config{
	ConnectionString: "",
	Timeout:          30 * time.Second,
	SignalTimeout:    100 * time.Millisecond,
	Parallelism:      1,
	CoverageFile:     ".pgcov/coverage.json",
	Verbose:          false,
}

// NewConfig returns a fresh copy of the defaults, ready to be overridden by
// flags. Preferred over &DefaultConfig, which hands out the shared template.
func NewConfig() *Config {
	cfg := DefaultConfig
	return &cfg
}

// Flag names for the `run` command, shared between the CLI wiring and the
// override logic so the two cannot drift apart.
const (
	FlagConnection    = "connection"
	FlagTimeout       = "timeout"
	FlagSignalTimeout = "signal-timeout"
	FlagParallel      = "parallel"
	FlagCoverageFile  = "coverage-file"
	FlagSetup         = "setup"
	FlagSource        = "source"
	FlagVerbose       = "verbose"
	FlagFailUnder     = "fail-under"
)

// FlagLookup reports whether a flag was explicitly provided on the command
// line. *urfave/cli.Command satisfies it.
//
// Overrides are driven by this rather than by comparing values against their
// zero value, which made explicitly-zero settings unreachable: `--timeout 0`
// and `--parallel 0` were silently ignored instead of being honoured or
// rejected.
type FlagLookup interface {
	IsSet(name string) bool
}

// RunFlags carries the raw flag values for the `run` command.
type RunFlags struct {
	Connection    string
	Timeout       time.Duration
	SignalTimeout time.Duration
	Parallel      int
	CoverageFile  string
	SetupFiles    []string
	SourceFiles   []string
	Verbose       bool
	FailUnder     float64
}

// ApplyFlagsToConfig overlays explicitly-provided flags onto c, leaving every
// other field at whatever the caller started from (normally NewConfig()).
//
// A nil set means no flag was provided and c is left untouched.
func ApplyFlagsToConfig(c *Config, set FlagLookup, f RunFlags) {
	if c == nil || set == nil {
		return
	}

	if set.IsSet(FlagConnection) {
		c.ConnectionString = f.Connection
	}
	if set.IsSet(FlagTimeout) {
		c.Timeout = f.Timeout
	}
	if set.IsSet(FlagSignalTimeout) {
		c.SignalTimeout = f.SignalTimeout
	}
	if set.IsSet(FlagParallel) {
		c.Parallelism = f.Parallel
	}
	if set.IsSet(FlagCoverageFile) {
		c.CoverageFile = f.CoverageFile
	}
	if set.IsSet(FlagSetup) {
		c.SetupFiles = f.SetupFiles
	}
	if set.IsSet(FlagSource) {
		c.SourceFiles = f.SourceFiles
	}
	if set.IsSet(FlagVerbose) {
		c.Verbose = f.Verbose
	}
	if set.IsSet(FlagFailUnder) {
		c.FailUnder = f.FailUnder
	}
}
