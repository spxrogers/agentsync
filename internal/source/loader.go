package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
	"sigs.k8s.io/yaml"
)

// Load reads a canonical model from <home>. Missing home or missing
// subdirectories return an empty Canonical (not an error). Malformed files
// return an error with a path prefix for actionability.
func Load(fs afero.Fs, home string) (Canonical, error) {
	return LoadWithCache(fs, home, "")
}

// LoadWithCache is like Load but also projects plugin manifests from the plugin
// cache directory. For each plugins/<id>.toml entry, the loader reads
// <cacheDir>/<id>/.claude-plugin/plugin.json (via the same afero FS), runs
// marketplace.Project, and merges the resulting MCPServers / Skills / Subagents
// / Commands / Hooks / LSPServers into the canonical model. Adapters downstream
// therefore see plugin components transparently without knowing about plugins.
//
// If cacheDir is empty the function behaves identically to Load (no projection).
func LoadWithCache(fs afero.Fs, home string, cacheDir string) (Canonical, error) {
	var c Canonical

	if err := loadConfig(fs, home, &c.Config); err != nil {
		return c, err
	}
	var err error
	if c.MCPServers, err = loadMCP(fs, home); err != nil {
		return c, err
	}
	if c.Plugins, err = loadPlugins(fs, home); err != nil {
		return c, err
	}
	if c.Marketplaces, err = loadMarketplaces(fs, home); err != nil {
		return c, err
	}
	if c.Skills, err = loadSkills(fs, home); err != nil {
		return c, err
	}
	if c.Subagents, err = loadSubagents(fs, home); err != nil {
		return c, err
	}
	if c.Commands, err = loadCommands(fs, home); err != nil {
		return c, err
	}
	if c.Hooks, err = loadHooks(fs, home); err != nil {
		return c, err
	}
	if c.LSPServers, err = loadLSP(fs, home); err != nil {
		return c, err
	}
	if c.Memory, err = loadMemory(fs, home); err != nil {
		return c, err
	}

	// Plugin projection: expand each plugin's cached manifest into the canonical
	// model so downstream adapters see plugin components transparently.
	if cacheDir != "" {
		if err := projectPlugins(fs, &c, cacheDir); err != nil {
			return c, err
		}
	}

	return c, nil
}

// projectPlugins iterates the canonical plugin list, projects each plugin's
// manifest from the cache (using the afero FS for reading), and merges the
// PluginProjection into the canonical model.
//
// Projection is performed inline (no import of the marketplace package) to avoid
// an import cycle: marketplace already imports source. Only strict-mode
// plugin.json parsing is performed here; PluginEntry-level overrides are not
// applied at load time (they are handled during rendering per adapter).
func projectPlugins(fs afero.Fs, c *Canonical, cacheDir string) error {
	for _, pl := range c.Plugins {
		// Derive the plugin's simple ID (strip any "@marketplace" suffix).
		id := pl.Plugin.ID
		if idx := strings.LastIndex(id, "@"); idx >= 0 {
			id = id[:idx]
		}
		if id == "" {
			id = pl.ID
		}

		pluginCacheDir := filepath.Join(cacheDir, id)
		proj, err := readPluginProjection(fs, pluginCacheDir)
		if err != nil {
			return fmt.Errorf("project plugin %s: %w", id, err)
		}

		c.MCPServers = append(c.MCPServers, proj.MCPServers...)
		c.Skills = append(c.Skills, proj.Skills...)
		c.Subagents = append(c.Subagents, proj.Subagents...)
		c.Commands = append(c.Commands, proj.Commands...)
		c.Hooks = append(c.Hooks, proj.Hooks...)
		c.LSPServers = append(c.LSPServers, proj.LSPServers...)
	}
	return nil
}

// pluginManifestJSON is the minimal structure we need from plugin.json to build
// a PluginProjection. It intentionally mirrors marketplace.PluginManifest but
// lives here to avoid the import cycle (marketplace imports source).
type pluginManifestJSON struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
	LSPServers map[string]json.RawMessage `json:"lspServers"`
	Skills     json.RawMessage            `json:"skills"`
	Commands   json.RawMessage            `json:"commands"`
	Agents     json.RawMessage            `json:"agents"`
}

