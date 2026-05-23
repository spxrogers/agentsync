# agentsync M0 — Skeleton

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Conventions (TDD cycle, test framework, lint, commit format) are defined once in [`2026-05-04-agentsync-v1.0-overview.md`](2026-05-04-agentsync-v1.0-overview.md#conventions-used-across-all-milestone-plans).

**Goal:** Bootstrap the `agentsync` Go module with the foundational skeleton: path resolution honoring `AGENTSYNC_HOME` / `AGENTSYNC_TARGET_ROOT`, atomic file write + file lock, slog setup, canonical TOML schema + loader, JSON state store, adapter interface + registry, a `NoopAdapter` for testing the apply pipeline, and a thin cobra CLI exposing `init` / `agent {add,list,remove}` / `doctor` / `verify` / `apply --dry-run`. CI green.

**Architecture:** Single Go module (`github.com/spxrogers/agentsync`) → single static binary at `cmd/agentsync/`. Internal packages encapsulate one concern each. The `paths` helper is the only place real `$HOME` is touched in production code; tests redirect via `AGENTSYNC_TARGET_ROOT`. Adapter interface is defined here but no real adapter is implemented yet — `NoopAdapter` lets the apply pipeline run end-to-end with empty file operations so M1+ can plug in real adapters.

**Tech stack:** Go 1.22+, `github.com/spf13/cobra` (CLI), `github.com/pelletier/go-toml/v2` (TOML), `github.com/spf13/afero` (FS test fakes), `github.com/gofrs/flock` (file lock), `log/slog` (stdlib), `golangci-lint`, `goreleaser` (config only; release in M7).

---

## Files created in this milestone

```
agentsync/
├── go.mod, go.sum
├── .gitignore, Makefile, README.md
├── .goreleaser.yaml, .golangci.yml
├── .github/workflows/ci.yml
├── cmd/agentsync/main.go
└── internal/
    ├── paths/{paths.go, paths_test.go}
    ├── iox/{atomic.go, atomic_test.go, lock.go, lock_test.go}
    ├── log/{log.go, log_test.go}
    ├── source/{schema.go, loader.go, loader_test.go}
    ├── state/{schema.go, store.go, store_test.go}
    ├── adapter/{adapter.go, registry.go, registry_test.go, noop/{noop.go, noop_test.go}}
    ├── render/{pipeline.go, pipeline_test.go}
    └── cli/
        ├── root.go, root_test.go
        ├── init.go, init_test.go
        ├── agent.go, agent_test.go
        ├── doctor.go, doctor_test.go
        ├── verify.go, verify_test.go
        ├── apply.go, apply_test.go
        └── testhelper_test.go    # shared test helpers
```

---

## Task 0: Module + tooling scaffolding

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `README.md`, `.golangci.yml`, `.goreleaser.yaml`, `.github/workflows/ci.yml`, `cmd/agentsync/main.go`

- [ ] **Step 0.1: `go mod init`**

```bash
go mod init github.com/spxrogers/agentsync
```

Expected: creates `go.mod` with `module github.com/spxrogers/agentsync` and `go 1.22` (or current toolchain).

- [ ] **Step 0.2: `.gitignore`**

```gitignore
/bin/
/dist/
/agentsync
/agentsync.exe
/coverage.out
/coverage.html
.vscode/
.idea/
*.swp
.DS_Store
```

- [ ] **Step 0.3: `Makefile`** (real tabs!)

```make
.PHONY: build test lint fmt tidy ci clean

build:
	go build -o bin/agentsync ./cmd/agentsync

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w -s .
	go run mvdan.cc/gofumpt@latest -w .

tidy:
	go mod tidy

ci: lint test
	goreleaser release --snapshot --skip publish --clean

clean:
	rm -rf bin/ dist/ coverage.out coverage.html
```

- [ ] **Step 0.4: `README.md`**

```markdown
# agentsync

Centrally manages AI coding-agent configurations across Claude Code, OpenCode, Codex CLI, and Cursor.

**Status:** pre-release. Design at `docs/superpowers/specs/2026-05-04-agentsync-design.md`. Implementation roadmap at `docs/superpowers/plans/`.

Distribution (coming in v1.0 M7): Homebrew, Scoop, Chocolatey, native Linux packages. No `go install`, no npm, no curl-bash.

## Build from source

    git clone https://github.com/spxrogers/agentsync.git
    cd agentsync
    make build
```

- [ ] **Step 0.5: `.golangci.yml`**

```yaml
run:
  timeout: 3m
  tests: true

linters:
  enable:
    - govet
    - staticcheck
    - errcheck
    - gocritic
    - ineffassign
    - gofumpt
    - forbidigo

linters-settings:
  forbidigo:
    forbid:
      - p: '^os\.UserHomeDir$'
        msg: "use paths.HomeDir(env) so AGENTSYNC_TARGET_ROOT honored in tests"
        # apply only to test files
        pattern: ".*"
    analyze-types: true

issues:
  exclude-rules:
    - path: '(^|/)cmd/.*'
      linters: [forbidigo]    # main.go can use os.UserHomeDir if needed
```

(Note: forbidigo's `_test.go`-only filter is configured via `issues.exclude-rules` invert; if golangci-lint version doesn't support `pattern:`, switch to: enable `forbidigo` globally then `exclude-rules` with `path-except: '_test\.go$'` for the rule. Confirm with `golangci-lint run --help` on the version installed.)

- [ ] **Step 0.6: `.goreleaser.yaml`** (config only; pipeline wired in M7)

```yaml
project_name: agentsync

builds:
  - id: agentsync
    main: ./cmd/agentsync
    binary: agentsync
    env: [CGO_ENABLED=0]
    goos: [linux, darwin, windows]
    goarch: [amd64, arm64]
    ldflags:
      - -s -w
      - -X main.version={{.Version}}
      - -X main.commit={{.ShortCommit}}
      - -X main.date={{.Date}}

archives:
  - id: default
    formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]

checksum:
  name_template: "checksums.txt"

snapshot:
  version_template: "{{ incpatch .Version }}-snapshot-{{.ShortCommit}}"

# Homebrew/Scoop/Chocolatey blocks added in M7.
```

- [ ] **Step 0.7: `.github/workflows/ci.yml`**

```yaml
name: ci
on:
  pull_request:
  push:
    branches: [main]

jobs:
  test:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22.x' }
      - run: go test -race ./...

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22.x' }
      - uses: golangci/golangci-lint-action@v6
        with: { version: latest }

  goreleaser-snapshot:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: actions/setup-go@v5
        with: { go-version: '1.22.x' }
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: latest
          args: release --snapshot --skip publish --clean
```

- [ ] **Step 0.8: `cmd/agentsync/main.go`** (placeholder; cli.Execute defined in Task 13)

```go
package main

import (
    "fmt"
    "os"
)

var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)

func main() {
    if len(os.Args) > 1 && os.Args[1] == "--version" {
        fmt.Printf("agentsync %s (commit %s, built %s)\n", version, commit, date)
        return
    }
    fmt.Println("agentsync — placeholder; cli wiring lands in Task 13")
}
```

- [ ] **Step 0.9: Verify build + commit**

```bash
go build ./cmd/agentsync
./agentsync --version
```

Expected: `agentsync dev (commit none, built unknown)`

```bash
git add .
git commit -m "$(cat <<'EOF'
chore: initialize Go module + tooling scaffolding

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 1: `internal/paths` — `AGENTSYNC_HOME` / `AGENTSYNC_TARGET_ROOT` resolution

**Files:**
- Create: `internal/paths/paths.go`, `internal/paths/paths_test.go`

The single chokepoint where production code maps logical names ("home dir," "config dir") to filesystem paths. Tests redirect via `AGENTSYNC_TARGET_ROOT`.

- [ ] **Step 1.1: Write the failing test**

`internal/paths/paths_test.go`:

```go
package paths_test

import (
    "path/filepath"
    "testing"

    "github.com/spxrogers/agentsync/internal/paths"
)

func TestHomeDir(t *testing.T) {
    cases := []struct {
        name string
        env  map[string]string
        want string
    }{
        {
            name: "AGENTSYNC_TARGET_ROOT overrides everything",
            env:  map[string]string{"AGENTSYNC_TARGET_ROOT": "/tmp/redirect", "HOME": "/Users/real"},
            want: "/tmp/redirect",
        },
        {
            name: "falls back to HOME when no override",
            env:  map[string]string{"HOME": "/Users/real"},
            want: "/Users/real",
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := paths.HomeDir(paths.MapEnv(tc.env))
            if got != tc.want {
                t.Fatalf("HomeDir = %q, want %q", got, tc.want)
            }
        })
    }
}

func TestAgentsyncHome(t *testing.T) {
    cases := []struct {
        name string
        env  map[string]string
        want string
    }{
        {
            name: "AGENTSYNC_HOME explicit override",
            env:  map[string]string{"AGENTSYNC_HOME": "/explicit/path", "HOME": "/Users/real"},
            want: "/explicit/path",
        },
        {
            name: "default ~/.agentsync under HOME",
            env:  map[string]string{"HOME": "/Users/real"},
            want: filepath.Join("/Users/real", ".agentsync"),
        },
        {
            name: "AGENTSYNC_TARGET_ROOT shifts default",
            env:  map[string]string{"AGENTSYNC_TARGET_ROOT": "/tmp/x", "HOME": "/Users/real"},
            want: filepath.Join("/tmp/x", ".agentsync"),
        },
    }
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            got := paths.AgentsyncHome(paths.MapEnv(tc.env))
            if got != tc.want {
                t.Fatalf("AgentsyncHome = %q, want %q", got, tc.want)
            }
        })
    }
}
```

- [ ] **Step 1.2: Run; verify failure**

```bash
go test ./internal/paths/...
```

Expected: `package github.com/spxrogers/agentsync/internal/paths is not in std`. Test file references symbols that don't exist yet (`paths.HomeDir`, `paths.AgentsyncHome`, `paths.MapEnv`).

- [ ] **Step 1.3: Implement**

`internal/paths/paths.go`:

```go
// Package paths centralizes filesystem path resolution honoring AGENTSYNC_HOME
// and AGENTSYNC_TARGET_ROOT. Production code MUST use this package; lint forbids
// os.UserHomeDir in *_test.go files.
package paths

import (
    "os"
    "path/filepath"
)

// Env abstracts environment-variable lookup so tests can inject a fake.
type Env interface {
    Get(key string) string
}

