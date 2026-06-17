package windsurf

import (
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// memoryRuleFrontmatter is the activation frontmatter render prepends to the
// project-scope rule. Windsurf workspace rules declare their activation mode in
// frontmatter via the `trigger` field (always_on/model_decision/glob/manual);
// without it a rule's activation is undefined, so the projected memory could be
// inert. `always_on` matches memory semantics. Ingest strips exactly this block
// so the canonical body round-trips byte-clean.
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

// stripMemoryRuleFrontmatter removes the agentsync-rendered activation
// frontmatter from a captured workspace rule, returning the canonical body.
// exact reports whether the prefix was the agentsync-rendered block; when the
// file carries no (or different) frontmatter the content is returned untouched
// and the caller decides whether to warn — a hand-changed trigger has no
// canonical home, so capturing it would lose it on the next apply anyway.
func stripMemoryRuleFrontmatter(data []byte) (body string, exact bool) {
	s := string(data)
	if len(s) >= len(memoryRuleFrontmatter) && s[:len(memoryRuleFrontmatter)] == memoryRuleFrontmatter {
		return s[len(memoryRuleFrontmatter):], true
	}
	// Tolerate the fence without the trailing blank line.
	const bare = "---\ntrigger: always_on\n---\n"
	if len(s) >= len(bare) && s[:len(bare)] == bare {
		return s[len(bare):], true
	}
	return s, false
}
