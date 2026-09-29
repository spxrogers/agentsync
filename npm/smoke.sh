#!/usr/bin/env bash
# Install the packages npm/build.mjs assembled (packed to tarballs, exactly as
# they'd be published) into a throwaway project, then run `agentsync --version`
# through npx and — when bun is on PATH — bunx, asserting it reports the
# packages' version. Proves the launcher, the platform package's os/cpu gating,
# and the binary's exec bit survive a real `npm pack` + install.
#
#   npm/smoke.sh <out-dir-from-build.mjs>
#
# Only this machine's platform package is installed; the others are optional
# dependencies at a version that isn't on the registry, which npm and bun both
# skip. CI runs this against the goreleaser snapshot (ci.yml).
set -euo pipefail

out="${1:?usage: npm/smoke.sh <npm-out-dir>}"
out="$(cd "$out" && pwd)"
main="agentsync.cc"
version="$(node -p "require('$out/$main/package.json').version")"
plat="$(node -p 'process.platform + "-" + process.arch')"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/tgz"
for d in "$out/$main" "$out/$main-$plat"; do
	npm pack "$d" --pack-destination "$work/tgz" --loglevel=error >/dev/null
done
tgz=("$work/tgz/$main-$version.tgz" "$work/tgz/$main-$plat-$version.tgz")

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

mkdir "$work/npm" && cd "$work/npm"
echo '{"name":"smoke","version":"0.0.0","private":true}' >package.json
npm install --no-audit --no-fund --loglevel=error "${tgz[@]}"
check npx npx --no-install agentsync

if command -v bun >/dev/null; then
	mkdir "$work/bun" && cd "$work/bun"
	echo '{"name":"smoke","version":"0.0.0","private":true}' >package.json
	bun add --silent "${tgz[@]}"
	check bunx bunx agentsync
	check "bunx --bun" bunx --bun agentsync
fi
