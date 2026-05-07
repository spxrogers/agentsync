package claude_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: every test in this package
// touches the filesystem (tmp dirs, AGENTSYNC_TARGET_ROOT redirection,
// state files, etc.), so we refuse to run on the host. Use `just test`
// or `just test-release` to invoke through the hermetic container.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}
