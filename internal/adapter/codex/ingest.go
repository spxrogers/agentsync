package codex

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/afero"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
)

// Ingest reads Codex's native config files and returns a partial
// source.Canonical. It is the inverse of Render.
//
// Round-trip note: subagents lose the Claude-side `tools`/`color` frontmatter
// (Codex agents have no equivalent), so Ingest reconstructs the `name` (required
// by Codex, re-populated from the TOML `name` field so a frontmatter name that
// deliberately diverges from the file stem survives the round-trip),
// `description`, and `model` that were written to the agent TOML, plus the body
// from `developer_instructions`. Project-scope slash commands are never written
// (global-only), so they don't ingest at project scope either.
func (a *Adapter) Ingest(scope adapter.Scope, project string) (source.Canonical, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return source.Canonical{}, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	var c source.Canonical
	warn := a.stderr()

	// MCP ([mcp_servers.<id>]) and hooks ([hooks.<event>]) both live in
	// config.toml, so parse it once. A corrupt-but-present config.toml fails
	// loudly (parse error returned) rather than silently reading as "no MCP, no
	// hooks", which drift could misclassify as both components cleared.
	if data, present, err := adapter.ReadFileOptional(p.Config); err != nil {
		return c, fmt.Errorf("read %s: %w", p.Config, err)
	} else if present {
		var top map[string]any
		if err := toml.Unmarshal(data, &top); err != nil {
			return c, fmt.Errorf("parse %s: %w", p.Config, err)
		}
		if servers, ok := top["mcp_servers"].(map[string]any); ok {
			for id, raw := range servers {
				spec, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				c.MCPServers = append(c.MCPServers, source.MCPServer{ID: id, Server: IngestMCPSpec(spec)})
			}
		}
		// Refused events surface via RefusedHookEvents (adapter.HookIngestGuard):
		// import uses that to retire a stale canonical hooks/<event>.toml.
		hooks, _ := ingestHooks(top["hooks"], warn)
		c.Hooks = append(c.Hooks, hooks...)
	}

	// Skills from ~/.agents/skills/<name>/ (SKILL.md + bundled files)
	skillEntries, present, err := adapter.ReadDirOptional(p.SkillsDir)
	if err != nil {
		return c, fmt.Errorf("read skills dir %s: %w", p.SkillsDir, err)
	}
	if present {
		for _, e := range skillEntries {
			if !e.IsDir() {
				continue
			}
			skillDir := filepath.Join(p.SkillsDir, e.Name())
			data, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping skill %q: read SKILL.md: %v\n", e.Name(), err)
				}
				continue
			}
			fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping skill %q: %v\n", e.Name(), err)
				continue
			}
			if lenient {
				fmt.Fprintf(warn, "warning: skill %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", e.Name())
			}
			files, err := source.ReadSkillFiles(afero.NewOsFs(), skillDir)
			if err != nil {
				fmt.Fprintf(warn, "warning: skipping skill %q: read bundled files: %v\n", e.Name(), err)
				continue
			}
			c.Skills = append(c.Skills, source.Skill{Name: e.Name(), Frontmatter: fm, Body: body, Files: files})
		}
	}

	// Subagents from ~/.codex/agents/<name>.toml (TOML → frontmatter + body)
	agentEntries, present, err := adapter.ReadDirOptional(p.AgentsDir)
	if err != nil {
		return c, fmt.Errorf("read agents dir %s: %w", p.AgentsDir, err)
	}
	if present {
		for _, e := range agentEntries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
				continue
			}
			name := e.Name()[:len(e.Name())-len(".toml")]
			data, err := os.ReadFile(filepath.Join(p.AgentsDir, e.Name()))
			if err != nil {
				if !os.IsNotExist(err) {
					fmt.Fprintf(warn, "warning: skipping subagent %q: read: %v\n", name, err)
				}
				continue
			}
			var af codexAgentFile
			if err := toml.Unmarshal(data, &af); err != nil {
				fmt.Fprintf(warn, "warning: skipping subagent %q: %v\n", name, err)
				continue
			}
			fm := map[string]any{}
			// Re-populate the frontmatter `name` from the TOML `name` field
			// unconditionally on presence (even when it equals the file stem):
			// this makes the round-trip lossless for a deliberately-divergent name
			// and a harmless no-op for a matching one, since Render prefers
			// Frontmatter["name"] when present and falls back to the canonical
			// (filename-derived) name otherwise.
			if af.Name != "" {
				fm["name"] = af.Name
			}
			if af.Description != "" {
				fm["description"] = af.Description
			}
			if af.Model != "" {
				fm["model"] = af.Model
			}
			// The canonical Name stays derived from the filename — it is the
			// on-disk identity / SourceID stem — while the frontmatter carries the
			// TOML `name` value above.
			c.Subagents = append(c.Subagents, source.Subagent{Name: name, Frontmatter: fm, Body: af.DeveloperInstructions})
		}
	}

	// Commands from ~/.codex/prompts/<name>.md. Codex prompts are global-only, so
	// render writes them at user scope ONLY; mirror that here so a stray
	// <project>/.codex/prompts/ (which Codex ignores) is not captured as a
	// phantom project-scope command that apply would never write back.
	if scope == adapter.ScopeUser {
		promptEntries, present, err := adapter.ReadDirOptional(p.PromptsDir)
		if err != nil {
			return c, fmt.Errorf("read prompts dir %s: %w", p.PromptsDir, err)
		}
		if present {
			for _, e := range promptEntries {
				if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
					continue
				}
				name := e.Name()[:len(e.Name())-len(".md")]
				data, err := os.ReadFile(filepath.Join(p.PromptsDir, e.Name()))
				if err != nil {
					if !os.IsNotExist(err) {
						fmt.Fprintf(warn, "warning: skipping command %q: read: %v\n", name, err)
					}
					continue
				}
				fm, body, lenient, err := claude.ParseFrontmatterWithReport(data)
				if err != nil {
					fmt.Fprintf(warn, "warning: skipping command %q: %v\n", name, err)
					continue
				}
				if lenient {
					fmt.Fprintf(warn, "warning: command %q frontmatter is not strict YAML; parsed leniently (consider quoting values containing ': ')\n", name)
				}
				c.Commands = append(c.Commands, source.Command{Name: name, Frontmatter: fm, Body: body})
			}
		}
	}

	// Memory from AGENTS.md (managed-file banner stripped — see claude/ingest.go)
	if data, present, err := adapter.ReadFileOptional(p.Memory); err != nil {
		return c, fmt.Errorf("read memory %s: %w", p.Memory, err)
	} else if present {
		c.Memory.Body = source.StripManagedBanner(string(data))
	}

	return c, nil
}

