Feature: Marketplaces and plugins
  Plugins consumed from a Claude-style marketplace fan their components out
  to every enabled agent. v1.0 covers Claude + OpenCode.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agents "claude" and "opencode"

  Scenario: install a plugin from a local marketplace and apply
    Given I create a local marketplace "fixture-mp" with plugin "demo-plugin" exposing MCP "demo-mcp" command "echo"
    When I run "agentsync marketplace add ./fixture-mp"
    Then the command succeeds
    When I run "agentsync marketplace list"
    Then the command succeeds
    And the output contains "fixture-mp"
    When I run "agentsync plugin install demo-plugin@fixture-mp"
    Then the command succeeds
    When I run "agentsync plugin list"
    Then the command succeeds
    And the output contains "demo-plugin"
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude.json" contains "demo-mcp"
    And the file ".config/opencode/opencode.json" contains "demo-mcp"

  Scenario: explain --json produces parseable JSON
    Given I create a local marketplace "fixture-mp" with plugin "demo-plugin" exposing MCP "demo-mcp" command "echo"
    And I run "agentsync marketplace add ./fixture-mp"
    And I run "agentsync plugin install demo-plugin@fixture-mp"
    And I run "agentsync apply"
    When I run "agentsync explain --json demo-plugin"
    Then the command succeeds
    And the output is valid JSON
    And the output contains "demo-plugin"
