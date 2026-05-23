package secrets

import (
	"encoding/json"
	"sort"
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
	// walkSecretFields mutates in place; returning each value unchanged makes
	// every assignment a no-op, so this stays a read-only redaction pass.
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
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
			// Also register the JSON-escaped representation. A secret value
			// containing a quote/backslash/control char (GCP JSON keys,
			// escaped tokens) is stored JSON-escaped in destination files like
			// .claude.json, and diff JSON-marshals it again before masking. A
			// map keyed only on the raw value would never match the escaped
			// on-disk form, leaking the cleartext to stdout. The longest-match
			// ordering in MaskResolved handles raw-vs-escaped overlap.
			if esc := jsonEscapeInner(v); esc != v {
				out[esc] = placeholder
			}
		}
		return s
	})
	return out
}

// UnresolvedSecretRefs returns the sorted, de-duplicated set of ${secret:KEY}
// reference keys in c that sec cannot resolve. Callers that print content
// derived from a destination file written by a prior apply (notably
// `agentsync diff`) use this to fail closed: if a secret reference cannot be
// resolved now, the cleartext value substituted into that on-disk file on the
// last apply cannot be redacted, so the safe action is to refuse rather than
// leak it to stdout / logs / screenshots.
//
// ${env:…} references are intentionally excluded — the env backend is always
// available, and an unresolved env ref is not a credential-leak risk.
//
// Walks the single walkSecretFields field set, shared with SubstituteCanonical,
// CollectResolved, and ReReferenceCanonical.
func UnresolvedSecretRefs(c *source.Canonical, sec Resolver) []string {
	if c == nil {
		return nil
	}
	missing := map[string]bool{}
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 || m[1] != "secret" {
				continue
			}
			key := m[2]
			if _, err := sec.Resolve(key); err != nil {
				missing[key] = true
			}
		}
		return s
	})
	if len(missing) == 0 {
		return nil
	}
	out := make([]string, 0, len(missing))
	for k := range missing {
		out = append(out, k)
	}
	sort.Strings(out)
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

// jsonEscapeInner returns s as it would appear INSIDE a JSON string literal
// (the json.Marshal output with the surrounding quotes stripped). For a value
// with no special characters this equals s. Used so redaction also catches a
// secret stored JSON-escaped in a destination file.
func jsonEscapeInner(s string) string {
	b, err := json.Marshal(s)
	if err != nil || len(b) < 2 {
		return s
	}
	return string(b[1 : len(b)-1])
}

// sortByLengthDesc sorts s in place by descending length.
func sortByLengthDesc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && len(s[j-1]) < len(s[j]); j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