// codexHookDefModeledKeys / codexHookEntryModeledKeys enumerate the config.toml
// hook fields the canonical source.Hook can represent — the schema matches
// Claude's (event → []{matcher, hooks: [{type, command}]}). Anything else in an
// event makes it unrepresentable — see ingestHooks.
var (
	codexHookDefModeledKeys   = map[string]bool{"matcher": true, "hooks": true}
	codexHookEntryModeledKeys = map[string]bool{"type": true, "command": true}
)

// ingestHooks decodes config.toml's [hooks.<event>] tables (the value of the
// top-level "hooks" key) into canonical hooks, warning on anything it cannot
// capture. The TOML decode yields the same map shape as the JSON Codex/Claude
// hook schema (event → []{matcher, hooks: [{type, command}]}), so the walk is
// format-agnostic. Inverse of renderHooks; codex spells events canonically, so
// there is no name remapping and refused already carries retire-ready names.
//
// Guard-and-warn posture (round-2 review of the epic #178 residual close —
// this was the LAST hook ingest still capturing a lossy modeled subset, the
// FIRST-order issue #124 loss): an event whose def or handler carries an
// unmodeled key is refused WHOLE (semantic — import retires its stale
// canonical hooks/<event>.toml), and a malformed shape (non-object
// def/handler, non-string matcher/type/command, absent command, missing hooks
// array) warns without capturing and never retires (structural). One
// deliberate divergence from the claude/gemini/cursor twins: a non-command
// handler TYPE is NOT refused — codex parses-and-skips unknown types at
// runtime and renderHooks re-emits Type verbatim (with a reported reduced
// Skip), so a non-command handler round-trips losslessly and refusing it
// would hand back an event on which nothing is lost. Because of that, the
// handler-level unmodeled-keys check runs BEFORE the command checks: a native
// handler converted to another engine shape (e.g. type="prompt" plus a
// prompt field, no command) must surface as a SEMANTIC refusal on its
// unmodeled field, not be short-circuited into a structural skip by its
// missing command.
func ingestHooks(raw any, warn io.Writer) (out []source.Hook, refused []string) {
	hooks, ok := raw.(map[string]any)
	if !ok {
		return nil, nil
	}
	for event, rawEntries := range hooks {
		entries, ok := rawEntries.([]any)
		if !ok {
			fmt.Fprintf(warn, "warning: hook event %q value is not an array of tables; event not captured\n", event)
			continue // structural: warn + skip capture, but never a retire-triggering refusal
		}
		var captured []source.Hook
		representable := true
		structural := false
	defs:
		for _, rawEntry := range entries {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				fmt.Fprintf(warn, "warning: hook event %q has a malformed definition (not a table); event not captured\n", event)
				representable = false
				structural = true
				break
			}
			if extra := unmodeledKeys(entry, codexHookDefModeledKeys); len(extra) > 0 {
				fmt.Fprintf(warn, "warning: hook event %q has a definition with unmodeled fields (%s); event not captured\n", event, quotedKeys(extra))
				representable = false
				break
			}
			if rawMatcher, present := entry["matcher"]; present {
				if _, isStr := rawMatcher.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a definition whose \"matcher\" is not a string; event not captured\n", event)
					representable = false
					structural = true
					break
				}
			}
			matcher := asStr(entry["matcher"])
			hooksArr, isArr := entry["hooks"].([]any)
			if !isArr {
				fmt.Fprintf(warn, "warning: hook event %q has a definition without a valid \"hooks\" array; event not captured\n", event)
				representable = false
				structural = true
				break
			}
			for _, rawH := range hooksArr {
				h, ok := rawH.(map[string]any)
				if !ok {
					fmt.Fprintf(warn, "warning: hook event %q has a malformed handler (not a table); event not captured\n", event)
					representable = false
					structural = true
					break defs
				}
				if rawType, present := h["type"]; present {
					if _, isStr := rawType.(string); !isStr {
						fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"type\" is not a string; event not captured\n", event)
						representable = false
						structural = true
						break defs
					}
				}
				if extra := unmodeledKeys(h, codexHookEntryModeledKeys); len(extra) > 0 {
					fmt.Fprintf(warn, "warning: hook event %q has a handler with unmodeled fields (%s); event not captured\n", event, quotedKeys(extra))
					representable = false
					break defs
				}
				// An absent or non-string command would be asStr-coerced to "" and
				// captured as an EMPTY-command handler renderHooks then writes over
				// the user's native entry. Unlike the twins this guards EVERY
				// handler (codex has no semantic type refusal — see above), and it
				// runs after the unmodeled-keys check for the same reason.
				if rawCmd, present := h["command"]; !present {
					fmt.Fprintf(warn, "warning: hook event %q has a handler without a \"command\"; event not captured\n", event)
					representable = false
					structural = true
					break defs
				} else if _, isStr := rawCmd.(string); !isStr {
					fmt.Fprintf(warn, "warning: hook event %q has a handler whose \"command\" is not a string; event not captured\n", event)
					representable = false
					structural = true
					break defs
				}
				captured = append(captured, source.Hook{
					Event:   untrusted.Wrap(event), // native config map key
					Matcher: matcher,
					Type:    asStr(h["type"]),
					Command: asStr(h["command"]),
				})
			}
		}
		if representable {
			out = append(out, captured...)
		} else if !structural {
			refused = append(refused, event)
		}
	}
	sort.Strings(refused) // map iteration order — keep output deterministic
	return out, refused
}

