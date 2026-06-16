package generic

// Specs returns the verified breadth-tier agent table. Each entry's paths were
// cross-referenced against the agent's upstream docs AND prior-art config-sync
// tools (ruler, rulesync) before inclusion — see docs/capability-matrix.md
// (§ "Breadth tier") for the per-agent basis and the deliberate exclusions.
//
// Rules of the table:
//   - MEMORY is the universal axis: every entry projects the canonical memory to
//     the agent's verified rules/instructions file (the AGENTS.md standard where
//     the agent reads it, else its agent-specific file). Plain markdown body.
//   - MCP is enabled ONLY where the agent reads a JSON server-map file whose
//     dialect the engine can express faithfully (root key + transport field +
//     url key). Agents whose MCP is an array, YAML, TOML, app-storage, or
//     otherwise unmodeled leave MCP empty — the engine reports it as a skip
//     rather than write a shape the agent can't read.
//   - SKILLS is enabled where the agent natively scans Agent-Skills (SKILL.md)
//     directories (the open agentskills.io spec). The on-disk skill format is
//     uniform across agents, so there is no dialect to model — only the scanned
//     directory varies. Most agents read the cross-vendor `.agents/skills`
//     convention (shared with Codex, so the render pipeline dedupes the
//     byte-identical ops); a few scan only their own `.<agent>/skills`. Agents
//     that don't natively scan a SKILL.md directory — Jules/Firebase (publish
//     skills FOR other agents), Amazon Q (skills only via an MCP server, a
//     different component), OpenHands (programmatic load, no auto-scanned dir) —
//     leave Skills empty and report it as a skip.
//   - A scope with no verified target is left empty (reported as a skip), never
//     guessed. Low-confidence user-scope paths from the research are omitted.
//
// Deep, agent-specific adapters (claude, codex, cursor, gemini, opencode,
// continuedev, windsurf, roo, cline) are NOT here — they live in their own
// packages with richer component support.
func Specs() []Spec {
	return []Spec{
		// ---- MCP-enabled (verified JSON server-map dialects) ----

		// Qwen Code — Gemini-CLI lineage. Native context file QWEN.md; MCP in
		// settings.json with the Gemini dual-URL split: `httpUrl` = streamable
		// HTTP, `url` = SSE (httpUrl wins when both are present, per upstream).
		{
			Name: "qwen", DetectBin: "qwen", DetectDir: ".qwen",
			Memory: FileTarget{User: ".qwen/QWEN.md", Project: "QWEN.md"},
			MCP:    MCPTarget{User: ".qwen/settings.json", Project: ".qwen/settings.json", RemoteURLKey: "httpUrl", SSEURLKey: "url"},
			Skills: FileTarget{User: ".qwen/skills", Project: ".qwen/skills"},
		},
		// Warp — WARP.md rules; MCP in `.warp/.mcp.json` (inferred). Global rules
		// are app-managed (omitted).
		{
			Name: "warp", DetectBin: "warp", DetectDir: ".warp",
			Memory: FileTarget{Project: "WARP.md"},
			MCP:    MCPTarget{User: ".warp/.mcp.json", Project: ".warp/.mcp.json"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Junie (JetBrains) — memory default is the project AGENTS.md (JetBrains
		// documents no global guidelines file, so no user-scope memory target);
		// MCP `.junie/mcp/mcp.json` at both scopes (documented).
		{
			Name: "junie", DetectBin: "junie", DetectDir: ".junie",
			Memory: FileTarget{Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".junie/mcp/mcp.json", Project: ".junie/mcp/mcp.json"},
			Skills: FileTarget{User: ".junie/skills", Project: ".junie/skills"},
		},
		// Kiro (AWS) — steering markdown; MCP `.kiro/settings/mcp.json`.
		{
			Name: "kiro", DetectBin: "kiro", DetectDir: ".kiro",
			Memory: FileTarget{User: ".kiro/steering/agentsync.md", Project: ".kiro/steering/agentsync.md"},
			MCP:    MCPTarget{User: ".kiro/settings/mcp.json", Project: ".kiro/settings/mcp.json"},
			Skills: FileTarget{User: ".kiro/skills", Project: ".kiro/skills"},
		},
		// Kilo Code — Roo/Cline lineage. Project rules dir + `.kilocode/mcp.json`
		// (claude-style). Global MCP is VS Code app-storage (omitted).
		{
			Name: "kilocode", DetectBin: "kilocode", DetectDir: ".kilocode",
			Memory: FileTarget{Project: ".kilocode/rules/agentsync.md"},
			MCP:    MCPTarget{Project: ".kilocode/mcp.json"},
			Skills: FileTarget{User: ".kilo/skills", Project: ".agents/skills"},
		},
		// Amazon Q Developer CLI — project rules dir; MCP `.amazonq/mcp.json` (proj)
		// + `~/.aws/amazonq/mcp.json` (global). Global rules dir unverified (omitted).
		// No native skills dir: Q consumes SKILL.md only via an MCP server (a
		// different component), so Skills is left empty (reported as a skip).
		{
			Name: "amazonq", DetectBin: "q", DetectDir: ".amazonq",
			Memory: FileTarget{Project: ".amazonq/rules/agentsync.md"},
			MCP:    MCPTarget{User: ".aws/amazonq/mcp.json", Project: ".amazonq/mcp.json"},
		},
		// Factory Droid — AGENTS.md; MCP `.factory/mcp.json` with explicit `type`.
		{
			Name: "factory", DetectBin: "droid", DetectDir: ".factory",
			Memory: FileTarget{Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".factory/mcp.json", Project: ".factory/mcp.json", TransportKey: "type"},
			Skills: FileTarget{User: ".factory/skills", Project: ".factory/skills"},
		},
		// Pi Coding Agent — AGENTS.md (project + ~/.pi/agent/AGENTS.md); MCP
		// `~/.pi/agent/mcp.json` (inferred). No documented project MCP file.
		{
			Name: "pi", DetectBin: "pi", DetectDir: ".pi",
			Memory: FileTarget{User: ".pi/agent/AGENTS.md", Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".pi/agent/mcp.json"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Zed — AGENTS.md; MCP in settings.json under `context_servers` (inferred).
		{
			Name: "zed", DetectBin: "zed", DetectDir: ".config/zed",
			Memory: FileTarget{User: ".config/zed/AGENTS.md", Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".config/zed/settings.json", Project: ".zed/settings.json", RootKey: "context_servers"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Firebase Studio (IDX) — `.idx/airules.md`; MCP `.idx/mcp.json` (inferred).
		// Cloud IDE: per-workspace only (no user scope). No native skills dir:
		// Firebase publishes skills FOR other assistants, so Skills stays empty.
		{
			Name: "firebase", DetectDir: ".idx",
			Memory: FileTarget{Project: ".idx/airules.md"},
			MCP:    MCPTarget{Project: ".idx/mcp.json"},
		},
		// GitHub Copilot (VS Code) — `.github/copilot-instructions.md`; MCP
		// `.vscode/mcp.json` under `servers` with explicit `type`.
		{
			Name:   "copilot",
			Memory: FileTarget{Project: ".github/copilot-instructions.md"},
			MCP:    MCPTarget{Project: ".vscode/mcp.json", RootKey: "servers", TransportKey: "type"},
			Skills: FileTarget{User: ".copilot/skills", Project: ".github/skills"},
		},
		// GitHub Copilot CLI — AGENTS.md; MCP `~/.copilot/mcp-config.json` with
		// explicit `type` whose stdio value is "local".
		{
			Name: "copilot-cli", DetectBin: "copilot", DetectDir: ".copilot",
			Memory: FileTarget{Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".copilot/mcp-config.json", TransportKey: "type", StdioValue: "local"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Crush (Charm) — AGENTS.md (default context); MCP in crush.json under the
		// `mcp` key with explicit `type`.
		{
			Name: "crush", DetectBin: "crush", DetectDir: ".config/crush",
			Memory: FileTarget{Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".config/crush/crush.json", Project: "crush.json", RootKey: "mcp", TransportKey: "type"},
			Skills: FileTarget{User: ".config/crush/skills", Project: ".agents/skills"},
		},
		// Amp (Sourcegraph) — AGENTS.md (project + ~/.config/amp/AGENTS.md). MCP is
		// the namespaced `amp.mcpServers` key (a flat dotted key, not a nested map)
		// in `~/.config/amp/settings.json` (JSONC — handled by the JSONC-tolerant merge).
		{
			Name: "amp", DetectBin: "amp", DetectDir: ".config/amp",
			Memory: FileTarget{User: ".config/amp/AGENTS.md", Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".config/amp/settings.json", RootKey: "amp.mcpServers"},
			Skills: FileTarget{User: ".config/agents/skills", Project: ".agents/skills"},
		},
		// Antigravity (Google) — AGENTS.md (+ GEMINI.md). MCP in
		// `~/.gemini/config/mcp_config.json` (shared by the Antigravity IDE + CLI;
		// per Google's codelab); remote servers use `serverUrl`. Global rules path
		// is secondary-sourced, so memory stays project-scope.
		{
			Name: "antigravity", DetectBin: "agy", DetectDir: ".agent",
			Memory: FileTarget{Project: "AGENTS.md"},
			MCP:    MCPTarget{User: ".gemini/config/mcp_config.json", RemoteURLKey: "serverUrl"},
			// Project-scope only: the global Antigravity skills path is disputed
			// across sources (~/.gemini/config/skills vs the CLI's
			// ~/.gemini/antigravity-cli/skills), so we don't assert a user target.
			Skills: FileTarget{Project: ".agents/skills"},
		},

		// ---- MCP-skipped (MCP is array/YAML/TOML/app-storage/cloud — reported as a skip).
		//      Most still carry memory + skills; only Jules/OpenHands have neither MCP nor skills. ----
		// Goose (Block) — `.goosehints`. MCP lives in YAML `config.yaml` extensions.
		{
			Name: "goose", DetectBin: "goose", DetectDir: ".config/goose",
			Memory: FileTarget{Project: ".goosehints"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Jules (Google) — AGENTS.md. Cloud agent; MCP is dashboard-only. No
		// native skills dir (it reads AGENTS.md only), so Skills is left empty.
		{
			Name: "jules", DetectBin: "jules",
			Memory: FileTarget{Project: "AGENTS.md"},
		},
		// OpenHands — AGENTS.md (current). MCP is TOML `[mcp]` arrays. Skills are
		// loaded programmatically (no auto-scanned dir to drop SKILL.md into), so
		// Skills is left empty.
		{
			Name: "openhands", DetectBin: "openhands", DetectDir: ".openhands",
			Memory: FileTarget{Project: "AGENTS.md"},
		},
		// Trae AI — `.trae/rules/project_rules.md`. MCP is a non-standard array shape.
		{
			Name: "trae", DetectDir: ".trae",
			Memory: FileTarget{Project: ".trae/rules/project_rules.md"},
			// Project scope only: no user/global Trae skills directory is documented.
			Skills: FileTarget{Project: ".agents/skills"},
		},
		// JetBrains AI Assistant — `.aiassistant/rules/`. MCP is IDE app-storage.
		{
			Name: "jetbrains", DetectDir: ".aiassistant",
			Memory: FileTarget{Project: ".aiassistant/rules/agentsync.md"},
			// AI Assistant loads skills through its configured external agent
			// (Claude/Codex) from the cross-agent project `.agents/skills`.
			Skills: FileTarget{Project: ".agents/skills"},
		},
		// AugmentCode — `.augment/rules/` (project + ~/.augment/rules/). MCP is
		// IDE app-storage.
		{
			Name: "augmentcode", DetectBin: "auggie", DetectDir: ".augment",
			Memory: FileTarget{User: ".augment/rules/agentsync.md", Project: ".augment/rules/agentsync.md"},
			Skills: FileTarget{User: ".agents/skills", Project: ".agents/skills"},
		},
		// Mistral (Vibe / Le Chat) — AGENTS.md (ruler default). MCP is TOML
		// (`.vibe/config.toml`).
		{
			Name: "mistral", DetectBin: "vibe", DetectDir: ".vibe",
			Memory: FileTarget{Project: "AGENTS.md"},
			Skills: FileTarget{User: ".vibe/skills", Project: ".agents/skills"},
		},
	}
}
