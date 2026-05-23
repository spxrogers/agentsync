# agentsync M2 — OpenCode adapter

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md#conventions-used-across-all-milestone-plans). Builds on [M0](2026-05-04-agentsync-m0-skeleton.md) + [M1](2026-05-04-agentsync-m1-claude.md).

**Goal:** Second adapter. Implements `internal/adapter/opencode` covering MCP, memory (`AGENTS.md`), skill (write to shared `.claude/skills/<name>/SKILL.md` since OpenCode reads it natively), subagent (markdown w/ different frontmatter), slash command (markdown w/ `template:` frontmatter). Hooks `✗ skip(warn)` (OpenCode hooks are JS/TS plugin event subscriptions; shim generation deferred). LSP `✗ skip(warn)`. Replaces NoopAdapter for `opencode` in registry.

**Architecture:** Mirrors M1's package layout. Settings file is `~/.config/opencode/opencode.json` — **JSONC** (JSON with comments). `tailscale/hujson` parses it; `MergeKeys` from M1's `claude` package is *generalized* and moved to a shared package (or duplicated; trade-off explained below).

**Decision:** Move `MergeKeys`, `splitPointer`, `pointerExists`, `deletePointer`, `deepCopyMap`, `mergeMaps` from `internal/adapter/claude/settings.go` into `internal/jsonkeys/jsonkeys.go` so OpenCode and (eventually) Cursor adapters share. M2 Task 1 does this refactor.

**Tech stack:** `tailscale/hujson` for JSONC parse/preserve, otherwise same as M1.

---

## Files created/modified

```
NEW:
internal/jsonkeys/                 # extracted from M1 claude settings.go
├── jsonkeys.go
└── jsonkeys_test.go

internal/adapter/opencode/
├── opencode.go                    # Adapter, Name, Capabilities, Detect
├── paths.go                       # ~/.config/opencode/* path resolution
├── render.go                      # Render(canonical) -> []FileOp
├── render_test.go
├── ingest.go                      # Disk -> source.Canonical
├── ingest_test.go
├── apply.go                       # FileOp writer, JSONC-aware
├── apply_test.go
├── settings.go                    # opencode.json (JSONC) merge logic
├── settings_test.go
├── skill.go                       # write SKILL.md to .claude/skills/ shared path
├── subagent.go                    # frontmatter munge: tools->permission, color drop, mode add
├── command.go                     # frontmatter munge for commands
└── memory.go                      # AGENTS.md (project root for project scope)

MODIFIED:
internal/adapter/claude/settings.go    # delegate to internal/jsonkeys
internal/cli/registry_internal.go      # register opencode.New(...)
```

---

## Task 1: Extract `internal/jsonkeys` from claude settings

**Files:**
- Create: `internal/jsonkeys/jsonkeys.go`, `internal/jsonkeys/jsonkeys_test.go`
- Modify: `internal/adapter/claude/settings.go`

- [ ] **Step 1.1: Move code**

Copy the body of `internal/adapter/claude/settings.go` into `internal/jsonkeys/jsonkeys.go`, change package name to `jsonkeys`, and re-export `MergeKeys`. Move the test file too (`internal/jsonkeys/jsonkeys_test.go` mirrors the existing tests with package rename).

- [ ] **Step 1.2: Stub claude/settings.go to delegate**

```go
package claude

import "github.com/spxrogers/agentsync/internal/jsonkeys"

// MergeKeys is preserved as a re-export for backward compat with claude
// internal callers; new callers should import internal/jsonkeys directly.
func MergeKeys(existing, ours map[string]any, ownedPointers []string) (map[string]any, []string, []string) {
    return jsonkeys.MergeKeys(existing, ours, ownedPointers)
}
```

- [ ] **Step 1.3: Run; commit**

```bash
go test -race ./internal/jsonkeys/... ./internal/adapter/claude/...
git add internal/jsonkeys internal/adapter/claude
git commit -m "$(cat <<'EOF'
refactor(jsonkeys): extract per-key JSON merge from claude adapter

Identical behavior, shared package. Claude re-exports MergeKeys for
internal callers; opencode and future adapters import jsonkeys directly.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Add `tailscale/hujson` dependency + JSONC settings module

**Files:**
- Modify: `go.mod`
- Create: `internal/adapter/opencode/settings.go`, `internal/adapter/opencode/settings_test.go`

`opencode.json` is JSONC: it can have `// line comments` and trailing commas. We must round-trip them.

