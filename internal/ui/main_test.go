package ui

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain scrubs the ambient environment before any test runs. This package is
// pure-unit (host-safe, part of `just test-fast`), so it does not call the
// container guard that scrubs for the FS-touching packages — but its colour
// resolution honours NO_COLOR for ANY value, so an exported NO_COLOR=1 in a
// developer's shell flipped every ColorAuto+terminal assertion here
// (TestColorReportsTheOutStream, TestSpinnerTakesTheErrStreamDecision) without
// a single test asking for it (issue #270). Tests that exercise NO_COLOR set it
// themselves with t.Setenv.
func TestMain(m *testing.M) {
	testenv.ScrubAmbient()
	os.Exit(m.Run())
}