// readPluginProjection reads <pluginCacheDir>/.claude-plugin/plugin.json and
// builds a PluginProjection. MCP and LSP server entries are parsed inline to
// avoid the source↔marketplace import cycle. Skills, commands, and subagents
// are loaded by reading the markdown files the manifest points at (using
// resolveComponentPath so relative paths are joined against pluginCacheDir).
// If the file is absent, an empty projection is returned without error.
func readPluginProjection(fs afero.Fs, pluginCacheDir string) (PluginProjection, error) {
	var pr PluginProjection
	manifestPath := filepath.Join(pluginCacheDir, ".claude-plugin", "plugin.json")
	data, err := afero.ReadFile(fs, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return pr, nil
		}
		return pr, fmt.Errorf("read plugin.json: %w", err)
	}

	var manifest pluginManifestJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		return pr, fmt.Errorf("parse plugin.json: %w", err)
	}

	root := pluginCacheDir // used to resolve ${CLAUDE_PLUGIN_ROOT}
	for name, raw := range manifest.MCPServers {
		spec := parseMCPSpecJSON(raw, root)
		pr.MCPServers = append(pr.MCPServers, MCPServer{ID: name, Server: spec})
	}
	for name, raw := range manifest.LSPServers {
		spec := parseLSPSpecJSON(raw, root)
		pr.LSPServers = append(pr.LSPServers, LSPServer{ID: name, Spec: spec})
	}

	// Skills: each entry is a path to a directory containing SKILL.md, or
	// a direct SKILL.md path. Relative entries are resolved against pluginCacheDir.
	skillPaths := rawToStringSlice(manifest.Skills)
	if len(skillPaths) == 0 {
		// Convention-based discovery: scan pluginCacheDir/skills/ for subdirs.
		discovered, _ := discoverSkillDirsFS(fs, filepath.Join(pluginCacheDir, "skills"))
		skillPaths = discovered
	}
	for _, sk := range skillPaths {
		abs := resolveComponentPathFS(sk, pluginCacheDir)
		skill, err := loadSkillEntryFS(fs, abs)
		if err != nil {
			return pr, fmt.Errorf("load skill %q: %w", sk, err)
		}
		if skill != nil {
			pr.Skills = append(pr.Skills, *skill)
		}
	}

	// Commands: each entry is a path to a <name>.md file.
	for _, cmd := range rawToStringSlice(manifest.Commands) {
		abs := resolveComponentPathFS(cmd, pluginCacheDir)
		entry, err := loadMarkdownEntryFS(fs, abs)
		if err != nil {
			return pr, fmt.Errorf("load command %q: %w", cmd, err)
		}
		if entry != nil {
			pr.Commands = append(pr.Commands, Command{Name: entry.name, Frontmatter: entry.fm, Body: entry.body})
		}
	}

	// Subagents: each entry is a path to a <name>.md file.
	for _, ag := range rawToStringSlice(manifest.Agents) {
		abs := resolveComponentPathFS(ag, pluginCacheDir)
		entry, err := loadMarkdownEntryFS(fs, abs)
		if err != nil {
			return pr, fmt.Errorf("load agent %q: %w", ag, err)
		}
		if entry != nil {
			pr.Subagents = append(pr.Subagents, Subagent{Name: entry.name, Frontmatter: entry.fm, Body: entry.body})
		}
	}

	return pr, nil
}

// resolveComponentPathFS resolves a manifest-listed component path against
// pluginCacheDir. It mirrors marketplace.resolveComponentPath but lives here to
// avoid the import cycle.
//
// Resolution order:
//  1. ${CLAUDE_PLUGIN_ROOT} substitution if present.
//  2. Absolute paths returned as-is.
//  3. Relative paths joined to pluginCacheDir.
func resolveComponentPathFS(s, pluginCacheDir string) string {
	const placeholder = "${CLAUDE_PLUGIN_ROOT}"
	if strings.Contains(s, placeholder) {
		return strings.ReplaceAll(s, placeholder, pluginCacheDir)
	}
	if filepath.IsAbs(s) {
		return s
	}
	return filepath.Join(pluginCacheDir, s)
}

// rawToStringSlice coerces a json.RawMessage that holds either a JSON string
// or a JSON array of strings into a []string.
func rawToStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// Try array first.
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	// Try single string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	return nil
}

// markdownEntryFS holds the parsed result of a single markdown file (loader-side).
type markdownEntryFS struct {
	name string
	fm   map[string]any
	body string
}

