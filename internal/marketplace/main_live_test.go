//go:build live

package marketplace_test

import (
	"os"
	"testing"
)

// TestMain for the live build tag. Live tests make real network calls and
// are intended to run on the host (not in the hermetic container). They are
// opt-in via AGENTSYNC_LIVE_PLUGIN_TEST=1 and the -tags=live build flag.
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
