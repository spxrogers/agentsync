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
build_image() {
    local engine="$1"
    local build_args=()
    if [[ "$engine" == "podman" ]]; then
        build_args+=(--build-arg "UID=$(id -u)" --build-arg "GID=$(id -g)")
    fi
    "$engine" build \
        "${build_args[@]}" \
        -f "$ROOT/test/container/Containerfile" \
        -t "$IMAGE_NAME" \
        "$ROOT"
}

echo "==> building test image with $ENGINE"
if ! build_image "$ENGINE"; then
    # A podman *build* failure (as opposed to podman being absent) is usually
    # the host's rootless podman/crun toolchain, not our Containerfile — seen
    # on CI when a hosted-runner image update ships a broken podman/crun
    # pairing (e.g. crun erroring "unknown version specified" on a plain
    # groupadd/useradd RUN step). Docker ships on every GH-hosted Ubuntu
    # runner, so fall back to it rather than failing release-gate CI on an
    # infra regression neither engine choice nor our image caused.
    if [[ "$ENGINE" == "podman" ]] && command -v docker >/dev/null 2>&1; then
        echo "==> podman build failed; falling back to docker" >&2
        ENGINE="docker"
        build_image "$ENGINE"
    else
        exit 1
    fi
fi

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
    # No named cache volumes: the image already ships a pre-warmed module
    # cache at /home/runner/go/pkg/mod, and overlayfs CoW makes the cache
    # writable for runtime additions. Volumes only matter for persisting
    # state across runs, which we don't need (CI is one-shot, local dev
    # gets fast warm runs from the docker image-layer cache).
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
