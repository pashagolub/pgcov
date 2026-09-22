# pgcov — Copilot Instructions

PostgreSQL test runner and coverage tool — pure Go (no CGO, no external CLI tools).

## Execution Pipeline

1. `internal/discovery` — walk dirs, classify `.sql` files as `Source` or `Test` (`*_test.sql`)
2. `internal/parser` — token-split SQL into `[]*Statement` using `pashagolub/pglex` (pure Go lexer, not pg_query_go)
3. `internal/instrument` — per-statement: PL/pgSQL/SQL function bodies get `EXECUTE 'SELECT pg_notify(''<channel>'', ''<relPath>:<startByteOffset>:<byteLength>'')'` injected (EXECUTE keeps `FOUND` intact, PERFORM does not); all other DDL/DML is marked implicitly covered
4. `internal/database` — create isolated temp DB (`pgcov_test_<yyyymmdd_hhmmss>_<4-byte hex>`), deploy instrumented sources, execute the test `.sql` file, then `DROP DATABASE ... WITH (FORCE)`
5. `internal/database.Listener` — dedicated `pgx.Conn` running `LISTEN coverage_signal`; forwards signals to a buffered channel (size 1000)
6. `internal/coverage.Collector` — thread-safe (`sync.Mutex`) aggregation of signals into `Coverage.Positions[file]["startPos:length"]`
7. `internal/report` — `Formatter` interface (`Format`, `FormatString`, `Name`); implementations `json.go`, `lcov.go`, `html.go`; factory `GetFormatter(FormatType)`

**Parallelism**: `runner.WorkerPool` fans out `*testJob` over a buffered channel; each worker owns its own temp DB — no shared state at all.

**Config flow**: `pkg/types.Config` is the canonical struct; aliased in `internal/cli`; CLI flags applied via `ApplyFlagsToConfig`; default coverage output path `.pgcov/coverage.json`.

## Key Files

| Path | Role |
|---|---|
| `cmd/pgcov/main.go` | `run` / `report` subcommands via `urfave/cli/v3`; wires flags → `ApplyFlagsToConfig` |
| `internal/parser/parse.go` | `pglex` token scan → `[]*Statement` with `Type`, `Language`, `RawSQL`, byte offsets |
| `internal/instrument/instrumenter.go` | `instrumentBody()` single-pass token scan; `instrumentStatement()` dispatch |
| `internal/instrument/location.go` | `ParseSignalID(id) (file, start, length, err)` — canonical signal parser |
| `internal/database/tempdb.go` | `CreateTempDatabase` / `DestroyTempDatabase` — preserves pool SSL config |
| `internal/database/listener.go` | `NewListener` → goroutine `receiveLoop`; `Signals()` returns `<-chan CoverageSignal` |
| `internal/runner/executor.go` | `Executor.Execute` orchestrates one test; `filterSourcesByDirectory` scopes sources |
| `internal/runner/parallel.go` | `WorkerPool.ExecuteParallel` — falls back to sequential when `maxWorkers==1` |
| `internal/coverage/collector.go` | `CollectFromRuns` → `AddPosition`; `Coverage()` returns snapshot |
| `internal/report/formatter.go` | `GetFormatter(FormatType)` factory |
| `pkg/types/types.go` | `Config`, `CoverageSignal` shared types |
| `internal/testutil/postgres.go` | Docker-based Postgres setup for integration tests (`testcontainers-go`) |

## Build & Test

```bash
# Build (no CGO required — pure Go)
go build ./cmd/pgcov

# Unit tests
go test ./...

# Integration tests (require Docker)
go test ./... -run Integration
```

VS Code tasks: **Build pgcov** (`Ctrl+Shift+B`), **Unit Test**, **Coverage Report**.

## Project-Specific Conventions

**Signal ID format** — `"<relPath>:<startByteOffset>:<byteLength>"` (byte offsets, not line numbers). Always use `instrument.ParseSignalID` to parse, never split manually.

**Source scoping** — each test only sees sources from its own directory. `filterSourcesByDirectory` in `executor.go` enforces this. Never pass sources from sibling directories.

**Logging** — no logger package exists. Use `fmt.Printf(...)` guarded by `if verbose`. Do not add a logger dependency.

**Error wrapping** — always `fmt.Errorf("context: %w", err)`, never bare `return err`.

**Adding a report format** — implement `report.Formatter` (`Format`, `FormatString`, `Name`), add a `case` in `GetFormatter`, add `_test.go` alongside.

**Adding a config flag** — add field to `pkg/types.Config`, wire in `ApplyFlagsToConfig` in `internal/cli/config.go`, support `PGCOV_*` env var via `urfave/cli/v3` `EnvVars`.

## Hard Constraints

- Never use `psql` or shell exec — always `pgx` directly
- Never require PostgreSQL extensions
- Instrumentation must not change semantic behavior of tested SQL
- Each test gets its own temp DB — no shared DB state, ever
- Do not add CGO dependencies — the project is intentionally pure Go
