# AI Agent Instructions for pgcov

Comprehensive guidance for AI agents contributing to pgcov.

---

## Project Overview

**pgcov** is a PostgreSQL test runner and coverage tool written in Go. It discovers `*_test.sql` files, instruments SQL/PL/pgSQL source code for coverage tracking, executes tests in isolated temporary databases, and generates coverage reports.

### Key Facts

- **Language**: Go 1.24+
- **Build**: CGO-enabled (requires C compiler)
- **PostgreSQL**: Version 13+
- **Output Formats**: JSON, LCOV, HTML

### Project Structure

```
pgcov/
├── cmd/pgcov/main.go       # CLI entry point (urfave/cli/v3, inline commands)
├── internal/
│   ├── cli/               # Run/Report handlers + ApplyFlagsToConfig
│   ├── coverage/          # Collector, Coverage model, Store
│   ├── database/          # Pool, Listener (LISTEN/NOTIFY), temp DB
│   ├── discovery/         # ClassifyFile/ClassifyPath, discover walk
│   ├── instrument/        # Instrumenter, ParseSignalID, FormatSignalID
│   ├── parser/            # pglex token scan → []*Statement
│   ├── report/            # Formatter interface, JSON/LCOV/HTML impls
│   └── runner/            # Executor, WorkerPool, TestRun/TestStatus
├── internal/testutil/     # Docker-based Postgres setup (testcontainers-go)
├── internal/integration_test.go  # Integration tests (package integration_test)
├── pkg/types/types.go     # Shared Config, CoverageSignal types
├── testdata/              # Integration test fixtures
└── examples/              # Usage examples
```

---

## Core Principles (Non-Negotiable)

1. **Direct Protocol** — Use `pgx` only. Never `psql`, shell exec, or extensions.
2. **Test Isolation** — Each test gets its own temp DB. No shared state between tests, ever.
3. **Instrumentation Transparency** — Rewrite in-memory only. Must not change SQL semantics.
4. **CLI-First** — Flags, env vars (`PGCOV_*`), and `pkg/types.Config` are the config contract.
5. **Coverage Accuracy** — Deterministic results over speed.
6. **Idiomatic Go** — No CGO deps beyond build toolchain. No logger package (`fmt.Printf` + verbose flag).

### Non-Goals

- Database migration management
- Assertion libraries (use pgTAP/others externally)
- PostgreSQL extensions

---

## Architecture

### Execution Pipeline

```
1. Discovery   → walk dirs, classify *.sql vs *_test.sql
2. Parsing     → pglex token scan → []*Statement with byte offsets
3. Instrumentation → inject EXECUTE 'SELECT pg_notify(...)' with '<file>:<start>:<len>' (EXECUTE keeps FOUND intact)
                     into PL/pgSQL/SQL function bodies; mark other DDL implicitly covered
4. Temp DB     → CREATE DATABASE pgcov_test_<yyyymmdd_hhmmss>_<4-byte hex>
5. Deploy      → execute instrumented SQL in temp DB
6. Execute     → run the *_test.sql file
7. Collect     → LISTEN coverage_signal on dedicated pgx.Conn
8. Cleanup     → DROP DATABASE ... WITH (FORCE)
9. Report      → aggregate Coverage, write JSON/LCOV/HTML
```

### Coverage Signal Format

```
Channel: coverage_signal
Payload: <relPath>:<startByteOffset>:<byteLength>
Example: src/auth.sql:128:42
```

Always use `instrument.ParseSignalID` to parse signals — never split manually.

### Source Scoping

Each test only sees sources from **its own directory** (non-recursive). `filterSourcesByDirectory` in `runner/executor.go` enforces this. Never pass sources from sibling directories.

### Key Types

| Type | Package | Role |
| --- | --- | --- |
| `Config` | `pkg/types` | Canonical config struct (aliased in `internal/cli`) |
| `CoverageSignal` | `pkg/types` | Shared signal type (aliased in `internal/runner`) |
| `Formatter` | `internal/report` | Interface: `Format`, `FormatString`, `Name` |
| `Collector` | `internal/coverage` | Thread-safe signal aggregation |
| `Executor` | `internal/runner` | Orchestrates one test (temp DB + signals) |
| `WorkerPool` | `internal/runner` | Fans out jobs over buffered channel |

---

## Development Environment

### Prerequisites

- **Go 1.24+**
- **C compiler**: Linux: `build-essential` · macOS: `xcode-select --install` · Windows: MSYS2 + MinGW-w64 (see BUILD.md)
- **Docker**: For integration tests

### Build & Test

```bash
# Build
CGO_ENABLED=1 go build ./cmd/pgcov

# Unit tests (no Docker required)
go test -short ./...

# All tests including integration (requires Docker)
go test ./...

# Integration tests only
go test ./... -run Integration
```

### VS Code Tasks

