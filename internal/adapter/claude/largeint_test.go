package claude_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
)

// TestApply_PreservesForeignLargeInt is the regression for key-merge silently
// rounding a foreign integer larger than 2^53: agentsync promises to preserve
// foreign keys verbatim, but the merge unmarshalled the existing dest into
// map[string]any with encoding/json's float64 default, so a user's
// 1234567890123456789 became 1234567890123456800 on the next apply.
func TestApply_PreservesForeignLargeInt(t *testing.T) {
	tmp := t.TempDir()
	dest := filepath.Join(tmp, ".claude.json")
	if err := os.WriteFile(dest, []byte(`{"foreignBig":1234567890123456789}`), 0o644); err != nil {
		t.Fatal(err)
	}
	op := adapter.FileOp{
		Action:        "write",
		Path:          dest,
		MergeStrategy: "merge-json-keys",
		Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
	}
	a := claude.New(claude.Options{TargetRoot: tmp})
	if err := a.Apply([]adapter.FileOp{op}, adapter.PassThroughWriter{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if !strings.Contains(string(got), "1234567890123456789") {
		t.Fatalf("foreign large int was rounded during merge:\n%s", got)
	}
}
