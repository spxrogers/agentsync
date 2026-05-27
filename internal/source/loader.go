package source

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/jsonkeys"
)

// Load reads a canonical model from <home>. Missing home or missing
// subdirectories return an empty Canonical (not an error). Malformed files
// return an error with a path prefix for actionability.
//
// Load is plugin-UNAWARE: it does not project plugin manifests. Callers that
// need plugin components expanded into the model use marketplace.LoadProjected
// (which owns the single plugin projector). This keeps source free of any
// dependency on plugin/marketplace concepts.
func Load(fs afero.Fs, home string) (Canonical, error) {
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
	return c, nil
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
		skillDir := filepath.Join(dir, e.Name())
		raw, err := afero.ReadFile(fs, filepath.Join(skillDir, "SKILL.md"))
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
		files, err := ReadSkillFiles(fs, skillDir)
		if err != nil {
			return nil, fmt.Errorf("read bundled files for skill %s: %w", e.Name(), err)
		}
		out = append(out, Skill{Name: e.Name(), Frontmatter: fm, Body: body, Files: files})
	}
	return out, nil
}

// ReadSkillFiles walks skillDir and returns every regular file other than
// SKILL.md as a SkillFile, with a slash-separated path relative to skillDir and
// the file's permission bits preserved. It is the single bundled-file capture
// implementation shared by the canonical loader and every adapter's Ingest, so
// the "a skill is a directory, not just SKILL.md" rule cannot drift between
// them. Non-regular files (symlinks, devices) are skipped — only real bundled
// resources are captured. Results are sorted by path for deterministic ordering.
func ReadSkillFiles(fs afero.Fs, skillDir string) ([]SkillFile, error) {
	var files []SkillFile
	walkErr := afero.Walk(fs, skillDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "SKILL.md" {
			return nil
		}
		data, err := afero.ReadFile(fs, path)
		if err != nil {
			return err
		}
		files = append(files, SkillFile{Path: rel, Content: data, Mode: uint32(info.Mode().Perm())})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
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
	// Normalize CRLF → LF so a component .md saved by a Windows editor
	// ("---\r\n"), or shipped CRLF inside a fetched plugin, is recognized as
	// having frontmatter. Without this the literal "---\n" check fails and the
	// whole file becomes body, silently dropping description/model/mode.
	// agentsync re-renders bodies with LF, so the normalization is lossless.
	if bytes.IndexByte(data, '\r') >= 0 {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return map[string]any{}, string(data), nil
	}
	rest := data[len("---\n"):]
	yml, body, ok := splitFrontmatterBody(rest)
	if !ok {
		return nil, "", fmt.Errorf("unterminated frontmatter")
	}
	// An empty frontmatter block ("---\n---\n…") is a valid empty mapping.
	if len(bytes.TrimSpace(yml)) == 0 {
		return map[string]any{}, string(body), nil
	}
	fm, err := jsonkeys.DecodeYAML(yml)
	if err != nil {
		return nil, "", fmt.Errorf("parse yaml frontmatter: %w", err)
	}
	return fm, string(body), nil
}

// splitFrontmatterBody splits the bytes AFTER the opening "---\n" into the YAML
// frontmatter and the body. It accepts the closing "---" fence whether it sits
// mid-file ("\n---\n"), at end-of-file with no trailing newline ("\n---"), or —
// for an empty frontmatter mapping — as the very first line ("---\n…" or just
// "---"). ok is false only when there is no closing fence at all. (Editors that
// strip a trailing newline, and frontmatter-only files, are common; requiring a
// trailing "\n" after the fence used to abort the whole source.Load.)
func splitFrontmatterBody(rest []byte) (yml, body []byte, ok bool) {
	switch {
	case bytes.HasPrefix(rest, []byte("---\n")):
		return nil, rest[len("---\n"):], true
	case bytes.Equal(rest, []byte("---")):
		return nil, nil, true
	}
	if i := bytes.Index(rest, []byte("\n---\n")); i >= 0 {
		return rest[:i], rest[i+len("\n---\n"):], true
	}
	if bytes.HasSuffix(rest, []byte("\n---")) {
		return rest[:len(rest)-len("\n---")], nil, true
	}
	return nil, nil, false
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
	// Walk recursively and key each fragment by its slash-separated path UNDER
	// memory/fragments/, because the @import directive accepts
	// "./fragments/<name>" where <name> may contain "/" (a nested fragment).
	// A flat, basename-only read silently never loaded those, leaving the
	// directive literal in the rendered memory.
	werr := afero.Walk(fs, fragDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil // no fragments/ dir is fine
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, rerr := afero.ReadFile(fs, path)
		if rerr != nil {
			return fmt.Errorf("read fragment %s: %w", path, rerr)
		}
		rel, rerr := filepath.Rel(fragDir, path)
		if rerr != nil {
			return rerr
		}
		m.Fragments[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if werr != nil {
		return m, fmt.Errorf("read memory/fragments: %w", werr)
	}
	return m, nil
}
