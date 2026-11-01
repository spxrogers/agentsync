package generic

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/adapter"
	"github.com/spxrogers/agentsync/internal/adapter/claude"
	"github.com/spxrogers/agentsync/internal/source"
)

func RenderVSCodeInstructions(fragments map[string]string, harness, home, profile string, scope adapter.Scope, project string) ([]adapter.FileOp, error) {
	dir := filepath.Join(home, ".copilot", "instructions")
	if harness == "vscode-local" {
		dir = profile
	}
	if scope == adapter.ScopeProject {
		dir = filepath.Join(project, ".github", "instructions")
	}
	var names []string
	for name := range fragments {
		if strings.HasSuffix(name, ".instructions.md") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var ops []adapter.FileOp
	for _, name := range names {
		if filepath.IsAbs(name) || strings.Contains(name, "\\") || !filepath.IsLocal(filepath.FromSlash(name)) {
			return nil, fmt.Errorf("invalid instructions fragment path %q", name)
		}
		if name == "agentsync.instructions.md" {
			return nil, fmt.Errorf("instructions fragment %q collides with global instructions", name)
		}
		fm, body, err := source.ParseFrontmatter([]byte(fragments[name]))
		if err != nil {
			return nil, fmt.Errorf("instructions %s: %w", name, err)
		}
		for key := range fm {
			if key != "name" && key != "description" && key != "applyTo" {
				return nil, fmt.Errorf("instructions %s: unsupported field %q", name, key)
			}
			if value, ok := fm[key].(string); !ok || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("instructions %s: %s must be a nonempty string", name, key)
			}
		}
		if fm["applyTo"] == nil && fm["description"] == nil {
			return nil, fmt.Errorf("instructions %s requires applyTo or description", name)
		}
		body = source.RenderManagedMemory(body, nil, filepath.Base(name), true)
		content, err := claude.EncodeFrontmatter(fm, body)
		if err != nil {
			return nil, err
		}
		ops = append(ops, adapter.FileOp{Path: filepath.Join(dir, filepath.FromSlash(name)), Content: content, Mode: 0o644, SourceID: filepath.Join("memory", "fragments", filepath.FromSlash(name)), MergeStrategy: "replace"})
	}
	return ops, nil
}