// OSEnv reads the live process environment.
type OSEnv struct{}

func (OSEnv) Get(key string) string { return os.Getenv(key) }

// MapEnv is a fake Env backed by a map (for tests).
type MapEnv map[string]string

func (m MapEnv) Get(key string) string { return m[key] }

// HomeDir returns the effective home dir. AGENTSYNC_TARGET_ROOT takes precedence
// (used by tests to redirect away from the real $HOME); otherwise falls back to
// $HOME.
func HomeDir(e Env) string {
    if root := e.Get("AGENTSYNC_TARGET_ROOT"); root != "" {
        return root
    }
    return e.Get("HOME")
}

// AgentsyncHome returns the directory where agentsync stores its source repo.
// Resolution order:
//   1. $AGENTSYNC_HOME (explicit override; absolute path)
//   2. <HomeDir>/.agentsync
func AgentsyncHome(e Env) string {
    if h := e.Get("AGENTSYNC_HOME"); h != "" {
        return h
    }
    return filepath.Join(HomeDir(e), ".agentsync")
}
```

- [ ] **Step 1.4: Run; verify pass**

```bash
go test ./internal/paths/...
```

Expected: `ok github.com/spxrogers/agentsync/internal/paths`

- [ ] **Step 1.5: Commit**

```bash
git add internal/paths
git commit -m "$(cat <<'EOF'
feat(paths): resolve AGENTSYNC_HOME with target-root override

Single chokepoint for HOME-relative paths. AGENTSYNC_TARGET_ROOT lets tests
redirect to tmpdirs without touching real $HOME. Lint rule (forbidigo) bans
os.UserHomeDir in *_test.go from M0 onward.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `internal/iox` — atomic file write

**Files:**
- Create: `internal/iox/atomic.go`, `internal/iox/atomic_test.go`

Two-phase write: write to `<dest>.agentsync.tmp`, fsync, rename to final. A crash mid-write leaves either old or new content, never partial.

- [ ] **Step 2.1: Write the failing test**

`internal/iox/atomic_test.go`:

```go
package iox_test

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/spxrogers/agentsync/internal/iox"
)

func TestAtomicWrite_NewFile(t *testing.T) {
    dir := t.TempDir()
    dest := filepath.Join(dir, "config.toml")
    payload := []byte("hello\n")

    if err := iox.AtomicWrite(dest, payload, 0o644); err != nil {
        t.Fatalf("AtomicWrite: %v", err)
    }

    got, err := os.ReadFile(dest)
    if err != nil {
        t.Fatalf("ReadFile: %v", err)
    }
    if string(got) != string(payload) {
        t.Fatalf("content = %q, want %q", got, payload)
    }

    info, err := os.Stat(dest)
    if err != nil {
        t.Fatalf("Stat: %v", err)
    }
    // mode comparison is platform-aware; on windows mode bits are ignored.
    if info.Mode().Perm() != 0o644 {
        t.Logf("mode = %v (informational; windows ignores)", info.Mode().Perm())
    }
}

func TestAtomicWrite_OverwriteExisting(t *testing.T) {
    dir := t.TempDir()
    dest := filepath.Join(dir, "config.toml")
    if err := os.WriteFile(dest, []byte("old\n"), 0o644); err != nil {
        t.Fatal(err)
    }

    if err := iox.AtomicWrite(dest, []byte("new\n"), 0o644); err != nil {
        t.Fatalf("AtomicWrite: %v", err)
    }
    got, _ := os.ReadFile(dest)
    if string(got) != "new\n" {
        t.Fatalf("content = %q, want %q", got, "new\n")
    }
}

func TestAtomicWrite_LeavesNoTempFile(t *testing.T) {
    dir := t.TempDir()
    dest := filepath.Join(dir, "config.toml")
    if err := iox.AtomicWrite(dest, []byte("x\n"), 0o644); err != nil {
        t.Fatal(err)
    }
    entries, _ := os.ReadDir(dir)
    if len(entries) != 1 {
        t.Fatalf("dir entries = %d, want 1 (only the dest); got: %+v", len(entries), entries)
    }
}
```

- [ ] **Step 2.2: Run; verify failure**

```bash
go test ./internal/iox/...
```

Expected: `undefined: iox.AtomicWrite`

- [ ] **Step 2.3: Implement**

`internal/iox/atomic.go`:

```go
// Package iox provides atomic file IO and file-locking primitives used by
// agentsync's apply pipeline.
package iox

import (
    "fmt"
    "os"
    "path/filepath"
)

// AtomicWrite writes data to dest using a two-phase approach: write to a
// sibling .agentsync.tmp file, fsync it, then rename(2) into place. If the
// process crashes between phases, the destination is either the old content
// (rename did not run) or the new content (rename ran). Never partial.
//
// Parent directory is created if missing (with mode 0o755).
func AtomicWrite(dest string, data []byte, mode os.FileMode) error {
    if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
        return fmt.Errorf("mkdir parent of %s: %w", dest, err)
    }
    tmp := dest + ".agentsync.tmp"

    f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
    if err != nil {
        return fmt.Errorf("open temp %s: %w", tmp, err)
    }
    if _, err := f.Write(data); err != nil {
        _ = f.Close()
        _ = os.Remove(tmp)
        return fmt.Errorf("write temp %s: %w", tmp, err)
    }
    if err := f.Sync(); err != nil {
        _ = f.Close()
        _ = os.Remove(tmp)
        return fmt.Errorf("sync temp %s: %w", tmp, err)
    }
    if err := f.Close(); err != nil {
        _ = os.Remove(tmp)
        return fmt.Errorf("close temp %s: %w", tmp, err)
    }
    if err := os.Rename(tmp, dest); err != nil {
        _ = os.Remove(tmp)
        return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
    }
    return nil
}
```

- [ ] **Step 2.4: Run; verify pass**

```bash
go test -race ./internal/iox/...
```

Expected: `ok github.com/spxrogers/agentsync/internal/iox`

- [ ] **Step 2.5: Commit**

```bash
git add internal/iox
git commit -m "$(cat <<'EOF'
feat(iox): atomic write via .agentsync.tmp + fsync + rename

A crash mid-apply leaves either old or new content, never partial. Used
throughout agentsync's apply pipeline.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `internal/iox` — file lock

**Files:**
- Create: `internal/iox/lock.go`, `internal/iox/lock_test.go`
- Modify: `go.mod` (adds `github.com/gofrs/flock`)

Cross-process file lock so concurrent `agentsync apply` runs serialize cleanly.

- [ ] **Step 3.1: Add dependency**

```bash
go get github.com/gofrs/flock@latest
```

- [ ] **Step 3.2: Write the failing test**

`internal/iox/lock_test.go`:

```go
package iox_test

import (
    "path/filepath"
    "sync"
    "testing"
    "time"

    "github.com/spxrogers/agentsync/internal/iox"
)

func TestLock_AcquireRelease(t *testing.T) {
    p := filepath.Join(t.TempDir(), "test.lock")
    l, err := iox.AcquireLock(p)
    if err != nil {
        t.Fatalf("AcquireLock: %v", err)
    }
    if err := l.Release(); err != nil {
        t.Fatalf("Release: %v", err)
    }
}

func TestLock_SecondAcquireBlocks(t *testing.T) {
    p := filepath.Join(t.TempDir(), "test.lock")

    l1, err := iox.AcquireLock(p)
    if err != nil {
        t.Fatalf("first AcquireLock: %v", err)
    }
    t.Cleanup(func() { _ = l1.Release() })

    var (
        wg     sync.WaitGroup
        result error
    )
    wg.Add(1)
    go func() {
        defer wg.Done()
        l2, err := iox.AcquireLockTimeout(p, 100*time.Millisecond)
        if err == nil {
            _ = l2.Release()
        }
        result = err
    }()
    wg.Wait()
    if result == nil {
        t.Fatalf("second AcquireLockTimeout returned nil; expected lock-busy error")
    }
}
```

- [ ] **Step 3.3: Run; verify failure**

```bash
go test ./internal/iox/...
```

Expected: `undefined: iox.AcquireLock`, `undefined: iox.AcquireLockTimeout`

- [ ] **Step 3.4: Implement**

`internal/iox/lock.go`:

```go
package iox

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
    "time"

    "github.com/gofrs/flock"
)

// Lock represents an acquired exclusive file lock. Release() drops it.
type Lock struct {
    fl *flock.Flock
}

// Release drops the lock. Idempotent.
func (l *Lock) Release() error {
    if l == nil || l.fl == nil {
        return nil
    }
    return l.fl.Unlock()
}

// AcquireLock takes an exclusive lock on path, blocking forever until it
// succeeds. The parent directory is created if missing.
func AcquireLock(path string) (*Lock, error) {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return nil, fmt.Errorf("mkdir parent of lock %s: %w", path, err)
    }
    fl := flock.New(path)
    if err := fl.Lock(); err != nil {
        return nil, fmt.Errorf("lock %s: %w", path, err)
    }
    return &Lock{fl: fl}, nil
}

// AcquireLockTimeout takes an exclusive lock on path, returning an error if
// the lock cannot be acquired within timeout.
func AcquireLockTimeout(path string, timeout time.Duration) (*Lock, error) {
    if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
        return nil, fmt.Errorf("mkdir parent of lock %s: %w", path, err)
    }
    fl := flock.New(path)
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()
    locked, err := fl.TryLockContext(ctx, 25*time.Millisecond)
    if err != nil {
        return nil, fmt.Errorf("locking %s: %w", path, err)
    }
    if !locked {
        return nil, fmt.Errorf("lock %s busy after %s", path, timeout)
    }
    return &Lock{fl: fl}, nil
}
```

- [ ] **Step 3.5: Run; verify pass; commit**

```bash
go test -race ./internal/iox/...
```

Expected: `ok github.com/spxrogers/agentsync/internal/iox`

```bash
git add go.mod go.sum internal/iox
git commit -m "$(cat <<'EOF'
feat(iox): cross-process file lock via gofrs/flock

apply.lock + reconcile.lock will sit in ~/.agentsync/.state/. Two concurrent
agentsync apply invocations serialize on the lock; AcquireLockTimeout fails
fast for tools that prefer error over indefinite wait.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `internal/log` — slog wrapper

**Files:**
- Create: `internal/log/log.go`, `internal/log/log_test.go`

Tiny wrapper around `log/slog` so the rest of the codebase imports one package and a `--verbose` global flag flips levels.

