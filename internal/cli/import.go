package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/capture"
	"github.com/spxrogers/agentsync/internal/marketplace"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/spxrogers/agentsync/internal/ui"
)

// loaderFsForState returns an afero.Fs suitable for re-loading the
// canonical repo when seeding state — the OS FS is correct here because
// state lives on the same disk as ~/.agentsync/.
func loaderFsForState() afero.Fs { return afero.NewOsFs() }

// jsonUnmarshalLoose is a thin wrapper that returns nil on empty input
// (so callers can treat empty as "absent") and surfaces real parse errors.
// It accepts JSONC (comments, trailing commas) via hujson so seeding state
// from a hand-commented opencode.json doesn't mis-hash the dest as null —
// matching the apply/ingest read path.
func jsonUnmarshalLoose(data []byte, v *map[string]any) error {
	if len(data) == 0 {
		*v = map[string]any{}
		return nil
	}
	std, err := standardizeJSONC(data)
	if err != nil {
		return err
	}
	return json.Unmarshal(std, v)
}

// decodeDestBytes decodes a destination config file's bytes into a generic map
// per the op's merge strategy: TOML for merge-toml-keys (Codex config.toml),
// otherwise the JSONC-tolerant loose reader (which standardizes comments/trailing
// commas in a hand-edited opencode.json). A rendered op.Content is always JSON
// regardless of the on-disk format, so callers still decode op.Content with
// jsonUnmarshalLoose. This is the single CLI-side dest decoder; the render
// package has its own (decodeDestObject) whose JSON arm is plain encoding/json
// because apply re-writes those dests as standard JSON. The key-merge predicate
// is shared: render.IsKeyMerge.
func decodeDestBytes(strategy string, data []byte, v *map[string]any) error {
	if strategy == "merge-toml-keys" {
		if len(data) == 0 {
			*v = map[string]any{}
			return nil
		}
		return toml.Unmarshal(data, v)
	}
	return jsonUnmarshalLoose(data, v)
}

// readDestFile reads a destination file and decodes it per the op's merge
// strategy, swallowing read/parse errors into an empty map — the behavior the
// drift diagnostics (status/diff/reconcile) want so a missing or transiently
// unreadable dest classifies as "absent" rather than crashing. Replaces the
// JSON-only readJSONFile so a TOML config.toml decodes correctly.
func readDestFile(strategy, path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	_ = decodeDestBytes(strategy, data, &m)
	return m
}

// collectStateSeedPointers returns the JSON pointers we record state for
// when seeding from a freshly imported canonical. We borrow the same
// ownership rule used by render.RecordOpsState (second-level granularity).
func collectStateSeedPointers(m map[string]any) []string {
	return render.CollectPointers(m, "")
}

