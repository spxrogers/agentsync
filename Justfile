# agentsync — task runner
#
# Run `just` (no args) to see this list. Run `just <recipe>` to invoke one.
#
# Mirrors the four-tier test contract from the README:
#   test            — unit + integration (fast)
#   test-e2e        — lifecycle binary e2e
#   test-bdd        — Gherkin behaviour lock
#   test-container  — hermetic release gate (podman first, docker fallback)

set shell := ["bash", "-euo", "pipefail", "-c"]
set dotenv-load := false

# Show the recipe list when invoked with no arguments.
default:
    @just --list

# Build the agentsync binary into ./bin/agentsync.
build:
    go build -o bin/agentsync ./cmd/agentsync

# Fast unit + integration tests. Slow suites (e2e, bdd) are opt-in below.
test:
    go test -race ./...

# Lifecycle e2e: exercises the binary end-to-end (build tag `e2e`).
test-e2e:
    go test -tags=e2e -count=1 ./test/e2e/...

# Gherkin BDD suite: the authoritative behaviour lock (build tag `bdd`).
test-bdd:
    go test -tags=bdd -count=1 ./test/bdd/...

# Run every host-side gate in sequence (mirror of the container suite).
test-all: test test-e2e test-bdd

# Release-safe gate: full suite inside a hermetic container. If green, ship.
test-container:
    ./scripts/test-in-container.sh

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

# Full CI gate: lint, every test layer, and the cross-build matrix.
ci: lint test test-e2e test-bdd
    goreleaser release --snapshot --skip=publish --clean

# Remove generated artefacts (binaries, dist, coverage reports).
clean:
    rm -rf bin/ dist/ coverage.out coverage.html
