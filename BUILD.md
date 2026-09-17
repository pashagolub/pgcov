# Building pgcov

Quick reference for building pgcov on different platforms.

## Requirements

- **Go**: 1.21+
- **C Compiler**: GCC (Linux/macOS) or MinGW-w64 (Windows)
- **CGO**: Must be enabled

## Linux

```bash
# Install compiler
sudo apt-get install build-essential  # Ubuntu/Debian
sudo dnf install gcc                   # Fedora/RHEL

# Build
export CGO_ENABLED=1
go build -o pgcov ./cmd/pgcov

# Test
go test ./...
```

## macOS

```bash
# Install compiler
xcode-select --install

# Build
export CGO_ENABLED=1
go build -o pgcov ./cmd/pgcov

# Test
go test ./...
```

## Windows

### PowerShell

```powershell
# Install MSYS2 from https://www.msys2.org/
# Then in MSYS2 terminal: pacman -S mingw-w64-x86_64-gcc

# Build
$env:CGO_ENABLED = "1"
$env:CC = "C:\msys64\mingw64\bin\gcc.exe"
$env:PATH = "$env:PATH;C:\msys64\mingw64\bin"
go build -o pgcov.exe .\cmd\pgcov

# Test
go test .\...
```

### CMD

```cmd
REM Build
set CGO_ENABLED=1
set CC=C:\msys64\mingw64\bin\gcc.exe
set PATH=%PATH%;C:\msys64\mingw64\bin
go build -o pgcov.exe .\cmd\pgcov

REM Test
go test .\...
```

## Build Scripts

### Linux/macOS (build.sh)

```bash
#!/bin/bash
set -e
export CGO_ENABLED=1
go build -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty)" -o pgcov ./cmd/pgcov
echo "Build complete"
```

`-X main.version=...` stamps the reported version into the binary. Without it
`pgcov --version` falls back to the module version the Go toolchain records
(set for `go install ...@version`) and otherwise reports `dev`.


### Windows (build.ps1)

```powershell
$ErrorActionPreference = "Stop"
$env:CGO_ENABLED = "1"
$env:CC = "C:\msys64\mingw64\bin\gcc.exe"
$env:PATH = "$env:PATH;C:\msys64\mingw64\bin"
go build -ldflags="-s -w -X main.version=$(git describe --tags --always --dirty)" -o pgcov.exe .\cmd\pgcov
Write-Host "Build complete"
```

## Common Issues

### "gcc: command not found"

**Linux**: `sudo apt-get install build-essential`
**macOS**: `xcode-select --install`
**Windows**: Install MSYS2 and add to PATH

### "Missing DLL" (Windows)

Add to PATH: `C:\msys64\mingw64\bin`

### "Cannot find package"

```bash
go mod download
go mod tidy
```

## Why CGO?

pgcov uses `pg_query_go` which wraps PostgreSQL's C parser (libpg_query). This provides native PostgreSQL SQL parsing but requires CGO.

## Clean Build

```bash
go clean -cache
go clean -testcache
go build ./cmd/pgcov
```

## See Also

- [README.md](README.md) - Full documentation
- [CONTRIBUTING.md](CONTRIBUTING.md) - Development guide