// hashAtPointer returns the SHA-256 of the JSON-marshaled value at ptr, or the
// empty string when no value exists there. The empty sentinel (rather than
// hashing a synthesized null) lets the seed loop SKIP an absent pointer, which
// matches render.RecordOpsState skipping never-landed pointers — a present-null
// value still hashes.
func hashAtPointer(m map[string]any, ptr string) string {
	v, ok := getJSONPointer(m, ptr)
	if !ok {
		return ""
	}
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// getJSONPointer resolves a "/a/b/c" RFC 6901 pointer against m. The bool is
// false when any segment is missing — distinct from a present value that
// happens to be null. We re-implement here rather than exporting
// render.getPointer because keeping that helper unexported preserves the
// current package boundary.
func getJSONPointer(m map[string]any, ptr string) (any, bool) {
	if ptr == "" || ptr[0] != '/' {
		return m, true
	}
	parts := strings.Split(ptr[1:], "/")
	for i, p := range parts {
		// Decode RFC 6901 escapes.
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	var cur any = m
	for _, p := range parts {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, exists := mm[p]
		if !exists {
			return nil, false
		}
		cur = v
	}
	return cur, true
}

// newImportCmd returns the "import" subcommand.
// Selector grammar: <agent>[:<component>[:<name>]]
// e.g. claude (full config), claude:mcp (all servers), claude:mcp:github (one).
func newImportCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "import <agent>[:<component>[:<name>]]",
		Args:  cobra.ExactArgs(1),
		Short: "capture native config back into the canonical source",
		Long: `import reads items from an agent's native config and writes them back
into ~/.agentsync/ as the canonical source of truth.

Selector grammar: <agent>[:<component>[:<name>]]
  agent     - registered agent name (claude, opencode, codex, cursor)
  component - mcp | skill | agent | command | hook | lsp | memory | plugin
  name      - item name (server id, skill/subagent/command name, hook event,
              or plugin name)

Dropping the name imports every entry of that component; dropping the
component too imports the agent's full native config in one pass. Use
--dry-run to preview which source files would be written, without writing.

The plugin component captures the agent's installed plugins + their
marketplaces (Claude only in v1): it re-fetches each marketplace and plugin
into the agentsync cache and pins them, so a real import needs network access.
A plugin's marketplace is resolved from agentsync's own registered marketplaces
first, then the agent's native config; a plugin whose marketplace is registered
in neither (e.g. a built-in like 'claude-plugins-official' you have not yet
added) is reported and skipped. Register it with 'agentsync marketplace add
<source>' and re-import.

Examples:
  agentsync import claude                 # all importable components
  agentsync import claude:mcp             # every MCP server
  agentsync import claude:mcp:github      # a single MCP server
  agentsync import claude:plugin          # every installed plugin + marketplace
  agentsync import claude --dry-run       # preview without writing
  agentsync import opencode:agent:reviewer`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// import mutates ~/.agentsync/ AND .state/targets.json. It
			// must hold the global lock so a concurrent apply on the
			// other terminal cannot interleave its own state.Save and
			// destroy our seed entries (or vice versa). --dry-run writes
			// neither, so it is read-only and skips the lock — matching
			// `apply --dry-run`.
			if dryRun {
				return importRun(cmd, args, true)
			}
			home := paths.AgentsyncHome(paths.OSEnv{})
			return withGlobalLock(home, func() error {
				return importRun(cmd, args, false)
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview which source files would be written, without writing")
	return cmd
}

func importRun(cmd *cobra.Command, args []string, dryRun bool) error {
	sel := args[0]
	agentName, component, name, err := parseSelector(sel)
	if err != nil {
		return err
	}

	p, perr := newPrinter(cmd)
	if perr != nil {
		return perr
	}
	// Wrap stderr once so every "warning: …" line — ours, the adapter's
	// Ingest, capture's re-reference, etc. — picks up the same bold-yellow
	// "⚠️ warning:" styling. Lines that don't start with "warning: " pass
	// through unchanged, so indented continuations and "agentsync:" notes
	// look the same as before.
	warnW := ui.NewWarnWriter(p.Err, p)
	io := &importIO{p: p, out: p.Out, err: warnW, dryRun: dryRun}

	home := paths.AgentsyncHome(paths.OSEnv{})
	reg := registryFactory()
	a := reg.Lookup(agentName)
	if a == nil {
		return fmt.Errorf("adapter %q not registered; valid agents: %s", agentName, validAgents)
	}
	// Route the adapter's Ingest warnings through the same styled writer.
	// Adapters that don't implement the setter (the noop adapter today) keep
	// writing to os.Stderr — fine, they emit no Ingest warnings anyway.
	if s, ok := a.(adapterStderrSetter); ok {
		s.SetStderr(warnW)
	}
	// Gate codex/cursor the same way `agent add` does: they're registered as
	// noop adapters, so Ingest returns an empty canonical and import would
	// otherwise fail with a misleading "<component> not found in native config".
	// Tell the user the agent is unimplemented instead.
	if !v1Supported[agentName] && os.Getenv("AGENTSYNC_ALLOW_UNIMPLEMENTED") != "1" {
		return fmt.Errorf("agent %q is not yet implemented in v1.0 "+
			"(cursor is planned for a later release); "+
			"set AGENTSYNC_ALLOW_UNIMPLEMENTED=1 to import from its noop adapter anyway", agentName)
	}

	c, err := a.Ingest(adapter.ScopeUser, "")
	if err != nil {
		return fmt.Errorf("ingest %s: %w", agentName, err)
	}

	// Three selector depths, narrowing from broad to specific:
	//   <agent>                     -> every importable component
	//   <agent>:<component>         -> every entry of that component
	//   <agent>:<component>:<name>  -> a single named entry
	// The bulk forms reuse the same per-component importers with an empty name
	// filter, so MCP/LSP/hook capture still routes through capture.Capture
	// (secret re-referencing + source-only field preservation) — see the
	// importer bodies. Text components write directly via source.Write*.
	var imp importedSet
	var importErr error
	switch component {
	case "":
		imp, importErr = importAllComponents(io, home, a, agentName, c)
	default:
		ids, err := importComponent(io, home, a, agentName, c, component, name)
		importErr = err
		imp.add(component, ids)
		// A bulk component import (no name) that matched nothing is not an
		// error — report it and exit cleanly. A named import that matched
		// nothing already returned a "not found" error above.
		if err == nil && len(ids) == 0 && name == "" {
			io.infof("no %s found in %s native config", component, agentName)
		}
	}

	// A dry-run wrote nothing, so there is no state to seed and the
	// foreign-collision warning below would describe the pre-import world.
	// Stop here with just the preview (surfacing any selector/not-found error).
	if dryRun {
		return importErr
	}

	// Seed state with the destination's current content hash so the next
	// \`apply\` sees Clean/Converged instead of ForeignCollision and
	// silently overwriting the file the user just imported from. We do
	// this from the *current on-disk dest* hashes — not the rendered
	// content — because the canonical→render pipeline may translate the
	// content slightly (frontmatter normalization, etc.) and we want the
	// next apply to compare against the file that exists today.
	//
	// Run this even when importErr != nil: a bulk/full-agent import can fail
	// partway after already writing earlier components to the canonical, and
	// those writes MUST be seeded or the next apply foreign-collides and
	// overwrites the file they were just imported from. Seeding is scoped to
	// imp (the items actually imported), so a partial failure seeds exactly what
	// was written and an un-imported sibling's state is never re-stamped.
	if seedErr := seedStateFromCurrentDest(home, agentName, reg, imp); seedErr != nil {
		io.warnf("import state seed failed: %v", seedErr)
	}

	// Warn if the destination file has additional pointers the canonical
	// does not own. Scoped to top-level sections the canonical actually
	// renders — pointers under sections agentsync doesn't model at all
	// (Claude Code's runtime state, telemetry, settings agentsync doesn't
	// own) are out of scope by design, and the merge-keys writer's per-
	// pointer OwnedKeys check preserves them on apply rather than colliding.
	if warnings := unimportedDestPointers(home, agentName, reg); len(warnings) > 0 {
		io.note("these items exist in the destination but agentsync did not capture them:")
		for _, w := range warnings {
			fmt.Fprintf(io.err, "  %s\n", w)
		}
		fmt.Fprintln(io.err, "  agentsync will leave them alone on apply (it only writes keys it owns). Import an item by name if you want agentsync to manage it.")
	}
	// Surface a partial-import error after seeding so the command still exits
	// non-zero, but the components that were written are now owned.
	return importErr
}

// unimportedDestPointers returns a list of human-readable labels for
// destination pointers / files that exist on disk for the given agent
// but are NOT in the canonical model after the import. Used to alert
// the user that import covers ONE item and the rest of the destination
// is still unowned.
//
// We render canonical → ops, build the set of (path, pointer) pairs
// canonical claims, and diff against the actual on-disk contents.
func unimportedDestPointers(home, agentName string, reg *adapter.Registry) []string {
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	// Lenient: this is a post-import diagnostic re-render. A strict plugin
	// conflict (unrelated to the import) must not abort it — matching the
	// read-only commands (status/diff/explain).
	c, err := marketplace.LoadProjectedLenient(loaderFsForState(), home, pluginCacheRoot, nil)
	if err != nil {
		return nil
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return nil
	}
	ops, _, err := a.Render(secrets.ForRender(c), adapter.ScopeUser, "")
	if err != nil {
		return nil
	}
	var out []string
	for _, op := range ops {
		if !render.IsKeyMerge(op.MergeStrategy) {
			continue
		}
		data, readErr := os.ReadFile(op.Path)
		if readErr != nil {
			continue
		}
		var existing map[string]any
		if decErr := decodeDestBytes(op.MergeStrategy, data, &existing); decErr != nil {
			continue
		}
		var ours map[string]any
		if jsonErr := jsonUnmarshalLoose(op.Content, &ours); jsonErr != nil {
			continue
		}
		ownedPtrs := map[string]bool{}
		ourSections := map[string]bool{}
		for k := range ours {
			ourSections[escapePointerSegment(k)] = true
		}
		for _, p := range collectStateSeedPointers(ours) {
			ownedPtrs[p] = true
		}
		// Walk existing's second-level pointers and flag ones agentsync's
		// canonical doesn't own — but ONLY under top-level sections the
		// canonical actually renders (mirrors render.scopeOwnedToSections).
		// Pointers under sections agentsync doesn't model at all (Claude
		// Code's runtime state, telemetry, unmodeled settings) are out of
		// scope: the merge-keys writer leaves them untouched on apply, so
		// flagging them would be noise — and the previous "will trigger
		// ForeignCollision" prediction for them was factually wrong (the
		// per-pointer OwnedKeys check fires only for keys this op writes).
		for _, p := range collectStateSeedPointers(existing) {
			if ownedPtrs[p] {
				continue
			}
			if !ourSections[firstPointerSegmentEsc(p)] {
				continue
			}
			out = append(out, op.Path+"#"+p)
		}
	}
	return out
}

// firstPointerSegmentEsc returns the first (already-escaped) segment of a JSON
// pointer — "/enabledPlugins/foo@bar" → "enabledPlugins". Mirrors render's
// firstPointerSegment so the import-scope filter agrees with the writer's
// section-scoping of OwnedKeys.
func firstPointerSegmentEsc(ptr string) string {
	ptr = strings.TrimPrefix(ptr, "/")
	if i := strings.IndexByte(ptr, '/'); i >= 0 {
		return ptr[:i]
	}
	return ptr
}

// escapePointerSegment escapes a top-level map key into its JSON-pointer form
// (~ → ~0, / → ~1), matching the encoding collectStateSeedPointers emits, so
// the section set used for filtering agrees byte-for-byte.
func escapePointerSegment(k string) string {
	k = strings.ReplaceAll(k, "~", "~0")
	k = strings.ReplaceAll(k, "/", "~1")
	return k
}

// seedStateFromCurrentDest re-renders the canonical for agent and writes
// state entries reflecting the current on-disk content of each destination.
// This makes the next \`apply\` non-destructive — the destination is
// already known to agentsync, so any future divergence is real drift, not a
// first-run foreign-collision.
func seedStateFromCurrentDest(home, agentName string, reg *adapter.Registry, imp importedSet) error {
	statePath := filepath.Join(home, ".state", "targets.json")
	// State keys are HOME-relative against the user's $HOME (paths.HomeDir),
	// matching render.RecordOpsState — NOT the agentsync home, or apply
	// would never recognise the seeded entries as owned.
	userHome := paths.HomeDir(paths.OSEnv{})
	st, err := state.Load(statePath)
	if err != nil {
		return err
	}

	// Build a fresh canonical from disk and render ONLY the items this import
	// actually captured (imp). Lenient: a strict plugin conflict must not block
	// seeding (which would leave the just-imported dest exposed to a
	// ForeignCollision overwrite on next apply). Rendering the imported SUBSET —
	// not the whole canonical — is what scopes the seed: an un-imported sibling
	// isn't in the subset, so its state (and any drift it carries) is untouched.
	pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
	c, err := marketplace.LoadProjectedLenient(loaderFsForState(), home, pluginCacheRoot, nil)
	if err != nil {
		return err
	}
	a := reg.Lookup(agentName)
	if a == nil {
		return fmt.Errorf("adapter %q not registered", agentName)
	}
	sub := filterCanonicalTo(c, imp)
	ops, _, err := a.Render(secrets.ForRender(sub), adapter.ScopeUser, "")
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, op := range ops {
		if op.Action != "" && op.Action != "write" {
			continue
		}
		switch {
		case render.IsKeyMerge(op.MergeStrategy):
			// Per-key seed: hash the *current* value at each pointer the
			// rendered op claims to own. The dest is decoded per strategy (TOML
			// for merge-toml-keys); op.Content is always JSON.
			data, readErr := os.ReadFile(op.Path)
			if readErr != nil {
				continue // dest doesn't exist yet; nothing to seed
			}
			var existing map[string]any
			if decErr := decodeDestBytes(op.MergeStrategy, data, &existing); decErr != nil {
				continue
			}
			var ours map[string]any
			if jsonErr := jsonUnmarshalLoose(op.Content, &ours); jsonErr != nil {
				continue
			}
			for _, ptr := range collectStateSeedPointers(ours) {
				h := hashAtPointer(existing, ptr)
				if h == "" {
					// Pointer absent on disk — skip rather than seed a phantom
					// entry, matching render.RecordOpsState's skip of never-landed
					// pointers (a present-null value hashes and is seeded).
					continue
				}
				key := fmt.Sprintf("%s:%s:%s:%s:%s",
					agentName, adapter.ScopeUser.String(), "", paths.HomeRelative(userHome, op.Path), ptr)
				st.Keys[key] = state.KeyEntry{
					SHA256:    h,
					AppliedAt: now,
					SourceID:  op.SourceID,
				}
			}
		default:
			data, readErr := os.ReadFile(op.Path)
			if readErr != nil {
				continue
			}
			sum := sha256.Sum256(data)
			key := fmt.Sprintf("%s:%s:%s:%s",
				agentName, adapter.ScopeUser.String(), "", paths.HomeRelative(userHome, op.Path))
			st.Files[key] = state.FileEntry{
				SHA256:    hex.EncodeToString(sum[:]),
				Mode:      op.Mode,
				AppliedAt: now,
				SourceID:  op.SourceID,
			}
		}
	}

	return state.Save(statePath, st)
}

// filterCanonicalTo returns a copy of c containing only the components named in
// imp — the items this import actually captured. Rendering THIS subset (rather
// than the whole canonical) scopes state seeding to the imported items. A
// reported-but-unwritten id simply won't be present in the re-loaded canonical,
// so it self-corrects on a partial import. Config is intentionally omitted so no
// settings op is emitted (settings are never imported).
func filterCanonicalTo(c source.Canonical, imp importedSet) source.Canonical {
	var sub source.Canonical
	sub.MCPServers = filterByKey(c.MCPServers, imp.MCP, func(m source.MCPServer) string { return m.ID })
	sub.LSPServers = filterByKey(c.LSPServers, imp.LSP, func(l source.LSPServer) string { return l.ID })
	sub.Skills = filterByKey(c.Skills, imp.Skills, func(s source.Skill) string { return s.Name })
	sub.Subagents = filterByKey(c.Subagents, imp.Subagents, func(s source.Subagent) string { return s.Name })
	sub.Commands = filterByKey(c.Commands, imp.Commands, func(cm source.Command) string { return cm.Name })
	sub.Hooks = filterByKey(c.Hooks, imp.HookEvents, func(h source.Hook) string { return h.Event })
	if imp.Memory {
		sub.Memory = c.Memory
	}
	return sub
}

// filterByKey returns the items whose key is in want (preserving order).
func filterByKey[T any](items []T, want []string, key func(T) string) []T {
	if len(want) == 0 {
		return nil
	}
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	var out []T
	for _, it := range items {
		if set[key(it)] {
			out = append(out, it)
		}
	}
	return out
}

// parseSelector splits "agent[:component[:name]]" into its parts. A bare
// "agent" (component == "") selects the whole config; "agent:component"
// (name == "") selects every entry of that component.
func parseSelector(sel string) (agentName, component, name string, err error) {
	parts := strings.SplitN(sel, ":", 3)
	agentName = parts[0]
	if len(parts) >= 2 {
		component = parts[1]
	}
	if len(parts) == 3 {
		name = parts[2]
	}
	if agentName == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: agent must be non-empty; expected <agent>[:<component>[:<name>]]", sel)
	}
	// "claude:" is a typo, not a request to import the whole agent.
	if len(parts) >= 2 && component == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: component must be non-empty", sel)
	}
	// "claude:mcp:" (trailing colon) is a typo, not a bulk request. Drop the
	// colon for a bulk import (claude:mcp) or supply a name (claude:mcp:github).
	if len(parts) == 3 && name == "" {
		return "", "", "", fmt.Errorf("invalid selector %q: trailing colon with no name; use %q to import every %s, or supply a name", sel, agentName+":"+component, component)
	}
	return agentName, component, name, nil
}

// importComponentOrder lists the importable components in the order a
// full-agent import walks them (and the order they appear in the summary).
// "subagent" is an accepted alias for "agent" in selectors but is not listed
// here to avoid importing subagents twice. "plugin" walks last because it
// re-fetches from the network (see importPlugins), so the offline components
// are captured first even if a plugin fetch later fails.
var importComponentOrder = []string{"mcp", "lsp", "skill", "agent", "command", "hook", "memory", "plugin"}

// importVerb is the past/conditional verb used in per-item and summary lines so
// a --dry-run preview reads "would import …" instead of "imported …".
func importVerb(dryRun bool) string {
	if dryRun {
		return "would import"
	}
	return "imported"
}

// adapterStderrSetter is the optional setter each concrete adapter
// (claude/opencode/codex) implements, letting CLI commands route the
// adapter's Ingest warnings through a styled writer.
type adapterStderrSetter interface{ SetStderr(io.Writer) }

// importIO bundles the styled printer, the streams it writes to, and the
// dry-run flag so every importXxx function emits its per-item lines, section
// headers, and warnings through one consistent shape — the same green ✓ /
// cyan → vocabulary `apply` and `status` use. The lazy `section` header keeps
// component groups from showing a name with no rows under it.
type importIO struct {
	p      *ui.Printer
	out    io.Writer
	err    io.Writer
	dryRun bool
	// section is the component label printed once just before the first item
	// in a full-agent walk (e.g. "mcp servers", "skills"). Empty for a named
	// or single-component import, where the indented item lines stand alone.
	section      string
	sectionShown bool
}

// item prints one "imported X" / "would import X" line, prefixed with a glyph
// and indented for readability. suffix is appended after path (e.g.
// " (3 entries)" for hooks). Calling this lazily flushes the section header
// the first time, so an empty component prints nothing.
func (i *importIO) item(path, suffix string) {
	i.flushSection()
	if i.dryRun {
		fmt.Fprintf(i.out, "  %s would import %s%s\n", i.p.Cyan(ui.GlyphArrow), path, suffix)
		return
	}
	fmt.Fprintf(i.out, "  %s imported %s%s\n", i.p.Green(ui.GlyphOK), path, suffix)
}

// flushSection prints the pending section header (if any) once. Callers should
// not need to invoke this directly — item() and warn() do it for the writer
// side; the full-agent walker resets section state between components.
func (i *importIO) flushSection() {
	if i.section == "" || i.sectionShown {
		return
	}
	fmt.Fprintln(i.out, i.p.Faint(i.section))
	i.sectionShown = true
}

// warn emits a "warning: …" line. Styling (bold-yellow "⚠️ warning:") is
// applied by the ui.WarnWriter wrapping i.err — emitting the plain prefix
// here means adapter Ingest, capture, and importIO all share one styling
// point, and no caller has to know about it. msg should not include a
// trailing newline; warn appends one.
func (i *importIO) warn(msg string) {
	fmt.Fprintf(i.err, "warning: %s\n", msg)
}

// note prints a cyan "note:" prefix followed by msg, for informational lines
// that aren't problems — matches the styling status.go uses for the same
// "this is FYI, not a warning" tier. msg should not include a trailing
// newline; note appends one.
func (i *importIO) note(msg string) {
	fmt.Fprintf(i.err, "%s %s\n", i.p.Cyan("note:"), msg)
}

// warnf is warn + fmt.Sprintf for the common "%v / %q" formatting.
func (i *importIO) warnf(format string, args ...any) {
	i.warn(fmt.Sprintf(format, args...))
}

// note prints a yellow "agentsync:" prefix (matching apply.go) for diagnostics
// that aren't warnings about user data but about how agentsync is proceeding.
func (i *importIO) notef(format string, args ...any) {
	fmt.Fprintf(i.err, "%s ", i.p.Yellow("agentsync:"))
	fmt.Fprintf(i.err, format, args...)
	if !strings.HasSuffix(format, "\n") {
		fmt.Fprintln(i.err)
	}
}

// info prints a faint informational line on stdout — for "nothing to do"
// outcomes the user still wants confirmed, like "no importable items found".
func (i *importIO) infof(format string, args ...any) {
	fmt.Fprintf(i.out, "%s ", i.p.Faint(ui.GlyphInfo))
	fmt.Fprintf(i.out, format, args...)
	if !strings.HasSuffix(format, "\n") {
		fmt.Fprintln(i.out)
	}
}

// sectionLabel maps the internal component key to the friendly header shown in
// a full-agent walk. Plural / two-word forms make the header scan as a heading,
// not as a tag.
var sectionLabel = map[string]string{
	"mcp":     "mcp servers",
	"lsp":     "lsp servers",
	"skill":   "skills",
	"agent":   "subagents",
	"command": "commands",
	"hook":    "hooks",
	"memory":  "memory",
	"plugin":  "plugins",
}

// importedSet records which component identities an import actually captured,
// so the state seeder can scope itself to exactly those items and not re-stamp
// (and thereby mask drift on) un-imported siblings. Plugins + marketplaces are
// intentionally NOT tracked here: the Claude adapter does not render
// enabledPlugins or extraKnownMarketplaces back into settings.json (it projects
// each plugin's components to native paths instead, where the consumer agent
// serves them as regular components — see the comment at the bottom of
// claude.Render), so there are no dest pointers to seed for them.
type importedSet struct {
	MCP        []string
	LSP        []string
	Skills     []string
	Subagents  []string
	Commands   []string
	HookEvents []string
	Memory     bool
}

// add routes a component's imported identities into the set.
func (s *importedSet) add(component string, ids []string) {
	switch component {
	case "mcp":
		s.MCP = append(s.MCP, ids...)
	case "lsp":
		s.LSP = append(s.LSP, ids...)
	case "skill":
		s.Skills = append(s.Skills, ids...)
	case "agent", "subagent":
		s.Subagents = append(s.Subagents, ids...)
	case "command":
		s.Commands = append(s.Commands, ids...)
	case "hook":
		s.HookEvents = append(s.HookEvents, ids...)
	case "memory":
		if len(ids) > 0 {
			s.Memory = true
		}
	}
}

// importComponent imports one component class from c. When name is empty it
// imports every entry of that component (the bulk form); when name is set it
// imports just that entry and errors if it is absent. The io carries the
// dry-run flag (which writes nothing and only reports what it would write) and
// the styled writers per-item lines go through. It returns the identities
// (server id, skill/subagent/command name, hook event) that were (or would be)
// imported; len is the item count.
func importComponent(io *importIO, home string, a adapter.Adapter, agentName string, c source.Canonical, component, name string) ([]string, error) {
	switch component {
	case "mcp":
		return importMCP(io, home, c, name)
	case "skill":
		return importSkill(io, home, c, name)
	case "agent", "subagent":
		return importSubagent(io, home, c, name)
	case "command":
		return importCommand(io, home, c, name)
	case "hook":
		return importHook(io, home, c, name)
	case "lsp":
		return importLSP(io, home, c, name)
	case "memory":
		return importMemory(io, home, c)
	case "plugin":
		return importPlugins(io, home, agentName, a, name)
	default:
		return nil, fmt.Errorf("unknown component %q; valid: mcp, skill, agent, command, hook, lsp, memory, plugin", component)
	}
}

// importAllComponents imports every importable component for the agent. Each
// non-empty component gets a faint section header above its items (printed
// lazily so an empty component prints nothing), followed by a bold summary
// line and a faint breakdown. dryRun is carried on the io so the preview
// writes nothing. The returned set names everything captured, so the caller
// can scope state seeding to it.
func importAllComponents(io *importIO, home string, a adapter.Adapter, agentName string, c source.Canonical) (importedSet, error) {
	var imp importedSet
	counts := map[string]int{}
	total := 0
	for _, comp := range importComponentOrder {
		// Set up the lazy section header for this component. The header is
		// printed by io.item() on the first row; if there are no rows, the
		// header is silently dropped — so an empty component is invisible.
		io.section = sectionLabel[comp]
		io.sectionShown = false
		ids, err := importComponent(io, home, a, agentName, c, comp, "")
		// Seed whatever WAS written even on error: a component can fail partway
		// after writing earlier items, and those must be owned or the next apply
		// foreign-collides on a file just imported from.
		imp.add(comp, ids)
		if err != nil {
			return imp, err
		}
		if len(ids) > 0 {
			counts[comp] = len(ids)
			total += len(ids)
		}
	}
	// Clear the section state so any later non-walk caller (none today, but the
	// io is reused for the seed/foreign-pointer warnings below) doesn't pick up
	// a stale header.
	io.section = ""
	io.sectionShown = false

	if total == 0 {
		io.infof("no importable items found in %s native config", agentName)
		return imp, nil
	}

	noun := "items"
	if total == 1 {
		noun = "item"
	}
	verb := importVerb(io.dryRun)
	glyph := io.p.Green(ui.GlyphOK)
	if io.dryRun {
		glyph = io.p.Cyan(ui.GlyphArrow)
	}
	fmt.Fprintln(io.out, "")
	fmt.Fprintf(io.out, "%s %s\n", glyph,
		io.p.Bold(fmt.Sprintf("%s %d %s from %s", verb, total, noun, agentName)))
	var parts []string
	for _, comp := range importComponentOrder {
		if counts[comp] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[comp], comp))
		}
	}
	fmt.Fprintf(io.out, "  %s\n", io.p.Faint(strings.Join(parts, "  ·  ")))
	return imp, nil
}

