[![Coverage Status](https://coveralls.io/repos/github/cybertec-postgresql/pgcov/badge.svg)](https://coveralls.io/github/cybertec-postgresql/pgcov)

# pgcov

PostgreSQL test runner and coverage tool

## Overview

pgcov is a pure-Go CLI tool that discovers `*_test.sql` files, instruments PL/pgSQL and SQL function bodies for statement-level coverage tracking, executes tests in isolated temporary databases, and generates coverage reports in JSON, LCOV, and HTML formats. It talks to PostgreSQL directly over the wire protocol via `pgx/v5` — no `psql`, no server extensions, no CGO.

## Features

- 🧪 **Automatic Test Discovery**: Recursively finds `*_test.sql` files and the source files co-located with them
- 🔒 **Complete Test Isolation**: Each test runs in its own temporary database (`pgcov_test_<timestamp>_<random>`), dropped after the run
- 📊 **Coverage Tracking**: Statement-level coverage via `pg_notify` instrumentation signals captured over `LISTEN`/`NOTIFY`
- 📈 **Multiple Report Formats**: JSON, LCOV, and HTML output for CI/CD integration
- ⚡ **Parallel Execution**: Optional concurrent test execution with the `--parallel` flag
- 🐘 **PostgreSQL Native**: Direct protocol access via `pgx/v5`, no external CLI tools
- 🪶 **Pure Go**: SQL is parsed with `pashagolub/pglex`; a plain `go build` works on every platform, no C toolchain needed

## Prerequisites

- **Go**: 1.25 or later (for building)
- **PostgreSQL**: 13 or later, running and accessible (the server version is checked on connect)
- **Permissions**: The database user needs the `CREATEDB` privilege for test isolation

## Installation

### Building from Source

```bash
git clone https://github.com/cybertec-postgresql/pgcov.git
cd pgcov
go build -o pgcov ./cmd/pgcov
```

### Install to `GOPATH/bin`

```bash
go install github.com/cybertec-postgresql/pgcov/cmd/pgcov@latest
```

pgcov is pure Go — there is no CGO requirement and no C compiler involved.

## Quick Start

### 1. Connect to PostgreSQL

Pass a connection string to every `pgcov run` invocation (URI or keyword/value format):

```bash
pgcov run . --connection "postgresql://postgres:yourpassword@localhost:5432/postgres"

# or keyword/value format
pgcov run . --connection "host=localhost port=5432 user=postgres password=yourpassword dbname=postgres"
```

Fields omitted from the connection string fall back to the standard `PG*` environment variables (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, ...) and libpq defaults, handled by `pgx`.

### 2. Create Test Files

Test files must match the `*_test.sql` pattern and be co-located with the source files they exercise:

```
myproject/
├── auth/
│   ├── authenticate.sql      # Source (will be instrumented)
│   └── auth_test.sql         # Test
```

### 3. Run Tests

Discovery is recursive from the given path — there is no `./...` syntax:

```bash
# Current directory (recurses into subdirectories)
pgcov run . --connection "..."

# Specific directory (also recurses)
pgcov run ./tests/ --connection "..."
```

On success pgcov prints a summary and persists the raw coverage data:

```
Tests:    3 passed, 0 failed, 3 total
Coverage: 87.50% executable (14/16 statements)
          12/12 DDL/DML statements loaded
Time:     1.234s

Coverage data written to .pgcov/coverage.json
```

### 4. Generate Coverage Reports

```bash
# JSON (default format) to stdout
pgcov report

# HTML format (human-readable)
pgcov report --format=html -o coverage.html

# LCOV format (for CI)
pgcov report --format=lcov -o coverage.lcov
```

## Usage

### Commands

```bash
pgcov run [path] [flags]    # Run tests and collect coverage (path defaults to .)
pgcov report [flags]        # Generate a coverage report from saved coverage data
pgcov help [command]        # Show help
pgcov --version             # Show version
```

### `pgcov run` flags

| Flag | Default | Description |
|---|---|---|
| `--connection`, `-c` | *(required)* | PostgreSQL connection string (URI or keyword/value); omitted fields fall back to `PG*` environment variables |
| `--timeout` | `30s` | Per-test timeout (`10s`, `1m`, `90s`) |
| `--parallel` | `1` | Maximum concurrent tests; `1` = sequential, values above `100` are rejected |
| `--coverage-file` | `.pgcov/coverage.json` | Coverage data output path |
| `--setup` | — | SQL file(s) — globs allowed — run verbatim in each test's temporary database before the instrumented sources are loaded. Repeatable; order preserved |
| `--verbose` | `false` | Enable debug output |

### `pgcov report` flags

| Flag | Default | Description |
|---|---|---|
| `--format` | `json` | Output format: `json`, `lcov`, or `html` |
| `--output`, `-o` | `-` (stdout) | Output file path (`-` writes to stdout) |
| `--coverage-file` | `.pgcov/coverage.json` | Coverage data input path |

### Setup scripts (`--setup`)

Each test only deploys sources from its own directory into its temporary database. When sources depend on shared or "global" schema living elsewhere, load it with `--setup`. Setup scripts run verbatim — no instrumentation, not counted as covered source — in every test's temporary database before the instrumented sources:

```bash
pgcov run . --connection "..." \
  --setup "schema/global.sql" \
  --setup "shared_types/*.sql"
```

Glob matches are sorted for determinism, the flag is repeatable, and pattern order is preserved. A pattern that matches nothing is an error.

### Source deployment order

Within a single directory, source files are deployed in **lexical filename order**, because pgcov walks the tree with Go's `filepath.Walk` (which sorts entries lexicographically inside each directory). There is no `pgcov.toml` manifest or per-file ordering annotation — the filename is the only lever you control.

When sources have interdependencies, prefix the filenames with two-digit numbers so they sort the way you want:

```
auth/
├── 01_schema.sql         # Tables, types — must load first
├── 02_functions.sql      # Functions depend on tables from 01
├── 03_views.sql          # Views depend on functions from 02
└── auth_test.sql         # Test (excluded from the lexical sort pattern)
```

Sources are loaded in that order for every test in the directory. If a prerequisite lives **outside** the test directory (shared/global schema), load it verbatim with the repeatable `--setup` flag — setup runs before the instrumented sources and is not counted as covered source.

### Exit Codes

| Code | Meaning |
|---|---|
| `0` | All tests passed |
| `1` | Test failures, timeouts, or runtime errors |
| `2` | Invalid configuration |

### Configuration Validation

pgcov validates the configuration up front and reports actionable errors:

```bash
# Missing connection string
$ pgcov run .
Error: configuration error for connection: PostgreSQL connection string is required

Suggestion: Set via --connection flag or standard PG* environment variables (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE).

# Invalid parallelism
$ pgcov run . --connection "..." --parallel=-1
Error: configuration error for parallel: parallelism must be at least 1, got: -1

Suggestion: Use --parallel=N where N is number of tests to run concurrently. Use 1 for sequential execution.

# Invalid timeout
$ pgcov run . --connection "..." --timeout=-5s
Error: configuration error for timeout: timeout must be positive

Suggestion: Use --timeout flag with format like '30s', '1m', '90s'. Default is 30s.
```

## Writing Tests

### Test File Structure

```sql
-- auth_test.sql

-- Setup: Create schema and test data
CREATE TABLE users (id INT PRIMARY KEY, name TEXT);
INSERT INTO users VALUES (1, 'Alice'), (2, 'Bob');

-- Test: Verify behavior
DO $$
BEGIN
    IF NOT authenticate(1) THEN
        RAISE EXCEPTION 'Test failed: User 1 should authenticate';
    END IF;

    IF authenticate(999) THEN
        RAISE EXCEPTION 'Test failed: Invalid user should not authenticate';
    END IF;

    RAISE NOTICE 'All tests passed';
END;
$$;
```

### Source File Structure
Source files in the same directory as test files are automatically instrumented:

```sql
-- authenticate.sql

CREATE OR REPLACE FUNCTION authenticate(user_id INT) RETURNS BOOLEAN AS $$
BEGIN
    RETURN EXISTS(SELECT 1 FROM users WHERE id = user_id);
END;
$$ LANGUAGE plpgsql;
```

## CI/CD Integration

### GitHub Actions Example

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_PASSWORD: postgres
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
        ports:
          - 5432:5432

    steps:
      - uses: actions/checkout@v6

      - uses: actions/setup-go@v6
        with:
          go-version: '1.25'

      - name: Install pgcov
        run: go install github.com/cybertec-postgresql/pgcov/cmd/pgcov@latest

      - name: Run tests
        run: pgcov run . --connection "postgresql://postgres:postgres@localhost:5432/postgres"

      - name: Generate LCOV report
        run: pgcov report --format=lcov -o coverage.lcov

      - name: Upload coverage
        uses: codecov/codecov-action@v4
        with:
          files: coverage.lcov
```

See [examples/ci-integration](./examples/ci-integration) for GitHub Actions and GitLab CI variants.

## Architecture

- **CLI Layer**: `run` and `report` subcommands and flag wiring (`urfave/cli/v3`), `cmd/pgcov`
- **Discovery Layer**: Recursive filesystem scan; files ending in `_test.sql` are tests, all other `.sql` files are sources, and each test is scoped to sources from its own directory
- **Parser Layer**: Pure-Go SQL statement splitting and classification via `pashagolub/pglex`
- **Instrumentation Layer**: Statements inside PL/pgSQL function bodies get a `PERFORM pg_notify('pgcov', '<relPath>:<startOffset>:<byteLength>')` coverage signal; statements in SQL-language functions are wrapped in a `WITH _pgcov_signal AS (SELECT pg_notify(...))` CTE; all other DDL/DML counts as implicitly covered
- **Database Layer**: One temporary database per test (`pgcov_test_<yyyymmdd_hhmmss>_<random hex>`), created from the configured server and dropped after the run (`pgx/v5`)
- **Runner Layer**: Sequential executor, or a worker pool when `--parallel` is greater than 1; a dedicated connection `LISTEN`s on the `pgcov` channel for coverage signals during each test
- **Coverage Layer**: Signals are aggregated into per-file byte-range hit counts; every instrumented position is seeded with 0 hits so uncovered branches show up in reports
- **Reporter Layer**: Output formatting for JSON (default), LCOV, and HTML

## Development

### Running Tests

Unit tests need nothing but Go. Integration tests use testcontainers-go and require a running Docker daemon (they start a `postgres:16-alpine` container):

```bash
# All tests
go test ./...

# Verbose output
go test -v ./...

# A specific integration test
go test -v ./internal/ -run TestEndToEndWithTestcontainers

# Longer timeout (integration tests pull and start containers)
go test -timeout 5m ./...

# With Go coverage
go test -cover ./...
```

Other integration tests in the same package: `TestRunnerIsolation`, `TestOrderIndependence`, `TestTestIndependence`, `TestSQLFunctionInstrumentation`.

### Building

```bash
# Development build
go build -o pgcov ./cmd/pgcov

# Release build with optimizations
go build -ldflags="-s -w" -o pgcov ./cmd/pgcov

# Format and lint
go fmt ./...
go vet ./...
golangci-lint run   # matches CI
```

### Troubleshooting

**Test container startup failures**:

```bash
# Verify Docker is running
docker ps

# Pull the PostgreSQL image manually
docker pull postgres:16-alpine
```

## VS Code Integration

The repository ships `.vscode` configuration covering common workflows:

- **settings.json** — gopls environment and Go test defaults
- **launch.json** — debug configurations: launch the CLI, debug the package or file under the cursor, and run the integration test suite (5-minute timeout)
- **tasks.json** — build (default: `Ctrl+Shift+B`), test, coverage, lint, format, and `go mod tidy` tasks

See [.vscode/README.md](.vscode/README.md) for details.

## License

MIT

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) and open an issue or pull request.

## Support

- **Documentation**: [`docs/`](./docs) — [quickstart](./docs/quickstart.md), [CLI contract](./docs/cli-contract.md)
- **Issues**: [GitHub Issues](https://github.com/cybertec-postgresql/pgcov/issues)
- **Examples**: [Examples directory](./examples)
