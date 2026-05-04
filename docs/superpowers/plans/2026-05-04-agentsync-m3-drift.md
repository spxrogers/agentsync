# agentsync M3 — Drift, status, diff, reconcile

> Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md). Builds on M0/M1/M2.

**Goal:** 3-way hash classifier + commands `status`, `diff`, `reconcile`. File-level + JSON-pointer key-level. Foreign-key reporting. Bulk hotkeys + `--auto-*` flags.

**Architecture:** New `internal/drift` package houses the classifier (pure function). `internal/cli/{status,diff,reconcile}.go` wire CLI surface. State is updated only on successful apply or successful reconcile-write-back; a successful reconcile-override re-applies via existing render pipeline.

**Tech stack:** stdlib only for classifier. `charmbracelet/huh` for the reconcile prompt loop (lightweight; v1 only uses Confirm + simple character input).

---

## Files

```
NEW:
internal/drift/{classifier.go, classifier_test.go}
internal/cli/{status.go, status_test.go, diff.go, diff_test.go, reconcile.go, reconcile_test.go}
internal/render/state_apply.go     # applies state updates after Apply

MODIFIED:
internal/cli/apply.go              # wire state updates post-apply
internal/cli/root.go               # AddCommand status/diff/reconcile
internal/render/pipeline.go        # populate FileOp.OwnedKeys from state
```

---

## Task 1: Drift classifier

**Files:** `internal/drift/{classifier.go, classifier_test.go}`

Pure function: given (H_src, H_applied, H_dest) → Class.

- [ ] **Test (table-driven covering all 9 cases)**

```go
package drift_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/drift"
)

func TestClassify(t *testing.T) {
    type tc struct {
        name                                string
        hsrc, happlied, hdest               string // "" = nil
        want                                drift.Class
    }
    cases := []tc{
        {name: "clean", hsrc: "a", happlied: "a", hdest: "a", want: drift.Clean},
        {name: "pending", hsrc: "b", happlied: "a", hdest: "a", want: drift.Pending},
        {name: "drift", hsrc: "a", happlied: "a", hdest: "b", want: drift.Drift},
        {name: "converged", hsrc: "b", happlied: "a", hdest: "b", want: drift.Converged},
        {name: "conflict", hsrc: "b", happlied: "a", hdest: "c", want: drift.Conflict},
        {name: "new", hsrc: "a", happlied: "", hdest: "", want: drift.New},
        {name: "foreign-collision", hsrc: "a", happlied: "", hdest: "x", want: drift.ForeignCollision},
        {name: "orphan", hsrc: "", happlied: "a", hdest: "a", want: drift.Orphan},
        {name: "orphan-drifted", hsrc: "", happlied: "a", hdest: "b", want: drift.OrphanDrifted},
    }
    for _, c := range cases {
        t.Run(c.name, func(t *testing.T) {
            got := drift.Classify(c.hsrc, c.happlied, c.hdest)
            if got != c.want {
                t.Fatalf("Classify(%q,%q,%q) = %v, want %v", c.hsrc, c.happlied, c.hdest, got, c.want)
            }
        })
    }
}
```

- [ ] **Implement**

`internal/drift/classifier.go`:

```go
// Package drift classifies (source, applied, destination) hash triples per the
// 9-case table in the agentsync design spec. Pure function, no IO.
package drift

type Class int

const (
    Clean Class = iota
    Pending
    Drift
    Converged
    Conflict
    New
    ForeignCollision
    Orphan
    OrphanDrifted
)

func (c Class) String() string {
    switch c {
    case Clean:
        return "clean"
    case Pending:
        return "pending"
    case Drift:
        return "drift"
    case Converged:
        return "converged"
    case Conflict:
        return "conflict"
    case New:
        return "new"
    case ForeignCollision:
        return "foreign-collision"
    case Orphan:
        return "orphan"
    case OrphanDrifted:
        return "orphan-drifted"
    }
    return "unknown"
}

// Classify returns the case for one tracked item. Empty string means "absent."
// hsrc=src hash now; happlied=last-applied hash; hdest=on-disk hash now.
func Classify(hsrc, happlied, hdest string) Class {
    switch {
    case happlied == "" && hdest == "" && hsrc != "":
        return New
    case happlied == "" && hdest != "" && hsrc != "":
        return ForeignCollision
    case happlied != "" && hsrc == "":
        if hdest == happlied {
            return Orphan
        }
        return OrphanDrifted
    case hsrc == happlied && hdest == happlied:
        return Clean
    case hsrc != happlied && hdest == happlied:
        return Pending
    case hsrc == happlied && hdest != happlied:
        return Drift
    case hsrc != happlied && hdest != happlied && hsrc == hdest:
        return Converged
    default:
        return Conflict
    }
}

// SafeForAutoApply returns true for cases apply can resolve without prompting.
func SafeForAutoApply(c Class) bool {
    return c == Clean || c == Pending || c == New || c == Converged
}
```