- [ ] **Step 4.1: Write the failing test**

`internal/log/log_test.go`:

```go
package log_test

import (
    "bytes"
    "log/slog"
    "strings"
    "testing"

    aslog "github.com/spxrogers/agentsync/internal/log"
)

func TestNew_DefaultLevel(t *testing.T) {
    var buf bytes.Buffer
    lg := aslog.New(&buf, false)
    lg.Info("hello", slog.String("k", "v"))
    lg.Debug("invisible at default level")

    out := buf.String()
    if !strings.Contains(out, `"msg":"hello"`) {
        t.Fatalf("expected info-level msg, got: %s", out)
    }
    if strings.Contains(out, "invisible") {
        t.Fatalf("debug message leaked into default-level output: %s", out)
    }
}

func TestNew_VerboseLevel(t *testing.T) {
    var buf bytes.Buffer
    lg := aslog.New(&buf, true)
    lg.Debug("now visible")

    if !strings.Contains(buf.String(), "now visible") {
        t.Fatalf("debug message missing in verbose output: %s", buf.String())
    }
}
```

- [ ] **Step 4.2: Run; verify failure**

Expected: `undefined: aslog.New`

- [ ] **Step 4.3: Implement**

`internal/log/log.go`:

```go
// Package log centralizes slog setup. CLI commands receive *slog.Logger from
// the root cobra command's PersistentPreRun.
package log

import (
    "io"
    "log/slog"
)

// New returns a JSON slog.Logger writing to w. If verbose is true, level is
// Debug; otherwise Info.
func New(w io.Writer, verbose bool) *slog.Logger {
    level := slog.LevelInfo
    if verbose {
        level = slog.LevelDebug
    }
    return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}
```

- [ ] **Step 4.4: Run; verify pass; commit**

```bash
go test -race ./internal/log/...
```

```bash
git add internal/log
git commit -m "$(cat <<'EOF'
feat(log): slog JSON logger with --verbose toggle

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `internal/source` — canonical schema (TOML structs)

**Files:**
- Create: `internal/source/schema.go`

Defines the Go structs that ARE agentsync's canonical model. No test for this task in isolation — exercised by Task 6's loader tests.

- [ ] **Step 5.1: Add TOML dependency**

```bash
go get github.com/pelletier/go-toml/v2@latest
```

- [ ] **Step 5.2: Create schema**

`internal/source/schema.go`:

```go
// Package source loads and represents the canonical agentsync repo layout
// (~/.agentsync/). Structs in this file are TOML-tagged and serve as the
// canonical model — there is no separate IR layer; adapters consume these
// types directly.
package source

// Canonical is the in-memory image of a fully-loaded ~/.agentsync/ tree.
type Canonical struct {
    Config      Config
    MCPServers  []MCPServer
    Skills      []Skill
    Plugins     []Plugin
    Marketplaces []Marketplace
    Memory      Memory
    Project     *Canonical // nil for user-scope canonical; populated by M5 overlay
}

// Config mirrors agentsync.toml at the root of ~/.agentsync/.
type Config struct {
    Agents   map[string]Agent     `toml:"agents"`
    Updates  UpdateDefaults       `toml:"updates"`
    Secrets  SecretsConfig        `toml:"secrets"`
}

type Agent struct {
    Enabled bool   `toml:"enabled"`
    Scope   string `toml:"scope,omitempty"` // "user" | "project"
}

type UpdateDefaults struct {
    DefaultMode     string `toml:"default_mode"`     // pinned | track | manual
    DefaultInterval string `toml:"default_interval"` // e.g. "24h"
}

type SecretsConfig struct {
    Backend      string `toml:"backend"`       // "env" | "age"
    File         string `toml:"file"`
    Recipient    string `toml:"recipient"`
    IdentityFile string `toml:"identity_file"`
}

// MCPServer mirrors mcp/<id>.toml.
type MCPServer struct {
    ID      string             `toml:"-"` // filename minus .toml
    Server  MCPServerSpec      `toml:"server"`
}

type MCPServerSpec struct {
    Type     string            `toml:"type"`     // stdio | http | sse
    Command  string            `toml:"command,omitempty"`
    Args     []string          `toml:"args,omitempty"`
    URL      string            `toml:"url,omitempty"`
    Headers  map[string]string `toml:"headers,omitempty"`
    Env      map[string]string `toml:"env,omitempty"`
    Agents   []string          `toml:"agents,omitempty"` // ["*"] or ["claude","opencode"]
    Enabled  *bool             `toml:"enabled,omitempty"` // nil means default-on
}

// Skill mirrors skills/<name>/SKILL.md (frontmatter + body).
type Skill struct {
    Name        string                 `toml:"-"` // dirname
    Frontmatter map[string]any         `toml:"-"` // YAML frontmatter parsed
    Body        string                 `toml:"-"` // markdown body
}

// Plugin mirrors plugins/<id>.toml.
type Plugin struct {
    ID           string                       `toml:"-"`
    Plugin       PluginSpec                   `toml:"plugin"`
    Overrides    map[string]PluginOverrideSet `toml:"plugin.overrides"` // per-agent
}

type PluginSpec struct {
    ID          string   `toml:"id"`
    Version     string   `toml:"version,omitempty"`
    ManifestSHA string   `toml:"manifest_sha,omitempty"`
    Update      string   `toml:"update,omitempty"` // pinned | track | manual
    Agents      []string `toml:"agents,omitempty"`
}

// PluginOverrideSet captures per-agent component overrides for one plugin.
// e.g. [plugin.overrides.cursor] commands = "skip"
type PluginOverrideSet map[string]string // component -> action ("skip" today; future: "force", etc.)

// Marketplace mirrors marketplaces/<name>.toml.
type Marketplace struct {
    Name        string             `toml:"-"`
    Marketplace MarketplaceSpec    `toml:"marketplace"`
}

type MarketplaceSpec struct {
    URL               string `toml:"url"`
    Ref               string `toml:"ref,omitempty"`
    DefaultUpdateMode string `toml:"default_update_mode,omitempty"`
}

// Memory mirrors memory/AGENTS.md and memory/fragments/.
type Memory struct {
    Body      string                  // resolved AGENTS.md after @import expansion
    Fragments map[string]string       // path -> body, keyed by repo-relative path under memory/
}
```

- [ ] **Step 5.3: Verify it compiles + commit**

```bash
go build ./internal/source/...
```

Expected: clean (no test yet).

```bash
git add go.mod go.sum internal/source
git commit -m "$(cat <<'EOF'
feat(source): canonical schema for ~/.agentsync/ — TOML structs are the model

No separate IR layer; adapters consume these structs directly. Memory keeps
markdown body verbatim plus resolved fragments. Plugin.Overrides keys are
per-agent component name -> action.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `internal/source` — loader

**Files:**
- Create: `internal/source/loader.go`, `internal/source/loader_test.go`

Walks `<home>/` and produces a `Canonical`. Uses `afero.Fs` so tests can stay in-memory.

- [ ] **Step 6.1: Add afero dependency**

```bash
go get github.com/spf13/afero@latest
```

- [ ] **Step 6.2: Write the failing test**

`internal/source/loader_test.go`:

```go
package source_test

import (
    "testing"

    "github.com/spf13/afero"
    "github.com/spxrogers/agentsync/internal/source"
)

func TestLoad_EmptyHome(t *testing.T) {
    fs := afero.NewMemMapFs()
    _ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(""), 0o644)

    c, err := source.Load(fs, "/home/.agentsync")
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if len(c.MCPServers) != 0 {
        t.Fatalf("expected no MCP servers, got %d", len(c.MCPServers))
    }
}

func TestLoad_AgentsAndMCP(t *testing.T) {
    fs := afero.NewMemMapFs()
    _ = afero.WriteFile(fs, "/home/.agentsync/agentsync.toml", []byte(`
[agents]
claude   = { enabled = true,  scope = "user" }
opencode = { enabled = true }
`), 0o644)
    _ = afero.WriteFile(fs, "/home/.agentsync/mcp/github.toml", []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
agents  = ["claude", "opencode"]

[server.env]
GITHUB_TOKEN = "${secret:github.token}"
`), 0o644)

    c, err := source.Load(fs, "/home/.agentsync")
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    if !c.Config.Agents["claude"].Enabled {
        t.Fatalf("claude agent should be enabled")
    }
    if c.Config.Agents["claude"].Scope != "user" {
        t.Fatalf("claude scope = %q, want user", c.Config.Agents["claude"].Scope)
    }
    if len(c.MCPServers) != 1 {
        t.Fatalf("expected 1 MCP server, got %d", len(c.MCPServers))
    }
    g := c.MCPServers[0]
    if g.ID != "github" {
        t.Fatalf("MCP id = %q, want github", g.ID)
    }
    if g.Server.Type != "stdio" {
        t.Fatalf("MCP type = %q, want stdio", g.Server.Type)
    }
    if g.Server.Env["GITHUB_TOKEN"] != "${secret:github.token}" {
        t.Fatalf("env GITHUB_TOKEN = %q", g.Server.Env["GITHUB_TOKEN"])
    }
}

func TestLoad_MissingHomeReturnsEmpty(t *testing.T) {
    fs := afero.NewMemMapFs()
    c, err := source.Load(fs, "/nonexistent")
    if err != nil {
        t.Fatalf("Load nonexistent: %v", err)
    }
    if len(c.MCPServers) != 0 {
        t.Fatalf("expected empty canonical, got %d MCP", len(c.MCPServers))
    }
}
```

- [ ] **Step 6.3: Run; verify failure**

Expected: `undefined: source.Load`

- [ ] **Step 6.4: Implement**

`internal/source/loader.go`:

```go
package source

import (
    "errors"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/pelletier/go-toml/v2"
    "github.com/spf13/afero"
)

// Load reads a canonical model from <home>. Missing home or missing
// subdirectories return an empty Canonical (not an error). Malformed files
// return an error with a path prefix for actionability.
func Load(fs afero.Fs, home string) (Canonical, error) {
    var c Canonical

    if err := loadConfig(fs, home, &c.Config); err != nil {
        return c, err
    }
    var err error
    if c.MCPServers, err = loadMCP(fs, home); err != nil {
        return c, err
    }
    if c.Plugins, err = loadPlugins(fs, home); err != nil {
        return c, err
    }
    if c.Marketplaces, err = loadMarketplaces(fs, home); err != nil {
        return c, err
    }
    if c.Skills, err = loadSkills(fs, home); err != nil {
        return c, err
    }
    if c.Memory, err = loadMemory(fs, home); err != nil {
        return c, err
    }
    return c, nil
}

