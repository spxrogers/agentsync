package cli

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// explainInputs bundles everything buildExplainModel needs, so the builder stays
// a pure-ish function the tests can drive without a cobra command.
type explainInputs struct {
	fs            afero.Fs
	target        string
	pointer       string
	plan          render.RenderPlan
	agents        []string // registry order, for stable owner ordering
	canonical     source.Canonical
	state         *state.Targets
	userHome      string
	agentsyncHome string
	srcHome       string
	scope         adapter.Scope
	projectRoot   string
}

// buildExplainModel assembles the provenance answer for one destination path
// (optionally narrowed to one merged key).
func buildExplainModel(in explainInputs) explainModel {
	model := explainModel{
		Path:    in.target,
		Pointer: in.pointer,
		Scope:   in.scope.String(),
	}
	if in.scope == adapter.ScopeProject {
		model.Project = in.projectRoot
	}

	origins := pluginOrigins(in)
	secretRefs := secrets.SecretRefsByComponent(&in.canonical)
	hookEvents := canonicalHookEvents(in.canonical)

	// fileContent tracks the rendered bytes each agent would write to the target
	// as a whole file, so two agents disagreeing on one path is reported rather
	// than silently reduced to whichever row prints last.
	fileContent := map[string]string{}
	// renderedPointers is the union, across agents, of the keys agentsync owns in
	// this destination — the complement is what "foreign" means here.
	renderedPointers := map[string]bool{}
	// keyMergers lists the agents that key-merge into the target; the foreign
	// remainder is attached to each of them (in practice there is exactly one —
	// each agent has its own settings file).
	var keyMergers []string
	// pathManaged records whether the PATH matched any rendered op, independent
	// of the pointer filter, so "unmanaged path" and "managed path, unowned key"
	// are reported as the different problems they are.
	pathManaged := false

	items := walkPlanItems(planWalk{
		plan: in.plan, agents: in.agents, state: in.state, userHome: in.userHome, scope: in.scope, projectRoot: in.projectRoot,
		matchOp: func(agent string, op adapter.FileOp) bool {
			if normalizeDestPathBestEffort(op.Path) != in.target {
				return false
			}
			pathManaged = true
			if render.IsKeyMerge(op.MergeStrategy) && !containsString(keyMergers, agent) {
				keyMergers = append(keyMergers, agent)
			}
			return true
		},
	})
	byAgent := map[string][]planItem{}
	for _, it := range items {
		byAgent[it.agent] = append(byAgent[it.agent], it)
	}
	for _, name := range in.agents {
		res, ok := in.plan.PerAgent[name]
		if !ok {
			continue
		}
		owner := explainOwner{Agent: name}
		for _, it := range byAgent[name] {
			if it.ptr != "" {
				// Every rendered pointer counts as owned for the foreign-key
				// complement, whether or not the query narrows to one.
				renderedPointers[it.ptr] = true
				if in.pointer != "" && it.ptr != in.pointer {
					continue
				}
				owner.Items = append(owner.Items, keyItem(in, it, res.Skips, origins, secretRefs, hookEvents))
				continue
			}
			// Whole-file item. A pointer query does not apply to it.
			if in.pointer != "" {
				continue
			}
			fileContent[name] = string(it.op.Content)
			owner.Items = append(owner.Items, fileItem(in, it, res.Skips, origins, secretRefs))
		}
		if len(owner.Items) > 0 {
			model.Owners = append(model.Owners, owner)
		}
	}

	// Foreign keys: present in the destination, rendered by nobody. Reported as
	// first-class items (metadata only — the VALUE is never read out), because
	// "agentsync did not put this here" is exactly the answer a merged file
	// usually needs.
	if len(keyMergers) > 0 {
		foreign := foreignPointers(in, renderedPointers)
		for _, ptr := range foreign {
			if in.pointer != "" && ptr != in.pointer {
				continue
			}
			for i := range model.Owners {
				if containsString(keyMergers, model.Owners[i].Agent) {
					model.Owners[i].Items = append(model.Owners[i].Items,
						explainItem{Pointer: ptr, Ownership: "foreign"})
				}
			}
		}
	}

	model.Conflicts = crossAgentConflicts(fileContent)
	model.Unmanaged = len(model.Owners) == 0
	model.pathManaged = pathManaged
	for i := range model.Owners {
		items := model.Owners[i].Items
		sort.SliceStable(items, func(a, b int) bool { return items[a].Pointer < items[b].Pointer })
	}
	return model
}

