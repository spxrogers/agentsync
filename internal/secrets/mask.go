package secrets

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
	"github.com/spxrogers/agentsync/internal/untrusted"
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

// UnresolvedSecretRefs returns the sorted, de-duplicated set of ${secret:…} /
// ${env:…} references in c that cannot be resolved now (formatted "secret:KEY"
// / "env:KEY"). Callers that print or write content derived from a destination
// file a prior apply wrote (notably `agentsync diff`) use this to fail closed:
// if a reference cannot be resolved now, the cleartext value substituted into
// that on-disk file on the last apply cannot be redacted, so the safe action is
// to refuse rather than leak it.
//
// ${env:…} refs are included: an env var that was set at apply time (so its
// value landed in the dest in cleartext) can be UNSET at diff/capture time, and
// the redaction map (CollectResolved) skips it precisely then — exactly the
// case that would leak. They are not "always available".
//
// Walks the single walkSecretFields field set, shared with SubstituteCanonical,
// CollectResolved, and ReReferenceCanonical.
func UnresolvedSecretRefs(c *source.Canonical, sec, env Resolver) []string {
	if c == nil {
		return nil
	}
	missing := map[string]bool{}
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 {
				continue
			}
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
			if r == nil {
				missing[kind+":"+key] = true
				continue
			}
			if _, err := r.Resolve(key); err != nil {
				missing[kind+":"+key] = true
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

// MalformedSecretRefs walks every secret-bearing canonical field and returns each
// ${secret:…}/${env:…}-SHAPED token that does NOT match the strict reference shape
// (a non-empty [A-Za-z0-9._-]+ key after a colon) — e.g. ${secret:} or
// ${secret foo}. It needs no backend, so it is the offline-validatable shape check
// `verify` runs when reference resolution is skipped (issue #171).
//
// The returned tokens are the raw, loosely-matched candidates — they are
// config-derived and therefore attacker-influenceable (agentsync configs are
// shareable dotfiles), and looseRefRe's `[^}]*` tail can capture ESC / C1 /
// newline bytes that the strict shape rejects. They are returned as
// untrusted.Text so any display site (the `verify` offline error) sanitizes them
// on print BY DEFAULT — a raw print cannot reintroduce the #93/PR100
// terminal-escape-injection class. Match/dedup/sort run on the raw bytes; only
// the returned rendering is display-guarded.
func MalformedSecretRefs(c *source.Canonical) []untrusted.Text {
	if c == nil {
		return nil
	}
	bad := map[string]bool{}
	walkSecretFields(c, func(_ secretFieldLoc, s string) string {
		for _, cand := range looseRefRe.FindAllString(s, -1) {
			// Match the WHOLE candidate against the strict shape, not a substring:
			// re.MatchString is a substring search, so a malformed outer ref that
			// merely embeds a well-formed nested ref (e.g. "${secret:${env:FOO}}",
			// whose loose candidate "${secret:${env:FOO}" contains a valid
			// "${env:FOO}") would be wrongly accepted. Require the strict match to
			// span the entire candidate so nested/partial refs are flagged.
			if loc := re.FindStringIndex(cand); loc == nil || loc[0] != 0 || loc[1] != len(cand) {
				bad[cand] = true
			}
		}
		return s
	})
	if len(bad) == 0 {
		return nil
	}
	out := make([]string, 0, len(bad))
	for k := range bad {
		out = append(out, k)
	}
	sort.Strings(out)
	refs := make([]untrusted.Text, len(out))
	for i, s := range out {
		refs[i] = untrusted.Wrap(s)
	}
	return refs
}