// loadSkillEntryFS reads a skill from path, which may be a directory containing
// SKILL.md or a SKILL.md file directly. Returns nil when the file is absent.
func loadSkillEntryFS(fs afero.Fs, path string) (*Skill, error) {
	skillPath := filepath.Join(path, "SKILL.md")
	data, err := afero.ReadFile(fs, skillPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Try treating path itself as the file.
			data, err = afero.ReadFile(fs, path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil, nil // silently skip missing skill
				}
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			skillPath = path
		} else {
			return nil, fmt.Errorf("read %s: %w", skillPath, err)
		}
	}

	fm, body, err := ParseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", skillPath, err)
	}

	name := ""
	if v, ok := fm["name"].(string); ok && v != "" {
		name = v
	}
	if name == "" {
		name = filepath.Base(path)
	}
	return &Skill{Name: name, Frontmatter: fm, Body: body}, nil
}

// loadMarkdownEntryFS reads a markdown file at path. Returns nil when absent.
func loadMarkdownEntryFS(fs afero.Fs, path string) (*markdownEntryFS, error) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // silently skip missing entry
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	fm, body, err := ParseFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("parse frontmatter in %s: %w", path, err)
	}

	name := ""
	if v, ok := fm["name"].(string); ok && v != "" {
		name = v
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	return &markdownEntryFS{name: name, fm: fm, body: body}, nil
}

// discoverSkillDirsFS scans skillsDir for subdirectories (convention-based
// discovery) and returns their absolute paths. Used when plugin.json lists no
// skills. Mirrors marketplace.discoverSkillDirs but uses an afero.Fs.
func discoverSkillDirsFS(fs afero.Fs, skillsDir string) ([]string, error) {
	entries, err := afero.ReadDir(fs, skillsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			paths = append(paths, filepath.Join(skillsDir, e.Name()))
		}
	}
	return paths, nil
}

// resolvePR replaces ${CLAUDE_PLUGIN_ROOT} with root in s.
func resolvePR(s, root string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", root)
}

// parseMCPSpecJSON converts raw JSON (from mcpServers map value) into MCPServerSpec.
func parseMCPSpecJSON(raw json.RawMessage, root string) MCPServerSpec {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return MCPServerSpec{}
	}
	spec := MCPServerSpec{}
	if v, ok := m["type"]; ok {
		json.Unmarshal(v, &spec.Type) //nolint:errcheck
	}
	if v, ok := m["command"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			spec.Command = resolvePR(s, root)
		}
	}
	if v, ok := m["url"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			spec.URL = resolvePR(s, root)
		}
	}
	if v, ok := m["args"]; ok {
		var args []string
		if json.Unmarshal(v, &args) == nil {
			for i, a := range args {
				args[i] = resolvePR(a, root)
			}
			spec.Args = args
		}
	}
	if v, ok := m["env"]; ok {
		var env map[string]string
		if json.Unmarshal(v, &env) == nil {
			spec.Env = make(map[string]string, len(env))
			for k, val := range env {
				spec.Env[k] = resolvePR(val, root)
			}
		}
	}
	if v, ok := m["agents"]; ok {
		var agents []string
		if json.Unmarshal(v, &agents) == nil {
			spec.Agents = agents
		}
	}
	return spec
}

// parseLSPSpecJSON converts raw JSON into LSPServerSpec.
func parseLSPSpecJSON(raw json.RawMessage, root string) LSPServerSpec {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return LSPServerSpec{}
	}
	spec := LSPServerSpec{}
	if v, ok := m["command"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			spec.Command = resolvePR(s, root)
		}
	}
	if v, ok := m["url"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			spec.URL = resolvePR(s, root)
		}
	}
	if v, ok := m["args"]; ok {
		var args []string
		if json.Unmarshal(v, &args) == nil {
			for i, a := range args {
				args[i] = resolvePR(a, root)
			}
			spec.Args = args
		}
	}
	return spec
}

func loadConfig(fs afero.Fs, home string, cfg *Config) error {
	p := filepath.Join(home, "agentsync.toml")
	data, err := afero.ReadFile(fs, p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", p, err)
	}
	// Strict-decode the top-level config so misspelled keys
	// (`comunicate_mode`, `defauls`) surface as a clear error instead of
	// being silently dropped. We only apply strictness to agentsync.toml;
	// the per-component TOML files (mcp/<id>.toml, plugins/<id>.toml, …)
	// keep the lenient default so plugin authors can add forward-compatible
	// keys without breaking existing installs.
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		// pelletier's strict mode returns *StrictMissingError whose
		// Error() is generic; its String() lists each unknown key with
		// a position. Surface the detailed form so the user sees the
		// typo, not "fields in the document are missing in the target
		// struct".
		var strictErr *toml.StrictMissingError
		if errors.As(err, &strictErr) {
			return fmt.Errorf("parse %s:\n%s", p, strictErr.String())
		}
		return fmt.Errorf("parse %s: %w", p, err)
	}
	return nil
}

