package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
)

// newPromptCmd builds a bare command wired to the given stdin so we can exercise
// promptScopeChoice without a real TTY.
func newPromptCmd(stdin string) (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{Use: "apply"}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd, &out
}

func TestPromptScopeChoice(t *testing.T) {
	const root, home = "/repo", "/home/u/.agentsync"
	tests := []struct {
		name      string
		stdin     string
		wantScope adapter.Scope
		wantRoot  string
		wantErr   bool
	}{
		{"picks project", "1\n", adapter.ScopeProject, root, false},
		{"picks user", "2\n", adapter.ScopeUser, "", false},
		{"reprompts then project", "x\n\n1\n", adapter.ScopeProject, root, false},
		{"valid choice at EOF (no newline)", "1", adapter.ScopeProject, root, false},
		{"closed stdin with no choice errors", "", adapter.ScopeUser, "", true},
		{"invalid then EOF errors", "9\n", adapter.ScopeUser, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, _ := newPromptCmd(tt.stdin)
			sc, gotRoot, err := promptScopeChoice(cmd, root, home)
			if tt.wantErr != (err != nil) {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if sc != tt.wantScope || gotRoot != tt.wantRoot {
				t.Fatalf("got (%v, %q), want (%v, %q)", sc, gotRoot, tt.wantScope, tt.wantRoot)
			}
		})
	}
}