// RefusedHookEvents implements adapter.HookIngestGuard — the codex leg. Codex
// spells hook events canonically, so unlike the renaming adapters the returned
// names need no mapping. config.toml is re-read and parsed exactly as Ingest
// parses it (toml.Unmarshal); warnings are discarded here — Ingest already
// emitted them on the same shapes. See adapter.HookIngestGuard for the shared
// contract and the corruption class this closes (issue #124).
func (a *Adapter) RefusedHookEvents(scope adapter.Scope, project string) ([]string, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, err
	}
	p := ResolvePaths(a.opts.TargetRoot, project, scope == adapter.ScopeProject)
	data, present, err := adapter.ReadFileOptional(p.Config)
	if err != nil || !present {
		return nil, err
	}
	var top map[string]any
	if err := toml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p.Config, err)
	}
	_, refused := ingestHooks(top["hooks"], io.Discard)
	return refused, nil
}

// unmodeledKeys returns the sorted keys of m that are not in modeled.
func unmodeledKeys(m map[string]any, modeled map[string]bool) []string {
	var out []string
	for k := range m {
		if !modeled[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// quotedKeys renders untrusted native key names for a warning line: each key
// %q-quoted (control bytes escaped — a key containing a newline cannot forge
// a second warning line), comma-separated.
func quotedKeys(keys []string) string {
	qs := make([]string, len(keys))
	for i, k := range keys {
		qs[i] = strconv.Quote(k)
	}
	return strings.Join(qs, ", ")
}
