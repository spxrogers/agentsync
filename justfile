# agentsync — task runner
#
# Run `just` (no args) to see this list. Run `just <recipe>` to invoke one.
#
# Hermeticity contract: every `test*` recipe (except `test-fast` and
# `test-live`) runs inside the container — podman-first, docker fallback.
# The repo is mounted read-only and the network is off, so a misbehaving
# test can never touch your real ~/.claude.json, ~/.config/opencode/, or
# ~/.agentsync/. `test-live` is the explicit exception: live tests need
# network access (e.g. cloning github.com/obra/superpowers) and run on
# host with their own permissive TestMain.

set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := false

# Show the recipe list when invoked with no arguments.
default:
    @just --list

# Build the agentsync binary into ./bin/agentsync (host).
build:
    go build -o bin/agentsync ./cmd/agentsync

# Unit + integration tests inside the hermetic container.
test:
    ./scripts/test-in-container.sh -- go test -race -count=1 ./...

# Lifecycle e2e (build tag `e2e`) inside the hermetic container.
test-e2e:
    ./scripts/test-in-container.sh -- go test -tags=e2e -count=1 ./test/e2e/...

# Gherkin BDD suite (build tag `bdd`) inside the hermetic container.
test-bdd:
    ./scripts/test-in-container.sh -- go test -tags=bdd -count=1 ./test/bdd/...

# (entrypoint orchestrates vet → build → race tests → e2e → bdd → smoke)
# Release gate: every layer in one hermetic container run. If green, ship.
test-release:
    ./scripts/test-in-container.sh

# The packages below are the pure-unit set: zero filesystem access.
# Anything that touches the FS (even tmp dirs) is gated behind the
# container — see internal/testenv/container.go.
# Iteration only — pure-unit tests on the host (no container, no FS).
test-fast:
    go test -race -count=1 \
        ./internal/log/... \
        ./internal/jsonkeys/... \
        ./internal/drift/... \
        ./internal/paths/... \
        ./internal/adapter \
        ./internal/adapter/noop/... \
        ./internal/testenv/...

# Live tests fetch real upstream sources (e.g. cloning
# github.com/obra/superpowers via go-git) so they run on host with their
# own permissive TestMain. NOT part of test-release — the release gate
# stays hermetic and offline. Run this manually before any change touching
# internal/marketplace/projection or the source loader's plugin projection
# path, and as a periodic check that upstream plugin shapes haven't drifted.
# Live network-dependent tests (build tag `live`) on host. Opt-in.
test-live:
    AGENTSYNC_LIVE_PLUGIN_TEST=1 go test -tags=live -count=1 -v ./internal/marketplace/...

# Run golangci-lint over every package.
lint:
    golangci-lint run ./...

# Format Go sources with gofmt + gofumpt.
fmt:
    gofmt -w -s .
    go run mvdan.cc/gofumpt@latest -w .

# Run `go mod tidy`.
tidy:
    go mod tidy

# Full CI gate: lint + the hermetic release suite + the cross-build matrix.
ci: lint test-release
    goreleaser release --snapshot --skip=publish --clean

# Remove generated artefacts (binaries, dist, coverage reports).
clean:
    rm -rf bin/ dist/ coverage.out coverage.html