// importMCP captures the MCP server named name (or all of them when name is
// empty). Capture.Capture batches the whole slice in one call, so it
// re-references secrets and preserves source-only fields for every server.
// When io.dryRun is set it reports the targets without writing.
func importMCP(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.MCPServer
	for _, m := range c.MCPServers {
		if name == "" || m.ID == name {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("mcp server %q not found in native config", name)
		}
		return nil, nil
	}
	// Validate ids up front (before any write, and in dry-run) so the preview
	// matches a real import and a bulk write is atomic on a bad id.
	for _, m := range matched {
		if err := source.ValidateComponentID("mcp", m.ID); err != nil {
			return nil, err
		}
	}
	if !io.dryRun {
		single := source.Canonical{MCPServers: matched}
		if res, err := capture.Capture(home, &single, capture.Opts{Warn: io.err}); err != nil {
			// Seed the servers capture DID write before failing, so a partial
			// import doesn't leave them foreign-collided on the next apply.
			return idsFromWritten(res.Written), err
		}
	}
	ids := make([]string, len(matched))
	for i, m := range matched {
		io.item(fmt.Sprintf("mcp/%s.toml", m.ID), "")
		ids[i] = m.ID
	}
	return ids, nil
}

// idsFromWritten extracts component ids/events from capture.Capture's
// Result.Written paths ("mcp/<id>.toml", "lsp/<id>.toml", "hooks/<event>.toml"),
// so a partial capture seeds exactly what was written.
func idsFromWritten(written []string) []string {
	out := make([]string, 0, len(written))
	for _, w := range written {
		out = append(out, strings.TrimSuffix(filepath.Base(w), ".toml"))
	}
	return out
}

