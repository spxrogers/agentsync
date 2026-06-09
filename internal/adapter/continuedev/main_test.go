package continuedev_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: every test in this package touches
// the filesystem, so we refuse to run on the host. Use `just test`.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}
