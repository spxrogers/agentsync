package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/project"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/state"
	"github.com/tailscale/hujson"
)

func newStatusCmd() *cobra.Command {
	var (
		scopeFlag   string
		projectFlag string
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "report drift across registered agents",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home := paths.AgentsyncHome(paths.OSEnv{})
			// Load WITH the plugin cache so drift classification sees the
			// same plugin-projected components `apply` writes; source.Load
			// alone would report plugin-managed files/keys as untracked.
			pluginCacheRoot := filepath.Join(home, ".state", "cache", "plugins")
			userHome := paths.HomeDir(paths.OSEnv{})
			c, err := source.LoadWithCache(afero.NewOsFs(), home, pluginCacheRoot)
			if err != nil {
				return err
			}

			sc, projectRoot, err := resolveProjectScope(scopeFlag, projectFlag, c)
			if err != nil {
				return err
			}

			if sc == adapter.ScopeProject && projectRoot != "" {
				marker, merr := project.Discover(projectRoot)
				if merr != nil {
					return fmt.Errorf("load project marker: %w", merr)
				}
				if marker != nil {
					c = project.Merge(c, marker)
				}
			}

			statePath := filepath.Join(home, ".state", "targets.json")
			s, err := state.Load(statePath)
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
			if len(agents) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no agents enabled; run `agentsync agent add claude` (or opencode)")
				return nil
			}
			// apply WRITES (and RecordOpsState HASHES) the secret-RESOLVED
			// content, so status must hash the resolved source too — otherwise a
			// synced ${secret:…}/${env:…} item compares templated-vs-resolved and
			// classifies as phantom "pending" forever. Resolve like apply; fall
			// back to the templated render only when the backend is unavailable
			// (locked age key / CI), preserving offline status at the cost of the
			// pre-existing false-pending in that degraded mode. Resolved values
			// are only hashed here, never printed.
			rendered := secrets.ForRender(c)
			secBackend := secrets.SelectBackend(c.Config.Secrets, home, userHome)
			if resolved, serr := secrets.SubstituteCanonical(c, secBackend, secrets.EnvBackend{}); serr == nil {
				rendered = resolved
			}
			plan, err := render.Plan(rendered, reg, agents, sc, projectRoot, s, userHome)
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()
			for _, name := range reg.Names() {
				res, ok := plan.PerAgent[name]
				if !ok {
					continue
				}
				fmt.Fprintf(w, "[%s]\n", name)
				seen := map[string]bool{}
				// file-level: for each op, classify. Only key-merge ops are
				// handled key-by-key below; every other strategy (including
				// "replace", used by skills/subagents/commands/memory) is a
				// whole-file op and must be classified here. Skipping on any
				// non-empty MergeStrategy silently dropped all replace-strategy
				// files, so status reported no drift for them.
				for _, op := range res.Ops {
					if op.MergeStrategy == "merge-json-keys" || op.MergeStrategy == "merge-jsonc-keys" {
						continue // covered key-by-key below
					}
					if seen[op.Path] {
						continue
					}
					seen[op.Path] = true
					hsrc := hashContent(op.Content)
					happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, op.Path)].SHA256
					hdest := hashFile(op.Path)
					cls := drift.Classify(hsrc, happlied, hdest)
					fmt.Fprintf(w, "  %-20s %s\n", cls, op.Path)
				}
				// key-level: for each merge op, walk owned pointers
				for _, op := range res.Ops {
					if op.MergeStrategy != "merge-json-keys" && op.MergeStrategy != "merge-jsonc-keys" {
						continue
					}
					var ours map[string]any
					_ = json.Unmarshal(op.Content, &ours)
					final := readJSONFile(op.Path)
					for _, ptr := range render.CollectPointers(ours, "") {
						hsrc := hashAnyValue(getPointerValue(ours, ptr))
						happlied := s.Keys[stateKeyKey(userHome, name, sc, projectRoot, op.Path, ptr)].SHA256
						hdest := hashAnyValue(getPointerValue(final, ptr))
						cls := drift.Classify(hsrc, happlied, hdest)
						fmt.Fprintf(w, "  %-20s %s#%s\n", cls, op.Path, ptr)
					}
				}
				// orphans: whole-file dests this agent still owns in state but no
				// longer renders (the source component was removed). Without this,
				// status reported nothing for them — falsely "clean" — though the
				// file lingers and the next apply / a reconcile would act on it.
				for _, orphan := range render.OrphanFiles(s, userHome, name, sc, projectRoot, res.Ops) {
					happlied := s.Files[stateFileKey(userHome, name, sc, projectRoot, orphan)].SHA256
					cls := drift.Classify("", happlied, hashFile(orphan))
					fmt.Fprintf(w, "  %-20s %s\n", cls, orphan)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// stateFileKey builds the state Files map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath" (project and path are
// HOME-relative against the user's $HOME so keys are portable across
// machines — userHome must be paths.HomeDir, NOT the agentsync home).
func stateFileKey(userHome, agent string, sc adapter.Scope, projectRoot, path string) string {
	return fmt.Sprintf("%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path))
}

// stateKeyKey builds the state Keys map key matching render.RecordOpsState.
// Format: "agent:scope:portableProject:portablePath:ptr".
func stateKeyKey(userHome, agent string, sc adapter.Scope, projectRoot, path, ptr string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", agent, sc.String(),
		paths.HomeRelative(userHome, projectRoot), paths.HomeRelative(userHome, path), ptr)
}

func hashContent(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// hashFile returns the SHA-256 hex digest of the file at path. Returns
// the empty string on missing-file errors (which `drift.Classify` reads
// as "absent" — the expected signal for Orphan / OrphanDrifted).
//
// If the path is a symlink, hashFile returns a special marker so the
// drift classifier can flag the file as drifted in a way the user can
// act on. A managed file becoming a symlink (e.g. user replaced
// .claude.json with `ln -s /dev/null`) used to silently read through
// the link and compare hashes — making the swap invisible to status.
func hashFile(path string) string {
	info, lerr := os.Lstat(path)
	if lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		// Return a sentinel that will never match a content hash.
		// We don't include the link target to keep the sentinel stable
		// (target may resolve to whatever attacker chose); just signal
		// "this is a symlink now."
		return "symlink-not-regular-file"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return hashContent(data)
}

func hashAnyValue(v any) string {
	if v == nil {
		return ""
	}
	data, _ := json.Marshal(v)
	return hashContent(data)
}

// standardizeJSONC converts JSONC (comments, trailing commas) to plain JSON
// bytes so encoding/json can parse it. It mirrors how the adapters read these
// destination files (hujson.Parse + Standardize), keeping the drift/import
// read paths in agreement with the apply write path.
func standardizeJSONC(data []byte) ([]byte, error) {
	v, err := hujson.Parse(data)
	if err != nil {
		return nil, err
	}
	v.Standardize()
	return v.Pack(), nil
}

func readJSONFile(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	m := map[string]any{}
	// Accept JSONC: apply/ingest write and read these dests via hujson, so a
	// user may legitimately have `//` comments or trailing commas in
	// opencode.json. Reading them with plain encoding/json would fail and
	// yield an empty map, making drift classification (status/diff/reconcile)
	// report phantom conflicts for every owned pointer.
	if std, serr := standardizeJSONC(data); serr == nil {
		_ = json.Unmarshal(std, &m)
	}
	return m
}

func getPointerValue(m map[string]any, ptr string) any {
	if !strings.HasPrefix(ptr, "/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	var cur any = m
	for _, p := range parts {
		// Decode RFC 6901 escapes so a managed id containing '~' or '/'
		// (which CollectPointers escaped to ~0/~1) matches the real key.
		// Without this, status/diff looked up the literal escaped key, found
		// nothing, and reported phantom drift forever for that item.
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		mp, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mp[p]
	}
	return cur
}