func importSkill(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.Skill
	for _, sk := range c.Skills {
		if name == "" || sk.Name == name {
			matched = append(matched, sk)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("skill %q not found in native config", name)
		}
		return nil, nil
	}
	for _, sk := range matched {
		if err := source.ValidateComponentID("skill", sk.Name); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(matched))
	for _, sk := range matched {
		if !io.dryRun {
			if err := source.WriteSkill(home, sk); err != nil {
				// Return the names already written so the caller seeds them;
				// otherwise the next apply foreign-collides on a file just imported.
				return names, fmt.Errorf("write skill %s: %w", sk.Name, err)
			}
		}
		io.item(fmt.Sprintf("skills/%s/SKILL.md", sk.Name), "")
		names = append(names, sk.Name)
	}
	return names, nil
}

func importSubagent(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.Subagent
	for _, sa := range c.Subagents {
		if name == "" || sa.Name == name {
			matched = append(matched, sa)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("subagent %q not found in native config", name)
		}
		return nil, nil
	}
	for _, sa := range matched {
		if err := source.ValidateComponentID("subagent", sa.Name); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(matched))
	for _, sa := range matched {
		if !io.dryRun {
			if err := source.WriteSubagent(home, sa); err != nil {
				return names, fmt.Errorf("write subagent %s: %w", sa.Name, err)
			}
		}
		io.item(fmt.Sprintf("agents/%s.md", sa.Name), "")
		names = append(names, sa.Name)
	}
	return names, nil
}