// fileItem builds the provenance for a whole-file destination from its walk
// item, whose happlied is the recorded hash. Drift is classWithModeDrift — the
// same folded reading status reports — so a content-identical chmod is `drift`
// here exactly when it is there, rather than `clean` beside status's `drift`.
func fileItem(in explainInputs, it planItem, skips []adapter.Skip,
	origins map[string]explainPluginOrigin, secretRefs map[secrets.RefLocation][]string,
) explainItem {
	kind, name := componentFromSourceID(it.op.SourceID)

	item := explainItem{
		Ownership:  ownership(it.happlied),
		Component:  kind,
		Name:       name,
		Source:     sourceOf(in, it.op.SourceID, kind),
		Transforms: matchingSkips(skips, kind, name),
		Drift:      it.classWithModeDrift().String(),
	}
	if o, ok := origins[componentKey(kind, name)]; ok {
		po := o
		item.Plugin = &po
	}
	if refs := secretRefs[secrets.RefLocation{Kind: kind, ID: name}]; len(refs) > 0 {
		item.Secrets = refs
	}
	return item
}

// keyItem builds the provenance for one merged key inside a shared destination
// from its walk item. The walk decoded the destination once for the whole op,
// so every pointer of one file classifies against the same snapshot (#229
// axis 5); nothing here reads the destination again.
func keyItem(in explainInputs, it planItem, skips []adapter.Skip, origins map[string]explainPluginOrigin,
	secretRefs map[secrets.RefLocation][]string, hookEvents []string,
) explainItem {
	kind, name := componentFromPointer(it.agent, it.ptr, hookEvents)

	item := explainItem{
		Pointer:    it.ptr,
		Ownership:  ownership(it.happlied),
		Component:  kind,
		Name:       name,
		Transforms: matchingSkips(skips, kind, name),
		Drift:      it.cls.String(),
	}
	// KeyEntry.SourceID inherits the OP-level SourceID, which for every MCP/hook
	// key-merge op is a "<kind>/* (multiple)" sentinel — the state file genuinely
	// does not say which mcp/<id>.toml produced /mcpServers/github. The real
	// per-key resolution is the pointer-shape mapping reconcile owns, so this
	// derives from it rather than trusting the coarser recorded value.
	item.Source = pointerSource(in, it.agent, it.ptr, it.op.SourceID, hookEvents)
	if o, ok := origins[componentKey(kind, name)]; ok {
		po := o
		item.Plugin = &po
	}
	if refs := secretRefs[secrets.RefLocation{Kind: kind, ID: name}]; len(refs) > 0 {
		item.Secrets = refs
	}
	return item
}

// ownership maps a recorded state hash to the ownership vocabulary: a rendered
// item state has never recorded is "untracked" (agentsync would write it on the
// next apply, but has not yet), anything recorded is "managed".
func ownership(appliedSHA string) string {
	if appliedSHA == "" {
		return "untracked"
	}
	return "managed"
}

// sourceOf resolves a whole-file op's SourceID to the report's source block,
// decomposing fragment-composed memory and flagging a source file that is named
// but absent.
func sourceOf(in explainInputs, srcID, kind string) *explainSource {
	if srcID == "" {
		return nil
	}
	if strings.HasSuffix(srcID, "(multiple)") {
		return &explainSource{File: srcID, Multiple: true}
	}
	out := &explainSource{File: filepath.ToSlash(srcID)}
	// Memory's SourceID names only AGENTS.md even when the source is
	// fragment-composed, so a naive answer would under-report exactly the
	// provenance this command promises ("memory/AGENTS.md + fragments"). Decompose
	// at query time via the loaded model's fragment map.
	if kind == "memory" && len(in.canonical.Memory.Fragments) > 0 {
		frags := make([]string, 0, len(in.canonical.Memory.Fragments))
		for f := range in.canonical.Memory.Fragments {
			frags = append(frags, "memory/fragments/"+f)
		}
		sort.Strings(frags)
		out.Fragments = frags
	}
	if _, err := in.fs.Stat(filepath.Join(in.srcHome, filepath.FromSlash(srcID))); err != nil {
		// A stale old-spelling SourceID (F1's rewrite is scoped per tree and a
		// miss self-heals only on the next apply) must degrade gracefully rather
		// than claim an `agents/…` file that no longer exists.
		out.Missing = true
	}
	return out
}

