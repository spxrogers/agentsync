package ui

// This package is pure-unit (host-safe, part of `just test-fast`), so it has no
// container guard — but its colour resolution honours NO_COLOR for ANY value,
// and an exported NO_COLOR=1 in a developer's shell flipped every
// ColorAuto+terminal assertion here (TestColorReportsTheOutStream,
// TestSpinnerTakesTheErrStreamDecision) without a single test asking for it
// (issue #270). Importing testenv runs its ambient-environment scrub at init,
// before any test body; tests that exercise NO_COLOR set it themselves with
// t.Setenv.
import _ "github.com/spxrogers/agentsync/internal/testenv"
