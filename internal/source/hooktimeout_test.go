package source_test

import (
	"math"
	"testing"

	"github.com/spxrogers/agentsync/internal/source"
)

// TestMaxHookTimeout_FitsGeminiMilliseconds pins the reason the cap is what it
// is. Every adapter test uses source.MaxHookTimeout symbolically, so they pass
// for any cap; only this test fails if the cap is raised past the point where
// Gemini's render (seconds × 1000) stops fitting in a 32-bit int and stops
// reading back as the same canonical value. The literal is pinned too because
// CHANGELOG.md and docs/capability-matrix.md quote it (2,147,483 seconds).
func TestMaxHookTimeout_FitsGeminiMilliseconds(t *testing.T) {
	if got := int64(source.MaxHookTimeout) * 1000; got > math.MaxInt32 {
		t.Errorf("MaxHookTimeout×1000 = %d ms, exceeds MaxInt32 (%d); Gemini could not re-ingest its own render", got, int64(math.MaxInt32))
	}
	if source.MaxHookTimeout != 2_147_483 {
		t.Errorf("MaxHookTimeout = %d; the docs quote 2,147,483 seconds — update CHANGELOG.md and docs/capability-matrix.md with the constant", source.MaxHookTimeout)
	}
}