func loadConfig(fs afero.Fs, home string, cfg *Config) error {
    p := filepath.Join(home, "agentsync.toml")
    data, err := afero.ReadFile(fs, p)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil
        }
        return fmt.Errorf("read %s: %w", p, err)
    }
    if err := toml.Unmarshal(data, cfg); err != nil {
        return fmt.Errorf("parse %s: %w", p, err)
    }
    return nil
}

func loadMCP(fs afero.Fs, home string) ([]MCPServer, error) {
    dir := filepath.Join(home, "mcp")
    entries, err := afero.ReadDir(fs, dir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, fmt.Errorf("read %s: %w", dir, err)
    }
    var out []MCPServer
    for _, e := range entries {
        if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
            continue
        }
        p := filepath.Join(dir, e.Name())
        data, err := afero.ReadFile(fs, p)
        if err != nil {
            return nil, fmt.Errorf("read %s: %w", p, err)
        }
        var m MCPServer
        if err := toml.Unmarshal(data, &m); err != nil {
            return nil, fmt.Errorf("parse %s: %w", p, err)
        }
        m.ID = strings.TrimSuffix(e.Name(), ".toml")
        out = append(out, m)
    }
    return out, nil
}

func loadPlugins(fs afero.Fs, home string) ([]Plugin, error) {
    dir := filepath.Join(home, "plugins")
    entries, err := afero.ReadDir(fs, dir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, fmt.Errorf("read %s: %w", dir, err)
    }
    var out []Plugin
    for _, e := range entries {
        if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
            continue
        }
        p := filepath.Join(dir, e.Name())
        data, err := afero.ReadFile(fs, p)
        if err != nil {
            return nil, fmt.Errorf("read %s: %w", p, err)
        }
        var pl Plugin
        if err := toml.Unmarshal(data, &pl); err != nil {
            return nil, fmt.Errorf("parse %s: %w", p, err)
        }
        pl.ID = strings.TrimSuffix(e.Name(), ".toml")
        out = append(out, pl)
    }
    return out, nil
}

func loadMarketplaces(fs afero.Fs, home string) ([]Marketplace, error) {
    dir := filepath.Join(home, "marketplaces")
    entries, err := afero.ReadDir(fs, dir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, fmt.Errorf("read %s: %w", dir, err)
    }
    var out []Marketplace
    for _, e := range entries {
        if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
            continue
        }
        p := filepath.Join(dir, e.Name())
        data, err := afero.ReadFile(fs, p)
        if err != nil {
            return nil, fmt.Errorf("read %s: %w", p, err)
        }
        var m Marketplace
        if err := toml.Unmarshal(data, &m); err != nil {
            return nil, fmt.Errorf("parse %s: %w", p, err)
        }
        m.Name = strings.TrimSuffix(e.Name(), ".toml")
        out = append(out, m)
    }
    return out, nil
}

// loadSkills walks skills/<name>/SKILL.md. Frontmatter parsing is added in M1
// when the Claude adapter actually uses skills; for M0 we just record the
// skill names + body so the canonical model is complete.
func loadSkills(fs afero.Fs, home string) ([]Skill, error) {
    dir := filepath.Join(home, "skills")
    entries, err := afero.ReadDir(fs, dir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, fmt.Errorf("read %s: %w", dir, err)
    }
    var out []Skill
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        body, err := afero.ReadFile(fs, filepath.Join(dir, e.Name(), "SKILL.md"))
        if err != nil {
            if errors.Is(err, os.ErrNotExist) {
                continue
            }
            return nil, fmt.Errorf("read SKILL.md for %s: %w", e.Name(), err)
        }
        out = append(out, Skill{Name: e.Name(), Body: string(body)})
    }
    return out, nil
}

func loadMemory(fs afero.Fs, home string) (Memory, error) {
    var m Memory
    body, err := afero.ReadFile(fs, filepath.Join(home, "memory", "AGENTS.md"))
    if err != nil && !errors.Is(err, os.ErrNotExist) {
        return m, fmt.Errorf("read memory/AGENTS.md: %w", err)
    }
    m.Body = string(body)

    m.Fragments = map[string]string{}
    fragDir := filepath.Join(home, "memory", "fragments")
    entries, err := afero.ReadDir(fs, fragDir)
    if err == nil {
        for _, e := range entries {
            if e.IsDir() {
                continue
            }
            data, err := afero.ReadFile(fs, filepath.Join(fragDir, e.Name()))
            if err != nil {
                return m, fmt.Errorf("read fragment %s: %w", e.Name(), err)
            }
            m.Fragments[e.Name()] = string(data)
        }
    } else if !errors.Is(err, os.ErrNotExist) {
        return m, fmt.Errorf("read memory/fragments: %w", err)
    }
    return m, nil
}
```

- [ ] **Step 6.5: Run; verify pass; commit**

```bash
go test -race ./internal/source/...
```

```bash
git add go.mod go.sum internal/source
git commit -m "$(cat <<'EOF'
feat(source): loader walks ~/.agentsync/ -> Canonical

Reads agentsync.toml, mcp/, plugins/, marketplaces/, skills/, memory/.
Missing dirs return empty (not error); malformed files return path-prefixed
errors. Frontmatter parsing for skills lands in M1 where it's first used.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: `internal/state` — schema + store

**Files:**
- Create: `internal/state/schema.go`, `internal/state/store.go`, `internal/state/store_test.go`

Single JSON file at `.state/targets.json`. Atomic via `iox.AtomicWrite`.

- [ ] **Step 7.1: Schema**

`internal/state/schema.go`:

```go
// Package state persists agentsync's last-applied hashes and marketplace/plugin
// pinning to ~/.agentsync/.state/targets.json.
package state

import "time"

const SchemaVersion = 1

// Targets is the root state document.
type Targets struct {
    SchemaVersion int                       `json:"schema_version"`
    Files         map[string]FileEntry      `json:"files,omitempty"`
    Keys          map[string]KeyEntry       `json:"keys,omitempty"`
    Marketplaces  map[string]Marketplace    `json:"marketplaces,omitempty"`
    Plugins       map[string]PluginEntry    `json:"plugins,omitempty"`
}

// FileEntry tracks one fully-managed destination file.
// Key format: "<agent>:<scope>:<project>:<dest_path>"
type FileEntry struct {
    SHA256    string    `json:"sha256"`
    Mode      uint32    `json:"mode"`
    AppliedAt time.Time `json:"applied_at"`
    SourceID  string    `json:"source_id"` // canonical file that produced this dest
}

// KeyEntry tracks one managed JSON-pointer-addressable key inside a shared
// destination file.
// Key format: "<agent>:<scope>:<project>:<file>:<json_pointer>"
type KeyEntry struct {
    SHA256    string    `json:"sha256"`
    AppliedAt time.Time `json:"applied_at"`
    SourceID  string    `json:"source_id"`
}

type Marketplace struct {
    URL       string    `json:"url"`
    Ref       string    `json:"ref"`
    HeadSHA   string    `json:"head_sha"`
    FetchedAt time.Time `json:"fetched_at"`
}

type PluginEntry struct {
    Version     string `json:"version"`
    ManifestSHA string `json:"manifest_sha"`
    Enabled     bool   `json:"enabled"`
}

// New returns a fresh empty Targets at SchemaVersion.
func New() *Targets {
    return &Targets{
        SchemaVersion: SchemaVersion,
        Files:         map[string]FileEntry{},
        Keys:          map[string]KeyEntry{},
        Marketplaces:  map[string]Marketplace{},
        Plugins:       map[string]PluginEntry{},
    }
}
```

- [ ] **Step 7.2: Test for store load/save**

`internal/state/store_test.go`:

```go
package state_test

import (
    "os"
    "path/filepath"
    "testing"
    "time"

    "github.com/spxrogers/agentsync/internal/state"
)

func TestStore_RoundTrip(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "targets.json")

    in := state.New()
    in.Files["claude:user::~/.claude/settings.json"] = state.FileEntry{
        SHA256:    "abc",
        Mode:      0o644,
        AppliedAt: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
        SourceID:  "mcp/github.toml",
    }

    if err := state.Save(p, in); err != nil {
        t.Fatalf("Save: %v", err)
    }
    out, err := state.Load(p)
    if err != nil {
        t.Fatalf("Load: %v", err)
    }
    got := out.Files["claude:user::~/.claude/settings.json"]
    if got.SHA256 != "abc" || got.SourceID != "mcp/github.toml" {
        t.Fatalf("entry round-trip lost data: %+v", got)
    }
    if out.SchemaVersion != state.SchemaVersion {
        t.Fatalf("schema_version = %d", out.SchemaVersion)
    }
}

func TestStore_LoadMissingReturnsNew(t *testing.T) {
    p := filepath.Join(t.TempDir(), "missing.json")
    s, err := state.Load(p)
    if err != nil {
        t.Fatalf("Load missing: %v", err)
    }
    if s.SchemaVersion != state.SchemaVersion {
        t.Fatalf("missing-load did not produce a fresh state: %+v", s)
    }
}

func TestStore_AtomicReplaceLeavesNoTemp(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, "targets.json")
    if err := state.Save(p, state.New()); err != nil {
        t.Fatal(err)
    }
    if err := state.Save(p, state.New()); err != nil {
        t.Fatal(err)
    }
    entries, _ := os.ReadDir(dir)
    if len(entries) != 1 {
        t.Fatalf("expected 1 entry, got %d: %+v", len(entries), entries)
    }
}
```

- [ ] **Step 7.3: Implement store**

`internal/state/store.go`:

```go
package state

import (
    "encoding/json"
    "errors"
    "fmt"
    "os"

    "github.com/spxrogers/agentsync/internal/iox"
)

// Load reads targets.json from path. If the file is missing, returns a fresh
// state at the current SchemaVersion.
func Load(path string) (*Targets, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return New(), nil
        }
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    var t Targets
    if err := json.Unmarshal(data, &t); err != nil {
        return nil, fmt.Errorf("parse %s: %w", path, err)
    }
    if t.SchemaVersion == 0 {
        t.SchemaVersion = SchemaVersion
    }
    if t.Files == nil {
        t.Files = map[string]FileEntry{}
    }
    if t.Keys == nil {
        t.Keys = map[string]KeyEntry{}
    }
    if t.Marketplaces == nil {
        t.Marketplaces = map[string]Marketplace{}
    }
    if t.Plugins == nil {
        t.Plugins = map[string]PluginEntry{}
    }
    return &t, nil
}

// Save serializes t to path atomically (iox.AtomicWrite).
func Save(path string, t *Targets) error {
    if t == nil {
        return fmt.Errorf("save nil targets")
    }
    data, err := json.MarshalIndent(t, "", "  ")
    if err != nil {
        return fmt.Errorf("marshal targets: %w", err)
    }
    return iox.AtomicWrite(path, append(data, '\n'), 0o644)
}
```

