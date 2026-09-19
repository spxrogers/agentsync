package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/testenv"
)

// entrypointPath is resolved at package init, while the working directory is
// still this package's directory: TestMain then chdirs to a neutral dir (see
// main_test.go), after which a relative path to the repo would not resolve.
var entrypointPath = func() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Join(wd, "..", "..", "test", "container", "entrypoint.sh")
}()

// TestConfiguredLegFakesEveryAgentBinary is the parity guard between the agent
// binaries agentsync probes on PATH (deepAgentBinaries + every generic Spec's
// DetectBin) and the stubs the configured-environment container leg puts on
// PATH (test/container/entrypoint.sh). PATH is the one ambient input the
// harness cannot scrub, so that leg is how CI catches a test that passes only
// because an agent binary happens to be absent (issue #270). A probed binary
// missing from the leg is a binary whose presence CI never exercises.
func TestConfiguredLegFakesEveryAgentBinary(t *testing.T) {
	testenv.RequireContainer(t)
	if entrypointPath == "" {
		t.Fatal("could not resolve the package directory at init")
	}
	src, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatal(err)
	}
	// The stub list is the `for bin in … ; do` word list, possibly continued
	// over lines with backslashes.
	m := regexp.MustCompile(`(?s)for bin in (.*?); do`).FindSubmatch(src)
	if m == nil {
		t.Fatal("entrypoint.sh: could not find the `for bin in …; do` stub list")
	}
	faked := map[string]bool{}
	for _, w := range strings.Fields(strings.ReplaceAll(string(m[1]), "\\", " ")) {
		faked[w] = true
	}
	want := map[string]bool{}
	for _, b := range deepAgentBinaries {
		want[b] = true
	}
	for _, s := range generic.Specs() {
		if s.DetectBin != "" {
			want[s.DetectBin] = true
		}
	}
	if len(want) < 10 {
		t.Fatalf("only %d probed binaries found — the sources of truth moved", len(want))
	}
	for b := range want {
		if !faked[b] {
			t.Errorf("agentsync probes %q on PATH but the configured leg in test/container/entrypoint.sh does not fake it", b)
		}
	}
	for b := range faked {
		if !want[b] {
			t.Errorf("entrypoint.sh fakes %q, which no adapter probes any more — remove it", b)
		}
	}
}
