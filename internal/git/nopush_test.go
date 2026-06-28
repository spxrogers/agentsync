package git

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoPushSurface guards the load-bearing invariant of issue #118: these repos
// are a LOCAL-ONLY rollback history that is never pushed. agentsync must never add
// a remote or push, and the cheapest way to keep that true is to ensure this
// package never even references the go-git remote/push API. If a future change
// needs one of these tokens for a legitimate, non-pushing reason, narrow the guard
// deliberately — do not just delete it.
func TestNoPushSurface(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{"Push(", "PushContext", "CreateRemote", ".Remote(", "Remotes(", "RemoteAdd"}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, tok := range banned {
			if bytes.Contains(b, []byte(tok)) {
				t.Errorf("%s references %q — internal/git must never push or add a remote (issue #118)", f, tok)
			}
		}
	}
}