// pointerSource resolves a merged key's canonical source-of-record from the
// pointer shape (the mcp/lsp/hooks container families), falling back to the
// op-level SourceID when the pointer names no single source.
func pointerSource(in explainInputs, agent, ptr, opSourceID string, hookEvents []string) *explainSource {
	abs := pointerSourceFile(in.srcHome, agent, ptr, hookEvents)
	if abs == "" {
		if opSourceID == "" {
			return nil
		}
		return &explainSource{File: filepath.ToSlash(opSourceID), Multiple: strings.HasSuffix(opSourceID, "(multiple)")}
	}
	rel, err := filepath.Rel(in.srcHome, abs)
	if err != nil {
		rel = abs
	}
	out := &explainSource{File: filepath.ToSlash(rel)}
	if _, err := in.fs.Stat(abs); err != nil {
		out.Missing = true
	}
	return out
}

// componentFromSourceID maps a canonical-relative SourceID to (kind, name).
// The legacy `agents/` spelling is accepted so a state entry written before the
// #200 rename still resolves to a subagent instead of an unknown kind.
func componentFromSourceID(srcID string) (kind, name string) {
	if srcID == "" || strings.HasSuffix(srcID, "(multiple)") {
		return "", ""
	}
	slash := filepath.ToSlash(srcID)
	head, rest, ok := strings.Cut(slash, "/")
	if !ok {
		return "", ""
	}
	switch head {
	case "mcp":
		return "mcp", strings.TrimSuffix(rest, ".toml")
	case "lsp":
		return "lsp", strings.TrimSuffix(rest, ".toml")
	case "hooks":
		return "hook", strings.TrimSuffix(rest, ".toml")
	case "skills":
		first, _, _ := strings.Cut(rest, "/")
		return "skill", first
	case source.SubagentsDir, source.LegacySubagentsDir:
		return "subagent", strings.TrimSuffix(rest, ".md")
	case "commands":
		return "command", strings.TrimSuffix(rest, ".md")
	case "memory":
		return "memory", ""
	}
	return "", ""
}

// componentFromPointer maps a NATIVE key-merge pointer to (kind, name), routing
// hooks through the same canonical-event inversion reconcile's write-back uses.
func componentFromPointer(agent, ptr string, hookEvents []string) (kind, name string) {
	parts := strings.SplitN(strings.TrimPrefix(ptr, "/"), "/", 3)
	if len(parts) < 2 || parts[1] == "" {
		return "", ""
	}
	switch parts[0] {
	case "mcpServers", "mcp", "mcp_servers":
		return "mcp", parts[1]
	case "lspServers", "lsp":
		return "lsp", parts[1]
	case "hooks":
		if event, ok := canonicalHookEvent(agent, parts[1], hookEvents); ok {
			return "hook", event
		}
		return "hook", ""
	}
	return "", ""
}

// matchingSkips narrows an agent's reported losses to the one component this
// item is. render.SkipDetail is already the structured, --json-shaped half of
// the transform answer, so it is carried through verbatim.
func matchingSkips(skips []adapter.Skip, kind, name string) []render.SkipDetail {
	if kind == "" {
		return nil
	}
	var out []render.SkipDetail
	for _, s := range skips {
		if s.Component != kind {
			continue
		}
		if s.Name != name {
			continue
		}
		out = append(out, render.SkipDetail{
			Component: s.Component,
			Name:      untrusted.Wrap(s.Name),
			Reason:    untrusted.Wrap(s.Reason),
			Kind:      s.Kind,
		})
	}
	return out
}

// foreignPointers lists the destination's own second-level keys that no enabled
// agent renders — the user/agent-authored remainder a key-merge preserves. Only
// the POINTERS are collected; values are never read out of the model.
func foreignPointers(in explainInputs, rendered map[string]bool) []string {
	strategy := ""
	for _, name := range in.agents {
		res, ok := in.plan.PerAgent[name]
		if !ok {
			continue
		}
		for _, op := range res.Ops {
			if render.IsKeyMerge(op.MergeStrategy) && normalizeDestPathBestEffort(op.Path) == in.target {
				strategy = op.MergeStrategy
			}
		}
	}
	if strategy == "" {
		return nil
	}
	dest := readDestFile(strategy, in.target)
	if len(dest) == 0 {
		return nil
	}
	var out []string
	for _, ptr := range render.CollectPointers(dest, "") {
		if !rendered[ptr] {
			out = append(out, ptr)
		}
	}
	sort.Strings(out)
	return out
}

