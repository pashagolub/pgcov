## Inconsistencies

### I1 — `pgcov` NOTIFY channel name hardcoded in two separate places

> **Status: IMPLEMENTED** — stacked PR #16 (`feat(coverage): single-source, per-run unique NOTIFY channel name`, combined with E6). Verified 2026-08-14: hardcodes confirmed at instrumenter.go ×3 and executor.go ×1.

`internal/instrument/instrumenter.go` hardcodes `'pgcov'` inside the injected
`pg_notify(...)` call.  `internal/runner/executor.go` passes `"pgcov"` to
`database.NewListener`.  If either is changed independently, coverage collection breaks
silently.  A single exported constant (e.g. `instrument.CoverageChannel`) should be the
single source of truth.

---

### I2 — `Branch` field in `CoveragePoint` is always empty string

> **Status: IMPLEMENTED** — stacked PR #17 (`refactor(instrument): remove dead branch-coverage API surface`). Dead code removed: `CoveragePoint.Branch` field, `branch` param in `FormatSignalID`, 4-part branch path in `ParseSignalID`. Note: the `TrackBranchPosition` claim in this finding was inaccurate — that function does not exist; the rest was verified.

`types.go` defines `CoveragePoint.Branch`, `FormatSignalID` and `ParseSignalID` both
handle a `:branch` suffix, and `TrackBranchPosition` exists in `location.go` — but
`instrumentBody` always sets `Branch: ""` and the collector ignores branch data when
aggregating.  This is an unimplemented feature occupying API surface.  Either implement
it or remove the dead code.

---

### I3 — `PositionKey()` format differs from the key format used by the Collector

> **Status: IGNORED** — finding is factually wrong (verified 2026-08-14). No exported `PositionKey` function exists in `internal/instrument/location.go` (or anywhere in the tree); it contains only `FormatSignalID`, `ParseSignalID`, `parseNumber`. The collector's private `formatPositionKey` (`internal/coverage/model.go`) is the sole, consistent position-key format.

`location.go` exports:

```go
func PositionKey(file, startPos, length) string { return "file:startPos:length" }
```

The collector stores keys as:

```go
posKey := fmt.Sprintf("%d:%d", startPos, length)   // no file prefix
```

`PositionKey` is never called by the collector; the two formats are incompatible.  The
exported function is misleading and unused internally.

---

### I4 — `TrackPosition()` and `TrackBranchPosition()` in `location.go` are dead code

> **Status: IGNORED** — finding is factually wrong (verified 2026-08-14). Neither `TrackPosition` nor `TrackBranchPosition` exists anywhere in the source tree (`location.go` holds only `FormatSignalID`/`ParseSignalID`/`parseNumber`). The genuinely dead branch API was `Branch`/`FormatSignalID`, handled under I2.

Neither function is called anywhere in the production code.  They are part of the
unimplemented branch-tracking API (see I2).

---

### I5 — `IsolationValidator` lives in production code but is never wired up

> **Status: IGNORED** — finding is factually wrong (verified 2026-08-14). `internal/runner/isolation.go` does not exist and no `IsolationValidator`/`TrackDatabase`/`MarkCleaned`/`ValidateCleanup` symbol exists anywhere in the repository.

`internal/runner/isolation.go` provides a fully functional `IsolationValidator` with
`TrackDatabase` / `MarkCleaned` / `ValidateCleanup`, but `executor.go` never calls any
of these methods.  The struct is only used in tests.  It should either be wired in
(pre-flight check for uniqueness / post-run cleanup confirmation) or moved to a test
helper package.

---

### I6 — `cli-contract.md` describes a schema that was never implemented

> **Status: IMPLEMENTED** — stacked PR #24 (`docs(cli): rewrite cli-contract.md to match the implemented CLI and JSON schema`). Document rewritten to match reality: `--connection` flag set, exit codes 0/1/2, `{version, timestamp, positions}` JSON schema. Verified: fictitious `--host/--port/--user/--password/--database` flags, `lines`/`branches` schema, and exit code 3 confirmed absent from code.

`docs/cli-contract.md` (§ Coverage Data File Contract) documents a JSON schema with
`lines` (containing `LineCoverage` objects) and `branches` (containing `BranchCoverage`
objects).  The actual JSON output is:

```json
{"positions": {"file.sql": {"0:42": 3}}}
```

