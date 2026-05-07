Feature: Safety primitives
  agentsync's correctness depends on three locked behaviours: hermetic
  AGENTSYNC_TARGET_ROOT redirection, atomic file writes, and the apply.lock
  file lock that serialises concurrent apply invocations.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agent "claude"
    And I write the file ".agentsync/mcp/echo.toml" with:
      """
      [server]
      type    = "stdio"
      command = "echo"
      args    = ["safety"]
      agents  = ["claude"]
      """

  Scenario: AGENTSYNC_TARGET_ROOT redirects every adapter path
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude.json" exists

  Scenario: concurrent apply invocations both succeed via apply.lock
    When I run two "agentsync apply" invocations concurrently
    Then the file ".claude.json" exists
    And the file ".claude.json" contains "safety"

  Scenario: apply records state for drift tracking
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".agentsync/.state/targets.json" exists

  Scenario: invalid TOML fails verify, not apply silently
    Given I write the file ".agentsync/mcp/broken.toml" with:
      """
      this is not valid toml = = =
      """
    When I run "agentsync verify" and it fails
    Then the command fails