- [ ] **Step 2.1: Add dependency**

```bash
go get github.com/tailscale/hujson@latest
```

- [ ] **Step 2.2: Test for parse + render round-trip**

```go
package opencode_test

import (
    "strings"
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter/opencode"
)

func TestApplyJSONCMerge_PreservesComments(t *testing.T) {
    existing := []byte(`{
  // top comment, must survive
  "foreign": 1,
  "mcp": {
    "stale": {} // soon-to-be-removed
  }
}`)
    ours := map[string]any{
        "mcp": map[string]any{
            "github": map[string]any{"command": "npx"},
        },
    }
    owned := []string{"/mcp/stale", "/mcp/github"}

    out, err := opencode.MergeJSONC(existing, ours, owned)
    if err != nil {
        t.Fatal(err)
    }
    s := string(out)
    if !strings.Contains(s, "top comment, must survive") {
        t.Fatalf("comment lost:\n%s", s)
    }
    if strings.Contains(s, "stale") {
        t.Fatalf("stale should be removed:\n%s", s)
    }
    if !strings.Contains(s, `"github"`) {
        t.Fatalf("github not added:\n%s", s)
    }
}
```

- [ ] **Step 2.3: Implement**

`internal/adapter/opencode/settings.go`:

```go
// Package opencode-settings: JSONC-aware merge for opencode.json. Uses
// tailscale/hujson which preserves comments and trailing commas across
// parse->mutate->serialize cycles.
package opencode

import (
    "encoding/json"
    "fmt"

    "github.com/tailscale/hujson"
    "github.com/spxrogers/agentsync/internal/jsonkeys"
)

// MergeJSONC merges ours into existing JSONC content, removing ownedPointers
// no longer present in ours. Comments and trailing-comma formatting from
// existing are preserved as much as the hujson AST allows.
//
// Strategy: parse existing JSONC -> standardize to JSON (Pack), MergeKeys,
// then format result as plain JSON. v1 trade-off: trailing comma + comment
// preservation is partial — comments outside touched keys survive; comments
// adjacent to deleted keys are also removed. M2 ships this; comment-position
// fidelity can be tightened later if pain emerges.
func MergeJSONC(existing []byte, ours map[string]any, ownedPointers []string) ([]byte, error) {
    if len(existing) == 0 {
        existing = []byte("{}")
    }
    val, err := hujson.Parse(existing)
    if err != nil {
        return nil, fmt.Errorf("parse jsonc: %w", err)
    }
    val.Standardize()
    var existingMap map[string]any
    if err := json.Unmarshal(val.Pack(), &existingMap); err != nil {
        return nil, fmt.Errorf("standardize jsonc: %w", err)
    }
    if existingMap == nil {
        existingMap = map[string]any{}
    }
    merged, _, _ := jsonkeys.MergeKeys(existingMap, ours, ownedPointers)
    out, err := json.MarshalIndent(merged, "", "  ")
    if err != nil {
        return nil, fmt.Errorf("marshal merged: %w", err)
    }
    return append(out, '\n'), nil
}
```

(The comment "comments lost when adjacent to deleted keys" is honest — full hujson AST round-trip with surgical key edits is more complex; v1 ships the simpler standardize+merge approach. Trade-off documented.)

- [ ] **Step 2.4: Run; commit**

```bash
go test -race ./internal/adapter/opencode/...
git add go.mod go.sum internal/adapter/opencode
git commit -m "$(cat <<'EOF'
feat(adapter/opencode): JSONC-aware merge via tailscale/hujson + jsonkeys

opencode.json is JSONC; hujson parses comments+trailing-commas. v1 ships
standardize+merge; trailing-comma fidelity may regress in edge cases —
documented trade-off. Comment fidelity around touched keys is partial;
fine for the typical "edit one MCP server" path.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Adapter skeleton + Detect()

**Files:**
- Create: `internal/adapter/opencode/{opencode.go, paths.go, opencode_test.go}`

Mirrors M1 Task 1. Detection: `~/.config/opencode/` exists OR `opencode` on PATH.

- [ ] **Implement**

`internal/adapter/opencode/paths.go`:

```go
package opencode

