package main

import (
	"context"
	"fmt"
	"os"

	"github.com/cybertec-postgresql/pgcov/internal/cli"
	urfavecli "github.com/urfave/cli/v3"
)

const version = "1.0.0"

func main() {
	app := &urfavecli.Command{
		Name:    "pgcov",
		Usage:   "PostgreSQL test runner and coverage tool",
		Version: version,
		Commands: []*urfavecli.Command{
			{
				Name:   "run",
				Usage:  "Run tests and collect coverage",
				Action: runCommand,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{
						Name:    cli.FlagConnection,
						Aliases: []string{"c"},
						Usage:   "PostgreSQL connection string (URI or key=value format). Supports standard PG* environment variables.",
					},
					&urfavecli.DurationFlag{
						Name:  cli.FlagTimeout,
						Usage: "Per-test timeout",
					},
					&urfavecli.DurationFlag{
						Name:  cli.FlagSignalTimeout,
						Usage: "Grace period to wait for in-flight coverage NOTIFY signals after test SQL executes",
					},
					&urfavecli.IntFlag{
						Name:  cli.FlagParallel,
						Usage: "Maximum concurrent tests (1 = sequential)",
					},
					&urfavecli.StringFlag{
						Name:  cli.FlagCoverageFile,
						Usage: "Coverage data output path",
					},
					&urfavecli.StringSliceFlag{
						Name:  cli.FlagSetup,
						Usage: "SQL file(s) (globs allowed) run verbatim in each test's temp database before loading instrumented sources. Use for prerequisite schema the sources depend on. Repeatable; order preserved.",
					},
					&urfavecli.StringSliceFlag{
						Name:  cli.FlagSource,
						Usage: "Source file(s) (globs allowed) to instrument and load, in this order, for every test instead of the sources co-located with each test. Repeatable; order preserved.",
					},
					&urfavecli.BoolFlag{
						Name:  cli.FlagVerbose,
						Usage: "Enable debug output",
					},
					&urfavecli.Float64Flag{
						Name:  cli.FlagFailUnder,
						Usage: "Fail (exit 1) if total coverage percentage is below this threshold (0 = disabled)",
					},
				},
			},
			{
				Name:   "report",
				Usage:  "Generate coverage report",
				Action: reportCommand,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{
						Name:  "format",
						Usage: "Output format (json, lcov, or html)",
						Value: "json",
					},
					&urfavecli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "Output file path (use - for stdout)",
						Value:   "-",
					},
					&urfavecli.StringFlag{
						Name:  "coverage-file",
						Usage: "Coverage data input path",
						Value: ".pgcov/coverage.json",
					},
					&urfavecli.StringFlag{
						Name:  "base-dir",
						Usage: "Base directory used to resolve relative source paths in coverage data (default: current working directory)",
					},
				},
			},
			{
				Name:   "merge",
				Usage:  "Merge two or more coverage JSON files (positional args). Per-position hit counts are summed across inputs.",
				Action: mergeCommand,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{
						Name:    "output",
						Aliases: []string{"o"},
						Usage:   "Output file path (use - for stdout)",
						Value:   "-",
					},
				},
			},
			{
				Name:      "init",
				Usage:     "Generate a commented test scaffold for a source SQL file",
				ArgsUsage: "<source.sql>",
				Action:    initCommand,
				Flags: []urfavecli.Flag{
					&urfavecli.StringFlag{
						Name:        "output",
						Aliases:     []string{"o"},
						Usage:       "Output path for the scaffold (default: sibling <stem>_test.sql)",
						DefaultText: "<source_dir>/<stem>_test.sql",
					},
					&urfavecli.BoolFlag{
						Name:  "force",
						Usage: "Overwrite the output file if it already exists",
					},
				},
			},
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// runCommand handles the 'pgcov run' command
func runCommand(ctx context.Context, cmd *urfavecli.Command) error {
	// Start from a copy of the defaults. Taking &cli.DefaultConfig would mutate
	// the package-level template and leak settings between commands.
	config := cli.NewConfig()

	// Overrides are driven by cmd.IsSet, so an explicitly-passed zero value
	// (--timeout 0, --parallel 0) reaches Validate instead of being silently
	// dropped by a non-zero check.
	cli.ApplyFlagsToConfig(config, cmd, cli.RunFlags{
		Connection:    cmd.String(cli.FlagConnection),
		Timeout:       cmd.Duration(cli.FlagTimeout),
		SignalTimeout: cmd.Duration(cli.FlagSignalTimeout),
		Parallel:      cmd.Int(cli.FlagParallel),
		CoverageFile:  cmd.String(cli.FlagCoverageFile),
		SetupFiles:    cmd.StringSlice(cli.FlagSetup),
		SourceFiles:   cmd.StringSlice(cli.FlagSource),
		Verbose:       cmd.Bool(cli.FlagVerbose),
		FailUnder:     cmd.Float64(cli.FlagFailUnder),
	})

	// Validate configuration
	if err := config.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	// Get search path (first non-flag argument, default to current directory)
	searchPath := cmd.Args().First()
	if searchPath == "" {
		searchPath = "."
	}

	// Run tests
	exitCode, err := cli.Run(ctx, config, searchPath)
	if err != nil {
		return err
	}

	// Exit with appropriate code
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	return nil
}

// reportCommand handles the 'pgcov report' command
func reportCommand(ctx context.Context, cmd *urfavecli.Command) error {
	format := cmd.String("format")
	output := cmd.String("output")
	coverageFile := cmd.String("coverage-file")
	baseDir := cmd.String("base-dir")

	return cli.Report(ctx, coverageFile, format, output, baseDir)
}

// mergeCommand handles the 'pgcov merge' command
func mergeCommand(ctx context.Context, cmd *urfavecli.Command) error {
	output := cmd.String("output")
	inputs := cmd.Args().Slice()
	return cli.Merge(ctx, inputs, output)
}

// initCommand handles the 'pgcov init' command.
//
// It generates a commented test scaffold for an existing source SQL file,
// using the lexer-driven extraction in internal/cli to find CREATE
// [OR REPLACE] FUNCTION statements and emit a commented call template for
// each. The default target path is the sibling <stem>_test.sql.
func initCommand(_ context.Context, cmd *urfavecli.Command) error {
	source := cmd.Args().First()
	if source == "" {
		return fmt.Errorf("usage: pgcov init <source.sql> [--output PATH] [--force]")
	}
	output := cmd.String("output")
	force := cmd.Bool("force")

	outPath, err := cli.Init(source, output, force)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Scaffold written to %s\n", outPath)
	return nil
}
