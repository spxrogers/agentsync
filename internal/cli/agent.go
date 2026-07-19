package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/generic"
	"github.com/spxrogers/agentsync/internal/iox"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/state"
)

// deepAdapterNames are the hand-written, agent-specific adapters (richer than the
// breadth-tier generic specs). The full valid-agent set is these plus every
// generic.Specs() entry — see allAgentNames.
var deepAdapterNames = []string{
	"claude", "opencode", "codex", "cursor", "gemini", "continue", "windsurf", "roo", "cline",
}

// deepAgentBinaries maps a deep adapter to the executable agentsync looks for on
// PATH at `agent add` time (to warn when it's not installed). Breadth-tier agents
// carry their binary in their Spec (see agentBinary).
var deepAgentBinaries = map[string]string{
	"claude":   "claude",
	"opencode": "opencode",
	"codex":    "codex",
	"cursor":   "cursor",
	"gemini":   "gemini",
	"continue": "cn",
	"windsurf": "windsurf",
	"roo":      "roo",
	"cline":    "cline",
}

// allAgentNames returns every valid agent name — deep adapters plus breadth-tier
// generic specs — sorted. Single source of truth for agent-name validation.
func allAgentNames() []string {
	names := append([]string(nil), deepAdapterNames...)
	for _, s := range generic.Specs() {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// isValidAgent reports whether name is a known agent (deep or breadth-tier).
func isValidAgent(name string) bool {
	for _, n := range allAgentNames() {
		if n == name {
			return true
		}
	}
	return false
}

// validAgentsList is the comma-joined valid-agent set for error messages.
func validAgentsList() string { return strings.Join(allAgentNames(), ", ") }

// agentBinary returns the PATH executable to probe for name, or "" if none.
func agentBinary(name string) string {
	if b, ok := deepAgentBinaries[name]; ok {
		return b
	}
	for _, s := range generic.Specs() {
		if s.Name == name {
			return s.DetectBin
		}
	}
	return ""
}

// v1Supported reports whether name has a real (non-noop) adapter. Every valid
// agent does today, so this is a guard for a hypothetical future noop-registered
// agent (overridable with AGENTSYNC_ALLOW_UNIMPLEMENTED=1).
func v1Supported(name string) bool { return isValidAgent(name) }

// boolStr returns "true" or "false" as a string.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "agent", Short: "manage which agents agentsync targets"}
	cmd.AddCommand(
		newAgentAddCmd(),
		newAgentRemoveCmd(),
		newAgentListCmd(),
		newAgentEnableCmd(),
		newAgentDisableCmd(),
	)
	return cmd
}

func newAgentAddCmd() *cobra.Command {
	var scopeFlag, projectFlag string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "register an agent (any supported agent; see `agent list --all`)",
		Args:  cobra.ExactArgs(1),
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			return agentAddRun(cmd, args, scopeFlag, projectFlag)
		}),
	}
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

func newAgentRemoveCmd() *cobra.Command {
	var scopeFlag, projectFlag string
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "unregister an agent",
		Args:  cobra.ExactArgs(1),
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			return agentRemoveRun(cmd, args, scopeFlag, projectFlag)
		}),
	}
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

func newAgentEnableCmd() *cobra.Command {
	var scopeFlag, projectFlag string
	cmd := &cobra.Command{
		Use:   "enable <name>",
		Args:  cobra.ExactArgs(1),
		Short: "enable a registered agent",
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			return agentEnableRun(cmd, args, scopeFlag, projectFlag)
		}),
	}
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

