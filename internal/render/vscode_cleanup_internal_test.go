package render

import (
	"path/filepath"
	"testing"
)

func TestConditionalInstructionOrphanKinds(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "conditional instruction", id: "memory/fragments/python.instructions.md", want: true},
		{name: "nested conditional instruction", id: "memory/fragments/nested/python.instructions.md", want: true},
		{name: "ordinary memory fragment", id: "memory/fragments/shared.md"},
		{name: "global memory", id: "memory/AGENTS.md"},
		{name: "other instruction source", id: "other/python.instructions.md"},
		{name: "existing command", id: "commands/verify.md", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, id := range []string{tc.id, filepath.FromSlash(tc.id)} {
				if got := isOrphanReclaimable(id); got != tc.want {
					t.Fatalf("isOrphanReclaimable(%q) = %v, want %v", id, got, tc.want)
				}
			}
		})
	}
}
