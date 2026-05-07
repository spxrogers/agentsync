package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"
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

// loadSkills walks skills/<name>/SKILL.md. Frontmatter parsing is added in M1
// when the Claude adapter actually uses skills; for M0 we just record the
// skill names + body so the canonical model is complete.
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
		body, err := afero.ReadFile(fs, filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read SKILL.md for %s: %w", e.Name(), err)
		}
		out = append(out, Skill{Name: e.Name(), Body: string(body)})
	}
	return out, nil
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