- [ ] **Step 7.4: Run; verify; commit**

```bash
go test -race ./internal/state/...
```

```bash
git add internal/state
git commit -m "$(cat <<'EOF'
feat(state): targets.json read/save with atomic write + schema v1

Files map keyed by <agent>:<scope>:<project>:<dest>. Keys map keyed by
<agent>:<scope>:<project>:<file>:<json_pointer>. Loading a missing file
returns a fresh Targets so first-run never errors.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: `internal/adapter` — interface + registry

**Files:**
- Create: `internal/adapter/adapter.go`, `internal/adapter/registry.go`, `internal/adapter/registry_test.go`

Defines the `Adapter` interface that M1 (Claude), M2 (OpenCode), and later milestones implement. Registry is just a map.

- [ ] **Step 8.1: Interface + types**

`internal/adapter/adapter.go`:

```go
// Package adapter declares the interface every per-agent adapter implements.
// The registry holds zero or more concrete implementations; the apply pipeline
// asks each registered adapter to Render a CanonicalModel into FileOps.
package adapter

import (
    "github.com/spxrogers/agentsync/internal/source"
)

// Capability is a bitmask of components an adapter can produce. M1's Claude
// adapter is full-spectrum; M2's OpenCode adapter omits Hook + LSP.
type Capability uint32

const (
    CapMCP Capability = 1 << iota
    CapMemory
    CapSkill
    CapSubagent
    CapCommand
    CapHook
    CapLSP
)

// Scope distinguishes user-level vs project-level apply targets.
type Scope int

const (
    ScopeUser Scope = iota
    ScopeProject
)

func (s Scope) String() string {
    switch s {
    case ScopeProject:
        return "project"
    default:
        return "user"
    }
}

// FileOp describes one destination-side change. Action is "write" or "delete".
// Path is absolute (after AGENTSYNC_TARGET_ROOT redirection).
type FileOp struct {
    Action   string // "write" | "delete"
    Path     string
    Content  []byte
    Mode     uint32
    SourceID string // canonical source path that produced this op
}

// Skip describes a component the adapter chose not to render. Surfaces in the
// translation report and in `apply --strict`'s exit logic.
type Skip struct {
    Component string // "skill" | "subagent" | etc.
    Name      string
    Reason    string
}

// Adapter is the per-agent contract.
type Adapter interface {
    Name() string
    Capabilities() Capability
    Detect() (bool, error)
    Render(c source.Canonical, scope Scope, project string) ([]FileOp, []Skip, error)
    Ingest(scope Scope, project string) (source.Canonical, error)
    Apply(ops []FileOp) error
}
```

- [ ] **Step 8.2: Registry**

`internal/adapter/registry.go`:

```go
package adapter

import "fmt"

// Registry is an in-memory map of adapter name -> Adapter.
type Registry struct {
    items map[string]Adapter
}

func NewRegistry() *Registry {
    return &Registry{items: map[string]Adapter{}}
}

// Register adds a; returns error if name collides.
func (r *Registry) Register(a Adapter) error {
    name := a.Name()
    if _, ok := r.items[name]; ok {
        return fmt.Errorf("adapter %q already registered", name)
    }
    r.items[name] = a
    return nil
}

// Lookup returns the adapter for name, or nil.
func (r *Registry) Lookup(name string) Adapter { return r.items[name] }

// Names returns adapter names in deterministic order (sorted).
func (r *Registry) Names() []string {
    out := make([]string, 0, len(r.items))
    for n := range r.items {
        out = append(out, n)
    }
    // bubble sort small slice — keep stdlib only here
    for i := 1; i < len(out); i++ {
        for j := i; j > 0 && out[j-1] > out[j]; j-- {
            out[j-1], out[j] = out[j], out[j-1]
        }
    }
    return out
}
```

- [ ] **Step 8.3: Test for registry**

`internal/adapter/registry_test.go`:

```go
package adapter_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

type stub struct{ name string }

func (s stub) Name() string                                                                        { return s.name }
func (stub) Capabilities() adapter.Capability                                                      { return 0 }
func (stub) Detect() (bool, error)                                                                  { return false, nil }
func (stub) Render(source.Canonical, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
    return nil, nil, nil
}
func (stub) Ingest(adapter.Scope, string) (source.Canonical, error) { return source.Canonical{}, nil }
func (stub) Apply([]adapter.FileOp) error                            { return nil }

func TestRegistry_RegisterLookup(t *testing.T) {
    r := adapter.NewRegistry()
    if err := r.Register(stub{name: "x"}); err != nil {
        t.Fatal(err)
    }
    if r.Lookup("x") == nil {
        t.Fatalf("lookup x = nil")
    }
    if r.Lookup("missing") != nil {
        t.Fatalf("lookup missing should be nil")
    }
}

func TestRegistry_DuplicateError(t *testing.T) {
    r := adapter.NewRegistry()
    _ = r.Register(stub{name: "x"})
    if err := r.Register(stub{name: "x"}); err == nil {
        t.Fatal("expected duplicate-register error")
    }
}

func TestRegistry_NamesSorted(t *testing.T) {
    r := adapter.NewRegistry()
    _ = r.Register(stub{name: "c"})
    _ = r.Register(stub{name: "a"})
    _ = r.Register(stub{name: "b"})
    names := r.Names()
    if names[0] != "a" || names[1] != "b" || names[2] != "c" {
        t.Fatalf("names not sorted: %v", names)
    }
}
```

- [ ] **Step 8.4: Run; verify; commit**

```bash
go test -race ./internal/adapter/...
```

```bash
git add internal/adapter
git commit -m "$(cat <<'EOF'
feat(adapter): interface + in-memory registry

Capability bitmask covers all 7 component types from the spec. Scope is
ScopeUser/ScopeProject. FileOp.Action is "write" or "delete". Skip carries
component+name+reason for the translation report.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 9: `internal/adapter/noop` — NoopAdapter

**Files:**
- Create: `internal/adapter/noop/noop.go`, `internal/adapter/noop/noop_test.go`

Empty adapter for end-to-end testing of the apply pipeline before any real adapter exists.

- [ ] **Step 9.1: Implementation**

`internal/adapter/noop/noop.go`:

```go
// Package noop provides a NoopAdapter that always Detect()s true, Renders no
// FileOps, and Apply()s nothing. Used as a registry placeholder in tests and
// as the default adapter set in M0 before M1+ adds real ones.
package noop

import (
    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

type Adapter struct {
    AdapterName string // overridable for tests
}

func New(name string) *Adapter { return &Adapter{AdapterName: name} }

func (a *Adapter) Name() string                  { return a.AdapterName }
func (a *Adapter) Capabilities() adapter.Capability { return 0 }
func (a *Adapter) Detect() (bool, error)         { return true, nil }
func (a *Adapter) Render(source.Canonical, adapter.Scope, string) ([]adapter.FileOp, []adapter.Skip, error) {
    return nil, nil, nil
}
func (a *Adapter) Ingest(adapter.Scope, string) (source.Canonical, error) {
    return source.Canonical{}, nil
}
func (a *Adapter) Apply([]adapter.FileOp) error { return nil }
```

- [ ] **Step 9.2: Test**

`internal/adapter/noop/noop_test.go`:

```go
package noop_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
)

func TestNoop_Implements(t *testing.T) {
    var _ adapter.Adapter = noop.New("test")
}

func TestNoop_RenderEmpty(t *testing.T) {
    a := noop.New("test")
    ops, skips, err := a.Render(source.CanonicalZero(), adapter.ScopeUser, "")
    _ = ops
    _ = skips
    if err != nil {
        t.Fatal(err)
    }
}
```

(Note: `source.CanonicalZero()` doesn't exist yet; the test as written wouldn't compile. Use `source.Canonical{}` literal directly to avoid adding a helper just for this. Adjust:)

```go
package noop_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
    "github.com/spxrogers/agentsync/internal/source"
)

func TestNoop_Implements(t *testing.T) {
    var _ adapter.Adapter = noop.New("test")
}

func TestNoop_Render(t *testing.T) {
    a := noop.New("test")
    ops, skips, err := a.Render(source.Canonical{}, adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    if len(ops) != 0 || len(skips) != 0 {
        t.Fatalf("noop render returned %d ops, %d skips", len(ops), len(skips))
    }
}
```

- [ ] **Step 9.3: Run; commit**

```bash
go test -race ./internal/adapter/...
```

```bash
git add internal/adapter/noop
git commit -m "$(cat <<'EOF'
feat(adapter/noop): no-op adapter for testing apply pipeline pre-M1

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 10: `internal/render` — apply pipeline

**Files:**
- Create: `internal/render/pipeline.go`, `internal/render/pipeline_test.go`

Orchestrates: load canonical → ask each registered+enabled adapter to Render → collect FileOps + Skips → either `apply` (write) or return for `--dry-run` printing.

- [ ] **Step 10.1: Test (driven by NoopAdapter)**

`internal/render/pipeline_test.go`:

```go
package render_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
    "github.com/spxrogers/agentsync/internal/render"
    "github.com/spxrogers/agentsync/internal/source"
)