func renderCopilotHooks(c source.Canonical, home string, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	events := map[string]bool{"sessionStart": true, "sessionEnd": true, "userPromptSubmitted": true, "preToolUse": true, "postToolUse": true, "preCompact": true, "subagentStart": true, "subagentStop": true, "agentStop": true}
	byEvent := map[string][]map[string]any{}
	var skips []adapter.Skip
	for _, hook := range c.Hooks {
		event := hook.Event.Unverified()
		reason := ""
		switch {
		case !c.Config.Copilot.HooksEnabled:
			reason = "Copilot hooks require explicit [copilot] hooks_enabled = true after script review"
		case !events[event]:
			reason = "use a native Copilot event name and review the script payload; automatic hook migration is unsupported"
		case hook.Type != "" && hook.Type != "command":
			reason = "only command hooks are supported"
		case hook.Matcher != "":
			reason = "filtered hooks require native configuration; matcher semantics are not translated"
		case hook.Command == "":
			reason = "hook command is empty"
		}
		if reason != "" {
			skips = append(skips, adapter.Skip{Component: "hook", Name: event, Reason: reason, Kind: adapter.SkipDropped})
			continue
		}
		entry := map[string]any{"type": "command", "command": hook.Command}
		if hook.Timeout > 0 {
			entry["timeoutSec"] = hook.Timeout
		}
		byEvent[event] = append(byEvent[event], entry)
	}
	if len(byEvent) == 0 {
		return nil, skips, nil
	}
	path := filepath.Join(home, ".copilot", "hooks", "agentsync.json")
	if scope == adapter.ScopeProject {
		path = filepath.Join(project, ".github", "hooks", "agentsync.json")
	}
	body, err := json.MarshalIndent(map[string]any{"version": 1, "hooks": byEvent}, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return []adapter.FileOp{{Path: path, Content: append(body, '\n'), Mode: 0o644, SourceID: "hooks/* (multiple)", MergeStrategy: "merge-jsonc-keys"}}, skips, nil
}

func RenderVSCodeMCP(c source.Canonical, harness, path string) ([]adapter.FileOp, []adapter.Skip, error) {
	projection := New(Spec{Name: harness, MCP: MCPTarget{User: path, RootKey: "servers", TransportKey: "type"}}, Options{})
	return projection.renderMCP(c, adapter.ScopeUser, "")
}

func RenderVSCodeSkills(c source.Canonical, home string, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	projection := New(Spec{Skills: FileTarget{User: ".copilot/skills", Project: ".github/skills"}}, Options{TargetRoot: home})
	return projection.renderSkills(c, scope, project)
}

func RenderVSCodeResources(c source.Canonical, harness, home, profile string, scope adapter.Scope, project string) ([]adapter.FileOp, []adapter.Skip, error) {
	if err := adapter.RequireProjectRoot(scope, project); err != nil {
		return nil, nil, err
	}
	agentsDir := filepath.Join(home, ".copilot", "agents")
	commandsDir := filepath.Join(home, ".copilot", "skills")
	if harness == "vscode-local" {
		agentsDir, commandsDir = profile, profile
	}
	if scope == adapter.ScopeProject {
		agentsDir = filepath.Join(project, ".github", "agents")
		commandsDir = filepath.Join(project, ".github", "skills")
		if harness == "vscode-local" {
			commandsDir = filepath.Join(project, ".github", "prompts")
		}
	}
	var ops []adapter.FileOp
	var skips []adapter.Skip
	appendMarkdown := func(path, id string, fm map[string]any, body string) error {
		body = source.RenderManagedMemory(body, nil, filepath.Base(path), true)
		content, err := claude.EncodeFrontmatter(fm, body)
		if err != nil {
			return err
		}
		ops = append(ops, adapter.FileOp{Path: path, Content: content, Mode: 0o644, SourceID: id, MergeStrategy: "replace"})
		return nil
	}
	for _, subagent := range c.Subagents {
		if err := source.ValidateComponentID("subagent", subagent.Name); err != nil {
			return nil, nil, err
		}
		fm, reason := vscodeAgentFrontmatter(subagent.Frontmatter, harness)
		if reason != "" {
			skips = append(skips, adapter.Skip{Component: "subagent", Name: subagent.Name, Reason: reason, Kind: adapter.SkipDropped})
			continue
		}
		if err := appendMarkdown(filepath.Join(agentsDir, subagent.Name+".agent.md"), source.SubagentSourceID(subagent.Name), fm, subagent.Body); err != nil {
			return nil, nil, err
		}
	}
commands:
	for _, command := range c.Commands {
		if err := source.ValidateComponentID("command", command.Name); err != nil {
			return nil, nil, err
		}
		fm := map[string]any{}
		allowed := map[string]bool{"name": true, "description": true, "argument-hint": true, "agent": true, "model": true, "tools": true}
		if harness == "copilot" {
			allowed = map[string]bool{"description": true, "argument-hint": true}
			fm["name"] = command.Name
			fm["disable-model-invocation"] = true
			fm["user-invocable"] = true
			if _, exists := command.Frontmatter["description"]; !exists {
				fm["description"] = "Run " + command.Name
			}
			for _, skill := range c.Skills {
				if skill.Name == command.Name {
					skips = append(skips, adapter.Skip{Component: "command", Name: command.Name, Reason: "command collides with an existing skill; rename the command or skill", Kind: adapter.SkipDropped})
					continue commands
				}
			}
		}
		var unsupported []string
		for key, value := range command.Frontmatter {
			if !allowed[key] {
				unsupported = append(unsupported, key)
				continue
			}
			fm[key] = value
		}
		if len(unsupported) > 0 {
			sort.Strings(unsupported)
			skips = append(skips, adapter.Skip{Component: "command", Name: command.Name, Reason: "unsupported command fields: " + strings.Join(unsupported, ", "), Kind: adapter.SkipDropped})
			continue
		}
		path := filepath.Join(commandsDir, command.Name+".prompt.md")
		body := command.Body
		if harness == "copilot" {
			path = filepath.Join(commandsDir, command.Name, "SKILL.md")
			body = "<!-- agentsync:command -->\n" + body
		}
		if err := appendMarkdown(path, filepath.Join("commands", command.Name+".md"), fm, body); err != nil {
			return nil, nil, err
		}
	}
	return ops, skips, nil
}

func vscodeAgentFrontmatter(original map[string]any, harness string) (map[string]any, string) {
	fm := map[string]any{}
	allowed := map[string]bool{"name": true, "description": true, "tools": true, "model": true, "user-invocable": true, "disable-model-invocation": true}
	if harness == "vscode-local" {
		for _, key := range []string{"argument-hint", "agents", "handoffs", "advancedOptions"} {
			allowed[key] = true
		}
	} else {
		allowed["skills"] = true
		allowed["reasoning-effort"] = true
	}
	var unsupported []string
	for key, value := range original {
		if key == "target" {
			expected := "github-copilot"
			if harness == "vscode-local" {
				expected = "vscode"
			}
			if value != expected {
				return nil, "agent targets another harness"
			}
			fm[key] = value
			continue
		}
		if !allowed[key] {
			unsupported = append(unsupported, key)
			continue
		}
		fm[key] = value
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		return nil, "unsupported agent fields: " + strings.Join(unsupported, ", ")
	}
	if harness == "copilot" {
		if description, ok := fm["description"].(string); !ok || strings.TrimSpace(description) == "" {
			return nil, "Copilot agents require a description"
		}
	}
	if raw, ok := fm["tools"]; ok {
		var tools []string
		switch value := raw.(type) {
		case string:
			tools = strings.Split(value, ",")
		case []string:
			tools = append([]string(nil), value...)
		case []any:
			for _, item := range value {
				tool, ok := item.(string)
				if !ok {
					return nil, "agent tools must be strings"
				}
				tools = append(tools, tool)
			}
		default:
			return nil, "agent tools must be a string or string array"
		}
		aliases := map[string]string{"Read": "read", "Grep": "search", "Glob": "search", "Bash": "execute", "Write": "edit", "Edit": "edit", "Task": "agent", "WebFetch": "web", "WebSearch": "web", "TodoWrite": "todo"}
		for index, tool := range tools {
			tool = strings.TrimSpace(tool)
			if mapped, exists := aliases[tool]; exists {
				tool = mapped
			}
			if harness == "copilot" && tool == "agent/runSubagent" {
				tool = "agent"
			}
			if harness == "copilot" && !strings.Contains(tool, "/") && tool != "*" {
				switch tool {
				case "execute", "read", "edit", "search", "agent", "web", "todo":
				default:
					return nil, "unsupported Copilot tool: " + tool
				}
			}
			tools[index] = tool
		}
		fm["tools"] = tools
	}
	return fm, ""
}

func (a *Adapter) ingestVSCodeResources(c *source.Canonical, scope adapter.Scope, project string) error {
	var skills []source.Skill
	for _, skill := range c.Skills {
		body, converted := strings.CutPrefix(source.StripManagedBanner(skill.Body), "<!-- agentsync:command -->\n")
		if !converted {
			skills = append(skills, skill)
			continue
		}
		fm := skill.Frontmatter
		if len(skill.Files) != 0 || fm["name"] != skill.Name || fm["disable-model-invocation"] != true || fm["user-invocable"] != true {
			return fmt.Errorf("command skill %q has bundled files or modified invocation metadata; edit canonical commands instead", skill.Name)
		}
		for key := range fm {
			if key != "name" && key != "description" && key != "argument-hint" && key != "disable-model-invocation" && key != "user-invocable" {
				return fmt.Errorf("command skill %q has unsupported field %q; refusing lossy import", skill.Name, key)
			}
		}
		delete(fm, "name")
		delete(fm, "disable-model-invocation")
		delete(fm, "user-invocable")
		c.Commands = append(c.Commands, source.Command{Name: skill.Name, Frontmatter: fm, Body: body})
	}
	c.Skills = skills
	dir := filepath.Join(a.opts.TargetRoot, ".copilot", "agents")
	if scope == adapter.ScopeProject {
		dir = filepath.Join(project, ".github", "agents")
	}
	entries, _, err := adapter.ReadDirOptional(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".agent.md") {
			continue
		}
		data, _, err := adapter.ReadFileOptional(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		fm, body, err := source.ParseFrontmatter(data)
		if err != nil {
			return err
		}
		c.Subagents = append(c.Subagents, source.Subagent{Name: strings.TrimSuffix(entry.Name(), ".agent.md"), Frontmatter: fm, Body: source.StripManagedBanner(body)})
	}
	instructionsDir := filepath.Join(a.opts.TargetRoot, ".copilot", "instructions")
	hooksPath := filepath.Join(a.opts.TargetRoot, ".copilot", "hooks", "agentsync.json")
	if scope == adapter.ScopeProject {
		instructionsDir = filepath.Join(project, ".github", "instructions")
		hooksPath = filepath.Join(project, ".github", "hooks", "agentsync.json")
	}
	if err := ingestVSCodeInstructions(c, instructionsDir, ""); err != nil {
		return err
	}
	if _, present, err := adapter.ReadFileOptional(hooksPath); err != nil {
		return err
	} else if present {
		return fmt.Errorf("copilot hooks import/write-back is unsupported; edit canonical hooks and apply")
	}
	return nil
}

func ingestVSCodeInstructions(c *source.Canonical, root, relative string) error {
	entries, _, err := adapter.ReadDirOptional(filepath.Join(root, relative))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := filepath.Join(relative, entry.Name())
		if entry.IsDir() {
			if err := ingestVSCodeInstructions(c, root, name); err != nil {
				return err
			}
			continue
		}
		if !strings.HasSuffix(name, ".instructions.md") || filepath.ToSlash(name) == "agentsync.instructions.md" {
			continue
		}
		data, _, err := adapter.ReadFileOptional(filepath.Join(root, name))
		if err != nil {
			return err
		}
		fm, body, err := source.ParseFrontmatter(data)
		if err != nil {
			return err
		}
		content, err := claude.EncodeFrontmatter(fm, source.StripManagedBanner(body))
		if err != nil {
			return err
		}
		if c.Memory.Fragments == nil {
			c.Memory.Fragments = map[string]string{}
		}
		c.Memory.Fragments[filepath.ToSlash(name)] = string(content)
	}
	return nil
}
