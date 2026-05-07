Feature: Drift detection and reconcile
  Per the design spec's 3-way classifier, agentsync must detect when the
  destination has been hand-edited away from what was last applied, and let
  the engineer resolve via reconcile.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agent "claude"
    And I write the file ".agentsync/mcp/github.toml" with:
      """
      [server]
      type    = "stdio"
      command = "npx"
      args    = ["-y", "@modelcontextprotocol/server-github"]
      agents  = ["claude"]
      """
    And I run "agentsync apply"

  Scenario: status is converged immediately after apply
    When I run "agentsync status"
    Then the command succeeds
    And the output does not contain "drift"
    And the output does not contain "conflict"

  Scenario: tampered destination is detected as drift
    Given I tamper with ".claude.json" by replacing "@modelcontextprotocol/server-github" with "tampered-value"
    When I run "agentsync status"
    Then the command succeeds
    And the output contains "drift"

  Scenario: reconcile --auto-override restores the destination
    Given I tamper with ".claude.json" by replacing "@modelcontextprotocol/server-github" with "tampered-value"
    When I run "agentsync reconcile --auto-override"
    Then the command succeeds
    And the file ".claude.json" contains "@modelcontextprotocol/server-github"
    And the file ".claude.json" does not contain "tampered-value"

  Scenario: reconcile --auto-safe is a no-op on a clean state
    When I run "agentsync reconcile --auto-safe"
    Then the command succeeds
    And the output does not contain "drift"

  Scenario: diff renders a unified-style header for drifted paths
    Given I tamper with ".claude.json" by replacing "@modelcontextprotocol/server-github" with "tampered-value"
    When I run "agentsync diff"
    Then the command succeeds
    And the output contains "--- source"
    And the output contains "+++ dest"
    And the output contains "/mcpServers/github"
