Feature: age-encrypted secrets
  agentsync expands ${secret:foo.bar} references at apply-time. The cleartext
  must reach the destination but never the source-of-truth.

  Background:
    Given a clean agentsync home
    And I have run "agentsync init"
    And I have added agent "claude"
    And I configure age secrets

  Scenario: secrets get round-trips an encrypted value
    Given I encrypt secret "github.token" = "ghp_bdd_xyz"
    When I run "agentsync secrets get github.token"
    Then the command succeeds
    And the output contains "ghp_bdd_xyz"

  Scenario: secrets set followed by secrets get
    When I run "agentsync secrets set openai.key=sk-bdd-test"
    Then the command succeeds
    When I run "agentsync secrets get openai.key"
    Then the command succeeds
    And the output contains "sk-bdd-test"

  Scenario: ${secret:...} is resolved on apply but not in source
    Given I encrypt secret "github.token" = "ghp_bdd_xyz"
    And I write the file ".agentsync/mcp/github.toml" with:
      """
      [server]
      type    = "stdio"
      command = "npx"
      args    = ["-y", "@modelcontextprotocol/server-github"]
      agents  = ["claude"]

      [server.env]
      GITHUB_TOKEN = "${secret:github.token}"
      """
    When I run "agentsync apply"
    Then the command succeeds
    And the file ".claude.json" contains "ghp_bdd_xyz"
    And the file ".claude.json" does not contain "${secret:"
    And the file ".agentsync/mcp/github.toml" contains "${secret:github.token}"
    And the file ".agentsync/mcp/github.toml" does not contain "ghp_bdd_xyz"

  Scenario: missing secret blocks apply with a loud error
    Given I write the file ".agentsync/mcp/needs-secret.toml" with:
      """
      [server]
      type    = "stdio"
      command = "echo"
      args    = ["x"]
      agents  = ["claude"]

      [server.env]
      OPENAI_API_KEY = "${secret:openai.missing}"
      """
    When I run "agentsync apply" and it fails
    Then the command fails
