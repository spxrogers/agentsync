package cli_test

import (
	"strings"
	"testing"
)

// TestSecrets_ExpandsEnvHomeInIdentityFile pins the user-home expansion the
// `secret` subcommands apply to [secrets] paths — the same ${env:HOME} / ~
// expansion apply applies (TestApply_SecretsEnvHomeIdentity), reached through
// the CLI's vault binding rather than through AgeBackend. The init template
// ships identity_file = "${env:HOME}/.config/agentsync/age.key"; a vault bound
// without the user home resolves that literally (a relative path joined under
// the agentsync home), so `secret set` refuses with "unreadable by
// identity_file" and `secret get` cannot decrypt. Under AGENTSYNC_TARGET_ROOT
// the user home IS the target root, which is what makes the expansion
// observable here.
//
// It exists because the binding became an explicit argument (secrets.NewVault
// takes userHome; the CLI supplies paths.HomeDir in vaultFor) and nothing else
// drove `secret set`/`get` through a ${env:HOME} path: with the user home
// dropped from vaultFor, the whole suite stayed green.
func TestSecrets_ExpandsEnvHomeInIdentityFile(t *testing.T) {
	tmp, env, recipient := setupFirstRunSecrets(t)
	writeSecretsConfig(t, tmp, recipient,
		"file = \"secrets/secrets.age\"\n",
		"identity_file = \"${env:HOME}/.config/agentsync/age.key\"\n")

	if out, err := runCLI(t, env, "secret", "set", "probe.envhome=via_env_home"); err != nil {
		t.Fatalf("secret set through a ${env:HOME} identity_file failed: %v\n%s", err, out)
	}
	out, err := runCLI(t, env, "secret", "get", "probe.envhome")
	if err != nil {
		t.Fatalf("secret get through a ${env:HOME} identity_file failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "via_env_home") {
		t.Fatalf("secret get did not print the value set through the expanded identity path; got:\n%s", out)
	}
	// The seeded key must survive: the set decrypted and re-encrypted the
	// existing vault rather than starting a new one at a mis-resolved path.
	out, err = runCLI(t, env, "secret", "get", "github.token")
	if err != nil || !strings.Contains(out, "ghp_firstrun") {
		t.Fatalf("seeded github.token must survive a set through the expanded path: err=%v out:\n%s", err, out)
	}
}
