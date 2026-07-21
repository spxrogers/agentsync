// Package release holds guard tests that keep the release configuration
// (.goreleaser.yaml) honest — asserting the config still backs the
// reproducibility claims its comments make, so a future edit that strips the
// backing without also correcting the prose fails CI. It has no runtime code;
// see reproducibility_test.go.
//
// Context: issue #173 empirically verified (by diffing two snapshot releases
// built from the same commit) that the binaries and the tar.gz/zip archives are
// byte-for-byte reproducible, while the nfpms deb/rpm — and therefore
// checksums.txt — are not (the packages embed the wall-clock build time). This
// package guards the reproducible half in code, per the CLAUDE.md rule that
// "doc claims are not self-certifying."
package release
