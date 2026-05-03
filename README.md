# opensync

One source of truth for AI coding-agent configuration.

opensync syncs plugins, MCP servers, skills, agents, rules, hooks, and memory across multiple coding-agent CLIs (Claude Code, OpenCode, Cursor, OpenAI Codex CLI) from a single git-managed source repo. It borrows chezmoi's three-state model (source → target → destination) and generalises it from "one home directory" to "N agent-specific config trees", with a canonical intermediate representation in the middle and per-agent adapters at the edges.

Status: pre-release. The implementation plan lives in [`docs/PLAN.md`](docs/PLAN.md).
