package cli

import (
	"fmt"
	"strings"

	"github.com/spxrogers/agentsync/internal/ui"
)

// printExplain renders the provenance model human-shaped. It is the same data
// the --json payload carries, with the `path#pointer` labels `diff` prints — and
// like the payload it emits METADATA ONLY: no destination content, no key
// values, no drift snippets.
func printExplain(p *ui.Printer, m explainModel) {
	label := ui.Sanitize(m.Path)
	if m.Pointer != "" {
		label += "#" + ui.Sanitize(m.Pointer)
	}
	scope := m.Scope
	if m.Project != "" {
		scope += " (" + ui.Sanitize(m.Project) + ")"
	}
	fmt.Fprintf(p.Out, "%s %s\n", p.Bold("agentsync explain"), p.Faint("— "+label))
	fmt.Fprintf(p.Out, "  %s %s\n", p.Faint("scope:"), scope)
	fmt.Fprintln(p.Out, "")

	for i, owner := range m.Owners {
		if i > 0 {
			fmt.Fprintln(p.Out, "")
		}
		fmt.Fprintf(p.Out, "%s %s\n", p.Bold(p.Cyan("▸")+" "+owner.Agent),
			p.Faint(fmt.Sprintf("(%s)", pluralize(len(owner.Items), "item"))))
		for _, item := range owner.Items {
			printExplainItem(p, item)
		}
	}

	for _, cf := range m.Conflicts {
		fmt.Fprintln(p.Out, "")
		fmt.Fprintf(p.Out, "%s %s\n", p.Yellow(ui.GlyphWarn+" cross-agent:"),
			p.Yellow(strings.Join(cf.Agents, ", ")))
		fmt.Fprintf(p.Out, "    %s\n", p.Faint(cf.Reason))
	}

	fmt.Fprintln(p.Out, "")
	fmt.Fprintf(p.Out, "%s %s\n", p.Faint(ui.GlyphArrow),
		p.Faint("metadata only — for the content itself, run `agentsync diff "+ui.Sanitize(m.Path)+"`"))
}

func printExplainItem(p *ui.Printer, item explainItem) {
	head := item.Pointer
	if head == "" {
		head = "(whole file)"
	}
	fmt.Fprintf(p.Out, "  %s %s  %s\n",
		p.Faint(ui.GlyphArrow),
		p.Bold(ui.Sanitize(head)),
		ownershipStyled(p, item.Ownership))

	if item.Component != "" {
		what := item.Component
		if item.Name != "" {
			what += " " + item.Name
		}
		fmt.Fprintf(p.Out, "      %s %s\n", p.Faint("component:"), ui.Sanitize(what))
	}
	if item.Source != nil {
		fmt.Fprintf(p.Out, "      %s %s\n", p.Faint("source:   "), explainSourceLine(p, *item.Source))
		for _, f := range item.Source.Fragments {
			fmt.Fprintf(p.Out, "                %s %s\n", p.Faint("+"), p.Faint(ui.Sanitize(f)))
		}
	}
	if item.Plugin != nil {
		line := ui.Sanitize(item.Plugin.ID)
		if item.Plugin.Version != "" {
			line += " " + p.Faint("v"+ui.Sanitize(item.Plugin.Version))
		}
		if item.Plugin.AlsoAuthored {
			line += "  " + p.Yellow("(a component of this name is ALSO authored in your source; apply keeps the authored one)")
		}
		fmt.Fprintf(p.Out, "      %s %s\n", p.Faint("plugin:   "), line)
	}
	if item.Drift != "" {
		fmt.Fprintf(p.Out, "      %s %s\n", p.Faint("drift:    "), driftStyled(p, item.Drift))
	}
	for _, t := range item.Transforms {
		word, color := skipStatus(p, t.Kind)
		fmt.Fprintf(p.Out, "      %s %s  %s\n", p.Faint("transform:"),
			color(word), p.Faint(ui.Sanitize(t.Reason)))
	}
	if len(item.Secrets) > 0 {
		// References only — never a resolved value. This is what lets explain
		// answer at all when the vault is locked.
		fmt.Fprintf(p.Out, "      %s %s\n", p.Faint("secrets:  "),
			p.Faint(ui.Sanitize(strings.Join(item.Secrets, ", "))+" (reference only; never the value)"))
	}
}

func explainSourceLine(p *ui.Printer, s explainSource) string {
	switch {
	case s.Multiple:
		return ui.Sanitize(s.File) + "  " + p.Faint("(assembled from several canonical sources)")
	case s.Missing:
		return ui.Sanitize(s.File) + "  " + p.Yellow("(recorded, but not on disk)")
	default:
		return ui.Sanitize(s.File)
	}
}

func ownershipStyled(p *ui.Printer, o string) string {
	switch o {
	case "managed":
		return p.Green(ui.GlyphOK + " managed")
	case "foreign":
		return p.Faint(ui.GlyphInfo + " foreign (yours; preserved by the key merge)")
	case "untracked":
		return p.Yellow(ui.GlyphWarn + " untracked (rendered, not yet applied)")
	default:
		return p.Faint(o)
	}
}

// driftStyled colors the classifier's own vocabulary — explain deliberately
// reuses those names verbatim rather than inventing a second one.
func driftStyled(p *ui.Printer, cls string) string {
	switch cls {
	case "clean":
		return p.Green(cls)
	case "converged", "new", "pending":
		return p.Cyan(cls)
	case "conflict", "foreign-collision", "orphan", "orphan-drifted":
		return p.Red(cls)
	default:
		return p.Yellow(cls)
	}
}