func importCommand(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.Command
	for _, cm := range c.Commands {
		if name == "" || cm.Name == name {
			matched = append(matched, cm)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("command %q not found in native config", name)
		}
		return nil, nil
	}
	for _, cm := range matched {
		if err := source.ValidateComponentID("command", cm.Name); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(matched))
	for _, cm := range matched {
		if !io.dryRun {
			if err := source.WriteCommand(home, cm); err != nil {
				return names, fmt.Errorf("write command %s: %w", cm.Name, err)
			}
		}
		io.item(fmt.Sprintf("commands/%s.md", cm.Name), "")
		names = append(names, cm.Name)
	}
	return names, nil
}

// importHook captures hooks for the named event (or all events when name is
// empty). name addresses an event, not an individual hook. It returns the
// DISTINCT events captured (one source file per event); the per-event line
// still reports the entry count. When io.dryRun is set it reports the target
// event files without writing.
func importHook(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.Hook
	for _, h := range c.Hooks {
		if name == "" || h.Event == name {
			matched = append(matched, h)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("hook event %q not found in native config", name)
		}
		return nil, nil
	}
	for _, h := range matched {
		if err := source.ValidateComponentID("hook event", h.Event); err != nil {
			return nil, err
		}
	}
	if !io.dryRun {
		single := source.Canonical{Hooks: matched}
		if res, err := capture.Capture(home, &single, capture.Opts{Warn: io.err}); err != nil {
			return idsFromWritten(res.Written), err
		}
	}
	// One file per event; report each, preserving first-seen order.
	perEvent := map[string]int{}
	var order []string
	for _, h := range matched {
		if _, seen := perEvent[h.Event]; !seen {
			order = append(order, h.Event)
		}
		perEvent[h.Event]++
	}
	for _, ev := range order {
		io.item(fmt.Sprintf("hooks/%s.toml", ev), fmt.Sprintf(" (%d entries)", perEvent[ev]))
	}
	return order, nil
}