Commit:

```bash
git commit -am "feat(drift): 9-case 3-way hash classifier (pure function)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Wire state writes post-Apply

**Files:** `internal/render/state_apply.go`, modify `internal/render/pipeline.go`, `internal/cli/apply.go`

After every successful Apply, opensync must:
1. For each `write` FileOp: hash final on-disk content + record in `state.Files[<key>]`.
2. For each `merge-json-keys`/`merge-jsonc-keys` op: parse final on-disk JSON, for each pointer that came from `ours`, hash that subtree + record in `state.Keys[<key>]`.

- [ ] **Test**

```go
package render_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
    "github.com/spxrogers/agentsync/internal/render"
    "github.com/spxrogers/agentsync/internal/state"
)

func TestRecordState_FilesAndKeys(t *testing.T) {
    dir := t.TempDir()
    p := filepath.Join(dir, ".claude.json")
    _ = os.WriteFile(p, []byte(`{"mcpServers":{"github":{"command":"npx"}},"foreign":{}}`), 0o644)

    s := state.New()
    err := render.RecordOpsState(s, "claude", adapter.ScopeUser, "", []adapter.FileOp{{
        Action:        "write",
        Path:          p,
        MergeStrategy: "merge-json-keys",
        Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
        SourceID:      "mcp/github.toml",
    }})
    if err != nil {
        t.Fatal(err)
    }

    // Expect a key entry for /mcpServers/github
    var found bool
    for k := range s.Keys {
        if k == "claude:user::"+p+":/mcpServers/github" {
            found = true
        }
    }
    if !found {
        t.Fatalf("missing key entry; have: %+v", s.Keys)
    }
    _ = json.RawMessage{}
    _ = noop.New
}
```

- [ ] **Implement**

`internal/render/state_apply.go`:

```go
package render

import (
    "crypto/sha256"
    "encoding/hex"
    "encoding/json"
    "fmt"
    "os"
    "time"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/state"
)

// RecordOpsState updates s with hashes for files and keys produced by ops.
// Caller is expected to call this AFTER a successful Apply.
func RecordOpsState(s *state.Targets, agent string, scope adapter.Scope, project string, ops []adapter.FileOp) error {
    now := time.Now().UTC()
    for _, op := range ops {
        if op.Action != "" && op.Action != "write" {
            continue
        }
        switch op.MergeStrategy {
        case "merge-json-keys", "merge-jsonc-keys":
            // Re-read final on-disk content and record per pointer
            data, err := os.ReadFile(op.Path)
            if err != nil {
                return fmt.Errorf("read post-apply %s: %w", op.Path, err)
            }
            var final map[string]any
            if err := json.Unmarshal(data, &final); err != nil {
                return fmt.Errorf("parse post-apply %s: %w", op.Path, err)
            }
            var ours map[string]any
            if err := json.Unmarshal(op.Content, &ours); err != nil {
                return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
            }
            for _, ptr := range collectPointers(ours, "") {
                v := getPointer(final, ptr)
                hash := hashAny(v)
                key := fmt.Sprintf("%s:%s:%s:%s:%s", agent, scope.String(), project, op.Path, ptr)
                s.Keys[key] = state.KeyEntry{
                    SHA256:    hash,
                    AppliedAt: now,
                    SourceID:  op.SourceID,
                }
            }
        default:
            data, err := os.ReadFile(op.Path)
            if err != nil {
                return fmt.Errorf("read post-apply %s: %w", op.Path, err)
            }
            sum := sha256.Sum256(data)
            key := fmt.Sprintf("%s:%s:%s:%s", agent, scope.String(), project, op.Path)
            s.Files[key] = state.FileEntry{
                SHA256:    hex.EncodeToString(sum[:]),
                Mode:      op.Mode,
                AppliedAt: now,
                SourceID:  op.SourceID,
            }
        }
    }
    return nil
}