// crossAgentConflicts reports two agents rendering DIFFERENT whole-file content
// to one destination. Several agents legitimately share a path (the breadth tier
// shares ~/.agents/skills/; several agents pass through AGENTS.md) — identical
// content is fine and silent. Disagreement is a real answer the user needs.
func crossAgentConflicts(fileContent map[string]string) []explainRivalry {
	if len(fileContent) < 2 {
		return nil
	}
	byContent := map[string][]string{}
	for agent, content := range fileContent {
		byContent[hashContent([]byte(content))] = append(byContent[hashContent([]byte(content))], agent)
	}
	if len(byContent) < 2 {
		return nil
	}
	agents := make([]string, 0, len(fileContent))
	for agent := range fileContent {
		agents = append(agents, agent)
	}
	sort.Strings(agents)
	return []explainRivalry{{
		Agents: agents,
		Reason: "these agents render DIFFERENT content to this one path; whichever applies last wins",
	}}
}

// pluginOrigins attributes each component to the plugin that contributes it, by
// set-difference plus per-plugin isolated re-projection — the derive-first
// approach `plugin explain` already uses, so it adds no new invariants and no
// canonical-schema change.
//
// Nothing in the canonical model marks a component projected-vs-authored at all,
// so the authored set is established by loading the source WITHOUT the plugin
// cache; anything a plugin projects that is also in that set is reported as a
// tie (AlsoAuthored) rather than silently attributed to the plugin.
func pluginOrigins(in explainInputs) map[string]explainPluginOrigin {
	if len(in.canonical.Plugins) == 0 {
		return nil
	}
	authored := map[string]bool{}
	if base, err := source.Load(in.fs, in.srcHome); err == nil {
		for _, k := range canonicalComponentKeys(base) {
			authored[k] = true
		}
	}
	pluginCacheRoot := filepath.Join(in.agentsyncHome, ".state", "cache", "plugins")
	out := map[string]explainPluginOrigin{}
	for _, pl := range in.canonical.Plugins {
		proj, ok, err := marketplace.ProjectInstalled(in.fs, in.agentsyncHome, pluginCacheRoot, pl, true)
		if err != nil || !ok {
			continue
		}
		origin := explainPluginOrigin{
			ID:      pluginOriginLabel(pl),
			Version: pl.Plugin.Version.Unverified(),
		}
		projected := source.Canonical{
			MCPServers: proj.MCPServers,
			Skills:     proj.Skills,
			Subagents:  proj.Subagents,
			Commands:   proj.Commands,
			Hooks:      proj.Hooks,
			LSPServers: proj.LSPServers,
		}
		for _, k := range canonicalComponentKeys(projected) {
			o := origin
			o.AlsoAuthored = authored[k]
			// First plugin wins the slot, matching the concatenation order
			// projection uses; a later plugin's same-named component would lose
			// the same way at apply.
			if _, taken := out[k]; !taken {
				out[k] = o
			}
		}
	}
	return out
}

// pluginOriginLabel is the human label for a plugin (the id@marketplace form,
// falling back to the short id) — the same label `plugin explain` prints.
func pluginOriginLabel(pl source.Plugin) string {
	if !pl.Plugin.ID.Empty() {
		return pl.Plugin.ID.Unverified()
	}
	return pl.ID.Unverified()
}

// canonicalComponentKeys enumerates a canonical's components as "kind\x00name"
// identity keys, the currency plugin attribution matches on.
func canonicalComponentKeys(c source.Canonical) []string {
	var out []string
	for _, m := range c.MCPServers {
		out = append(out, componentKey("mcp", m.ID))
	}
	for _, l := range c.LSPServers {
		out = append(out, componentKey("lsp", l.ID))
	}
	for _, s := range c.Skills {
		out = append(out, componentKey("skill", s.Name))
	}
	for _, s := range c.Subagents {
		out = append(out, componentKey("subagent", s.Name))
	}
	for _, cm := range c.Commands {
		out = append(out, componentKey("command", cm.Name))
	}
	for _, h := range c.Hooks {
		out = append(out, componentKey("hook", h.Event.Unverified()))
	}
	return out
}

func componentKey(kind, name string) string { return kind + "\x00" + name }

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// normalizeDestPathBestEffort is normalizeDestPath for a path agentsync itself
// produced (an op.Path), where the error branch is unreachable in practice.
func normalizeDestPathBestEffort(p string) string {
	out, err := normalizeDestPath(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return out
}
