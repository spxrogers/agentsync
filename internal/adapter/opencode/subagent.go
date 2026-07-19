package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// opencodeAgentPassthroughKeys are the OpenCode agent (subagent) frontmatter
// keys agentsync does not model explicitly but that OpenCode supports, so they
// pass through verbatim rather than being dropped. Verified against the OpenCode
// agent docs (https://opencode.ai/docs/agents/). `description`, `model`, and
// `mode` are handled explicitly by renderSubagents; `tools`/`color` are
// Claude-only and reported as Skips.
var opencodeAgentPassthroughKeys = map[string]bool{
	"temperature": true,
	"top_p":       true,
	"permission":  true,
	"disable":     true,
	"prompt":      true,
	"steps":       true,
}

// passthroughOrSkip copies every unhandled frontmatter key from fm into out:
// a key in `legal` (an OpenCode-supported frontmatter key) is copied verbatim
// (passthrough); any other key is dropped with a SkipReduced note naming it, so
// no key is ever both absent from the rendered output AND absent from the skips.
// `handled` names the keys the caller already dealt with (copied to out or given
// a dedicated Skip), which this loop must not re-process.
func passthroughOrSkip(component, name string, fm, out map[string]any, handled, legal map[string]bool, skips []adapter.Skip) []adapter.Skip {
	for k, v := range fm {
		if handled[k] {
			continue
		}
		if legal[k] {
			out[k] = v // legal OpenCode key — pass through verbatim
			continue
		}
		skips = append(skips, adapter.Skip{
			Component: component, Name: name,
			Reason: fmt.Sprintf("frontmatter key %q has no OpenCode %s equivalent (dropped)", k, component),
			Kind:   adapter.SkipReduced,
		})
	}
	return skips
}

// renderSubagents translates Claude-shaped subagent frontmatter to
// OpenCode-shaped and emits FileOps for .config/opencode/agents/<name>.md.
//
// Frontmatter mapping:
//
//	description -> description  (direct copy)
//	model       -> model        (direct copy)
//	mode        -> mode         (preserved if present; defaults to "subagent")
//	tools       -> drop + Skip  (OpenCode uses permission model; non-trivial mapping)
//	color       -> drop + Skip  (no OpenCode equivalent)
//	<other>     -> passthrough if a legal OpenCode agent key (temperature,
//	               top_p, permission, disable, prompt, steps); else drop + Skip
//
// No frontmatter key is ever silently dropped: it is either copied to the output
// or named in an adapter.Skip.
func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, s := range c.Subagents {
		out := map[string]any{}
		if v, ok := s.Frontmatter["description"]; ok {
			out["description"] = v
		}
		if v, ok := s.Frontmatter["model"]; ok {
			out["model"] = v
		}
		// Preserve an OpenCode `mode` (primary/all/subagent) carried in the
		// canonical frontmatter — e.g. one captured from a native agent by Ingest
		// — so a primary/all agent is not silently demoted to subagent on the next
		// apply. A Claude-shaped subagent has no `mode`, so it still defaults to
		// "subagent" (apply-only flow unchanged).
		if v, ok := s.Frontmatter["mode"]; ok {
			out["mode"] = v
		} else {
			out["mode"] = "subagent"
		}

		// Dedicated Skips for the two well-known Claude-only keys, whose Reason
		// strings carry specific guidance (and are asserted by existing tests).
		if _, ok := s.Frontmatter["tools"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "subagent", Name: s.Name,
				Reason: "Claude `tools` allowlist not projected to OpenCode `permission` (manual rule design needed)",
				Kind:   adapter.SkipReduced,
			})
		}
		if _, ok := s.Frontmatter["color"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "subagent", Name: s.Name,
				Reason: "Claude `color` has no OpenCode equivalent",
				Kind:   adapter.SkipReduced,
			})
		}

		// Every remaining key: pass through if it is a legal OpenCode agent key,
		// otherwise Skip. `mode` is not `handled` here as an alias — it is copied
		// to out above — so it must be listed so the loop leaves it alone.
		handled := map[string]bool{"description": true, "model": true, "mode": true, "tools": true, "color": true}
		skips = passthroughOrSkip("subagent", s.Name, s.Frontmatter, out, handled, opencodeAgentPassthroughKeys, skips)

		body, err := claude.EncodeFrontmatter(out, s.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode opencode subagent %s: %w", s.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.AgentsDir, s.Name+".md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("agents", s.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}
