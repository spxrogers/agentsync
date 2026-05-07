#!/usr/bin/env bash
# Inside-container test orchestrator. The host runner script mounts the repo
# read-only at /workspace and invokes this. Every gate listed below must pass
# for the engineer to release; any failure aborts immediately.
#
# Hermeticity contract:
#   - The repo is mounted read-only; tests cannot write into the source tree.
#   - $HOME and $XDG_* default to the runner user inside the container — they
#     are NOT the engineer's host paths.
#   - When the runner script invokes us with --network=none (default), no
#     gate is permitted to touch the network. Module deps were pre-warmed at
#     image build time.
set -euo pipefail

# Optional verbose mode for debugging CI breakages.
if [[ "${AGENTSYNC_TEST_DEBUG:-}" == "1" ]]; then
    set -x
fi

cd /workspace

# Surface basic diagnostics up front. Any of these lines failing is itself
# a useful signal — they're cheap and help triage failures from CI logs.
echo "==> diagnostics"
echo "    pwd:       $(pwd)"
echo "    user:      $(id)"
echo "    cgroup:    $(awk '/docker|podman|kube|libpod|containerd/{print; exit}' /proc/1/cgroup 2>/dev/null || echo '(none)')"
echo "    /workspace ls:"
ls -la | head -10
echo "    go.mod first 4 lines:"
head -4 go.mod || true

# Hermeticity signal honoured by internal/testenv.RequireContainer. Tests
# that touch the filesystem refuse to run unless this is exported.
export AGENTSYNC_TEST_IN_CONTAINER=1

step() {
    printf '\n\033[1;36m==> %s\033[0m\n' "$1"
}

# We can't run `go mod tidy` against a read-only tree, but we can verify the
# build & tests under -mod=mod against the pre-warmed module cache.
export GOFLAGS="${GOFLAGS:--mod=mod}"

step "go env"
go version
go env GOMOD GOMODCACHE GOCACHE GOFLAGS

step "go vet ./..."
go vet ./...

step "go build ./..."
go build ./...

step "go test -race ./... (unit + integration, 16 packages)"
go test -race -count=1 ./...

step "go test -tags=e2e ./test/e2e/... (lifecycle e2e)"
go test -tags=e2e -count=1 ./test/e2e/...

step "go test -tags=bdd ./test/bdd/... (Gherkin BDD suite)"
go test -tags=bdd -count=1 ./test/bdd/...

step "agentsync smoke (--version, --help)"
go run ./cmd/agentsync --version
go run ./cmd/agentsync --help >/dev/null

# Optional gates: lint and the cross-build matrix run inside the container
# only when the tools are present. They are CI gates separately; here they
# are a bonus when an engineer happens to have installed them locally.
if command -v golangci-lint >/dev/null 2>&1 && [[ "${AGENTSYNC_TEST_SKIP_LINT:-}" != "1" ]]; then
    step "golangci-lint run ./... (optional gate, present on PATH)"
    golangci-lint run ./...
fi

if command -v goreleaser >/dev/null 2>&1 && [[ "${AGENTSYNC_TEST_SKIP_GORELEASER:-}" != "1" ]]; then
    step "goreleaser release --snapshot --skip publish --clean (optional gate)"
    SCRATCH="$(mktemp -d)"
    cp -r /workspace/. "$SCRATCH"/
    cd "$SCRATCH"
    goreleaser release --snapshot --skip=publish --clean
    cd /workspace
fi

printf '\n\033[1;32m==> all gates green — release-safe\033[0m\n'
