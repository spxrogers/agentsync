# Marketplace distribution in the wild

Research notes on how vendors distribute MCP/plugin integrations across multiple AI coding agents today (Claude Code, Codex, Cursor, Gemini, etc.). Used to validate the "marketplace primacy" thesis in `PLAN.md` and to seed test fixtures for M2.

## TL;DR

There is no cross-agent plugin marketplace in 2026. Every vendor we looked at converges on the same shape:

1. **One canonical hosted MCP server** as the real artifact.
2. **One Claude Code plugin** in Anthropic's official marketplace, which wraps that MCP server (and may add skills/slash commands).
3. **Per-agent copy-paste config snippets** for everyone else (Codex, Gemini CLI, Cursor IDE-rules path, Copilot CLI, etc.). No published artifact, no registry — just docs.

Vendors fan-out manually. The fragmentation is real and visible in their repo layouts.

## Pattern catalog

Three distribution patterns observed:

1. **Single remote MCP, BYO client config** — the lowest common denominator. Vendor hosts an OAuth-protected endpoint, docs page has a copy-pasteable JSON/TOML snippet per agent. Used by Atlassian, Slack, GitHub, Linear, Notion. This is what every non-Anthropic agent actually consumes.
2. **Claude Code plugin = MCP + skills/commands wrapper** — for agents with a real marketplace (Claude Code, OpenCode), vendors publish a thin bundle that registers the same MCP server and layers on slash commands/skills/subagents.
3. **Aggregator catalogs** — Docker MCP Catalog, Smithery, mcpmarket.com, claudepluginhub, "awesome-*" lists. Secondary indexes that republish vendor MCPs with one-click installers per host. Not authoritative.

## Case 1 — Atlassian

Atlassian does not ship a "Codex plugin." They ship two things:

- **The Rovo MCP Server**: hosted at `https://mcp.atlassian.com/v1/mcp/authv2`, OAuth 2.1, respects Jira/Confluence permissions. The canonical artifact.
- **A Claude Code plugin**: `/plugin install atlassian@claude-plugins-official` (~57k installs at time of writing). Wraps the MCP server above and adds Claude-specific skills:
  - `/capture-tasks-from-meeting-notes`
  - `/spec-to-backlog`
  - `/generate-status-report`
  - `/search-company-knowledge`
  - `/triage-issue`

For **Codex**, there is no Atlassian-published plugin or marketplace listing. Official path: edit `~/.codex/config.toml`, add `[mcp_servers.atlassian]` block pointing at the hosted endpoint, drop markdown skills under `~/.codex/skills/` with `[features].skills = true`. Atlassian's "Setting up clients" doc explicitly punts: *"depending on the client you're using, the setup process may vary."*

Same shape holds for Gemini CLI, Cursor, Copilot CLI, Amazon Q — all listed as supported clients but each is a separate copy-paste integration guide.

## Case 2 — Slack

Slack's setup is structurally identical to Atlassian's but with one telling difference: they made the multi-marketplace problem explicit in their repo layout.

**Canonical artifact**: `https://mcp.slack.com/mcp` (OAuth 2.0, hosted-only, no SSE).