The CLI contract also lists individual flags (`--host`, `--port`, `--user`, `--password`,
`--database`) that do not exist; the real flag is a single `--connection` URI string.
The document should be updated or the flags added.

---

### I7 — `ClassifyFile` returns `FileTypeSource` for non-SQL files

> **Status: IMPLEMENTED** — stacked PR #14 (`fix(discovery): return FileTypeUnknown for non-SQL files in ClassifyFile`). Note: the claimed risk was partially inaccurate — `Discover` already filters non-`.sql` files before calling `ClassifyFile`, so the parser could never receive arbitrary files; the fix is API hardening only.

If `filepath.Walk` delivers a non-`.sql` file, `ClassifyFile` returns `FileTypeSource`
(documented as an "edge case").  This could cause the parser to attempt reading arbitrary
files.  An explicit `FileTypeUnknown` or early-return guard in `Discover` would be safer.

---

### I8 — `GetFiles()` / `GetFileList()` return unsorted slices

> **Status: IMPLEMENTED** — stacked PR #15 (`refactor(coverage): return sorted file lists from GetFiles and GetFileList`). Verified: both getters iterated maps unsorted and every reporter caller immediately re-sorted; sorting moved into the getters, redundant caller sorts removed.

Both `Coverage.GetFiles()` (`model.go`) and `Collector.GetFileList()` (`collector.go`)
return files in random map-iteration order.  Every caller (all three reporters) performs
its own `sort.Strings()` immediately after.  Sorting once in these methods would
eliminate the repeated pattern.

---

### I9 — `isExecutableSegment` does not recognise `RETURN` as a segment boundary marker

> **Status: IGNORED** — not a defect (verified 2026-08-14). The finding itself concedes treating `RETURN` as executable "is correct". The claimed follow-on risk (signals after `RETURN` being unreachable) is already handled: `findTerminalPos` places the coverage signal BEFORE terminal `RETURN`/`RAISE` statements, covered by `TestInstrumentBody_ReturnInBranches` and related B2 tests.

`isExecutableSegment` excludes `BEGIN`, `END`, `LOOP`, `DECLARE`, and `EXCEPTION` from
instrumentation.  `RETURN` is treated as an ordinary executable statement.  While this
is correct — a `RETURN` is executable — it means a bare `RETURN;` at the end of a
complex function body generates its own signal, inflating coverage point counts.  More
importantly, any signal injected immediately *after* a `RETURN` statement becomes
unreachable (see B2), but the exclusion list does not account for this.

---

## Improvements / Enhancements

### E1 — No coverage merge / accumulation across runs

> **Status: IMPLEMENTED** — stacked PR #21 (`feat(cli): add merge command to accumulate coverage across runs`). New `pgcov merge` subcommand + `coverage.Merge()` summing per-position hit counts.

`pgcov run` always overwrites `.pgcov/coverage.json`.  There is no `pgcov merge` command
or `--append` flag for accumulating coverage across CI matrix shards or multiple test
directories.  The CI example configs work around this with external `jq` post-processing.

---

### E2 — No `--fail-under` coverage threshold

> **Status: IMPLEMENTED** — stacked PR #20 (`feat(cli): add --fail-under coverage threshold to run command`). `--fail-under` flag; exit 1 when total coverage is below the threshold, with test-failure exit codes taking precedence.

There is no built-in mechanism to exit non-zero when coverage falls below a configured
threshold.  This is a standard CI requirement and is simulated in the example CI configs
using shell arithmetic on the JSON output.

---

### E3 — Source file deployment order is undefined

> **Status: IMPLEMENTED (docs)** — stacked PR #25 (`docs: document lexical source deployment order and numeric-prefix convention`). Note: the premise was partially wrong — `filepath.Walk` guarantees lexical order within each directory, so deployment order is deterministic; the gap was documentation of that guarantee plus the numeric-prefix convention and `--setup` escape hatch.

`filepath.Walk` delivers files in lexicographic order within each directory, but the
behaviour is OS-specific.  If `02_functions.sql` depends on objects created by
`01_schema.sql`, naming convention is the only ordering guarantee.  A numeric-prefix
convention should be documented, or an explicit `order` annotation / `pgcov.toml`
manifest should be supported.

---

### E4 — No `--base-dir` flag for source resolution in reporters

> **Status: IMPLEMENTED** — stacked PR #22 (`feat(report): add --base-dir flag for source resolution at report time`). HTML/LCOV reporters gain `SetBaseDir` (Formatter interface unchanged); verified sources resolve against base-dir from a different CWD.