import "path/filepath"

type Paths struct {
    ConfigDir       string // ~/.config/opencode
    Settings        string // ~/.config/opencode/opencode.json
    AgentsDir       string // ~/.config/opencode/agents (user); or .opencode/agents (project)
    CommandsDir     string
    ClaudeSkillsDir string // ~/.claude/skills (shared with Claude!)
    Memory          string // CLAUDE not used; AGENTS.md at project root
}

func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
    if projectScope && project != "" {
        return Paths{
            ConfigDir:       filepath.Join(project, ".opencode"),
            Settings:        filepath.Join(project, ".opencode", "opencode.json"),
            AgentsDir:       filepath.Join(project, ".opencode", "agents"),
            CommandsDir:     filepath.Join(project, ".opencode", "commands"),
            ClaudeSkillsDir: filepath.Join(project, ".claude", "skills"),
            Memory:          filepath.Join(project, "AGENTS.md"),
        }
    }
    cfg := filepath.Join(targetRoot, ".config", "opencode")
    return Paths{
        ConfigDir:       cfg,
        Settings:        filepath.Join(cfg, "opencode.json"),
        AgentsDir:       filepath.Join(cfg, "agents"),
        CommandsDir:     filepath.Join(cfg, "commands"),
        ClaudeSkillsDir: filepath.Join(targetRoot, ".claude", "skills"),
        Memory:          filepath.Join(targetRoot, ".config", "opencode", "AGENTS.md"),
    }
}
```

`internal/adapter/opencode/opencode.go`:

```go
package opencode

import (
    "os"
    "os/exec"

    "github.com/spxrogers/agentsync/internal/adapter"
)

type Options struct{ TargetRoot string }

type Adapter struct{ opts Options }

func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "opencode" }

func (a *Adapter) Capabilities() adapter.Capability {
    return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
        adapter.CapSubagent | adapter.CapCommand
    // Hook + LSP capabilities omitted: we ship them as ✗ skip in v1.
}

func (a *Adapter) Detect() (bool, error) {
    p := ResolvePaths(a.opts.TargetRoot, "", false)
    if _, err := os.Stat(p.ConfigDir); err == nil {
        return true, nil
    }
    if _, err := exec.LookPath("opencode"); err == nil {
        return true, nil
    }
    return false, nil
}
```

Test analogue of M1 Task 1, commit.

---

## Task 4: Render — MCP

OpenCode `opencode.json` shape: `{"mcp": {"<id>": {"command": "...", "args": [...], "env": {...}}}}` — note the key is `mcp` not `mcpServers`.

- [ ] **Test**

```go
func TestRender_MCP(t *testing.T) {
    enabled := true
    c := source.Canonical{MCPServers: []source.MCPServer{{
        ID: "github",
        Server: source.MCPServerSpec{
            Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
            Agents: []string{"opencode"}, Enabled: &enabled,
        },
    }}}
    a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
    ops, _, _ := a.Render(c, adapter.ScopeUser, "")
    var found bool
    for _, op := range ops {
        if strings.HasSuffix(op.Path, "opencode.json") {
            found = true
            if op.MergeStrategy != "merge-jsonc-keys" {
                t.Fatalf("merge strategy = %q", op.MergeStrategy)
            }
            var ours map[string]any
            _ = json.Unmarshal(op.Content, &ours)
            mcp := ours["mcp"].(map[string]any)["github"].(map[string]any)
            if mcp["command"] != "npx" {
                t.Fatalf("command = %v", mcp["command"])
            }
        }
    }
    if !found {
        t.Fatal("opencode.json op missing")
    }
}
```

- [ ] **Implement**

`internal/adapter/opencode/render.go`:

```go
package opencode

