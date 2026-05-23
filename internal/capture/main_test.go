package capture_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: these tests write to disk
// (temp ~/.agentsync trees), so they run only inside the hermetic container.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}
