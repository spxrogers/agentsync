Feature: MCP server fanout
  The headline pain agentsync v1.0 must solve: an MCP server declared once
  in the source must land in every enabled agent's native config.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agents "claude" and "opencode"

  Scenario: a single MCP file fans out to both Claude and OpenCode
    Given I write the file ".agentsync/mcp/github.toml" with:
      """
      [server]
      type    = "stdio"
      command = "npx"
      args    = ["-y", "@modelcontextprotocol/server-github"]
      agents  = ["*"]
      """
    When I run "agentsync apply"
    Then the command succeeds
    And the output contains "applied"
    And the file ".claude.json" contains "@modelcontextprotocol/server-github"
    And the file ".config/opencode/opencode.json" contains "@modelcontextprotocol/server-github"

  Scenario: an agents allowlist scopes the fanout
    Given I write the file ".agentsync/mcp/claude-only.toml" with:
      """
      [server]
      type    = "stdio"
      command = "echo"
      args    = ["claude-only"]
      agents  = ["claude"]
      """
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude.json" contains "claude-only"
    And the file ".config/opencode/opencode.json" does not contain "claude-only"

  Scenario: apply --dry-run does not write any destination files
    Given I write the file ".agentsync/mcp/echo.toml" with:
      """
      [server]
      type    = "stdio"
      command = "echo"
      args    = ["dry"]
      agents  = ["claude"]
      """
    When I run "agentsync apply --dry-run"
    Then the command succeeds
    And the file ".claude.json" does not exist
