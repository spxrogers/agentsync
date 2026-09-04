package adapter_test

import (
	"fmt"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestFileOpEnums_ZeroValues is the one explicit pin of the premise the typed
// FileOp fields rest on: a FileOp built without naming an action writes. The
// intake normalizations ("" → "write") were deleted on the strength of this,
// so the bare `var` declaration — never an explicit constant — is the point of
// each subtest.
func TestFileOpEnums_ZeroValues(t *testing.T) {
	t.Run("zero Action is ActionWrite", func(t *testing.T) {
		var a adapter.Action
		if a != adapter.ActionWrite {
			t.Fatalf("zero adapter.Action = %v, want ActionWrite", a)
		}
	})
}

// TestFileOpEnums_String pins the human surface the dry-run op label and
// DispatchOps' error text depend on: %s and %q both route through Stringer, so
// an out-of-range value reads "action(<n>)" rather than a bare integer.
func TestFileOpEnums_String(t *testing.T) {
	tests := []struct {
		name string
		v    fmt.Stringer
		want string
	}{
		{name: "ActionWrite", v: adapter.ActionWrite, want: "write"},
		{name: "ActionDelete", v: adapter.ActionDelete, want: "delete"},
		{name: "Action(9)", v: adapter.Action(9), want: "action(9)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			if got, want := fmt.Sprintf("%q", tt.v), fmt.Sprintf("%q", tt.want); got != want {
				t.Errorf("%%q = %s, want %s", got, want)
			}
		})
	}
}
