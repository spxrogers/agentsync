# agentsync — task runner
#
# Run `just` (no args) to see this list. Run `just <recipe>` to invoke one.
#
# Hermeticity contract: every `test*` recipe (except `test-fast`) runs
# inside the container — podman-first, docker fallback. The repo is
# mounted read-only and the network is off, so a misbehaving test can
# never touch your real ~/.claude.json, ~/.config/opencode/, or ~/.agentsync/.

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

# Existing tests already redirect HOME via AGENTSYNC_TARGET_ROOT, so they
# don't touch your real config; the container is still the release gate.
# Iteration only — host-mode unit + integration tests with no container.
test-fast:
    go test -race -count=1 ./...

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
