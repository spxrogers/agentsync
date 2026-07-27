package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
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

// TestScopeUnawareCommandsActuallyRefuse is the BEHAVIORAL half of F6, and the
// one the other three tests in this file do not provide.
//
// Those three read annotations and flag registration — all static. Delete the
// enforceScopeStance call from NewRoot and every one of them still passes, so
// the headline promise ("a command that cannot honor scope refuses, naming the
// reason, instead of silently ignoring it") had no test that ever EXECUTED it.
// That is not hypothetical: the upgrade-notice change reassigned the root's
// PersistentPreRunE, dropping this enforcement entirely, and CI stayed green.
//
// So: run real commands with real flags and require a real error.
func TestScopeUnawareCommandsActuallyRefuse(t *testing.T) {
	// One representative per shape: a leaf, a group parent, a group child, and
	// the version command (whose stance is the least intuitive).
	paths := [][]string{
		{"doctor"},
		{"version"},
		{"revert"},
		{"plugin"},
		{"plugin", "list"},
		{"marketplace"},
		{"secret"},
	}
	for _, flag := range [][]string{{"--scope", "project"}, {"--project", "/tmp/x"}} {
		for _, path := range paths {
			name := strings.Join(path, " ") + " " + flag[0]
			t.Run(name, func(t *testing.T) {
				root := NewRoot()
				// Confirm the fixture is actually scope-UNAWARE, so this test
				// cannot silently degrade into asserting nothing if a command
				// later becomes scope-aware on purpose.
				target, _, err := root.Find(path)
				if err != nil {
					t.Fatalf("command %v not found: %v", path, err)
				}
				if target.Annotations[scopeStanceAnnotation] != scopeStanceUnaware {
					t.Skipf("%s is no longer scope-unaware; move it out of this table", name)
				}

				root.SetArgs(append(append([]string{}, path...), flag...))
				root.SetOut(io.Discard)
				root.SetErr(io.Discard)
				execErr := root.Execute()
				if execErr == nil {
					t.Fatalf("`agentsync %s %s` was ACCEPTED; a scope-unaware command must refuse "+
						"rather than silently ignore the flag (that is the bug F6 exists to kill)", strings.Join(path, " "), flag[0])
				}
				// The refusal must name the flag and carry the reason, or it
				// reads to the user as a bug rather than a decision.
				msg := execErr.Error()
				if !strings.Contains(msg, flag[0]) {
					t.Errorf("refusal does not name %s: %v", flag[0], execErr)
				}
				if !strings.Contains(msg, "does not take") {
					t.Errorf("refusal is not the stance refusal (some other error fired first): %v", execErr)
				}
				reason := target.Annotations[scopeReasonAnnotation]
				if reason != "" && !strings.Contains(msg, reason) {
					t.Errorf("refusal drops the declared reason %q: %v", reason, execErr)
				}
			})
		}
	}
}

// TestScopeAwareCommandAcceptsTheFlag is the negative control for the test
// above: if enforceScopeStance refused EVERYTHING, the refusal assertions would
// pass for entirely the wrong reason.
func TestScopeAwareCommandAcceptsTheFlag(t *testing.T) {
	root := NewRoot()
	target, _, err := root.Find([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Annotations[scopeStanceAnnotation] != scopeStanceAware {
		t.Fatalf("fixture `status` is not scope-aware (%q)", target.Annotations[scopeStanceAnnotation])
	}
	if err := enforceScopeStance(target); err != nil {
		t.Fatalf("a scope-aware command must not be refused: %v", err)
	}
}

// TestRootDeclaresExactlyOnePersistentPreRun is the house-style source guard for
// the failure mode that produced the regression above.
//
// cobra's PersistentPreRunE is a plain field: a second `cmd.PersistentPreRunE =`
// in NewRoot silently DISCARDS the first. Nothing fails to compile, no test
// breaks, and the dropped hook's behavior just quietly stops happening — which
// is exactly what happened when the upgrade notice was wired in beside the scope
// enforcement. Composition has to be explicit, inside one closure.
func TestRootDeclaresExactlyOnePersistentPreRun(t *testing.T) {
	// Locate root.go relative to THIS file rather than the process cwd: other
	// tests in this package chdir into temp trees, and `go test` shares one
	// process per package.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "root.go"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(src), "cmd.PersistentPreRunE ="); n != 1 {
		t.Fatalf("root.go assigns cmd.PersistentPreRunE %d times; a later assignment SILENTLY DISCARDS the "+
			"earlier hook (this dropped F6's enforceScopeStance once already).\n"+
			"Compose them in ONE closure instead:\n"+
			"    cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {\n"+
			"        maybePrintUpgradeNotice(c)\n"+
			"        return enforceScopeStance(c)\n"+
			"    }", n)
	}
}

// TestEveryGroupParentIsStrict pins strictGroup's coverage.
//
// A noun-group parent with subcommands but no RunE makes cobra return
// flag.ErrHelp BEFORE it validates Args — so `agentsync plugin install x`
// printed the plugin help and exited 0. For a batch of HARD renames with no
// aliases, "the old spelling silently prints help and succeeds" is the worst
// available outcome: a script keeps passing while doing nothing.
//
// strictGroup fixes that by giving each parent a RunE, but nothing checked that
// every group actually calls it. Seven sites rely on it today.
func TestEveryGroupParentIsStrict(t *testing.T) {
	var lax []string
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		if !c.HasSubCommands() {
			return
		}
		if c.RunE == nil && c.Run == nil {
			lax = append(lax, path)
		}
	})
	if len(lax) > 0 {
		t.Fatalf("these command groups have subcommands but no Run/RunE, so cobra returns flag.ErrHelp "+
			"BEFORE validating Args — an unknown subcommand prints help and exits 0:\n  %s\n"+
			"Wrap the parent in strictGroup(cmd).", strings.Join(lax, "\n  "))
	}
}

// TestUnknownSubcommandOfEveryGroupFails is the behavioral half: strictGroup
// being called is one thing, an unknown verb actually failing is another.
func TestUnknownSubcommandOfEveryGroupFails(t *testing.T) {
	var groups []string
	walkCommands(NewRoot(), func(path string, c *cobra.Command) {
		if c.HasSubCommands() {
			groups = append(groups, path)
		}
	})
	if len(groups) == 0 {
		t.Fatal("no command groups found; this guard would vacuously pass")
	}
	for _, g := range groups {
		t.Run(g, func(t *testing.T) {
			root := NewRoot()
			root.SetOut(io.Discard)
			root.SetErr(io.Discard)
			root.SetArgs(append(strings.Fields(g), "definitely-not-a-verb"))
			if err := root.Execute(); err == nil {
				t.Fatalf("`agentsync %s definitely-not-a-verb` exited 0 — an unknown subcommand must be an "+
					"error, or a hard-renamed spelling looks like it still works", g)
			}
		})
	}
}
