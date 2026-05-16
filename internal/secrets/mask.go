package secrets

import (
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// CollectResolved walks every string field of the canonical that
// SubstituteCanonical would expand, resolves each ${secret:…} / ${env:…}
// reference, and returns a map from resolved-value → original placeholder.
//
// Used by callers that need to print text which may contain resolved
// secret values (notably `agentsync diff`, which reads the on-disk file
// — written by a prior apply with the secret already substituted — and
// would otherwise leak the cleartext token to stdout). Unresolvable
// references are silently skipped; this is a redaction helper, not a
// validator.
func CollectResolved(c *source.Canonical, sec, env Resolver) map[string]string {
	out := map[string]string{}
	if c == nil {
		return out
	}
	collectString := func(s string) {
		// Find every ${secret:foo} / ${env:NAME}; resolve each; record
		// the mapping. We do not error on missing keys — this is a
		// best-effort redaction.
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 {
				continue
			}
			placeholder := m[0]
			kind, key := m[1], m[2]
			var r Resolver
			switch kind {
			case "secret":
				r = sec
			case "env":
				r = env
			default:
				continue
			}
			v, err := r.Resolve(key)
			if err != nil || v == "" {
				continue
			}
			out[v] = placeholder
		}
	}
	for _, srv := range c.MCPServers {
		collectString(srv.Server.Command)
		collectString(srv.Server.URL)
		for _, a := range srv.Server.Args {
			collectString(a)
		}
		for _, v := range srv.Server.Env {
			collectString(v)
		}
		for _, v := range srv.Server.Headers {
			collectString(v)
		}
	}
	for _, h := range c.Hooks {
		collectString(h.Command)
	}
	for _, ls := range c.LSPServers {
		collectString(ls.Spec.Command)
		collectString(ls.Spec.URL)
		for _, a := range ls.Spec.Args {
			collectString(a)
		}
		for _, v := range ls.Spec.Env {
			collectString(v)
		}
		for _, v := range ls.Spec.Headers {
			collectString(v)
		}
	}
	if c.Project != nil {
		for k, v := range CollectResolved(c.Project, sec, env) {
			out[k] = v
		}
	}
	return out
}

// MaskResolved replaces every resolved value in b with its original
// placeholder. Idempotent — a placeholder that appears in b stays as a
// placeholder. Designed for redacting cleartext that may contain secrets
// before printing it (diff output, error messages, etc.).
//
// We deliberately do not anchor the replacement to word boundaries: a
// secret token is usually high-entropy and its longest match is what we
// want to redact wherever it appears.
func MaskResolved(s string, resolved map[string]string) string {
	if len(resolved) == 0 {
		return s
	}
	// Replace longest values first so a token that is a substring of
	// another token gets the right placeholder.
	values := make([]string, 0, len(resolved))
	for v := range resolved {
		values = append(values, v)
	}
	sortByLengthDesc(values)
	for _, v := range values {
		s = strings.ReplaceAll(s, v, resolved[v])
	}
	return s
}

// sortByLengthDesc sorts s in place by descending length.
func sortByLengthDesc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && len(s[j-1]) < len(s[j]); j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
