package cli

import (
	"reflect"
	"testing"
	"time"
)

// setFlags is a FlagLookup listing the flags a user explicitly passed.
type setFlags map[string]bool

func (f setFlags) IsSet(name string) bool { return f[name] }

func TestApplyFlagsToConfig_NoFlagsPreserveConfig(t *testing.T) {
	original := Config{
		ConnectionString: "host=originalhost port=5433 user=originaluser dbname=originaldb",
		Timeout:          45 * time.Second,
		SignalTimeout:    250 * time.Millisecond,
		Parallelism:      2,
		CoverageFile:     "original.json",
		Verbose:          false,
	}
	cfg := original

	// Values are present but no flag was set: nothing may be applied.
	ApplyFlagsToConfig(&cfg, setFlags{}, RunFlags{
		Connection:    "host=ignored",
		Timeout:       time.Hour,
		SignalTimeout: time.Hour,
		Parallel:      99,
		CoverageFile:  "ignored.json",
		SetupFiles:    []string{"ignored.sql"},
		Verbose:       true,
		FailUnder:     99,
	})

	if !reflect.DeepEqual(cfg, original) {
		t.Errorf("unset flags must not change the config:\n got %+v\nwant %+v", cfg, original)
	}
}

func TestApplyFlagsToConfig_NilLookupIsANoop(t *testing.T) {
	original := Config{Timeout: 45 * time.Second}
	cfg := original
	ApplyFlagsToConfig(&cfg, nil, RunFlags{Timeout: time.Hour})
	if !reflect.DeepEqual(cfg, original) {
		t.Errorf("nil FlagLookup must leave the config untouched, got %+v", cfg)
	}
}

func TestApplyFlagsToConfig_SetFlagsApply(t *testing.T) {
	cfg := DefaultConfig
	ApplyFlagsToConfig(&cfg, setFlags{
		FlagConnection:    true,
		FlagTimeout:       true,
		FlagSignalTimeout: true,
		FlagParallel:      true,
		FlagCoverageFile:  true,
		FlagSetup:         true,
		FlagVerbose:       true,
		FlagFailUnder:     true,
	}, RunFlags{
		Connection:    "host=localhost",
		Timeout:       90 * time.Second,
		SignalTimeout: 500 * time.Millisecond,
		Parallel:      8,
		CoverageFile:  "out.json",
		SetupFiles:    []string{"a.sql", "glob/*.sql"},
		Verbose:       true,
		FailUnder:     80,
	})

	if cfg.ConnectionString != "host=localhost" {
		t.Errorf("connection = %q", cfg.ConnectionString)
	}
	if cfg.Timeout != 90*time.Second {
		t.Errorf("timeout = %v", cfg.Timeout)
	}
	if cfg.SignalTimeout != 500*time.Millisecond {
		t.Errorf("signal timeout = %v", cfg.SignalTimeout)
	}
	if cfg.Parallelism != 8 {
		t.Errorf("parallelism = %d", cfg.Parallelism)
	}
	if cfg.CoverageFile != "out.json" {
		t.Errorf("coverage file = %q", cfg.CoverageFile)
	}
	if len(cfg.SetupFiles) != 2 {
		t.Errorf("setup files = %v", cfg.SetupFiles)
	}
	if !cfg.Verbose {
		t.Error("verbose not applied")
	}
	if cfg.FailUnder != 80 {
		t.Errorf("fail-under = %v", cfg.FailUnder)
	}
}

// TestApplyFlagsToConfig_ExplicitZeroIsHonoured is the point of the change.
// Overrides used to be driven by comparing each value against its zero value,
// so an explicitly-passed zero was indistinguishable from an absent flag and
// silently dropped. `--timeout 0` and `--parallel 0` must now reach Validate,
// which is what rejects them.
func TestApplyFlagsToConfig_ExplicitZeroIsHonoured(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		flags RunFlags
		check func(Config) bool
		want  string
	}{
		{
			name:  "timeout",
			flag:  FlagTimeout,
			flags: RunFlags{Timeout: 0},
			check: func(c Config) bool { return c.Timeout == 0 },
			want:  "Timeout == 0",
		},
		{
			name:  "parallel",
			flag:  FlagParallel,
			flags: RunFlags{Parallel: 0},
			check: func(c Config) bool { return c.Parallelism == 0 },
			want:  "Parallelism == 0",
		},
		{
			name:  "signal timeout",
			flag:  FlagSignalTimeout,
			flags: RunFlags{SignalTimeout: 0},
			check: func(c Config) bool { return c.SignalTimeout == 0 },
			want:  "SignalTimeout == 0",
		},
		{
			name:  "fail-under",
			flag:  FlagFailUnder,
			flags: RunFlags{FailUnder: 0},
			check: func(c Config) bool { return c.FailUnder == 0 },
			want:  "FailUnder == 0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig
			cfg.FailUnder = 50 // non-zero starting point for the fail-under case
			ApplyFlagsToConfig(&cfg, setFlags{tc.flag: true}, tc.flags)
			if !tc.check(cfg) {
				t.Errorf("explicit zero for --%s was dropped; want %s, got %+v", tc.flag, tc.want, cfg)
			}
		})
	}
}