func newAgentDisableCmd() *cobra.Command {
	var (
		purge       bool
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "disable <name>",
		Args:  cobra.ExactArgs(1),
		Short: "disable a registered agent (optionally purging its destination files)",
		RunE: lockedRun(func(cmd *cobra.Command, args []string) error {
			return agentDisableRun(cmd, args, purge, scopeFlag, projectFlag)
		}),
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "remove agent destination files that agentsync owns")
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

type agentsyncCfg struct {
	Agents map[string]map[string]any `toml:"agents"`
	// other top-level keys preserved verbatim via decoder
}

// validateAgent reports whether name is a recognized adapter (deep or
// breadth-tier). Adding a new agent is a code change (a new package or a
// generic.Specs() entry), not a config change.
func validateAgent(name string) error {
	if isValidAgent(name) {
		return nil
	}
	return fmt.Errorf("unknown agent %q; valid: %s", name, validAgentsList())
}

// agentScopeHome resolves the effective scope for an agent command and the
// agentsync home whose agentsync.toml it edits: ~/.agentsync at user scope, the
// <root>/.agentsync project tree at project scope. Scope resolution is shared
// with apply/status/diff (resolveScope), so the same explicit-opt-in rules hold:
// --project implies project scope, --scope project walks up from cwd, and a bare
// invocation inside a project tree prompts (or fails closed when non-interactive).
func agentScopeHome(cmd *cobra.Command, scopeFlag, projectFlag string) (home string, sc adapter.Scope, projectRoot string, err error) {
	sc, projectRoot, err = resolveScope(cmd, scopeFlag, projectFlag, noInputFlag(cmd))
	if err != nil {
		return "", sc, projectRoot, err
	}
	if sc == adapter.ScopeProject {
		return project.Home(projectRoot), sc, projectRoot, nil
	}
	return paths.AgentsyncHome(paths.OSEnv{}), sc, "", nil
}

// readAgentsyncTOMLAt returns the file path + raw bytes + parsed `agents`
// section of the agentsync.toml under home. The missing-file hint points at the
// scope-appropriate init.
func readAgentsyncTOMLAt(home string, sc adapter.Scope) (string, []byte, map[string]map[string]any, error) {
	p := filepath.Join(home, "agentsync.toml")
	raw, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			hint := "run `agentsync init` first"
			if sc == adapter.ScopeProject {
				hint = "run `agentsync init --scope project` first"
			}
			return p, nil, nil, fmt.Errorf("no agentsync config at %s; %s", p, hint)
		}
		return p, nil, nil, fmt.Errorf("read %s: %w", p, err)
	}
	var cfg agentsyncCfg
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return p, raw, nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]map[string]any{}
	}
	return p, raw, cfg.Agents, nil
}

// buildAgentsSection returns a TOML [agents] block with inline table values,
// preserving comment header context. Agents are sorted alphabetically. It
// emits ONLY the fields the canonical model reads (enabled + scope) — any
// extra key a user hand-added inside an agent entry is deliberately dropped,
// matching source.Agent, which never loads such keys either.
func buildAgentsSection(agents map[string]map[string]any) string {
	names := make([]string, 0, len(agents))
	for n := range agents {
		names = append(names, n)
	}
	sort.Strings(names)

	var sb strings.Builder
	sb.WriteString("[agents]\n")
	for _, n := range names {
		v := agents[n]
		enabled, _ := v["enabled"].(bool)
		// The scope field is omitted when empty: project-scope entries carry no
		// scope key (the file's location already IS the scope), and regenerating
		// `scope = ""` for them would add noise the loader never reads.
		if scope, _ := v["scope"].(string); scope != "" {
			fmt.Fprintf(&sb, "%s = { enabled = %s, scope = %q }\n", n, boolStr(enabled), scope)
		} else {
			fmt.Fprintf(&sb, "%s = { enabled = %s }\n", n, boolStr(enabled))
		}
	}
	return sb.String()
}

