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
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// Load reads a canonical model from <home>. Missing home or missing
// subdirectories return an empty Canonical (not an error). Malformed files
// return an error with a path prefix for actionability.
//
// Load is plugin-UNAWARE: it does not project plugin manifests. Callers that
// need plugin components expanded into the model use marketplace.LoadProjected
// (which owns the single plugin projector). This keeps source free of any
// dependency on plugin/marketplace concepts.
//
// Load also GATES on the retired agents/ → subagents/ layout: a home that still
// carries a non-empty agents/ directory returns a *LegacySubagentDirError and
// loads nothing. The gate is fail-closed on purpose — loading such a tree would
// report zero subagents, and PruneStaleState reads "no longer rendered" as
// "source component removed", so one apply would disown every subagent already
// rendered. Diagnostics that must report the condition rather than die use
// LoadTolerant.
func Load(fs afero.Fs, home string) (Canonical, error) {
	if err := CheckSubagentLayout(fs, home); err != nil {
		return Canonical{}, err
	}
	return LoadTolerant(fs, home)
}

// LoadTolerant is Load WITHOUT the legacy-subagent-directory gate. It exists
// for one caller: `agentsync doctor`, which must surface a pending migration as
// a failing check with the fix instead of aborting at load. Every other caller
// must use Load — a tolerant load of an unmigrated tree silently under-reports
// subagents.
func LoadTolerant(fs afero.Fs, home string) (Canonical, error) {
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

// decodeStrictTOML unmarshals TOML data into v with unknown keys REJECTED, so a
// typo'd key (`comand`, `defualt_update_mode`) surfaces as an actionable error
// naming the key and its position instead of being silently dropped. p is used
// only for the error prefix. It is the single strict decoder shared by the
// top-level agentsync.toml and every per-component loader
// (mcp/plugins/marketplaces/hooks/lsp), so their strictness cannot drift.
//
// Strict decoding COEXISTS with the MCP/LSP `extra` passthrough
// (MCPServerSpec.Extra / LSPServerSpec.Extra): because `extra` is a NAMED struct
// field of map type, DisallowUnknownFields still accepts arbitrary keys INSIDE
// the `[server.extra]` table (a map's keys are not "unknown fields") while
// rejecting a stray key elsewhere under `[server]`. The one loader that does NOT
// use this directly is loadMarketplaces — see the note there.
func decodeStrictTOML(p string, data []byte, v any) error {
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		// pelletier's strict mode returns *StrictMissingError whose Error() is
		// generic; its String() lists each unknown key with a position. Surface
		// the detailed form so the user sees the typo, not the terse "fields in
		// the document are missing in the target struct".
		var strictErr *toml.StrictMissingError
		if errors.As(err, &strictErr) {
			return fmt.Errorf("parse %s:\n%s", p, strictErr.String())
		}
		return fmt.Errorf("parse %s: %w", p, err)
	}
	return nil
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
	// (`comunicate_mode`, `defauls`) surface as a clear error instead of being
	// silently dropped. The per-component loaders below now share the same
	// strictness (decodeStrictTOML), so a typo'd key in mcp/<id>.toml,
	// plugins/<id>.toml, marketplaces/<name>.toml, hooks/<event>.toml, or
	// lsp/<id>.toml is surfaced too — never dropped.
	if err := decodeStrictTOML(p, data, cfg); err != nil {
		return err
	}
	// Validate enumerated values the strict decoder can't catch: it rejects
	// unknown KEYS but accepts any VALUE, so a typo'd git-backup mode
	// (mode = "On"/"yes"/"true") would otherwise slip through and silently
	// change behavior. Reject it here so every source.Load caller (apply,
	// doctor) agrees at load time.
	if err := cfg.DestinationGitBackup.validateMode(); err != nil {
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
		if err := decodeStrictTOML(p, data, &m); err != nil {
			return nil, err
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
		if err := decodeStrictTOML(p, data, &pl); err != nil {
			return nil, err
		}
		pl.ID = untrusted.Wrap(strings.TrimSuffix(e.Name(), ".toml"))
		out = append(out, pl)
	}
	return out, nil
}

// marketplaceStrictFile is the strict-decode shape for marketplaces/<name>.toml.
// It deliberately MODELS the two fetch-CACHE keys (head_sha, name) that the CLI
// persists into the file but the canonical Marketplace does NOT round-trip —
// both are regenerated by `marketplace add`/`update` on every fetch and re-
// derived on load (see the MarketplaceSpec doc). Tolerating them here is what
// lets loadMarketplaces STRICT-decode — surfacing a genuine typo'd key like
// `defualt_update_mode` — without falsely rejecting those two deliberate,
// documented cache keys. The two throwaway fields are read and discarded; only
// url/ref/default_update_mode are carried into the returned canonical model.
type marketplaceStrictFile struct {
	Marketplace struct {
		URL               string `toml:"url"`
		Ref               string `toml:"ref,omitempty"`
		DefaultUpdateMode string `toml:"default_update_mode,omitempty"`
		HeadSHA           string `toml:"head_sha,omitempty"` // CLI fetch-cache metadata; discarded here
		Name              string `toml:"name,omitempty"`     // CLI fetch-cache metadata; discarded here
	} `toml:"marketplace"`
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
		var mf marketplaceStrictFile
		if err := decodeStrictTOML(p, data, &mf); err != nil {
			return nil, err
		}
		m := Marketplace{Marketplace: MarketplaceSpec{
			URL:               mf.Marketplace.URL,
			Ref:               mf.Marketplace.Ref,
			DefaultUpdateMode: mf.Marketplace.DefaultUpdateMode,
		}}
		m.Name = untrusted.Wrap(strings.TrimSuffix(e.Name(), ".toml"))
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
		// Lenient fallback: a canonical SKILL.md authored by hand may carry an
		// unquoted "Triggers on: …" colon-space sequence in `description:`.
		// Strict YAML rejects it; we coerce to valid YAML on render anyway, so
		// the load is silent.
		fm, body, _, err := ParseFrontmatterWithReport(raw)
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

// loadSubagents walks subagents/<name>.md, parsing YAML frontmatter if present.
func loadSubagents(fs afero.Fs, home string) ([]Subagent, error) {
	dir := filepath.Join(home, SubagentsDir)
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
		fm, body, _, err := ParseFrontmatterWithReport(raw)
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
		fm, body, _, err := ParseFrontmatterWithReport(raw)
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
	dir := filepath.Join(home, "hooks") // the dir side of the HookPath layout (walked, not composed per-event)
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
		if err := decodeStrictTOML(p, data, &hf); err != nil {
			return nil, err
		}
		for _, h := range hf.Hook {
			out = append(out, Hook{
				Event:   untrusted.Wrap(event),
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
		if err := decodeStrictTOML(p, data, &lf); err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(e.Name(), ".toml")
		out = append(out, LSPServer{ID: id, Spec: lf.Server})
	}
	return out, nil
}

// ParseFrontmatter extracts YAML frontmatter and body from a markdown file.
// If the input doesn't begin with "---\n", returns an empty map and the
// entire input as body. Returns an error on any YAML decode failure; callers
// that want to accept the kind of relaxed "key: value-with-colons" frontmatter
// that Claude Code itself reads should use ParseFrontmatterWithReport instead.
func ParseFrontmatter(data []byte) (map[string]any, string, error) {
	fm, body, _, err := parseFrontmatter(data, false)
	return fm, body, err
}

// ParseFrontmatterWithReport is the lenient-fallback variant: it first tries
// strict YAML; on a YAML decode failure it falls back to a line-oriented
// "key: rest-of-line" parser that accepts bare colons inside values. lenient
// is true iff the strict parse failed but the lenient one succeeded — callers
// (typically Ingest paths) can then surface a warning so the user knows their
// SKILL.md is not strict YAML.
//
// The lenient parser exists because Claude Code itself reads frontmatter that
// way: a SKILL.md whose description contains a bare "Triggers on: X, Y" parses
// fine in claude.ai but breaks sigs.k8s.io/yaml. Without this fallback, `import`
// silently dropped any skill with such a description (the call site swallowed
// the parse error with `continue`).
func ParseFrontmatterWithReport(data []byte) (fm map[string]any, body string, lenient bool, err error) {
	return parseFrontmatter(data, true)
}

func parseFrontmatter(data []byte, allowLenient bool) (fm map[string]any, body string, lenient bool, err error) {
	// Normalize CRLF → LF so a component .md saved by a Windows editor
	// ("---\r\n"), or shipped CRLF inside a fetched plugin, is recognized as
	// having frontmatter. Without this the literal "---\n" check fails and the
	// whole file becomes body, silently dropping description/model/mode.
	// agentsync re-renders bodies with LF, so the normalization is lossless.
	if bytes.IndexByte(data, '\r') >= 0 {
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	}
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return map[string]any{}, string(data), false, nil
	}
	rest := data[len("---\n"):]
	yml, bodyBytes, ok := splitFrontmatterBody(rest)
	if !ok {
		return nil, "", false, fmt.Errorf("unterminated frontmatter")
	}
	// An empty frontmatter block ("---\n---\n…") is a valid empty mapping.
	if len(bytes.TrimSpace(yml)) == 0 {
		return map[string]any{}, string(bodyBytes), false, nil
	}
	fm, strictErr := jsonkeys.DecodeYAML(yml)
	if strictErr == nil {
		return fm, string(bodyBytes), false, nil
	}
	if !allowLenient {
		return nil, "", false, fmt.Errorf("parse yaml frontmatter: %w", strictErr)
	}
	lfm, lerr := parseFrontmatterLenient(yml)
	if lerr != nil {
		// Both parsers failed — return the strict error since it's typically
		// more informative ("mapping values are not allowed in this context"
		// vs. our generic lenient error).
		return nil, "", false, fmt.Errorf("parse yaml frontmatter: %w", strictErr)
	}
	return lfm, string(bodyBytes), true, nil
}

// parseFrontmatterLenient parses YAML frontmatter as a flat string-string map
// using "key: rest-of-line" semantics: the first ": " on each line splits the
// key from the value, the value is taken verbatim to end-of-line, and any
// further colons (the failure mode strict YAML rejects) are preserved.
//
// This is intentionally narrow — it does NOT support nested mappings, arrays,
// multi-line scalars, or quoting. The lenient path exists only to recover skill
// frontmatter whose `description:` contains bare colon-space, which is by far
// the dominant real-world breakage. Anything more structured belongs in strict
// YAML; if a future component frontmatter needs richer leniency, extend here.
func parseFrontmatterLenient(yml []byte) (map[string]any, error) {
	out := map[string]any{}
	lines := bytes.Split(yml, []byte("\n"))
	for _, line := range lines {
		// Skip blank lines and YAML comments. A document marker ("---")
		// shouldn't appear inside the frontmatter block (we already stripped
		// the fences), but treat it defensively as a separator.
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' || bytes.Equal(trimmed, []byte("---")) {
			continue
		}
		idx := bytes.Index(line, []byte(": "))
		if idx < 0 {
			// A line ending with ": " (no value) is rare but valid YAML for an
			// empty value; accept it.
			if bytes.HasSuffix(bytes.TrimRight(line, " \t"), []byte(":")) {
				key := strings.TrimSpace(string(bytes.TrimSuffix(bytes.TrimRight(line, " \t"), []byte(":"))))
				if key == "" {
					return nil, fmt.Errorf("lenient parse: malformed line %q", string(line))
				}
				out[key] = ""
				continue
			}
			return nil, fmt.Errorf("lenient parse: no key/value separator on line %q", string(line))
		}
		key := strings.TrimSpace(string(line[:idx]))
		if key == "" {
			return nil, fmt.Errorf("lenient parse: empty key on line %q", string(line))
		}
		val := strings.TrimRight(string(line[idx+2:]), " \t")
		out[key] = val
	}
	return out, nil
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
	// Reject a canonical that uses the reserved managed-banner marker (see
	// checkReservedMarkers): it would collide with the banner's reversible markers.
	if err := checkReservedMarkers(m.Body, m.Fragments); err != nil {
		return m, err
	}
	return m, nil
}
