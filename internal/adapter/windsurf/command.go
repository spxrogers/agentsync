package windsurf

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// renderCommands projects canonical slash commands into Windsurf workflows
// (invoked as `/<name>`): project scope → `.windsurf/workflows/<name>.md`, user
// scope → `~/.codeium/windsurf/global_workflows/<name>.md` (both documented
// filesystem locations). Windsurf workflows are PLAIN markdown (title/
// description/steps as prose, no frontmatter), so only the body survives: a
// command's `description`/`argument-hint`/`allowed-tools` frontmatter has no
// Windsurf field and is dropped with a reported Skip.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.WorkflowsDir == "" {
		var skips []adapter.Skip
		for _, cmd := range c.Commands {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "no Windsurf workflows target at this scope",
				Kind:      adapter.SkipDropped,
			})
		}
		return nil, skips, nil
	}
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		if len(cmd.Frontmatter) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Windsurf workflows are plain markdown (no frontmatter); dropped " + strings.Join(sortedKeys(cmd.Frontmatter), ", "),
				Kind:      adapter.SkipReduced,
			})
		}
		// Windsurf documents a 12,000-character-per-file limit for workflows
		// (verified against docs.devin.ai — same figure as workspace rules).
		// agentsync writes the body verbatim and leaves enforcement to Windsurf:
		// it neither truncates nor flags an oversized workflow, mirroring the
		// memory handling (documented in the capability matrix).
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.WorkflowsDir, cmd.Name+".md"),
			Content:       []byte(cmd.Body),
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}

// sortedKeys returns the keys of fm in sorted order.
func sortedKeys(fm map[string]any) []string {
	out := make([]string, 0, len(fm))
	for k := range fm {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// stripLeadingFrontmatter removes a leading YAML frontmatter fence from s when
// one is present at byte 0, returning the content after it and whether a fence
// was stripped. A fence opens only with a byte-0 `---\n` line and closes at the
// next line that is exactly `---`; a single following blank line is consumed so
// the returned body starts at the first real content line. If s does not open
// with `---\n`, or no closing `---` line is found, s is returned untouched with
// stripped=false — so an in-body `---` horizontal rule (never at byte 0 as an
// opener, never reached without an opener) is never mistaken for a fence.
//
// It is shared by the rule path (stripMemoryRuleFrontmatter) and the workflow
// ingest path: both strip a hand-authored leading fence so it never folds into
// the canonical body. Windsurf workflows are plain markdown with no honored
// frontmatter, so this only tidies a stray fence out of the captured body.
func stripLeadingFrontmatter(s string) (rest string, stripped bool) {
	if !strings.HasPrefix(s, "---\n") {
		return s, false
	}
	body := s[len("---\n"):]
	for {
		nl := strings.IndexByte(body, '\n')
		line := body
		if nl >= 0 {
			line = body[:nl]
		}
		if line == "---" {
			if nl < 0 {
				return "", true // closing fence at EOF, empty body
			}
			after := body[nl+1:]
			return strings.TrimPrefix(after, "\n"), true // consume one blank line
		}
		if nl < 0 {
			return s, false // reached EOF without a closing fence: not frontmatter
		}
		body = body[nl+1:]
	}
}
