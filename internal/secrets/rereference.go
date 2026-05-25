package secrets

import (
	"strings"

	"github.com/spxrogers/agentsync/internal/source"
)

// ReReferenceCanonical restores ${secret:…} placeholders in c — a canonical
// reconstructed from a destination (or resolved by SubstituteCanonical) — using
// against, the current templated source, as the reference. It is the inverse of
// SubstituteCanonical and the single boundary through which a resolved /
// ingested canonical is converted back toward the templated source form before
// any write-back. apply substitutes ${secret:…} to cleartext into the
// destination; capture reads that destination back, so without this re-reference
// a live credential would be persisted into ~/.agentsync (often a committed
// dotfiles repo).
//
// Matching is FIELD-POSITIONAL FIRST: a field is restored to its templated form
// when (a) the corresponding source field actually referenced a secret and (b)
// the ingested value still resolves to the same thing (the field is unchanged).
// A field the source did NOT template (e.g. command = "npx") is never rewritten
// by this pass, even if its literal happens to equal some secret's value.
//
// A VALUE-BASED FALLBACK then catches structural edits the positional pass can't
// see (a shifted MCP arg index, a renamed env key / server id): it replaces any
// source-referenced ${secret:…} resolved cleartext that remains with its
// placeholder, so the resolved credential is never persisted into the canonical
// source. To keep the positional pass's "don't rewrite a coincidental literal"
// guarantee, a value that ALSO appears as a non-templated source literal is
// treated as ambiguous and left untouched (see sourceSecretValues /
// sourceLiterals). ${env:…} is never inverted by either pass.
//
// Both c and against are walked through the one walkSecretFields enumeration, so
// a new secret-bearing field is re-referenced automatically. Hooks have no
// stable id, so they are matched by event + resolution (see rereferenceHook),
// and the value-based fallback covers a hook command edited out of position too.
func ReReferenceCanonical(c *source.Canonical, against *source.Canonical, sec, env Resolver) {
	if c == nil || against == nil {
		return
	}
	srcByLoc := make(map[secretFieldLoc]string)
	walkSecretFields(against, func(loc secretFieldLoc, s string) string {
		srcByLoc[loc] = s
		return s
	})
	walkSecretFields(c, func(loc secretFieldLoc, ingested string) string {
		if loc.kind == "hook" {
			return rereferenceHook(against, loc.id, ingested, sec, env)
		}
		return restoreField(srcByLoc[loc], ingested, sec, env)
	})

	// Value-based fallback for STRUCTURAL changes the positional pass can't see.
	// The positional restore above only fires when the dest field kept the same
	// location (same arg index, env/header key, server id). A native edit that
	// shifts structure — prepending an MCP arg, renaming an env key or a server
	// id — moves the resolved cleartext to a location with no source counterpart,
	// so srcByLoc misses it and the cleartext would be persisted into the
	// canonical source (a committed dotfiles repo) with no warning. Re-reference
	// by value as a safety net: replace any source-referenced secret's resolved
	// cleartext with its ${secret:…} placeholder.
	//
	// To preserve the deliberate "don't rewrite a coincidental literal" property
	// (see restoreField + TestReReferenceCanonical_FieldPositional), EXCLUDE any
	// value that also appears verbatim as a non-templated source literal: such a
	// value is ambiguous (it could be the user's own literal, not the leaked
	// secret), so leave it exactly as the positional pass would. Only ${secret:…}
	// is inverted (never ${env:…}, matching restoreField); short values are
	// skipped so a low-entropy "secret" can't corrupt unrelated text.
	refs := sourceSecretValues(against, sec)
	for lit := range sourceLiterals(against) {
		delete(refs, lit)
	}
	if len(refs) == 0 {
		return
	}
	walkSecretFields(c, func(_ secretFieldLoc, ingested string) string {
		return MaskResolved(ingested, refs)
	})
}

// minReReferenceLen is the shortest resolved secret value the value-based
// fallback will invert. A 1–3 char "secret" is not a credential worth the risk
// of substring-rewriting unrelated text (a flag, a path segment); the
// field-positional pass still restores it when the dest field is unchanged.
const minReReferenceLen = 4

// sourceSecretValues resolves every ${secret:…} reference in against and returns
// a map from the resolved cleartext to its placeholder. ${env:…} is excluded:
// an env value is not a credential-at-rest concern and is never inverted (see
// restoreField). Values shorter than minReReferenceLen are skipped.
func sourceSecretValues(against *source.Canonical, sec Resolver) map[string]string {
	out := map[string]string{}
	if against == nil || sec == nil {
		return out
	}
	walkSecretFields(against, func(_ secretFieldLoc, s string) string {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if len(m) < 3 || m[1] != "secret" {
				continue
			}
			v, err := sec.Resolve(m[2])
			if err != nil || len(v) < minReReferenceLen {
				continue
			}
			out[v] = m[0]
		}
		return s
	})
	return out
}

// sourceLiterals returns the set of secret-bearing field values in against that
// contain NO ${secret:…}/${env:…} reference — i.e. values the user typed
// verbatim. A resolved secret value that also appears in this set is ambiguous
// (literal vs leaked credential), so the value-based fallback leaves it alone.
func sourceLiterals(against *source.Canonical) map[string]struct{} {
	out := map[string]struct{}{}
	if against == nil {
		return out
	}
	walkSecretFields(against, func(_ secretFieldLoc, s string) string {
		if s != "" && !re.MatchString(s) {
			out[s] = struct{}{}
		}
		return s
	})
	return out
}

// restoreField returns the source field's templated value when it referenced a
// secret and still resolves to the (unchanged) ingested value; otherwise it
// keeps the ingested value verbatim. ${env:…} references are intentionally not
// inverted — an env value is not a credential-at-rest concern the way a
// ${secret:…} backend value is, and inverting it risks rewriting an unrelated
// literal that happens to equal the env var's value.
func restoreField(srcVal, ingested string, sec, env Resolver) string {
	if !strings.Contains(srcVal, "${secret:") {
		return ingested
	}
	if resolved, _, err := SubstituteRefs(srcVal, sec, env); err == nil && resolved == ingested {
		return srcVal
	}
	return ingested
}

// rereferenceHook restores a hook command. Hooks have no stable id, so a
// templated source command for the same event whose resolution equals the
// ingested command is the match.
func rereferenceHook(against *source.Canonical, event, ingested string, sec, env Resolver) string {
	for _, h := range against.Hooks {
		if h.Event != event || !strings.Contains(h.Command, "${secret:") {
			continue
		}
		if resolved, _, err := SubstituteRefs(h.Command, sec, env); err == nil && resolved == ingested {
			return h.Command
		}
	}
	return ingested
}
