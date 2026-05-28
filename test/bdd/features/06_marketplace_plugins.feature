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

  Scenario: explicit manifest skill/command/agent paths project to destination files with correct content
    # Regression lock for 4b781b1: manifest-declared relative paths (skills/commands/agents)
    # must be joined against the plugin cache dir, not used verbatim. Before the fix, these
    # paths were passed directly to resolvePluginRoot which only substitutes ${CLAUDE_PLUGIN_ROOT},
    # leaving relative paths unresolved and producing missing/stub destination files.
    Given I create a local marketplace "fixture-proj-mp" with plugin "proj-plugin" with explicit skills commands and agents
    When I run "agentsync marketplace add ./fixture-proj-mp"
    Then the command succeeds
    When I run "agentsync plugin install proj-plugin@fixture-proj-mp"
    Then the command succeeds
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude/skills/proj-skill/SKILL.md" exists
    And the file ".claude/skills/proj-skill/SKILL.md" contains "BODY_TOKEN_skill_proj-skill"
    And the file ".claude/skills/proj-skill/scripts/run.sh" exists
    And the file ".claude/skills/proj-skill/scripts/run.sh" contains "BODY_TOKEN_skill_script"
    And the file ".claude/agents/proj-agent.md" exists
    And the file ".claude/agents/proj-agent.md" contains "BODY_TOKEN_agent_proj-agent"
    And the file ".claude/commands/proj-cmd.md" exists
    And the file ".claude/commands/proj-cmd.md" contains "BODY_TOKEN_cmd_proj-cmd"
