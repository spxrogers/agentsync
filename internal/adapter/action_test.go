package adapter_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/spxrogers/agentsync/internal/adapter"
)

// TestFileOpEnums_ZeroValues is the one explicit pin of the premise the typed
// FileOp fields rest on: a FileOp built without naming an action writes, and
// one built without naming a kind is an ordinary render. The intake
// normalizations ("" → "write") were deleted on the strength of this, so the
// bare `var` declaration — never an explicit constant — is the point of each
// subtest.
func TestFileOpEnums_ZeroValues(t *testing.T) {
	t.Run("zero Action is ActionWrite", func(t *testing.T) {
		var a adapter.Action
		if a != adapter.ActionWrite {
			t.Fatalf("zero adapter.Action = %v, want ActionWrite", a)
		}
	})
	t.Run("zero OpKind is OpRender", func(t *testing.T) {
		var k adapter.OpKind
		if k != adapter.OpRender {
			t.Fatalf("zero adapter.OpKind = %v, want OpRender", k)
		}
	})
}

// TestFileOpEnums_String pins the human surface the dry-run op label and
// DispatchOps' error text depend on: %s and %q both route through Stringer, so
// an out-of-range value reads "action(<n>)" / "opkind(<n>)" rather than a bare
// integer.
func TestFileOpEnums_String(t *testing.T) {
	tests := []struct {
		name string
		v    fmt.Stringer
		want string
	}{
		{name: "ActionWrite", v: adapter.ActionWrite, want: "write"},
		{name: "ActionDelete", v: adapter.ActionDelete, want: "delete"},
		{name: "Action(9)", v: adapter.Action(9), want: "action(9)"},
		{name: "OpRender", v: adapter.OpRender, want: "render"},
		{name: "OpCleanup", v: adapter.OpCleanup, want: "cleanup"},
		{name: "OpKind(9)", v: adapter.OpKind(9), want: "opkind(9)"},
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

// TestCleanupOp pins every field the single cleanup-op constructor sets. It is
// the ONLY producer of OpCleanup — render.orphanCleanupOps and `agent disable
// --purge` both call it — so a field it drops is dropped at every synthesis
// site at once, and a missing Kind stamp here relabels every key removal as a
// write in `apply`.
func TestCleanupOp(t *testing.T) {
	owned := []string{"/mcpServers/a", "/mcpServers/b"}
	op := adapter.CleanupOp("/home/u/.claude.json", "merge-json-keys", owned)
	tests := []struct {
		name      string
		got, want any
	}{
		{name: "Action is ActionWrite", got: op.Action, want: adapter.ActionWrite},
		{name: "Kind is OpCleanup", got: op.Kind, want: adapter.OpCleanup},
		{name: "Path", got: op.Path, want: "/home/u/.claude.json"},
		{name: "Content is the empty object", got: string(op.Content), want: "{}"},
		{name: "Mode", got: op.Mode, want: uint32(0o644)},
		{name: "MergeStrategy", got: op.MergeStrategy, want: "merge-json-keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %v, want %v", tt.got, tt.want)
			}
		})
	}
	t.Run("OwnedKeys are the input pointers", func(t *testing.T) {
		if !slices.Equal(op.OwnedKeys, owned) {
			t.Errorf("OwnedKeys = %v, want %v", op.OwnedKeys, owned)
		}
	})
}