The HTML and LCOV reporters resolve source file paths relative to the current working
directory at report time.  If `pgcov report` is run from a different directory than
`pgcov run`, source content cannot be found and the HTML report shows an error comment
in place of annotated code.  A `--base-dir` flag would decouple the report generation
location from the run location.

---

### E5 — Hardcoded 100 ms grace period in `CollectSignals`

> **Status: IMPLEMENTED** — stacked PR #18 (`feat(cli): add --signal-timeout flag for coverage signal grace period`). Verified hardcode at executor.go (`100*time.Millisecond`); now `Config.SignalTimeout` + `--signal-timeout` flag, with `runner.DefaultSignalTimeout` fallback for non-positive values (bare `Config{}` constructions previously got a 0 ms grace and lost signals nondeterministically — caught by `TestTestIndependence` during stack verification).

After test SQL executes, `CollectSignals` waits 100 ms for in-flight notifications.  On
slow systems or under heavy parallel load this window may be too short, causing
legitimate signals to be lost.  The value should be exposed as a configuration option
(e.g. `--signal-timeout`).

---

### E6 — Listener channel is not per-run unique

> **Status: IMPLEMENTED** — stacked PR #16 (combined with I1: `feat(coverage): single-source, per-run unique NOTIFY channel name`). `Config.CoverageChannel` is the single source of truth; `pgcov run` generates `pgcov_<8 hex>` per invocation and threads it to both the injected `pg_notify` calls and the listener. Note: NOTIFY is database-scoped and each test runs in its own temp DB, so interference required user code inside the temp DB NOTIFYing on `pgcov` — now structurally impossible.

The `pgcov` NOTIFY channel is a well-known name.  If the user's application already
publishes notifications on a channel named `pgcov`, signals will be delivered to the
listener in addition to (or instead of) the coverage ones.  Making the channel name
incorporate the temp-database name or a random UUID would eliminate interference.

---

### E7 — `TestRun.Error` is set but not surfaced in the summary output

> **Status: IMPLEMENTED** — stacked PR #19 (`feat(cli): print failed test errors in run summary`). Verified: summary printed only counts; now `runner.FormatFailedTests` prints `FAILED <path>: <error>` per failed run (unit-tested helper, no DB needed).

When a test fails, `testRun.Error` holds the Go-level error.  The CLI summary prints
`X failed` but does not print the error message.  Users must re-run with `--verbose` to
see why a test failed.

---

### E8 — No `pgcov init` / scaffold command

> **Status: IMPLEMENTED** — stacked PR #23 (`feat(cli): add init command to scaffold test files from sources`). `pgcov init <source.sql>` extracts `CREATE [OR REPLACE] FUNCTION` signatures via the existing pglex tokenizer (no regex) and writes a commented `_test.sql` scaffold, with `--output`/`--force` guards.

There is no helper to generate a skeleton `_test.sql` file for an existing source.  A
`pgcov init` command that reads function signatures from a source file and emits a
commented test scaffold would lower the barrier to adoption.

---

### E9 — Pool `MaxConns` formula may be insufficient

> **Status: IGNORED** — premise is factually wrong (verified 2026-08-14). The LISTEN connection is a dedicated `pgx.ConnectConfig` connection in `NewListener` — it does NOT come from the pool. Source deployment and test SQL acquire pool connections sequentially (acquire → exec → release), never 3 simultaneously. `MaxConns = parallelism*2` applies to the admin pool, which only serves short `CREATE/DROP DATABASE` calls. No acquire-timeout path exists as described; bumping the multiplier would address nothing.

`pool.go` sets `MaxConns = parallelism * 2` under the assumption that each test uses
two connections (one for execution, one for LISTEN).  Each test actually acquires up to
three connections simultaneously (pool acquire for sources, listener connection, pool
acquire for test SQL).  Under high parallelism this can cause connection pool exhaustion
and `pgx` acquire timeouts.

---

### E10 — `CREATE DATABASE` uses an unquoted identifier

> **Status: IMPLEMENTED** — stacked PR #13 (`fix(database): quote temp database identifiers with pgx.Identifier.Sanitize`). Verified: both `CREATE DATABASE` and `DROP DATABASE ... WITH (FORCE)` (plus the rollback DROP) now sanitize the identifier.

`tempdb.go`:

```go
adminPool.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s", dbName))
```

