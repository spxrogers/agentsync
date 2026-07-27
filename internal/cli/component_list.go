package cli

import (
	"fmt"
	"sort"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/ui"
)

// Before #200 F5, `mcp` was the only one of the peer components with a
// management group — so "what skills do I have?" had no answer short of `ls
// ~/.agentsync/skills`, while "what MCP servers do I have?" was a command. The
// asymmetry was not a capability decision: a skill is a DIRECTORY and a hook is
// an event file, so neither is flag-authorable the way `mcp add` is, but both
// are perfectly listable.
//
// So every peer component gets the READ side uniformly. The write side stays
// deliberately absent for the ones that are not flag-shaped: you author a skill
// directory or a subagent markdown file, or capture it with `import`.

// componentLister describes one component's read-side listing.
type componentLister struct {
	// noun is the command name and the group's help noun ("skill", "hook", …).
	noun string
	// plural is used in the empty-state hint.
	plural string
	// authoredBy completes "author one by …" in the empty-state hint.
	authoredBy string
	// list returns the component identities present in a loaded canonical.
	list func(c source.Canonical) []string
}

var componentListers = []componentLister{
	{
		noun: "skill", plural: "skills",
		authoredBy: "creating skills/<name>/SKILL.md (a skill is a DIRECTORY — bundled scripts/, references/ and assets/ ride along), or capture one with `agentsync import <agent>:skill`",
		list: func(c source.Canonical) []string {
			out := make([]string, 0, len(c.Skills))
			for _, s := range c.Skills {
				out = append(out, s.Name)
			}
			return out
		},
	},
	{
		noun: "subagent", plural: "subagents",
		authoredBy: "creating subagents/<name>.md, or capture one with `agentsync import <agent>:subagent`",
		list: func(c source.Canonical) []string {
			out := make([]string, 0, len(c.Subagents))
			for _, s := range c.Subagents {
				out = append(out, s.Name)
			}
			return out
		},
	},
	{
		noun: "command", plural: "slash commands",
		authoredBy: "creating commands/<name>.md, or capture one with `agentsync import <agent>:command`",
		list: func(c source.Canonical) []string {
			out := make([]string, 0, len(c.Commands))
			for _, cm := range c.Commands {
				out = append(out, cm.Name)
			}
			return out
		},
	},
	{
		noun: "hook", plural: "hooks",
		authoredBy: "creating hooks/<event>.toml, or capture one with `agentsync import <agent>:hook`",
		list: func(c source.Canonical) []string {
			seen := map[string]bool{}
			var out []string
			for _, h := range c.Hooks {
				e := h.Event.Unverified()
				if e == "" || seen[e] {
					continue
				}
				seen[e] = true
				out = append(out, e)
			}
			return out
		},
	},
	{
		noun: "lsp", plural: "LSP servers",
		authoredBy: "creating lsp/<id>.toml, or capture one with `agentsync import <agent>:lsp`",
		list: func(c source.Canonical) []string {
			out := make([]string, 0, len(c.LSPServers))
			for _, l := range c.LSPServers {
				out = append(out, l.ID)
			}
			return out
		},
	},
}

// newComponentListCmds builds the per-component groups registered on the root.
func newComponentListCmds() []*cobra.Command {
	out := make([]*cobra.Command, 0, len(componentListers))
	for _, cl := range componentListers {
		out = append(out, newComponentCmd(cl))
	}
	return out
}

func newComponentCmd(cl componentLister) *cobra.Command {
	group := &cobra.Command{
		Use:   cl.noun,
		Short: fmt.Sprintf("inspect %s in the canonical source", cl.plural),
		Long: fmt.Sprintf(`%s groups the read-side commands for %s.

There is no `+"`add`"+` here on purpose: unlike an MCP server, a %s is not
flag-authorable — %s.`, cl.noun, cl.plural, cl.noun, cl.authoredBy),
	}
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   fmt.Sprintf("list %s in the canonical source", cl.plural),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, _, _, err := scopedSourceHome(cmd)
			if err != nil {
				return err
			}
			// LoadTolerant, not Load: `list` is a read-only inventory, and a
			// pending subagent migration should not stop you seeing your skills.
			// The subagent lister is the one that would under-report, and doctor
			// already surfaces that condition loudly.
			c, err := source.LoadTolerant(afero.NewOsFs(), home)
			if err != nil {
				return fmt.Errorf("load source: %w", err)
			}
			names := cl.list(c)
			sort.Strings(names)
			w := cmd.OutOrStdout()
			if len(names) == 0 {
				fmt.Fprintf(w, "(no %s; author one by %s)\n", cl.plural, cl.authoredBy)
				return nil
			}
			for _, n := range names {
				// Names come from directory/file basenames a shared dotfiles repo
				// can carry; sanitize on display like every other listing.
				fmt.Fprintln(w, ui.Sanitize(n))
			}
			return nil
		},
	}
	markScopeAware(list)
	group.AddCommand(list)
	strictGroup(group)
	markScopeAware(group)
	return group
}

// componentListNouns is the set of nouns the component groups occupy, so the
// root registration and any future name check share one list.
func componentListNouns() []string {
	out := make([]string, 0, len(componentListers))
	for _, cl := range componentListers {
		out = append(out, cl.noun)
	}
	sort.Strings(out)
	return out
}