func writeAgents(p string, raw []byte, agents map[string]map[string]any) error {
	// Round-trip: regenerate the [agents] block in inline-table format, then
	// splice it back in preserving everything OUTSIDE the agents config.
	//
	// We work line-by-line and drop every agents-owned line — both the inline
	// `[agents]` header form AND the idiomatic `[agents.<name>]` sub-table form
	// — then reinsert the regenerated block where the first agents section was.
	// The old string-search for a literal "[agents]" missed the sub-table form
	// entirely: it appended a second [agents] block while leaving the
	// sub-tables, defining agents.<name> twice and bricking the config (go-toml
	// rejects the duplicate on the next load).
	newSection := strings.TrimRight(buildAgentsSection(agents), "\n")
	newLines := strings.Split(newSection, "\n")

	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines)+len(newLines))
	insertAt := -1
	inAgents := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			// New section header: agents-owned iff it's [agents] or [agents.*].
			inAgents = trimmed == "[agents]" || strings.HasPrefix(trimmed, "[agents.")
			if inAgents {
				if insertAt < 0 {
					insertAt = len(out) // first agents section → reinsert here
				}
				continue // drop the header
			}
		}
		if inAgents {
			continue // drop lines inside an agents section
		}
		out = append(out, line)
	}

	if insertAt < 0 {
		// No agents section existed; append after a blank-line separator.
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, newLines...)
	} else {
		tail := append([]string(nil), out[insertAt:]...)
		out = append(out[:insertAt], newLines...)
		// Keep a blank line between the regenerated [agents] block and whatever
		// section follows, so repeated agent edits don't collapse the file's
		// section spacing. The loop above drops blank lines inside the agents
		// section, so a single separator is re-added each run (no accumulation).
		if len(tail) > 0 && strings.TrimSpace(tail[0]) != "" {
			out = append(out, "")
		}
		out = append(out, tail...)
	}

	// Fail-closed backstop: the splicer above is line-based, not a TOML parser,
	// so a construct it cannot see (e.g. a multi-line string whose CONTENT
	// contains an "[agents]" line) could corrupt the file — either bricking
	// every subsequent load, or worse, silently mangling ANOTHER table's data
	// while still parsing. Before writing, re-parse the spliced result and
	// require BOTH that the agents table matches the intended entries AND that
	// everything outside [agents] is semantically unchanged from the original;
	// refuse the write (leaving the on-disk file untouched) otherwise.
	content := []byte(strings.Join(out, "\n"))
	var check agentsyncCfg
	if err := toml.Unmarshal(content, &check); err != nil {
		return fmt.Errorf("refusing to rewrite %s: the regenerated config does not parse (%v); "+
			"the file likely uses a TOML construct the [agents] rewriter cannot splice "+
			"(e.g. a multi-line string containing an \"[agents]\" line) — edit the [agents] table by hand", p, err)
	}
	if !agentsTablesEqual(check.Agents, agents) {
		return fmt.Errorf("refusing to rewrite %s: the regenerated [agents] table does not round-trip; "+
			"edit the [agents] table by hand", p)
	}
	same, err := nonAgentsUnchanged(raw, content)
	if err != nil {
		return fmt.Errorf("refusing to rewrite %s: %w; edit the [agents] table by hand", p, err)
	}
	if !same {
		return fmt.Errorf("refusing to rewrite %s: the rewrite would alter content outside the [agents] table "+
			"(the file likely uses a TOML construct the rewriter cannot splice, e.g. a multi-line string "+
			"containing an \"[agents]\" line) — edit the [agents] table by hand", p)
	}
	return iox.AtomicWrite(p, content, 0o644)
}

// nonAgentsUnchanged reports whether every table EXCEPT [agents] decodes to
// the same value in the original and the spliced config. Comments and
// whitespace are free to differ; data outside the section the rewriter owns is
// not — a splice that still parses but changed another table's values is
// silent corruption and must be refused.
func nonAgentsUnchanged(oldRaw, newRaw []byte) (bool, error) {
	var oldDoc, newDoc map[string]any
	if err := toml.Unmarshal(oldRaw, &oldDoc); err != nil {
		return false, fmt.Errorf("re-parse original config: %w", err)
	}
	if err := toml.Unmarshal(newRaw, &newDoc); err != nil {
		return false, fmt.Errorf("re-parse regenerated config: %w", err)
	}
	delete(oldDoc, "agents")
	delete(newDoc, "agents")
	return reflect.DeepEqual(oldDoc, newDoc), nil
}

