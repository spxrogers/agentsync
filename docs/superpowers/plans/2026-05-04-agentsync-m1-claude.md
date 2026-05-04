# agentsync M1 — Claude Code adapter

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Conventions in [`overview`](2026-05-04-agentsync-v1.0-overview.md#conventions-used-across-all-milestone-plans). Builds on [M0 skeleton](2026-05-04-agentsync-m0-skeleton.md).

**Goal:** First real adapter. Implements `internal/adapter/claude` covering all 7 component types (MCP, memory, skill, subagent, slash command, hook, LSP). Renders + ingests round-trip. Settings written to `~/.claude/settings.json` and `~/.claude.json` use **per-key merge** so foreign keys (Claude's own writes, user hand-edits) survive untouched. Replaces the NoopAdapter for `claude` in the registry.

**Architecture:** One package (`internal/adapter/claude`) with one file per concern. The adapter consumes a `source.Canonical` and emits a `[]adapter.FileOp` ready for atomic write. Settings/`.claude.json` merging uses a JSON-pointer-based AST walk: opensync only touches keys it has written before (tracked in M0's `state.Keys`); foreign keys flow through unchanged.

**Tech stack:** Stdlib `encoding/json` for Claude's strict-JSON files; `pelletier/go-toml/v2` already vendored from M0; `os/exec` to detect Claude installation.

---

## Files created in this milestone

```
internal/adapter/claude/
├── claude.go              # Adapter struct, Name(), Capabilities(), Detect()
├── paths.go               # Resolves ~/.claude/, settings.json, .claude.json paths
├── render.go              # Render(): canonical -> []FileOp
├── render_test.go
├── ingest.go              # Ingest(): disk -> source.Canonical
├── ingest_test.go
├── apply.go               # Apply([]FileOp) -> writes via iox.AtomicWrite
├── apply_test.go
├── settings.go            # JSON key-level merge for settings.json + .claude.json
├── settings_test.go
├── skill.go               # SKILL.md frontmatter + body passthrough
├── skill_test.go
├── memory.go              # CLAUDE.md rendering from canonical Memory
├── subagent.go            # agents/<name>.md from canonical
├── command.go             # commands/<name>.md from canonical
├── hook.go                # hooks JSON appended to settings.json
├── lsp.go                 # lspServers JSON appended to settings.json
├── frontmatter.go         # YAML frontmatter parser shared by skill/subagent
└── frontmatter_test.go
```

Plus modifications:
- `internal/cli/registry_internal.go` — wire `claude.New()` for the "claude" name
- `testdata/claude/<case>/{source,expected}` — golden fixtures

---

## Task 1: `claude.Adapter` skeleton + `Detect()`

**Files:**
- Create: `internal/adapter/claude/{claude.go,paths.go}`
- Create: `internal/adapter/claude/claude_test.go`

`Adapter` struct + paths helper. `Detect()` returns true if `~/.claude/` exists OR `claude` is on PATH.

- [ ] **Step 1.1: Write the failing test**

`internal/adapter/claude/claude_test.go`:

```go
package claude_test

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/claude"
)

func TestAdapter_Identity(t *testing.T) {
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    if a.Name() != "claude" {
        t.Fatalf("Name = %q", a.Name())
    }
    if a.Capabilities() == 0 {
        t.Fatalf("expected non-zero capabilities")
    }
    var _ adapter.Adapter = a
}

func TestAdapter_DetectsHomeDir(t *testing.T) {
    tmp := t.TempDir()
    _ = os.MkdirAll(filepath.Join(tmp, ".claude"), 0o755)

    a := claude.New(claude.Options{TargetRoot: tmp})
    ok, err := a.Detect()
    if err != nil {
        t.Fatal(err)
    }
    if !ok {
        t.Fatalf("expected Detect=true when ~/.claude/ exists")
    }
}

func TestAdapter_DetectsAbsentReturnsFalse(t *testing.T) {
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    ok, _ := a.Detect()
    if ok {
        t.Fatalf("expected Detect=false on empty home")
    }
}
```

- [ ] **Step 1.2: Run; verify undefined**

`undefined: claude.New, claude.Options`

- [ ] **Step 1.3: Implement**

`internal/adapter/claude/paths.go`:

```go
package claude

import "path/filepath"

// Paths resolves the destination paths for a given (scope, project, target-root).
type Paths struct {
    Home            string // ~/.claude
    Settings        string // ~/.claude/settings.json
    DotClaude       string // ~/.claude.json (mcpServers + plugin enables live here)
    SkillsDir       string // ~/.claude/skills
    AgentsDir       string // ~/.claude/agents
    CommandsDir     string // ~/.claude/commands
    Memory          string // ~/.claude/CLAUDE.md (user scope) or <proj>/CLAUDE.md (project scope)
    PluginsCacheDir string // ~/.claude/plugins/cache
}

func ResolvePaths(targetRoot, project string, projectScope bool) Paths {
    home := filepath.Join(targetRoot, ".claude")
    p := Paths{
        Home:            home,
        Settings:        filepath.Join(home, "settings.json"),
        DotClaude:       filepath.Join(targetRoot, ".claude.json"),
        SkillsDir:       filepath.Join(home, "skills"),
        AgentsDir:       filepath.Join(home, "agents"),
        CommandsDir:     filepath.Join(home, "commands"),
        Memory:          filepath.Join(home, "CLAUDE.md"),
        PluginsCacheDir: filepath.Join(home, "plugins", "cache"),
    }
    if projectScope && project != "" {
        // project-scope settings live under <project>/.claude/
        projHome := filepath.Join(project, ".claude")
        p.Home = projHome
        p.Settings = filepath.Join(projHome, "settings.json")
        p.SkillsDir = filepath.Join(projHome, "skills")
        p.AgentsDir = filepath.Join(projHome, "agents")
        p.CommandsDir = filepath.Join(projHome, "commands")
        p.Memory = filepath.Join(project, "CLAUDE.md")
    }
    return p
}
```

`internal/adapter/claude/claude.go`:

```go
// Package claude implements the Claude Code adapter for agentsync.
package claude

import (
    "os"
    "os/exec"

    "github.com/spxrogers/agentsync/internal/adapter"
)

// Options configure the adapter at construction.
type Options struct {
    TargetRoot string // honors AGENTSYNC_TARGET_ROOT (real "/Users/x" in production)
}

// Adapter implements adapter.Adapter for Claude Code.
type Adapter struct {
    opts Options
}

// New constructs a Claude adapter.
func New(opts Options) *Adapter { return &Adapter{opts: opts} }

func (a *Adapter) Name() string { return "claude" }

func (a *Adapter) Capabilities() adapter.Capability {
    return adapter.CapMCP | adapter.CapMemory | adapter.CapSkill |
        adapter.CapSubagent | adapter.CapCommand | adapter.CapHook | adapter.CapLSP
}

func (a *Adapter) Detect() (bool, error) {
    p := ResolvePaths(a.opts.TargetRoot, "", false)
    if _, err := os.Stat(p.Home); err == nil {
        return true, nil
    }
    if _, err := exec.LookPath("claude"); err == nil {
        return true, nil
    }
    return false, nil
}
```

- [ ] **Step 1.4: Run; verify pass; commit**

```bash
go test -race ./internal/adapter/claude/...
```

```bash
git add internal/adapter/claude
git commit -m "$(cat <<'EOF'
feat(adapter/claude): adapter skeleton + Detect()

Capabilities cover all 7 component types (full-spectrum). Detect prefers
filesystem evidence (~/.claude exists) and falls back to PATH.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Per-key JSON merge for `settings.json`

**Files:**
- Create: `internal/adapter/claude/settings.go`, `internal/adapter/claude/settings_test.go`

The hardest single piece of M1. Claude's `settings.json` is shared between Claude Code itself, the user's hand edits, and agentsync. agentsync owns specific JSON pointers (e.g. `$.mcpServers.<id>`, `$.hooks.<event>[*]`, `$.lspServers.<id>`); everything else must be left untouched.

**Contract:**
- `MergeKeys(existing, ours map[string]any, ownedPointers []string) (merged map[string]any, kept, removed []string)`
- `existing` = raw parse of the on-disk file
- `ours` = the keys agentsync wants to write (also as map)
- `ownedPointers` = JSON pointers (`/mcpServers/github`, `/hooks/PreToolUse/0`) that opensync wrote *last apply* — these are reclaimable
- Returns merged map: foreign keys preserved; owned-but-now-absent pointers deleted; new opensync pointers from `ours` overlaid.

- [ ] **Step 2.1: Test**

`internal/adapter/claude/settings_test.go`:

```go
package claude_test

import (
    "encoding/json"
    "reflect"
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter/claude"
)

func decode(s string) map[string]any {
    var m map[string]any
    _ = json.Unmarshal([]byte(s), &m)
    return m
}

func TestMergeKeys_NewKey(t *testing.T) {
    existing := decode(`{"foreign": {"keep": true}}`)
    ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)

    merged, _, _ := claude.MergeKeys(existing, ours, nil)
    if _, ok := merged["foreign"]; !ok {
        t.Fatalf("foreign key dropped: %+v", merged)
    }
    if _, ok := merged["mcpServers"].(map[string]any)["github"]; !ok {
        t.Fatalf("ours not added: %+v", merged)
    }
}

func TestMergeKeys_ForeignKeyAtSameLevelPreserved(t *testing.T) {
    existing := decode(`{"mcpServers": {"foreign-server": {"command": "x"}}}`)
    ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)

    merged, _, _ := claude.MergeKeys(existing, ours, nil)
    s := merged["mcpServers"].(map[string]any)
    if _, ok := s["foreign-server"]; !ok {
        t.Fatalf("sibling foreign mcpServers entry dropped: %+v", s)
    }
    if _, ok := s["github"]; !ok {
        t.Fatalf("our entry missing: %+v", s)
    }
}

func TestMergeKeys_OrphanRemoval(t *testing.T) {
    existing := decode(`{"mcpServers": {"github": {"command": "old"}, "stale": {"command": "x"}}}`)
    ours := decode(`{"mcpServers": {"github": {"command": "npx"}}}`)
    owned := []string{"/mcpServers/github", "/mcpServers/stale"} // both are ours per state

    merged, _, removed := claude.MergeKeys(existing, ours, owned)

    if len(removed) != 1 || removed[0] != "/mcpServers/stale" {
        t.Fatalf("expected /mcpServers/stale removed, got %v", removed)
    }
    s := merged["mcpServers"].(map[string]any)
    if _, ok := s["stale"]; ok {
        t.Fatalf("stale should be deleted: %+v", s)
    }
    if reflect.DeepEqual(s["github"].(map[string]any)["command"], "old") {
        t.Fatalf("github not updated to new value: %+v", s["github"])
    }
}

func TestMergeKeys_ForeignNotInOwnedListPreserved(t *testing.T) {
    existing := decode(`{"mcpServers": {"foreign": {"command": "x"}}}`)
    ours := decode(`{}`) // we removed everything
    owned := []string{"/mcpServers/old-mine"}

    merged, _, removed := claude.MergeKeys(existing, ours, owned)
    s := merged["mcpServers"].(map[string]any)
    if _, ok := s["foreign"]; !ok {
        t.Fatalf("foreign mcpServers entry must survive: %+v", s)
    }
    if len(removed) != 0 {
        t.Fatalf("we owned old-mine but it wasn't in existing; nothing to remove. Got %v", removed)
    }
}
```

- [ ] **Step 2.2: Run; verify undefined**

- [ ] **Step 2.3: Implement**

`internal/adapter/claude/settings.go`:

```go
package claude

import (
    "strings"
)

// MergeKeys merges ours into existing, removing ownedPointers that are no
// longer in ours. Returns the merged map plus diagnostic lists.
//
// kept: pointers from ownedPointers that are still present in ours.
// removed: pointers from ownedPointers that are absent from ours and were
// deleted from existing.
//
// JSON pointer syntax: leading "/", "/" separated path segments. RFC 6901
// escapes ("~0" for "~", "~1" for "/") are supported.
func MergeKeys(existing, ours map[string]any, ownedPointers []string) (map[string]any, []string, []string) {
    merged := deepCopyMap(existing)
    if merged == nil {
        merged = map[string]any{}
    }

    // Step 1: overlay ours onto merged
    for k, v := range ours {
        switch ev := merged[k].(type) {
        case map[string]any:
            if vv, ok := v.(map[string]any); ok {
                merged[k] = mergeMaps(ev, vv)
                continue
            }
        }
        merged[k] = v
    }

    // Step 2: walk ownedPointers; if a pointer is no longer present in `ours`,
    // delete it from merged. If still present, mark kept.
    var kept, removed []string
    for _, p := range ownedPointers {
        if pointerExists(ours, p) {
            kept = append(kept, p)
            continue
        }
        if pointerExists(merged, p) {
            deletePointer(merged, p)
            removed = append(removed, p)
        }
    }
    return merged, kept, removed
}

func mergeMaps(a, b map[string]any) map[string]any {
    out := deepCopyMap(a)
    for k, v := range b {
        switch existing := out[k].(type) {
        case map[string]any:
            if vv, ok := v.(map[string]any); ok {
                out[k] = mergeMaps(existing, vv)
                continue
            }
        }
        out[k] = v
    }
    return out
}

func deepCopyMap(m map[string]any) map[string]any {
    if m == nil {
        return nil
    }
    out := make(map[string]any, len(m))
    for k, v := range m {
        if mm, ok := v.(map[string]any); ok {
            out[k] = deepCopyMap(mm)
        } else {
            out[k] = v
        }
    }
    return out
}

func pointerExists(m map[string]any, ptr string) bool {
    parts := splitPointer(ptr)
    var cur any = m
    for _, p := range parts {
        mp, ok := cur.(map[string]any)
        if !ok {
            return false
        }
        cur, ok = mp[p]
        if !ok {
            return false
        }
    }
    return true
}

func deletePointer(m map[string]any, ptr string) {
    parts := splitPointer(ptr)
    if len(parts) == 0 {
        return
    }
    cur := m
    for i, p := range parts {
        if i == len(parts)-1 {
            delete(cur, p)
            return
        }
        next, ok := cur[p].(map[string]any)
        if !ok {
            return
        }
        cur = next
    }
}

func splitPointer(ptr string) []string {
    ptr = strings.TrimPrefix(ptr, "/")
    if ptr == "" {
        return nil
    }
    raw := strings.Split(ptr, "/")
    out := make([]string, len(raw))
    for i, s := range raw {
        s = strings.ReplaceAll(s, "~1", "/")
        s = strings.ReplaceAll(s, "~0", "~")
        out[i] = s
    }
    return out
}
```

(Note: this v1 implementation handles object keys but not array indices in pointers — sufficient for `mcpServers`/`lspServers` which are objects. Hooks structure under Claude is `{"hooks":{"PreToolUse":[...]}}` arrays; M1 hook ownership is at the *event-name* level (`/hooks/PreToolUse`), not array-index granularity, so the simpler implementation works. Array-index support can be added in a later milestone if drift across arrays becomes painful.)

- [ ] **Step 2.4: Run; verify pass; commit**

```bash
go test -race ./internal/adapter/claude/...
```

```bash
git add internal/adapter/claude
git commit -m "$(cat <<'EOF'
feat(adapter/claude): per-key JSON merge for settings.json + .claude.json

Foreign keys survive verbatim. ownedPointers list (read from
state.Keys at apply-time) controls reclamation: keys we wrote last
apply but no longer want are deleted; foreign keys at the same level
preserved. Object-pointer support only; array-index drift can be
deferred to a future milestone since hooks track at event-name level.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: YAML frontmatter parser

**Files:**
- Create: `internal/adapter/claude/frontmatter.go`, `internal/adapter/claude/frontmatter_test.go`

Skills, subagents, and slash commands all use the convention `---\n<YAML>\n---\n<body>`. Need a tiny parser that returns frontmatter as `map[string]any` and the body as a string.

- [ ] **Step 3.1: Add YAML dependency**

```bash
go get sigs.k8s.io/yaml@latest
```

(`sigs.k8s.io/yaml` parses YAML by converting to JSON first, so it returns `map[string]any` directly — exactly the shape we need to round-trip without losing keys.)

- [ ] **Step 3.2: Test**

`internal/adapter/claude/frontmatter_test.go`:

```go
package claude_test

import (
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter/claude"
)

func TestParseFrontmatter_Standard(t *testing.T) {
    input := []byte(`---
name: my-skill
description: Does the thing
disable-model-invocation: true
---
This is the body.

Multiple lines.
`)
    fm, body, err := claude.ParseFrontmatter(input)
    if err != nil {
        t.Fatal(err)
    }
    if fm["name"] != "my-skill" {
        t.Fatalf("name = %v", fm["name"])
    }
    if fm["disable-model-invocation"] != true {
        t.Fatalf("disable-model-invocation = %v", fm["disable-model-invocation"])
    }
    if body != "This is the body.\n\nMultiple lines.\n" {
        t.Fatalf("body mismatch: %q", body)
    }
}

func TestParseFrontmatter_NoFrontmatter(t *testing.T) {
    fm, body, err := claude.ParseFrontmatter([]byte("plain markdown\n"))
    if err != nil {
        t.Fatal(err)
    }
    if len(fm) != 0 {
        t.Fatalf("fm should be empty: %+v", fm)
    }
    if body != "plain markdown\n" {
        t.Fatalf("body = %q", body)
    }
}

func TestEncodeFrontmatter_Roundtrip(t *testing.T) {
    fm := map[string]any{"name": "x", "description": "y"}
    out, err := claude.EncodeFrontmatter(fm, "body")
    if err != nil {
        t.Fatal(err)
    }
    fm2, body2, err := claude.ParseFrontmatter(out)
    if err != nil {
        t.Fatal(err)
    }
    if fm2["name"] != "x" || fm2["description"] != "y" {
        t.Fatalf("roundtrip lost data: %+v", fm2)
    }
    if body2 != "body" {
        t.Fatalf("body = %q", body2)
    }
}
```

- [ ] **Step 3.3: Implement**

`internal/adapter/claude/frontmatter.go`:

```go
package claude

import (
    "bytes"
    "fmt"
    "strings"

    "sigs.k8s.io/yaml"
)

// ParseFrontmatter extracts the YAML frontmatter and the markdown body. If
// the input doesn't begin with "---\n", returns an empty map and the entire
// input as body.
func ParseFrontmatter(data []byte) (map[string]any, string, error) {
    if !bytes.HasPrefix(data, []byte("---\n")) {
        return map[string]any{}, string(data), nil
    }
    rest := data[len("---\n"):]
    end := bytes.Index(rest, []byte("\n---\n"))
    if end < 0 {
        return nil, "", fmt.Errorf("unterminated frontmatter")
    }
    yml := rest[:end]
    body := rest[end+len("\n---\n"):]

    var fm map[string]any
    if err := yaml.Unmarshal(yml, &fm); err != nil {
        return nil, "", fmt.Errorf("parse yaml frontmatter: %w", err)
    }
    if fm == nil {
        fm = map[string]any{}
    }
    return fm, string(body), nil
}

// EncodeFrontmatter writes "---\n<yaml>\n---\n<body>" with the keys in fm.
// fm is empty: returns just the body.
func EncodeFrontmatter(fm map[string]any, body string) ([]byte, error) {
    if len(fm) == 0 {
        return []byte(body), nil
    }
    yml, err := yaml.Marshal(fm)
    if err != nil {
        return nil, fmt.Errorf("encode yaml: %w", err)
    }
    var buf strings.Builder
    buf.WriteString("---\n")
    buf.Write(yml)
    buf.WriteString("---\n")
    buf.WriteString(body)
    return []byte(buf.String()), nil
}
```

- [ ] **Step 3.4: Run; commit**

```bash
go test -race ./internal/adapter/claude/...
git add go.mod go.sum internal/adapter/claude
git commit -m "$(cat <<'EOF'
feat(adapter/claude): YAML frontmatter parse/encode round-trip

Used by skill/subagent/command. sigs.k8s.io/yaml goes via JSON so
parsed frontmatter is map[string]any (round-trip safe even for keys we
don't recognize, like disable-model-invocation).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Update `source.Skill` loader to parse frontmatter

**Files:**
- Modify: `internal/source/loader.go` (extend `loadSkills`)
- Modify: `internal/source/loader_test.go` (add frontmatter assertion)

In M0 we kept skill body as one big string. Now M1 needs the frontmatter map separately.

- [ ] **Step 4.1: Test addition**

Append to `internal/source/loader_test.go`:

```go
func TestLoad_SkillFrontmatter(t *testing.T) {
    fs := afero.NewMemMapFs()
    _ = afero.WriteFile(fs, "/home/.agentsync/skills/foo/SKILL.md", []byte(`---
name: foo
description: Test skill
---
Body.
`), 0o644)

    c, err := source.Load(fs, "/home/.agentsync")
    if err != nil {
        t.Fatal(err)
    }
    if len(c.Skills) != 1 {
        t.Fatalf("skills = %d", len(c.Skills))
    }
    s := c.Skills[0]
    if s.Frontmatter["name"] != "foo" {
        t.Fatalf("frontmatter name = %v", s.Frontmatter["name"])
    }
    if s.Body != "Body.\n" {
        t.Fatalf("body = %q", s.Body)
    }
}
```

- [ ] **Step 4.2: Update `loadSkills`**

In `internal/source/loader.go`, replace the existing `loadSkills` body:

```go
func loadSkills(fs afero.Fs, home string) ([]Skill, error) {
    dir := filepath.Join(home, "skills")
    entries, err := afero.ReadDir(fs, dir)
    if err != nil {
        if errors.Is(err, os.ErrNotExist) {
            return nil, nil
        }
        return nil, fmt.Errorf("read %s: %w", dir, err)
    }
    var out []Skill
    for _, e := range entries {
        if !e.IsDir() {
            continue
        }
        raw, err := afero.ReadFile(fs, filepath.Join(dir, e.Name(), "SKILL.md"))
        if err != nil {
            if errors.Is(err, os.ErrNotExist) {
                continue
            }
            return nil, fmt.Errorf("read SKILL.md for %s: %w", e.Name(), err)
        }
        fm, body, err := parseFrontmatter(raw)
        if err != nil {
            return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
        }
        out = append(out, Skill{Name: e.Name(), Frontmatter: fm, Body: body})
    }
    return out, nil
}
```

…and add a small `parseFrontmatter` helper to the `source` package (mirrored from M1 task 3 — kept in `source` to avoid the source package importing the claude adapter). Add to `loader.go`:

```go
import (
    // ...existing imports...
    "bytes"
    "sigs.k8s.io/yaml"
)

func parseFrontmatter(data []byte) (map[string]any, string, error) {
    if !bytes.HasPrefix(data, []byte("---\n")) {
        return map[string]any{}, string(data), nil
    }
    rest := data[len("---\n"):]
    end := bytes.Index(rest, []byte("\n---\n"))
    if end < 0 {
        return nil, "", fmt.Errorf("unterminated frontmatter")
    }
    yml := rest[:end]
    body := rest[end+len("\n---\n"):]
    var fm map[string]any
    if err := yaml.Unmarshal(yml, &fm); err != nil {
        return nil, "", fmt.Errorf("parse yaml frontmatter: %w", err)
    }
    if fm == nil {
        fm = map[string]any{}
    }
    return fm, string(body), nil
}
```

- [ ] **Step 4.3: Run; commit**

```bash
go test -race ./internal/source/...
git add internal/source
git commit -m "$(cat <<'EOF'
feat(source): parse skill frontmatter into Skill.Frontmatter

Frontmatter is map[string]any so unknown keys (disable-model-invocation,
custom vendor extensions) round-trip without loss.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: `Render()` MCP servers

**Files:**
- Create: `internal/adapter/claude/render.go`, `internal/adapter/claude/render_test.go`

For each `MCPServer` in canonical, emit one settings.json key under `/mcpServers/<id>` (or `~/.claude.json` `/mcpServers/<id>` for user-scope MCP — Claude reads both, but `~/.claude.json` is the user-scope authority for MCP).

- [ ] **Step 5.1: Test**

`internal/adapter/claude/render_test.go`:

```go
package claude_test

import (
    "encoding/json"
    "strings"
    "testing"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/claude"
    "github.com/spxrogers/agentsync/internal/source"
)

func TestRender_MCP_UserScope(t *testing.T) {
    enabled := true
    c := source.Canonical{
        MCPServers: []source.MCPServer{{
            ID: "github",
            Server: source.MCPServerSpec{
                Type:    "stdio",
                Command: "npx",
                Args:    []string{"-y", "@modelcontextprotocol/server-github"},
                Env:     map[string]string{"GITHUB_TOKEN": "xyz"},
                Agents:  []string{"*"},
                Enabled: &enabled,
            },
        }},
    }
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    ops, skips, err := a.Render(c, adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    if len(skips) != 0 {
        t.Fatalf("unexpected skips: %+v", skips)
    }
    // The MCP write goes into .claude.json under user scope.
    var found bool
    for _, op := range ops {
        if strings.HasSuffix(op.Path, ".claude.json") {
            found = true
            var got map[string]any
            if err := json.Unmarshal(op.Content, &got); err != nil {
                t.Fatalf("not valid json: %v", err)
            }
            srv := got["mcpServers"].(map[string]any)["github"].(map[string]any)
            if srv["command"] != "npx" {
                t.Fatalf("command = %v", srv["command"])
            }
        }
    }
    if !found {
        t.Fatalf(".claude.json op not produced: %+v", ops)
    }
}

func TestRender_MCP_AgentsAllowlist(t *testing.T) {
    enabled := true
    c := source.Canonical{
        MCPServers: []source.MCPServer{{
            ID: "private",
            Server: source.MCPServerSpec{
                Type:    "stdio",
                Command: "x",
                Agents:  []string{"opencode"}, // claude not in list
                Enabled: &enabled,
            },
        }},
    }
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    ops, _, err := a.Render(c, adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    // No .claude.json write because the server isn't targeted at claude.
    for _, op := range ops {
        if strings.HasSuffix(op.Path, ".claude.json") {
            t.Fatalf("expected no .claude.json op when allowlist excludes claude")
        }
    }
}
```

- [ ] **Step 5.2: Render skeleton**

`internal/adapter/claude/render.go`:

```go
package claude

import (
    "encoding/json"
    "fmt"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

// Render produces the full set of FileOps for a given canonical model.
// Pure function: returns the same output for the same input.
func (a *Adapter) Render(c source.Canonical, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
    paths := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)

    var ops []adapter.FileOp
    var skips []adapter.Skip

    // 1. MCP -> .claude.json (user) or settings.json (project)
    if mcpOps, err := a.renderMCP(c, paths, scope); err != nil {
        return nil, nil, err
    } else {
        ops = append(ops, mcpOps...)
    }

    // 2-7: implemented in Tasks 6-11.

    return ops, skips, nil
}

func (a *Adapter) renderMCP(c source.Canonical, p Paths, scope adapter.Scope) ([]adapter.FileOp, error) {
    targeted := map[string]map[string]any{}
    for _, m := range c.MCPServers {
        if m.Server.Enabled != nil && !*m.Server.Enabled {
            continue
        }
        if !agentTargeted("claude", m.Server.Agents) {
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
        if len(m.Server.Headers) > 0 {
            spec["headers"] = m.Server.Headers
        }
        targeted[m.ID] = spec
    }
    if len(targeted) == 0 {
        return nil, nil
    }
    obj := map[string]any{"mcpServers": targeted}

    var dest, sourceID string
    if scope == adapter.ScopeProject {
        dest = p.Settings // project-scope settings.json holds mcpServers
    } else {
        dest = p.DotClaude
    }
    sourceID = "mcp/* (multiple)"

    body, err := json.MarshalIndent(obj, "", "  ")
    if err != nil {
        return nil, fmt.Errorf("marshal mcp: %w", err)
    }
    return []adapter.FileOp{{
        Action:   "write",
        Path:     dest,
        Content:  append(body, '\n'),
        Mode:     0o644,
        SourceID: sourceID,
    }}, nil
}

// agentTargeted reports whether agents allowlist includes us. Empty / nil
// list / "*" entry means everyone.
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

(Note: this initial render doesn't yet *merge* into existing files. Apply (Task 12) reads existing settings.json/.claude.json from disk, calls MergeKeys with the rendered ours, then atomically writes the merged result. That's what makes per-key ownership real. For Render's contract — which is pure — we just emit the "ours" payload; the merge happens in apply.)

…actually that contract makes Render less clean: a downstream consumer calling Render expects the FileOp.Content to be the FINAL bytes to write, not a "your half." Better contract: Render computes the final bytes (merging disk state inline). That requires Render to read the on-disk settings.json. Acceptable since Render needs `targetRoot` anyway.

Update the contract: Render reads disk for shared files (`settings.json`, `.claude.json`) and produces post-merge content. Pure on canonical+disk inputs. Move on.

Refactor `renderMCP`:

```go
func (a *Adapter) renderMCP(c source.Canonical, p Paths, scope adapter.Scope, ownedKeys []string) ([]adapter.FileOp, error) {
    targeted := map[string]any{}
    // ...same loop building `targeted`...

    if len(targeted) == 0 && len(ownedKeys) == 0 {
        return nil, nil
    }

    var dest string
    if scope == adapter.ScopeProject {
        dest = p.Settings
    } else {
        dest = p.DotClaude
    }
    existing := readJSONFile(dest) // returns empty map if missing
    ours := map[string]any{"mcpServers": targeted}
    merged, _, _ := MergeKeys(existing, ours, ownedKeys)

    body, err := json.MarshalIndent(merged, "", "  ")
    if err != nil {
        return nil, fmt.Errorf("marshal mcp: %w", err)
    }
    return []adapter.FileOp{{
        Action:   "write",
        Path:     dest,
        Content:  append(body, '\n'),
        Mode:     0o644,
        SourceID: "mcp/* (multiple)",
    }}, nil
}

func readJSONFile(path string) map[string]any {
    data, err := os.ReadFile(path)
    if err != nil {
        return map[string]any{}
    }
    var m map[string]any
    _ = json.Unmarshal(data, &m)
    if m == nil {
        return map[string]any{}
    }
    return m
}
```

`ownedKeys` flows into Render via the apply path that has access to the state store — so Render's call signature must change. Update the `Adapter.Render` signature in `internal/adapter/adapter.go` to accept owned keys:

Actually a cleaner solution: keep `Render` signature unchanged; Render produces "ours" bytes to a separate FileOp that signals "merge target." Apply pipeline (Task 12) does the merge using state. Trade-off: Render is no longer pure (it doesn't compute final bytes), but the adapter interface stays clean.

Decision: extend `adapter.FileOp` with a `MergeStrategy` field. Values: `"replace"` (whole-file write) or `"merge-json-keys"` (apply does merge). Render sets the strategy; Apply respects it.

Update `internal/adapter/adapter.go`:

```go
type FileOp struct {
    Action        string
    Path          string
    Content       []byte
    Mode          uint32
    SourceID      string
    MergeStrategy string   // "replace" (default) | "merge-json-keys"
    OwnedKeys     []string // populated by Apply from state.Keys, not by Render
}
```

Render emits FileOp with MergeStrategy set. Apply reads existing on-disk file, parses, calls MergeKeys, writes atomically.

This keeps Render pure and pushes state-dependence to Apply (which is naturally state-dependent because it persists).

Apply signature:

```go
func (a *Adapter) Apply(ops []adapter.FileOp) error {
    for _, op := range ops {
        switch op.MergeStrategy {
        case "merge-json-keys":
            existing := readJSONFile(op.Path)
            var ours map[string]any
            _ = json.Unmarshal(op.Content, &ours)
            merged, _, _ := MergeKeys(existing, ours, op.OwnedKeys)
            body, _ := json.MarshalIndent(merged, "", "  ")
            return iox.AtomicWrite(op.Path, append(body, '\n'), os.FileMode(op.Mode))
        default:
            return iox.AtomicWrite(op.Path, op.Content, os.FileMode(op.Mode))
        }
    }
    return nil
}
```

`OwnedKeys` is populated by the apply pipeline (in M3 / state integration); for M1 the Apply implementation works correctly with `OwnedKeys=nil` (no orphan removal but no mistakes either).

- [ ] **Step 5.3: Update render_test to expect MergeStrategy**

Adjust `TestRender_MCP_UserScope` to assert `op.MergeStrategy == "merge-json-keys"` and that the rendered Content is valid JSON containing only `{"mcpServers": {...}}`.

- [ ] **Step 5.4: Add `MergeStrategy` and `OwnedKeys` to `adapter.FileOp`**

- [ ] **Step 5.5: Run; commit**

```bash
go test -race ./internal/adapter/claude/... ./internal/adapter/...
git add internal/adapter
git commit -m "$(cat <<'EOF'
feat(adapter/claude): Render MCP servers with merge-json-keys strategy

FileOp gains MergeStrategy + OwnedKeys; Render emits "ours" payload, Apply
performs the merge against existing on-disk content. Owned-key tracking
wired through in Task 12.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: `Render()` memory (CLAUDE.md)

**Files:**
- Create: `internal/adapter/claude/memory.go`
- Modify: `internal/adapter/claude/render.go` (call renderMemory)
- Modify: `internal/adapter/claude/render_test.go` (test memory render)

Concatenates `memory/AGENTS.md` body with resolved fragments (replacing `@import ./fragments/style.md` with the fragment's content). Writes to `~/.claude/CLAUDE.md` (user) or `<project>/CLAUDE.md` (project).

- [ ] **Step 6.1: Test**

```go
func TestRender_Memory(t *testing.T) {
    c := source.Canonical{
        Memory: source.Memory{
            Body: "# Personal style\n\n@import ./fragments/style.md\n\nMore.\n",
            Fragments: map[string]string{
                "style.md": "Use semicolons.\n",
            },
        },
    }
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    ops, _, err := a.Render(c, adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    var memOp *adapter.FileOp
    for i, op := range ops {
        if strings.HasSuffix(op.Path, "/CLAUDE.md") {
            memOp = &ops[i]
        }
    }
    if memOp == nil {
        t.Fatalf("no CLAUDE.md op")
    }
    if !strings.Contains(string(memOp.Content), "Use semicolons.") {
        t.Fatalf("fragment not inlined: %s", memOp.Content)
    }
    if strings.Contains(string(memOp.Content), "@import") {
        t.Fatalf("@import directive leaked: %s", memOp.Content)
    }
}
```

- [ ] **Step 6.2: Implement**

`internal/adapter/claude/memory.go`:

```go
package claude

import (
    "regexp"
    "strings"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

var importRe = regexp.MustCompile(`(?m)^@import\s+\./fragments/(\S+)\s*$`)

func (a *Adapter) renderMemory(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
    if c.Memory.Body == "" {
        return nil, nil
    }
    body := importRe.ReplaceAllStringFunc(c.Memory.Body, func(line string) string {
        m := importRe.FindStringSubmatch(line)
        if len(m) < 2 {
            return line
        }
        if frag, ok := c.Memory.Fragments[m[1]]; ok {
            return strings.TrimRight(frag, "\n")
        }
        // Unknown fragment; preserve line so the user notices.
        return line
    })
    return []adapter.FileOp{{
        Action:        "write",
        Path:          p.Memory,
        Content:       []byte(body),
        Mode:          0o644,
        SourceID:      "memory/AGENTS.md",
        MergeStrategy: "replace",
    }}, nil
}
```

Wire into `Render()`:

```go
if memOps, err := a.renderMemory(c, paths); err != nil { return nil, nil, err } else { ops = append(ops, memOps...) }
```

- [ ] **Step 6.3: Commit**

```bash
git add internal/adapter/claude
git commit -m "feat(adapter/claude): render CLAUDE.md with @import resolution

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 7: `Render()` skills (write SKILL.md to ~/.claude/skills/<name>/)

Each canonical Skill yields one FileOp writing `<SkillsDir>/<name>/SKILL.md` with frontmatter + body re-encoded via `EncodeFrontmatter`.

- [ ] **Step 7.1: Test**

```go
func TestRender_Skills(t *testing.T) {
    c := source.Canonical{
        Skills: []source.Skill{{
            Name:        "review",
            Frontmatter: map[string]any{"name": "review", "description": "Review code"},
            Body:        "Do a code review.\n",
        }},
    }
    a := claude.New(claude.Options{TargetRoot: t.TempDir()})
    ops, _, _ := a.Render(c, adapter.ScopeUser, "")
    var found *adapter.FileOp
    for i, op := range ops {
        if strings.Contains(op.Path, "/skills/review/SKILL.md") {
            found = &ops[i]
        }
    }
    if found == nil {
        t.Fatalf("no SKILL.md op")
    }
    if !strings.HasPrefix(string(found.Content), "---\n") {
        t.Fatalf("missing frontmatter delimiter: %s", found.Content)
    }
}
```

- [ ] **Step 7.2: Implement**

`internal/adapter/claude/skill.go`:

```go
package claude

import (
    "fmt"
    "path/filepath"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderSkills(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
    var ops []adapter.FileOp
    for _, s := range c.Skills {
        body, err := EncodeFrontmatter(s.Frontmatter, s.Body)
        if err != nil {
            return nil, fmt.Errorf("encode skill %s: %w", s.Name, err)
        }
        ops = append(ops, adapter.FileOp{
            Action:        "write",
            Path:          filepath.Join(p.SkillsDir, s.Name, "SKILL.md"),
            Content:       body,
            Mode:          0o644,
            SourceID:      filepath.Join("skills", s.Name, "SKILL.md"),
            MergeStrategy: "replace",
        })
    }
    return ops, nil
}
```

Wire into `Render()`. Commit:

```bash
git commit -am "feat(adapter/claude): render skills (frontmatter passthrough)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Task 8: `Render()` subagents

Subagents in Claude live at `~/.claude/agents/<name>.md` with frontmatter (`description`, `tools`, `model`, `color`). Same canonical Skill-like type but stored separately.

Add `Subagents []Skill` to `source.Canonical` (since they share the markdown+frontmatter shape; Skill is a misleading type name, but adapters can disambiguate). Actually — better — add a distinct `Subagent` type to `internal/source/schema.go` for clarity:

```go
type Subagent struct {
    Name        string
    Frontmatter map[string]any
    Body        string
}
```

…and `Subagents []Subagent` on `Canonical`. Loader walks `<home>/agents/<name>.md`.

- [ ] **Step 8.1: Update source schema + loader**

Add `Subagent` type to `internal/source/schema.go`. Add `loadSubagents` mirroring `loadSkills` but reading `<home>/agents/*.md`. Test in `loader_test.go`. Commit.

- [ ] **Step 8.2: Render**

`internal/adapter/claude/subagent.go`:

```go
package claude

import (
    "fmt"
    "path/filepath"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) renderSubagents(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
    var ops []adapter.FileOp
    for _, s := range c.Subagents {
        body, err := EncodeFrontmatter(s.Frontmatter, s.Body)
        if err != nil {
            return nil, fmt.Errorf("encode subagent %s: %w", s.Name, err)
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
    return ops, nil
}
```

Test, wire into Render, commit.

---

## Task 9: `Render()` slash commands

Mirror Task 8 for `Commands []Command` on `source.Canonical` and `<home>/commands/*.md`. Path: `<CommandsDir>/<name>.md`. Frontmatter passthrough.

Test, implement, wire, commit.

---

## Task 10: `Render()` hooks (settings.json `/hooks/<event>`)

Hooks differ from MCP: they go into `settings.json` not `.claude.json`, and the structure is `{"hooks": {"PreToolUse": [{matcher, hooks: [{type, command}]}]}}`. agentsync owns the entire `/hooks/<event>` array per event.

- [ ] **Step 10.1: Add `Hooks` to `source.Canonical`**

```go
type Hook struct {
    Event   string  // e.g. "PreToolUse"
    Matcher string  // glob/regex string
    Type    string  // "command"
    Command string  // shell command
}
```

…and `Hooks []Hook` on `Canonical`. Loader: from a top-level `hooks/<event>.toml` if you want them in canonical, OR from individual plugin entries (M4 territory). For M1, support `hooks/<event>.toml` with array of `{matcher, type, command}` entries:

```toml
# hooks/PreToolUse.toml
[[hook]]
matcher = "Write|Edit"
type    = "command"
command = "echo intercepting Write/Edit"
```

Test the loader. Commit.

- [ ] **Step 10.2: Render hooks**

`internal/adapter/claude/hook.go`:

```go
package claude

import (
    "encoding/json"
    "fmt"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

// renderHooks writes a single op for settings.json containing /hooks/<event>
// entries. Per-event ownership: agentsync owns the entire array under its
// event key. Foreign event keys (e.g. PreToolUse if user has authored
// directly) are NOT touched if they're not in canonical.
func (a *Adapter) renderHooks(c source.Canonical, p Paths) ([]adapter.FileOp, error) {
    if len(c.Hooks) == 0 {
        return nil, nil
    }
    byEvent := map[string][]map[string]any{}
    for _, h := range c.Hooks {
        entry := map[string]any{
            "matcher": h.Matcher,
            "hooks": []map[string]any{{
                "type":    h.Type,
                "command": h.Command,
            }},
        }
        byEvent[h.Event] = append(byEvent[h.Event], entry)
    }
    obj := map[string]any{"hooks": byEvent}
    body, err := json.MarshalIndent(obj, "", "  ")
    if err != nil {
        return nil, fmt.Errorf("marshal hooks: %w", err)
    }
    return []adapter.FileOp{{
        Action:        "write",
        Path:          p.Settings,
        Content:       append(body, '\n'),
        Mode:          0o644,
        SourceID:      "hooks/* (multiple)",
        MergeStrategy: "merge-json-keys",
    }}, nil
}
```

Test (foreign hooks event preserved); commit.

---

## Task 11: `Render()` LSP servers

Mirror MCP at `/lspServers/<id>` in `settings.json`. Same structure (command/args/env/url/headers). Same merge strategy.

- [ ] Add `LSPServer` type to `source.Canonical`. Loader from `lsp/<id>.toml`. Render to `/lspServers/<id>` in `settings.json`. Test, wire, commit.

---

## Task 12: `Apply()` — write FileOps with merge-aware logic

**Files:**
- Create: `internal/adapter/claude/apply.go`, `internal/adapter/claude/apply_test.go`

The merge-aware write loop. For each FileOp: if `MergeStrategy == "merge-json-keys"`, read existing JSON, parse our content, MergeKeys (with OwnedKeys passed in), write merged. Otherwise atomic-write Content directly.

- [ ] **Step 12.1: Test**

```go
func TestApply_NewSettings_WritesContent(t *testing.T) {
    tmp := t.TempDir()
    a := claude.New(claude.Options{TargetRoot: tmp})

    op := adapter.FileOp{
        Action:        "write",
        Path:          filepath.Join(tmp, ".claude.json"),
        Content:       []byte(`{"mcpServers":{"github":{"command":"npx"}}}`),
        Mode:          0o644,
        MergeStrategy: "merge-json-keys",
    }
    if err := a.Apply([]adapter.FileOp{op}); err != nil {
        t.Fatal(err)
    }
    body, _ := os.ReadFile(op.Path)
    if !strings.Contains(string(body), `"github"`) {
        t.Fatalf("missing github key: %s", body)
    }
}

func TestApply_PreservesForeignKeys(t *testing.T) {
    tmp := t.TempDir()
    a := claude.New(claude.Options{TargetRoot: tmp})
    target := filepath.Join(tmp, ".claude.json")

    // pre-existing foreign content
    _ = os.WriteFile(target, []byte(`{"foreign":{"x":1},"mcpServers":{"old":{}}}`), 0o644)

    op := adapter.FileOp{
        Action:        "write",
        Path:          target,
        Content:       []byte(`{"mcpServers":{"new":{"command":"x"}}}`),
        Mode:          0o644,
        MergeStrategy: "merge-json-keys",
        OwnedKeys:     nil, // no owned keys -> no orphan removal
    }
    if err := a.Apply([]adapter.FileOp{op}); err != nil {
        t.Fatal(err)
    }
    var out map[string]any
    body, _ := os.ReadFile(target)
    _ = json.Unmarshal(body, &out)
    if _, ok := out["foreign"]; !ok {
        t.Fatalf("foreign key dropped: %s", body)
    }
    s := out["mcpServers"].(map[string]any)
    if _, ok := s["old"]; !ok {
        t.Fatalf("foreign mcpServers.old dropped: %s", body)
    }
    if _, ok := s["new"]; !ok {
        t.Fatalf("our mcpServers.new missing: %s", body)
    }
}

func TestApply_OrphanRemoval(t *testing.T) {
    tmp := t.TempDir()
    a := claude.New(claude.Options{TargetRoot: tmp})
    target := filepath.Join(tmp, ".claude.json")
    _ = os.WriteFile(target, []byte(`{"mcpServers":{"github":{"command":"old"},"stale":{}}}`), 0o644)

    op := adapter.FileOp{
        Action:        "write",
        Path:          target,
        Content:       []byte(`{"mcpServers":{"github":{"command":"new"}}}`),
        Mode:          0o644,
        MergeStrategy: "merge-json-keys",
        OwnedKeys:     []string{"/mcpServers/github", "/mcpServers/stale"},
    }
    if err := a.Apply([]adapter.FileOp{op}); err != nil {
        t.Fatal(err)
    }
    var out map[string]any
    body, _ := os.ReadFile(target)
    _ = json.Unmarshal(body, &out)
    s := out["mcpServers"].(map[string]any)
    if _, ok := s["stale"]; ok {
        t.Fatalf("stale should be deleted: %s", body)
    }
    if s["github"].(map[string]any)["command"] != "new" {
        t.Fatalf("github should be updated: %s", body)
    }
}
```

- [ ] **Step 12.2: Implement**

`internal/adapter/claude/apply.go`:

```go
package claude

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
    if op.MergeStrategy == "merge-json-keys" {
        existing := readJSONFile(op.Path)
        var ours map[string]any
        if err := json.Unmarshal(op.Content, &ours); err != nil {
            return fmt.Errorf("parse our payload for %s: %w", op.Path, err)
        }
        merged, _, _ := MergeKeys(existing, ours, op.OwnedKeys)
        body, err := json.MarshalIndent(merged, "", "  ")
        if err != nil {
            return fmt.Errorf("marshal merged for %s: %w", op.Path, err)
        }
        return iox.AtomicWrite(op.Path, append(body, '\n'), mode)
    }
    return iox.AtomicWrite(op.Path, op.Content, mode)
}

func readJSONFile(path string) map[string]any {
    data, err := os.ReadFile(path)
    if err != nil {
        return map[string]any{}
    }
    var m map[string]any
    _ = json.Unmarshal(data, &m)
    if m == nil {
        return map[string]any{}
    }
    return m
}
```

- [ ] **Step 12.3: Run; commit**

```bash
go test -race ./internal/adapter/claude/...
git add internal/adapter/claude
git commit -m "$(cat <<'EOF'
feat(adapter/claude): Apply writes via iox.AtomicWrite, merge-aware for JSON

merge-json-keys ops read existing JSON, MergeKeys with OwnedKeys (orphan
removal), then write merged content. replace ops write Content directly.
delete ops remove the file (no-op if absent).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 13: `Ingest()` — read native files back into canonical

**Files:**
- Create: `internal/adapter/claude/ingest.go`, `internal/adapter/claude/ingest_test.go`

Inverse of Render. Reads `.claude.json`, settings.json, agents/, commands/, skills/, CLAUDE.md → produces a partial `source.Canonical`. Used by `agentsync import <selector>` and drift detection (M3).

For each component type, ingest the native form back. Round-trip parity (`Ingest(Render(c)) == c`) is the test target.

- [ ] **Step 13.1: Test (round-trip)**

```go
func TestIngest_RoundTripsMCPAndSkills(t *testing.T) {
    tmp := t.TempDir()
    enabled := true
    in := source.Canonical{
        MCPServers: []source.MCPServer{{
            ID: "github",
            Server: source.MCPServerSpec{
                Type: "stdio", Command: "npx", Args: []string{"-y", "x"},
                Env: map[string]string{"K": "V"}, Agents: []string{"*"},
                Enabled: &enabled,
            },
        }},
        Skills: []source.Skill{{
            Name:        "review",
            Frontmatter: map[string]any{"name": "review", "description": "x"},
            Body:        "body\n",
        }},
    }
    a := claude.New(claude.Options{TargetRoot: tmp})
    ops, _, _ := a.Render(in, adapter.ScopeUser, "")
    if err := a.Apply(ops); err != nil {
        t.Fatal(err)
    }
    out, err := a.Ingest(adapter.ScopeUser, "")
    if err != nil {
        t.Fatal(err)
    }
    if len(out.MCPServers) != 1 || out.MCPServers[0].ID != "github" {
        t.Fatalf("MCP roundtrip lost: %+v", out.MCPServers)
    }
    if len(out.Skills) != 1 || out.Skills[0].Name != "review" {
        t.Fatalf("Skill roundtrip lost: %+v", out.Skills)
    }
}
```

- [ ] **Step 13.2: Implement**

`internal/adapter/claude/ingest.go`:

```go
package claude

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
    "strings"

    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/source"
)

func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
    p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
    var c source.Canonical

    // MCP from .claude.json (user) or settings.json (project)
    var mcpFile string
    if scope == adapter.ScopeProject {
        mcpFile = p.Settings
    } else {
        mcpFile = p.DotClaude
    }
    if data, err := os.ReadFile(mcpFile); err == nil {
        var top map[string]any
        if err := json.Unmarshal(data, &top); err != nil {
            return c, fmt.Errorf("parse %s: %w", mcpFile, err)
        }
        if servers, ok := top["mcpServers"].(map[string]any); ok {
            for id, raw := range servers {
                spec, ok := raw.(map[string]any)
                if !ok {
                    continue
                }
                m := source.MCPServer{ID: id, Server: source.MCPServerSpec{
                    Type:    asStr(spec["type"]),
                    Command: asStr(spec["command"]),
                    Args:    asStrSlice(spec["args"]),
                    Env:     asStrMap(spec["env"]),
                    URL:     asStr(spec["url"]),
                    Headers: asStrMap(spec["headers"]),
                }}
                c.MCPServers = append(c.MCPServers, m)
            }
        }
    }

    // Skills
    if entries, err := os.ReadDir(p.SkillsDir); err == nil {
        for _, e := range entries {
            if !e.IsDir() {
                continue
            }
            data, err := os.ReadFile(filepath.Join(p.SkillsDir, e.Name(), "SKILL.md"))
            if err != nil {
                continue
            }
            fm, body, err := ParseFrontmatter(data)
            if err != nil {
                continue
            }
            c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body})
        }
    }

    // Subagents (agents/<name>.md), commands, hooks, lsp, memory: similar pattern
    // — see Task 13.3 for the per-component snippets.

    return c, nil
}

