# CLI Contract: pgcov

**Version**: 1.0  
**Date**: 2026-08-14

## Overview

This document defines the command-line interface contract for pgcov, including commands, flags, exit codes, and output formats.

---

## Commands

### `pgcov run [path]`

Discover tests and source files, execute tests with coverage tracking, and generate coverage data.

**Arguments**:
- `[path]`: Directory or pattern to search (default: `.`)
  - `.` - Current directory only
  - `./...` - Recursive from current directory (Go-style)
  - `./tests/` - Specific directory

**Flags**:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--connection`, `-c` | string | (empty) | PostgreSQL connection string (URI or `key=value` format). When omitted, `pgx` falls back to its standard `PG*` environment variables (`PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`, …). |
| `--timeout` | duration | `30s` | Per-test timeout |
| `--parallel` | int | `1` | Maximum concurrent tests (`1` = sequential) |
| `--coverage-file` | string | `.pgcov/coverage.json` | Coverage data output path |
| `--setup` | string (repeatable) | (none) | SQL file(s) (globs allowed) executed verbatim in each test's temp database before loading instrumented sources. Use for prerequisite schema the sources depend on. Repeatable; order preserved. |
| `--verbose` | bool | `false` | Enable debug output |

**Exit Codes**:
- `0`: All tests passed — also returned when no `*_test.sql` files are discovered (a message is printed)
- `1`: One or more tests failed, or a runtime error occurred (e.g. failed discovery, parse, instrumentation, database connection, or test execution)
- `2`: Configuration error (e.g. invalid flags, missing connection string, non-positive timeout, parallelism outside `1..100`)

**stdout Output**:

```
pgcov: discovering tests in .
Found 3 test file(s)
Found 5 source file(s)
Connected to PostgreSQL

Tests:    2 passed, 1 failed, 3 total
Coverage: 78.50% executable (157/200 statements)
          42/42 DDL/DML statements loaded
Time:     4.1s

Coverage data written to .pgcov/coverage.json
```

When one or more tests exceed `--timeout`, a `timed out` segment is added and
those tests are listed with a `TIMEOUT ` prefix instead of `FAILED `:

```
TIMEOUT slow_test.sql: test execution failed: context deadline exceeded

Tests:    1 passed, 0 failed, 1 timed out, 2 total
```

When no test files are found:

```
No test files found (*_test.sql)
```

**stderr Output** (errors only):

```
Error: database connection failed: failed to connect to PostgreSQL: ...

Suggestion: Set via --connection flag or standard PG* environment variables (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE).
```

**Environment Variables**:
Standard PostgreSQL / `pgx` environment variables are honored by the underlying connection layer when `--connection` is omitted or partial:
- `PGHOST` — PostgreSQL host
- `PGPORT` — PostgreSQL port
- `PGUSER` — PostgreSQL user
- `PGPASSWORD` — PostgreSQL password
- `PGDATABASE` — Template database

pgcov itself does not document per-flag PG\* overrides; all connection configuration is funneled through `--connection`.

---

### `pgcov report`

Generate coverage report from existing coverage data.

**Arguments**: None

**Flags**:

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--format` | string | `json` | Output format (`json`, `lcov`, or `html`) |
| `--output`, `-o` | string | `-` | Output file path (use `-` for stdout) |
| `--coverage-file` | string | `.pgcov/coverage.json` | Coverage data input path |

**Exit Codes**:
- `0`: Report generated successfully
- `1`: Coverage data file not found, failed to parse, unsupported format, or output write failure

**stdout Output** (`--format=json`):

```json
{
  "version": "2.0",
  "timestamp": "2026-08-14T16:00:00Z",
  "positions": {
    "src/auth.sql": {
      "0:42": 5,
      "42:128": 5,
      "170:37": 3
    }
  }
}
```

Position keys are `"<startByteOffset>:<byteLength>"`; values are integer hit counts.

**stdout Output** (`--format=lcov`):

```
TN:
SF:src/auth.sql
DA:1,5
DA:2,5
DA:5,3
LF:3
LH:2
end_of_record
```

The LCOV reporter converts the stored byte-offset positions to line numbers by reading each source file (positions are accumulated onto the line they start on). When a source file cannot be read, it falls back to emitting `DA:<startByteOffset>,<hitCount>` instead.