// agentsTablesEqual reports whether a re-parsed agents table carries exactly
// the intended entries (same names, enabled bits, and scope markers). It is
// the writeAgents backstop's oracle, so it compares only the fields
// buildAgentsSection emits.
func agentsTablesEqual(got, want map[string]map[string]any) bool {
	if len(got) != len(want) {
		return false
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			return false
		}
		ge, _ := g["enabled"].(bool)
		we, _ := w["enabled"].(bool)
		gs, _ := g["scope"].(string)
		ws, _ := w["scope"].(string)
		if ge != we || gs != ws {
			return false
		}
	}
	return true
}

func agentAddRun(cmd *cobra.Command, args []string, scopeFlag, projectFlag string) error {
	name := args[0]
	if err := validateAgent(name); err != nil {
		return err
	}
	if !v1Supported(name) && os.Getenv("AGENTSYNC_ALLOW_UNIMPLEMENTED") != "1" {
		return fmt.Errorf("agent %q has no implemented adapter yet; "+
			"set AGENTSYNC_ALLOW_UNIMPLEMENTED=1 to register anyway and accept a no-op apply", name)
	}
	home, sc, _, err := agentScopeHome(cmd, scopeFlag, projectFlag)
	if err != nil {
		return err
	}
	p, raw, agents, err := readAgentsyncTOMLAt(home, sc)
	if err != nil {
		return err
	}
	if _, ok := agents[name]; ok {
		fmt.Fprintf(cmd.OutOrStdout(), "agent %s already registered\n", name)
		return nil
	}
	entry := map[string]any{"enabled": true}
	if sc != adapter.ScopeProject {
		// The user-scope entry keeps its historical scope marker; project-scope
		// entries carry none — the project file's location already is the scope.
		entry["scope"] = "user"
	}
	agents[name] = entry
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "added agent: %s\n", name)

	// Warn (don't fail) if the agent's binary is not on PATH. The user
	// may be authoring config on machine A to apply on machine B, so we
	// don't want a hard reject — but a silent success when Claude Code
	// isn't installed leaves them debugging "why didn't apply do
	// anything?" later.
	if bin := agentBinary(name); bin != "" {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: %s binary not found on PATH; "+
					"agentsync will still write config to its destination dirs "+
					"but %s itself must be installed to read it\n",
				bin, name)
		}
	}
	return nil
}

func agentRemoveRun(cmd *cobra.Command, args []string, scopeFlag, projectFlag string) error {
	name := args[0]
	home, sc, _, err := agentScopeHome(cmd, scopeFlag, projectFlag)
	if err != nil {
		return err
	}
	p, raw, agents, err := readAgentsyncTOMLAt(home, sc)
	if err != nil {
		return err
	}
	if _, ok := agents[name]; !ok {
		fmt.Fprintf(cmd.OutOrStdout(), "agent %s not registered\n", name)
		return nil
	}
	delete(agents, name)
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "removed agent: %s\n", name)
	return nil
}