import (
    "encoding/json"
    "fmt"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) Render(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
    p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
    var ops []adapter.FileOp
    var skips []adapter.Skip

    if mcpOps, err := a.renderMCP(c, p); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, mcpOps...)
    }
    if memOps, err := a.renderMemory(c, p); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, memOps...)
    }
    if skOps, err := a.renderSkills(c, p); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, skOps...)
    }
    if saOps, saSkips, err := a.renderSubagents(c, p); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, saOps...)
        skips = append(skips, saSkips...)
    }
    if cmdOps, err := a.renderCommands(c, p); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, cmdOps...)
    }
    // Hooks
    for _, h := range c.Hooks {
        skips = append(skips, adapter.Skip{
            Component: "hook", Name: h.Event,
            Reason: "OpenCode hooks are JS/TS plugins; shim generation deferred to post-v1",
        })
    }
    // LSP
    for _, l := range c.LSPServers {
        skips = append(skips, adapter.Skip{
            Component: "lsp", Name: l.ID,
            Reason: "OpenCode LSP projection deferred to v1.x",
        })
    }
    return ops, skips, nil
}

func (a *Adapter) renderMCP(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
    mcp := map[string]any{}
    for _, m := range c.MCPServers {
        if m.Server.Enabled != nil && !*m.Server.Enabled {
            continue
        }
        if !agentTargeted("opencode", m.Server.Agents) {
            continue
        }
        spec := map[string]any{}
        if m.Server.Type != "" {
            spec["type"] = m.Server.Type
        }
        if m.Server.Command != "" {
            spec["command"] = m.Server.Command
        }
        if len(m.Server.Args) > 0 {
            spec["args"] = m.Server.Args
        }
        if len(m.Server.Env) > 0 {
            spec["env"] = m.Server.Env
        }
        if m.Server.URL != "" {
            spec["url"] = m.Server.URL
        }
        mcp[m.ID] = spec
    }
    if len(mcp) == 0 {
        return nil, nil
    }
    ours := map[string]any{"mcp": mcp}
    body, err := json.MarshalIndent(ours, "", "  ")
    if err != nil {
        return nil, fmt.Errorf("marshal opencode mcp: %w", err)
    }
    return []adapter.FileOp{{
        Action:        "write",
        Path:          p.Settings,
        Content:       append(body, '\n'),
        Mode:          0o644,
        SourceID:      "mcp/* (multiple)",
        MergeStrategy: "merge-jsonc-keys",
    }}, nil
}

func agentTargeted(name string, agents []string) bool {
    if len(agents) == 0 {
        return true
    }
    for _, a := range agents {
        if a == "*" || a == name {
            return true
        }
    }
    return false
}
```

Commit:

```bash
git commit -am "feat(adapter/opencode): render MCP into opencode.json with merge-jsonc-keys

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Render — memory (AGENTS.md)

`renderMemory` writes `c.Memory.Body` (with @import expansion) to `p.Memory`. Same shape as Claude's renderMemory but to a different path. Skill-test for Claude/M1 Task 6 applies; copy and adjust path expectations. Commit.

---

## Task 6: Render — skills (write to shared `.claude/skills/`)

OpenCode reads `.claude/skills/<name>/SKILL.md` natively. We write SKILL.md to `p.ClaudeSkillsDir` (which Claude also writes to in M1).

**Coordination with M1:** if both adapters render the same skill file, we get two FileOps with identical paths and identical content (since Skill.Frontmatter and Body are deterministic). The apply pipeline must dedupe per-path. Add this to render.Apply (in `internal/render/pipeline.go`):

```go
func Apply(p Plan, reg *adapter.Registry) error {
    seen := map[string]bool{}
    for name, res := range p.PerAgent {
        a := reg.Lookup(name)
        if a == nil {
            return fmt.Errorf("adapter %q not registered at apply", name)
        }
        var deduped []adapter.FileOp
        for _, op := range res.Ops {
            if op.Action == "write" {
                if seen[op.Path] {
                    continue
                }
                seen[op.Path] = true
            }
            deduped = append(deduped, op)
        }
        if err := a.Apply(deduped); err != nil {
            return fmt.Errorf("apply %s: %w", name, err)
        }
    }
    return nil
}
```

Add a test for the dedupe:

```go
func TestPipeline_DedupesIdenticalWritesAcrossAdapters(t *testing.T) {
    // ... build canonical with one Skill,
    // ... two adapters that both render the same path,
    // ... assert filesystem only sees one write.
}
```

Skill render in opencode mirrors Claude's renderSkills. Commit:

