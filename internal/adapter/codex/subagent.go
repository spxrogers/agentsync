package codex

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// codexAgentFile is the TOML shape of a Codex custom agent (~/.codex/agents/<name>.toml).
// Codex defines subagents as standalone TOML, where the prose lives under
// `developer_instructions` rather than a markdown body (per
// developers.openai.com/codex/subagents). agentsync projects the canonical
// markdown body onto developer_instructions and carries the `name` (required by
// Codex), `description`, and `model` frontmatter; there is no per-agent tools
// allowlist. Codex-only agent keys agentsync has no canonical source for
// (model_reasoning_effort, sandbox_mode, mcp_servers, skills.config,
// nickname_candidates) are simply not emitted.
type codexAgentFile struct {
	Name                  string `toml:"name"`
	Description           string `toml:"description,omitempty"`
	Model                 string `toml:"model,omitempty"`
	DeveloperInstructions string `toml:"developer_instructions"`
}

// codexAgentKnownKeys are the canonical frontmatter keys with a Codex TOML home.
// `name` is required by Codex and is written to the TOML `name` field — it is a
// known key, NOT a dropped one (omitting it here once caused `explain` to report
// a spurious "dropped name" against agents whose name in fact carried over).
// Anything else (notably `tools` and `color`) has no target and is dropped with
// a Skip.
var codexAgentKnownKeys = map[string]bool{"name": true, "description": true, "model": true}

// renderSubagents projects canonical subagents into Codex agent TOML files.
// markdown body → developer_instructions; name + description + model carry over;
// any other frontmatter key (notably `tools` and `color`) has no Codex
// equivalent and is dropped with a reported Skip. Because Codex's `name` is the
// agent's identity, two subagents with distinct file stems that resolve to the
// same effective `name` would write two TOMLs claiming one identity; that
// collision is reported as an error rather than emitted silently.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	// seenNames maps each effective Codex agent name already rendered to the
	// canonical name (file stem) that first produced it, so a later subagent
	// resolving to the same effective name is caught as a collision.
	seenNames := map[string]string{}
	for _, s := range c.Subagents {
		// Codex treats the `name` field as the source of truth; prefer the
		// frontmatter name when present, falling back to the filename-derived
		// canonical name (which is what Claude subagents use).
		name := s.Name
		if fmName := fmString(s.Frontmatter, "name"); fmName != "" {
			name = fmName
		}
		// Two agents claiming one Codex identity cannot both be written. Return
		// an error (Render's contract already carries one) rather than silently
		// overwrite or emit duplicate TOMLs — a silent drop would violate the
		// "capture it or acknowledge it, never drop it silently" invariant.
		if first, ok := seenNames[name]; ok {
			return nil, nil, fmt.Errorf(
				"codex subagents %q and %q resolve to the same agent name %q; rename one or set distinct frontmatter names",
				first, s.Name, name,
			)
		}
		seenNames[name] = s.Name
		af := codexAgentFile{
			Name:                  name,
			Description:           fmString(s.Frontmatter, "description"),
			Model:                 fmString(s.Frontmatter, "model"),
			DeveloperInstructions: s.Body,
		}
		if dropped := droppedKeys(s.Frontmatter); len(dropped) > 0 {
			skips = append(skips, adapter.Skip{
				Component: "subagent",
				Name:      s.Name,
				Reason: fmt.Sprintf("Codex agents are TOML with no per-agent tools allowlist; dropped %s",
					strings.Join(dropped, ", ")),
				Kind: adapter.SkipReduced,
			})
		}
		body, err := toml.Marshal(af)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal subagent %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.AgentsDir, s.Name+".toml"),
			Content:       body,
			Mode:          0o644,
			SourceID:      source.SubagentSourceID(s.Name),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}

// fmString returns the string value at key in a frontmatter map, or "".
func fmString(fm map[string]any, key string) string {
	s, _ := fm[key].(string)
	return s
}

// droppedKeys returns the sorted frontmatter keys with no Codex projection
// target (anything outside codexAgentKnownKeys), reported in a Skip so the loss
// is never silent.
func droppedKeys(fm map[string]any) []string {
	var out []string
	for k := range fm {
		if !codexAgentKnownKeys[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