func newAgentListCmd() *cobra.Command {
	var (
		all         bool
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "list registered agents (--all to list every supported agent)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return agentListRun(cmd, all, scopeFlag, projectFlag)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "list every agent agentsync supports, marking which are registered")
	addScopeFlags(cmd, &scopeFlag, &projectFlag)
	return cmd
}

// agentEntrySuffix renders the trailing detail of one `agent list` line. The
// scope field is shown only when the entry carries one (project-scope entries
// don't — the file's location is the scope).
func agentEntrySuffix(v map[string]any) string {
	enabled, _ := v["enabled"].(bool)
	if scope, _ := v["scope"].(string); scope != "" {
		return fmt.Sprintf("enabled=%t scope=%s", enabled, scope)
	}
	return fmt.Sprintf("enabled=%t", enabled)
}

func agentListRun(cmd *cobra.Command, all bool, scopeFlag, projectFlag string) error {
	home, sc, _, err := agentScopeHome(cmd, scopeFlag, projectFlag)
	if err != nil {
		return err
	}
	_, _, agents, err := readAgentsyncTOMLAt(home, sc)
	if err != nil {
		return err
	}
	if all {
		// The full supported set (deep adapters + breadth-tier specs), with
		// each agent's registration state — the discovery surface `agent add`
		// points at.
		for _, n := range allAgentNames() {
			if v, ok := agents[n]; ok {
				fmt.Fprintf(cmd.OutOrStdout(), "%-12s registered %s\n", n, agentEntrySuffix(v))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%-12s available\n", n)
			}
		}
		return nil
	}
	names := make([]string, 0, len(agents))
	for n := range agents {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		if sc == adapter.ScopeProject {
			fmt.Fprintln(cmd.OutOrStdout(), "(no agents declared for this project; project scope renders only to declared agents — try: agentsync agent add claude --scope project)")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "(no agents registered; try: agentsync agent add claude, or agentsync agent list --all)")
		}
		return nil
	}
	for _, n := range names {
		fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", n, agentEntrySuffix(agents[n]))
	}
	return nil
}

// agentRegisterHint is the scope-appropriate "register it first" suffix for
// enable/disable errors on an unregistered agent.
func agentRegisterHint(name string, sc adapter.Scope) string {
	if sc == adapter.ScopeProject {
		return fmt.Sprintf("use 'agentsync agent add %s --scope project' first", name)
	}
	return fmt.Sprintf("use 'agentsync agent add %s' first", name)
}

func agentEnableRun(cmd *cobra.Command, args []string, scopeFlag, projectFlag string) error {
	name := args[0]
	home, sc, _, err := agentScopeHome(cmd, scopeFlag, projectFlag)
	if err != nil {
		return err
	}
	p, raw, agents, err := readAgentsyncTOMLAt(home, sc)
	if err != nil {
		return err
	}
	v, ok := agents[name]
	if !ok {
		return fmt.Errorf("agent %q not registered; %s", name, agentRegisterHint(name, sc))
	}
	v["enabled"] = true
	agents[name] = v
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "enabled agent: %s\n", name)
	return nil
}

func agentDisableRun(cmd *cobra.Command, args []string, purge bool, scopeFlag, projectFlag string) error {
	name := args[0]
	cfgHome, sc, projectRoot, err := agentScopeHome(cmd, scopeFlag, projectFlag)
	if err != nil {
		return err
	}
	p, raw, agents, err := readAgentsyncTOMLAt(cfgHome, sc)
	if err != nil {
		return err
	}
	// State (and therefore purge bookkeeping) always lives in the USER agentsync
	// home — project trees carry no .state/ (apply records project-scope state
	// centrally, keyed by project root).
	home := paths.AgentsyncHome(paths.OSEnv{})
	v, ok := agents[name]
	if !ok {
		// Not registered. A removed agent can still own orphaned destination
		// files + state keys; `--purge` is the reachable cleanup path for it,
		// working purely from state. Still reject a name that was never a valid
		// agent, so `disable bogus --purge` doesn't report a misleading success.
		if purge {
			if err := validateAgent(name); err != nil {
				return err
			}
			return purgeAgentDests(cmd, name, home, sc, projectRoot)
		}
		return fmt.Errorf("agent %q not registered (pass --purge to clean up an already-removed agent's leftover files)", name)
	}
	v["enabled"] = false
	agents[name] = v
	if err := writeAgents(p, raw, agents); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "disabled agent: %s\n", name)

	if !purge {
		return nil
	}

	// The whole disable command (including this purge) already runs under the
	// global lock via lockedRun, so call purge directly — re-acquiring here
	// would deadlock on the re-entrant flock.
	return purgeAgentDests(cmd, name, home, sc, projectRoot)
}