// TestNewConfigDoesNotShareStateWithDefault guards the other half: runCommand
// used to take &cli.DefaultConfig and mutate it, rewriting the package-level
// defaults for the rest of the process.
func TestNewConfigDoesNotShareStateWithDefault(t *testing.T) {
	before := DefaultConfig

	cfg := NewConfig()
	cfg.ConnectionString = "host=mutated"
	cfg.Parallelism = 99
	cfg.Verbose = true

	if !reflect.DeepEqual(DefaultConfig, before) {
		t.Errorf("mutating a NewConfig() result changed the shared defaults:\n got %+v\nwant %+v",
			DefaultConfig, before)
	}

	second := NewConfig()
	if second.ConnectionString != before.ConnectionString || second.Parallelism != before.Parallelism {
		t.Errorf("a later NewConfig() inherited state from an earlier one: %+v", second)
	}
}

func TestConfigValidate_ValidConfig(t *testing.T) {
	cfg := &Config{
		ConnectionString: "host=localhost port=5432 dbname=postgres",
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     ".pgcov/coverage.json",
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("valid config should not return error: %v", err)
	}
}

func TestConfigValidate_EmptyConnectionString(t *testing.T) {
	cfg := &Config{
		ConnectionString: "",
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     ".pgcov/coverage.json",
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for empty connection string")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "connection" {
		t.Errorf("expected error field 'connection', got '%s'", configErr.Field)
	}
}

func TestConfigValidate_InvalidTimeout(t *testing.T) {
	cfg := &Config{
		ConnectionString: "host=localhost port=5432 dbname=postgres",
		Timeout:          -1 * time.Second,
		Parallelism:      1,
		CoverageFile:     ".pgcov/coverage.json",
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for negative timeout")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "timeout" {
		t.Errorf("expected error field 'timeout', got '%s'", configErr.Field)
	}
}

func TestConfigValidate_InvalidParallelism(t *testing.T) {
	tests := []struct {
		name        string
		parallelism int
	}{
		{"zero parallelism", 0},
		{"negative parallelism", -1},
		{"too high parallelism", 101},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				ConnectionString: "host=localhost port=5432 dbname=postgres",
				Timeout:          30 * time.Second,
				Parallelism:      tt.parallelism,
				CoverageFile:     ".pgcov/coverage.json",
			}

			err := cfg.Validate()
			if err == nil {
				t.Errorf("expected validation error for parallelism %d", tt.parallelism)
			}

			configErr, ok := err.(*ConfigError)
			if !ok {
				t.Errorf("expected ConfigError, got %T", err)
			}
			if configErr.Field != "parallel" {
				t.Errorf("expected error field 'parallel', got '%s'", configErr.Field)
			}
		})
	}
}

func TestConfigValidate_EmptyCoverageFile(t *testing.T) {
	cfg := &Config{
		ConnectionString: "host=localhost port=5432 dbname=postgres",
		Timeout:          30 * time.Second,
		Parallelism:      1,
		CoverageFile:     "",
	}

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for empty coverage file")
	}

	configErr, ok := err.(*ConfigError)
	if !ok {
		t.Errorf("expected ConfigError, got %T", err)
	}
	if configErr.Field != "coverage-file" {
		t.Errorf("expected error field 'coverage-file', got '%s'", configErr.Field)
	}
	if configErr.Suggestion == "" {
		t.Error("expected suggestion to be provided")
	}
}

func TestConfigValidate_FailUnder(t *testing.T) {
	tests := []struct {
		name      string
		failUnder float64
		wantErr   bool
	}{
		{"zero disables", 0, false},
		{"valid boundary zero", 0.0, false},
		{"valid mid", 80.5, false},
		{"valid boundary hundred", 100, false},
		{"negative invalid", -1, true},
		{"too high invalid", 100.01, true},
		{"way too high invalid", 250, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				ConnectionString: "host=localhost port=5432 dbname=postgres",
				Timeout:          30 * time.Second,
				Parallelism:      1,
				CoverageFile:     ".pgcov/coverage.json",
				FailUnder:        tt.failUnder,
			}

			err := cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected validation error for fail-under=%g", tt.failUnder)
				}
				configErr, ok := err.(*ConfigError)
				if !ok {
					t.Fatalf("expected ConfigError, got %T", err)
				}
				if configErr.Field != "fail-under" {
					t.Errorf("expected field 'fail-under', got %q", configErr.Field)
				}
			} else if err != nil {
				t.Errorf("unexpected error for fail-under=%g: %v", tt.failUnder, err)
			}
		})
	}
}

func TestConfigError_Error(t *testing.T) {
	err := &ConfigError{
		Field:      "connection",
		Value:      "",
		Message:    "PostgreSQL connection string is required",
		Suggestion: "Set via --connection flag or standard PG* environment variables.",
	}

	errStr := err.Error()
	if errStr == "" {
		t.Error("error string should not be empty")
	}

	// Check that error contains field, message, and suggestion
	expectedSubstrings := []string{"connection", "PostgreSQL connection string is required", "Suggestion"}
	for _, substr := range expectedSubstrings {
		if !contains(errStr, substr) {
			t.Errorf("error string should contain '%s', got: %s", substr, errStr)
		}
	}
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
