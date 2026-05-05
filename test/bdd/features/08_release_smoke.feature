Feature: Release smoke
  These scenarios are the last green-light before an engineer cuts a release.
  They exercise only externally-observable behaviour from the binary surface.

  Scenario: --help prints a usage banner
    Given a clean agentsync home
    When I run "agentsync --help"
    Then the command succeeds
    And the output contains "agentsync"
    And the output contains "Available Commands"

  Scenario: every documented top-level subcommand is wired
    Given a clean agentsync home
    When I run "agentsync --help"
    Then the output contains "init"
    And the output contains "agent"
    And the output contains "apply"
    And the output contains "status"
    And the output contains "diff"
    And the output contains "reconcile"
    And the output contains "doctor"
    And the output contains "verify"
    And the output contains "marketplace"
    And the output contains "plugin"
    And the output contains "secrets"
    And the output contains "explain"
    And the output contains "import"
    And the output contains "update"

  Scenario: full v1.0 lifecycle on a fresh box
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agents "claude" and "opencode"
    And I write the file ".agentsync/mcp/github.toml" with:
      """
      [server]
      type    = "stdio"
      command = "npx"
      args    = ["-y", "@modelcontextprotocol/server-github"]
      agents  = ["*"]
      """
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude.json" contains "@modelcontextprotocol/server-github"
    And the file ".config/opencode/opencode.json" contains "@modelcontextprotocol/server-github"
    When I run "agentsync status"
    Then the command succeeds
    And the output does not contain "drift"
