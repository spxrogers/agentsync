// Package-level write-back helpers for the canonical source (~/.agentsync/).
// These are called by the reconcile command when the user selects [w]rite-back,
// and by the import command to capture native edits.
//
// SECRET-SAFETY INVARIANT (read before adding a caller): these Write* helpers
// take only the TEMPLATED source types (source.Canonical / its sub-structs) and
// perform NO secret re-referencing. apply substitutes ${secret:…} to cleartext
// into destinations, so any canonical reconstructed from a destination holds
// live credentials. The ONLY sanctioned dest->source write path is
// capture.Capture, which calls secrets.ReReferenceCanonical first. Do NOT pass
// these helpers a value obtained by unwrapping secrets.Resolved.Canonical() — it
// is the resolved (cleartext) apply model and would leak the secret into source.
// This API is intentionally not lint-fenced (it is the legitimate templated
// writer Capture is built on), so this discipline is the guard; see CLAUDE.md
// "Secret-handling invariants" and internal/secrets/resolved.go.
//
// v1 trade-off: TOML comments in the original file are not preserved on
// write-back. Comment-preserving mutation is deferred to v1.x.
package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spxrogers/agentsync/internal/iox"
	"sigs.k8s.io/yaml"
)

// ValidateComponentID rejects a component id/name/event that would not produce
// a clean file path under its source subdirectory. This is the single
// dest->source write boundary, reached by `import` and `reconcile` write-back
// with ids/keys taken from a NATIVE config — which may be foreign, synced, or
// project-supplied. Without this, an id like "../../../tmp/x" joined into the
// path is an arbitrary-file-write (on write) / read (on read) primitive.
// Mirrors the CLI's validateMCPID; here it guards every Write*/Read* so no
// caller can bypass it.
func ValidateComponentID(kind, id string) error {
	if id == "" {
		return fmt.Errorf("%s id is empty", kind)
	}
	// An all-whitespace id, or a lone ".", passes the separator/traversal checks
	// below yet writes a nonsense file into the canonical source (" .toml",
	// "..toml") that no command can address cleanly. Reject it as a degenerate
	// name rather than persist the confusing artifact.
	if strings.TrimSpace(id) == "" || id == "." {
		return fmt.Errorf("%s id %q is empty or a bare '.'", kind, id)
	}
	// Reject path separators, traversal, and absolute paths (write-anywhere
	// guard), plus ':' — the id becomes a filename, and a colon is illegal on
	// Windows, so allowing it would make the canonical source non-portable.
	if strings.ContainsAny(id, `/\:`) || strings.Contains(id, "..") || filepath.IsAbs(id) {
		return fmt.Errorf("%s id %q contains a path separator, '..', ':' or is absolute", kind, id)
	}
	return nil
}

// ReadMCP reads mcp/<id>.toml from home. ok is false when the file does not
// exist. Used by reconcile write-back to preserve source-only fields
// (agents/enabled) that the rendered destination spec doesn't carry.
func ReadMCP(home, id string) (m MCPServer, ok bool, err error) {
	if verr := ValidateComponentID("mcp", id); verr != nil {
		return MCPServer{}, false, verr
	}
	data, rerr := os.ReadFile(filepath.Join(home, "mcp", id+".toml"))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return MCPServer{}, false, nil
		}
		return MCPServer{}, false, fmt.Errorf("read mcp %s: %w", id, rerr)
	}
	if uerr := toml.Unmarshal(data, &m); uerr != nil {
		return MCPServer{}, false, fmt.Errorf("parse mcp %s: %w", id, uerr)
	}
	m.ID = id
	return m, true, nil
}

// ReadLSP reads lsp/<id>.toml from home. ok is false when the file does not
// exist. Mirrors ReadMCP: used by capture to preserve source-only LSP fields
// (agents/enabled) that the rendered destination spec doesn't carry.
func ReadLSP(home, id string) (ls LSPServer, ok bool, err error) {
	if verr := ValidateComponentID("lsp", id); verr != nil {
		return LSPServer{}, false, verr
	}
	data, rerr := os.ReadFile(filepath.Join(home, "lsp", id+".toml"))
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return LSPServer{}, false, nil
		}
		return LSPServer{}, false, fmt.Errorf("read lsp %s: %w", id, rerr)
	}
	var lf lspFileOut
	if uerr := toml.Unmarshal(data, &lf); uerr != nil {
		return LSPServer{}, false, fmt.Errorf("parse lsp %s: %w", id, uerr)
	}
	return LSPServer{ID: id, Spec: lf.Server}, true, nil
}

// WriteMCP writes mcp/<id>.toml from m into home. Overwrites atomically.
func WriteMCP(home, id string, m MCPServer) error {
	if err := ValidateComponentID("mcp", id); err != nil {
		return err
	}
	body, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal mcp %s: %w", id, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "mcp", id+".toml"), body, 0o644)
}

// WritePlugin writes plugins/<id>.toml from p into home. Overwrites atomically.
func WritePlugin(home, id string, p Plugin) error {
	if err := ValidateComponentID("plugin", id); err != nil {
		return err
	}
	body, err := toml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal plugin %s: %w", id, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "plugins", id+".toml"), body, 0o644)
}

// WriteMarketplace writes marketplaces/<name>.toml from m into home. Overwrites atomically.
func WriteMarketplace(home, name string, m Marketplace) error {
	if err := ValidateComponentID("marketplace", name); err != nil {
		return err
	}
	body, err := toml.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshal marketplace %s: %w", name, err)
	}
	return iox.AtomicWrite(filepath.Join(home, "marketplaces", name+".toml"), body, 0o644)
}