The generated name (`pgcov_test_20260318_140534_ab12cd34`) uses only lowercase, digits
and underscores, so it is safe today.  However, the pattern should use `pgx`'s
`pgx.Identifier.Sanitize()` (quote the identifier) to be robust against any future
change to the name format and to prevent a theoretical injection if the name-generation
logic ever incorporates external input.

---

*File generated 2026-03-18 based on a code review of the full pgcov source tree and
hands-on testing with the `examples/demo` walkthrough.*

---
---

# Round 2 — code review of 2026-09-04

Reviewed against `findings/e3-source-load-order` (d258a76), i.e. after all Round 1
stacked PRs (#13–#25) landed. Numbering continues from Round 1 (I10+/E11+).

Every claim below was verified against the current tree before being written; the
verification method is recorded inline. In Round 1, four findings referenced code that
does not exist or inverted a premise (I3, I4, I5, E9) and a fifth (I9) was not a defect,
so this round trades breadth for confirmation.

## Inconsistencies / Defects

### P1 findings

---

### I10 — Parallel execution path skips per-directory source filtering

> **Status: IMPLEMENTED** — Filtering moved into `Executor.Execute`, the single choke point both `ExecuteBatch` and `WorkerPool.worker` funnel through, so the two paths can no longer diverge. Regression test `TestParallelFiltersSourcesPerTestDirectory` builds two sibling directories whose sources both define a table `shared` with different columns; pre-fix it reproduces the defect exactly (`failed to load source ... relation "shared" already exists`, and `sequential=passed parallel=failed`), post-fix both pass.

> **Verified 2026-09-04** — `grep -rn filterSourcesByDirectory` returns exactly one
> call site: `executor.go:105`, inside `ExecuteBatch`. `WorkerPool.ExecuteParallel`
> passes the unfiltered `sourceFiles` slice to `wp.worker`, which passes it verbatim
> to `executor.Execute`.

`Executor.ExecuteBatch` (the sequential path) filters the instrumented sources down to
those co-located with the test being run:

```go
testDir := filepath.Dir(testFiles[i].Path)
filteredSources := filterSourcesByDirectory(sourceFiles, testDir)
run, err := e.Execute(ctx, &testFiles[i], filteredSources)
```

`WorkerPool.worker` does not:

```go
run, err := wp.executor.Execute(ctx, job.testFile, sourceFiles)   // parallel.go
```

`ExecuteParallel` falls back to `ExecuteBatch` when `maxWorkers == 1 || numTests == 1`,
so the divergence requires **both** `--parallel >= 2` **and** `>= 2` discovered test
files — which is exactly the CI configuration.

Consequences:

1. Every temp database loads every source file from every test directory, not just the
   co-located ones. Sources from unrelated directories that depend on schema created
   elsewhere fail to load, so `pgcov run --parallel 4` reports
   `failed to load source ...` for tests that pass under `--parallel 1`.
2. Coverage attribution differs between the two modes: under `--parallel` each test run
   emits implicit DDL/DML signals for *all* sources, not only its own.

`--parallel` is advertised in the README feature list, so this is a correctness
divergence on a documented path. Fix: move the filtering into `Executor.Execute` (single
choke point) so both paths inherit it. Unit-testable without a database by asserting the
source slice handed to a stub executor.

---

### I11 — Implicit DDL/DML positions can never be uncovered, inflating the headline percentage

> **Verified 2026-09-04** — reproduced with a throwaway test over `Collector` and a
> synthetic `InstrumentedSQL` holding 3 implicit points and 1 never-executed executable
> point: `TotalCoveragePercent()` reported **75.00%** where real executable coverage was
> **0.00%** (`positions = map[0:10:1 10:10:1 20:10:1 30:10:0]`).

`Collector.InitializeFromInstrumented` deliberately skips implicit points:

```go
if cp.ImplicitCoverage {
    continue // DDL/DML are tracked separately
}
```

so those positions enter `Coverage.Positions` only through
`executeTestWorkflow`, which appends a signal for every implicit location the moment the
source file loads successfully:

```go
for _, loc := range source.Locations {
    if loc.ImplicitCoverage { testRun.CoverageSigs = append(...) }
}
```

An implicit position therefore exists in the map **if and only if its hit count is at
least 1**. But `TotalPositionCoveragePercent` counts every entry in the denominator:

```go
for _, posHits := range c.Positions {
    for _, count := range posHits { totalPositions++; if count > 0 { coveredPositions++ } }
}
```

Every `CREATE TABLE`, `CREATE INDEX`, `INSERT`, etc. is thus a permanently-100%-covered
denominator entry. A source file that is 90% DDL scores approximately 90% before a single
test assertion runs, and the `--fail-under` threshold added in E2 is measured against this
inflated number — so the flag can pass a suite with zero real PL/pgSQL coverage.

This is the most substantive finding in this round: it is the tool's headline output.

Options, in preference order:

1. Report executable and implicit coverage as two numbers; make `--fail-under` apply to
   the executable one.
2. Exclude implicit positions from the percentage entirely (keep them in the JSON for
   the HTML reporter's line highlighting).
3. Keep the current single number but document loudly what it measures.

Whichever is chosen, `InitializeFromInstrumented` should seed implicit points too, so
that a source file that *fails* to load is visibly 0% rather than absent.

---

### P2 findings

---

### I12 — `CollectSignals` deadline is a total window, not an idle window; leftover signals are silently discarded

> **Verified 2026-09-04** — `internal/database/listener.go`: `timer := time.NewTimer(timeout)`
> is created once outside the loop and is never `Reset`. The `case signal := <-l.signals`
> branch does not touch the timer.

```go
timer := time.NewTimer(timeout)
defer timer.Stop()
for {
    select {
    case signal, ok := <-l.signals: signals = append(signals, signal)   // timer not reset
    case <-timer.C: return signals, nil
    ...
    }
}
```

The grace period is a hard cap on the *entire* collection phase, not on the gap between
signals. A test that emits several thousand coverage signals can exhaust the window
mid-drain, and `CollectSignals` returns while `l.signals` still holds buffered entries.
`executeTestWorkflow`'s `defer listener.Close(ctx)` then discards them.

Worse, this loss is invisible: `droppedSignals` only increments in
`handleNotification`'s `default:` branch (buffer full at *enqueue* time). Signals lost
at *dequeue* time are not counted, so the "coverage data is incomplete" warning does not
fire and the run reports confidently wrong coverage.

E5 (`--signal-timeout`) widened this window but did not change its shape — a user hitting
this has no value that reliably fixes it, only a value that is large enough today.

Fix: `timer.Reset(timeout)` on each received signal (idle timeout), plus a non-blocking
drain of `l.signals` on the timer branch before returning. Optionally count the drained
remainder toward a reported total.

---

### I13 — Coverage-file keys are CWD- and OS-dependent, which silently breaks `merge`

> **Verified 2026-09-04** — reproduced with a throwaway test calling
> `discovery.DiscoverTests` on the same absolute directory from two different working
> directories: `RelativePath` came back as `sql\a_test.sql` and `a_test.sql` for the
> same file.

`Discover` computes the relative path against the process CWD:

```go
cwd, err := os.Getwd()
...
relPath, err := filepath.Rel(cwd, path)
```

`RelativePath` becomes the `File` in every `CoveragePoint`, the file half of every signal
ID, and therefore the **top-level key** in `coverage.json` (`Coverage.Positions`).

Two consequences the Round 1 work did not cover:

1. `pgcov merge` keys purely on the file string. Two CI shards that invoke `pgcov run`
   from different working directories produce disjoint keys for the *same* source file.
   `Merge` then unions the two spellings instead of summing them: the source file appears
   twice in the output under different names, each per-file percentage is computed over
   only half the position set, and a position covered in shard A but not in shard B no
   longer merges into "covered". Reporters cannot resolve at least one of the two
   spellings. Nothing warns.
2. The separator is OS-native (`sql\a_test.sql` on Windows, `sql/a_test.sql` elsewhere),
   so coverage files are not portable across a mixed-OS CI matrix, and the HTML/LCOV
   reporters cannot resolve the foreign-separator paths.

E4's `--base-dir` addressed source *resolution* at report time; it does not normalise the
keys themselves, so it cannot fix either case.

Fix: key coverage on a path relative to the discovery root (not the CWD) and normalise to
forward slashes with `filepath.ToSlash` before it reaches the signal ID. Record the root
in the coverage file so `--base-dir` keeps working.

---

### P3 findings

---

### I14 — `TestTimeout` status is never assigned; all timeout accounting is dead

> **Verified 2026-09-04** — `grep -rn TestTimeout` finds only the const declaration
> (`types.go:33`), its `String()` case (`types.go:47`), two read-only `switch` cases
> (`executor.go:154`, `parallel.go:83`) and one *test* construction
> (`types_test.go:55`). No production assignment exists.

`Executor.Execute` sets `TestFailed` for every error, including a per-test context
deadline. Nothing ever writes `TestTimeout`, so:

- `TestSummary.TimedOutTests` is always 0;
- the `s.TimedOutTests == 0` clause in `AllPassed` is unreachable;
- the `case TestTimeout` arms in `SummarizeRuns` and the parallel verbose printer are
  dead;
- a timed-out test is indistinguishable from a genuinely failing one in the summary, even
  though `--timeout` is a documented flag.

Fix: in `Execute`, check `errors.Is(err, context.DeadlineExceeded)` against `testCtx` and
set `TestTimeout`. Alternatively remove the status and its accounting.

---

### I15 — `Executor.Execute` never returns a non-nil error, so both callers' error paths are unreachable

> **Status: IMPLEMENTED** — `Execute` now returns a bare `*TestRun`; the unreachable `if err != nil` block in `ExecuteBatch` and the doubly-unreachable `run == nil` fallback in `worker` are gone. `ExecuteBatch`/`ExecuteParallel` keep their `error` returns (consumed by `cli.Run` and existing tests) — only the always-nil channel from `Execute` was removed. Doc comment now states that `TestRun.Error` is the sole failure channel.

> **Verified 2026-09-04** — `executor.go`: `Execute` ends with `return testRun, nil` and
> has no other return statement; failures are recorded on `testRun.Status`/`.Error`.

Both call sites branch on an error that cannot occur:

```go
run, err := e.Execute(ctx, &testFiles[i], filteredSources)   // ExecuteBatch
if err != nil { ... }                                        // dead

run, err := wp.executor.Execute(ctx, job.testFile, sourceFiles)  // worker
if err != nil && run == nil { ... }                              // dead
```

The `worker` fallback that synthesises a `TestRun` when `run == nil` is doubly
unreachable. This is misleading rather than broken — but it hides that the *only* error
channel is `TestRun.Error`, which is what E7 had to discover. Either drop the `error`
return from `Execute` or make it return real infrastructure errors (temp-DB creation
failure is arguably not a test failure).

---

### I16 — Flag application uses zero-value sentinels and mutates a package-level global

> **Verified 2026-09-04** — `main.go:runCommand` does `config := &cli.DefaultConfig`;
> `cli/config.go:ApplyFlagsToConfig` guards every field with `!= 0` / `!= ""`.

Two coupled problems:

```go
config := &cli.DefaultConfig          // pointer to the package-level var
```

`runCommand` takes the address of the exported `DefaultConfig` and then mutates it, so
"defaults" are permanently rewritten for the life of the process and any in-process test
that runs two commands in sequence sees leakage from the first. It works today only
because the binary runs exactly one command and exits.

```go
if timeout != 0    { c.Timeout = timeout }
if parallel != 0   { c.Parallelism = parallel }
if signalTimeout != 0 { c.SignalTimeout = signalTimeout }
```

The zero-value sentinel makes explicitly-zero values unsettable. `--timeout 0` (no
per-test timeout) and `--parallel 0` are silently ignored rather than honoured or
rejected. `--verbose` and `--fail-under` are assigned unconditionally, so the two halves
of the function follow different rules.

Fix: copy the default (`config := cli.DefaultConfig` then take `&config`) and drive the
overrides from `cmd.IsSet("...")` rather than value comparison. urfave/cli v3 exposes
`IsSet` on `*Command`.

---

### P4 findings

---

### I17 — `fmt.Sscanf("%d")` silently accepts trailing garbage (API hardening)

> **Verified 2026-09-04** — measured directly: `fmt.Sscanf("12abc", "%d", &n)` returns
> `n=12, count=1, err=<nil>`; `fmt.Sscanf("10:20junk", "%d:%d", &a, &b)` returns
> `a=10, b=20, count=2, err=<nil>`.

`instrument.parseNumber` (used by `ParseSignalID`) and `coverage.ParsePositionKey` both
parse with `Sscanf`, which stops at the first non-matching byte and reports success. A
malformed signal ID such as `a.sql:12abc:5` parses cleanly as position 12.

**This is hardening, not a live risk** — signal IDs and position keys are generated by
pgcov itself and never accept external input, so no current path can produce a malformed
value. (Round 1's I7 was corrected for exactly this kind of overclaim.) It matters only
for hand-edited or third-party-produced coverage files, which `pgcov merge` and
`pgcov report` now both accept from disk.

Fix: `strconv.Atoi` (and an explicit `strings.Cut` for the position key), which rejects
trailing bytes.

---

### I18 — `Merge` ignores the `version` schema field

> **Verified 2026-09-04** — `coverage.Merge` reads only `c.Positions`; the `Version`
> field of each input is never inspected, and the result is hard-coded to `NewCoverage()`'s
> `"1.0"`.

`Coverage.Version` exists precisely so the format can evolve, but `merge` will happily
combine a `"1.0"` file with a hypothetical `"2.0"` file and stamp the output `"1.0"`.
`Store.Load` does not check it either. Since `merge` is the one command that consumes
files it did not write, it should reject unknown versions with a clear message rather
than producing plausible-looking wrong output.

---

### I19 — Binary version is hardcoded and not wired to the build

> **Verified 2026-09-04** — `cmd/pgcov/main.go: const version = "1.0.0"`. `BUILD.md`
> documents `-ldflags="-s -w"` only (no `-X`), and `.github/workflows/build.yml` builds
> with a bare `go build ./cmd/pgcov`.

`pgcov --version` reports `1.0.0` for every build ever produced, including local
development builds and any future release. There is no release workflow, so the constant
is the only version source.

Fix: `var version = "dev"` plus an `-X main.version=...` ldflag fed from
`git describe --tags --always --dirty`, documented in `BUILD.md` and applied in CI.
Optionally fall back to `runtime/debug.ReadBuildInfo()` so `go install ...@latest`
reports the module version.

---

## Improvements / Enhancements

---

### E11 — Coverage data records no source checksum, so stale reports are undetectable

Coverage positions are **byte offsets** into the source file
(`StartPos = stmt.StartPos + bodyIndexInOriginal + segStart`). `coverage.json` stores
only the file path and those offsets — no size, mtime, or hash.

Editing a source file between `pgcov run` and `pgcov report` therefore shifts every
offset after the edit, and the HTML reporter highlights arbitrary spans of the *new*
file with the *old* hit counts, with no warning. The same applies across `pgcov merge`
inputs collected from different revisions: positions from revision A are summed onto
revision B's, producing a report that is internally consistent and entirely wrong.

Suggested: store a per-file `sha256` (or size+mtime) alongside the positions; have
`report` warn (or fail with `--strict`) on mismatch, and have `merge` refuse to combine
inputs whose checksums for a shared file disagree. This also gives `--base-dir` (E4) a
way to confirm it resolved to the right tree.

---

### E12 — Several packages on the critical path have no unit tests

The test suite covers `cli/config`, `cli/init`, `coverage/collector`, `coverage/merge`,
`database/tempdb`, `discovery/classifier`, `instrument`, `parser`, all three reporters,
`runner/parallel` and `runner/types`. The following have **no** dedicated test file:

| File | Untested surface |
|---|---|
| `internal/cli/run.go` | `loadSetupScripts` glob expansion, dedup, error-on-no-match; `generateCoverageChannel` charset; `--fail-under` exit-code precedence |
| `internal/cli/merge.go` | input-count validation, stdout vs. file output |
| `internal/cli/report.go` | format validation, the `SetBaseDir` type assertion, stdout vs. file |
| `internal/discovery/discover.go` | `Discover` / `DiscoverCoLocatedSources`, incl. the CWD dependence in I13 |
| `internal/database/pool.go` | version-gate parsing and the `MaxConns` formula |
| `internal/database/listener.go` | `CollectSignals` window semantics (I12), `DroppedSignals` |
| `internal/runner/executor.go` | `filterSourcesByDirectory` (I10), `SummarizeRuns` |

Most of these are pure functions or need only a temp directory — `loadSetupScripts`,
`filterSourcesByDirectory`, `SummarizeRuns`, `generateCoverageChannel`,
`Discover` and the `report` format/base-dir plumbing all test without a database, and
`CollectSignals` tests against a hand-fed `signals` channel. Several of the findings above
(I10, I12, I13, I14) are exactly the class of defect these tests would have caught.

---

*Round 2 generated 2026-09-04 from a review of the full pgcov source tree at d258a76.
Claims marked "Verified" were confirmed by grep, by throwaway Go tests run against the
current packages, or by direct measurement; no finding was included on code reading
alone where a cheap empirical check was available.*