**Official "plugin" repo**: [`slackapi/slack-mcp-plugin`](https://github.com/slackapi/slack-mcp-plugin). Description: *"configuration information for the Slack MCP to be added to other clients."* Layout:

```
slack-mcp-plugin/
├── .claude-plugin/    # Claude Code shim
├── .cursor-plugin/    # Cursor shim
├── .cursor-mcp.json
└── .mcp.json
```

This is the manual, hand-maintained version of what opensync wants to automate. Same vendor, same backend MCP, parallel shim folders side-by-side because there is no shared format. If Slack adds Codex tomorrow, they will add a `.codex-plugin/` folder.

**Officially supported clients**: Claude.ai, Claude Code, Perplexity, Cursor. **Codex is not on the list.**

**Second-artifact problem**: there are *two* "official-ish" Slack MCP servers:

- Slack's hosted one (OAuth, recommended)
- Anthropic's `@modelcontextprotocol/server-slack` (npm, bot-token, older)
- plus prominent community forks (`korotovsky/slack-mcp-server`)

When a user types `opensync mcp add slack`, which do they mean? Need a canonical-id namespace (`slack@slackapi` vs `slack@modelcontextprotocol` vs `slack@korotovsky`) to disambiguate. Atlassian had one clear winner; Slack does not.

## Side-by-side

| | Atlassian | Slack |
|---|---|---|
| Canonical MCP | one hosted (Rovo) | one hosted (`mcp.slack.com`) |
| Claude Code plugin | yes, with skills wrapper | yes, just MCP registration |
| Multi-agent shim repo | no — separate per-client docs | yes — single repo with `.claude-plugin/` + `.cursor-plugin/` |
| Codex official path | none | none |
| Competing "official" servers | one | two (hosted vs npm) + community forks |

## Implications for opensync

1. **Marketplace primacy is real but only on Claude Code and OpenCode.** For everyone else, what opensync projects is not a marketplace install — it is the same hand-rolled MCP/skills config that vendors today expect users to paste manually. The `Plugin content × Agent` translation table in `PLAN.md` is exactly the gap.

2. **Atlassian is the canonical M2 test fixture.** Take `atlassian@claude-plugins-official`, project the MCP server universally, project the 5 slash-commands as Cursor/Continue rules, skip skills on Codex with a logged reason. If opensync handles this one plugin cleanly across all seven agents, it has solved the real problem.

3. **`slackapi/slack-mcp-plugin` is the proof-of-concept fixture for per-agent passthrough.** A real-world repo with `.claude-plugin/` and `.cursor-plugin/` side-by-side is exactly what opensync's source layout should ingest cleanly. Worth using as a golden test case in M2.

4. **Canonical IDs need to handle multiple implementations of the same logical service.** Slack exposes the gap: hosted-OAuth vs npm-bot-token vs community fork. The current `id@marketplace` scheme in `PLAN.md` works when there is one marketplace with one canonical entry; it is ambiguous when "official" splits. Recommend extending to `id@marketplace` with a tiebreak on implementation, or namespacing by upstream owner.

## Sources

- [Atlassian – Claude Plugin (claude.com)](https://claude.com/plugins/atlassian)
- [Discover and install prebuilt plugins through marketplaces (Claude Code Docs)](https://code.claude.com/docs/en/discover-plugins)
- [anthropics/claude-plugins-official (GitHub)](https://github.com/anthropics/claude-plugins-official)
- [atlassian/atlassian-mcp-server (GitHub)](https://github.com/atlassian/atlassian-mcp-server)
- [Getting started with the Atlassian Rovo MCP Server](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/)
- [Setting up clients — Atlassian Rovo MCP Server](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/setting-up-clients/)
- [Extend Atlassian into any AI assistant using MCP](https://www.atlassian.com/platform/remote-mcp-server)
- [Atlassian MCP Server: Quick Start with Your Favorite Agent (Docker)](https://www.docker.com/blog/atlassian-remote-mcp-server-getting-started-with-docker/)
- [Slack MCP server overview (Slack Developer Docs)](https://docs.slack.dev/ai/slack-mcp-server/)
- [Connect to Claude (Slack Developer Docs)](https://docs.slack.dev/ai/slack-mcp-server/connect-to-claude/)
- [Guide to the Slack MCP server (slack.com)](https://slack.com/help/articles/48855576908307-Guide-to-the-Slack-MCP-server)
- [slackapi/slack-mcp-plugin (GitHub)](https://github.com/slackapi/slack-mcp-plugin)
- [@modelcontextprotocol/server-slack (npm)](https://www.npmjs.com/package/@modelcontextprotocol/server-slack)
- [korotovsky/slack-mcp-server (GitHub)](https://github.com/korotovsky/slack-mcp-server)
- [How to integrate Slack MCP with Codex (Composio)](https://composio.dev/toolkits/slack/framework/codex)