// purgeKeyRest reports whether a state key ("agent:scope:project:path[:ptr]")
// belongs to the purge set and, if so, returns the key's remainder after the
// dest-identifying prefix (the path, or "path:ptr" for a Keys entry). At user
// scope the purge keeps its historical cleanup-everything semantics: every
// entry of the named agent, across all scopes and projects. At project scope it
// matches only the agent's entries for THAT project root — purging a project
// must not delete the user's machine-wide destinations or another repo's
// rendered files. The project-scope match compares the full literal prefix
// (like render.PruneStaleState's key prefixes) instead of colon-split fields:
// a portable project root is not guaranteed colon-free (a root outside $HOME
// stays an absolute path), and field-splitting would silently mismatch it.
func purgeKeyRest(key, agent string, sc adapter.Scope, portableProject string) (rest string, ok bool) {
	if sc == adapter.ScopeProject {
		prefix := agent + ":" + adapter.ScopeProject.String() + ":" + portableProject + ":"
		if !strings.HasPrefix(key, prefix) {
			return "", false
		}
		return key[len(prefix):], true
	}
	if !strings.HasPrefix(key, agent+":") {
		return "", false
	}
	// RATIFIED POLICY (issue #171, decision confirmed): a USER-scope `--purge` is
	// intentionally cross-scope — it is the machine-wide "remove everything this
	// agent ever wrote" affordance, so it spans every scope:project pair for the
	// agent (including project-scope dest files in checked-out repos). It matches by
	// the `agent:` prefix and takes the 4th colon field as the remainder. That is
	// exact for user-scope keys (empty project segment) and colon-free project
	// roots; a colon-bearing root mangles it — an accepted residual, safe because
	// otherFilesKeyPath mangles identically, keeping the shared-file guard aligned.
	// (Project-scope `--purge --scope project` stays isolated to that repo, above.)
	parts := strings.SplitN(key, ":", 4)
	if len(parts) < 4 {
		return "", false
	}
	return parts[3], true
}