---

### `pgcov help [command]`

Display help information.

**Arguments**:
- `[command]`: Optional command name for detailed help

**Exit Codes**:
- `0`: Always

**stdout Output**:

```
NAME:
   pgcov - PostgreSQL test runner and coverage tool

USAGE:
   pgcov [global options] command [command options] [arguments...]

VERSION:
   1.0.0

COMMANDS:
   run      Run tests and collect coverage
   report   Generate coverage report
   help     Show help

GLOBAL OPTIONS:
   --help, -h     show help
   --version, -v  print the version
```

---

### `pgcov --version`

Display version information.

**Exit Codes**:
- `0`: Always

**stdout Output**:

```
pgcov version 1.0.0
```

---

## Coverage Data File Contract

### File Path

Default: `.pgcov/coverage.json`  
Configurable via: `--coverage-file` flag on `run` and `report`

### JSON Schema

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["version", "timestamp", "positions"],
  "properties": {
    "version": {
      "type": "string",
      "const": "2.0",
      "description": "Coverage-file schema version. Readers must reject any other value rather than guess; regenerate the data with 'pgcov run'."
    },
    "timestamp": {
      "type": "string",
      "format": "date-time",
      "description": "RFC 3339 timestamp of coverage collection"
    },
    "root": {
      "type": "string",
      "description": "Absolute discovery root the run used; the directory the keys in `positions` are relative to. A hint for source resolution at report time: `--base-dir` overrides it and it is ignored when the directory no longer exists. Omitted when unknown."
    },
    "positions": {
      "type": "object",
      "description": "Per-file coverage for EXECUTABLE statements (instrumented PL/pgSQL and SQL function bodies). Key: file path relative to the run's discovery root, normalised to forward slashes on every platform. Value: position -> hit count map. The coverage percentage is computed over these and only these.",
      "additionalProperties": {
        "$ref": "#/definitions/PositionHits"
      }
    },
    "implicit_positions": {
      "type": "object",
      "description": "Per-file coverage for DDL/DML statements, which are marked covered as soon as their source file loads. Kept separate because they can never be uncovered: counting them would make every CREATE TABLE a permanently-100%-covered denominator entry. Reporters still render them so those lines stay highlighted. Omitted when empty.",
      "additionalProperties": {
        "$ref": "#/definitions/PositionHits"
      }
    }
  },
  "definitions": {
    "PositionHits": {
      "type": "object",
      "description": "Position key -> hit count. Keys use the form \"<startByteOffset>:<byteLength>\". Values are non-negative integers; a value of 0 means the instrumented position was not executed.",
      "additionalProperties": {
        "type": "integer",
        "minimum": 0
      }
    }
  }
}
```

### Example Coverage Data File

```json
{
  "version": "2.0",
  "timestamp": "2026-08-14T16:00:00Z",
  "positions": {
    "src/auth.sql": {
      "0:42": 5,
      "42:128": 5,
      "170:37": 3,
      "207:41": 0
    },
    "src/user.sql": {
      "0:120": 8,
      "120:96": 8,
      "216:48": 0
    }
  }
}
```

All instrumented positions are seeded with `0` even if the test never executes them, so unexecuted branches (for example `ELSIF`/`ELSE` arms) are visible as `0` rather than being absent from the file.

### What the coverage percentage counts

The reported percentage covers **executable statements only** - the PL/pgSQL and
SQL function bodies instrumented with `pg_notify`. DDL/DML statements are
recorded separately under `implicit_positions` and reported on their own line.

They are kept apart because an implicit position is marked covered the instant
its source file loads, so it can never be uncovered. Folding them into the
percentage made every `CREATE TABLE` a permanently-100%-covered denominator
entry: a source file that is 90% DDL scored roughly 90% before a single
assertion ran, and `--fail-under` could be satisfied by schema alone.

`--fail-under` therefore compares against the executable percentage. When a run
instruments no executable statements at all, there is nothing a threshold can
measure and `--fail-under` fails with an explanatory message rather than
passing on an empty measurement.


---

## LCOV Output Contract

### Format Specification

LCOV trace file format (compatible with `genhtml` and `coverage.py`).

The reporter reads each source file referenced in `positions` and converts the stored byte-offset positions into line numbers (a position is attributed to the line on which its `startByteOffset` falls; multiple positions on the same line accumulate their hit counts). If a source file cannot be read, positions are emitted directly with `DA:<startByteOffset>,<hitCount>` as a fallback.

### Example Output

```
TN:
SF:src/auth.sql
DA:1,5
DA:2,5
DA:5,3
LF:3
LH:2
end_of_record

