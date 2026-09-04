package opencode

import (
	"fmt"
	"path/filepath"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

// opencodeCommandPassthroughKeys are the OpenCode command frontmatter keys
// agentsync does not model explicitly but that OpenCode supports, so they pass
// through verbatim rather than being dropped. Verified against the OpenCode
// command docs (https://opencode.ai/docs/commands/). `description` and `model`
// are handled explicitly by renderCommands; `argument-hint` is Claude-only and
// reported as a Skip. The command body is the template, so there is no
// `template` key here (see the note below).
var opencodeCommandPassthroughKeys = map[string]bool{
	"agent":   true,
	"subtask": true,
}

// renderCommands translates Claude-shaped command frontmatter to OpenCode-shaped
// and emits FileOps for .config/opencode/commands/<name>.md.
//
// Frontmatter mapping:
//
//	description   -> description  (direct copy)
//	model         -> model        (direct copy)
//	argument-hint -> drop + Skip  (no OpenCode equivalent)
//	<other>       -> passthrough if a legal OpenCode command key (agent,
//	                 subtask); else drop + Skip
//
// No frontmatter key is silently dropped: it is either copied to the output or
// named in an adapter.Skip. Body is preserved as-is; OpenCode treats the body as
// the command template. We do NOT add a `template:` frontmatter key to avoid
// duplication.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	var ops []adapter.FileOp
	var skips []adapter.Skip
	for _, cmd := range c.Commands {
		out := map[string]any{}
		if v, ok := cmd.Frontmatter["description"]; ok {
			out["description"] = v
		}
		if v, ok := cmd.Frontmatter["model"]; ok {
			out["model"] = v
		}

		// Dedicated Skip for the well-known Claude-only key (its Reason string is
		// asserted by existing tests).
		if _, ok := cmd.Frontmatter["argument-hint"]; ok {
			skips = append(skips, adapter.Skip{
				Component: "command", Name: cmd.Name,
				Reason: "Claude `argument-hint` has no OpenCode equivalent",
				Kind:   adapter.SkipReduced,
			})
		}

		// Every remaining key: pass through if it is a legal OpenCode command key,
		// otherwise Skip.
		handled := map[string]bool{"description": true, "model": true, "argument-hint": true}
		skips = passthroughOrSkip("command", cmd.Name, cmd.Frontmatter, out, handled, opencodeCommandPassthroughKeys, skips)

		body, err := claude.EncodeFrontmatter(out, cmd.Body)
		if err != nil {
			return nil, nil, fmt.Errorf("encode opencode command %s: %w", cmd.Name, err)
		}
		ops = append(ops, adapter.FileOp{
			Action:        adapter.ActionWrite,
			Path:          filepath.Join(p.CommandsDir, cmd.Name+".md"),
			Content:       body,
			Mode:          0o644,
			SourceID:      filepath.Join("commands", cmd.Name+".md"),
			MergeStrategy: "replace",
		})
	}
	return ops, skips, nil
}
