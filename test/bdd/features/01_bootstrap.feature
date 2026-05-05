Feature: Bootstrap and inspect commands
  As an engineer setting up agentsync on a new machine
  I want `init`, `doctor`, and `verify` to behave predictably
  So that I can trust the tool before I commit any config to it.

  Scenario: init scaffolds the source layout
    Given a clean agentsync home
    When I run "agentsync init"
    Then the command succeeds
    And the output contains "initialized"
    And the directory ".agentsync" exists
    And the directory ".agentsync/mcp" exists
    And the directory ".agentsync/marketplaces" exists
    And the directory ".agentsync/plugins" exists
    And the file ".agentsync/agentsync.toml" exists

  Scenario: init refuses to overwrite an existing home
    Given a clean agentsync home
    And I have run "agentsync init"
    When I run "agentsync init" and it fails
    Then the command fails
    And the output contains "already contains"

  Scenario: verify reports valid schema after init
    Given a clean agentsync home
    And I have run "agentsync init"
    When I run "agentsync verify"
    Then the command succeeds
    And the output contains "ok"

  Scenario: doctor inspects the environment
    Given a clean agentsync home
    And I have run "agentsync init"
    When I run "agentsync doctor"
    Then the command succeeds

  Scenario: --version prints something
    Given a clean agentsync home
    When I run "agentsync --version"
    Then the command succeeds
    And the output contains "agentsync"