func TestPipeline_PlanEmpty(t *testing.T) {
    reg := adapter.NewRegistry()
    _ = reg.Register(noop.New("claude"))
    _ = reg.Register(noop.New("opencode"))

    plan, err := render.Plan(source.Canonical{}, reg, []string{"claude", "opencode"}, adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    if plan.Total() != 0 {
        t.Fatalf("expected empty plan, got %+v", plan)
    }
    if len(plan.PerAgent) != 2 {
        t.Fatalf("expected per-agent entries for both agents")
    }
}

func TestPipeline_UnknownAgentError(t *testing.T) {
    reg := adapter.NewRegistry()
    _ = reg.Register(noop.New("claude"))
    _, err := render.Plan(source.Canonical{}, reg, []string{"missing"}, adapter.ScopeUser, "")
    if err == nil {
        t.Fatal("expected error for unknown adapter")
    }
}
```

- [ ] **Step 10.2: Implementation**

`internal/render/pipeline.go`:

```go
// Package render orchestrates the apply pipeline: canonical model + adapter
// registry -> per-agent FileOps + Skips. apply flag controls whether ops are
// written to disk or returned for inspection (e.g. --dry-run).
package render

import (
    "fmt"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

// Plan holds the result of rendering a canonical model through every selected
// adapter. PerAgent[name] is the per-agent breakdown.
type Plan struct {
    PerAgent map[string]AgentResult
}

type AgentResult struct {
    Ops   []adapter.FileOp
    Skips []adapter.Skip
}

// Total returns the total number of FileOps across all agents.
func (p Plan) Total() int {
    n := 0
    for _, r := range p.PerAgent {
        n += len(r.Ops)
    }
    return n
}

// Plan asks each adapter named in agents to render the canonical model.
// Returns a Plan, never writes anything. Use Apply() to commit.
func Plan(c source.Canonical, reg *adapter.Registry, agents []string, scope adapter.Scope, project string) (Plan, error) {
    out := Plan{PerAgent: map[string]AgentResult{}}
    for _, name := range agents {
        a := reg.Lookup(name)
        if a == nil {
            return out, fmt.Errorf("adapter %q not registered", name)
        }
        ops, skips, err := a.Render(c, scope, project)
        if err != nil {
            return out, fmt.Errorf("render %s: %w", name, err)
        }
        out.PerAgent[name] = AgentResult{Ops: ops, Skips: skips}
    }
    return out, nil
}

// Apply commits a Plan by calling each adapter's Apply with its FileOps. If
// any adapter returns an error, applies completed so far are NOT rolled back
// (each adapter's Apply is itself atomic per-file via iox.AtomicWrite).
func Apply(p Plan, reg *adapter.Registry) error {
    for name, res := range p.PerAgent {
        a := reg.Lookup(name)
        if a == nil {
            return fmt.Errorf("adapter %q not registered at apply", name)
        }
        if err := a.Apply(res.Ops); err != nil {
            return fmt.Errorf("apply %s: %w", name, err)
        }
    }
    return nil
}
```

- [ ] **Step 10.3: Run; commit**

```bash
go test -race ./internal/render/...
```

```bash
git add internal/render
git commit -m "$(cat <<'EOF'
feat(render): apply pipeline orchestrator (Plan + Apply)

Plan() asks each named adapter to Render; Apply() commits all FileOps.
Per-agent results in plan.PerAgent map keyed by adapter name. Used by
cli/apply in Task 17.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 11: `internal/cli` — root command + global flags

**Files:**
- Create: `internal/cli/root.go`, `internal/cli/root_test.go`, `internal/cli/testhelper_test.go`

cobra root with `--verbose`, `--config`, version printing. Subcommands attach via `root.AddCommand()`.

- [ ] **Step 11.1: Add cobra dependency**

```bash
go get github.com/spf13/cobra@latest
```

- [ ] **Step 11.2: Test helpers (used by all cli tests)**

`internal/cli/testhelper_test.go`:

```go
package cli_test

import (
    "bytes"
    "io"
    "strings"
    "testing"

    "github.com/spxrogers/agentsync/internal/cli"
)

// runCLI runs the CLI with given args, returns stdout+stderr combined and
// the resulting error. Sets AGENTSYNC_TARGET_ROOT to the supplied tmp via env.
func runCLI(t *testing.T, env map[string]string, args ...string) (string, error) {
    t.Helper()
    var buf bytes.Buffer
    root := cli.NewRoot()
    root.SetOut(&buf)
    root.SetErr(&buf)
    root.SetArgs(args)
    for k, v := range env {
        t.Setenv(k, v)
    }
    err := root.Execute()
    out, _ := io.ReadAll(&buf)
    return strings.TrimSpace(string(out)), err
}
```

- [ ] **Step 11.3: Test for root**

`internal/cli/root_test.go`:

```go
package cli_test

import (
    "strings"
    "testing"
)

func TestRoot_VersionFlag(t *testing.T) {
    out, err := runCLI(t, nil, "--version")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(out, "agentsync") {
        t.Fatalf("version output missing 'agentsync': %s", out)
    }
}

func TestRoot_HelpListsSubcommands(t *testing.T) {
    out, _ := runCLI(t, nil, "--help")
    for _, sub := range []string{"init", "agent", "doctor", "verify", "apply"} {
        if !strings.Contains(out, sub) {
            t.Fatalf("--help missing subcommand %q. Got: %s", sub, out)
        }
    }
}
```

- [ ] **Step 11.4: Implementation**

`internal/cli/root.go`:

```go
// Package cli wires cobra subcommands. NewRoot returns the root *cobra.Command
// with all subcommands attached.
package cli

import (
    "github.com/spf13/cobra"
)

// version metadata; main.go injects via -ldflags. Tests use the literal
// strings below.
var (
    Version = "dev"
    Commit  = "none"
    Date    = "unknown"
)

// NewRoot constructs the root command tree. Tests build their own root via
// this constructor so flag state is isolated per test.
func NewRoot() *cobra.Command {
    var verbose bool

    cmd := &cobra.Command{
        Use:           "agentsync",
        Short:         "Centrally manage AI coding-agent configurations",
        SilenceUsage:  true,
        SilenceErrors: true,
        Version:       Version,
    }
    cmd.SetVersionTemplate(`{{.Use}} {{.Version}} (commit ` + Commit + `, built ` + Date + `)
`)
    cmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

    cmd.AddCommand(
        newInitCmd(),
        newAgentCmd(),
        newDoctorCmd(),
        newVerifyCmd(),
        newApplyCmd(),
    )
    return cmd
}

// Execute is the main.go entry point.
func Execute() error { return NewRoot().Execute() }
```

(this references `newInitCmd`, `newAgentCmd`, etc.; placeholder forward declarations are added in Tasks 12–17 as each command lands.)

- [ ] **Step 11.5: Stub the command-constructor functions so root.go compiles**

Until each subcommand is fleshed out in Tasks 12–17, add stubs in their respective files. Create `internal/cli/init.go`, `agent.go`, `doctor.go`, `verify.go`, `apply.go` each with a minimal stub:

```go
package cli

import "github.com/spf13/cobra"

func newInitCmd() *cobra.Command {
    return &cobra.Command{Use: "init", Short: "scaffold ~/.agentsync/", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}
```

…and analogous stubs for `newAgentCmd`, `newDoctorCmd`, `newVerifyCmd`, `newApplyCmd`. They'll be replaced in subsequent tasks.

- [ ] **Step 11.6: Wire main.go**

Update `cmd/agentsync/main.go`:

```go
package main

import (
    "fmt"
    "os"

    "github.com/spxrogers/agentsync/internal/cli"
)

var (
    version = "dev"
    commit  = "none"
    date    = "unknown"
)

func main() {
    cli.Version = version
    cli.Commit = commit
    cli.Date = date

    if err := cli.Execute(); err != nil {
        fmt.Fprintln(os.Stderr, "agentsync:", err)
        os.Exit(1)
    }
}
```

- [ ] **Step 11.7: Run; verify; commit**

```bash
go test -race ./internal/cli/...
```

Expected: `--version` test passes, `--help` test passes (subcommand stubs are present).

```bash
git add go.mod go.sum cmd/agentsync internal/cli
git commit -m "$(cat <<'EOF'
feat(cli): cobra root with --verbose; command stubs for init/agent/doctor/verify/apply

Stubs are wired so root.go compiles and --help lists every command. Each
stub is replaced with real behavior in Tasks 12–17.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 12: `agentsync init`

**Files:**
- Modify: `internal/cli/init.go`
- Create: `internal/cli/init_test.go`

Scaffolds `~/.agentsync/` with empty subdirectories and a stub `agentsync.toml`.

- [ ] **Step 12.1: Test**

`internal/cli/init_test.go`:

```go
package cli_test

import (
    "os"
    "path/filepath"
    "testing"
)

func TestInit_FreshScaffold(t *testing.T) {
    tmp := t.TempDir()
    out, err := runCLI(t,
        map[string]string{"AGENTSYNC_TARGET_ROOT": tmp},
        "init")
    if err != nil {
        t.Fatalf("init: %v\n%s", err, out)
    }

    home := filepath.Join(tmp, ".agentsync")
    for _, d := range []string{"mcp", "marketplaces", "plugins", "memory", "memory/fragments", "skills", "secrets", ".state"} {
        if _, err := os.Stat(filepath.Join(home, d)); err != nil {
            t.Fatalf("missing dir %s: %v", d, err)
        }
    }
    if _, err := os.Stat(filepath.Join(home, "agentsync.toml")); err != nil {
        t.Fatalf("missing agentsync.toml: %v", err)
    }
}

func TestInit_RefusesPopulatedHome(t *testing.T) {
    tmp := t.TempDir()
    _ = os.MkdirAll(filepath.Join(tmp, ".agentsync"), 0o755)
    _ = os.WriteFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"), []byte("# already there"), 0o644)

    _, err := runCLI(t,
        map[string]string{"AGENTSYNC_TARGET_ROOT": tmp},
        "init")
    if err == nil {
        t.Fatalf("init should refuse to overwrite a populated home")
    }
}
```

- [ ] **Step 12.2: Implementation**

Replace `internal/cli/init.go`:

```go
package cli

import (
    "fmt"
    "os"
    "path/filepath"

    "github.com/spf13/cobra"
    "github.com/spxrogers/agentsync/internal/paths"
)

const initialOpensyncTOML = `# agentsync source-of-truth config
# See docs/superpowers/specs/2026-05-04-agentsync-design.md for the full schema.

[agents]
# claude   = { enabled = true,  scope = "user" }
# opencode = { enabled = true,  scope = "user" }
# codex    = { enabled = false }   # v1.1
# cursor   = { enabled = false }   # v1.2

[updates]
default_mode     = "track"        # pinned | track | manual
default_interval = "24h"

# [secrets]
# backend       = "age"
# file          = "secrets/secrets.age"
# recipient     = "age1...your-public-key..."
# identity_file = "${env:HOME}/.config/agentsync/age.key"
`

func newInitCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "init",
        Short: "scaffold ~/.agentsync/ with empty subdirectories and stub agentsync.toml",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            home := paths.AgentsyncHome(paths.OSEnv{})
            if entries, _ := os.ReadDir(home); len(entries) > 0 {
                return fmt.Errorf("%s already contains files; refusing to overwrite", home)
            }

            for _, sub := range []string{"mcp", "marketplaces", "plugins", "memory", "memory/fragments", "skills", "secrets", ".state"} {
                if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
                    return fmt.Errorf("mkdir %s: %w", sub, err)
                }
            }
            if err := os.WriteFile(filepath.Join(home, "agentsync.toml"), []byte(initialOpensyncTOML), 0o644); err != nil {
                return fmt.Errorf("write agentsync.toml: %w", err)
            }
            fmt.Fprintln(cmd.OutOrStdout(), "agentsync home initialized at", home)
            return nil
        },
    }
}
```

- [ ] **Step 12.3: Run; commit**

```bash
go test -race ./internal/cli/...
```

```bash
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli/init): scaffold ~/.agentsync/ with empty subdirs + stub config