func asStr(v any) string { s, _ := v.(string); return s }
func asStrSlice(v any) []string {
    arr, ok := v.([]any)
    if !ok {
        return nil
    }
    out := make([]string, 0, len(arr))
    for _, x := range arr {
        if s, ok := x.(string); ok {
            out = append(out, s)
        }
    }
    return out
}
func asStrMap(v any) map[string]string {
    m, ok := v.(map[string]any)
    if !ok {
        return nil
    }
    out := make(map[string]string, len(m))
    for k, val := range m {
        if s, ok := val.(string); ok {
            out[k] = s
        }
    }
    // also accept JSON-numeric values cast to string
    _ = strings.Title // unused; placeholder so import lines stay clean
    return out
}
```

- [ ] **Step 13.3: Add the rest of the ingest paths**

Subagents (`p.AgentsDir/*.md` → `Subagents`), Commands (`p.CommandsDir/*.md` → `Commands`), Hooks (read `settings.json` `/hooks/<event>` arrays → `Hooks` slice), LSP (`/lspServers` → `LSPServers`), Memory (`p.Memory` → `Memory.Body` verbatim, no fragment de-resolution since fragments aren't recoverable).

Each follows the same pattern as Skills/MCP. Test each in `ingest_test.go` — round-trip Apply→Ingest. Commit.

---

## Task 14: Wire claude adapter into the registry

**Files:**
- Modify: `internal/cli/registry_internal.go`

Replace the NoopAdapter for "claude" with a real `claude.New(...)`:

```go
package cli

import (
    "github.com/spxrogers/agentsync/internal/adapter"
    "github.com/spxrogers/agentsync/internal/adapter/claude"
    "github.com/spxrogers/agentsync/internal/adapter/noop"
    "github.com/spxrogers/agentsync/internal/paths"
)

var registryFactory = func() *adapter.Registry {
    r := adapter.NewRegistry()
    home := paths.HomeDir(paths.OSEnv{})
    _ = r.Register(claude.New(claude.Options{TargetRoot: home}))
    for _, name := range []string{"opencode", "codex", "cursor"} {
        _ = r.Register(noop.New(name))
    }
    return r
}
```

- [ ] **Step 14.1: Update `cli/apply.go` to allow real apply (drop the M0 error)**

In `apply.go` `RunE`, change:

```go
if !dryRun {
    return fmt.Errorf("M0 only supports --dry-run; ...")
}
```

…to:

```go
if !dryRun {
    plan, err := render.Plan(c, reg, agents, sc, "")
    if err != nil {
        return err
    }
    if err := render.Apply(plan, reg); err != nil {
        return err
    }
    fmt.Fprintln(cmd.OutOrStdout(), "applied:", plan.Total(), "ops")
    return nil
}
// (existing dry-run code follows)
```

- [ ] **Step 14.2: Integration test**

`internal/cli/m1_integration_test.go`:

```go
package cli_test

import (
    "encoding/json"
    "os"
    "path/filepath"
    "strings"
    "testing"
)

func TestIntegration_M1_ClaudeMCPApply(t *testing.T) {
    tmp := t.TempDir()
    env := map[string]string{"AGENTSYNC_TARGET_ROOT": tmp}

    if _, err := runCLI(t, env, "init"); err != nil {
        t.Fatal(err)
    }
    if _, err := runCLI(t, env, "agent", "add", "claude"); err != nil {
        t.Fatal(err)
    }

    // Author an MCP file directly (the `mcp add` CLI lands in M4; for M1 we
    // exercise via direct file creation, which is the supported "vim-able"
    // path anyway).
    mcpFile := filepath.Join(tmp, ".agentsync", "mcp", "github.toml")
    _ = os.MkdirAll(filepath.Dir(mcpFile), 0o755)
    _ = os.WriteFile(mcpFile, []byte(`
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
agents  = ["claude"]
`), 0o644)

    out, err := runCLI(t, env, "apply")
    if err != nil {
        t.Fatalf("apply: %v\n%s", err, out)
    }

    body, err := os.ReadFile(filepath.Join(tmp, ".claude.json"))
    if err != nil {
        t.Fatalf("read .claude.json: %v", err)
    }
    var top map[string]any
    _ = json.Unmarshal(body, &top)
    s := top["mcpServers"].(map[string]any)["github"].(map[string]any)
    if !strings.HasPrefix(s["command"].(string), "npx") {
        t.Fatalf("github command = %v", s["command"])
    }
}
```

- [ ] **Step 14.3: Run; commit**

```bash
go test -race ./...
git commit -am "$(cat <<'EOF'
feat(cli): register real claude adapter; apply writes destinations

Replaces NoopAdapter for "claude" with claude.New. apply (no flag) now
writes; --dry-run continues to compute plan only. Integration test
verifies end-to-end MCP fanout to ~/.claude.json.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done When

```bash
$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync init
$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync agent add claude
$ cat > /tmp/x/.agentsync/mcp/github.toml <<'TOML'
[server]
type    = "stdio"
command = "npx"
args    = ["-y", "@modelcontextprotocol/server-github"]
TOML
$ AGENTSYNC_TARGET_ROOT=/tmp/x agentsync apply
applied: 1 ops

$ jq '.mcpServers.github' /tmp/x/.claude.json
{
  "type": "stdio",
  "command": "npx",
  "args": ["-y", "@modelcontextprotocol/server-github"]
}
```

Per-key merge survives foreign keys. Round-trip: `Render → Apply → Ingest` is parity-clean for every component except where the spec calls out lossiness (none for Claude — Claude is the source-of-truth shape). CI green on linux/macos/windows. Lint clean.

Hooks, skills, subagents, commands, LSP — all rendered from canonical, written to disk in their native shapes, ingested back without loss.