SF:src/user.sql
DA:1,8
DA:2,8
DA:3,0
LF:3
LH:2
end_of_record
```

**Legend**:
- `TN:` - Test name (empty for pgcov)
- `SF:` - Source file path
- `DA:line,hitcount` - Line coverage data (derived from byte-offset positions)
- `LF:` - Lines found (total)
- `LH:` - Lines hit
- `end_of_record` - End of file marker

`BRDA`, `BRF`, `BRH` records are not emitted; branch coverage is folded into the position map.

---

## Behavioral Contracts

### Test Discovery

**Contract**: Files matching `*_test.sql` pattern are test files; all other `.sql` files are source files.

**Examples**:
- ✅ `auth_test.sql` → Test
- ✅ `user_functions_test.sql` → Test
- ✅ `auth.sql` → Source
- ❌ `test_auth.sql` → Source (wrong pattern)

### Test Isolation

**Contract**: Each test runs in a unique temporary database.

**Guarantees**:
- Test execution order does not affect results
- Tests can run in parallel without interference
- No database artifacts persist after test completion

### Coverage Accuracy

**Contract**: Same code and tests produce identical coverage results.

**Guarantees**:
- Deterministic hit counts
- Reproducible across runs
- No false positives (covered position must have executed)
- No false negatives (executed position must be marked covered)

### Error Reporting

**Contract**: All errors include actionable context.

**Guarantees**:
- Parse errors show file and underlying error
- Connection errors suggest configuration fixes (`--connection` or PG\* env vars)
- Test failures propagate SQL error code and message
- Timeout errors identify which test timed out

---

## Versioning

**Contract Version**: 1.0  
**Breaking Changes**: Require major version bump

Breaking changes include:
- CLI flag removals or renames
- Exit code changes
- Coverage data JSON schema changes (incompatible with previous parsers)
- LCOV format deviations

**Non-Breaking Changes**: Minor/patch version bumps

Non-breaking changes include:
- New CLI flags
- New output formats
- Additional fields in JSON schema
- Performance improvements

---

## Stability Guarantees

- **CLI Interface**: Stable after v1.0 (flag additions only)
- **Coverage Data Format**: Backward-compatible schema evolution
- **Exit Codes**: Fixed contract (no reassignment)
- **LCOV Format**: Strict adherence to specification

---

## Contract Tests

Implementation must pass these contract validation tests:

1. **CLI Help Output**: `pgcov help` returns exit code 0 and shows all commands
2. **Version Output**: `pgcov --version` shows version string
3. **Exit Code 0**: All passing tests return exit code 0
4. **Exit Code 0 (no tests)**: A run that finds no `*_test.sql` files returns exit code 0
5. **Exit Code 1**: Any failing test, runtime error, or missing coverage file in `report` returns exit code 1
6. **Exit Code 2**: Invalid configuration (e.g. missing `--connection`, non-positive `--timeout`) returns exit code 2
7. **Coverage File**: `pgcov run` creates `.pgcov/coverage.json` containing `version`, `timestamp`, and `positions`
8. **LCOV Output**: `pgcov report --format=lcov` produces parseable LCOV format
9. **HTML Output**: `pgcov report --format=html` produces an HTML report
10. **Test Pattern**: `*_test.sql` files discovered, others treated as source
11. **Parallel Execution**: `--parallel=N` respects concurrency limit
12. **Timeout Enforcement**: `--timeout=Xs` terminates test after X seconds
13. **Deterministic Coverage**: Multiple runs produce identical coverage data

---

## Summary

This contract defines:
- ✅ CLI commands and flags
- ✅ Exit codes and their meanings
- ✅ Output formats (text, JSON, LCOV, HTML)
- ✅ Coverage data file schema
- ✅ Behavioral guarantees
- ✅ Versioning policy

All implementations must comply with this contract for v1.0 compatibility.
