package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// walkCommands visits every runnable command in the tree, depth-first, with its
// full path.
func walkCommands(root *cobra.Command, fn func(path string, c *cobra.Command)) {
	var walk func(c *cobra.Command, path string)
	walk = func(c *cobra.Command, path string) {
		for _, sub := range c.Commands() {
			p := strings.TrimSpace(path + " " + sub.Name())
			fn(p, sub)
			walk(sub, p)
		}
	}
	walk(root, "")
}

// TestEveryCommandDeclaresScopeStance is the guard behind #200 F6. Now that
// --scope/--project are inherited by EVERY command, a command that neither
// consumes them (markScopeAware) nor refuses them (markScopeUnaware) would
// silently accept and ignore the user's flag — the exact bug F6 exists to kill,
// just relocated. Every command must state which it is.
//
// A new command therefore fails this test until its author decides. That is the
// point: "does this operate on a scoped source tree?" is a real design question,
// not a detail to leave implicit.
func TestEveryCommandDeclaresScopeStance(t *testing.T) {
	var undeclared []string
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		switch c.Annotations[scopeStanceAnnotation] {
		case scopeStanceAware, scopeStanceUnaware:
			return
		}
		undeclared = append(undeclared, path)
	})
	if len(undeclared) > 0 {
		t.Fatalf("these commands declare no --scope/--project stance, so the flags would be silently ignored:\n  %s\n"+
			"Call markScopeAware(cmd) if the command honors scope, or markScopeUnaware(cmd, reason) if it cannot.",
			strings.Join(undeclared, "\n  "))
	}
}

// TestScopeUnawareCommandsExplainThemselves pins the other half of F6's
// proposal — "commands that can't honor them reject explicitly WITH A REASON".
// A bare refusal would leave the user guessing whether it's a bug.
func TestScopeUnawareCommandsExplainThemselves(t *testing.T) {
	var bare []string
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		if c.Annotations[scopeStanceAnnotation] != scopeStanceUnaware {
			return
		}
		if strings.TrimSpace(c.Annotations[scopeReasonAnnotation]) == "" {
			bare = append(bare, path)
		}
	})
	if len(bare) > 0 {
		t.Fatalf("these scope-unaware commands give no reason, so their refusal reads as a bug:\n  %s",
			strings.Join(bare, "\n  "))
	}
}

// TestScopeFlagsAreDeclaredOnceOnTheRoot is the anti-regression for the change
// itself: a subcommand re-declaring a local --scope/--project would SHADOW the
// inherited one, and its stance annotation would then govern a flag the root
// never sees.
func TestScopeFlagsAreDeclaredOnceOnTheRoot(t *testing.T) {
	root := NewRoot()
	for _, name := range []string{"scope", "project"} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Fatalf("root does not declare a persistent --%s", name)
		}
	}
	var shadowed []string
	walkCommands(root, func(path string, c *cobra.Command) {
		for _, name := range []string{"scope", "project"} {
			if c.LocalFlags().Lookup(name) != nil && c.Flags().Lookup(name) != c.InheritedFlags().Lookup(name) {
				if c.LocalNonPersistentFlags().Lookup(name) != nil {
					shadowed = append(shadowed, path+" --"+name)
				}
			}
		}
	})
	if len(shadowed) > 0 {
		t.Fatalf("these commands re-declare a scope flag locally, shadowing the root's:\n  %s",
			strings.Join(shadowed, "\n  "))
	}
}
