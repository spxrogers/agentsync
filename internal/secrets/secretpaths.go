package secrets

import (
	"path/filepath"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// DefaultAgeFile is the encrypted-secrets location relative to the agentsync
// home, used when [secrets].file is unset (it is optional in the init
// template). apply, verify, diff, doctor, and the `secrets` subcommands MUST
// resolve the age file the same way — otherwise a user who omits `file` can
// `secrets set` / `secrets get` successfully (those default the path) yet have
// apply fail to find the same file.
const DefaultAgeFile = "secrets/secrets.age"

// ResolveAgeFile returns the absolute path to the encrypted secrets file.
// An empty cfg.File falls back to DefaultAgeFile. ${env:HOME} and a leading
// ~ are expanded via userHome; relative paths are joined under agentsyncHome.
func ResolveAgeFile(cfg source.SecretsConfig, agentsyncHome, userHome string) string {
	f := cfg.File
	if f == "" {
		f = DefaultAgeFile
	}
	return resolveSecretPath(f, agentsyncHome, userHome)
}

// ResolveIdentityFile returns the absolute path to the age identity (private
// key) file, expanding ${env:HOME} / leading ~ via userHome and joining
// relative paths under agentsyncHome. An empty identity_file returns "".
//
// The init template ships identity_file = "${env:HOME}/.config/agentsync/age.key";
// without this expansion AgeBackend.load would os.ReadFile the literal
// "${env:HOME}/..." string and fail at apply time even though doctor/verify
// (which expanded it for their stat check) reported the config healthy.
func ResolveIdentityFile(cfg source.SecretsConfig, agentsyncHome, userHome string) string {
	if cfg.IdentityFile == "" {
		return ""
	}
	return resolveSecretPath(cfg.IdentityFile, agentsyncHome, userHome)
}

func resolveSecretPath(p, agentsyncHome, userHome string) string {
	p = expandHome(p, userHome)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(agentsyncHome, p)
}

// expandHome replaces every ${env:HOME} occurrence and a leading ~ with
// userHome. userHome should be paths.HomeDir(...) so it honours
// AGENTSYNC_TARGET_ROOT (test redirection) the same way the rest of agentsync
// resolves "home".
func expandHome(p, userHome string) string {
	if userHome == "" {
		return p
	}
	p = strings.ReplaceAll(p, "${env:HOME}", userHome)
	switch {
	case p == "~":
		return userHome
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		return filepath.Join(userHome, p[2:])
	}
	return p
}
