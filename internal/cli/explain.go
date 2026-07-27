package cli

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// ---- model ------------------------------------------------------------------

// explainModel is the `--json` contract. The text output renders the same data,
// human-shaped, with the `path#pointer` labels `diff` already prints.
//
// It carries METADATA ONLY — never destination content. That is not just
// caution: `diff` must read dest content a prior apply wrote with resolved
// cleartext, so it fails closed when a ${secret:…} can't be resolved for
// masking. With no content emitted there is nothing to mask, so explain works
// even with a locked vault — a diagnostic advantage diff structurally can't
// have. The only secret-adjacent output is the FACT that a reference resolved
// at a location, which carries no secret material. For content, use `diff`.
type explainModel struct {
	Path      string           `json:"path"`
	Pointer   string           `json:"pointer,omitempty"`
	Scope     string           `json:"scope"`
	Project   string           `json:"project,omitempty"`
	Unmanaged bool             `json:"unmanaged"`
	Owners    []explainOwner   `json:"owners"`
	Conflicts []explainRivalry `json:"conflicts,omitempty"`

	// pathManaged is internal: whether the PATH matched a rendered op before the
	// pointer filter narrowed it. It shapes the not-found error and is not part
	// of the machine contract.
	pathManaged bool
}

// explainOwner groups a path's items by the agent that renders them. Several
// agents can render the same destination (the breadth tier shares
// `~/.agents/skills/`; several agents pass through `AGENTS.md`), so per-owner
// grouping is a first-class part of the shape, not a formatting choice.
type explainOwner struct {
	Agent string        `json:"agent"`
	Items []explainItem `json:"items"`
}

type explainItem struct {
	// Pointer is absent for whole-file items.
	Pointer string `json:"pointer,omitempty"`
	// Ownership is managed (agentsync renders it and state records it), untracked
	// (agentsync renders it but no apply has recorded it yet), or foreign (present
	// in the destination, rendered by nobody — a user/agent-authored key the
	// key-merge preserves).
	Ownership string `json:"ownership"`
	// Component/Name identify the canonical component behind a managed item.
	Component string               `json:"component,omitempty"`
	Name      string               `json:"name,omitempty"`
	Source    *explainSource       `json:"source,omitempty"`
	Plugin    *explainPluginOrigin `json:"plugin,omitempty"`
	// Transforms are the adapter's reported losses for this component, verbatim
	// from render.SkipDetail (kind = reduced | dropped).
	Transforms []render.SkipDetail `json:"transforms,omitempty"`
	// Drift reuses the existing classifier's vocabulary verbatim.
	Drift string `json:"drift,omitempty"`
	// Secrets lists ${secret:…}/${env:…} REFERENCES, never resolved values.
	Secrets []string `json:"secrets,omitempty"`
}

type explainSource struct {
	// File is the canonical file, relative to the agentsync home that produced it.
	File string `json:"file,omitempty"`
	// Multiple marks a destination assembled from N canonical sources — the
	// "(multiple)" SourceID sentinel the MCP/hook key-merge ops carry.
	Multiple bool `json:"multiple"`
	// Fragments lists memory/fragments/* that compose File, when it is
	// fragment-composed memory. Without this, a SourceID-based answer would
	// under-report exactly the provenance this command promises.
	Fragments []string `json:"fragments,omitempty"`
	// Missing marks a source file the recorded provenance names but that is not
	// on disk — e.g. a state entry still spelling the retired `agents/` subagent
	// directory. Reported, never claimed as the source.
	Missing bool `json:"missing,omitempty"`
}

type explainPluginOrigin struct {
	ID      string `json:"id"`
	Version string `json:"version,omitempty"`
	// AlsoAuthored marks a plugin component whose name collides with a
	// user-authored component of the same kind. Apply resolves that by
	// concatenation with the authored component FIRST, so the authored one wins
	// any last-write-wins destination; explain reports the tie rather than
	// inventing a second precedence rule for the read side.
	AlsoAuthored bool `json:"alsoAuthored,omitempty"`
}

// explainRivalry reports two agents rendering DIFFERENT content to one
// destination path — surfaced as a first-class answer, not an error.
type explainRivalry struct {
	Pointer string   `json:"pointer,omitempty"`
	Agents  []string `json:"agents"`
	Reason  string   `json:"reason"`
}

// ---- command ----------------------------------------------------------------