// collectPointers walks m and returns JSON pointers for every leaf-or-object
// at the second level (e.g. /mcpServers/github -> stop). agentsync owns at
// the second-level granularity; deeper edits fall under that key's value
// hash.
func collectPointers(m map[string]any, prefix string) []string {
    var out []string
    for k, v := range m {
        ptr := prefix + "/" + escapeJSONPointer(k)
        switch vv := v.(type) {
        case map[string]any:
            // Drill one level: each child key becomes a pointer.
            for kk := range vv {
                out = append(out, ptr+"/"+escapeJSONPointer(kk))
            }
        default:
            out = append(out, ptr)
        }
    }
    return out
}

func escapeJSONPointer(s string) string {
    s = replaceAll(s, "~", "~0")
    s = replaceAll(s, "/", "~1")
    return s
}

func replaceAll(s, from, to string) string {
    out := make([]byte, 0, len(s))
    for i := 0; i < len(s); {
        if i+len(from) <= len(s) && s[i:i+len(from)] == from {
            out = append(out, to...)
            i += len(from)
            continue
        }
        out = append(out, s[i])
        i++
    }
    return string(out)
}

func getPointer(m map[string]any, ptr string) any {
    parts := splitPtr(ptr)
    var cur any = m
    for _, p := range parts {
        mm, ok := cur.(map[string]any)
        if !ok {
            return nil
        }
        cur = mm[p]
    }
    return cur
}

func splitPtr(ptr string) []string {
    if len(ptr) > 0 && ptr[0] == '/' {
        ptr = ptr[1:]
    }
    if ptr == "" {
        return nil
    }
    parts := []string{}
    cur := []byte{}
    for i := 0; i < len(ptr); i++ {
        if ptr[i] == '/' {
            parts = append(parts, string(cur))
            cur = cur[:0]
            continue
        }
        cur = append(cur, ptr[i])
    }
    parts = append(parts, string(cur))
    for i, p := range parts {
        p = replaceAll(p, "~1", "/")
        p = replaceAll(p, "~0", "~")
        parts[i] = p
    }
    return parts
}

func hashAny(v any) string {
    data, _ := json.Marshal(v)
    sum := sha256.Sum256(data)
    return hex.EncodeToString(sum[:])
}
```

- [ ] **Wire into apply**

In `internal/cli/apply.go`, after `render.Apply(plan, reg)`:

```go
// Load + update state
home := paths.AgentsyncHome(paths.OSEnv{})
statePath := filepath.Join(home, ".state", "targets.json")
s, err := state.Load(statePath)
if err != nil {
    return err
}
for name, res := range plan.PerAgent {
    if err := render.RecordOpsState(s, name, sc, "", res.Ops); err != nil {
        return err
    }
}
if err := state.Save(statePath, s); err != nil {
    return err
}
```

Test, commit:

```bash
git commit -am "$(cat <<'EOF'
feat(render,cli): record state after successful apply

Files/keys hashed and stored to ~/.agentsync/.state/targets.json. Drift
detection in Task 4+ reads from this. State save is atomic
(iox.AtomicWrite via state.Save).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Pipeline owns OwnedKeys flow

**Files:** `internal/render/pipeline.go`

Before each adapter's Render is called, pipeline reads state.Keys for that adapter+scope+project, attaches matching `/file/pointer` paths to `op.OwnedKeys` for each op the adapter returns.

Actually cleaner: adapter returns ops without OwnedKeys; pipeline iterates state.Keys keyed by `<agent>:<scope>:<project>:<file>:<pointer>` and groups by file path → for each file, collects pointers → attaches to ops with that path.

- [ ] **Implement** the pointer-injection step in `Plan()` after collecting ops; test that orphan removal works through the full path.