Refuses to overwrite a populated home so re-init is opt-in via wipe.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: `agentsync agent {add,list,remove}`

**Files:**
- Modify: `internal/cli/agent.go`
- Create: `internal/cli/agent_test.go`

Manipulates the `[agents]` table in `~/.agentsync/agentsync.toml` via TOML AST round-trip (preserves comments + key order).

- [ ] **Step 13.1: Test**

`internal/cli/agent_test.go`:

```go
package cli_test

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestAgent_AddListRemove(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    if _, err := runCLI(t, env, "init"); err != nil {
        t.Fatal(err)
    }
    if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
        t.Fatalf("agent add: %v", err)
    }
    if _, err := runCLI(t, env, "agent", "add", "opencode"); err != nil {
        t.Fatalf("agent add opencode: %v", err)
    }

    listOut, err := runCLI(t, env, "agent", "list")
    if err != nil {
        t.Fatalf("agent list: %v", err)
    }
    if !strings.Contains(listOut, "claude") || !strings.Contains(listOut, "opencode") {
        t.Fatalf("list missing entries: %s", listOut)
    }

    if _, err := runCLI(t, env, "agent", "remove", "opencode"); err != nil {
        t.Fatalf("agent remove: %v", err)
    }
    listOut2, _ := runCLI(t, env, "agent", "list")
    if strings.Contains(listOut2, "opencode") {
        t.Fatalf("list still contains removed agent: %s", listOut2)
    }

    cfg, _ := os.ReadFile(filepath.Join(tmp, ".agentsync", "agentsync.toml"))
    if !strings.Contains(string(cfg), `claude = `) {
        t.Fatalf("config didn't preserve claude line:\n%s", cfg)
    }
}
```

- [ ] **Step 13.2: Implementation**

Replace `internal/cli/agent.go`:

```go
package cli

import (
    "fmt"
    "os"
    "path/filepath"
    "sort"
    "strings"

    "github.com/pelletier/go-toml/v2"
    "github.com/spf13/cobra"
    "github.com/spxrogers/agentsync/internal/iox"
    "github.com/spxrogers/agentsync/internal/paths"
)

const validAgents = "claude, opencode, codex, cursor"

func newAgentCmd() *cobra.Command {
    cmd := &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets"}
    cmd.AddCommand(
        &cobra.Command{Use: "add <name>", Args: cobra.ExactArgs(1), RunE: agentAddRun},
        &cobra.Command{Use: "remove <name>", Args: cobra.ExactArgs(1), RunE: agentRemoveRun},
        &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: agentListRun},
    )
    return cmd
}

type agentsyncCfg struct {
    Agents map[string]map[string]any `toml:"agents"`
    // other top-level keys preserved verbatim via decoder
}

// agentName must be one of the recognized adapter names. M0 only accepts the
// four — adding a new agent in v1.x is a code change, not a config change.
func validateAgent(name string) error {
    switch name {
    case "claude", "opencode", "codex", "cursor":
        return nil
    }
    return fmt.Errorf("unknown agent %q; valid: %s", name, validAgents)
}

// readOpensyncTOML returns the file contents + parsed `agents` section.
func readOpensyncTOML() (string, []byte, map[string]map[string]any, error) {
    home := paths.AgentsyncHome(paths.OSEnv{})
    p := filepath.Join(home, "agentsync.toml")
    raw, err := os.ReadFile(p)
    if err != nil {
        return p, nil, nil, fmt.Errorf("read %s: %w", p, err)
    }
    var cfg agentsyncCfg
    if err := toml.Unmarshal(raw, &cfg); err != nil {
        return p, raw, nil, fmt.Errorf("parse %s: %w", p, err)
    }
    if cfg.Agents == nil {
        cfg.Agents = map[string]map[string]any{}
    }
    return p, raw, cfg.Agents, nil
}

func writeAgents(p string, raw []byte, agents map[string]map[string]any) error {
    // Round-trip: marshal the agents map, splice into raw bytes preserving
    // top-of-file comments. Simpler v1 approach: regenerate the [agents]
    // block; preserve everything before and after via slice.
    var buf strings.Builder
    if err := toml.NewEncoder(&buf).Encode(struct {
        Agents map[string]map[string]any `toml:"agents"`
    }{Agents: agents}); err != nil {
        return fmt.Errorf("encode agents: %w", err)
    }
    newSection := buf.String()
    rawStr := string(raw)
    start := strings.Index(rawStr, "[agents]")
    if start < 0 {
        // no [agents] section yet; append.
        return iox.AtomicWrite(p, []byte(rawStr+"\n"+newSection), 0o644)
    }
    rest := rawStr[start:]
    nextHeader := strings.Index(rest[len("[agents]"):], "\n[")
    var tail string
    if nextHeader >= 0 {
        tail = rest[len("[agents]")+nextHeader:]
    }
    out := rawStr[:start] + newSection + tail
    return iox.AtomicWrite(p, []byte(out), 0o644)
}

func agentAddRun(cmd *cobra.Command, args []string) error {
    name := args[0]
    if err := validateAgent(name); err != nil {
        return err
    }
    p, raw, agents, err := readOpensyncTOML()
    if err != nil {
        return err
    }
    if _, ok := agents[name]; ok {
        fmt.Fprintf(cmd.OutOrStdout(), "agent %s already registered\n", name)
        return nil
    }
    agents[name] = map[string]any{"enabled": true, "scope": "user"}
    if err := writeAgents(p, raw, agents); err != nil {
        return err
    }
    fmt.Fprintf(cmd.OutOrStdout(), "added agent: %s\n", name)
    return nil
}

func agentRemoveRun(cmd *cobra.Command, args []string) error {
    name := args[0]
    p, raw, agents, err := readOpensyncTOML()
    if err != nil {
        return err
    }
    if _, ok := agents[name]; !ok {
        fmt.Fprintf(cmd.OutOrStdout(), "agent %s not registered\n", name)
        return nil
    }
    delete(agents, name)
    if err := writeAgents(p, raw, agents); err != nil {
        return err
    }
    fmt.Fprintf(cmd.OutOrStdout(), "removed agent: %s\n", name)
    return nil
}

func agentListRun(cmd *cobra.Command, _ []string) error {
    _, _, agents, err := readOpensyncTOML()
    if err != nil {
        return err
    }
    names := make([]string, 0, len(agents))
    for n := range agents {
        names = append(names, n)
    }
    sort.Strings(names)
    if len(names) == 0 {
        fmt.Fprintln(cmd.OutOrStdout(), "(no agents registered; try: agentsync agent add claude)")
        return nil
    }
    for _, n := range names {
        v := agents[n]
        enabled, _ := v["enabled"].(bool)
        scope, _ := v["scope"].(string)
        fmt.Fprintf(cmd.OutOrStdout(), "%-10s enabled=%t scope=%s\n", n, enabled, scope)
    }
    return nil
}
```

- [ ] **Step 13.3: Run; commit**

```bash
go test -race ./internal/cli/...
```

```bash
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli/agent): add/list/remove via TOML round-trip in agentsync.toml

[agents] section is regenerated; surrounding comments/sections preserved
by simple slice operations. Comment-preserving AST mutations for
mid-section edits land in M1 where MCP needs them.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 14: `agentsync doctor`

**Files:**
- Modify: `internal/cli/doctor.go`
- Create: `internal/cli/doctor_test.go`

Reports environment + agent installation detection. M0 only checks PATH; richer per-adapter detection lands in M1+.

- [ ] **Step 14.1: Test**

`internal/cli/doctor_test.go`:

```go
package cli_test

import (
    "strings"
    "testing"
)

func TestDoctor_PrintsEnvAndAgents(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")

    out, err := runCLI(t, env, "doctor")
    if err != nil {
        t.Fatalf("doctor: %v\n%s", err, out)
    }
    for _, want := range []string{"AGENTSYNC_HOME", "Go version", "OS", "claude", "opencode"} {
        if !strings.Contains(out, want) {
            t.Fatalf("doctor output missing %q. Got:\n%s", want, out)
        }
    }
}
```

- [ ] **Step 14.2: Implementation**

Replace `internal/cli/doctor.go`:

```go
package cli

import (
    "fmt"
    "os/exec"
    "runtime"

    "github.com/spf13/cobra"
    "github.com/spxrogers/agentsync/internal/paths"
)

func newDoctorCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "doctor",
        Short: "print environment + adapter detection",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            w := cmd.OutOrStdout()
            fmt.Fprintln(w, "agentsync doctor")
            fmt.Fprintln(w, "  AGENTSYNC_HOME:", paths.AgentsyncHome(paths.OSEnv{}))
            fmt.Fprintln(w, "  Go version:    ", runtime.Version())
            fmt.Fprintln(w, "  OS / arch:     ", runtime.GOOS, runtime.GOARCH)
            fmt.Fprintln(w, "")
            fmt.Fprintln(w, "Adapter detection (M0: PATH-only)")
            for _, agent := range []struct {
                name string
                bin  string
            }{
                {"claude", "claude"},
                {"opencode", "opencode"},
                {"codex", "codex"},
                {"cursor", "cursor"},
            } {
                p, err := exec.LookPath(agent.bin)
                if err != nil {
                    fmt.Fprintf(w, "  %-10s not found in PATH\n", agent.name)
                    continue
                }
                fmt.Fprintf(w, "  %-10s %s\n", agent.name, p)
            }
            return nil
        },
    }
}
```

- [ ] **Step 14.3: Run; commit**

```bash
go test -race ./internal/cli/...
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli/doctor): print env + PATH-based agent detection