func loadMCP(fs afero.Fs, home string) ([]MCPServer, error) {
	dir := filepath.Join(home, "mcp")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []MCPServer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var m MCPServer
		if err := toml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		m.ID = strings.TrimSuffix(e.Name(), ".toml")
		out = append(out, m)
	}
	return out, nil
}

func loadPlugins(fs afero.Fs, home string) ([]Plugin, error) {
	dir := filepath.Join(home, "plugins")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Plugin
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var pl Plugin
		if err := toml.Unmarshal(data, &pl); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		pl.ID = strings.TrimSuffix(e.Name(), ".toml")
		out = append(out, pl)
	}
	return out, nil
}

func loadMarketplaces(fs afero.Fs, home string) ([]Marketplace, error) {
	dir := filepath.Join(home, "marketplaces")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Marketplace
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var m Marketplace
		if err := toml.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		m.Name = strings.TrimSuffix(e.Name(), ".toml")
		out = append(out, m)
	}
	return out, nil
}

// loadSkills walks skills/<name>/SKILL.md, parsing YAML frontmatter if present.
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
		fm, body, err := ParseFrontmatter(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		out = append(out, Skill{Name: e.Name(), Frontmatter: fm, Body: body})
	}
	return out, nil
}

// loadSubagents walks agents/<name>.md, parsing YAML frontmatter if present.
func loadSubagents(fs afero.Fs, home string) ([]Subagent, error) {
	dir := filepath.Join(home, "agents")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Subagent
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		fm, body, err := ParseFrontmatter(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, Subagent{Name: name, Frontmatter: fm, Body: body})
	}
	return out, nil
}

// loadCommands walks commands/<name>.md, parsing YAML frontmatter if present.
func loadCommands(fs afero.Fs, home string) ([]Command, error) {
	dir := filepath.Join(home, "commands")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Command
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		fm, body, err := ParseFrontmatter(raw)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		out = append(out, Command{Name: name, Frontmatter: fm, Body: body})
	}
	return out, nil
}

// hookFile is the TOML shape for hooks/<event>.toml.
type hookFile struct {
	Hook []hookEntry `toml:"hook"`
}

type hookEntry struct {
	Matcher string `toml:"matcher"`
	Type    string `toml:"type"`
	Command string `toml:"command"`
}

// loadHooks walks hooks/<event>.toml files. Each file corresponds to one
// Claude hook event (e.g. "PreToolUse"). Entries within the file become
// individual Hook records sharing the same Event.
func loadHooks(fs afero.Fs, home string) ([]Hook, error) {
	dir := filepath.Join(home, "hooks")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []Hook
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		event := strings.TrimSuffix(e.Name(), ".toml")
		p := filepath.Join(dir, e.Name())
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var hf hookFile
		if err := toml.Unmarshal(data, &hf); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		for _, h := range hf.Hook {
			out = append(out, Hook{
				Event:   event,
				Matcher: h.Matcher,
				Type:    h.Type,
				Command: h.Command,
			})
		}
	}
	return out, nil
}

// lspFile is the TOML shape for lsp/<id>.toml.
type lspFile struct {
	Server LSPServerSpec `toml:"server"`
}

// loadLSP walks lsp/<id>.toml files.
func loadLSP(fs afero.Fs, home string) ([]LSPServer, error) {
	dir := filepath.Join(home, "lsp")
	entries, err := afero.ReadDir(fs, dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []LSPServer
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := afero.ReadFile(fs, p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		var lf lspFile
		if err := toml.Unmarshal(data, &lf); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		out = append(out, LSPServer{ID: id, Spec: lf.Server})
	}
	return out, nil
}

// ParseFrontmatter extracts YAML frontmatter and body from a markdown file.
// If the input doesn't begin with "---\n", returns an empty map and the
// entire input as body.
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

func loadMemory(fs afero.Fs, home string) (Memory, error) {
	var m Memory
	body, err := afero.ReadFile(fs, filepath.Join(home, "memory", "AGENTS.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return m, fmt.Errorf("read memory/AGENTS.md: %w", err)
	}
	m.Body = string(body)

	m.Fragments = map[string]string{}
	fragDir := filepath.Join(home, "memory", "fragments")
	entries, err := afero.ReadDir(fs, fragDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := afero.ReadFile(fs, filepath.Join(fragDir, e.Name()))
			if err != nil {
				return m, fmt.Errorf("read fragment %s: %w", e.Name(), err)
			}
			m.Fragments[e.Name()] = string(data)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return m, fmt.Errorf("read memory/fragments: %w", err)
	}
	return m, nil
}