func importLSP(io *importIO, home string, c source.Canonical, name string) ([]string, error) {
	var matched []source.LSPServer
	for _, ls := range c.LSPServers {
		if name == "" || ls.ID == name {
			matched = append(matched, ls)
		}
	}
	if len(matched) == 0 {
		if name != "" {
			return nil, fmt.Errorf("lsp server %q not found in native config", name)
		}
		return nil, nil
	}
	for _, ls := range matched {
		if err := source.ValidateComponentID("lsp", ls.ID); err != nil {
			return nil, err
		}
	}
	if !io.dryRun {
		single := source.Canonical{LSPServers: matched}
		if res, err := capture.Capture(home, &single, capture.Opts{Warn: io.err}); err != nil {
			return idsFromWritten(res.Written), err
		}
	}
	ids := make([]string, 0, len(matched))
	for _, ls := range matched {
		io.item(fmt.Sprintf("lsp/%s.toml", ls.ID), "")
		ids = append(ids, ls.ID)
	}
	return ids, nil
}

func importMemory(io *importIO, home string, c source.Canonical) ([]string, error) {
	// Memory is a single block, not a named collection; nothing to write when
	// the agent carries no memory (the common case during a full-agent import).
	if strings.TrimSpace(c.Memory.Body) == "" {
		return nil, nil
	}
	// The ingested memory is the rendered destination file. If apply wrote
	// fragment markers, reverse them back into AGENTS.md + the fragment files so
	// the round-trip is not lossy; otherwise fall back to the guard.
	mem, hadMarkers, err := source.CollapseMemoryMarkers(c.Memory.Body)
	switch {
	case err != nil:
		// Markers present but malformed/ambiguous — skip rather than guess.
		io.notef("skipping memory import — fragment markers could not be reversed (%v). Reconcile memory/ by hand, then apply.", err)
		return nil, nil
	case hadMarkers:
		if !io.dryRun {
			if werr := source.WriteMemory(home, mem); werr != nil {
				return nil, fmt.Errorf("write memory: %w", werr)
			}
		}
	case source.MemoryHasFragments(home):
		// No markers (collision/legacy) but the source is fragment-composed:
		// writing the expanded body would inline the @imports and orphan the
		// fragment files — skip with a warning rather than flatten silently.
		io.notef("skipping memory import — canonical memory uses fragments/ and the imported memory has no reversible markers; writing it back would inline the fragments and orphan their files. Edit memory/ directly, then apply.")
		return nil, nil
	default:
		if !io.dryRun {
			if werr := source.WriteMemory(home, c.Memory); werr != nil {
				return nil, fmt.Errorf("write memory: %w", werr)
			}
		}
	}
	io.item("memory/AGENTS.md", "")
	// A non-empty marker so importedSet.add flags memory was captured (it has no
	// id; the seeder includes c.Memory when this is set).
	return []string{"memory"}, nil
}

