package cline

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/source"
)

// managedWorkflowMarker is the reversible ownership marker agentsync prepends to
// every workflow it renders into `.clinerules/workflows/`. Unlike memory — a
// single fixed filename (`agentsync.md`) whose presence alone marks ownership —
// workflows have arbitrary, canonical-command-derived filenames, so Ingest cannot
// scope by name. Instead it captures ONLY workflows carrying this marker (stripping
// it, so the canonical command body never contains it) and leaves human-authored
// workflows untouched — the same ownership discipline memory gets from its fixed
// filename, closing the over-capture asymmetry. The marker is an HTML comment
// (inert in Cline's markdown) under agentsync's reserved `agentsync:managed`
// namespace, mirroring the memory managed-banner markers.
const managedWorkflowMarker = "<!-- agentsync:managed cline-workflow -->"

// wrapManagedWorkflow prepends the ownership marker to a workflow body.
func wrapManagedWorkflow(body string) string {
	return managedWorkflowMarker + "\n" + body
}

// stripManagedWorkflow removes the ownership marker from a rendered workflow file's
// content, reporting whether the file was agentsync-owned (carried the marker). A
// file without the marker is a human-authored workflow: owned is false and the
// caller skips it, so a foreign workflow is never captured into the canonical model.
func stripManagedWorkflow(content string) (body string, owned bool) {
	return strings.CutPrefix(content, managedWorkflowMarker+"\n")
}

// renderCommands projects canonical slash commands into Cline workflows
// (`.clinerules/workflows/<name>.md`, invoked as `/<name>.md`). Cline workflows are
// PLAIN markdown (steps as prose, no frontmatter), so only the body survives: a
// command's `description`/`argument-hint`/`allowed-tools` frontmatter is dropped
// with a reported Skip. Each rendered workflow carries a leading
// managedWorkflowMarker so Ingest can capture only agentsync-owned workflows (the
// marker is stripped on ingest, so the canonical body round-trips clean). Workflows
// are project-scoped; at user scope (p.WorkflowsDir == "") each command is skipped.
func (a *Adapter) renderCommands(c source.Canonical, p Paths) ([]adapter.FileOp, []adapter.Skip, error) {
	if p.WorkflowsDir == "" {
		var skips []adapter.Skip
		for _, cmd := range c.Commands {
			skips = append(skips, adapter.Skip{
				Component: "command",
				Name:      cmd.Name,
				Reason:    "Cline's global workflows live in the non-XDG ~/Documents/Cline/Workflows path agentsync does not target; workflows project at project scope only (.clinerules/workflows/)",
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
				Reason:    "Cline workflows are plain markdown (no frontmatter); dropped " + strings.Join(sortedKeys(cmd.Frontmatter), ", "),
				Kind:      adapter.SkipReduced,
			})
		}
		ops = append(ops, adapter.FileOp{
			Action:        "write",
			Path:          filepath.Join(p.WorkflowsDir, cmd.Name+".md"),
			Content:       []byte(wrapManagedWorkflow(cmd.Body)),
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