```bash
git commit -am "feat(adapter/opencode): write skills to shared .claude/skills/ path

OpenCode reads .claude/skills/<name>/SKILL.md natively. Render emits
identical FileOp to Claude's; render.Apply dedupes per path so the file
is written once per apply even when multiple adapters target it.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: Render — subagents (markdown w/ frontmatter munge)

Translate Claude-shaped subagent frontmatter to OpenCode-shaped.

| Claude frontmatter | OpenCode frontmatter | Notes |
|---|---|---|
| `description` | `description` | direct copy |
| `model` | `model` | direct copy |
| `tools` (allowlist) | drop / log | OpenCode uses `permission` model; mapping tools->permission is non-trivial — log a Skip note |
| `color` | drop | OpenCode has no color concept |
| (none) | `mode: subagent` | always added |
| (none) | `temperature` | not added (Claude doesn't carry it) |
| (none) | `permission` | not added in v1 (would need policy) |

- [ ] **Test**

```go
func TestRender_Subagent_FrontmatterMunge(t *testing.T) {
    c := source.Canonical{Subagents: []source.Subagent{{
        Name:        "review",
        Frontmatter: map[string]any{
            "description": "Code review",
            "model":       "claude-sonnet-4-7",
            "tools":       []string{"Read", "Grep"},
            "color":       "blue",
        },
        Body: "Review code.\n",
    }}}
    a := opencode.New(opencode.Options{TargetRoot: t.TempDir()})
    ops, skips, _ := a.Render(c, adapter.ScopeUser, "")
    // verify file content
    var op *adapter.FileOp
    for i, o := range ops {
        if strings.HasSuffix(o.Path, "/agents/review.md") {
            op = &ops[i]
        }
    }
    if op == nil {
        t.Fatal("no agent op")
    }
    if !strings.Contains(string(op.Content), "mode: subagent") {
        t.Fatalf("missing mode:subagent in: %s", op.Content)
    }
    if strings.Contains(string(op.Content), "color:") {
        t.Fatalf("color should be dropped: %s", op.Content)
    }
    if strings.Contains(string(op.Content), "tools:") {
        t.Fatalf("tools should be dropped: %s", op.Content)
    }
    // verify skip log
    var sawToolsSkip bool
    for _, s := range skips {
        if s.Component == "subagent-frontmatter" && s.Name == "review" {
            if strings.Contains(s.Reason, "tools") {
                sawToolsSkip = true
            }
        }
    }
    if !sawToolsSkip {
        t.Fatalf("no skip emitted for tools allowlist")
    }
}
```

- [ ] **Implement**

`internal/adapter/opencode/subagent.go`:

```go
package opencode

import (
    "fmt"
    "path/filepath"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/claude"
    "github.com/spxrogers/agentsync/internal/source"
)

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
        out["mode"] = "subagent"

        // Drop with skip notes for unmappable fields:
        if _, ok := s.Frontmatter["tools"]; ok {
            skips = append(skips, adapter.Skip{
                Component: "subagent-frontmatter", Name: s.Name,
                Reason: "Claude `tools` allowlist not projected to OpenCode `permission` (manual rule design needed)",
            })
        }
        if _, ok := s.Frontmatter["color"]; ok {
            skips = append(skips, adapter.Skip{
                Component: "subagent-frontmatter", Name: s.Name,
                Reason: "Claude `color` has no OpenCode equivalent",
            })
        }

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
```

Commit.

---

## Task 8: Render — slash commands (frontmatter munge)

Claude command frontmatter (`description`, `argument-hint`, `model`) → OpenCode (`description`, `agent`, `subtask`, `model`, `template`).

- For Claude `description` → OpenCode `description` (direct).
- `model` → `model` (direct).
- `argument-hint` → drop (skip note).
- Body of the markdown becomes the command body (template). OpenCode treats `template:` frontmatter or body interchangeably; we keep body and don't add `template:` frontmatter to avoid duplication.

- [ ] Mirror Task 7 pattern. Test, implement (`command.go`), commit.

---

## Task 9: `Apply()` — JSONC-aware writer

Like Claude's Apply but with `merge-jsonc-keys` strategy.

`internal/adapter/opencode/apply.go`:

```go
package opencode

import (
    "encoding/json"
    "fmt"
    "os"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/iox"
)