// otherFilesKeyPath extracts the dest path from a Files key that is NOT part
// of the purge set, for the shared-file guard. A key whose project segment
// matches the purge's own portableProject is stripped colon-safely by literal
// prefix — a co-owned dest under the same project root is exactly the case the
// guard protects, and the root is not guaranteed colon-free. Every other key
// falls back to the 4th colon field: user-scope keys carry an empty (colon-
// free) project segment there, and a DIFFERENT project's dest can never
// collide with this purge's paths, so a mangled extraction cannot cause a
// false unprotected delete (it can only fail to match, which is the status
// quo for keys that were never candidates).
func otherFilesKeyPath(key, portableProject string) string {
	if i := strings.Index(key, ":"); i >= 0 {
		marker := ":" + adapter.ScopeProject.String() + ":" + portableProject + ":"
		if tail := key[i:]; strings.HasPrefix(tail, marker) {
			return tail[len(marker):]
		}
	}
	parts := strings.SplitN(key, ":", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

// purgeAgentDests deletes the destination files owned solely by the named
// agent (at project scope: only within that project) and removes the matching
// state entries. Must be called under withGlobalLock.
func purgeAgentDests(cmd *cobra.Command, name, home string, sc adapter.Scope, projectRoot string) error {
	// State keys store dest paths HOME-relative ("${HOME}/.claude.json").
	// Expand them back to absolute via the user's $HOME before deleting —
	// otherwise os.Remove would target the literal "${HOME}/..." string,
	// silently delete nothing, yet still report "purged N path(s)".
	userHome := paths.HomeDir(paths.OSEnv{})
	statePath := filepath.Join(home, ".state", "targets.json")
	s, err := state.Load(statePath)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}

	// The project segment of state keys is HOME-relative (matching
	// render.RecordOpsState), so portabilize before comparing.
	portableProject := paths.HomeRelative(userHome, projectRoot)

	// File-owned dests (whole-file replace ops, e.g. ~/.claude/skills/<n>/
	// SKILL.md): the whole file is agentsync's, so a whole-file delete is
	// correct — unless another agent still owns the same shared file. The
	// dest path is field index 3 in a Files key ("agent:scope:project:path").
	purgedFilePaths := map[string]bool{}
	otherFilePaths := map[string]bool{}
	for key := range s.Files {
		if path, ok := purgeKeyRest(key, name, sc, portableProject); ok {
			if path != "" {
				purgedFilePaths[path] = true
			}
			continue
		}
		// Not ours: record the path so shared files stay protected below.
		if path := otherFilesKeyPath(key, portableProject); path != "" {
			otherFilePaths[path] = true
		}
	}

	// Key-owned dests (merge-key pointers within a possibly-shared file, e.g.
	// /mcpServers/<id> in ~/.claude.json): agentsync owns only those pointers,
	// NOT the whole file. A whole-file delete here would destroy the user's
	// foreign keys (other servers, top-level settings) with no backup. Collect
	// this agent's owned pointers per path so we can remove ONLY them.
	purgedKeyPtrs := map[string][]string{}
	for key := range s.Keys {
		rest, ok := purgeKeyRest(key, name, sc, portableProject)
		if !ok {
			continue
		}
		// rest is "path:ptr" for a Keys entry.
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) < 2 || parts[0] == "" {
			continue
		}
		purgedKeyPtrs[parts[0]] = append(purgedKeyPtrs[parts[0]], parts[1])
	}

	reg := registryFactory()
	a := reg.Lookup(name)
	deletedFiles := 0
	prunedFiles := 0
	sharedKept := 0
	if a != nil {
		var ops []adapter.FileOp
		// Whole-file deletes for file-owned dests not shared with another
		// agent. claude and opencode both render skills to the same path, so
		// deleting a shared one while purging one agent would destroy a file
		// the other still needs — Writer.Delete skips backup, so the loss is
		// unrecoverable. Keep those.
		for p := range purgedFilePaths {
			if otherFilePaths[p] {
				sharedKept++
				continue
			}
			ops = append(ops, adapter.FileOp{Action: "delete", Path: paths.FromHomeRelative(userHome, p)})
			deletedFiles++
		}
		// Pointer prunes for key-owned dests: an empty merge op carrying only
		// this agent's owned pointers, so MergeKeys removes exactly those and
		// preserves the user's (and any other agent's) keys in the shared file.
		if strat := a.KeyMergeStrategy(); strat != "" {
			for p, ptrs := range purgedKeyPtrs {
				abs := paths.FromHomeRelative(userHome, p)
				if _, err := os.Stat(abs); err != nil {
					continue // already gone; nothing to prune
				}
				ops = append(ops, adapter.FileOp{
					Action:        "write",
					Path:          abs,
					Content:       []byte("{}"),
					Mode:          0o644,
					MergeStrategy: strat,
					OwnedKeys:     ptrs,
				})
				prunedFiles++
			}
		}
		rw := render.NewWriter(s, home, userHome, sc, projectRoot, name)
		if err := a.Apply(ops, rw); err != nil {
			return fmt.Errorf("purge apply %s: %w", name, err)
		}
	}

	// Remove the matching state entries for this agent.
	for key := range s.Files {
		if _, ok := purgeKeyRest(key, name, sc, portableProject); ok {
			delete(s.Files, key)
		}
	}
	for key := range s.Keys {
		if _, ok := purgeKeyRest(key, name, sc, portableProject); ok {
			delete(s.Keys, key)
		}
	}
	if err := state.Save(statePath, s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "purged agent %s: deleted %d file(s), pruned owned keys from %d shared file(s)\n", name, deletedFiles, prunedFiles)
	if sharedKept > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  kept %d shared path(s) still owned by another agent\n", sharedKept)
	}
	return nil
}
