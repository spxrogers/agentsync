#!/usr/bin/env bash
# Install the packages npm/build.mjs assembled (packed to tarballs, exactly as
# they'd be published) into a throwaway project, then run `agentsync --version`
# through npx, bunx, and bunx --bun, asserting it reports the packages' version.
# Proves the launcher resolves its platform package and runs the binary after a
# real `npm pack` + install, and that the binary's exec bit survives packing
# (asserted directly — the launcher's chmod-retry would otherwise mask its loss).
# It does not exercise os/cpu gating: the matching platform tarball is installed
# explicitly. Bun is required when CI is set, optional locally.
#
#   npm/smoke.sh <out-dir-from-build.mjs>
#
# Only this machine's platform package is installed (from its local tarball).
# npm omits the launcher's other optional dependencies; bun still looks them up
# (404 warnings for an unpublished snapshot version). Either way no lifecycle
# script runs (--ignore-scripts), and those names live in our own npm scope.
# CI runs this against the goreleaser snapshot (ci.yml).
set -euo pipefail

out="${1:?usage: npm/smoke.sh <npm-out-dir>}"
out="$(cd "$out" && pwd)"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
main="agentsync.cc"
version="$(node -p "require('$out/$main/package.json').version")"
# The platform package this machine needs, resolved by the launcher's own logic.
plat_pkg="$(node -p "require('$here/bin/agentsync.js').platformPackage(require('$out/$main/package.json'), process.platform, process.arch) || ''")"
if [[ -z "$plat_pkg" ]]; then
	echo "npm smoke FAIL: $main@$version ships no package for this platform" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/tgz"
tgz=()
for d in "$out/$main" "$out/$plat_pkg"; do
	file="$(npm pack "$d" --pack-destination "$work/tgz" --json --loglevel=error | node -p 'JSON.parse(require("fs").readFileSync(0))[0].filename')"
	tgz+=("$work/tgz/$file")
done

check() {
	local via="$1" got
	shift
	got="$("$@" --version)"
	if [[ "$got" != *" $version "* ]]; then
		echo "npm smoke FAIL ($via): expected version $version, got: $got" >&2
		exit 1
	fi
	echo "npm smoke OK ($via): $got"
}

# Install the two local tarballs: --omit=optional stops npm resolving the
# launcher's other platform packages from the live registry, and
# --ignore-scripts guarantees no lifecycle script from anywhere runs here.
mkdir "$work/npm" && cd "$work/npm"
echo '{"name":"smoke","version":"0.0.0","private":true}' >package.json
npm install --no-audit --no-fund --loglevel=error --ignore-scripts --omit=optional "${tgz[@]}"
bin="node_modules/$plat_pkg/bin/agentsync"
[[ -e "$bin" ]] || bin="$bin.exe"
if [[ ! -x "$bin" ]]; then
	echo "npm smoke FAIL: $bin lost its exec bit in npm pack/install" >&2
	exit 1
fi
check npx npx --no-install agentsync

if ! command -v bun >/dev/null; then
	if [[ -n "${CI:-}" ]]; then
		echo "npm smoke FAIL: bun not on PATH in CI; the bunx legs would be skipped" >&2
		exit 1
	fi
	echo "npm smoke: bun not on PATH, skipping bunx"
else
	mkdir "$work/bun" && cd "$work/bun"
	echo '{"name":"smoke","version":"0.0.0","private":true}' >package.json
	bun add --silent --ignore-scripts --omit optional "${tgz[@]}"
	check bunx bunx agentsync
	check "bunx --bun" bunx --bun agentsync
fi