func (a *Adapter) Apply(ops []adapter.FileOp) error {
    for _, op := range ops {
        switch op.Action {
        case "delete":
            if err := os.Remove(op.Path); err != nil && !os.IsNotExist(err) {
                return fmt.Errorf("delete %s: %w", op.Path, err)
            }
        case "", "write":
            if err := a.applyWrite(op); err != nil {
                return err
            }
        default:
            return fmt.Errorf("unknown action %q", op.Action)
        }
    }
    return nil
}

func (a *Adapter) applyWrite(op adapter.FileOp) error {
    mode := os.FileMode(op.Mode)
    if mode == 0 {
        mode = 0o644
    }
    if op.MergeStrategy == "merge-jsonc-keys" {
        existing, _ := os.ReadFile(op.Path)
        var ours map[string]any
        if err := json.Unmarshal(op.Content, &ours); err != nil {
            return fmt.Errorf("parse our payload: %w", err)
        }
        out, err := MergeJSONC(existing, ours, op.OwnedKeys)
        if err != nil {
            return err
        }
        return iox.AtomicWrite(op.Path, out, mode)
    }
    return iox.AtomicWrite(op.Path, op.Content, mode)
}
```

Test (mirrors Claude apply tests, asserting JSONC comments survive). Commit.

---

## Task 10: `Ingest()`

Inverse of Render. Reads `opencode.json` → MCP; `<AgentsDir>/*.md` → Subagents (frontmatter munged back to canonical: drop `mode`, retain `description`+`model`); `<CommandsDir>/*.md` → Commands; AGENTS.md → Memory.Body verbatim.

For Subagent ingest: we lose the tools/color fields that we dropped on render — round-trip is therefore not parity for subagents (it CAN'T be since the data isn't on disk). Round-trip test for OpenCode subagents asserts `Render(Ingest(Apply(in))) ≈ in` modulo dropped fields.

Test, implement, commit.

---

## Task 11: Wire into registry + integration test

Modify `internal/cli/registry_internal.go`:

```go
_ = r.Register(opencode.New(opencode.Options{TargetRoot: home}))
```

Replace the noop entry for "opencode".

Integration test:

```go
func TestIntegration_M2_OpenCodeMCPApply(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    _, _ = runCLI(t, env, "init")
    _, _ = runCLI(t, env, "agent", "add", "claude")
    _, _ = runCLI(t, env, "agent", "add", "opencode")

    mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
    _ = os.MkdirAll(filepath.Dir(mcpFile), 0o755)
    _ = os.WriteFile(mcpFile, []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
`), 0o644)

    if _, err := runCLI(t, env, "apply"); err != nil {
        t.Fatal(err)
    }

    // Both Claude and OpenCode got it
    body, _ := os.ReadFile(filepath.Join(tmp, ".claude.json"))
    if !strings.Contains(string(body), "github") {
        t.Fatalf("claude missing github: %s", body)
    }
    body, _ = os.ReadFile(filepath.Join(tmp, ".config", "opencode", "opencode.json"))
    if !strings.Contains(string(body), "github") {
        t.Fatalf("opencode missing github: %s", body)
    }
}
```

Commit:

```bash
git commit -am "$(cat <<'EOF'
feat(cli): register real opencode adapter; cross-agent MCP fanout works

End-to-end: one mcp/<id>.toml -> ~/.claude.json AND
~/.config/opencode/opencode.json after a single apply. M2 done.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done When

- `agentsync apply` writes both `~/.claude.json` AND `~/.config/opencode/opencode.json` from a single canonical MCP file.
- A single canonical Subagent renders to both `~/.claude/agents/<n>.md` (Claude shape) AND `~/.config/opencode/agents/<n>.md` (OpenCode shape with frontmatter munge); skips for `tools`+`color` are surfaced in the apply translation report.
- A single canonical Skill writes ONE file at `~/.claude/skills/<n>/SKILL.md` (deduped), consumed by both Claude and OpenCode.
- JSONC comments in pre-existing `opencode.json` survive agentsync mutations to keys we don't touch.
- Hooks and LSP in canonical produce explicit `✗ skip(warn)` entries in the translation report.
- CI green on linux/macos/windows.