func newExplainCmd() *cobra.Command {
	var (
		jsonOut     bool
		pointerFlag string
	)
	cmd := &cobra.Command{
		Use:   "explain <path>[#<pointer>]",
		Short: "show what produced a destination file (or one merged key)",
		Long: `explain answers "why does this file look like this?" for a destination an
agent reads — which canonical file produced it, which plugin it came from, what
the adapter did to it on the way, and whether it has drifted.

  agentsync explain ~/.claude/settings.json
  agentsync explain ~/.claude/settings.json#/mcpServers/github
  agentsync explain ~/.claude/settings.json --pointer /mcpServers/github
  agentsync explain ~/.claude/settings.json --json

The argument splits on the LAST '#' (a filename may contain '#'; the JSON
pointers here never do — they are '/'-delimited). --pointer is the unambiguous
escape hatch and wins over an in-argument '#'.

explain prints METADATA ONLY — provenance, ownership, adapter transforms, and
the drift classification. It never prints destination content: no foreign key
values, no drift snippets. That is what makes it work with a locked secrets
vault, where 'diff' must fail closed. For content, use 'agentsync diff <path>'.

For per-plugin translation coverage, see 'agentsync plugin explain'.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rawPath, ptr := splitPathPointer(args[0])
			if pointerFlag != "" {
				ptr = pointerFlag
			}
			if ptr != "" && !strings.HasPrefix(ptr, "/") {
				return fmt.Errorf("pointer %q must start with '/' (e.g. /mcpServers/github)", ui.Sanitize(ptr))
			}
			return explainRun(cmd, rawPath, ptr, jsonOut)
		},
	}
	cmd.Flags().StringVar(&pointerFlag, "pointer", "", "JSON pointer of one merged key (unambiguous alternative to <path>#<pointer>)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the structured provenance payload instead of the formatted report")
	markScopeAware(cmd)
	return cmd
}

// splitPathPointer splits "<path>#<pointer>" on the LAST '#'. Filenames may
// contain '#'; the JSON pointers explain accepts never do (they are
// '/'-delimited and only '#'-free segments are reachable), so the last '#' is
// the unambiguous separator. A '#' with no '/'-leading remainder is treated as
// part of the filename.
func splitPathPointer(arg string) (path, pointer string) {
	i := strings.LastIndex(arg, "#")
	if i < 0 {
		return arg, ""
	}
	rest := arg[i+1:]
	if !strings.HasPrefix(rest, "/") {
		return arg, ""
	}
	return arg[:i], rest
}

func explainRun(cmd *cobra.Command, rawPath, ptr string, jsonOut bool) error {
	scopeFlag, projectFlag := scopeFlagValues(cmd)
	p, err := newPrinter(cmd)
	if err != nil {
		return err
	}
	target, err := normalizeDestPath(rawPath)
	if err != nil {
		return err
	}

	home := paths.AgentsyncHome(paths.OSEnv{})
	userHome := paths.HomeDir(paths.OSEnv{})
	fs := afero.NewOsFs()

	// Scope: honour the flags, and otherwise INFER project scope when the given
	// destination lies inside a project tree — the whole UX is "point at a file",
	// and a provenance report that describes the wrong scope's render is worse
	// than none. (The `plugin explain` machinery hardcodes user scope; it is
	// deliberately not reused here.)
	if scopeFlag == "" && projectFlag == "" {
		if root, ok := inferProjectRootForPath(target, home); ok {
			projectFlag = root
		}
	}
	// Live re-render, like `diff`: state alone describes the LAST apply and is
	// empty before the first one, so it would silently narrate history. State is
	// still consulted below, to tell "what was applied" from "what would be".
	//
	// The Flags variant, not the flag-reading one: the inference above may have
	// supplied a project root the user never typed, and re-reading the command's
	// flags here would throw it away.
	c, sc, projectRoot, err := loadProjectedForScopeFlags(cmd, fs, home, scopeFlag, projectFlag, true)
	if err != nil {
		return err
	}
	s, err := state.Load(filepath.Join(home, ".state", "targets.json"))
	if err != nil {
		return err
	}

	reg := registryFactory()
	var agents []string
	for name, ag := range c.Config.Agents {
		if ag.Enabled {
			agents = append(agents, name)
		}
	}
	sort.Strings(agents)
	// Resolve secrets for HASHING only, exactly as `status` does: apply writes
	// (and RecordOpsState hashes) the secret-RESOLVED content, so hashing a
	// templated render would classify every synced ${secret:…}/${env:…} item as
	// a phantom "pending" forever. Fall back to the templated render when the
	// backend is unavailable (locked age key / CI) — explain still answers, with
	// the same degraded false-pending status has in that mode.
	//
	// This does NOT weaken the metadata-only guarantee: the resolved model is
	// hashed and never emitted. That is what lets explain work against a locked
	// vault at all, where `diff` must fail closed because it prints content.
	rendered := secrets.ForRender(c)
	secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
	if resolved, serr := secrets.SubstituteCanonical(c, secBackend, secrets.EnvBackend{}); serr == nil {
		rendered = resolved
	}
	plan, err := render.Plan(rendered, reg, agents, sc, projectRoot, s, userHome)
	if err != nil {
		return err
	}

	srcHome := sourceHomeForScope(home, sc, projectRoot)
	model := buildExplainModel(explainInputs{
		fs:            fs,
		target:        target,
		pointer:       ptr,
		plan:          plan,
		agents:        reg.Names(),
		canonical:     c,
		state:         s,
		userHome:      userHome,
		agentsyncHome: home,
		srcHome:       srcHome,
		scope:         sc,
		projectRoot:   projectRoot,
	})

	if model.Unmanaged {
		if jsonOut {
			if err := emitJSON(p.Out, model); err != nil {
				return err
			}
		}
		return unmanagedPathError(target, ptr, model.pathManaged, plan, reg.Names())
	}
	if jsonOut {
		return emitJSON(p.Out, model)
	}
	printExplain(p, model)
	return nil
}

// normalizeDestPath resolves rawPath to the form op.Path comparisons expect:
// absolute, symlink-resolved, cleaned. `diff` matches by exact string equality
// after Abs alone, so a symlinked ~/.claude or a trailing slash reads as "not
// managed" — for a command whose entire UX is "point at a file" that is a bug,
// not a nicety. A path that does not exist yet still normalizes: its deepest
// existing ancestor is resolved and the remainder rejoined.
func normalizeDestPath(raw string) (string, error) {
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	// Not on disk (or unreadable): resolve the deepest existing ancestor so a
	// symlinked parent directory still matches, and rejoin the tail.
	dir, rest := filepath.Dir(abs), filepath.Base(abs)
	for dir != filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Clean(filepath.Join(resolved, rest)), nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = filepath.Dir(dir)
	}
	return abs, nil
}

// inferProjectRootForPath returns the project root whose tree contains path, so
// `explain <repo>/.claude/settings.json` describes the PROJECT render without
// the user having to say so. It walks up from the path's directory looking for a
// `.agentsync/` tree, exactly as --scope project does from cwd.
//
// The one tree it must NOT match is the USER agentsync home: a destination under
// $HOME sits below ~/.agentsync's own parent, so a home that happens to look
// like a project root would silently reclassify every user-scope destination.
func inferProjectRootForPath(path, userAgentsyncHome string) (string, bool) {
	root, found, err := project.Discover(filepath.Dir(path))
	if err != nil || !found {
		return "", false
	}
	if filepath.Clean(project.Home(root)) == filepath.Clean(userAgentsyncHome) {
		return "", false
	}
	return root, true
}

// unmanagedPathError reports a path (or pointer) no enabled agent renders,
// distinctly from a managed one — mirroring `diff [<path>]`. It suggests the
// nearest managed path so a typo or a symlink surprise is self-correcting.
func unmanagedPathError(target, ptr string, pathManaged bool, plan render.RenderPlan, names []string) error {
	if ptr != "" && pathManaged {
		return fmt.Errorf("%s is managed by agentsync, but no enabled agent owns the key %s; "+
			"run `agentsync explain %s` to see the keys that are owned",
			ui.Sanitize(target), ui.Sanitize(ptr), ui.Sanitize(target))
	}
	msg := fmt.Sprintf("path %s is not managed by agentsync (no enabled agent renders it); "+
		"explain takes a filesystem path, not an agent or plugin name", ui.Sanitize(target))
	if near := nearestManagedPath(target, plan, names); near != "" {
		msg += fmt.Sprintf("\n  did you mean %s?", ui.Sanitize(near))
	}
	return fmt.Errorf("%s", msg)
}

// nearestManagedPath picks the managed destination sharing the longest path
// prefix with target — enough to catch a wrong basename or a stale directory
// without pulling in an edit-distance dependency.
func nearestManagedPath(target string, plan render.RenderPlan, names []string) string {
	best, bestLen := "", 0
	for _, name := range names {
		res, ok := plan.PerAgent[name]
		if !ok {
			continue
		}
		for _, op := range res.Ops {
			n := commonPathPrefixLen(target, op.Path)
			if n > bestLen {
				best, bestLen = op.Path, n
			}
		}
	}
	// A shared root like "/home" is not a suggestion; require a real overlap.
	if bestLen < 2 {
		return ""
	}
	return best
}

func commonPathPrefixLen(a, b string) int {
	as := strings.Split(filepath.ToSlash(a), "/")
	bs := strings.Split(filepath.ToSlash(b), "/")
	n := 0
	for n < len(as) && n < len(bs) && as[n] == bs[n] {
		n++
	}
	return n
}