- **Build pgcov** (`Ctrl+Shift+B`) — `go build ./cmd/pgcov`
- **Unit Test** — tests in current directory with coverage + tparse
- **Coverage Report** — `go tool cover -html=coverage.out`
- **Lint** — `golangci-lint run`

---

## Code Standards

### Error Handling

Always wrap with context:

```go
// ✅
return fmt.Errorf("failed to connect to %s: %w", dbName, err)
// ❌
return err
```

### Logging

No logger package. Use `fmt.Printf` guarded by `verbose`:

```go
if verbose {
    fmt.Printf("Discovering tests in %s\n", path)
}
```

### Naming

- Files: `snake_case.go`
- Packages: short, lowercase, no underscores
- All exported symbols must have godoc comments starting with the symbol name

---

## Testing Requirements

### Organization

- **Unit tests**: `*_test.go` alongside source (same package)
- **Integration tests**: `internal/integration_test.go` (package `integration_test`)
- **Fixtures**: `testdata/` — simple, plpgsql, edge_cases, isolation, parallel, sqlfunc

### Patterns

Use table-driven tests. Do not use `require`/`assert` — use stdlib `testing` only.

```go
func TestFoo(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"valid", "SELECT 1", "expected", false},
        {"empty", "", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := Foo(tt.input)
            if (err != nil) != tt.wantErr {
                t.Fatalf("Foo() error = %v, wantErr %v", err, tt.wantErr)
            }
            if got != tt.want {
                t.Errorf("Foo() = %q, want %q", got, tt.want)
            }
        })
    }
}
```

Integration tests must skip without Docker:

```go
func TestIntegration_Something(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    // uses testutil.SetupPostgresContainer(t)
}
```

---

## Common Tasks

### Adding a New Command

1. Add `internal/cli/mycommand.go` with the action function.
2. Add an inline `*urfavecli.Command` entry in the `Commands` slice in `cmd/pgcov/main.go`.
3. Add tests in `internal/cli/mycommand_test.go`.

```go
// cmd/pgcov/main.go — inside Commands slice
{
    Name:   "mycommand",
    Usage:  "Does something useful",
    Action: myCommandAction,
    Flags:  []urfavecli.Flag{
        &urfavecli.StringFlag{Name: "option", Usage: "..."},
    },
},
```

### Adding a New Report Format

1. Implement the `Formatter` interface in `internal/report/myformat.go`:

```go
type MyFormatReporter struct{}

func NewMyFormatReporter() *MyFormatReporter { return &MyFormatReporter{} }

func (r *MyFormatReporter) Format(cov *coverage.Coverage, w io.Writer) error { ... }
func (r *MyFormatReporter) FormatString(cov *coverage.Coverage) (string, error) { ... }
func (r *MyFormatReporter) Name() string { return "myformat" }
```

1. Add a `case` in `GetFormatter` in `internal/report/formatter.go`.
1. Add `internal/report/myformat_test.go`.

### Adding a New Config Option

1. Add field to `pkg/types/types.go` → `Config` struct.
2. Add flag in `cmd/pgcov/main.go` (the relevant command's `Flags` slice) with `EnvVars: []string{"PGCOV_MY_OPTION"}`.
3. Wire in `internal/cli/config.go` → `ApplyFlagsToConfig`.
4. Add validation in `Config.Validate()` in `pkg/types/types.go` if needed.

### Modifying Instrumentation

**Must maintain Principle III (transparency).**

- Token-level rewriting is in `internal/instrument/instrumenter.go` (`instrumentBody`, `instrumentStatement`).
- Signal ID generation: `FormatSignalID` / `ParseSignalID` in `internal/instrument/location.go`.
- Test with `testdata/plpgsql/`, `testdata/sqlfunc/`, and `testdata/edge_cases/`.
- Verify deterministic output (same input → identical instrumented text).

---

## CI/CD

Pre-push checklist:

```bash
go fmt ./...
go vet ./...
golangci-lint run
go test -short ./...
```

Commit format (conventional commits):

```
feat: add SARIF report format
fix: correct byte offset in LCOV output
test: add edge case for empty function body
refactor: simplify signal collection loop
```

---

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `CGO not enabled` | `export CGO_ENABLED=1` (Linux/macOS) or `$env:CGO_ENABLED="1"` (PowerShell) |
| `gcc: command not found` (Windows) | `$env:PATH += ";C:\msys64\mingw64\bin"` and `$env:CC = "C:\msys64\mingw64\bin\gcc.exe"` |
| Integration tests panic / Docker error | Ensure Docker daemon is running: `docker ps` |
| `permission denied to create database` | `ALTER USER youruser CREATEDB;` |
| `failed to instrument source file` | Check SQL syntax; run with `--verbose` for details |
| Test timeout | Use `--timeout=60s` or increase per-test timeout in config |
