package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// The `--scope` / `--project` pair is declared ONCE, on the root, and inherited
// by every command (#200 F6). Before this, seven of seventeen commands declared
// their own copy and the rest silently ignored the flags — so `agentsync mcp add
// … --scope project` looked accepted and wrote to the user tree anyway, even
// though project-scope `mcp/*.toml` is a documented layout.
//
// Inheriting the flags everywhere creates the opposite hazard: a command that
// cannot honor scope would now ACCEPT the flag and ignore it. So every command
// must declare which it is — `markScopeAware` or `markScopeUnaware(reason)` —
// and the root's pre-run refuses the flags on an unaware command, naming the
// reason. A command that declares NEITHER also refuses (fail-closed), and
// TestEveryCommandDeclaresScopeStance fails the build for it.
const (
	scopeStanceAnnotation = "agentsync.scope.stance"
	scopeReasonAnnotation = "agentsync.scope.reason"

	scopeStanceAware   = "aware"
	scopeStanceUnaware = "unaware"
)

// markScopeAware records that a command consumes the inherited --scope/--project.
func markScopeAware(cmd *cobra.Command) {
	if cmd == nil {
		return
	}
	annotate(cmd, scopeStanceAnnotation, scopeStanceAware)
}

// markScopeUnaware records that a command cannot honor --scope/--project, with
// the reason a user sees when they pass one. Write the reason as a clause that
// completes "… does not take --scope/--project: <reason>".
func markScopeUnaware(cmd *cobra.Command, reason string) {
	annotate(cmd, scopeStanceAnnotation, scopeStanceUnaware)
	annotate(cmd, scopeReasonAnnotation, reason)
}

func annotate(cmd *cobra.Command, key, value string) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[key] = value
}

// markGroupScopeUnaware applies the same reason to a parent and every one of its
// subcommands, for a noun group that is wholly user-scope (plugin, marketplace,
// secret). Subcommands that DO honor scope re-mark themselves afterwards.
func markGroupScopeUnaware(cmd *cobra.Command, reason string) {
	markScopeUnaware(cmd, reason)
	for _, sub := range cmd.Commands() {
		markGroupScopeUnaware(sub, reason)
	}
}

// addScopeFlags marks a command as scope-aware. It no longer registers flags —
// the root owns them — but every previously-annotated call site keeps working,
// so the pairing of "declares the flags" and "consumes the flags" stays in one
// place per command.
func addScopeFlags(cmd *cobra.Command, _, _ *string) { markScopeAware(cmd) }

// scopeFlagValues reads the inherited --scope/--project off a command. They are
// persistent root flags, so cmd.Flags() resolves them through the inherited set.
func scopeFlagValues(cmd *cobra.Command) (scope, project string) {
	return inheritedString(cmd, "scope"), inheritedString(cmd, "project")
}

func inheritedString(cmd *cobra.Command, name string) string {
	if v, err := cmd.Flags().GetString(name); err == nil {
		return v
	}
	if f := cmd.InheritedFlags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

// scopeFlagsGiven reports which of the two flags the user actually set.
func scopeFlagsGiven(cmd *cobra.Command) []string {
	var used []string
	for _, name := range []string{"scope", "project"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			used = append(used, "--"+name)
		}
	}
	return used
}

// enforceScopeStance is the root pre-run check. It refuses --scope/--project on
// a command that cannot honor them, rather than accepting and ignoring them.
func enforceScopeStance(cmd *cobra.Command) error {
	used := scopeFlagsGiven(cmd)
	if len(used) == 0 {
		return nil
	}
	if cmd.Annotations[scopeStanceAnnotation] == scopeStanceAware {
		return nil
	}
	reason := cmd.Annotations[scopeReasonAnnotation]
	if reason == "" {
		// Unreachable while TestEveryCommandDeclaresScopeStance passes; refusing
		// is the fail-closed default for a command that forgot to declare.
		reason = "this command does not operate on a scoped source tree"
	}
	return fmt.Errorf("`%s` does not take %s: %s",
		cmd.CommandPath(), strings.Join(used, "/"), reason)
}

// strictGroup makes a noun-group parent REJECT an unknown subcommand instead of
// silently printing help and exiting 0.
//
// Cobra returns flag.ErrHelp for a non-runnable command BEFORE it validates
// args, so `Args: cobra.NoArgs` on a bare parent never fires — `agentsync
// plugin install x` printed the plugin help and exited 0. That makes a hard
// rename (#200 F4/F8 ship several) look like it still works, which is the worst
// possible outcome for a deprecation with no alias. Giving the parent a RunE
// makes it runnable, so NoArgs is enforced and a bare invocation still prints
// help.
func strictGroup(cmd *cobra.Command) *cobra.Command {
	cmd.Args = cobra.NoArgs
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	return cmd
}
