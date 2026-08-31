package state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/state"
)

// TestLoad_RefusesUnreadableKeys pins the fail-closed half of the schema-2
// migration. A v1 key with more than one possible reading must NOT be guessed:
// a wrong project/path split records the entry under the wrong project, which
// is the over-disown bug the typed key exists to remove. A malformed key in a
// file that already claims schema_version 2 means a hand-edit or corruption and
// is refused for the same reason.
func TestLoad_RefusesUnreadableKeys(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantInError string
	}{
		{
			name: "ambiguous legacy files key",
			content: `{"schema_version":1,"files":{` +
				`"claude:project:${HOME}/a:${HOME}/b:${HOME}/c":{"sha256":"x"}}}`,
			wantInError: "claude:project:${HOME}/a:${HOME}/b:${HOME}/c",
		},
		{
			name: "ambiguous legacy keys key",
			content: `{"schema_version":1,"keys":{` +
				`"claude:user::a:/b:/c":{"sha256":"x"}}}`,
			wantInError: "claude:user::a:/b:/c",
		},
		{
			name:        "malformed key in a schema-2 file",
			content:     `{"schema_version":2,"files":{"claude:user::x":{"sha256":"x"}}}`,
			wantInError: "claude:user::x",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "targets.json")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := state.Load(p)
			if err == nil {
				t.Fatal("Load must refuse a key it cannot read unambiguously")
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Fatalf("error must name the offending key %q; got %q", tc.wantInError, err)
			}
			if !strings.Contains(err.Error(), "bookkeeping only") {
				t.Fatalf("error must state the remedy; got %q", err)
			}
		})
	}
}
