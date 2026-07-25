#!/usr/bin/env bash
# reproducibility-diff.sh — re-run the issue #173 two-snapshot reproducibility
# diff against the current commit.
#
# Builds TWO goreleaser snapshot releases from the same working tree (with the
# release-pinned goreleaser, sleeping >1s in between so any wall-clock-derived
# byte would surface), then byte-compares the artifacts:
#
#   * ARCHIVES (tar.gz / zip) — expected byte-identical run-to-run (the
#     archive-entry mtime pins in .goreleaser.yaml, restored in #138; guarded
#     in code by internal/release/reproducibility_test.go). ANY difference
#     here fails the script (exit 1).
#   * KNOWN-NONREPRODUCIBLE artifacts — reported informationally, NEVER fatal:
#       - nfpms deb/rpm: the packages embed the wall-clock BUILD time, not the
#         commit date (deb: the octal mtime of the synthesized ./usr and
#         ./usr/bin directory entries in data.tar; rpm: RPMTAG_BUILDTIME plus
#         the signature digests over it);
#       - checksums.txt: hashes the deb/rpm, so it inherits their instability.
#     See the `checksum` block comment in .goreleaser.yaml (issue #173) for
#     the full attribution. Making the packages reproducible needs an
#     nfpms.mtime pin — a behavioral change that belongs with the
#     archive-pinning work in #138, not here.
#
# Deliberately NOT wired into CI: a job diffing the full artifact set would be
# permanently red on the deb/rpm/checksums.txt half, so that CI job was
# considered and declined in #173. Run this script manually when touching
# .goreleaser.yaml or bumping goreleaser, to re-verify the reproducible half
# still holds.
#
# Usage: scripts/reproducibility-diff.sh [extra goreleaser flags...]
#   e.g. scripts/reproducibility-diff.sh --skip=nfpm   # archives only (skips the
#        deb/rpm packaging, which is informational here anyway). Note `release`
#        has no --single-target (that's `goreleaser build`); the snapshot always
#        builds the full cross-matrix — pure-Go, no extra toolchains needed.
#
# Requires: go (the pinned goreleaser self-bootstraps via `go run`), plus
# network on the first run for the goreleaser module download.
set -euo pipefail

# Keep this pin in lock-step with ci.yml / release.yml / justfile — ci.yml's
# "Guard goreleaser version pin" step asserts the workflow+justfile pins agree
# (issue #138); this script reuses the justfile's `go run pkg@version` pattern.
GORELEASER="github.com/goreleaser/goreleaser/v2@v2.16.0"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

out="$(mktemp -d "${TMPDIR:-/tmp}/agentsync-repro-diff.XXXXXX")"
echo "==> building two snapshots into $out/{a,b}"

build() { # build <dest-dir> [extra flags...] — one snapshot, then move dist/ aside
	local dest="$1"
	shift
	# Skips: publish (no credentials in a local run) and sign (cosign is a
	# release-only dependency installed by release.yml). The snapshot still
	# builds the full cross-matrix archives plus the nfpms deb/rpm.
	go run "$GORELEASER" release --snapshot --clean --skip=publish,sign "$@"
	mv dist "$dest"
}

build "$out/a" "$@"
# Sleep across a second boundary so a wall-clock-derived byte (the very thing
# the archive mtime pins exist to prevent) cannot hide behind two builds
# landing in the same second.
sleep 2
build "$out/b" "$@"

fatal=0
compare() { # compare <class> <basename>; class: archive | informational
	local class="$1" name="$2"
	if [ ! -f "$out/b/$name" ]; then
		echo "MISSING   $name (present in run A, absent in run B)"
		[ "$class" = archive ] && fatal=1
		return 0
	fi
	if cmp -s "$out/a/$name" "$out/b/$name"; then
		echo "identical $name"
	elif [ "$class" = archive ]; then
		echo "DIFFERENT $name  <-- reproducible-archives claim violated"
		fatal=1
	else
		echo "different $name  (known-nonreproducible: wall-clock build time; informational — see header)"
	fi
}

echo
echo "==> archives (tar.gz/zip) — must be byte-identical, or this script fails"
found_archives=0
for f in "$out"/a/*.tar.gz "$out"/a/*.zip; do
	[ -e "$f" ] || continue
	found_archives=1
	compare archive "$(basename "$f")"
done
if [ "$found_archives" -eq 0 ]; then
	echo "ERROR: no tar.gz/zip archives found in $out/a — did the snapshot build run?" >&2
	exit 1
fi
# The loop above walks run A, so an archive present ONLY in run B (a
# nondeterministic artifact set — itself a reproducibility failure) would
# otherwise escape comparison entirely.
for f in "$out"/b/*.tar.gz "$out"/b/*.zip; do
	[ -e "$f" ] || continue
	if [ ! -f "$out/a/$(basename "$f")" ]; then
		echo "MISSING   $(basename "$f") (present in run B, absent in run A)"
		fatal=1
	fi
done

echo
echo "==> known-nonreproducible artifacts (deb/rpm/checksums.txt) — informational only"
for f in "$out"/a/*.deb "$out"/a/*.rpm "$out"/a/checksums.txt; do
	[ -e "$f" ] || continue
	compare informational "$(basename "$f")"
done

echo
if [ "$fatal" -ne 0 ]; then
	echo "FAIL: an archive differs run-to-run — the reproducible-archives claim in .goreleaser.yaml is broken (see internal/release/reproducibility_test.go and the archives block comment)."
	exit 1
fi
echo "OK: all archives byte-identical run-to-run (dirs kept at $out for inspection)."