```go
// in Plan():
for name, res := range out.PerAgent {
    for i, op := range res.Ops {
        if op.MergeStrategy == "merge-json-keys" || op.MergeStrategy == "merge-jsonc-keys" {
            res.Ops[i].OwnedKeys = ownedKeysFor(s, name, scope, project, op.Path)
        }
    }
    out.PerAgent[name] = res
}
```

```go
func ownedKeysFor(s *state.Targets, agent string, scope adapter.Scope, project, path string) []string {
    prefix := fmt.Sprintf("%s:%s:%s:%s:", agent, scope.String(), project, path)
    var out []string
    for k := range s.Keys {
        if strings.HasPrefix(k, prefix) {
            out = append(out, strings.TrimPrefix(k, prefix))
        }
    }
    return out
}
```

Plan signature changes to accept `*state.Targets`. Update callers. Test: pre-populate state with one owned pointer, ensure subsequent Render produces FileOp.OwnedKeys with that pointer.

Commit.

---

## Task 4: `agentsync status`

Walks `state.Files` + `state.Keys`, classifies each, prints per-(agent,file) breakdown.

- [ ] **Implement** `internal/cli/status.go`:

```go
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
    "github.com/spxrogers/agentsync/internal/render"
    "github.com/spxrogers/agentsync/internal/source"
    "github.com/spxrogers/agentsync/internal/state"
)

func newStatusCmd() *cobra.Command {
    return &cobra.Command{
        Use:   "status",
        Short: "report drift across registered agents",
        Args:  cobra.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            home := paths.AgentsyncHome(paths.OSEnv{})
            c, err := source.Load(afero.NewOsFs(), home)
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
            plan, err := render.Plan(c, reg, agents, adapter.ScopeUser, "", s)
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
                // file-level: for each op, classify
                for _, op := range res.Ops {
                    if op.MergeStrategy != "" {
                        continue // covered key-by-key below
                    }
                    if seen[op.Path] {
                        continue
                    }
                    seen[op.Path] = true
                    hsrc := hashContent(op.Content)
                    happlied := s.Files[fmt.Sprintf("%s:user::%s", name, op.Path)].SHA256
                    hdest := hashFile(op.Path)
                    cls := drift.Classify(hsrc, happlied, hdest)
                    fmt.Fprintf(w, "  %-9s %s\n", cls, op.Path)
                }
                // key-level: for each merge op, walk owned pointers
                for _, op := range res.Ops {
                    if op.MergeStrategy != "merge-json-keys" && op.MergeStrategy != "merge-jsonc-keys" {
                        continue
                    }
                    var ours map[string]any
                    _ = json.Unmarshal(op.Content, &ours)
                    final := readJSON(op.Path)
                    for _, ptr := range render.PublicCollectPointers(ours, "") {
                        hsrc := hashAny(getPointer(ours, ptr))
                        happlied := s.Keys[fmt.Sprintf("%s:user::%s:%s", name, op.Path, ptr)].SHA256
                        hdest := hashAny(getPointer(final, ptr))
                        cls := drift.Classify(hsrc, happlied, hdest)
                        fmt.Fprintf(w, "  %-9s %s#%s\n", cls, op.Path, ptr)
                    }
                }
            }
            return nil
        },
    }
}

func hashContent(b []byte) string {
    sum := sha256.Sum256(b)
    return hex.EncodeToString(sum[:])
}

func hashFile(path string) string {
    data, err := os.ReadFile(path)
    if err != nil {
        return ""
    }
    return hashContent(data)
}

func hashAny(v any) string {
    if v == nil {
        return ""
    }
    data, _ := json.Marshal(v)
    return hashContent(data)
}

func readJSON(path string) map[string]any {
    data, err := os.ReadFile(path)
    if err != nil {
        return map[string]any{}
    }
    var m map[string]any
    _ = json.Unmarshal(data, &m)
    return m
}

func getPointer(m map[string]any, ptr string) any {
    if !strings.HasPrefix(ptr, "/") {
        return nil
    }
    parts := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
    var cur any = m
    for _, p := range parts {
        mp, ok := cur.(map[string]any)
        if !ok {
            return nil
        }
        cur = mp[p]
    }
    return cur
}
```

