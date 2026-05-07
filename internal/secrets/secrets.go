// Package secrets resolves ${secret:foo.bar} and ${env:FOO} references at
// apply-time. The active backend is selected from opensync.toml [secrets]
// `backend` field (env|age).
package secrets

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// Resolver returns the cleartext value for a key like "github.token". An
// unknown key returns an error.
type Resolver interface {
	Resolve(key string) (string, error)
}

// re matches ${secret:dotted.key} and ${env:NAME} references.
var re = regexp.MustCompile(`\$\{(secret|env):([A-Za-z0-9._-]+)\}`)

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

// EnvBackend resolves ${env:NAME} via os.Getenv(NAME). Used both as the env
// resolver in SubstituteRefs and as the "backend = env" mode where secrets
// are also stored as env vars (e.g. by direnv / 1Password CLI).
type EnvBackend struct{}

func (EnvBackend) Resolve(key string) (string, error) {
	v := osGetenv(key)
	if v == "" {
		return "", fmt.Errorf("env var %q not set", key)
	}
	return v, nil
}

// osGetenv indirection so tests can inject without touching real env.
var osGetenv = func(k string) string {
	return os.Getenv(k)
}

// NopResolver is a Resolver that always returns an error (key not found).
// Used when no secrets backend is configured.
type NopResolver struct{}

func (NopResolver) Resolve(key string) (string, error) {
	return "", fmt.Errorf("no secrets backend configured; cannot resolve %q", key)
}

// SelectBackend returns the appropriate Resolver for the given SecretsConfig.
// For "age" backend it returns an AgeBackend; for "env" or empty it returns EnvBackend.
func SelectBackend(cfg source.SecretsConfig, homeDir string) Resolver {
	switch strings.ToLower(cfg.Backend) {
	case "age":
		ageFile := cfg.File
		if ageFile != "" && !isAbs(ageFile) {
			// relative paths are relative to the agentsync home/.agentsync/
			ageFile = homeDir + "/" + ageFile
		}
		return NewAgeBackend(ageFile, cfg.IdentityFile)
	case "env":
		return EnvBackend{}
	default:
		return NopResolver{}
	}
}

// isAbs reports whether p is an absolute path.
func isAbs(p string) bool {
	return len(p) > 0 && p[0] == '/'
}
