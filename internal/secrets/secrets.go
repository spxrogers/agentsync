// Package secrets resolves ${secret:foo.bar} and ${env:FOO} references at
// apply-time. The active backend is selected from agentsync.toml [secrets]
// `backend` field (env|age).
package secrets

import (
	"fmt"
	"os"
	"regexp"

	"github.com/spxrogers/agentsync/internal/source"
)

// Resolver returns the cleartext value for a key like "github.token". An
// unknown key returns an error.
type Resolver interface {
	Resolve(key string) (string, error)
}

// re matches ${secret:dotted.key} and ${env:NAME} references.
var re = regexp.MustCompile(`\$\{(secret|env):([A-Za-z0-9._-]+)\}`)

// looseRefRe matches anything SHAPED like a ${secret …}/${env …} reference (a
// colon or whitespace after the kind), so a MALFORMED ref — ${secret:} (empty
// key), ${secret foo} (no colon), a key with a space/illegal char — is caught for
// the offline shape check even though the strict `re` above silently skips it. It
// deliberately does not match `${env}` (no separator) or `${secretary}` (no
// boundary), which are not clearly references. (issue #171)
var looseRefRe = regexp.MustCompile(`\$\{\s*(secret|env)[:\s][^}]*\}`)

// SubstituteRefs walks s and replaces ${secret:dotted.key} and ${env:NAME}
// references. Unknown references are left as-is and reported in the
// returned []string of unresolved markers (caller decides whether to error).
func SubstituteRefs(s string, secrets Resolver, env Resolver) (string, []string, error) {
	var unresolved []string
	out := re.ReplaceAllStringFunc(s, func(m string) string {
		sub := re.FindStringSubmatch(m)
		if len(sub) < 3 {
			return m
		}
		kind, key := sub[1], sub[2]
		var r Resolver
		switch kind {
		case "secret":
			r = secrets
		case "env":
			r = env
		default:
			unresolved = append(unresolved, m)
			return m
		}
		v, err := r.Resolve(key)
		if err != nil {
			unresolved = append(unresolved, m)
			return m
		}
		return v
	})
	return out, unresolved, nil
}

// EnvBackend resolves ${env:NAME} via os.LookupEnv(NAME) — presence, not
// emptiness, is the criterion, so a var set to "" resolves to "" (matching
// AgeBackend) and only an unset var errors. Used both as the env resolver in
// SubstituteRefs and as the "backend = env" mode where secrets are also stored
// as env vars (e.g. by direnv / 1Password CLI).
type EnvBackend struct{}

func (EnvBackend) Resolve(key string) (string, error) {
	// Presence, not emptiness, is the criterion — a var set to the empty string
	// resolves to "" (matching AgeBackend's cache-presence model); only an
	// unset var is an error. (issue #163)
	v, ok := osLookupEnv(key)
	if !ok {
		return "", fmt.Errorf("env var %q not set", key)
	}
	return v, nil
}

// osLookupEnv indirection so tests can inject without touching real env.
var osLookupEnv = os.LookupEnv

// NopResolver is a Resolver that always returns an error (key not found).
// Used when no secrets backend is configured.
type NopResolver struct{}

func (NopResolver) Resolve(key string) (string, error) {
	return "", fmt.Errorf("no secrets backend configured; cannot resolve %q", key)
}

// SelectBackend returns the Resolver apply renders through for the given
// [secrets] config. "age" (in any casing) selects an AgeBackend; "env" selects
// the EnvBackend; ANY OTHER VALUE — including an EMPTY backend — selects
// NopResolver, which errors on every ${secret:…} lookup.
//
// (The previous version of this comment claimed an empty backend returned
// EnvBackend. It never did — an empty string falls to the default arm.)
//
// agentsyncHome anchors relative cfg.File / identity_file paths; userHome
// (paths.HomeDir) expands ${env:HOME} and a leading ~. Both the age file
// (defaulted via ResolveAgeFile) and the identity file (expanded via
// ResolveIdentityFile) are resolved here so apply/check/doctor/diff agree with
// the `secret` subcommands on which files to read.
//
// The backend name is folded through NormalizeBackend — the same helper
// ValidateConfig uses — so no validator built on it can reject a spelling apply
// accepts.
func SelectBackend(cfg source.SecretsConfig, agentsyncHome, userHome string) Resolver {
	switch NormalizeBackend(cfg.Backend) {
	case BackendAge:
		return NewAgeBackend(
			ResolveAgeFile(cfg, agentsyncHome, userHome),
			ResolveIdentityFile(cfg, agentsyncHome, userHome),
		)
	case BackendEnv:
		return EnvBackend{}
	default:
		return NopResolver{}
	}
}
