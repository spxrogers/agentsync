package continuedev_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/continuedev"
)

// TestIngest_DroppedPromptKeysWarningIsQuoted pins the adapter.QuotedKeys call
// in the dropped-prompt-keys warning (round-5 review: the quoting switch was
// otherwise unpinned — a revert to a raw strings.Join stayed green, and
// keys_test.go only proves the helper escapes, not that this site calls it).
func TestIngest_DroppedPromptKeysWarningIsQuoted(t *testing.T) {
	tmp := t.TempDir()
	promptsDir := filepath.Join(tmp, ".continue", "prompts")
	if err := os.MkdirAll(promptsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	native := "---\nname: review\ndescription: Review code\ninvokable: true\ntemperature: 0.2\n---\nReview it.\n"
	if err := os.WriteFile(filepath.Join(promptsDir, "review.md"), []byte(native), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn bytes.Buffer
	a := continuedev.New(continuedev.Options{TargetRoot: tmp, Stderr: &warn})
	if _, err := a.Ingest(adapter.ScopeUser, ""); err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if !strings.Contains(warn.String(), `dropped on import: "temperature"`) {
		t.Fatalf("dropped-keys warning must quote the key names, got:\n%s", warn.String())
	}
}
