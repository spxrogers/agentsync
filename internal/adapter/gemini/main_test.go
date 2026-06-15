package gemini_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: every test in this package touches
// the filesystem (tmp dirs, AGENTSYNC_TARGET_ROOT redirection, etc.), so we
// refuse to run on the host. Use `just test` or `just test-release`.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}
