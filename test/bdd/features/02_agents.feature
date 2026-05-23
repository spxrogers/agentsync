Feature: Agent registry
  Engineers register coding agents (claude, opencode, codex, cursor) in the
  source-of-truth, and agentsync fans canonical config out to each.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"

  Scenario: agent add registers an agent
    When I run "agentsync agent add claude"
    Then the command succeeds
    And the output contains "added agent"
    And the file ".agentsync/agentsync.toml" contains "claude"

  Scenario: agent list shows enabled agents
    Given I have added agents "claude" and "opencode"
    When I run "agentsync agent list"
    Then the command succeeds
    And the output contains "claude"
    And the output contains "opencode"

  Scenario: agent disable --purge prunes owned keys from a shared file
    Given I have added agent "claude"
    And I write the file ".agentsync/mcp/echo.toml" with:
      """
      [server]
      type    = "stdio"
      command = "echo"
      args    = ["hi"]
      agents  = ["claude"]
      """
    And I run "agentsync apply"
    And the file ".claude.json" exists
    When I run "agentsync agent disable claude --purge"
    Then the command succeeds
    # .claude.json is a key-merge dest: agentsync owns only /mcpServers/echo,
    # not the whole file. Purge must prune the owned server (so a user's
    # foreign keys would survive), never delete the shared file.
    And the file ".claude.json" exists
    And the file ".claude.json" does not contain "echo"

  Scenario: agent disable keeps the agent registered but disabled
    Given I have added agent "claude"
    When I run "agentsync agent disable claude --purge"
    Then the command succeeds
    When I run "agentsync agent list"
    Then the output contains "claude"
    And the output contains "enabled=false"