(Notes: `render.PublicCollectPointers` is the exported version of M3 Task 2's `collectPointers` — make it public.)

Wire into `cli.Root.AddCommand(newStatusCmd())`. Test:

```go
func TestStatus_DriftAfterDirectEdit(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
    _ = os.MkdirAll(filepath.Dir(mcp), 0o755)
    _ = os.WriteFile(mcp, []byte(`[server]
type="stdio"
command="npx"`), 0o644)
    _, _ = runCLI(t, env, "apply")

    // Modify destination directly
    dst := filepath.Join(tmp, ".claude.json")
    body, _ := os.ReadFile(dst)
    body = []byte(strings.Replace(string(body), `"npx"`, `"npm"`, 1))
    _ = os.WriteFile(dst, body, 0o644)

    out, err := runCLI(t, env, "status")
    if err != nil {
        t.Fatalf("status: %v\n%s", err, out)
    }
    if !strings.Contains(out, "drift") {
        t.Fatalf("status didn't report drift: %s", out)
    }
}
```

Commit.

---

## Task 5: `agentsync diff [<path>]`

For each non-clean item, show a unified diff (source vs destination). Use `github.com/sergi/go-diff/diffmatchpatch` for the diff renderer.

- [ ] Add dependency, implement, test (single-file diff and key-level diff). Commit.

---

## Task 6: `agentsync reconcile`

Walks all drifted/conflicting items, prompts user per-item. Uses `charmbracelet/huh`.

- [ ] **Test scaffold (TUI testing via `teatest`-style harness)**

```go
// integration test that pipes responses to stdin would be heavy; v1 ships
// reconcile with a non-interactive "--auto-*" path tested directly, plus
// a smoke test that reconcile exits 0 when input is empty (no drift).
func TestReconcile_NoDrift(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")
    _, _ = runCLI(t, env, "apply")
    if _, err := runCLI(t, env, "reconcile", "--auto-safe"); err != nil {
        t.Fatal(err)
    }
}

func TestReconcile_AutoOverride(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")

    mcp := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
    _ = os.MkdirAll(filepath.Dir(mcp), 0o755)
    _ = os.WriteFile(mcp, []byte(`[server]
type="stdio"
command="npx"`), 0o644)
    _, _ = runCLI(t, env, "apply")

    // Manually mutate destination to create drift
    dst := filepath.Join(tmp, ".claude.json")
    body, _ := os.ReadFile(dst)
    drifted := strings.Replace(string(body), `"npx"`, `"npm"`, 1)
    _ = os.WriteFile(dst, []byte(drifted), 0o644)

    // reconcile --auto-override should re-apply source value
    if _, err := runCLI(t, env, "reconcile", "--auto-override"); err != nil {
        t.Fatal(err)
    }
    final, _ := os.ReadFile(dst)
    if !strings.Contains(string(final), `"npx"`) {
        t.Fatalf("override didn't restore source value: %s", final)
    }
}
```

- [ ] **Implement** `internal/cli/reconcile.go`:

Three flags: `--auto-writeback`, `--auto-override`, `--auto-safe`. Walk plan items in same order as status; for each:
- `Clean` / `Pending` / `New` / `Converged` → no prompt; `--auto-safe` resolves them silently.
- `Drift` / `Conflict` / `OrphanDrifted` → prompt unless `--auto-*`.
- `Orphan` → no prompt (apply removes).
- `ForeignCollision` → no prompt; `apply` already backed up the original.

For each prompted item:

```
~/.claude/settings.json#/mcpServers/github   (drift)
  source:      {"command": "npx"}
  destination: {"command": "npm"}

  [w]rite-back  [o]verride  [s]kip  [i]gnore  [d]iff  [q]uit
```

- `w` (write-back): rewrite the canonical TOML key from destination's value via `internal/source.Writer` (introduced as a small helper that re-encodes a single MCP/Plugin/etc with `pelletier/go-toml/v2`).
- `o` (override): re-render and apply the source-side value to dest (effectively next `apply` does this).
- `s` (skip): leave inconsistent for now; reported again on next status.
- `i` (ignore): write path to `~/.agentsync/ignore.toml`; classifier omits it.
- `d` (diff): print unified diff, re-prompt.
- `q` (quit): stop; remaining items not touched.

Bulk: capital `W` / `O` / `S` apply that action to all remaining items.

Implementation uses cobra + a simple read-character loop on `os.Stdin` (no need for full huh component model — keep it small):

```go
package cli

import (
    "bufio"
    "fmt"
    "io"
    "os"
    "strings"

    "github.com/spf13/cobra"
    // ...
)

func newReconcileCmd() *cobra.Command {
    var autoWB, autoOR, autoSafe bool
    cmd := &cobra.Command{
        Use:   "reconcile",
        Short: "interactively resolve drift",
        RunE: func(cmd *cobra.Command, _ []string) error {
            // build plan, walk items, classify each
            // bulkAction tracks W/O/S overrides
            // for items needing prompt, read 1 char from os.Stdin or use --auto-* flags
            return reconcileRun(cmd, os.Stdin, autoWB, autoOR, autoSafe)
        },
    }
    cmd.Flags().BoolVar(&autoWB, "auto-writeback", false, "auto-resolve drift by writing dest back to source")
    cmd.Flags().BoolVar(&autoOR, "auto-override", false, "auto-resolve drift by re-applying source to dest")
    cmd.Flags().BoolVar(&autoSafe, "auto-safe", false, "auto-resolve only converged/pending/new")
    return cmd
}

func reconcileRun(cmd *cobra.Command, in io.Reader, autoWB, autoOR, autoSafe bool) error {
    // implementation: pseudocode
    //   for each item in plan:
    //     classify
    //     if SafeForAutoApply || (auto-safe && SafeForAutoApply): no-op or apply
    //     elif autoWB: writeBackItem(...)
    //     elif autoOR: overrideItem(...) (defer to next apply)
    //     else: prompt(in, item) -> {w,o,s,i,d,q,W,O,S}
    //   if any --auto-override happened, run render.Apply at end
    //   on quit: return early
    return nil
}
```

(The implementation is straightforward but boilerplate-heavy. The engineer fills in `writeBackItem` / `overrideItem` per-component using the source.Writer added in M3 Task 7.)

Commit.

---

## Task 7: `internal/source.Writer` for write-back

**Files:** `internal/source/writer.go`, `internal/source/writer_test.go`

When reconcile chooses write-back, opensync mutates the canonical file. Comment-preserving via `pelletier/go-toml/v2` AST: we don't have full AST mutation today; v1 strategy:

For `mcp/<id>.toml`: marshal the updated `MCPServer` as TOML and atomically write — comments above the file are lost on first write-back. **Documented v1 trade-off.** Better preservation lands as a v1.x improvement.

```go
package source

import (
    "fmt"
    "path/filepath"

    "github.com/pelletier/go-toml/v2"
    "github.com/spxrogers/agentsync/internal/iox"
)

// WriteMCP writes mcp/<id>.toml from m. Overwrites existing; comments lost.
func WriteMCP(home, id string, m MCPServer) error {
    body, err := toml.Marshal(m)
    if err != nil {
        return fmt.Errorf("marshal mcp %s: %w", id, err)
    }
    return iox.AtomicWrite(filepath.Join(home, "mcp", id+".toml"), body, 0o644)
}

// WritePlugin, WriteMarketplace, WriteSkill follow the same pattern.
```

Test, commit.

---

## Task 8: `--auto-safe` integration test + final commit

```go
func TestApplyThenReconcileAutoSafe(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}
    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")
    // ... apply, then reconcile --auto-safe should be a no-op
    if _, err := runCLI(t, env, "reconcile", "--auto-safe"); err != nil {
        t.Fatal(err)
    }
}
```

Commit.

---

## Done When

- `agentsync status` reports per-agent drift with the 9-class classifier; cleanly handles file-level + key-level items.
- `agentsync diff <path>` shows unified diff source-vs-dest.
- `agentsync reconcile` prompts per drifted item with `[w]/[o]/[s]/[i]/[d]/[q]` + bulk hotkeys; non-interactive flags (`--auto-writeback`, `--auto-override`, `--auto-safe`) work in CI.
- Foreign keys (paths with no entry in `state.Keys`) are listed in `status` but never enter the case table.
- A round-trip `apply → mutate dest → status (drift) → reconcile --auto-writeback → apply` produces clean status.
- CI green.
