package generic_test

import (
	"os"
	"testing"

	"github.com/spxrogers/agentsync/internal/testenv"
)

// TestMain enforces the hermeticity contract: tests here touch the filesystem.
func TestMain(m *testing.M) {
	testenv.MustRunInContainer()
	os.Exit(m.Run())
}