Richer per-adapter detection (e.g. Claude looks for ~/.claude/) lands in
M1+ as each adapter overrides Detect().

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 15: `agentsync verify`

**Files:**
- Modify: `internal/cli/verify.go`
- Create: `internal/cli/verify_test.go`

Lints `~/.agentsync/`: parses every TOML file, reports schema violations. M0 surface: "all files parse, all `[agents].<name>` are known adapters."

- [ ] **Step 15.1: Test**

`internal/cli/verify_test.go`:

```go
package cli_test

import (
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestVerify_Empty(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")

    out, err := runCLI(t, env, "verify")
    if err != nil {
        t.Fatalf("verify on empty home: %v", err)
    }
    if !strings.Contains(out, "ok") {
        t.Fatalf("verify output missing 'ok': %s", out)
    }
}

func TestVerify_BadTOML(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")

    badPath := filepath.Join(tmp, ".agentsync", "mcp", "broken.toml")
    _ = os.MkdirAll(filepath.Dir(badPath), 0o755)
    _ = os.WriteFile(badPath, []byte("[server\nmissing-bracket"), 0o644)

    _, err := runCLI(t, env, "verify")
    if err == nil {
        t.Fatal("verify should fail on malformed TOML")
    }
}

func TestVerify_UnknownAgent(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    cfg := filepath.Join(tmp, ".agentsync", "agentsync.toml")
    body, _ := os.ReadFile(cfg)
    body = append(body, []byte("\n[agents]\nbogus = { enabled = true }\n")...)
    _ = os.WriteFile(cfg, body, 0o644)

    _, err := runCLI(t, env, "verify")
    if err == nil {
        t.Fatal("verify should reject unknown agent name")
    }
}
```

- [ ] **Step 15.2: Implementation**

Replace `internal/cli/verify.go`:

```go
package cli

import (
    "fmt"

    "github.com/spf13/afero"
    "github.com/spf13/cobra"
    "github.com/spxrogers/agentsync/internal/paths"
    "github.com/spxrogers/agentsync/internal/source"
)

func newVerifyCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "verify",
        Short: "schema-lint ~/.agentsync/ on demand",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            home := paths.AgentsyncHome(paths.OSEnv{})
            c, err := source.Load(afero.NewOsFs(), home)
            if err != nil {
                return fmt.Errorf("verify: %w", err)
            }
            for name := range c.Config.Agents {
                if err := validateAgent(name); err != nil {
                    return fmt.Errorf("agents.%s: %w", name, err)
                }
            }
            fmt.Fprintln(cmd.OutOrStdout(), "ok: schema valid; all referenced agents are recognized adapters")
            return nil
        },
    }
}
```

- [ ] **Step 15.3: Run; commit**

```bash
go test -race ./internal/cli/...
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli/verify): schema-lint ~/.agentsync/ — parse every TOML + validate agents

M0 only checks: every file parses; agent names are known. Richer
validation (MCP env interpolation references resolvable secrets, plugin
versions match marketplace cache, etc.) lands as later milestones add
those features.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 16: `agentsync apply --dry-run`

**Files:**
- Modify: `internal/cli/apply.go`
- Create: `internal/cli/apply_test.go`

Wires source-loader → render.Plan → prints plan. M0 only supports `--dry-run` because no real adapter writes anything yet.

- [ ] **Step 16.1: Test (uses NoopAdapter via a thin registry helper)**

Add `internal/cli/registry_internal.go` exposing a hook for tests:

```go
package cli

import (
    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
)

// registryFactory returns an adapter.Registry. Production wires real
// adapters; tests can override via setRegistryFactory.
var registryFactory = func() *adapter.Registry {
    r := adapter.NewRegistry()
    // M0: only NoopAdapters under each known name
    for _, name := range []string{"claude", "opencode", "codex", "cursor"} {
        _ = r.Register(noop.New(name))
    }
    return r
}
```

`internal/cli/apply_test.go`:

```go
package cli_test

import (
    "strings"
    "testing"
)

func TestApply_DryRunEmptyHome(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    out, err := runCLI(t, env, "apply", "--dry-run")
    if err != nil {
        t.Fatalf("apply --dry-run: %v\n%s", err, out)
    }
    if !strings.Contains(out, "claude") {
        t.Fatalf("dry-run output missing per-agent breakdown: %s", out)
    }
    if !strings.Contains(out, "0 ops") {
        t.Fatalf("dry-run should report 0 ops on empty canonical: %s", out)
    }
}

func TestApply_NoFlagDefaultsToDryRun_M0(t *testing.T) {
    // M0 NEVER writes destinations; flag the user when they call apply
    // without --dry-run so they understand M0 vs M1+ scope.
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    out, err := runCLI(t, env, "apply")
    if err == nil {
        t.Fatalf("apply without --dry-run in M0 should error or warn; got: %s", out)
    }
}
```

- [ ] **Step 16.2: Implementation**

Replace `internal/cli/apply.go`:

```go
package cli

import (
    "fmt"

    "github.com/spf13/afero"
    "github.com/spf13/cobra"
    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/paths"
    "github.com/spxrogers/agentsync/internal/render"
    "github.com/spxrogers/agentsync/internal/source"
)

func newApplyCmd() *cobra.Command {
    var (
        dryRun bool
        scope  string
    )
    cmd := &cobra.Command{
        Use:   "apply",
        Short: "render canonical config and write per agent (M0: --dry-run only)",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            if !dryRun {
                return fmt.Errorf("M0 only supports --dry-run; real adapters arrive in M1 (claude) and M2 (opencode)")
            }
            home := paths.AgentsyncHome(paths.OSEnv{})
            c, err := source.Load(afero.NewOsFs(), home)
            if err != nil {
                return err
            }
            agents := []string{}
            for name, ag := range c.Config.Agents {
                if ag.Enabled {
                    agents = append(agents, name)
                }
            }

            sc := adapter.ScopeUser
            if scope == "project" {
                sc = adapter.ScopeProject
            }

            reg := registryFactory()
            plan, err := render.Plan(c, reg, agents, sc, "")
            if err != nil {
                return err
            }

            w := cmd.OutOrStdout()
            fmt.Fprintf(w, "Plan: %d ops total across %d agent(s)\n", plan.Total(), len(plan.PerAgent))
            for _, name := range reg.Names() {
                res, ok := plan.PerAgent[name]
                if !ok {
                    continue
                }
                fmt.Fprintf(w, "  %-10s %d ops, %d skips\n", name, len(res.Ops), len(res.Skips))
            }
            return nil
        },
    }
    cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute plan without writing destinations")
    cmd.Flags().StringVar(&scope, "scope", "user", "user | project")
    return cmd
}
```

- [ ] **Step 16.3: Run; commit**

```bash
go test -race ./internal/cli/...
git add internal/cli
git commit -m "$(cat <<'EOF'
feat(cli/apply): --dry-run pipeline using NoopAdapters

M0 errors on apply without --dry-run because no real adapter is
registered yet; this fails-loud rather than appearing to succeed. Real
adapters land in M1+ and the error is removed.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 17: Final integration test + CI run

**Files:**
- Create: `internal/cli/integration_test.go`

End-to-end: `init → agent add claude → agent add opencode → verify → apply --dry-run` returns 0 across the board.

- [ ] **Step 17.1: Test**

```go
package cli_test

import (
    "strings"
    "testing"
)

func TestIntegration_M0Lifecycle(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    type step struct {
        args      []string
        wantSubs  []string
        wantError bool
    }
    steps := []step{
        {args: []string{"init"}, wantSubs: []string{"initialized"}},
        {args: []string{"agent", "add", "claude"}, wantSubs: []string{"added agent: claude"}},
        {args: []string{"agent", "add", "opencode"}, wantSubs: []string{"added agent: opencode"}},
        {args: []string{"agent", "list"}, wantSubs: []string{"claude", "opencode"}},
        {args: []string{"verify"}, wantSubs: []string{"ok"}},
        {args: []string{"apply", "--dry-run"}, wantSubs: []string{"Plan", "claude", "opencode"}},
        {args: []string{"agent", "remove", "claude"}, wantSubs: []string{"removed agent: claude"}},
    }
    for _, s := range steps {
        out, err := runCLI(t, env, s.args...)
        if (err != nil) != s.wantError {
            t.Fatalf("%v: err=%v want-err=%v\n%s", s.args, err, s.wantError, out)
        }
        for _, sub := range s.wantSubs {
            if !strings.Contains(out, sub) {
                t.Fatalf("%v: output missing %q. Got:\n%s", s.args, sub, out)
            }
        }
    }
}
```

- [ ] **Step 17.2: Run, lint, push**

```bash
go test -race ./...
golangci-lint run ./...
```

Both must be green.

- [ ] **Step 17.3: Commit + push the branch**

```bash
git add internal/cli/integration_test.go
git commit -m "$(cat <<'EOF'
test(cli): M0 end-to-end lifecycle integration test

init -> agent add x2 -> agent list -> verify -> apply --dry-run -> agent remove
All steps exercise real cobra command path against AGENTSYNC_TARGET_ROOT
tmpdir; no $HOME pollution.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
git push
```

CI on the PR should go green: `test` job on linux/macos/windows, `lint` job, `goreleaser-snapshot` job.

---

## Done When

The engineer can demo end-to-end:

```bash
$ agentsync --version
agentsync v1.0.0-m0 (commit ..., built ...)

$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync init
agentsync home initialized at /tmp/x/.agentsync

$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync agent add claude
added agent: claude
$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync agent add opencode
added agent: opencode
$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync agent list
claude     enabled=true  scope=user
opencode   enabled=true  scope=user

$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync verify
ok: schema valid; all referenced agents are recognized adapters

$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync apply --dry-run
Plan: 0 ops total across 2 agent(s)
  claude     0 ops, 0 skips
  opencode   0 ops, 0 skips

$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync apply
Error: M0 only supports --dry-run; real adapters arrive in M1 (claude) and M2 (opencode)
```

CI green on linux + macos + windows for `test` (race) and `lint`. `goreleaser` snapshot builds successfully.

The skeleton supports M1 (Claude adapter), M2 (OpenCode adapter), M3 (drift), M4 (plugins), M5 (project), M6 (secrets), M7 (polish/release) without architectural changes.