// WriteSkill writes skills/<name>/SKILL.md plus every bundled file (scripts/,
// references/, assets/, …) from sk into home. Each file is written atomically
// with its preserved permission bits; bundled content is written verbatim
// (never frontmatter-encoded), so binary assets round-trip byte-for-byte.
func WriteSkill(home string, sk Skill) error {
	if err := ValidateComponentID("skill", sk.Name); err != nil {
		return err
	}
	dir := filepath.Join(home, "skills", sk.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir skills/%s: %w", sk.Name, err)
	}
	content := renderFrontmatter(sk.Frontmatter) + sk.Body
	if err := iox.AtomicWrite(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		return err
	}
	for _, f := range sk.Files {
		if err := validateSkillFilePath(f.Path); err != nil {
			return fmt.Errorf("skill %s: %w", sk.Name, err)
		}
		dest := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir for skills/%s/%s: %w", sk.Name, f.Path, err)
		}
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		if err := iox.AtomicWrite(dest, f.Content, mode); err != nil {
			return err
		}
	}
	return nil
}

// validateSkillFilePath rejects a bundled-file path that would escape the skill
// directory. Bundled paths derive from a directory walk (loader / ingest) or a
// foreign native config, so a "../" segment must never be joined into an
// arbitrary-file-write primitive — mirrors ValidateComponentID at the bundled
// granularity.
func validateSkillFilePath(rel string) error {
	if rel == "" {
		return fmt.Errorf("empty bundled-file path")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("bundled-file path %q escapes the skill directory", rel)
	}
	if clean == "SKILL.md" {
		return fmt.Errorf("bundled-file path %q collides with SKILL.md", rel)
	}
	return nil
}

// WriteSubagent writes agents/<name>.md from sa into home. Overwrites atomically.
func WriteSubagent(home string, sa Subagent) error {
	if err := ValidateComponentID("subagent", sa.Name); err != nil {
		return err
	}
	dir := filepath.Join(home, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir agents: %w", err)
	}
	content := renderFrontmatter(sa.Frontmatter) + sa.Body
	return iox.AtomicWrite(filepath.Join(dir, sa.Name+".md"), []byte(content), 0o644)
}

// WriteCommand writes commands/<name>.md from cm into home. Overwrites atomically.
func WriteCommand(home string, cm Command) error {
	if err := ValidateComponentID("command", cm.Name); err != nil {
		return err
	}
	dir := filepath.Join(home, "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir commands: %w", err)
	}
	content := renderFrontmatter(cm.Frontmatter) + cm.Body
	return iox.AtomicWrite(filepath.Join(dir, cm.Name+".md"), []byte(content), 0o644)
}

// hookFileOut is the TOML shape written to hooks/<event>.toml.
type hookFileOut struct {
	Hook []hookEntryOut `toml:"hook"`
}

type hookEntryOut struct {
	Matcher string `toml:"matcher,omitempty"`
	Type    string `toml:"type"`
	Command string `toml:"command"`
}

// WriteHooks writes hooks/<event>.toml for the given event. Overwrites atomically.
func WriteHooks(home, event string, hooks []Hook) error {
	if err := ValidateComponentID("hook event", event); err != nil {
		return err
	}
	dir := filepath.Join(home, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir hooks: %w", err)
	}
	hf := hookFileOut{}
	for _, h := range hooks {
		hf.Hook = append(hf.Hook, hookEntryOut{
			Matcher: h.Matcher,
			Type:    h.Type,
			Command: h.Command,
		})
	}
	body, err := toml.Marshal(hf)
	if err != nil {
		return fmt.Errorf("marshal hooks/%s: %w", event, err)
	}
	return iox.AtomicWrite(filepath.Join(dir, event+".toml"), body, 0o644)
}

// lspFileOut is the TOML shape written to lsp/<id>.toml.
type lspFileOut struct {
	Server LSPServerSpec `toml:"server"`
}

// WriteLSP writes lsp/<id>.toml from ls into home. Overwrites atomically.
func WriteLSP(home string, ls LSPServer) error {
	if err := ValidateComponentID("lsp", ls.ID); err != nil {
		return err
	}
	dir := filepath.Join(home, "lsp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir lsp: %w", err)
	}
	lf := lspFileOut{Server: ls.Spec}
	body, err := toml.Marshal(lf)
	if err != nil {
		return fmt.Errorf("marshal lsp %s: %w", ls.ID, err)
	}
	return iox.AtomicWrite(filepath.Join(dir, ls.ID+".toml"), body, 0o644)
}

// WriteMemory writes memory/AGENTS.md from m into home. Overwrites atomically.
func WriteMemory(home string, m Memory) error {
	dir := filepath.Join(home, "memory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir memory: %w", err)
	}
	return iox.AtomicWrite(filepath.Join(dir, "AGENTS.md"), []byte(m.Body), 0o644)
}

// renderFrontmatter serialises a frontmatter map as a YAML block enclosed in
// "---\n...\n---\n". If the map is empty or nil, returns "".
//
// It uses a real YAML marshaller (the same library ParseFrontmatter reads
// with, and that claude.EncodeFrontmatter writes with). The previous homemade
// emitter used Go's %v for non-string values, so a `tools: [Read, Write]` list
// serialized as "[Read Write]" and a nested map as "map[a:1]" — both re-parsed
// as a single mangled string, corrupting subagent tool allowlists and any
// structured frontmatter on import write-back. sigs.k8s.io/yaml sorts map keys,
// so output stays deterministic.
func renderFrontmatter(fm map[string]any) string {
	if len(fm) == 0 {
		return ""
	}
	y, err := yaml.Marshal(fm)
	if err != nil {
		return ""
	}
	return "---\n" + string(y) + "---\n"
}
