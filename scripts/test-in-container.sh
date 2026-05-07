#!/usr/bin/env bash
# scripts/test-in-container.sh — run the agentsync test suite hermetically.
#
# Picks podman first (per the design) and falls back to docker. The host
# repo is mounted read-only so a misbehaving test cannot damage the working
# tree. The Go module cache is mounted read-write to a named volume so warm
# runs are fast.
#
# Usage:
#   scripts/test-in-container.sh                # full release-readiness gate
#   scripts/test-in-container.sh shell          # interactive shell in the image
#   scripts/test-in-container.sh -- go test ./internal/cli/...   # raw command
#
# Exit code is the inside-container test exit code. 0 means safe to release.

set -euo pipefail

# Verbose mode for debugging CI breakages; opt-in.
if [[ "${AGENTSYNC_TEST_DEBUG:-}" == "1" ]]; then
    set -x
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE_NAME="agentsync-tests:local"
CACHE_VOL="agentsync-test-gocache"
MOD_VOL="agentsync-test-gomodcache"

# ----- pick a container engine ---------------------------------------------

ENGINE=""
if command -v podman >/dev/null 2>&1; then
    ENGINE="podman"
elif command -v docker >/dev/null 2>&1; then
    ENGINE="docker"
else
    echo "error: neither podman nor docker is on PATH" >&2
    echo "       install podman (preferred) or docker first" >&2
    exit 127
fi

# ----- build (cached on layer hashes) ---------------------------------------

# rootless podman needs the host UID/GID baked in to keep mounted files
# writable; docker we leave alone.
BUILD_ARGS=()
if [[ "$ENGINE" == "podman" ]]; then
    BUILD_ARGS+=(--build-arg "UID=$(id -u)" --build-arg "GID=$(id -g)")
fi

echo "==> building test image with $ENGINE"
"$ENGINE" build \
    "${BUILD_ARGS[@]}" \
    -f "$ROOT/test/container/Containerfile" \
    -t "$IMAGE_NAME" \
    "$ROOT"

# ----- run -----------------------------------------------------------------

# Mount strategy:
#   /workspace      → repo, ro,Z so the container can never mutate the host tree
#   gomodcache vol  → /home/runner/go/pkg/mod, rw, persists across runs
#   gobuild vol     → /home/runner/.cache/go-build, rw, persists across runs
#
# `Z` SELinux relabel is podman-specific; docker ignores it on systems
# without SELinux.

MOUNT_FLAG="ro,Z"
if [[ "$ENGINE" == "docker" ]]; then
    MOUNT_FLAG="ro"
fi

RUN_ARGS=(
    --rm
    --init
    --network=none      # tests must work offline; CI parity guard
    -v "$ROOT:/workspace:$MOUNT_FLAG"
    -v "$MOD_VOL:/home/runner/go/pkg/mod"
    -v "$CACHE_VOL:/home/runner/.cache/go-build"
    -e "GOFLAGS=-mod=mod"
    -e "TZ=UTC"
    # Hermeticity signal honoured by internal/testenv.RequireContainer.
    # FS-touching tests refuse to run unless this is set.
    -e "AGENTSYNC_TEST_IN_CONTAINER=1"
    # Pass the debug flag through to the entrypoint so CI can opt into
    # `set -x` tracing without editing the script.
    -e "AGENTSYNC_TEST_DEBUG=${AGENTSYNC_TEST_DEBUG:-}"
)

# Allow `--network=none` to be relaxed if the user explicitly passed it.
# (Useful when iterating locally; CI must keep network off.)
if [[ "${AGENTSYNC_TEST_ALLOW_NETWORK:-}" == "1" ]]; then
    # remove the --network=none flag
    NEW_ARGS=()
    for a in "${RUN_ARGS[@]}"; do
        [[ "$a" == "--network=none" ]] || NEW_ARGS+=("$a")
    done
    RUN_ARGS=("${NEW_ARGS[@]}")
fi

case "${1:-}" in
    shell)
        exec "$ENGINE" run -it "${RUN_ARGS[@]}" --entrypoint bash "$IMAGE_NAME"
        ;;
    --)
        shift
        exec "$ENGINE" run "${RUN_ARGS[@]}" --entrypoint bash "$IMAGE_NAME" -lc "$*"
        ;;
    "")
        exec "$ENGINE" run "${RUN_ARGS[@]}" "$IMAGE_NAME"
        ;;
    *)
        echo "unknown subcommand: $1" >&2
        echo "usage: $0 [shell|-- <cmd>]" >&2
        exit 64
        ;;
esac
