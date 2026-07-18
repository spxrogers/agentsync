package windsurf

import (
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// memoryRuleFrontmatter is the activation frontmatter render prepends to the
// project-scope rule. Windsurf workspace rules declare their activation mode in
// frontmatter via the `trigger` field (always_on/model_decision/glob/manual);
// without it a rule's activation is undefined, so the projected memory could be
// inert. `always_on` matches memory semantics. Ingest strips this block (and any
// other leading fence) so the canonical body round-trips byte-clean and a re-apply
// never double-fences (see stripMemoryRuleFrontmatter).
const memoryRuleFrontmatter = "---\ntrigger: always_on\n---\n\n"

// renderMemory projects the canonical memory body into Windsurf rules.
//
//   - Project scope: `.windsurf/rules/agentsync.md`, body prefixed with
//     `trigger: always_on` frontmatter (workspace rules declare activation in
//     frontmatter; see memoryRuleFrontmatter).
//   - User scope: the single global rules file
//     `~/.codeium/windsurf/memories/global_rules.md` (always-on, no frontmatter,
//     body verbatim). Windsurf documents a 6,000-character limit for this file;
//     agentsync writes the body as-is and leaves truncation to Windsurf, so an
//     oversized memory is reported as a (lossy) projection in the docs rather
//     than silently trimmed here. Like Claude's `~/.claude/CLAUDE.md`, the file
//     is whole-file owned; a pre-existing hand-authored copy is preserved by the
//     writer's foreign-collision backup on first apply.
func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if c.Memory.Body == "" {
		return nil, nil, nil
	}
	banner := c.Config.MemoryBannerEnabled()
	if p.RulesDir != "" {
		// The managed banner goes AFTER the activation frontmatter so the `---`
		// fence stays at byte 0 (Windsurf only parses frontmatter at the top of
		// the file). Ingest strips the frontmatter and capture strips the banner,
		// so both round-trip cleanly out of the canonical body.
		body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, memoryRuleFile, banner)
		return []adapter.FileOp{{
			Action:        "write",
			Path:          filepath.Join(p.RulesDir, memoryRuleFile),
			Content:       []byte(memoryRuleFrontmatter + body),
			Mode:          0o644,
			SourceID:      "memory/AGENTS.md",
			MergeStrategy: "replace",
		}}, nil, nil
	}
	if p.GlobalRules == "" {
		return nil, []adapter.Skip{{
			Component: "memory",
			Name:      "rules",
			Reason:    "no Windsurf rules target at this scope",
			Kind:      adapter.SkipDropped,
		}}, nil
	}
	body := source.RenderManagedMemory(c.Memory.Body, c.Memory.Fragments, filepath.Base(p.GlobalRules), banner)
	return []adapter.FileOp{{
		Action:        "write",
		Path:          p.GlobalRules,
		Content:       []byte(body),
		Mode:          0o644,
		SourceID:      "memory/AGENTS.md",
		MergeStrategy: "replace",
	}}, nil, nil
}

// stripMemoryRuleFrontmatter removes a leading activation-frontmatter fence from
// a captured workspace rule, returning the canonical body. It strips ANY leading
// `---`…`---` fence at byte 0 regardless of the `trigger:` value — Windsurf honors
// the trigger, so folding a hand-changed fence into the body would make the next
// apply re-prepend agentsync's own `trigger: always_on` fence on top of it,
// producing a malformed double-fenced rule that breaks activation.
//
// The two returned signals let the caller warn precisely:
//   - wasAgentsyncBlock reports whether the stripped fence was EXACTLY the
//     agentsync-rendered `trigger: always_on` block (the byte-clean round-trip);
//   - hadForeignFrontmatter reports whether a fence was present but was NOT the
//     agentsync block (a hand-changed trigger). Its activation mode has no
//     canonical home, so the caller warns that the metadata is not captured.
//
// A file with no leading fence returns both false and is left untouched — no
// warning. Only a byte-0 `---\n` opens a fence, and the fence closes at the next
// line that is exactly `---`, so an in-body `---` horizontal rule is never
// mistaken for a fence.
func stripMemoryRuleFrontmatter(data []byte) (body string, wasAgentsyncBlock, hadForeignFrontmatter bool) {
	s := string(data)
	if strings.HasPrefix(s, memoryRuleFrontmatter) {
		return s[len(memoryRuleFrontmatter):], true, false
	}
	// Tolerate the agentsync fence without the trailing blank line.
	const bare = "---\ntrigger: always_on\n---\n"
	if strings.HasPrefix(s, bare) {
		return s[len(bare):], true, false
	}
	// Any other leading frontmatter fence (a hand-changed trigger, extra keys):
	// strip it so re-apply never double-fences, and signal it was foreign so the
	// caller warns that its activation metadata is not captured.
	if rest, stripped := stripLeadingFrontmatter(s); stripped {
		return rest, false, true
	}
	return s, false, false
}
