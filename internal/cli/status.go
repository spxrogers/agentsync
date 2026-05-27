package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"
	"github.com/spf13/cobra"
	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/drift"
	"github.com/spxrogers/agentsync/internal/paths"
	"github.com/spxrogers/agentsync/internal/render"
	"github.com/spxrogers/agentsync/internal/secrets"
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
			userHome := paths.HomeDir(paths.OSEnv{})
			c, sc, projectRoot, err := loadProjectedForScope(afero.NewOsFs(), home, scopeFlag, projectFlag, true)
			if err != nil {
				return err
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
				// Still surface orphaned state from a removed/disabled agent, so
				// removing the LAST agent doesn't hide its leftover native config.
				for _, a := range orphanedStateAgents(s, agents) {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: agent %q is not enabled but still owns tracked files/keys in state; its "+
							"native config is orphaned. Run `agentsync agent disable %s --purge` to remove what agentsync wrote.\n",
						a, a)
				}
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
					if render.IsKeyMerge(op.MergeStrategy) {
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
					if !render.IsKeyMerge(op.MergeStrategy) {
						continue
					}
					var ours map[string]any
					_ = json.Unmarshal(op.Content, &ours)
					final := readDestFile(op.MergeStrategy, op.Path)
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
			// Surface agents that still own tracked state (and thus orphaned
			// native config) but are no longer enabled — a removed, or
			// disabled-not-purged, agent. status/apply otherwise iterate only the
			// enabled set, so these would accumulate silently with no diagnostic.
			for _, a := range orphanedStateAgents(s, agents) {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: agent %q is not enabled but still owns tracked files/keys in state; its "+
						"native config is orphaned. Run `agentsync agent disable %s --purge` to remove what agentsync wrote.\n",
					a, a)
			}
			// Nudge: plugins installed natively in an enabled agent but not yet
			// declared in source. agentsync treats them as foreign-managed (never
			// drift), so this is informational — it points at `import`.
			undeclared := undeclaredNativePlugins(c, reg, agents)
			for _, name := range reg.Names() {
				missing := undeclared[name]
				if len(missing) == 0 {
					continue
				}
				fmt.Fprintf(cmd.ErrOrStderr(),
					"note: %d plugin(s) installed in %s are not in your source (%s); "+
						"run `agentsync import %s:plugin` to manage them.\n",
					len(missing), name, strings.Join(missing, ", "), name)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scopeFlag, "scope", "", "user | project (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "explicit path to project root (implies --scope project)")
	return cmd
}

// orphanedStateAgents returns, sorted, the agent names that appear as a state
// key prefix ("agent:scope:…") but are not in the enabled set — i.e. agents
// whose rendered native config and state entries linger after a `remove` or a
// `disable` without `--purge`.
func orphanedStateAgents(s *state.Targets, enabled []string) []string {
	en := make(map[string]bool, len(enabled))
	for _, n := range enabled {
		en[n] = true
	}
	found := map[string]bool{}
	collect := func(key string) {
		if i := strings.IndexByte(key, ':'); i > 0 {
			if a := key[:i]; !en[a] {
				found[a] = true
			}
		}
	}
	for k := range s.Files {
		collect(k)
	}
	for k := range s.Keys {
		collect(k)
	}
	out := make([]string, 0, len(found))
	for a := range found {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
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
