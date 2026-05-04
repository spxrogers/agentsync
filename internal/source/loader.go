package source

import (
	"bytes"
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
	if err := toml.Unmarshal(data, cfg); err != nil {
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
		fm, body, err := parseFrontmatter(raw)
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
		fm, body, err := parseFrontmatter(raw)
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
		fm, body, err := parseFrontmatter(raw)
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

// parseFrontmatter extracts YAML frontmatter and body from a markdown file.
// If the input doesn't begin with "---\n", returns an empty map and the
// entire input as body.
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