// importPlugins captures the agent's installed plugins + their marketplaces into
// the canonical source. It is the read-back of `marketplace add` + `plugin
// install`: for each enabled plugin whose marketplace is resolvable from the
// agent's native config, it re-fetches the marketplace and the plugin into the
// agentsync cache and writes marketplaces/<name>.toml + plugins/<id>.toml,
// producing byte-identical artifacts to the manual commands.
//
// Only agents implementing adapter.PluginIngester have a native plugin concept
// (Claude in v1); others import no plugins (not an error, so a full
// `import <agent>` stays clean for them). A real import needs network access to
// re-fetch; --dry-run discovers and previews without fetching or writing.
//
// A plugin's marketplace is resolved from agentsync's own registered
// marketplaces first (so `marketplace add` then re-import captures plugins from
// any marketplace, including Claude built-ins like claude-plugins-official),
// then from the agent's native config. A plugin whose marketplace is registered
// in neither — or one whose source type agentsync cannot fetch — is reported and
// skipped. name, when non-empty, selects a single plugin (matched by its name or
// "name@marketplace"); a no-match is an error. A fetch/install failure for one
// marketplace or plugin warns and skips rather than aborts, so one bad item
// does not strand the rest. Plugins are NOT dest-seeded into state (unlike the
// file components): they are a source-side declaration whose projected
// components materialise as new dest files on the next apply. The Claude
// adapter intentionally does not render enabledPlugins / extraKnownMarketplaces
// back into settings.json either (see the note at the bottom of claude.Render),
// so there is nothing to seed there.
func importPlugins(io *importIO, home, agentName string, a adapter.Adapter, name string) ([]string, error) {
	pi, ok := a.(adapter.PluginIngester)
	if !ok {
		return nil, nil
	}
	mps, nativePlugins, err := pi.IngestPlugins(adapter.ScopeUser, "")
	if err != nil {
		return nil, fmt.Errorf("discover %s plugins: %w", agentName, err)
	}

	mpByID := make(map[string]adapter.NativeMarketplace, len(mps))
	for _, m := range mps {
		mpByID[m.ID] = m
	}

	var want []adapter.NativePlugin
	for _, pl := range nativePlugins {
		if !pl.Enabled {
			continue
		}
		if name != "" && pl.Name != name && pl.Name+"@"+pl.MarketplaceID != name {
			continue
		}
		want = append(want, pl)
	}
	if len(want) == 0 {
		if name != "" {
			return nil, fmt.Errorf("plugin %q not found among %s's enabled plugins", name, agentName)
		}
		return nil, nil
	}

	// Resolve (and, on a real run, fetch) each needed marketplace exactly once.
	// The cached value is the agentsync marketplace name a plugin installs from;
	// "" marks an unresolvable marketplace already warned about.
	registered := registeredMarketplaceNames(home)
	resolved := map[string]string{}
	resolveMp := func(mpID string) (string, bool) {
		if n, done := resolved[mpID]; done {
			return n, n != ""
		}
		// Store-first: a marketplace already registered with `agentsync
		// marketplace add` is authoritative and already cached, so its plugins
		// install straight from it — no re-fetch, no marketplace-TOML rewrite, and
		// (in dry-run) no marketplace preview line, since it is already in source.
		// mpID doubles as the install/cache-dir key here; native marketplace ids
		// are clean slugs, so a name that would need sanitizing simply misses the
		// cache and degrades to a warn+skip in installPluginInto (no leak/crash).
		if registered[mpID] {
			resolved[mpID] = mpID
			return mpID, true
		}
		// Fallback: discover the marketplace from the agent's native config,
		// fetch it, and register it.
		nm, known := mpByID[mpID]
		if !known {
			io.warnf("skipping plugins from marketplace %q: registered in neither %s's "+
				"native config nor agentsync; run `agentsync marketplace add <source>` then re-import",
				mpID, agentName)
			resolved[mpID] = ""
			return "", false
		}
		src, rawURL, mappable := claudeSourceToAgentsync(nm.Source)
		if !mappable {
			io.warnf("skipping marketplace %q: unsupported source type %q", mpID, nm.Source.Type)
			resolved[mpID] = ""
			return "", false
		}
		if io.dryRun {
			// No fetch, so the declared agentsync name is unknown; preview by the
			// native id. The real run resolves and prints the actual filename.
			io.item(fmt.Sprintf("marketplaces/%s.toml", mpID), "")
			resolved[mpID] = mpID
			return mpID, true
		}
		mpName, _, ferr := addMarketplaceSource(home, src, rawURL)
		if ferr != nil {
			io.warnf("skipping marketplace %q: %v", mpID, ferr)
			resolved[mpID] = ""
			return "", false
		}
		io.item(fmt.Sprintf("marketplaces/%s.toml", mpName), "")
		resolved[mpID] = mpName
		return mpName, true
	}

	var ids []string
	for _, pl := range want {
		if pl.MarketplaceID == "" {
			io.warnf("skipping plugin %q: native config records no marketplace for it", pl.Name)
			continue
		}
		// The plugin name becomes plugins/<name>.toml; validate it up front (and
		// in dry-run) so a hostile native id can't escape the source dir and the
		// preview matches a real import.
		if verr := source.ValidateComponentID("plugin", pl.Name); verr != nil {
			io.warnf("skipping plugin %q: %v", pl.Name, verr)
			continue
		}
		mpName, mpOK := resolveMp(pl.MarketplaceID)
		if !mpOK {
			continue
		}
		if io.dryRun {
			io.item(fmt.Sprintf("plugins/%s.toml", pl.Name), "")
			ids = append(ids, pl.Name)
			continue
		}
		if _, ierr := installPluginInto(home, pl.Name, mpName); ierr != nil {
			io.warnf("skipping plugin %q from %q: %v", pl.Name, mpName, ierr)
			continue
		}
		io.item(fmt.Sprintf("plugins/%s.toml", pl.Name), "")
		ids = append(ids, pl.Name)
	}
	return ids, nil
}

// claudeSourceToAgentsync maps a native marketplace source (Claude's
// extraKnownMarketplaces `source` object) onto an agentsync marketplace Source
// plus the raw source string stored in marketplaces/<name>.toml. ok is false
// for source kinds agentsync cannot fetch (npm/hostPattern/unknown) or a
// well-formed kind missing its required field — the caller warns and skips.
func claudeSourceToAgentsync(s adapter.NativeSource) (src marketplace.Source, rawURL string, ok bool) {
	switch s.Type {
	case "github":
		raw := "github:" + s.Repo
		if s.Ref != "" {
			raw += "@" + s.Ref
		}
		return marketplace.Source{Kind: "github", Repo: s.Repo, Ref: s.Ref}, raw, s.Repo != ""
	case "git":
		return marketplace.Source{Kind: "url", URL: s.URL, Ref: s.Ref}, s.URL, s.URL != ""
	case "url":
		return marketplace.Source{Kind: "url", URL: s.URL}, s.URL, s.URL != ""
	case "file", "directory":
		return marketplace.Source{Relative: s.Path}, s.Path, s.Path != ""
	default:
		return marketplace.Source{}, "", false
	}
}
